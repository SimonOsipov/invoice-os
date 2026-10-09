package platform

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const testSubject = "c0000000-0000-0000-0000-000000000001"

func TestIdentityMiddleware(t *testing.T) {
	var got auth.Identity
	var ok bool
	h := identityMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, ok = auth.IdentityFromContext(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Tenant-ID", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	req.Header.Set("X-User-ID", testSubject)
	req.Header.Set("X-User-Role", "authenticated")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !ok {
		t.Fatal("expected an identity in context when the gateway headers are present")
	}
	want := auth.Identity{
		Subject:  testSubject,
		Role:     "authenticated",
		TenantID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
	}
	if got != want {
		t.Errorf("identity = %+v, want %+v", got, want)
	}
}

func TestIdentityMiddlewareAbsent(t *testing.T) {
	// No tenant header → no identity: the middleware must fail closed so an
	// un-fronted service never fabricates a caller (db.WithinRequestTenantTx then
	// refuses with ErrNoTenant).
	var present bool
	h := identityMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, present = auth.IdentityFromContext(r.Context())
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))

	if present {
		t.Error("expected no identity in context when the tenant header is absent")
	}
}

// contextKeys runs identityMiddleware on req and reports what each context key holds.
func contextKeys(req *http.Request) (id auth.Identity, idOK bool, caller auth.Identity, callerOK bool) {
	identityMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		id, idOK = auth.IdentityFromContext(r.Context())
		caller, callerOK = auth.TenantlessCallerFromContext(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), req)
	return
}

func TestIdentityMiddleware_TenantlessCaller(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/workspaces", nil)
	req.Header.Set("X-User-ID", testSubject)
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if !callerOK {
		t.Fatal("expected a tenant-less caller when X-User-ID is set and X-Tenant-ID is not")
	}
	want := auth.Identity{Subject: testSubject, Role: "authenticated", Email: "ada@example.test"}
	if caller != want {
		t.Errorf("tenant-less caller = %+v, want %+v", caller, want)
	}
	if idOK {
		t.Errorf("IdentityFromContext = %+v, want none for a tenant-less caller", id)
	}
}

func TestIdentityMiddleware_TenantHeaderStillBuildsIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Tenant-ID", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	req.Header.Set("X-User-ID", testSubject)
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if !idOK {
		t.Fatal("expected an identity when X-Tenant-ID is set")
	}
	want := auth.Identity{
		Subject:  testSubject,
		Role:     "authenticated",
		TenantID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		Email:    "ada@example.test",
	}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
	if callerOK {
		t.Errorf("TenantlessCallerFromContext = %+v, want none when a tenant is present", caller)
	}
}

func TestIdentityMiddleware_NoUserNoIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if idOK {
		t.Errorf("IdentityFromContext = %+v, want none without X-Tenant-ID and X-User-ID", id)
	}
	if callerOK {
		t.Errorf("TenantlessCallerFromContext = %+v, want none without X-Tenant-ID and X-User-ID", caller)
	}

	// Control: the same request plus X-User-ID does build a caller.
	req.Header.Set("X-User-ID", testSubject)
	if _, _, _, ok := contextKeys(req); !ok {
		t.Error("control: adding X-User-ID built no tenant-less caller")
	}
}

const testTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestIdentityMiddleware_NonUUIDSubjectBuildsNoIdentity(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Tenant-ID", testTenant)
	req.Header.Set("X-User-ID", "user-42")
	req.Header.Set("X-User-Role", "authenticated")
	req.Header.Set("X-User-Email", "ada@example.test")

	id, idOK, caller, callerOK := contextKeys(req)
	if idOK {
		t.Errorf("IdentityFromContext = %+v, want none for a non-uuid subject", id)
	}
	if callerOK {
		t.Errorf("TenantlessCallerFromContext = %+v, want none when a tenant header is present", caller)
	}
}

