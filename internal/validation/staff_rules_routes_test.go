// The real staff handlers behind a gateway-guarded platform.App: the platform, not the handler, refuses non-rules callers.
package validation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	routesGatewayToken = "routes-test-gateway-token"
	routesSubject      = "11111111-1111-1111-1111-111111111111"
	switchBody         = `{"enabled":false,"reason":"r"}`
)

type routesRig struct {
	h        http.Handler
	lists    atomic.Int32
	switches atomic.Int32
	actor    atomic.Value // Subject that StaffFromContext reported
}

func newRoutesRig(t *testing.T) *routesRig {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	app, err := platform.New("validation")
	if err != nil {
		t.Fatal(err)
	}
	rig := &routesRig{}
	app.Mux.HandleFunc("GET /v1/staff/rules", StaffListRulesHandler(func(ctx context.Context) (InForceRules, error) {
		rig.lists.Add(1)
		id, _ := auth.StaffFromContext(ctx)
		rig.actor.Store(id.Subject)
		return InForceRules{Rules: []StaffRule{}}, nil
	}, nil))
	app.Mux.HandleFunc("PATCH /v1/staff/rules/{key}", StaffSwitchRuleHandler(func(ctx context.Context, key string, enabled bool, _ string) (SwitchResult, error) {
		rig.switches.Add(1)
		id, _ := auth.StaffFromContext(ctx)
		rig.actor.Store(id.Subject)
		return SwitchResult{Key: key, Enabled: enabled}, nil
	}, nil))
	app.Mux.HandleFunc("PATCH /v1/rules/{key}", ToggleHandler())
	app.RequireGateway(routesGatewayToken)
	rig.h = app.Handler()
	return rig
}

type caller struct {
	tenant, staff, rulesRole, anonymous bool
}

func (rig *routesRig) do(c caller, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(platform.HeaderGatewayToken, routesGatewayToken)
	if !c.anonymous {
		r.Header.Set("X-User-ID", routesSubject)
		r.Header.Set("X-User-Role", "authenticated")
		if c.tenant {
			r.Header.Set("X-Tenant-ID", uuid.NewString())
		}
		if c.staff {
			r.Header.Set("X-User-Staff", "true")
		}
		if c.rulesRole {
			r.Header.Set("X-User-Rules-Role", "true")
		}
	}
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, r)
	return rec
}

func TestStaffRulesRoutes_RefuseEveryNonRulesCaller(t *testing.T) {
	for name, c := range map[string]caller{
		"customer":                   {tenant: true},
		"staff without rules role":   {tenant: true, staff: true},
		"tenant-less staff, no role": {staff: true},
		"rules header without staff": {tenant: true, rulesRole: true},
		"no identity":                {anonymous: true},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newRoutesRig(t)
			for _, req := range []struct{ method, path, body string }{
				{"GET", "/v1/staff/rules", ""},
				{"PATCH", "/v1/staff/rules/vat-standard-rate", switchBody},
			} {
				rec := rig.do(c, req.method, req.path, req.body)
				if rec.Code != 403 {
					t.Errorf("%s %s = %d (%s), want 403", req.method, req.path, rec.Code, rec.Body.String())
				}
			}
			if rig.lists.Load() != 0 || rig.switches.Load() != 0 {
				t.Errorf("handlers ran: list %d, switch %d, want 0", rig.lists.Load(), rig.switches.Load())
			}
		})
	}

	t.Run("control: rules-role staff reaches both handlers", func(t *testing.T) {
		rig := newRoutesRig(t)
		for _, c := range []caller{{tenant: true, staff: true, rulesRole: true}, {staff: true, rulesRole: true}} {
			if rec := rig.do(c, "GET", "/v1/staff/rules", ""); rec.Code != 200 {
				t.Errorf("GET = %d (%s), want 200", rec.Code, rec.Body.String())
			}
			if rec := rig.do(c, "PATCH", "/v1/staff/rules/vat-standard-rate", switchBody); rec.Code != 200 {
				t.Errorf("PATCH = %d (%s), want 200", rec.Code, rec.Body.String())
			}
		}
		if rig.lists.Load() != 2 || rig.switches.Load() != 2 {
			t.Errorf("handler calls: list %d, switch %d, want 2 and 2", rig.lists.Load(), rig.switches.Load())
		}
		if got, _ := rig.actor.Load().(string); got != routesSubject {
			t.Errorf("StaffFromContext subject = %q, want %q", got, routesSubject)
		}
	})
}

func TestStaffRulesRoutes_ToggleHandlerStaysForStaffToo(t *testing.T) {
	rig := newRoutesRig(t)
	rec := rig.do(caller{tenant: true, staff: true, rulesRole: true}, "PATCH", "/v1/rules/vat-standard-rate", switchBody)
	if rec.Code != 403 || strings.TrimSpace(rec.Body.String()) != `{"error":"`+rulesManagedMessage+`"}` {
		t.Fatalf("got %d %q, want 403 %q", rec.Code, rec.Body.String(), rulesManagedMessage)
	}
}
