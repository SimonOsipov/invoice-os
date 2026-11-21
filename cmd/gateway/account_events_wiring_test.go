package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth/iaptest"
)

// With a gateway pool, each builder writes its account events to account_event_handoffs.
func TestGatewayMain_AccountEventsUseTheOutboxWithAPool(t *testing.T) {
	gw, super := os.Getenv("DATABASE_GATEWAY_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if gw == "" || super == "" {
		t.Skip("set DATABASE_GATEWAY_URL and DATABASE_SUPERUSER_URL (or run `make test-db PKGS=./cmd/gateway/...`)")
	}
	t.Setenv("DEPLOY_ENV", "")
	ctx := context.Background()
	open := func(dsn string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		t.Cleanup(p.Close)
		return p
	}
	pool, superPool := open(gw), open(super)
	subject := uuid.NewString()
	email := "ae-wiring-" + uuid.NewString() + "@example.test"
	t.Cleanup(func() {
		_, _ = superPool.Exec(context.Background(), `DELETE FROM account_event_handoffs WHERE email = $1 OR user_id = $2`, email, subject)
	})
	g := newGotrueFake(t, subject, email)
	authURL, _ := url.Parse(g.URL)
	site, _ := url.Parse("https://site.example")
	log := slog.New(slog.DiscardHandler)
	sessions := gateway.NewSessionChecker(authURL, http.DefaultClient, time.Now, log)
	h := handoffHandlers(authURL, site, sessions, log, nil, nil, nil, nil, pool)
	reg := registrationHandlers(authURL, site, 0, log, nil, noPendingInvite, pool)

	rows := func(event string) int {
		var n int
		if err := superPool.QueryRow(ctx, `SELECT count(*) FROM account_event_handoffs WHERE event = $1 AND (email = $2 OR user_id = $3)`,
			event, email, subject).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	post := func(handler http.Handler, path, body string) int {
		return serveJSON(handler, path, body).Code
	}

	if c := post(h.SignIn, "/auth/sign-in", fmt.Sprintf(`{"email":%q,"password":"wrong","state":%q}`, email, handoffState)); c != http.StatusUnauthorized {
		t.Fatalf("sign-in = %d, want 401", c)
	}
	post(reg.RequestPasswordReset, "/auth/request-password-reset", fmt.Sprintf(`{"email":%q}`, email))
	post(h.SignOut, "/auth/sign-out", `{"refresh_token":"r"}`)
	reset := resetPasswordHandler(authURL, site, sessions, h.SignInThrottle, log, h.AccountEvents, gateway.NewSignInThrottle("link-redeem-ip", gateway.LinkRedeemPerIP, gateway.LinkRedeemMaxKeys, gateway.LinkRedeemIPWindow, time.Now))
	if rec := serveForm(reset, "/auth/reset-password", "token=t&type=recovery&password=new-password-1"); rec.Code != http.StatusSeeOther || !strings.HasSuffix(rec.Header().Get("Location"), "/?reset=1") {
		t.Fatalf("reset = %d %q, want 303 to ?reset=1", rec.Code, rec.Header().Get("Location"))
	}

	g.grantOK.Store(true)
	state := "state=" + handoffState
	// A confirm mints no session: set-password records no signed_in row.
	if rec := serveForm(h.InvitationPassword, "/auth/invitation/password", "token=t&type=signup&password=new-password-1&"+state); rec.Code != http.StatusSeeOther || !strings.HasSuffix(rec.Header().Get("Location"), "/?verified=1") {
		t.Fatalf("set-password = %d %q, want 303 to ?verified=1", rec.Code, rec.Header().Get("Location"))
	}
	if n := rows(gateway.EventSignedIn); n != 0 {
		t.Errorf("signed_in rows after set-password = %d, want 0", n)
	}
	if c := post(h.SignIn, "/auth/sign-in", fmt.Sprintf(`{"email":%q,"password":"new-password-1","state":%q}`, email, handoffState)); c != http.StatusOK {
		t.Fatalf("sign-in = %d, want 200", c)
	}
	if n := rows(gateway.EventSignedIn); n != 1 {
		t.Errorf("signed_in rows after the sign-in = %d, want 1", n)
	}

	for _, event := range []string{gateway.EventSignInFailed, gateway.EventPasswordResetRequested, gateway.EventSignedOut, gateway.EventPasswordResetCompleted} {
		if n := rows(event); n != 1 {
			t.Errorf("%s rows = %d, want 1", event, n)
		}
	}

	// A staff mint through the IAP route adds one signed_in row for the account /verify names.
	g.verifyStaff.Store(true)
	signer, jwks := iaptest.NewSigner(t)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"token_hash":"wiring-hash"}`)
	}))
	t.Cleanup(worker.Close)
	workerURL, _ := url.Parse(worker.URL)
	app, err := platform.New("gateway")
	if err != nil {
		t.Fatal(err)
	}
	env := envOf(map[string]string{"IAP_AUDIENCES": iapAudienceOps})
	if err := mountIAPSession(app, env, auth.IAPConfig{JWKSURL: jwks}, workerURL, authURL, "gw-token", gateway.NewHandoffStore(gateway.HandoffTTL, time.Now), log, pool); err != nil {
		t.Fatalf("mountIAPSession: %v", err)
	}
	now := time.Now()
	assertion := signer.Sign(map[string]any{"iss": auth.IAPIssuer, "aud": iapAudienceOps, "sub": "accounts.google.com:1", "email": email,
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(10 * time.Minute).Unix()})
	before := rows(gateway.EventSignedIn)
	body, _ := json.Marshal(map[string]string{"state": handoffState})
	req := httptest.NewRequest(http.MethodPost, "/auth/iap-session", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Iap-Jwt-Assertion", assertion)
	rec := httptest.NewRecorder()
	app.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("iap-session = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if n := rows(gateway.EventSignedIn); n != before+1 {
		t.Errorf("signed_in rows after the IAP mint = %d, want %d", n, before+1)
	}
}
