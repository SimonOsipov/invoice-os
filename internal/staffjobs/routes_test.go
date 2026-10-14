package staffjobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

const routesToken = "staffjobs-routes-gateway-token"

type routesRig struct {
	h      http.Handler
	lists  atomic.Int32
	gets   atomic.Int32
	actors atomic.Value
}

func newRoutesRig(t *testing.T) *routesRig {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	app, err := platform.New("submission")
	if err != nil {
		t.Fatal(err)
	}
	rig := &routesRig{}
	app.Mux.HandleFunc("GET /v1/staff/jobs", ListHandler(func(ctx context.Context, _ *string, _ *uuid.UUID) ([]Job, error) {
		rig.lists.Add(1)
		id, _ := auth.StaffFromContext(ctx)
		rig.actors.Store(id.Subject)
		return nil, nil
	}, app.Logger))
	app.Mux.HandleFunc("GET /v1/staff/jobs/{id}", GetHandler(func(ctx context.Context, _ string) (Detail, error) {
		rig.gets.Add(1)
		return Detail{}, nil
	}, app.Logger))
	app.RequireGateway(routesToken)
	rig.h = app.Handler()
	return rig
}

func (rig *routesRig) do(path string, tenant, staff, anonymous bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set(platform.HeaderGatewayToken, routesToken)
	if !anonymous {
		r.Header.Set("X-User-ID", "11111111-1111-1111-1111-111111111111")
		r.Header.Set("X-User-Role", "authenticated")
		if tenant {
			r.Header.Set("X-Tenant-ID", uuid.NewString())
		}
		if staff {
			r.Header.Set("X-User-Staff", "true")
		}
	}
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, r)
	return rec
}

var jobPaths = []string{"/v1/staff/jobs", "/v1/staff/jobs?state=queued", "/v1/staff/jobs/" + "22222222-2222-2222-2222-222222222222"}

func TestStaffJobsRoutes_RefuseEveryNonStaffCaller(t *testing.T) {
	cases := map[string][3]bool{
		"customer":               {true, false, false},
		"tenant-less, not staff": {false, false, false},
		"no identity":            {false, false, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newRoutesRig(t)
			for _, p := range jobPaths {
				rec := rig.do(p, c[0], c[1], c[2])
				if rec.Code != http.StatusForbidden {
					t.Errorf("GET %s = %d (%s), want 403", p, rec.Code, rec.Body.String())
				}
			}
			if rig.lists.Load() != 0 || rig.gets.Load() != 0 {
				t.Errorf("handlers ran: list %d, get %d, want 0", rig.lists.Load(), rig.gets.Load())
			}
		})
	}
}

func TestStaffJobsRoutes_StaffReachesBothRoutes(t *testing.T) {
	rig := newRoutesRig(t)
	for _, p := range jobPaths {
		if rec := rig.do(p, true, true, false); rec.Code != http.StatusOK {
			t.Errorf("GET %s as staff = %d (%s), want 200", p, rec.Code, rec.Body.String())
		}
	}
	if rig.lists.Load() != 2 || rig.gets.Load() != 1 {
		t.Errorf("handler calls: list %d, get %d, want 2 and 1", rig.lists.Load(), rig.gets.Load())
	}
	if got, _ := rig.actors.Load().(string); got != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("StaffFromContext subject = %q", got)
	}
}
