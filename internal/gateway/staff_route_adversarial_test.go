package gateway

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	wireUserStaff     = "X-User-Staff"
	wireUserRulesRole = "X-User-Rules-Role"

	staffRoute = "/api/{svc}/v1/staff/rules"
)

var staffMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodConnect}

// rawBody writes the request line verbatim (a Go client would clean it), follows redirects by hand,
// and returns the final status, body and redirect count.
func (r *internalRig) rawBody(t *testing.T, method, target, bearer string) (status int, body string, redirects int) {
	t.Helper()
	for range 6 {
		conn, err := net.Dial("tcp", r.srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: gw.test\r\nAuthorization: Bearer %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", method, target, bearer)
		resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
		if err != nil {
			_ = conn.Close()
			t.Fatalf("%s %q: %v", method, target, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = conn.Close()
		if resp.StatusCode/100 != 3 {
			return resp.StatusCode, strings.TrimSpace(string(b)), redirects
		}
		loc, err := resp.Location()
		if err != nil {
			t.Fatalf("%s %q: %v", method, target, err)
		}
		target = loc.RequestURI()
		redirects++
	}
	t.Fatalf("%s %q: redirect loop", method, target)
	return 0, "", 0
}

func (r *internalRig) resetAll() {
	for _, c := range r.counts {
		c.reset()
	}
}

// total sums a counter over the whole fleet.
func (r *internalRig) total(pick func(*upstreamCount) int32) (n int32) {
	for _, c := range r.counts {
		n += pick(c)
	}
	return n
}

func (r *internalRig) mintOpts(t *testing.T, o auth.MintOptions) string {
	t.Helper()
	if o.Subject == "" {
		o.Subject = testSubject
	}
	if o.Role == "" {
		o.Role = testRole
	}
	return r.tg.mint(t, o)
}

func assertNoHeader(t *testing.T, h http.Header, key string) {
	t.Helper()
	if v, ok := h[key]; ok {
		t.Errorf("upstream header %s = %q, want it absent", key, v)
	}
}

func assertStaffHeaders(t *testing.T, h http.Header, staff, rules bool) {
	t.Helper()
	for _, c := range []struct {
		key  string
		want bool
	}{{wireUserStaff, staff}, {wireUserRulesRole, rules}} {
		if c.want {
			if got := h.Values(c.key); !slices.Equal(got, []string{"true"}) {
				t.Errorf("upstream header %s = %q, want exactly [true]", c.key, got)
			}
		} else {
			assertNoHeader(t, h, c.key)
		}
	}
}

func TestStaffRoute_EveryNonStaffTokenIsRefusedOnEveryService(t *testing.T) {
	rig := newInternalRig(t)
	tokens := map[string]string{
		"tenant-bearing customer":  rig.mintOpts(t, auth.MintOptions{TenantID: testTenant}),
		"tenant-less customer":     rig.mintOpts(t, auth.MintOptions{}),
		"customer admin":           rig.mintOpts(t, auth.MintOptions{Role: "admin", TenantID: testTenant}),
		"rules_role only":          rig.mintOpts(t, auth.MintOptions{TenantID: testTenant, RulesRole: true}),
		"tenant-less rules_role":   rig.mintOpts(t, auth.MintOptions{RulesRole: true}),
		"service_role":             rig.mintOpts(t, auth.MintOptions{Role: "service_role", TenantID: testTenant}),
		"tenant-less service_role": rig.mintOpts(t, auth.MintOptions{Role: "service_role"}),
	}

	// Control: the rig reaches an upstream and counts a staff hit, so the zero counts below are not vacuous.
	rig.resetAll()
	if status, _, _ := rig.rawBody(t, http.MethodGet, "/api/validation/v1/ping", tokens["tenant-bearing customer"]); status != http.StatusAccepted && status != http.StatusNotFound {
		t.Fatalf("control: GET /v1/ping answered %d", status)
	}
	if rig.counts["validation"].any.Load() != 1 {
		t.Fatalf("control: the upstream saw %d requests, want 1", rig.counts["validation"].any.Load())
	}
	staffTok := rig.mintOpts(t, auth.MintOptions{TenantID: testTenant, Staff: true})
	rig.resetAll()
	if status, _, _ := rig.rawBody(t, http.MethodGet, "/api/validation/v1/staff/rules", staffTok); status != http.StatusAccepted || rig.counts["validation"].staff.Load() != 1 {
		t.Fatalf("control: a tenant-bearing staff token answered %d with %d staff hits, want 202 and 1", status, rig.counts["validation"].staff.Load())
	}

	for name, tok := range tokens {
		for _, svc := range rig.services {
			for _, method := range staffMethods {
				t.Run(name+"/"+svc+"/"+method, func(t *testing.T) {
					rig.resetAll()
					status, body, _ := rig.rawBody(t, method, rig.path(staffRoute, svc), tok)
					if status != http.StatusForbidden || body != `{"error":"forbidden"}` {
						t.Errorf("%s answered %d %q, want 403 {\"error\":\"forbidden\"}", method, status, body)
					}
					if n := rig.total(func(c *upstreamCount) int32 { return c.any.Load() }); n != 0 {
						t.Errorf("%s reached an upstream %d times, want 0", method, n)
					}
				})
			}
		}
	}
}

func TestStaffRoute_BareStaffSegmentIsRefused(t *testing.T) {
	rig := newInternalRig(t)
	tok := rig.mintOpts(t, auth.MintOptions{TenantID: testTenant})
	for _, p := range []string{"/api/validation/v1/staff", "/api/validation/v1/staff/"} {
		t.Run(p, func(t *testing.T) {
			rig.resetAll()
			status, _, _ := rig.rawBody(t, http.MethodGet, p, tok)
			if status != http.StatusForbidden {
				t.Errorf("answered %d, want 403", status)
			}
			if n := rig.total(func(c *upstreamCount) int32 { return c.any.Load() }); n != 0 {
				t.Errorf("reached an upstream %d times, want 0", n)
			}
		})
	}
}

func TestStaffRoute_PathVariantsNeverReachAStaffHandler(t *testing.T) {
	rig := newInternalRig(t)
	cust := rig.mintOpts(t, auth.MintOptions{TenantID: testTenant})
	tenantless := rig.mintOpts(t, auth.MintOptions{})
	variants := []struct{ name, tmpl string }{
		{"encoded letter", "/api/{svc}/v1/%73taff/x"},
		{"encoded v1 segment", "/api/{svc}/%761/staff/x"},
		{"encoded service segment", "/api/{esvc}/v1/staff/x"},
		{"dot segment", "/api/{svc}/v1/./staff/x"},
		{"encoded dot segment", "/api/{svc}/v1/%2e/staff/x"},
		{"double slash", "/api/{svc}/v1//staff/x"},
		{"dot-dot", "/api/{svc}/x/../v1/staff/x"},
		{"encoded dot-dot", "/api/{svc}/x/%2e%2e/v1/staff/x"},
		{"dot-dot out of staff", "/api/{svc}/v1/staff/../invoices"},
		{"encoded dot-dot out of staff", "/api/{svc}/v1/staff/%2e%2e/invoices"},
	}

	// Control: a staff handler counts when the staff token reaches it, on the same service.
	staffTok := rig.mintOpts(t, auth.MintOptions{TenantID: testTenant, Staff: true})
	rig.resetAll()
	if status, _, _ := rig.rawBody(t, http.MethodGet, "/api/validation/v1/staff/x", staffTok); status != http.StatusAccepted || rig.counts["validation"].staff.Load() != 1 {
		t.Fatalf("control: tenant-bearing staff answered %d with %d staff hits, want 202 and 1", status, rig.counts["validation"].staff.Load())
	}

	for tokName, tok := range map[string]string{"tenant-bearing": cust, "tenant-less": tenantless} {
		for _, svc := range rig.services {
			for _, v := range variants {
				for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodConnect} {
					t.Run(tokName+"/"+svc+"/"+v.name+"/"+method, func(t *testing.T) {
						rig.resetAll()
						esvc := fmt.Sprintf("%%%02x", svc[0]) + svc[1:]
						target := strings.ReplaceAll(rig.path(v.tmpl, svc), "{esvc}", esvc)
						status, _, _ := rig.rawBody(t, method, target, tok)
						if status != http.StatusForbidden && status != http.StatusNotFound {
							t.Errorf("%s %s answered %d, want 403 or 404", method, target, status)
						}
						// CONNECT is not cleaned by the upstream mux, so the gateway's own refusal is the only guard.
						if n := rig.total(func(c *upstreamCount) int32 { return c.any.Load() }); method == http.MethodConnect && (status != http.StatusForbidden || n != 0) {
							t.Errorf("%s %s answered %d with %d upstream hits, want the gateway's 403 and 0", method, target, status, n)
						}
						if n := rig.total(func(c *upstreamCount) int32 { return c.staff.Load() }); n != 0 {
							t.Errorf("%s %s reached a staff handler %d times, want 0", method, target, n)
						}
					})
				}
			}
		}
	}
}

// staffing shares the "staff" prefix but not the segment: customers reach the upstream, nobody gets the staff class.
func TestStaffRoute_StaffingIsNotTheStaffClass(t *testing.T) {
	rig := newInternalRig(t)
	cust := rig.mintOpts(t, auth.MintOptions{TenantID: testTenant})
	rig.resetAll()
	if status, _, _ := rig.rawBody(t, http.MethodGet, "/api/validation/v1/staff/x", cust); status != http.StatusForbidden {
		t.Fatalf("control: customer on /v1/staff/x answered %d, want 403", status)
	}
	for _, p := range []string{"/api/validation/v1/staffing", "/api/validation/v1/staffing/x", "/api/validation/v1/staff-x"} {
		t.Run(p, func(t *testing.T) {
			rig.resetAll()
			status, _, _ := rig.rawBody(t, http.MethodGet, p, cust)
			if status != http.StatusNotFound || rig.counts["validation"].any.Load() != 1 {
				t.Errorf("answered %d with %d upstream hits, want the upstream's 404 and 1 hit (not the gateway's 403)", status, rig.counts["validation"].any.Load())
			}
		})
	}
}

func TestStaffRoute_EncodedSlashIsRefusedByTheGateway(t *testing.T) {
	rig := newInternalRig(t)
	tok := rig.mintOpts(t, auth.MintOptions{TenantID: testTenant})
	for _, svc := range rig.services {
		t.Run(svc, func(t *testing.T) {
			rig.resetAll()
			status, _, _ := rig.rawBody(t, http.MethodGet, "/api/"+svc+"/v1/staff%2Fx", tok)
			if status != http.StatusForbidden {
				t.Errorf("answered %d, want 403 from the gateway (the upstream mux answers 404)", status)
			}
			if n := rig.total(func(c *upstreamCount) int32 { return c.any.Load() }); n != 0 {
				t.Errorf("reached an upstream %d times, want 0", n)
			}
		})
	}
}

func TestStaffRoute_CaseVariantIsNoStaffRoute(t *testing.T) {
	rig := newInternalRig(t)
	tok := rig.mintOpts(t, auth.MintOptions{TenantID: testTenant})
	// Control: the exact-case path is a staff route, so the zero below is the case, not a dead counter.
	rig.resetAll()
	staffTok := rig.mintOpts(t, auth.MintOptions{TenantID: testTenant, Staff: true})
	rig.rawBody(t, http.MethodGet, "/api/validation/v1/staff/x", staffTok)
	if rig.counts["validation"].staff.Load() != 1 {
		t.Fatal("control: the staff route did not count a hit")
	}
	for _, p := range []string{"/api/validation/v1/Staff/x", "/api/validation/v1/STAFF/x", "/api/validation/V1/staff/x"} {
		t.Run(p, func(t *testing.T) {
			rig.resetAll()
			rig.rawBody(t, http.MethodGet, p, tok)
			if n := rig.total(func(c *upstreamCount) int32 { return c.staff.Load() }); n != 0 {
				t.Errorf("reached a staff handler %d times, want 0", n)
			}
		})
	}
}

func TestStaffRoute_TenantlessStaffReachesTheUpstream(t *testing.T) {
	rig := newInternalRig(t)
	tok := rig.mintOpts(t, auth.MintOptions{Staff: true})
	for _, svc := range rig.services {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			t.Run(svc+"/"+method, func(t *testing.T) {
				rig.resetAll()
				status, _, _ := rig.rawBody(t, method, rig.path(staffRoute, svc), tok)
				if status != http.StatusAccepted {
					t.Errorf("%s answered %d, want 202 from the staff handler", method, status)
				}
				if n := rig.counts[svc].staff.Load(); n != 1 {
					t.Errorf("%s reached the %s staff handler %d times, want 1", method, svc, n)
				}
			})
		}
	}

	t.Run("upstream sees no tenant", func(t *testing.T) {
		tg := setupGateway(t)
		rec := httptest.NewRecorder()
		tg.handler.ServeHTTP(rec, request("GET", "/api/validation/v1/staff/rules", tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Staff: true})))
		cap := tg.caps["validation"]
		if rec.Code != http.StatusOK || cap.hits != 1 {
			t.Fatalf("status = %d, hits = %d, want 200 and 1", rec.Code, cap.hits)
		}
		if cap.path != "/v1/staff/rules" {
			t.Errorf("upstream path = %q, want /v1/staff/rules", cap.path)
		}
		assertHeader(t, cap.header, headerTenantID, "")
		assertHeader(t, cap.header, headerUserID, testSubject)
	})
}

