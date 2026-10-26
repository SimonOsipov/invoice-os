package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"go/ast"
	"go/types"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// Two gateway instances on two pools to one database, built from main's own builders.
// CI runs these in the rls job, where DATABASE_GATEWAY_URL is set.

const (
	instIssuer    = "https://gotrue.instances.test/auth/v1"
	instToken     = "gw-token"
	instGoneBody  = `{"code":403,"error_code":"session_not_found","msg":"gone"}`
	instUnauth    = `{"error":"unauthorized"}`
	instPassword  = "Corr3ct-Horse"
	instTenantID  = "22222222-2222-2222-2222-222222222222"
	instSubscribe = `{"email":%q,"category_ids":["news"],"frequency":"weekly"}`
)

// gotrueFake answers the GoTrue paths the wired handlers call and counts each.
type gotrueFake struct {
	*httptest.Server
	subject string
	email   string

	mu         sync.Mutex
	userStatus int
	userBody   string

	userGets, passwordGrants, resends atomic.Int32
}

func newGotrueFake(t *testing.T, subject, email string) *gotrueFake {
	t.Helper()
	g := &gotrueFake{subject: subject, email: email, userStatus: http.StatusOK, userBody: `{"id":"` + subject + `"}`}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /user":
			g.userGets.Add(1)
			g.mu.Lock()
			status, body := g.userStatus, g.userBody
			g.mu.Unlock()
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		case "PUT /user":
			_, _ = io.WriteString(w, `{}`)
		case "POST /token":
			if r.URL.Query().Get("grant_type") == "password" {
				g.passwordGrants.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error_code":"invalid_credentials","msg":"Invalid login credentials"}`)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"`+handoffAccessToken(g.subject)+`"}`)
		case "POST /verify":
			_, _ = io.WriteString(w, `{"access_token":"`+handoffAccessToken(g.subject)+`","user":{"id":"`+g.subject+`","email":"`+g.email+`"}}`)
		case "POST /logout":
			w.WriteHeader(http.StatusNoContent)
		case "POST /resend":
			g.resends.Add(1)
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("fake GoTrue got unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

// goneEverywhere makes /user refuse every token, as GoTrue does after a global sign-out.
func (g *gotrueFake) goneEverywhere() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.userStatus, g.userBody = http.StatusForbidden, instGoneBody
}

type instance struct {
	api, reset, resend, subscribe http.Handler
	h                             handoff
}

// twoInstances is instances A and B, one fake of each upstream, and the superuser pool that cleans up.
type twoInstances struct {
	a, b       instance
	gotrue     *gotrueFake
	tenancyHit *atomic.Int32
	blogHits   *atomic.Int32
	subject    string
	address    string
	key        *ecdsa.PrivateKey
}

func newTwoInstances(t *testing.T) *twoInstances {
	t.Helper()
	gw, super := os.Getenv("DATABASE_GATEWAY_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if gw == "" || super == "" {
		t.Skip("set DATABASE_GATEWAY_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	t.Setenv("DEPLOY_ENV", "") // limits enforce only outside a preview
	ctx := context.Background()
	open := func(dsn string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		t.Cleanup(p.Close)
		return p
	}
	poolA, poolB, superPool := open(gw), open(gw), open(super)
	for _, tbl := range []string{"gateway_session_revocations", "gateway_throttle_counts"} {
		var reg *string
		if err := superPool.QueryRow(ctx, `SELECT to_regclass('public.' || $1)::text`, tbl).Scan(&reg); err != nil || reg == nil {
			t.Fatalf("table %s is missing (%v): the gateway_shared_limits migration has not run", tbl, err)
		}
	}

	r := &twoInstances{subject: uuid.NewString(), address: "flake04-" + uuid.NewString() + "@example.test"}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r.key = key
	sum := sha256.Sum256([]byte(r.address))
	t.Cleanup(func() { // D21: the test's own rows only, as the superuser
		_, _ = superPool.Exec(ctx, `DELETE FROM gateway_session_revocations WHERE subject = $1`, r.subject)
		_, _ = superPool.Exec(ctx, `DELETE FROM gateway_throttle_counts WHERE key_hash = $1`, sum[:])
	})

	r.gotrue = newGotrueFake(t, r.subject, r.address)
	authURL, _ := url.Parse(r.gotrue.URL)
	r.tenancyHit, r.blogHits = new(atomic.Int32), new(atomic.Int32)
	tenancy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.tenancyHit.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(tenancy.Close)
	tenancyURL, _ := url.Parse(tenancy.URL)
	notifications := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		if req.URL.Path == "/internal/blog/subscriptions" {
			r.blogHits.Add(1)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(notifications.Close)
	notificationsURL, _ := url.Parse(notifications.URL)

	b64 := func(n *big.Int) string { return base64.RawURLEncoding.EncodeToString(n.FillBytes(make([]byte, 32))) }
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"keys":[{"kty":"EC","crv":"P-256","alg":"ES256","use":"sig","kid":"k1","x":%q,"y":%q}]}`, b64(key.X), b64(key.Y))
	}))
	t.Cleanup(jwks.Close)

	site, _ := url.Parse("https://site.example")
	build := func(pool *pgxpool.Pool) instance {
		verifier, err := auth.NewVerifier(auth.Config{Issuer: instIssuer, JWKSURL: jwks.URL})
		if err != nil {
			t.Fatal(err)
		}
		log := slog.New(slog.DiscardHandler)
		sessions := gateway.NewSessionChecker(authURL, http.DefaultClient, time.Now, log)
		if checkerSharedInMain(t) {
			sessions.Shared(pool)
		}
		api, _ := gatewayHandlers(verifier, sessions, map[string]*url.URL{"tenancy": tenancyURL}, map[string]*url.URL{"auth": authURL},
			map[string]string{"auth": ".well-known/jwks.json"}, log, instToken)
		h := handoffHandlers(authURL, site, sessions, log, nil, verifier, pool)
		reg := registrationHandlers(authURL, site, 0, log, nil, noPendingInvite, pool)
		return instance{
			api:       api,
			h:         h,
			reset:     resetPasswordHandler(authURL, site, sessions, h.SignInThrottle, log),
			resend:    reg.ResendVerification,
			subscribe: blogSubscribeHandlers(notificationsURL, instToken, 0, log, pool).Subscribe,
		}
	}
	r.a, r.b = build(poolA), build(poolB)
	return r
}

// checkerSharedInMain reports whether main calls `sessions.Shared(gwPool)` right after building the checker.
// The rig does what main does, so a main that forgets the call fails the sign-out tests at runtime.
func checkerSharedInMain(t *testing.T) bool {
	t.Helper()
	_, body := parseMain(t)
	for i, st := range body.List[:len(body.List)-1] {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			continue
		}
		if _, ok := isCallTo(as.Rhs[0], "gateway", "NewSessionChecker"); ok {
			es, ok := body.List[i+1].(*ast.ExprStmt)
			return ok && types.ExprString(es.X) == types.ExprString(as.Lhs[0])+".Shared(gwPool)"
		}
	}
	t.Fatal("main has no top-level `x := gateway.NewSessionChecker(...)`")
	return false
}

// token signs a GoTrue-shaped access token for the subject on one session.
func (r *twoInstances) token(t *testing.T) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": instIssuer, "sub": r.subject, "aud": "authenticated", "role": "authenticated",
		"session_id": uuid.NewString(), "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
		"app_metadata": map[string]any{"tenant_id": instTenantID},
	})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(r.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (r *twoInstances) getMe(i instance, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/tenancy/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	i.api.ServeHTTP(rec, req)
	return rec
}

func serveJSON(h http.Handler, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertLive(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d, want 200: %s", what, rec.Code, rec.Body.String())
	}
}

func assertUnauthorized(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusUnauthorized || got != instUnauth {
		t.Errorf("%s = %d %s, want 401 %s", what, rec.Code, got, instUnauth)
	}
}

// A sign-out on A refuses every device's token on B, not only the one that signed out.
func TestTwoInstances_SignOutOnOneRefusesTheTokenOnTheOther(t *testing.T) {
	r := newTwoInstances(t)
	d1, d2 := r.token(t), r.token(t)
	assertLive(t, "device 1 on B before sign-out", r.getMe(r.b, d1))
	assertLive(t, "device 2 on B before sign-out", r.getMe(r.b, d2))

	rec := serveJSON(r.a.h.SignOut, "/auth/sign-out", `{"refresh_token":"rt"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("sign-out on A = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	r.gotrue.goneEverywhere()

	assertUnauthorized(t, "device 1 on B after sign-out", r.getMe(r.b, d1))
	assertUnauthorized(t, "device 2 on B after sign-out", r.getMe(r.b, d2))
}

// A reset on A signs the subject out everywhere, so B refuses the token it cached.
func TestTwoInstances_APasswordResetOnOneRefusesTheTokenOnTheOther(t *testing.T) {
	r := newTwoInstances(t)
	tok := r.token(t)
	assertLive(t, "token on B before the reset", r.getMe(r.b, tok))

	rec := serveForm(r.a.reset, "/auth/reset-password", url.Values{"token": {"link"}, "type": {"recovery"}, "password": {instPassword}}.Encode())
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reset on A = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	r.gotrue.goneEverywhere()

	assertUnauthorized(t, "token on B after the reset", r.getMe(r.b, tok))
}

// SignInMaxFailures wrong passwords for one address, split over both instances, lock it on both.
func TestTwoInstances_TheEleventhWrongPasswordIsRefusedOnEither(t *testing.T) {
	r := newTwoInstances(t)
	body := fmt.Sprintf(`{"email":%q,"password":"wrong","state":%q}`, r.address, handoffState)
	for i := range gateway.SignInMaxFailures {
		inst := r.a
		if i%2 == 1 {
			inst = r.b
		}
		if rec := serveJSON(inst.h.SignIn, "/auth/sign-in", body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d = %d, want 401: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	for name, inst := range map[string]instance{"A": r.a, "B": r.b} {
		rec := serveJSON(inst.h.SignIn, "/auth/sign-in", body)
		if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusTooManyRequests || got != `{"error":"too many requests"}` {
			t.Errorf("wrong password %d on %s = %d %s, want 429 too many requests", gateway.SignInMaxFailures+1, name, rec.Code, got)
		}
	}
	if n := r.gotrue.passwordGrants.Load(); n != gateway.SignInMaxFailures {
		t.Errorf("GoTrue password grants = %d, want %d: a refused sign-in must not reach GoTrue", n, gateway.SignInMaxFailures)
	}
}

// ResendPerAddress and BlogSubscribePerEmail each count one address across both instances.
func TestTwoInstances_ResendAndBlogLimitsCountAcrossInstances(t *testing.T) {
	r := newTwoInstances(t)
	resend := fmt.Sprintf(`{"email":%q}`, r.address)
	for i := range gateway.ResendPerAddress + 1 {
		inst := r.a
		if i%2 == 1 {
			inst = r.b
		}
		if rec := serveJSON(inst.resend, "/auth/resend-verification", resend); rec.Code != http.StatusAccepted {
			t.Fatalf("resend %d = %d, want 202: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if n := r.gotrue.resends.Load(); n != gateway.ResendPerAddress {
		t.Errorf("GoTrue /resend calls = %d, want %d", n, gateway.ResendPerAddress)
	}

	subscribe := fmt.Sprintf(instSubscribe, r.address)
	for i := range gateway.BlogSubscribePerEmail + 1 {
		inst := r.a
		if i%2 == 1 {
			inst = r.b
		}
		if rec := serveJSON(inst.subscribe, "/blog/subscriptions", subscribe); rec.Code != http.StatusAccepted {
			t.Fatalf("subscribe %d = %d, want 202: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if n := r.blogHits.Load(); n != gateway.BlogSubscribePerEmail {
		t.Errorf("notifications /internal/blog/subscriptions calls = %d, want %d", n, gateway.BlogSubscribePerEmail)
	}
}

// Holds today: with no sign-out the cache answers, so a shared checker adds no GoTrue call.
func TestTwoInstances_ALiveSessionIsStillCached(t *testing.T) {
	r := newTwoInstances(t)
	tok := r.token(t)
	for i := range 5 {
		assertLive(t, fmt.Sprintf("request %d on B", i+1), r.getMe(r.b, tok))
	}
	if n := r.gotrue.userGets.Load(); n != 1 {
		t.Errorf("GoTrue /user calls = %d, want 1", n)
	}
	if n := r.tenancyHit.Load(); n != 5 {
		t.Errorf("tenancy calls = %d, want 5", n)
	}
}