func TestIdentityMiddleware_EmptySubjectWithTenantBuildsNoIdentity(t *testing.T) {
	cases := []struct {
		name string
		user []string // nil: header absent
	}{
		{"absent", nil},
		{"present and empty", []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("X-Tenant-ID", testTenant)
			req.Header.Set("X-User-Role", "authenticated")
			if tc.user != nil {
				req.Header["X-User-Id"] = tc.user
			}
			id, idOK, caller, callerOK := contextKeys(req)
			if idOK {
				t.Errorf("IdentityFromContext = %+v, want none for an empty subject", id)
			}
			if callerOK {
				t.Errorf("TenantlessCallerFromContext = %+v, want none", caller)
			}
		})
	}
}

func TestIdentityMiddleware_NonUUIDTenantlessSubjectBuildsNoCaller(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/workspaces", nil)
	req.Header.Set("X-User-ID", "user-42")
	req.Header.Set("X-User-Role", "authenticated")

	id, idOK, caller, callerOK := contextKeys(req)
	if callerOK {
		t.Errorf("TenantlessCallerFromContext = %+v, want none for a non-uuid subject", caller)
	}
	if idOK {
		t.Errorf("IdentityFromContext = %+v, want none", id)
	}
}

func TestIdentityMiddleware_NonUUIDSubjectShapes(t *testing.T) {
	subjects := []string{
		"user-42",
		"c0000000-0000-0000-0000-00000000000", // 35 chars
		" ",
		"c0000000-0000-0000-0000-00000000000g", // non-hex digit
		" " + testSubject,                      // leading space, 37 chars
		testSubject + " ",
		testSubject + "\n",
		"\t" + testSubject,
		"c0000000-0000-0000-0000 000000000001", // space where a hyphen belongs
		"c0000000-0000-0000-0000-0000000000011",
		"urn:uuid:" + testSubject[:35],
		"urn:uuid " + testSubject,
		"c000000000000000000000000000000g", // 32 chars, non-hex
	}
	tenants := []struct {
		name string
		hdr  []string // nil: header absent
	}{
		{"tenant=absent", nil},
		{"tenant=empty", []string{""}},
		{"tenant=set", []string{testTenant}},
	}
	for _, subj := range subjects {
		for _, tenant := range tenants {
			t.Run(fmt.Sprintf("%q/%s", subj, tenant.name), func(t *testing.T) {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("X-User-ID", subj)
				req.Header.Set("X-User-Role", "authenticated")
				if tenant.hdr != nil {
					req.Header["X-Tenant-Id"] = tenant.hdr
				}
				id, idOK, caller, callerOK := contextKeys(req)
				if idOK {
					t.Errorf("IdentityFromContext = %+v, want none", id)
				}
				if callerOK {
					t.Errorf("TenantlessCallerFromContext = %+v, want none", caller)
				}
			})
		}
	}
}

// First value wins (Header.Get), so a second X-User-ID line cannot launder or poison the first.
func TestIdentityMiddleware_RepeatedUserHeaders(t *testing.T) {
	other := "c0000000-0000-0000-0000-000000000002"
	cases := []struct {
		name string
		user []string
		want string // "" = no identity
	}{
		{"uuid then junk", []string{testSubject, "user-42"}, testSubject},
		{"junk then uuid", []string{"user-42", testSubject}, ""},
		{"empty then uuid", []string{"", testSubject}, ""},
		{"uuid then uuid", []string{testSubject, other}, testSubject},
	}
	for _, tc := range cases {
		for _, withTenant := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/tenant=%v", tc.name, withTenant), func(t *testing.T) {
				req := httptest.NewRequest("GET", "/", nil)
				req.Header["X-User-Id"] = tc.user
				if withTenant {
					req.Header.Set("X-Tenant-ID", testTenant)
				}
				id, idOK, caller, callerOK := contextKeys(req)
				got, gotOK := caller, callerOK
				if withTenant {
					got, gotOK = id, idOK
				}
				if tc.want == "" {
					if idOK || callerOK {
						t.Errorf("identity %+v (%v) / caller %+v (%v), want neither", id, idOK, caller, callerOK)
					}
					return
				}
				if !gotOK || got.Subject != tc.want {
					t.Errorf("got %+v (ok %v), want Subject %q", got, gotOK, tc.want)
				}
			})
		}
	}
}

