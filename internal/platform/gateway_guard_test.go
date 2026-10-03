package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/google/uuid"
)

const (
	guardToken    = "tok"
	refusalBody   = `{"error":"unauthorized"}` + "\n"
	refusalLogMsg = "request refused: no gateway token"
)

// guardCalls records what the route handlers saw.
type guardCalls struct {
	mu       sync.Mutex
	n        int
	identity auth.Identity
	hasID    bool
	tenantl  bool
	tenantID string
	header   http.Header
}

func (c *guardCalls) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *guardCalls) handler(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	c.identity, c.hasID = auth.IdentityFromContext(r.Context())
	_, c.tenantl = auth.TenantlessCallerFromContext(r.Context())
	c.tenantID = TenantIDFromContext(r.Context())
	c.header = r.Header.Clone()
	w.WriteHeader(http.StatusOK)
}

func newGuardApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	app, err := New("svc")
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// guardedApp registers GET /v1/thing (and POST /v1/peer when open names it) behind RequireGateway.
func guardedApp(t *testing.T, open ...string) (*App, *guardCalls) {
	t.Helper()
	app := newGuardApp(t)
	calls := &guardCalls{}
	app.Mux.HandleFunc("GET /v1/thing", calls.handler)
	app.Mux.HandleFunc("POST /v1/peer", calls.handler)
	app.Mux.HandleFunc("GET /v1/peer", calls.handler)
	app.RequireGateway(guardToken, open...)
	return app, calls
}

type forged struct{ tenant, user, role, email string }

func newForged() forged {
	return forged{tenant: uuid.NewString(), user: uuid.NewString(), role: "authenticated", email: "forged@example.com"}
}

func (f forged) apply(r *http.Request) {
	r.Header.Set("X-Tenant-ID", f.tenant)
	r.Header.Set("X-User-ID", f.user)
	r.Header.Set("X-User-Role", f.role)
	r.Header.Set("X-User-Email", f.email)
}

func serve(app *App, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, r)
	return rec
}

func assertRefused(t *testing.T, rec *httptest.ResponseRecorder, label string) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("%s: status = %d, want 401", label, rec.Code)
	}
	if rec.Body.String() != refusalBody {
		t.Errorf("%s: body = %q, want %q", label, rec.Body.String(), refusalBody)
	}
	if got := rec.Header().Get(HeaderGatewayGuard); got != GatewayGuardRefused {
		t.Errorf("%s: %s = %q, want %q", label, HeaderGatewayGuard, got, GatewayGuardRefused)
	}
}

