package platform

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	wireStaff     = "X-User-Staff"
	wireRulesRole = "X-User-Rules-Role"

	forbiddenBody    = `{"error":"forbidden"}`
	unauthorizedBody = `{"error":"unauthorized"}`
)

// staffCalls records what a registered handler saw.
type staffCalls struct {
	mu        sync.Mutex
	n         int
	id        auth.Identity
	hasID     bool
	caller    auth.Identity
	hasCaller bool
	staff     auth.Identity
	hasStaff  bool
}

func (c *staffCalls) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *staffCalls) handle(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.n++
	c.id, c.hasID = auth.IdentityFromContext(r.Context())
	c.caller, c.hasCaller = auth.TenantlessCallerFromContext(r.Context())
	c.staff, c.hasStaff = staffCaller(r.Context())
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(c.staff.Subject))
}

// staffRig is an App with one counter for every /v1/staff route and one for the routes beside them.
type staffRig struct {
	app   *App
	log   *bytes.Buffer
	staff *staffCalls
	other *staffCalls
}

// newStaffRig registers staff routes with HandleFunc and Handle, a panicking staff route, and the
// boundary routes /v1/staffing and PATCH /v1/rules/{key}. gateway selects RequireGateway.
func newStaffRig(t *testing.T, gateway bool) *staffRig {
	t.Helper()
	app := newGuardApp(t)
	rig := &staffRig{app: app, log: &bytes.Buffer{}, staff: &staffCalls{}, other: &staffCalls{}}
	app.Logger = slog.New(slog.NewJSONHandler(rig.log, nil))

	app.Mux.HandleFunc("GET /v1/staff/x", rig.staff.handle)
	app.Mux.HandleFunc("GET /v1/staff/probe", rig.staff.handle)
	app.Mux.Handle("GET /v1/staff", http.HandlerFunc(rig.staff.handle))
	app.Mux.HandleFunc("GET /v1/staff/boom", func(http.ResponseWriter, *http.Request) { panic("staff boom") })
	app.Mux.HandleFunc("GET /v1/staffing", rig.other.handle)
	app.Mux.HandleFunc("GET /v1/ping", rig.other.handle)
	app.Mux.HandleFunc("PATCH /v1/rules/{key}", rig.other.handle)
	if gateway {
		app.RequireGateway(guardToken)
	}
	return rig
}

// who is the caller headers the gateway would forward.
type who struct {
	tenant      bool
	role        string
	staff       string // "" = header absent
	rulesRole   string // "" = header absent
	anonymous   bool   // no X-User-ID
	noGatewayTk bool
}

var (
	customer        = who{tenant: true, role: "authenticated"}
	customerAdmin   = who{tenant: true, role: "admin"}
	staffOnly       = who{tenant: true, role: "authenticated", staff: "true"}
	staffRules      = who{tenant: true, role: "authenticated", staff: "true", rulesRole: "true"}
	tenantlessSR    = who{role: "authenticated", staff: "true", rulesRole: "true"}
	tenantlessSO    = who{role: "authenticated", staff: "true"}
	rulesHeaderOnly = who{tenant: true, role: "authenticated", rulesRole: "true"}
)

func (w who) request(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	if !w.noGatewayTk {
		r.Header.Set(HeaderGatewayToken, guardToken)
	}
	if w.anonymous {
		return r
	}
	r.Header.Set(headerUserID, testSubject)
	r.Header.Set(headerUserRole, w.role)
	if w.tenant {
		r.Header.Set(headerTenantID, testTenant)
	}
	if w.staff != "" {
		r.Header.Set(wireStaff, w.staff)
	}
	if w.rulesRole != "" {
		r.Header.Set(wireRulesRole, w.rulesRole)
	}
	return r
}

func (rig *staffRig) serve(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rig.app.Handler().ServeHTTP(rec, r)
	return rec
}

func assertBody(t *testing.T, rec *httptest.ResponseRecorder, label, want string) {
	t.Helper()
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("%s: body = %q, want %q", label, got, want)
	}
}

// requestLines returns the decoded "request" log records.
func requestLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
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
		if rec["msg"] == "request" {
			out = append(out, rec)
		}
	}
	return out
}

func TestStaffClass_PlainRouteIsChecked(t *testing.T) {
	cases := []struct {
		name string
		who  who
		want int
	}{
		{"customer", customer, http.StatusForbidden},
		{"customer admin", customerAdmin, http.StatusForbidden},
		{"rules header without staff", rulesHeaderOnly, http.StatusForbidden},
		{"staff without rules role", staffOnly, http.StatusForbidden},
		{"tenant-less staff without rules role", tenantlessSO, http.StatusForbidden},
		{"staff with rules role", staffRules, http.StatusOK},
		{"tenant-less staff with rules role", tenantlessSR, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newStaffRig(t, true)
			rec := rig.serve(tc.who.request(http.MethodGet, "/v1/staff/x"))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusForbidden {
				assertBody(t, rec, tc.name, forbiddenBody)
				if n := rig.staff.count(); n != 0 {
					t.Errorf("handler ran %d times, want 0", n)
				}
				return
			}
			if n := rig.staff.count(); n != 1 {
				t.Fatalf("handler ran %d times, want 1", n)
			}
			if !rig.staff.hasStaff || rig.staff.staff.Subject != testSubject {
				t.Errorf("StaffFromContext = %+v (ok %v), want Subject %q", rig.staff.staff, rig.staff.hasStaff, testSubject)
			}
		})
	}
}

