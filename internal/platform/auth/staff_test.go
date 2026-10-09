package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const staffTenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

// serveRulesRole runs RequireRulesRole over ctx and reports the response, how often the handler ran, and
// the context the handler saw.
func serveRulesRole(ctx context.Context) (rec *httptest.ResponseRecorder, ran int, inner context.Context) {
	h := RequireRulesRole(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran++
		inner = r.Context()
		w.WriteHeader(http.StatusOK)
	}))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/staff/x", nil).WithContext(ctx))
	return rec, ran, inner
}

// callerContexts places id as a tenant caller and, with its tenant cleared, as a tenant-less caller.
func callerContexts(id Identity) map[string]context.Context {
	tenantless := id
	tenantless.TenantID = ""
	return map[string]context.Context{
		"tenant":      WithIdentity(context.Background(), id),
		"tenant-less": WithTenantlessCaller(context.Background(), tenantless),
	}
}

func TestRequireRulesRole_RefusesWithoutACaller(t *testing.T) {
	rec, ran, _ := serveRulesRole(context.Background())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unauthorized"}` {
		t.Errorf("body = %q, want {\"error\":\"unauthorized\"}", got)
	}
	if ran != 0 {
		t.Errorf("handler ran %d times, want 0", ran)
	}
}

func TestRequireRulesRole_RefusesEveryNonRulesCaller(t *testing.T) {
	ids := map[string]Identity{
		"customer":                     {Subject: testSubject, Role: "authenticated", TenantID: staffTenant},
		"customer admin":               {Subject: testSubject, Role: "admin", TenantID: staffTenant},
		"rules role without staff":     {Subject: testSubject, Role: "authenticated", TenantID: staffTenant, RulesRole: true},
		"staff without the rules role": {Subject: testSubject, Role: "authenticated", TenantID: staffTenant, Staff: true},
	}
	for name, id := range ids {
		for shape, ctx := range callerContexts(id) {
			t.Run(fmt.Sprintf("%s/%s", name, shape), func(t *testing.T) {
				rec, ran, _ := serveRulesRole(ctx)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403 (body %q)", rec.Code, rec.Body.String())
				}
				if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"forbidden"}` {
					t.Errorf("body = %q, want {\"error\":\"forbidden\"}", got)
				}
				if ran != 0 {
					t.Errorf("handler ran %d times, want 0", ran)
				}
			})
		}
	}
}

func TestRequireRulesRole_AdmitsAStaffRulesCaller(t *testing.T) {
	id := Identity{Subject: testSubject, Role: "authenticated", TenantID: staffTenant, Email: "ops@example.test", Staff: true, RulesRole: true}
	for shape, ctx := range callerContexts(id) {
		t.Run(shape, func(t *testing.T) {
			rec, ran, inner := serveRulesRole(ctx)
			if rec.Code != http.StatusOK || ran != 1 {
				t.Fatalf("status %d, handler ran %d times, want 200 and 1 (body %q)", rec.Code, ran, rec.Body.String())
			}
			got, ok := StaffFromContext(inner)
			if !ok || got.Subject != testSubject || !got.Staff || !got.RulesRole {
				t.Fatalf("StaffFromContext = %+v (ok %v), want the staff caller %q", got, ok, testSubject)
			}
			wantTenant := id.TenantID
			if shape == "tenant-less" {
				wantTenant = ""
			}
			if got.TenantID != wantTenant {
				t.Errorf("TenantID = %q, want %q", got.TenantID, wantTenant)
			}
		})
	}
}

func TestStaffFromContext_EmptyOutsideTheCheck(t *testing.T) {
	id := Identity{Subject: testSubject, Role: "authenticated", TenantID: staffTenant, Staff: true, RulesRole: true}
	for shape, ctx := range callerContexts(id) {
		t.Run(shape, func(t *testing.T) {
			if got, ok := StaffFromContext(ctx); ok {
				t.Errorf("StaffFromContext = %+v, want none without RequireRulesRole", got)
			}
		})
	}
	if _, ok := StaffFromContext(context.Background()); ok {
		t.Error("StaffFromContext reported a caller on an empty context")
	}

	// Control: the same caller through the check is reported, so the empty results above are the check's doing.
	_, _, inner := serveRulesRole(WithIdentity(context.Background(), id))
	if inner == nil {
		t.Fatal("control: the staff caller did not reach the handler")
	}
	if _, ok := StaffFromContext(inner); !ok {
		t.Error("control: StaffFromContext reported none behind RequireRulesRole")
	}
}