// D5: the middleware accepts exactly the subjects the verifier accepts. Both sides use uuid.Parse,
// so a stricter or looser middleware check diverges here.
func TestIdentityMiddleware_AcceptsExactlyWhatTheVerifierAccepts(t *testing.T) {
	iss, err := auth.NewMockIssuer("https://issuer.test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(iss.JWKSHandler())
	t.Cleanup(srv.Close)
	v, err := auth.NewVerifier(auth.Config{
		Issuer:   "https://issuer.test",
		JWKSURL:  srv.URL,
		CacheTTL: time.Hour,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	subjects := []string{
		testSubject,
		"00000000-0000-0000-0000-000000000000",
		"C0000000-0000-0000-0000-000000000001",
		"{c0000000-0000-0000-0000-000000000001}",
		"urn:uuid:c0000000-0000-0000-0000-000000000001",
		"URN:UUID:c0000000-0000-0000-0000-000000000001",
		"c0000000000000000000000000000001",
		"(c0000000-0000-0000-0000-000000000001)", // uuid.Parse examines only the middle 36 of 38 bytes
		" " + testSubject + " ",
		"user-42",
		"c0000000-0000-0000-0000-00000000000",
		"c0000000-0000-0000-0000-00000000000g",
		" " + testSubject,
		"c000000000000000000000000000000g",
	}
	accepted, refused := 0, 0
	for _, subj := range subjects {
		tok, err := iss.Mint(auth.MintOptions{Subject: subj, Role: "authenticated"})
		if err != nil {
			t.Fatalf("Mint %q: %v", subj, err)
		}
		_, verr := v.Verify(context.Background(), tok)

		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Tenant-ID", testTenant)
		req.Header.Set("X-User-ID", subj)
		_, idOK, _, _ := contextKeys(req)
		bare := httptest.NewRequest("GET", "/", nil)
		bare.Header.Set("X-User-ID", subj)
		_, _, _, callerOK := contextKeys(bare)

		if (verr == nil) != idOK || (verr == nil) != callerOK {
			t.Errorf("subject %q: verifier accepts = %v, middleware identity = %v, caller = %v", subj, verr == nil, idOK, callerOK)
		}
		if verr == nil {
			accepted++
		} else {
			refused++
		}
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("corpus must split both ways, got %d accepted and %d refused", accepted, refused)
	}
}

// uuid.Parse is the verifier's check (verify.go), so the middleware accepts the same forms.
func TestIdentityMiddleware_VerifierSubjectFormsBuildIdentity(t *testing.T) {
	forms := []struct{ name, subject string }{
		{"canonical", "c0000000-0000-0000-0000-000000000001"},
		{"upper-case", "C0000000-0000-0000-0000-000000000001"},
		{"braced", "{c0000000-0000-0000-0000-000000000001}"},
		{"urn", "urn:uuid:c0000000-0000-0000-0000-000000000001"},
		{"32-hex", "c0000000000000000000000000000001"},
		{"nil uuid", "00000000-0000-0000-0000-000000000000"},
	}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("X-Tenant-ID", testTenant)
			req.Header.Set("X-User-ID", f.subject)
			req.Header.Set("X-User-Role", "authenticated")

			id, idOK, _, callerOK := contextKeys(req)
			if !idOK {
				t.Fatalf("no identity for subject form %q", f.subject)
			}
			if id.Subject != f.subject || id.TenantID != testTenant {
				t.Errorf("identity = %+v, want Subject %q TenantID %q", id, f.subject, testTenant)
			}
			if callerOK {
				t.Error("tenant-less caller set beside an identity")
			}

			bare := httptest.NewRequest("POST", "/v1/workspaces", nil)
			bare.Header.Set("X-User-ID", f.subject)
			if _, _, caller, ok := contextKeys(bare); !ok || caller.Subject != f.subject {
				t.Errorf("tenant-less caller = %+v (ok %v), want Subject %q", caller, ok, f.subject)
			}
		})
	}
}

