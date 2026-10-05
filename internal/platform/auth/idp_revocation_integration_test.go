package auth_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// manualClock drives the session cache only; tokens still expire on real time.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

var revocationT0 = time.Unix(1_800_000_000, 0)

// revocation is the /api/ edge and /auth/sign-out sharing one SessionChecker, as the gateway mounts them.
type revocation struct {
	renewal
	clk     *manualClock
	edge    http.Handler
	signOut http.Handler
	hits    *atomic.Int64 // requests that reached the upstream
}

func newRevocation(t *testing.T, base string) revocation {
	t.Helper()
	authURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clk := &manualClock{t: revocationT0}
	sessions := gateway.NewSessionChecker(authURL, idpHTTP, clk.now, log)
	hits := &atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	upURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	return revocation{
		renewal: newRenewal(t, base),
		clk:     clk,
		edge: gateway.Handler(gateway.Options{
			Verifier:  idpVerifier(t, base),
			Sessions:  sessions,
			Upstreams: map[string]*url.URL{"svc": upURL},
			Logger:    log,

			GatewayToken: "gw-test-token",
		}),
		signOut: gateway.SignOutHandler(authURL, idpHTTP, sessions, log),
		hits:    hits,
	}
}

func (r revocation) call(t *testing.T, accessToken string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/svc/ping", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	r.edge.ServeHTTP(rec, req)
	return rec.Code
}

func (r revocation) expect(t *testing.T, label, accessToken string, wantStatus int, wantHits int64) {
	t.Helper()
	if status := r.call(t, accessToken); status != wantStatus {
		t.Errorf("edge %s: status %d, want %d", label, status, wantStatus)
	}
	if got := r.hits.Load(); got != wantHits {
		t.Errorf("edge %s: upstream hits %d, want %d", label, got, wantHits)
	}
}

func (r revocation) signOutWith(t *testing.T, refreshToken string) {
	t.Helper()
	status, body := serveJSON(t, r.signOut, "/auth/sign-out", map[string]string{"refresh_token": refreshToken})
	if status != http.StatusNoContent {
		t.Fatalf("sign-out: status %d (%d body bytes), want 204", status, len(body))
	}
}

// workspaceUser is a user whose tokens carry a tenant, so the edge routes them.
func workspaceUser(t *testing.T, base string) idpUser {
	t.Helper()
	conn := superConn(t)
	u := signUp(t, conn, base)
	seedActiveMembership(t, conn, seedTenant(t, conn), u.id)
	return u
}

func sessionID(t *testing.T, tok string) string {
	t.Helper()
	sid, _ := jwtPart(t, tok, 1)["session_id"].(string)
	return sid
}

func TestIdP_AccessTokenCarriesSessionID(t *testing.T) {
	base := idpURL(t)
	a, _ := newRenewal(t, base).session(t, signUp(t, superConn(t), base))

	sid := sessionID(t, a)
	if _, err := uuid.Parse(sid); err != nil {
		t.Fatalf("session_id claim (%d chars) is not a UUID: %v", len(sid), err)
	}
	id, err := idpVerifier(t, base).Verify(context.Background(), a)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.SessionID != sid {
		t.Errorf("Identity.SessionID differs from the session_id claim")
	}
}

func TestIdP_SignOutEndsEverySession(t *testing.T) {
	base := idpURL(t)
	u := workspaceUser(t, base)
	r := newRevocation(t, base)
	a1, r1 := r.session(t, u)
	a2, r2 := r.session(t, u)
	if sessionID(t, a1) == sessionID(t, a2) {
		t.Fatal("control: both sign-ins share one session_id; want two sessions")
	}

	r.signOutWith(t, r1)

	r.expect(t, "A1 after sign-out", a1, http.StatusUnauthorized, 0)
	r.expect(t, "A2 after sign-out", a2, http.StatusUnauthorized, 0)
	for _, c := range []struct{ label, token string }{{"R1", r1}, {"R2", r2}} {
		if status, m := r.renew(t, c.token); status != http.StatusUnauthorized {
			t.Errorf("refresh %s after sign-out: status %d, keys %v; want 401", c.label, status, keys(m))
		}
	}
	// A second tab signing out after the first: GoTrue's refusal is a 401, not a 502.
	if status, body := serveJSON(t, r.signOut, "/auth/sign-out", map[string]string{"refresh_token": r2}); status != http.StatusUnauthorized {
		t.Errorf("second sign-out with R2: status %d (%d body bytes); want 401", status, len(body))
	}

	// Control: another account's live token still reaches the upstream.
	other, _ := r.session(t, workspaceUser(t, base))
	r.expect(t, "another account after sign-out", other, http.StatusOK, 1)
}

// The app may send a refresh token another tab already rotated (D8); GoTrue's parent rule must still end every session.
func TestIdP_SignOutWithTheParentRefreshToken(t *testing.T) {
	base := idpURL(t)
	r := newRevocation(t, base)
	_, r0 := r.session(t, workspaceUser(t, base))
	a1, r1 := r.renewOK(t, "R0", r0)

	r.expect(t, "A1 live", a1, http.StatusOK, 1)
	r.signOutWith(t, r0)
	r.expect(t, "A1 after sign-out with its parent R0", a1, http.StatusUnauthorized, 1)
	if status, m := r.renew(t, r1); status != http.StatusUnauthorized {
		t.Errorf("refresh R1 after sign-out: status %d, keys %v; want 401", status, keys(m))
	}
}