func TestStaffRoute_TenantlessStaffIsRefusedElsewhere(t *testing.T) {
	rig := newInternalRig(t)
	tok := rig.mintOpts(t, auth.MintOptions{Staff: true, RulesRole: true})

	// Control: the same token is admitted on an exact staff route, so each 403 below is the narrow match.
	rig.resetAll()
	if status, _, _ := rig.rawBody(t, http.MethodGet, "/api/validation/v1/staff/rules", tok); status != http.StatusAccepted || rig.counts["validation"].staff.Load() != 1 {
		t.Fatalf("control: answered %d with %d staff hits, want 202 and 1", status, rig.counts["validation"].staff.Load())
	}

	// The mux cleans a dot or double slash for every method but CONNECT, then the clean path is a staff route: those variants run as CONNECT only.
	cases := []struct {
		name, path string
		methods    []string
	}{
		{"normal route", "/api/invoice/v1/invoices", []string{http.MethodGet, http.MethodPost}},
		{"normal route under validation", "/api/validation/v1/rules", []string{http.MethodGet}},
		{"encoded letter", "/api/validation/v1/%73taff/rules", []string{http.MethodGet, http.MethodConnect}},
		{"dot-dot out of staff", "/api/validation/v1/staff/../invoices", []string{http.MethodGet, http.MethodConnect}},
		{"dot segment", "/api/validation/v1/./staff/rules", []string{http.MethodConnect}},
		{"encoded dot segment", "/api/validation/v1/%2e/staff/rules", []string{http.MethodGet, http.MethodConnect}},
		{"double slash", "/api/validation/v1//staff/rules", []string{http.MethodConnect}},
		{"encoded slash", "/api/validation/v1/staff%2Frules", []string{http.MethodGet}},
		{"encoded v1", "/api/validation/%761/staff/rules", []string{http.MethodGet, http.MethodConnect}},
		{"dot-dot into staff", "/api/validation/x/../v1/staff/rules", []string{http.MethodConnect}},
		{"case", "/api/validation/v1/Staff/rules", []string{http.MethodGet}},
		{"bare staff segment (D28)", "/api/validation/v1/staff", []string{http.MethodGet, http.MethodPost, http.MethodConnect}},
		{"staffing is no staff route", "/api/validation/v1/staffing", []string{http.MethodGet}},
		{"dot after staff", "/api/validation/v1/staff/./rules", []string{http.MethodConnect}},
		{"double slash after staff", "/api/validation/v1/staff//rules", []string{http.MethodConnect}},
		{"encoded dot-dot after staff", "/api/validation/v1/staff/%2e%2e/invoices", []string{http.MethodGet, http.MethodConnect}},
		{"encoded slash after staff", "/api/validation/v1/staff/a%2Fb", []string{http.MethodGet, http.MethodConnect}},
	}
	for _, tc := range cases {
		for _, method := range tc.methods {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				rig.resetAll()
				status, _, _ := rig.rawBody(t, method, tc.path, tok)
				if status != http.StatusForbidden {
					t.Errorf("%s %s answered %d, want 403", method, tc.path, status)
				}
				if a, s := rig.total(func(c *upstreamCount) int32 { return c.any.Load() }), rig.total(func(c *upstreamCount) int32 { return c.staff.Load() }); a != 0 || s != 0 {
					t.Errorf("%s %s reached an upstream %d times (%d staff), want 0", method, tc.path, a, s)
				}
			})
		}
	}
}

