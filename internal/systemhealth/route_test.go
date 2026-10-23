package systemhealth

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/gateway"
	"github.com/SimonOsipov/invoice-os/internal/platform"
)

const (
	routeGatewayToken = "systemhealth-route-test-gateway-token"
	forbiddenBody     = `{"error":"forbidden"}`
	healthPath        = "/v1/staff/health"
	metricsPath       = "/v1/staff/metrics?range=1h"
)

// routePaths are the staff routes whose caller tables run once per path.
var routePaths = []struct{ name, path string }{{"health", healthPath}, {"metrics", metricsPath}}

func forEachRoute(t *testing.T, run func(t *testing.T, path string)) {
	t.Helper()
	for _, p := range routePaths {
		t.Run(p.name, func(t *testing.T) { run(t, p.path) })
	}
}

func routeMetrics() Metrics {
	return Metrics{Range: "1h", StepSeconds: 60, ConnectionBudget: ConnectionBudget, Panels: []PanelData{{Key: "route-test", Unit: "count", Available: true, Series: []Series{}}}}
}

func routeFleet() gateway.FleetHealth {
	up, down := true, false
	return gateway.FleetHealth{Status: "degraded", Services: []gateway.ServiceHealth{
		{Name: "gateway", Status: "up", Build: "541ac755", Sentry: "on", Contacts: "real", Ready: &up},
		{Name: "docling", Status: "down", Error: "connection refused", Build: "abcdef01", Ready: &down, NotReady: []string{"database"}},
	}}
}

type healthRig struct {
	h       http.Handler
	loads   atomic.Int32 // the database read or staff check ran
	sources atomic.Int32 // the metrics source ran
}

// newHealthRig serves the route through a gateway-guarded platform.App over the real store.
func newHealthRig(t *testing.T, store *Store, fleet gateway.FleetHealth) *healthRig {
	t.Helper()
	return newHealthRigWith(t, store, func(context.Context) (gateway.FleetHealth, error) { return fleet, nil }, time.Now)
}

func newHealthRigWith(t *testing.T, store *Store, fleet FleetReader, now func() time.Time) *healthRig {
	t.Helper()
	t.Setenv("SENTRY_DSN", "")
	app, err := platform.New("submission")
	if err != nil {
		t.Fatal(err)
	}
	rig := &healthRig{}
	app.Mux.HandleFunc("GET /v1/staff/health", Handler(func(ctx context.Context, since time.Time) (map[string]int64, []Sample, error) {
		rig.loads.Add(1)
		return store.StaffSnapshot(ctx, since)
	}, fleet, now, slog.Default()))
	app.Mux.HandleFunc("GET /v1/staff/metrics", MetricsHandler(func(ctx context.Context) error {
		rig.loads.Add(1)
		return store.StaffCheck(ctx)
	}, func(context.Context, string, string) (Metrics, error) {
		rig.sources.Add(1)
		return routeMetrics(), nil
	}, slog.Default()))
	app.RequireGateway(routeGatewayToken)
	rig.h = app.Handler()
	return rig
}

// healthCaller is one gateway-forwarded identity: no subject means no identity headers at all.
type healthCaller struct {
	subject, tenant string
	staff           bool
}

func (rig *healthRig) get(path string, c healthCaller) *httptest.ResponseRecorder {
	return rig.do(http.MethodGet, path, routeGatewayToken, c)
}

// do sends one request; an empty token sends no gateway token header.
func (rig *healthRig) do(method, path, token string, c healthCaller) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if token != "" {
		r.Header.Set(platform.HeaderGatewayToken, token)
	}
	if c.subject != "" {
		r.Header.Set("X-User-ID", c.subject)
		r.Header.Set("X-User-Role", "authenticated")
	}
	if c.tenant != "" {
		r.Header.Set("X-Tenant-ID", c.tenant)
	}
	if c.staff {
		r.Header.Set("X-User-Staff", "true")
	}
	rec := httptest.NewRecorder()
	rig.h.ServeHTTP(rec, r)
	return rec
}