// The clock never moves, so only the sign-out's eviction can drop the cached "live".
func TestIdP_RevokedTokenNeverReachesAService(t *testing.T) {
	base := idpURL(t)
	r := newRevocation(t, base)
	a1, r1 := r.session(t, workspaceUser(t, base))

	r.expect(t, "A1 live", a1, http.StatusOK, 1)
	r.signOutWith(t, r1)
	r.expect(t, "A1 after sign-out", a1, http.StatusUnauthorized, 1)
}

func TestIdP_StaffCutOffEndsEverySession(t *testing.T) {
	base := idpURL(t)
	u := workspaceUser(t, base)
	r := newRevocation(t, base)
	a1, r1 := r.session(t, u)
	a2, r2 := r.session(t, u) // never cached, so the cut-off reaches it at once

	r.expect(t, "A1 at t0", a1, http.StatusOK, 1)

	// The runbook's steps, verbatim.
	staff := superConn(t)
	exec(t, staff, `SET ROLE supabase_auth_admin`)
	tag, err := staff.Exec(context.Background(), `DELETE FROM auth.sessions WHERE user_id = $1`, u.id)
	if err != nil {
		t.Fatalf("runbook DELETE as supabase_auth_admin: %v", err)
	}
	// signUp's autoconfirmed sign-up holds a session too, so the count is not 1.
	if n := tag.RowsAffected(); n == 0 {
		t.Fatal("runbook DELETE removed no session")
	}

	r.expect(t, "uncached A2 at t0", a2, http.StatusUnauthorized, 1)
	r.clk.set(revocationT0.Add(gateway.SessionCheckTTL - time.Second))
	r.expect(t, "A1 at t0+TTL-1s (stale window)", a1, http.StatusOK, 2)
	r.clk.set(revocationT0.Add(gateway.SessionCheckTTL))
	r.expect(t, "A1 at t0+TTL", a1, http.StatusUnauthorized, 2)
	for _, c := range []struct{ label, token string }{{"R1", r1}, {"R2", r2}} {
		if status, m := r.renew(t, c.token); status != http.StatusUnauthorized {
			t.Errorf("refresh %s after cut-off: status %d, keys %v; want 401", c.label, status, keys(m))
		}
	}

	a3, _ := r.session(t, u)
	r.expect(t, "new sign-in after cut-off", a3, http.StatusOK, 3)
}

// countingTransport counts the calls that reach GoTrue.
type countingTransport struct {
	n atomic.Int64
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return http.DefaultTransport.RoundTrip(req)
}

// Logs the cost of the check; asserts verdicts and GoTrue call counts, never latency, which would flake.
func TestIdP_SessionCheckCost(t *testing.T) {
	const misses, hits = 200, 10_000
	base := idpURL(t)
	authURL, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	a := accessToken(t, base, signUp(t, superConn(t), base))
	id, err := idpVerifier(t, base).Verify(context.Background(), a)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.SessionID == "" {
		t.Fatal("control: token has no session_id; the checker would skip it")
	}

	clk := &manualClock{t: revocationT0}
	rt := &countingTransport{}
	client := &http.Client{Timeout: idpHTTP.Timeout, Transport: rt}
	var live atomic.Int64
	check := gateway.NewSessionChecker(authURL, client, clk.now, slog.New(slog.NewTextHandler(io.Discard, nil))).
		Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			live.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}))
	ctx := auth.WithIdentity(context.Background(), id)
	call := func(label string, i int) time.Duration {
		req := httptest.NewRequest(http.MethodGet, "/api/svc/ping", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+a)
		rec := httptest.NewRecorder()
		start := time.Now()
		check.ServeHTTP(rec, req)
		d := time.Since(start)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s %d: status %d, want the live verdict", label, i, rec.Code)
		}
		return d
	}

	missDur := make([]time.Duration, misses)
	for i := range misses {
		clk.set(revocationT0.Add(time.Duration(i+1) * gateway.SessionCheckTTL))
		missDur[i] = call("miss", i)
	}
	if n := rt.n.Load(); n != misses {
		t.Fatalf("misses reached GoTrue %d times, want %d", n, misses)
	}
	hitDur := make([]time.Duration, hits)
	for i := range hits {
		hitDur[i] = call("hit", i)
	}
	if n := rt.n.Load(); n != misses {
		t.Errorf("hits reached GoTrue %d times, want 0", n-misses)
	}
	if n := live.Load(); n != misses+hits {
		t.Errorf("live verdicts %d, want %d", n, misses+hits)
	}

	summary := "### Session check cost\n\n| path | n | p50 | p95 | max |\n|---|---|---|---|---|\n"
	for _, s := range []struct {
		label string
		d     []time.Duration
	}{{"misses", missDur}, {"hits", hitDur}} {
		slices.Sort(s.d)
		n := len(s.d)
		t.Logf("session check %s (n=%d): p50=%v p95=%v max=%v", s.label, n, s.d[n/2], s.d[n*95/100], s.d[n-1])
		summary += fmt.Sprintf("| %s | %d | %v | %v | %v |\n", s.label, n, s.d[n/2], s.d[n*95/100], s.d[n-1])
	}
	// The CI gate prints test output only on failure; the run summary is where the figures show.
	path := os.Getenv("GITHUB_STEP_SUMMARY")
	if path == "" && os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Fatalf("GITHUB_STEP_SUMMARY is empty on a CI run; the cost figures would be lost")
	}
	if path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatalf("open step summary: %v", err)
		}
		if _, err := f.WriteString(summary); err != nil {
			t.Fatalf("write step summary: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close step summary: %v", err)
		}
		t.Logf("cost figures appended to %s", path)
	}
}