func TestStaffClass_NoCallerIsUnauthorized(t *testing.T) {
	rig := newStaffRig(t, true)
	anon := who{anonymous: true}
	rec := rig.serve(anon.request(http.MethodGet, "/v1/staff/x"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body %q)", rec.Code, rec.Body.String())
	}
	assertBody(t, rec, "no caller", unauthorizedBody)
	if n := rig.staff.count(); n != 0 {
		t.Errorf("handler ran %d times, want 0", n)
	}
}

func TestStaffClass_VariantsAreChecked(t *testing.T) {
	variants := []string{
		"/v1/staff",
		"/v1/%73taff/x",
		"/v1/./staff/x",
		"/v1//staff/x",
		"/v1/x/../staff/x",
		"/v1/staff/../staff/x",
	}
	for _, target := range variants {
		t.Run(target, func(t *testing.T) {
			rig := newStaffRig(t, true)
			rec := rig.serve(staffOnly.request(http.MethodGet, target))
			if rec.Code != http.StatusForbidden {
				t.Errorf("staff without rules role: status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
			}
			assertBody(t, rec, target, forbiddenBody)
			if n := rig.staff.count(); n != 0 {
				t.Errorf("handler ran %d times for a staff caller without the rules role, want 0", n)
			}
		})
	}

	// Control: the encoded and bare forms reach a handler for a rules-role caller,
	// so the zero counts above come from the check and not from a dead route.
	for _, target := range []string{"/v1/%73taff/x", "/v1/staff"} {
		rig := newStaffRig(t, true)
		if rec := rig.serve(staffRules.request(http.MethodGet, target)); rec.Code != http.StatusOK || rig.staff.count() != 1 {
			t.Errorf("control %s: status %d, handler ran %d times, want 200 and 1", target, rec.Code, rig.staff.count())
		}
	}
}

func TestStaffClass_OtherPathsAreNotChecked(t *testing.T) {
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/v1/staffing"},
		{http.MethodPatch, "/v1/rules/some-key"},
		{http.MethodGet, "/v1/ping"},
	} {
		t.Run(tc.method+" "+tc.target, func(t *testing.T) {
			rig := newStaffRig(t, true)
			rec := rig.serve(customer.request(tc.method, tc.target))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			if n := rig.other.count(); n != 1 {
				t.Errorf("handler ran %d times, want 1", n)
			}
		})
	}

	// Control: the same customer is refused one segment over, so the 200s above are the prefix boundary.
	rig := newStaffRig(t, true)
	if rec := rig.serve(customer.request(http.MethodGet, "/v1/staff/x")); rec.Code != http.StatusForbidden {
		t.Errorf("control: customer on /v1/staff/x got %d, want 403", rec.Code)
	}
}

func TestStaffClass_RefusalIsRequestLogged(t *testing.T) {
	rig := newStaffRig(t, true)
	rec := rig.serve(staffOnly.request(http.MethodGet, "/v1/staff/x"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	lines := requestLines(t, rig.log)
	if len(lines) != 1 {
		t.Fatalf("log holds %d request line(s), want 1:\n%s", len(lines), rig.log.String())
	}
	got := lines[0]
	if got["method"] != "GET" || got["path"] != "/v1/staff/x" || got["status"] != float64(http.StatusForbidden) {
		t.Errorf("request line = %v, want method GET, path /v1/staff/x, status 403", got)
	}
}

// A staff handler's panic is recovered as a 500. A caller without the rules role never reaches
// the panic, so the 500 is the rules-role caller's and the 403 is the check's.
// The plan expects a request line with status 500; recovery wraps the request log, so none is written.
func TestStaffClass_PanicIsRecovered(t *testing.T) {
	rig := newStaffRig(t, true)
	rec := rig.serve(staffOnly.request(http.MethodGet, "/v1/staff/boom"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("staff without rules role: status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rig.log.String(), "panic recovered") {
		t.Fatalf("the refused caller reached the panicking handler:\n%s", rig.log.String())
	}

	rec = rig.serve(staffRules.request(http.MethodGet, "/v1/staff/boom"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %q)", rec.Code, rec.Body.String())
	}
	assertBody(t, rec, "recovered panic", `{"error":"internal server error"}`)
	if n := strings.Count(rig.log.String(), `"msg":"panic recovered"`); n != 1 {
		t.Errorf("log holds %d panic recovered record(s), want 1:\n%s", n, rig.log.String())
	}
}