func TestStaffRoute_TenantlessStaffKeepsTheTwoPostExemptions(t *testing.T) {
	for _, path := range []string{provisioningPath, "/api/tenancy/v1/invitations/accept"} {
		t.Run(path, func(t *testing.T) {
			tg := setupGateway(t)
			tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, Staff: true})
			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request("POST", path, tok))
			if rec.Code != http.StatusOK || tg.caps["tenancy"].hits != 1 {
				t.Fatalf("status = %d, tenancy hits = %d, want 200 and 1", rec.Code, tg.caps["tenancy"].hits)
			}
			assertHeader(t, tg.caps["tenancy"].header, headerTenantID, "")
			// Control: the exemption is the POST route, not staff: GET stays refused.
			rec = httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request("GET", path, tok))
			if rec.Code != http.StatusForbidden || tg.caps["tenancy"].hits != 1 {
				t.Errorf("GET: status = %d, tenancy hits = %d, want 403 and 1", rec.Code, tg.caps["tenancy"].hits)
			}
		})
	}
}

func TestStaffRoute_TenantBearingStaffReachesBoth(t *testing.T) {
	for _, svc := range []string{"validation", "invoice", "tenancy"} {
		t.Run(svc, func(t *testing.T) {
			tg := setupGateway(t)
			tok := tg.mint(t, auth.MintOptions{Subject: testSubject, Role: testRole, TenantID: testTenant, Staff: true})
			cap := tg.caps[svc]

			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request("GET", "/api/"+svc+"/v1/staff/rules", tok))
			if rec.Code != http.StatusOK || cap.hits != 1 || cap.path != "/v1/staff/rules" {
				t.Fatalf("staff route: status = %d, hits = %d, path = %q, want 200, 1, /v1/staff/rules", rec.Code, cap.hits, cap.path)
			}
			assertHeader(t, cap.header, headerTenantID, testTenant)

			rec = httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request("GET", "/api/"+svc+"/v1/invoices", tok))
			if rec.Code != http.StatusOK || cap.hits != 2 || cap.path != "/v1/invoices" {
				t.Fatalf("normal route: status = %d, hits = %d, path = %q, want 200, 2, /v1/invoices", rec.Code, cap.hits, cap.path)
			}
			assertHeader(t, cap.header, headerTenantID, testTenant)
		})
	}
}