func TestIdentityMiddleware_ReadsTheStaffHeaders(t *testing.T) {
	cases := []struct {
		name                 string
		staff, rules         string // "" = header absent
		wantStaff, wantRules bool
	}{
		{"both true", "true", "true", true, true},
		{"staff only", "true", "", true, false},
		{"rules only", "", "true", false, false},
		{"upper case and one", "TRUE", "1", false, false},
		{"rules upper case", "true", "TRUE", true, false},
		{"neither", "", "", false, false},
	}
	for _, tc := range cases {
		for _, tenant := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/tenant=%v", tc.name, tenant), func(t *testing.T) {
				rig := newStaffRig(t, true)
				w := who{tenant: tenant, role: "authenticated", staff: tc.staff, rulesRole: tc.rules}
				if rec := rig.serve(w.request(http.MethodGet, "/v1/ping")); rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200", rec.Code)
				}
				c := rig.other
				got, ok := c.id, c.hasID
				if !tenant {
					got, ok = c.caller, c.hasCaller
				}
				if !ok || got.Subject != testSubject {
					t.Fatalf("caller = %+v (ok %v), want Subject %q", got, ok, testSubject)
				}
				if got.Staff != tc.wantStaff || got.RulesRole != tc.wantRules {
					t.Errorf("(Staff, RulesRole) = (%v, %v), want (%v, %v)", got.Staff, got.RulesRole, tc.wantStaff, tc.wantRules)
				}
			})
		}
	}
}

func TestIdentityMiddleware_TenantlessCallerCarriesStaff(t *testing.T) {
	rig := newStaffRig(t, true)
	rec := rig.serve(tenantlessSR.request(http.MethodGet, "/v1/staff/probe"))
	if rec.Code != http.StatusOK || rig.staff.count() != 1 {
		t.Fatalf("status %d, handler ran %d times, want 200 and 1 (body %q)", rec.Code, rig.staff.count(), rec.Body.String())
	}
	got := rig.staff.staff
	if !rig.staff.hasStaff || got.Subject != testSubject || got.TenantID != "" || !got.Staff || !got.RulesRole {
		t.Errorf("StaffFromContext = %+v (ok %v), want the tenant-less staff caller %q with Staff and RulesRole", got, rig.staff.hasStaff, testSubject)
	}
	if rig.staff.hasID {
		t.Errorf("IdentityFromContext = %+v, want none for a tenant-less caller", rig.staff.id)
	}
}

func TestIdentityMiddleware_NoGatewayTokenNoStaff(t *testing.T) {
	for _, w := range []who{staffRules, tenantlessSR} {
		t.Run(fmt.Sprintf("tenant=%v", w.tenant), func(t *testing.T) {
			rig := newStaffRig(t, false)
			if rec := rig.serve(w.request(http.MethodGet, "/v1/ping")); rec.Code != http.StatusOK {
				t.Fatalf("ping status = %d, want 200", rec.Code)
			}
			c := rig.other
			if !c.hasID && !c.hasCaller {
				t.Fatal("no caller was built from X-User-ID, so the Staff checks below prove nothing")
			}
			for name, id := range map[string]auth.Identity{"identity": c.id, "tenant-less caller": c.caller} {
				if id.Staff || id.RulesRole {
					t.Errorf("%s = %+v, want no Staff and no RulesRole without RequireGateway", name, id)
				}
			}

			rec := rig.serve(w.request(http.MethodGet, "/v1/staff/probe"))
			if rec.Code != http.StatusForbidden {
				t.Errorf("staff path status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
			}
			assertBody(t, rec, "staff path", forbiddenBody)
			if n := rig.staff.count(); n != 0 {
				t.Errorf("staff handler ran %d times, want 0", n)
			}
		})
	}

	// Control: the same request on an App that called RequireGateway reaches the handler.
	rig := newStaffRig(t, true)
	if rec := rig.serve(staffRules.request(http.MethodGet, "/v1/staff/probe")); rec.Code != http.StatusOK || rig.staff.count() != 1 {
		t.Errorf("control: status %d, handler ran %d times, want 200 and 1", rec.Code, rig.staff.count())
	}
}