func TestRequireGateway_RefusesForgedIdentityWithoutToken(t *testing.T) {
	app, calls := guardedApp(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	newForged().apply(r)

	assertRefused(t, serve(app, r), "forged identity, no token")
	if n := calls.count(); n != 0 {
		t.Errorf("handler ran %d times, want 0", n)
	}
}

func TestRequireGateway_RefusesWrongEmptyAndShortTokens(t *testing.T) {
	for _, tok := range []string{"tik", "", "to", "tok ", "tokk", "TOK", "t"} {
		t.Run("token="+tok, func(t *testing.T) {
			app, calls := guardedApp(t)
			r := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
			r.Header.Set(HeaderGatewayToken, tok)
			newForged().apply(r)

			assertRefused(t, serve(app, r), "token "+tok)
			if n := calls.count(); n != 0 {
				t.Errorf("handler ran %d times, want 0", n)
			}
		})
	}
}

func TestRequireGateway_AdmitsTheGatewayAndBuildsIdentity(t *testing.T) {
	app, calls := guardedApp(t)
	f := newForged()
	// Same App refuses without the token, so the 200 below is the token's doing.
	assertRefused(t, serve(app, httptest.NewRequest(http.MethodGet, "/v1/thing", nil)), "no token")
	r := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	r.Header.Set(HeaderGatewayToken, guardToken)
	f.apply(r)

	rec := serve(app, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls.count() != 1 || !calls.hasID {
		t.Fatalf("handler calls = %d, identity present = %v; want 1 and true", calls.count(), calls.hasID)
	}
	want := auth.Identity{Subject: f.user, Role: f.role, TenantID: f.tenant, Email: f.email}
	if calls.identity != want {
		t.Errorf("identity = %+v, want %+v", calls.identity, want)
	}
}

func TestRequireGateway_UnmatchedPathAndMethodAre401(t *testing.T) {
	for _, tc := range []struct{ name, method, path string }{
		{"unregistered path", http.MethodGet, "/v1/nothing"},
		{"registered path, wrong method", http.MethodPost, "/v1/thing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, calls := guardedApp(t)
			assertRefused(t, serve(app, httptest.NewRequest(tc.method, tc.path, nil)), tc.name)
			if n := calls.count(); n != 0 {
				t.Errorf("handler ran %d times, want 0", n)
			}
		})
	}
}

func TestRequireGateway_HealthProbesStayOpen(t *testing.T) {
	guarded, _ := guardedApp(t)
	guarded.Ready("dep", func(_ context.Context) error { return nil })
	plain := newGuardApp(t)
	plain.Ready("dep", func(_ context.Context) error { return nil })

	// The guard must be engaged, or "probes answer as at head" proves nothing.
	assertRefused(t, serve(guarded, httptest.NewRequest(http.MethodGet, "/v1/thing", nil)), "guard engaged")

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, probe := range []string{"healthz", "readyz"} {
			for _, withForged := range []bool{false, true} {
				name := fmt.Sprintf("%s_%s_forged=%v", method, probe, withForged)
				t.Run(name, func(t *testing.T) {
					mk := func() *http.Request {
						r := httptest.NewRequest(method, "/"+probe, nil)
						if withForged {
							newForged().apply(r)
						}
						return r
					}
					got, want := serve(guarded, mk()), serve(plain, mk())
					if want.Code != http.StatusOK {
						t.Fatalf("control status = %d, want 200", want.Code)
					}
					if got.Code != want.Code || got.Body.String() != want.Body.String() {
						t.Errorf("got %d %q, want %d %q", got.Code, got.Body.String(), want.Code, want.Body.String())
					}
				})
			}
		}
	}
}

func TestRequireGateway_OpenRouteAdmitsWithoutIdentity(t *testing.T) {
	app, calls := guardedApp(t, "POST /v1/peer")
	r := httptest.NewRequest(http.MethodPost, "/v1/peer", nil)
	newForged().apply(r)

	rec := serve(app, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls.count() != 1 {
		t.Fatalf("handler ran %d times, want 1", calls.count())
	}
	if calls.hasID {
		t.Errorf("handler saw identity %+v, want none", calls.identity)
	}
	if calls.tenantl {
		t.Error("handler saw a tenant-less caller, want none")
	}
	if calls.tenantID != "" {
		t.Errorf("handler saw tenant id %q, want none", calls.tenantID)
	}
	for _, h := range []string{"X-Tenant-ID", "X-User-ID", "X-User-Role", "X-User-Email"} {
		if v := calls.header.Get(h); v != "" {
			t.Errorf("handler still sees %s = %q, want it deleted", h, v)
		}
	}
}

func TestRequireGateway_OpenPatternIsMethodExact(t *testing.T) {
	app, calls := guardedApp(t, "POST /v1/peer")

	assertRefused(t, serve(app, httptest.NewRequest(http.MethodGet, "/v1/peer", nil)), "GET on a POST-only open pattern")
	if n := calls.count(); n != 0 {
		t.Errorf("GET handler ran %d times, want 0", n)
	}
	// The open pattern itself admits, so the 401 above is the method, not a dead route.
	if rec := serve(app, httptest.NewRequest(http.MethodPost, "/v1/peer", nil)); rec.Code != http.StatusOK {
		t.Errorf("POST /v1/peer status = %d, want 200", rec.Code)
	}
}

func TestRequireGateway_RefusalLogsMethodAndPathOnly(t *testing.T) {
	var buf bytes.Buffer
	app, _ := guardedAppWithLogger(t, &buf)
	const (
		wrongTok = "wrong-marker-7731"
		idMarker = "id-marker-7731"
	)
	r := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	r.Header.Set(HeaderGatewayToken, wrongTok)
	r.Header.Set("X-Tenant-ID", idMarker+"-tenant")
	r.Header.Set("X-User-ID", idMarker+"-user")
	r.Header.Set("X-User-Role", idMarker+"-role")
	r.Header.Set("X-User-Email", idMarker+"-email")
	assertRefused(t, serve(app, r), "marker request")

	recs := logRecords(t, &buf)
	var refusals []map[string]any
	for _, rec := range recs {
		if rec["msg"] == refusalLogMsg {
			refusals = append(refusals, rec)
		}
	}
	if len(refusals) != 1 {
		t.Fatalf("refusal records = %d (of %d), want exactly 1: %s", len(refusals), len(recs), buf.String())
	}
	line := refusals[0]
	if line["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", line["level"])
	}
	vals := map[string]bool{}
	for _, v := range line {
		if s, ok := v.(string); ok {
			vals[s] = true
		}
	}
	if !vals[http.MethodGet] || !vals["/v1/thing"] {
		t.Errorf("record %v lacks method GET and path /v1/thing", line)
	}
	for _, m := range []string{wrongTok, idMarker} {
		if strings.Contains(buf.String(), m) {
			t.Errorf("log buffer holds %q: %s", m, buf.String())
		}
	}

	// An admitted request adds no refusal record.
	ok := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	ok.Header.Set(HeaderGatewayToken, guardToken)
	if rec := serve(app, ok); rec.Code != http.StatusOK {
		t.Fatalf("admitted status = %d, want 200", rec.Code)
	}
	if n := strings.Count(buf.String(), refusalLogMsg); n != 1 {
		t.Errorf("refusal lines after an admitted request = %d, want 1", n)
	}
}

