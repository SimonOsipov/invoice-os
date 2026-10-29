package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
)

// With a gateway pool, each builder writes its account events to account_event_handoffs.
func TestGatewayMain_AccountEventsUseTheOutboxWithAPool(t *testing.T) {
	gw, super := os.Getenv("DATABASE_GATEWAY_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if gw == "" || super == "" {
		t.Skip("set DATABASE_GATEWAY_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
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
	h := handoffHandlers(authURL, site, sessions, log, nil, nil, nil, pool)
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
	reset := resetPasswordHandler(authURL, site, sessions, h.SignInThrottle, log, h.AccountEvents)
	if rec := serveForm(reset, "/auth/reset-password", "token=t&type=recovery&password=new-password-1"); rec.Code != http.StatusSeeOther || !strings.HasSuffix(rec.Header().Get("Location"), "/?reset=1") {
		t.Fatalf("reset = %d %q, want 303 to ?reset=1", rec.Code, rec.Header().Get("Location"))
	}

	g.grantOK.Store(true)
	state := "state=" + handoffState
	if rec := serveForm(h.Verify, "/auth/verify", "token=t&type=signup&"+state); !strings.Contains(rec.Header().Get("Location"), "&handoff=") {
		t.Fatalf("verify = %d %q, want a hand-off", rec.Code, rec.Header().Get("Location"))
	}
	if n := rows(gateway.EventSignedIn); n != 1 {
		t.Errorf("signed_in rows after the verify hand-off = %d, want 1", n)
	}
	if rec := serveForm(h.InvitationPassword, "/auth/invitation/password", "token=t&type=signup&password=new-password-1&"+state); !strings.Contains(rec.Header().Get("Location"), "&handoff=") {
		t.Fatalf("invitation password = %d %q, want a hand-off", rec.Code, rec.Header().Get("Location"))
	}
	if n := rows(gateway.EventSignedIn); n != 2 {
		t.Errorf("signed_in rows after the invitation hand-off = %d, want 2", n)
	}

	for _, event := range []string{gateway.EventSignInFailed, gateway.EventPasswordResetRequested, gateway.EventSignedOut, gateway.EventPasswordResetCompleted} {
		if n := rows(event); n != 1 {
			t.Errorf("%s rows = %d, want 1", event, n)
		}
	}
}