func memberTenant(t *testing.T, super *pgxpool.Pool, subject string) string {
	t.Helper()
	var tenant string
	if err := super.QueryRow(context.Background(), `SELECT tenant_id::text FROM memberships WHERE user_id = $1`, subject).Scan(&tenant); err != nil {
		t.Fatalf("read the tenant of %s: %v", subject, err)
	}
	return tenant
}

func wantForbidden(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden || strings.TrimSpace(rec.Body.String()) != forbiddenBody {
		t.Errorf("got %d %q, want 403 %s", rec.Code, rec.Body.String(), forbiddenBody)
	}
}

func TestStaffHealthRoute_ReturnsFleetCountsAndDepth(t *testing.T) {
	super, app := dbTestPools(t)
	seedDeadLettered(t, super, 3)
	fleet := routeFleet()
	rig := newHealthRig(t, &Store{Pool: app}, fleet)
	wantRaw, _ := json.Marshal(fleet) // before serving: the handler shares the fake's slice

	rec := rig.get(healthPath, healthCaller{subject: houseMember(t, super), staff: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if got := sortedKeys(body); !slices.Equal(got, healthKeys) {
		t.Fatalf("keys = %v, want %v", got, healthKeys)
	}

	if got := rig.loads.Load(); got != 1 {
		t.Errorf("loader called %d times, want 1", got)
	}
	var gotFleet, wantFleet any
	if err := json.Unmarshal(body["fleet"], &gotFleet); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(wantRaw, &wantFleet)
	if !reflect.DeepEqual(gotFleet, wantFleet) {
		t.Errorf("fleet = %s, want the fake body field for field: %s", body["fleet"], wantRaw)
	}

	var jobs map[string]int64
	if err := json.Unmarshal(body["jobs"], &jobs); err != nil {
		t.Fatal(err)
	}
	assertFloor(t, jobs)
	for _, s := range JobStates {
		if _, ok := jobs[s]; !ok {
			t.Errorf("jobs lacks state %q: %v", s, jobs)
		}
	}
	var depth int64
	if err := json.Unmarshal(body["queue_depth"], &depth); err != nil {
		t.Fatal(err)
	}
	if want := jobs["queued"] + jobs["submitting"] + jobs["pending"]; depth != want {
		t.Errorf("queue_depth = %d, want queued+submitting+pending of the same body = %d", depth, want)
	}
	var checked time.Time
	if err := json.Unmarshal(body["checked_at"], &checked); err != nil || time.Since(checked) > time.Minute || time.Since(checked) < -time.Minute {
		t.Errorf("checked_at = %s (%v), want now", body["checked_at"], err)
	}
}

func TestStaffHealthRoute_TrendIsTheLastSevenDays(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
	now := time.Now().UTC().Truncate(time.Microsecond)
	jitter := time.Duration(rand.IntN(1_000_000)) * time.Microsecond
	tooOld, oldest, newest := now.Add(-TrendWindow-5*time.Minute-jitter), now.Add(-TrendWindow+5*time.Minute+jitter), now.Add(-5*time.Minute-jitter)
	for _, s := range []struct {
		at     time.Time
		status string
		down   *int
		counts string
	}{
		{newest, "unknown", nil, `{"dead_lettered":4}`},
		{tooOld, "ok", new(int), `{"queued":9}`},
		{oldest, "degraded", func() *int { n := 1; return &n }(), `{"queued":1,"pending":1}`},
	} {
		s := s
		if _, err := super.Exec(context.Background(),
			`INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, $2, $3, $4)`, s.at, s.status, s.down, s.counts); err != nil {
			t.Fatalf("seed %v: %v", s.at, err)
		}
		t.Cleanup(func() {
			_, _ = super.Exec(context.Background(), `DELETE FROM public.health_samples WHERE taken_at = $1`, s.at)
		})
	}

	rec := rig.get(healthPath, healthCaller{subject: houseMember(t, super), staff: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		Trend []struct {
			TakenAt      time.Time `json:"taken_at"`
			ServicesDown *int      `json:"services_down"`
			QueueDepth   int64     `json:"queue_depth"`
			DeadLettered int64     `json:"dead_lettered"`
		} `json:"trend"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	var mine []int
	for i, p := range body.Trend {
		if i > 0 && p.TakenAt.Before(body.Trend[i-1].TakenAt) {
			t.Fatalf("trend is not ascending at %d", i)
		}
		switch {
		case p.TakenAt.Equal(tooOld):
			t.Errorf("the sample older than 7 days is in the trend")
		case p.TakenAt.Equal(oldest), p.TakenAt.Equal(newest):
			mine = append(mine, i)
		}
	}
	if len(mine) != 2 || !body.Trend[mine[0]].TakenAt.Equal(oldest) || !body.Trend[mine[1]].TakenAt.Equal(newest) {
		t.Fatalf("own samples at %v of %d points, want [oldest newest] in that order", mine, len(body.Trend))
	}
	a, b := body.Trend[mine[0]], body.Trend[mine[1]]
	if a.ServicesDown == nil || *a.ServicesDown != 1 || a.QueueDepth != 2 || a.DeadLettered != 0 {
		t.Errorf("oldest kept sample = %+v, want down 1, depth 2, dead 0", a)
	}
	if b.ServicesDown != nil || b.QueueDepth != 0 || b.DeadLettered != 4 {
		t.Errorf("newest sample = %+v, want down null, depth 0, dead 4", b)
	}
}

// platform.App refuses these callers before the handler runs.
func TestStaffHealthRoute_RefusesEveryNonStaffCaller(t *testing.T) {
	super, app := dbTestPools(t)
	custSubject := customerMember(t, super)
	custTenant := memberTenant(t, super, custSubject)
	houseSubject := houseMember(t, super)
	callers := map[string]healthCaller{
		"customer":                    {subject: custSubject, tenant: custTenant},
		"tenant-less, not staff":      {subject: custSubject},
		"no identity":                 {},
		"house member, no staff flag": {subject: houseSubject},
	}

	forEachRoute(t, func(t *testing.T, path string) {
		rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
		for name, c := range callers {
			t.Run(name, func(t *testing.T) { wantForbidden(t, rig.get(path, c)) })
		}
		if loads, sources := rig.loads.Load(), rig.sources.Load(); loads != 0 || sources != 0 {
			t.Errorf("check ran %d time(s), metrics source %d, for non-staff callers, want 0 and 0", loads, sources)
		}
		// Control: a staff caller passes the platform and reaches the handler, so the 403s above are not a missing route.
		if rec := rig.get(path, healthCaller{subject: houseSubject, staff: true}); rec.Code != http.StatusOK || rig.loads.Load() != 1 {
			t.Fatalf("house member control = %d %q, loads %d, want 200 and 1", rec.Code, rec.Body.String(), rig.loads.Load())
		}
	})
}

// The staff header is a claim: the database function re-checks the subject.
func TestStaffHealthRoute_RefusesAForgedOrSuspendedStaffCaller(t *testing.T) {
	super, app := dbTestPools(t)
	custSubject := customerMember(t, super)
	custTenant := memberTenant(t, super, custSubject)
	house := adoptHouseTenant(t, super)
	suspended := seedMember(t, super, house, "suspended")
	invited := seedMember(t, super, house, "invited")
	callers := map[string]healthCaller{
		"customer-only member, tenant-bearing":   {subject: custSubject, tenant: custTenant, staff: true},
		"customer-only member, tenant-less":      {subject: custSubject, staff: true},
		"suspended house member":                 {subject: suspended, staff: true},
		"invited house member":                   {subject: invited, staff: true},
		"suspended house member, tenant-bearing": {subject: suspended, tenant: house, staff: true},
		"unknown subject":                        {subject: uuid.NewString(), staff: true},
	}

	forEachRoute(t, func(t *testing.T, path string) {
		rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
		if len(callers) == 0 {
			t.Fatal("no callers to refuse")
		}
		for name, c := range callers {
			t.Run(name, func(t *testing.T) { wantForbidden(t, rig.get(path, c)) })
		}
		if got := rig.loads.Load(); int(got) != len(callers) {
			t.Errorf("check ran %d times, want %d: the refusal must come from the database function, not the platform", got, len(callers))
		}
		if got := rig.sources.Load(); got != 0 {
			t.Errorf("metrics source ran %d times for refused callers, want 0", got)
		}
		if rec := rig.get(path, healthCaller{subject: houseMember(t, super), staff: true}); rec.Code != http.StatusOK {
			t.Errorf("house member control = %d %q, want 200", rec.Code, rec.Body.String())
		}
	})
}

func TestStaffHealthRoute_WritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	subject := houseMember(t, super)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := super.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	audits := func() int { return count(`SELECT count(*) FROM audit_log WHERE actor = $1`, subject) }
	samples := func() int { return count(`SELECT count(*) FROM public.health_samples`) }

	forEachRoute(t, func(t *testing.T, path string) {
		rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
		auditsBefore, samplesBefore := audits(), samples()
		for i := range 2 {
			if rec := rig.get(path, healthCaller{subject: subject, staff: true}); rec.Code != http.StatusOK {
				t.Fatalf("read %d = %d %q, want 200", i, rec.Code, rec.Body.String())
			}
		}
		if got := rig.loads.Load(); got != 2 {
			t.Fatalf("check ran %d times for 2 reads, want 2: the reads did not reach the database", got)
		}
		if got := audits(); got != auditsBefore {
			t.Errorf("audit_log rows for the caller: %d -> %d, want unchanged", auditsBefore, got)
		}
		if got := samples(); got != samplesBefore {
			t.Errorf("health_samples rows: %d -> %d, want unchanged", samplesBefore, got)
		}
	})
}

// The gateway token is the only door: a forged staff header from a house member's own subject still gets nothing.
func TestStaffHealthRoute_RefusesARequestNotFromTheGateway(t *testing.T) {
	super, app := dbTestPools(t)
	member := healthCaller{subject: houseMember(t, super), staff: true}

	forEachRoute(t, func(t *testing.T, path string) {
		rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
		for name, token := range map[string]string{"no token": "", "wrong token": routeGatewayToken + "x"} {
			t.Run(name, func(t *testing.T) {
				rec := rig.do(http.MethodGet, path, token, member)
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("got %d %q, want 401", rec.Code, rec.Body.String())
				}
			})
		}
		if loads, sources := rig.loads.Load(), rig.sources.Load(); loads != 0 || sources != 0 {
			t.Errorf("check ran %d time(s), metrics source %d, without the gateway token, want 0 and 0", loads, sources)
		}
		if rec := rig.get(path, member); rec.Code != http.StatusOK || rig.loads.Load() != 1 {
			t.Errorf("control with the token = %d %q, loads %d, want 200 and 1", rec.Code, rec.Body.String(), rig.loads.Load())
		}
	})
}

// A staff JWT carries the house tenant, so the tenant-bearing house member is the console's real caller.
func TestStaffHealthRoute_ATenantBearingHouseMemberIsServed(t *testing.T) {
	super, app := dbTestPools(t)
	house := adoptHouseTenant(t, super)
	wantMetrics, _ := json.Marshal(routeMetrics())

	forEachRoute(t, func(t *testing.T, path string) {
		callers := map[string]healthCaller{
			"tenant-less":  {subject: seedMember(t, super, house, "active"), staff: true},
			"house tenant": {subject: seedMember(t, super, house, "active"), tenant: house, staff: true},
		}
		rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
		for name, c := range callers {
			t.Run(name, func(t *testing.T) {
				rec := rig.get(path, c)
				if rec.Code != http.StatusOK {
					t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
				}
				if path == metricsPath && strings.TrimSpace(rec.Body.String()) != string(wantMetrics) {
					t.Errorf("body = %q, want the fake source's Metrics %s", rec.Body.String(), wantMetrics)
				}
			})
		}
		if got := int(rig.loads.Load()); got != len(callers) {
			t.Errorf("check ran %d times, want %d", got, len(callers))
		}
		if path == metricsPath && int(rig.sources.Load()) != len(callers) {
			t.Errorf("metrics source ran %d times, want %d", rig.sources.Load(), len(callers))
		}
	})
}

// Validation runs before the database check and after the platform: a refused caller never learns what is a valid query.
func TestStaffMetricsRoute_ABadQueryIs400OnlyForAStaffCallerAndNeverReadsTheDatabase(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
	const bad = "/v1/staff/metrics?range=2h"
	staff := healthCaller{subject: houseMember(t, super), staff: true}

	rec := rig.get(bad, staff)
	if rec.Code != http.StatusBadRequest || strings.TrimSpace(rec.Body.String()) != `{"error":"unknown range"}` {
		t.Errorf("staff caller, bad range = %d %q, want 400 unknown range", rec.Code, rec.Body.String())
	}
	if rec := rig.get("/v1/staff/metrics", staff); rec.Code != http.StatusBadRequest {
		t.Errorf("staff caller, no range = %d %q, want 400", rec.Code, rec.Body.String())
	}
	forged := healthCaller{subject: customerMember(t, super), staff: true}
	if rec := rig.get(bad, forged); rec.Code != http.StatusBadRequest {
		t.Errorf("forged staff caller, bad range = %d %q, want 400: validation runs before the database check", rec.Code, rec.Body.String())
	}
	if loads, sources := rig.loads.Load(), rig.sources.Load(); loads != 0 || sources != 0 {
		t.Fatalf("check ran %d time(s), source %d, for bad queries, want 0 and 0", loads, sources)
	}

	for name, c := range map[string]healthCaller{
		"customer":                    {subject: customerMember(t, super)},
		"house member, no staff flag": {subject: staff.subject},
		"no identity":                 {},
	} {
		t.Run(name, func(t *testing.T) { wantForbidden(t, rig.get(bad, c)) })
	}
	if rec := rig.do(http.MethodGet, bad, "", staff); rec.Code != http.StatusUnauthorized {
		t.Errorf("no gateway token, bad range = %d %q, want 401", rec.Code, rec.Body.String())
	}
	if loads := rig.loads.Load(); loads != 0 {
		t.Errorf("check ran %d time(s) for callers the platform refuses, want 0", loads)
	}

	// Control: the same staff caller with a valid range reaches the check and the source.
	rec = rig.get(metricsPath, staff)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rig.loads.Load() != 1 || rig.sources.Load() != 1 {
		t.Errorf("valid query = %d %q (%s), check %d, source %d, want 200 JSON and 1, 1", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"), rig.loads.Load(), rig.sources.Load())
	}
}

// Only GET is registered: another method gets 405 from the mux and no database or Google read.
func TestStaffMetricsRoute_OtherMethodsAreRefusedWithoutARead(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
	staff := healthCaller{subject: houseMember(t, super), staff: true}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(m, func(t *testing.T) {
			if rec := rig.do(m, metricsPath, routeGatewayToken, staff); rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s = %d %q, want 405", m, rec.Code, rec.Body.String())
			}
		})
	}
	if loads, sources := rig.loads.Load(), rig.sources.Load(); loads != 0 || sources != 0 {
		t.Errorf("check ran %d time(s), source %d, for other methods, want 0 and 0", loads, sources)
	}
	if rec := rig.get(metricsPath, staff); rec.Code != http.StatusOK {
		t.Errorf("GET control = %d %q, want 200", rec.Code, rec.Body.String())
	}
}

// A database fault in the check is a 500 with a fixed body, not a 403 and not a read.
func TestStaffMetricsRoute_ADatabaseFaultInTheCheckIs500(t *testing.T) {
	rig := newHealthRig(t, &Store{Pool: unreachablePool(t)}, routeFleet())
	rec := rig.get(metricsPath, healthCaller{subject: uuid.NewString(), staff: true})
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `{"error":"internal server error"}` {
		t.Errorf("got %d %q, want 500 with the fixed body", rec.Code, rec.Body.String())
	}
	if got := rig.loads.Load(); got != 1 {
		t.Errorf("check ran %d time(s), want 1", got)
	}
	if got := rig.sources.Load(); got != 0 {
		t.Errorf("metrics source ran %d time(s) after a check fault, want 0", got)
	}
}

// A real fleet reader against each way the gateway can fail: the database parts still arrive.
func TestStaffHealthRoute_AGatewayOutageStillServesCountsAndTrend(t *testing.T) {
	super, app := dbTestPools(t)
	seedDeadLettered(t, super, 3)
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-5*time.Minute - time.Duration(rand.IntN(1_000_000))*time.Microsecond)
	if _, err := super.Exec(context.Background(),
		`INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, 'ok', 0, '{"queued":2}')`, at); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM public.health_samples WHERE taken_at = $1`, at)
	})
	member := healthCaller{subject: houseMember(t, super), staff: true}

	serve := func(status int, body string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	closed := serve(200, "")
	closed.Close()
	servers := map[string]string{
		"connection refused": closed.URL,
		"500":                serve(http.StatusInternalServerError, `{"status":"ok","services":[]}`).URL,
		"200 not json":       serve(http.StatusOK, `<html>`).URL,
		"200 unknown status": serve(http.StatusOK, `{"status":"weird","services":[]}`).URL,
	}
	reader := func(base string) FleetReader {
		u, _ := url.Parse(base)
		return NewFleetReader(u, &http.Client{Timeout: 2 * time.Second}, "gw-token")
	}
	for name, base := range servers {
		t.Run(name, func(t *testing.T) {
			rec := newHealthRigWith(t, &Store{Pool: app}, reader(base), time.Now).get(healthPath, member)
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
			}
			var body struct {
				Fleet      json.RawMessage  `json:"fleet"`
				Jobs       map[string]int64 `json:"jobs"`
				QueueDepth int64            `json:"queue_depth"`
				Trend      []struct {
					TakenAt    time.Time `json:"taken_at"`
					QueueDepth int64     `json:"queue_depth"`
				} `json:"trend"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if string(body.Fleet) != `{"status":"unknown","services":[]}` {
				t.Errorf("fleet = %s, want the unknown roll-up", body.Fleet)
			}
			assertFloor(t, body.Jobs)
			if want := body.Jobs["queued"] + body.Jobs["submitting"] + body.Jobs["pending"]; body.QueueDepth != want {
				t.Errorf("queue_depth = %d, want %d", body.QueueDepth, want)
			}
			found := false
			for _, p := range body.Trend {
				if p.TakenAt.Equal(at) {
					found = p.QueueDepth == 2
				}
			}
			if !found {
				t.Errorf("the seeded sample is missing from the trend of %d points", len(body.Trend))
			}
		})
	}

	// Control: a degraded roll-up (503 with a body) is data, not an outage.
	degraded := serve(http.StatusServiceUnavailable, `{"status":"degraded","services":[{"name":"docling","status":"down"}]}`)
	rec := newHealthRigWith(t, &Store{Pool: app}, reader(degraded.URL), time.Now).get(healthPath, member)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"degraded"`) {
		t.Errorf("degraded control = %d %q, want 200 with status degraded", rec.Code, rec.Body.String())
	}
}

// The window is checked_at - 7 days, inclusive, to the microsecond.
func TestStaffHealthRoute_TrendWindowEdgeIsInclusiveToTheMicrosecond(t *testing.T) {
	super, app := dbTestPools(t)
	now := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Duration(rand.IntN(1_000_000)) * time.Microsecond)
	edge := now.Add(-TrendWindow)
	before, after := edge.Add(-time.Microsecond), edge.Add(time.Microsecond)
	for _, at := range []time.Time{before, edge, after} {
		at := at
		if _, err := super.Exec(context.Background(),
			`INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, 'ok', 0, '{}')`, at); err != nil {
			t.Fatalf("seed %v: %v", at, err)
		}
		t.Cleanup(func() {
			_, _ = super.Exec(context.Background(), `DELETE FROM public.health_samples WHERE taken_at = $1`, at)
		})
	}
	rig := newHealthRigWith(t, &Store{Pool: app}, func(context.Context) (gateway.FleetHealth, error) { return routeFleet(), nil }, func() time.Time { return now })
	rec := rig.get(healthPath, healthCaller{subject: houseMember(t, super), staff: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %q, want 200", rec.Code, rec.Body.String())
	}
	var body struct {
		Trend []struct {
			TakenAt time.Time `json:"taken_at"`
		} `json:"trend"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var mine []time.Time
	for _, p := range body.Trend {
		if p.TakenAt.Equal(before) || p.TakenAt.Equal(edge) || p.TakenAt.Equal(after) {
			mine = append(mine, p.TakenAt)
		}
	}
	if len(mine) != 2 || !mine[0].Equal(edge) || !mine[1].Equal(after) {
		t.Errorf("own samples in the trend = %v, want [edge edge+1us]: the sample 1us before the window is out, the one on the edge is in", mine)
	}
}
