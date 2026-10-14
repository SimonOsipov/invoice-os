package systemhealth

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
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
)

func routeFleet() gateway.FleetHealth {
	up, down := true, false
	return gateway.FleetHealth{Status: "degraded", Services: []gateway.ServiceHealth{
		{Name: "gateway", Status: "up", Build: "541ac755", Sentry: "on", Contacts: "real", Ready: &up},
		{Name: "docling", Status: "down", Error: "connection refused", Build: "abcdef01", Ready: &down, NotReady: []string{"database"}},
	}}
}

type healthRig struct {
	h     http.Handler
	loads atomic.Int32
}

// newHealthRig serves the route through a gateway-guarded platform.App over the real store.
func newHealthRig(t *testing.T, store *Store, fleet gateway.FleetHealth) *healthRig {
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
	}, func(context.Context) (gateway.FleetHealth, error) { return fleet, nil }, time.Now, slog.Default()))
	app.RequireGateway(routeGatewayToken)
	rig.h = app.Handler()
	return rig
}

// healthCaller is one gateway-forwarded identity: no subject means no identity headers at all.
type healthCaller struct {
	subject, tenant string
	staff           bool
}

func (rig *healthRig) get(c healthCaller) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/staff/health", nil)
	r.Header.Set(platform.HeaderGatewayToken, routeGatewayToken)
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

	rec := rig.get(healthCaller{subject: houseMember(t, super), staff: true})
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
	wantRaw, _ := json.Marshal(fleet)
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

	rec := rig.get(healthCaller{subject: houseMember(t, super), staff: true})
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

// Regression pin, green at head: platform.App refuses these callers before the handler runs.
func TestStaffHealthRoute_RefusesEveryNonStaffCaller(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
	custSubject := customerMember(t, super)
	custTenant := memberTenant(t, super, custSubject)
	houseSubject := houseMember(t, super)

	for name, c := range map[string]healthCaller{
		"customer":                    {subject: custSubject, tenant: custTenant},
		"tenant-less, not staff":      {subject: custSubject},
		"no identity":                 {},
		"house member, no staff flag": {subject: houseSubject},
	} {
		t.Run(name, func(t *testing.T) { wantForbidden(t, rig.get(c)) })
	}
	if got := rig.loads.Load(); got != 0 {
		t.Errorf("loader called %d times for non-staff callers, want 0", got)
	}
	// Control: a staff caller passes the platform, so the 403s above are not a missing route. The 200 is asserted in ReturnsFleetCountsAndDepth.
	if rec := rig.get(healthCaller{subject: houseSubject, staff: true}); rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound || rec.Code == http.StatusUnauthorized {
		t.Fatalf("house member control = %d %q, want it past the platform guard", rec.Code, rec.Body.String())
	}
}

// The staff header is a claim: the database function re-checks the subject.
func TestStaffHealthRoute_RefusesAForgedOrSuspendedStaffCaller(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
	custSubject := customerMember(t, super)
	custTenant := memberTenant(t, super, custSubject)
	suspended := seedMember(t, super, adoptHouseTenant(t, super), "suspended")

	for name, c := range map[string]healthCaller{
		"customer-only member, tenant-bearing": {subject: custSubject, tenant: custTenant, staff: true},
		"customer-only member, tenant-less":    {subject: custSubject, staff: true},
		"suspended house member":               {subject: suspended, staff: true},
		"unknown subject":                      {subject: uuid.NewString(), staff: true},
	} {
		t.Run(name, func(t *testing.T) { wantForbidden(t, rig.get(c)) })
	}
	if got := rig.loads.Load(); got != 4 {
		t.Errorf("loader called %d times, want 4: the refusal must come from the database function, not the platform", got)
	}
	if rec := rig.get(healthCaller{subject: houseMember(t, super), staff: true}); rec.Code != http.StatusOK {
		t.Errorf("house member control = %d %q, want 200", rec.Code, rec.Body.String())
	}
}

func TestStaffHealthRoute_WritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	rig := newHealthRig(t, &Store{Pool: app}, routeFleet())
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
	auditsBefore, samplesBefore := audits(), samples()

	for i := range 2 {
		if rec := rig.get(healthCaller{subject: subject, staff: true}); rec.Code != http.StatusOK {
			t.Fatalf("read %d = %d %q, want 200", i, rec.Code, rec.Body.String())
		}
	}
	if got := audits(); got != auditsBefore {
		t.Errorf("audit_log rows for the caller: %d -> %d, want unchanged", auditsBefore, got)
	}
	if got := samples(); got != samplesBefore {
		t.Errorf("health_samples rows: %d -> %d, want unchanged", samplesBefore, got)
	}
}