func TestStaffHeaders_FollowTheToken(t *testing.T) {
	const normal, staff = "/api/invoice/v1/invoices", "/api/validation/v1/staff/rules"
	cases := []struct {
		name         string
		opts         auth.MintOptions
		path, svc    string
		wantS, wantR bool
	}{
		{"customer", auth.MintOptions{TenantID: testTenant}, normal, "invoice", false, false},
		{"rules_role without staff", auth.MintOptions{TenantID: testTenant, RulesRole: true}, normal, "invoice", false, false},
		{"staff on a normal route", auth.MintOptions{TenantID: testTenant, Staff: true}, normal, "invoice", true, false},
		{"staff on a staff route", auth.MintOptions{TenantID: testTenant, Staff: true}, staff, "validation", true, false},
		{"staff and rules on a normal route", auth.MintOptions{TenantID: testTenant, Staff: true, RulesRole: true}, normal, "invoice", true, true},
		{"staff and rules on a staff route", auth.MintOptions{TenantID: testTenant, Staff: true, RulesRole: true}, staff, "validation", true, true},
		{"tenant-less staff and rules on a staff route", auth.MintOptions{Staff: true, RulesRole: true}, staff, "validation", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := setupGateway(t)
			tc.opts.Subject, tc.opts.Role = testSubject, testRole
			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, request("GET", tc.path, tg.mint(t, tc.opts)))
			cap := tg.caps[tc.svc]
			if rec.Code != http.StatusOK || cap.hits != 1 {
				t.Fatalf("status = %d, hits = %d, want 200 and 1", rec.Code, cap.hits)
			}
			assertStaffHeaders(t, cap.header, tc.wantS, tc.wantR)
		})
	}
}