func guardedAppWithLogger(t *testing.T, buf *bytes.Buffer) (*App, *guardCalls) {
	t.Helper()
	app := newGuardApp(t)
	app.Logger = slog.New(slog.NewJSONHandler(buf, nil))
	calls := &guardCalls{}
	app.Mux.HandleFunc("GET /v1/thing", calls.handler)
	app.RequireGateway(guardToken)
	return app, calls
}

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestRequireGateway_PanicsOnEmptyToken(t *testing.T) {
	app := newGuardApp(t)
	defer func() {
		if recover() == nil {
			t.Error(`RequireGateway("") did not panic`)
		}
	}()
	app.RequireGateway("")
}

func TestApp_WithoutRequireGatewayStillTrustsHeaders(t *testing.T) {
	app := newGuardApp(t)
	calls := &guardCalls{}
	app.Mux.HandleFunc("GET /v1/thing", calls.handler)
	f := newForged()
	r := httptest.NewRequest(http.MethodGet, "/v1/thing", nil)
	f.apply(r)

	if rec := serve(app, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := auth.Identity{Subject: f.user, Role: f.role, TenantID: f.tenant, Email: f.email}
	if !calls.hasID || calls.identity != want {
		t.Errorf("identity = %+v (present %v), want %+v", calls.identity, calls.hasID, want)
	}
}

// bodySpy counts reads of the request body.
type bodySpy struct {
	io.Reader
	reads int
}

func (b *bodySpy) Read(p []byte) (int, error) { b.reads++; return b.Reader.Read(p) }
func (b *bodySpy) Close() error               { return nil }

func TestRequireToken_AdmitsOnlyTheExactTokenBeforeReadingTheBody(t *testing.T) {
	const header = "X-Test-Token"
	var handlerReads int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		handlerReads++
		w.WriteHeader(http.StatusOK)
	})
	h := RequireToken(header, "t")(next)

	for _, tc := range []struct {
		name, token string
		set         bool
		want        int
	}{
		{"missing", "", false, http.StatusUnauthorized},
		{"wrong", "x", true, http.StatusUnauthorized},
		{"exact", "t", true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &bodySpy{Reader: strings.NewReader(`{"a":1}`)}
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r.Body = spy
			if tc.set {
				r.Header.Set(header, tc.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusUnauthorized {
				if rec.Body.String() != refusalBody {
					t.Errorf("body = %q, want %q", rec.Body.String(), refusalBody)
				}
				if spy.reads != 0 {
					t.Errorf("body read %d times before the 401, want 0", spy.reads)
				}
			} else if spy.reads == 0 {
				t.Error("handler never read the body; the spy cannot observe a read")
			}
		})
	}
	if handlerReads != 1 {
		t.Errorf("handler ran %d times, want 1 (exact token only)", handlerReads)
	}
}
