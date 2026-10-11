// The real staff handlers behind a gateway-guarded platform.App: the platform, not the handler, refuses non-rules callers.
package validation

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const (
	routesGatewayToken = "routes-test-gateway-token"
	routesSubject      = "11111111-1111-1111-1111-111111111111"
	switchBody         = `{"enabled":false,"reason":"r"}`
	ruleBody           = `{"type":"required","severity":"error","message":"m","enabled":true}`
)

type routesRig struct {
	h        http.Handler
	lists    atomic.Int32
	versions atomic.Int32
	switches atomic.Int32
	drafts   atomic.Int32
	syncs    atomic.Int32
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
	app.Mux.HandleFunc("GET /v1/staff/rules", StaffListRulesHandler(func(ctx context.Context, _ *int) (InForceRules, error) {
		rig.lists.Add(1)
		id, _ := auth.StaffFromContext(ctx)
		rig.actor.Store(id.Subject)
		return InForceRules{Rules: []StaffRule{}}, nil
	}, nil))
	app.Mux.HandleFunc("GET /v1/staff/rule-versions", StaffVersionsHandler(func(context.Context) (VersionList, error) {
		rig.versions.Add(1)
		return VersionList{Versions: []StaffVersion{}}, nil
	}, nil))
	app.Mux.HandleFunc("PATCH /v1/staff/rules/{key}", StaffSwitchRuleHandler(func(ctx context.Context, key string, enabled bool, _ string) (SwitchResult, error) {
		rig.switches.Add(1)
		id, _ := auth.StaffFromContext(ctx)
		rig.actor.Store(id.Subject)
		return SwitchResult{Key: key, Enabled: enabled}, nil
	}, nil))
	count := func() { rig.drafts.Add(1) }
	log := slog.Default()
	app.Mux.HandleFunc("POST /v1/staff/rule-versions/draft", StaffOpenDraftHandler(func(context.Context) (DraftOpened, error) {
		count()
		return DraftOpened{}, nil
	}, log))
	app.Mux.HandleFunc("POST /v1/staff/rule-versions/draft/rules", StaffAddDraftRuleHandler(func(context.Context, string, validated) (DraftRuleResult, error) {
		count()
		return DraftRuleResult{}, nil
	}, log))
	app.Mux.HandleFunc("PUT /v1/staff/rule-versions/draft/rules/{key}", StaffEditDraftRuleHandler(func(context.Context, string, validated) (DraftRuleEdited, error) {
		count()
		return DraftRuleEdited{}, nil
	}, log))
	app.Mux.HandleFunc("DELETE /v1/staff/rule-versions/draft/rules/{key}", StaffRemoveDraftRuleHandler(func(context.Context, string) (DraftRuleResult, error) {
		count()
		return DraftRuleResult{}, nil
	}, log))
	app.Mux.HandleFunc("POST /v1/staff/rule-versions/draft/publish", StaffPublishDraftHandler(func(context.Context, time.Time) (DraftPublished, error) {
		count()
		return DraftPublished{}, nil
	}, log))
	app.Mux.HandleFunc("POST /v1/staff/rule-versions/draft/test", StaffTestDraftHandler(func(context.Context, map[string]any) (DraftTestResult, error) {
		count()
		return DraftTestResult{}, nil
	}, log))
	if bindCodeListSyncsHandler != nil {
		app.Mux.Handle("GET /v1/staff/code-list-syncs", bindCodeListSyncsHandler(func(*string) { rig.syncs.Add(1) }))
	}
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
				{"GET", "/v1/staff/rules?version=4", ""},
				{"GET", "/v1/staff/rule-versions", ""},
				{"PATCH", "/v1/staff/rules/vat-standard-rate", switchBody},
				{"POST", "/v1/staff/rule-versions/draft", ""},
				{"POST", "/v1/staff/rule-versions/draft/rules", `{"key":"k",` + ruleBody[1:]},
				{"PUT", "/v1/staff/rule-versions/draft/rules/k", ruleBody},
				{"DELETE", "/v1/staff/rule-versions/draft/rules/k", ""},
				{"POST", "/v1/staff/rule-versions/draft/publish", `{"effective_from":"3001-01-01"}`},
				{"POST", "/v1/staff/rule-versions/draft/test", `{"invoice":{}}`},
				{"GET", "/v1/staff/code-list-syncs", ""},
				{"GET", "/v1/staff/code-list-syncs?list=hs-codes", ""},
			} {
				rec := rig.do(c, req.method, req.path, req.body)
				if rec.Code != 403 {
					t.Errorf("%s %s = %d (%s), want 403", req.method, req.path, rec.Code, rec.Body.String())
				}
			}
			if rig.lists.Load() != 0 || rig.switches.Load() != 0 || rig.versions.Load() != 0 || rig.drafts.Load() != 0 || rig.syncs.Load() != 0 {
				t.Errorf("handlers ran: list %d, switch %d, versions %d, drafts %d, syncs %d, want 0", rig.lists.Load(), rig.switches.Load(), rig.versions.Load(), rig.drafts.Load(), rig.syncs.Load())
			}
		})
	}

	t.Run("control: rules-role staff reaches both handlers", func(t *testing.T) {
		if bindCodeListSyncsHandler == nil {
			t.Fatal("StaffCodeListSyncsHandler does not exist: bindCodeListSyncsHandler is not assigned")
		}
		rig := newRoutesRig(t)
		for _, c := range []caller{{tenant: true, staff: true, rulesRole: true}, {staff: true, rulesRole: true}} {
			if rec := rig.do(c, "GET", "/v1/staff/rules", ""); rec.Code != 200 {
				t.Errorf("GET = %d (%s), want 200", rec.Code, rec.Body.String())
			}
			for _, path := range []string{"/v1/staff/rules?version=4", "/v1/staff/rule-versions", "/v1/staff/code-list-syncs", "/v1/staff/code-list-syncs?list=hs-codes"} {
				if rec := rig.do(c, "GET", path, ""); rec.Code != 200 {
					t.Errorf("GET %s = %d (%s), want 200", path, rec.Code, rec.Body.String())
				}
			}
			if rec := rig.do(c, "PATCH", "/v1/staff/rules/vat-standard-rate", switchBody); rec.Code != 200 {
				t.Errorf("PATCH = %d (%s), want 200", rec.Code, rec.Body.String())
			}
			for _, req := range []struct{ method, path, body string }{
				{"POST", "/v1/staff/rule-versions/draft", ""},
				{"POST", "/v1/staff/rule-versions/draft/rules", `{"key":"k",` + ruleBody[1:]},
				{"PUT", "/v1/staff/rule-versions/draft/rules/k", ruleBody},
				{"DELETE", "/v1/staff/rule-versions/draft/rules/k", ""},
				{"POST", "/v1/staff/rule-versions/draft/publish", `{"effective_from":"3001-01-01"}`},
				{"POST", "/v1/staff/rule-versions/draft/test", `{"invoice":{}}`},
			} {
				if rec := rig.do(c, req.method, req.path, req.body); rec.Code/100 != 2 {
					t.Errorf("%s %s = %d (%s), want 2xx", req.method, req.path, rec.Code, rec.Body.String())
				}
			}
		}
		if rig.lists.Load() != 4 || rig.switches.Load() != 2 || rig.versions.Load() != 2 || rig.drafts.Load() != 12 || rig.syncs.Load() != 4 {
			t.Errorf("handler calls: list %d, switch %d, versions %d, drafts %d, syncs %d, want 4, 2, 2, 12 and 4", rig.lists.Load(), rig.switches.Load(), rig.versions.Load(), rig.drafts.Load(), rig.syncs.Load())
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