func TestStaffHeaders_ClientCopiesAreOverwritten(t *testing.T) {
	cases := []struct {
		name         string
		opts         auth.MintOptions
		method, path string
		svc          string
		wantS, wantR bool
	}{
		{"customer, normal route", auth.MintOptions{TenantID: testTenant}, "GET", "/api/invoice/v1/invoices", "invoice", false, false},
		{"tenant-less customer, workspaces", auth.MintOptions{}, "POST", provisioningPath, "tenancy", false, false},
		{"tenant-less customer, accept", auth.MintOptions{}, "POST", "/api/tenancy/v1/invitations/accept", "tenancy", false, false},
		{"staff only, staff route", auth.MintOptions{TenantID: testTenant, Staff: true}, "GET", "/api/validation/v1/staff/rules", "validation", true, false},
		{"staff only, normal route", auth.MintOptions{TenantID: testTenant, Staff: true}, "GET", "/api/invoice/v1/invoices", "invoice", true, false},
		{"tenant-less staff only, staff route", auth.MintOptions{Staff: true}, "POST", "/api/validation/v1/staff/rules", "validation", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := setupGateway(t)
			tc.opts.Subject, tc.opts.Role = testSubject, testRole
			r := request(tc.method, tc.path, tg.mint(t, tc.opts))
			for _, k := range []string{wireUserStaff, wireUserRulesRole} {
				r.Header.Add(k, "true")
				r.Header.Add(k, "true")
			}
			rec := httptest.NewRecorder()
			tg.handler.ServeHTTP(rec, r)
			cap := tg.caps[tc.svc]
			if rec.Code != http.StatusOK || cap.hits != 1 {
				t.Fatalf("status = %d, hits = %d, want 200 and 1", rec.Code, cap.hits)
			}
			assertStaffHeaders(t, cap.header, tc.wantS, tc.wantR)
		})
	}
}
