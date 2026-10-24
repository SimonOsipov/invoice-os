package systemhealth

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const devHouseTenant = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"

func dbTestPools(t *testing.T) (super, app *pgxpool.Pool) {
	t.Helper()
	appURL, superURL := os.Getenv("DATABASE_URL"), os.Getenv("DATABASE_SUPERUSER_URL")
	if appURL == "" || superURL == "" {
		t.Skip("systemhealth db test skipped: set DATABASE_URL and DATABASE_SUPERUSER_URL (or run `make test-rls`)")
	}
	ctx := context.Background()
	super, err := pgxpool.New(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(super.Close)
	app, err = pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatalf("connect app: %v", err)
	}
	t.Cleanup(app.Close)
	if err := app.Ping(ctx); err != nil {
		t.Fatalf("ping app: %v", err)
	}
	return super, app
}

// unreachablePool never connects, so any statement on it fails.
func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), "postgres://x:y@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// testSlot returns a slot far from now, in an era per test so retention cannot touch another test's rows; the row is deleted on cleanup.
func testSlot(t *testing.T, super *pgxpool.Pool, era int, offsetSlots int) time.Time {
	t.Helper()
	slot := time.Date(era, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rand.IntN(1000)+offsetSlots) * SampleInterval)
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM public.health_samples WHERE taken_at = $1`, slot)
	})
	return slot
}

func seedDeadLettered(t *testing.T, super *pgxpool.Pool, n int) {
	seedJobs(t, super, "dead_lettered", n)
}

// seedJobs seeds n jobs in the given state in a fresh tenant of their own.
func seedJobs(t *testing.T, super *pgxpool.Pool, state string, n int) {
	t.Helper()
	ctx := context.Background()
	tenant := uuid.NewString()
	if _, err := super.Exec(ctx, `INSERT INTO tenants (id, name, kind) VALUES ($1, 'systemhealth seed', 'firm')`, tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	var entity string
	if err := super.QueryRow(ctx, `INSERT INTO business_entities (tenant_id, name) VALUES ($1, 'e') RETURNING id`, tenant).Scan(&entity); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = super.Exec(c, `DELETE FROM submission_jobs WHERE tenant_id = $1`, tenant)
		_, _ = super.Exec(c, `DELETE FROM invoices WHERE tenant_id = $1`, tenant)
		_, _ = super.Exec(c, `DELETE FROM business_entities WHERE tenant_id = $1`, tenant)
		_, _ = super.Exec(c, `DELETE FROM tenants WHERE id = $1`, tenant)
	})
	for i := 0; i < n; i++ {
		var inv string
		if err := super.QueryRow(ctx, `INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, $3) RETURNING id`,
			tenant, entity, "SH-"+uuid.NewString()[:8]).Scan(&inv); err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
		if _, err := super.Exec(ctx, `INSERT INTO submission_jobs (tenant_id, invoice_id, idempotency_key, adapter, adapter_version, state)
			VALUES ($1, $2, $3, 'mock', 'v1', $4)`, tenant, inv, uuid.NewString(), state); err != nil {
			t.Fatalf("seed job: %v", err)
		}
	}
}

func adoptHouseTenant(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	read := func() (string, error) {
		var id string
		err := super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&id)
		return id, err
	}
	id, err := read()
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'ASComply (dev)') ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the dev house tenant: %v", err)
		}
		if _, err := super.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the house row: %v", err)
		}
		id, err = read()
	}
	if err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	return id
}

func seedMember(t *testing.T, super *pgxpool.Pool, tenant, status string) string {
	t.Helper()
	subject := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', $3)`, tenant, subject, status); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenant, subject)
	})
	return subject
}

func houseMember(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	return seedMember(t, super, adoptHouseTenant(t, super), "active")
}

func customerMember(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	tenant := uuid.NewString()
	if _, err := super.Exec(context.Background(), `INSERT INTO tenants (id, name, kind) VALUES ($1, 'systemhealth customer', 'firm')`, tenant); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant) })
	return seedMember(t, super, tenant, "active")
}

// staffCtx returns the context auth.RequireStaff builds for a staff caller: the key is private to auth.
func staffCtx(subject string) context.Context {
	var got context.Context
	h := auth.RequireStaff(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Context() }))
	ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", Staff: true})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/staff/health", nil).WithContext(ctx))
	return got
}

type row struct {
	status string
	down   *int
	counts map[string]int64
}

func readRow(t *testing.T, super *pgxpool.Pool, slot time.Time) (row, bool) {
	t.Helper()
	var r row
	err := super.QueryRow(context.Background(),
		`SELECT fleet_status, services_down, job_counts FROM public.health_samples WHERE taken_at = $1`, slot).Scan(&r.status, &r.down, &r.counts)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false
	}
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	return r, true
}

func assertFloor(t *testing.T, counts map[string]int64) {
	t.Helper()
	for _, s := range JobStates {
		if _, ok := counts[s]; !ok && s == "dead_lettered" {
			t.Errorf("state %q missing from %v", s, counts)
		}
	}
	for k, v := range counts {
		if v < 0 {
			t.Errorf("count %q = %d", k, v)
		}
	}
	if counts["dead_lettered"] < 3 {
		t.Errorf("dead_lettered = %d, want >= 3", counts["dead_lettered"])
	}
}

func TestStore_RecordWritesTheCountsOnce(t *testing.T) {
	super, app := dbTestPools(t)
	seedDeadLettered(t, super, 3)
	seedJobs(t, super, "cancelled", 2)
	s := &Store{Pool: app}
	slot := testSlot(t, super, 2001, 0)
	zero := 0
	two := 2

	if written, err := s.Record(context.Background(), slot, "ok", &zero); err != nil || !written {
		t.Fatalf("first Record = %v, %v; want true, nil", written, err)
	}
	if written, err := s.Record(context.Background(), slot, "degraded", &two); err != nil || written {
		t.Fatalf("second Record = %v, %v; want false, nil", written, err)
	}
	r, ok := readRow(t, super, slot)
	if !ok || r.status != "ok" || r.down == nil || *r.down != 0 {
		t.Fatalf("row = %+v (found %v), want ok,0", r, ok)
	}
	assertFloor(t, r.counts)
	if r.counts["cancelled"] < 2 {
		t.Errorf("stored cancelled = %d, want >= 2 (the sampler counts the state, it does not zero-fill it)", r.counts["cancelled"])
	}
}

// seedRiverJobs seeds n river_job rows of one kind and state on a queue no worker reads, deleted on cleanup.
func seedRiverJobs(t *testing.T, super *pgxpool.Pool, kind, state string, n int) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM public.river_job WHERE kind = $1`, kind) })
	for range n {
		if _, err := super.Exec(ctx, `INSERT INTO public.river_job (kind, queue, state, max_attempts, args, finalized_at)
			VALUES ($1, 'systemhealth_test', $2::text::river_job_state, 25, '{}', CASE WHEN $2::text IN ('cancelled', 'completed', 'discarded') THEN now() END)`, kind, state); err != nil {
			t.Fatalf("seed river_job %s/%s: %v", kind, state, err)
		}
	}
}

func TestStore_RecordWritesKindCounts(t *testing.T) {
	super, app := dbTestPools(t)
	kind := "shtest_" + uuid.NewString()[:8]
	seedRiverJobs(t, super, kind, "available", 1)
	seedRiverJobs(t, super, kind, "running", 2)
	seedRiverJobs(t, super, kind, "discarded", 1)
	slot := testSlot(t, super, 2004, 0)
	zero := 0
	if _, err := (&Store{Pool: app}).Record(context.Background(), slot, "ok", &zero); err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]int64
	if err := super.QueryRow(context.Background(), `SELECT kind_counts FROM public.health_samples WHERE taken_at = $1`, slot).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := (map[string]int64{"available": 1, "running": 2, "discarded": 1}); !reflect.DeepEqual(got[kind], want) {
		t.Errorf("kind_counts[%s] = %v, want %v", kind, got[kind], want)
	}
}

func TestStore_StaffSnapshotReadsRiverKindsAndKindCounts(t *testing.T) {
	super, app := dbTestPools(t)
	kind := "shtest_" + uuid.NewString()[:8]
	seedRiverJobs(t, super, kind, "available", 3)
	base := testSlot(t, super, 2005, 0)
	old := base.Add(SampleInterval)
	if _, err := super.Exec(context.Background(),
		`INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, 'ok', 0, '{}')`, old); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM public.health_samples WHERE taken_at = $1`, old)
	})
	zero := 0
	st := &Store{Pool: app}
	if _, err := st.Record(context.Background(), base, "ok", &zero); err != nil {
		t.Fatal(err)
	}
	live, samples, err := st.StaffSnapshot(staffCtx(houseMember(t, super)), base)
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[string]int64{"available": 3}); !reflect.DeepEqual(live.Kinds[kind], want) {
		t.Errorf("Live.Kinds[%s] = %v, want %v", kind, live.Kinds[kind], want)
	}
	var withKinds, without *Sample
	for i := range samples {
		switch {
		case samples[i].TakenAt.Equal(base):
			withKinds = &samples[i]
		case samples[i].TakenAt.Equal(old):
			without = &samples[i]
		}
	}
	if withKinds == nil || without == nil {
		t.Fatalf("own samples missing from %d rows: %v %v", len(samples), withKinds, without)
	}
	if want := (map[string]int64{"available": 3}); !reflect.DeepEqual(withKinds.KindCounts[kind], want) {
		t.Errorf("sample KindCounts[%s] = %v, want %v", kind, withKinds.KindCounts[kind], want)
	}
	if without.KindCounts != nil {
		t.Errorf("a sample without kind_counts reads %v, want nil", without.KindCounts)
	}
}

func TestStore_RecordDeletesSamplesOlderThanTheWindow(t *testing.T) {
	super, app := dbTestPools(t)
	s := &Store{Pool: app}
	slot := testSlot(t, super, 2002, 0)
	week := 7 * 24 * time.Hour
	old, edge := slot.Add(-week-5*time.Minute), slot.Add(-week)
	zero := 0
	for _, at := range []time.Time{old, edge} {
		at := at
		if _, err := super.Exec(context.Background(),
			`INSERT INTO public.health_samples (taken_at, fleet_status, services_down, job_counts) VALUES ($1, 'ok', 0, '{}')`, at); err != nil {
			t.Fatalf("seed %v: %v", at, err)
		}
		t.Cleanup(func() {
			_, _ = super.Exec(context.Background(), `DELETE FROM public.health_samples WHERE taken_at = $1`, at)
		})
	}
	if _, err := s.Record(context.Background(), slot, "ok", &zero); err != nil {
		t.Fatal(err)
	}
	if _, ok := readRow(t, super, old); ok {
		t.Error("sample older than the window survived")
	}
	if _, ok := readRow(t, super, edge); !ok {
		t.Error("sample at exactly slot - 7d was deleted")
	}
	if _, ok := readRow(t, super, slot); !ok {
		t.Error("the new sample is missing")
	}
}

func TestStore_StaffSnapshotRefusesANonStaffCaller(t *testing.T) {
	if _, _, err := (&Store{Pool: unreachablePool(t)}).StaffSnapshot(context.Background(), time.Now()); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("plain context: err = %v, want ErrNotStaff before any statement", err)
	}
	if _, _, err := (&Store{Pool: unreachablePool(t)}).StaffSnapshot(staffCtx("not-a-uuid"), time.Now()); !errors.Is(err, db.ErrNotStaff) {
		t.Errorf("non-uuid subject: err = %v, want ErrNotStaff before any statement", err)
	}
	super, app := dbTestPools(t)
	house := adoptHouseTenant(t, super)
	for name, subject := range map[string]string{
		"customer member":        customerMember(t, super),
		"invited house member":   seedMember(t, super, house, "invited"),
		"suspended house member": seedMember(t, super, house, "suspended"),
		"unknown subject":        uuid.NewString(),
	} {
		if _, _, err := (&Store{Pool: app}).StaffSnapshot(staffCtx(subject), time.Now()); !errors.Is(err, db.ErrNotStaff) {
			t.Errorf("%s: err = %v, want ErrNotStaff", name, err)
		}
	}
}

func TestStore_StaffSnapshotReadsCountsAndWindow(t *testing.T) {
	super, app := dbTestPools(t)
	seedDeadLettered(t, super, 3)
	seedJobs(t, super, "cancelled", 2)
	base := testSlot(t, super, 2003, 0)
	s0, s1, s2 := base, base.Add(SampleInterval), base.Add(2*SampleInterval)
	for _, at := range []time.Time{s1, s2} {
		at := at
		t.Cleanup(func() {
			_, _ = super.Exec(context.Background(), `DELETE FROM public.health_samples WHERE taken_at = $1`, at)
		})
	}
	zero := 0
	st := &Store{Pool: app}
	// recorded out of order and with a different status per slot, so order and columns are observable
	for _, r := range []struct {
		at     time.Time
		status string
		down   *int
	}{{s2, "unknown", nil}, {s0, "ok", &zero}, {s1, "ok", &zero}} {
		if _, err := st.Record(context.Background(), r.at, r.status, r.down); err != nil {
			t.Fatal(err)
		}
	}
	ctx := staffCtx(houseMember(t, super))
	live, samples, err := st.StaffSnapshot(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	jobs := live.Jobs
	for _, state := range JobStates {
		if _, ok := Counts(jobs)[state]; !ok {
			t.Errorf("state %q missing", state)
		}
	}
	assertFloor(t, Counts(jobs))
	if jobs["cancelled"] < 2 {
		t.Errorf("staff live cancelled = %d, want >= 2", jobs["cancelled"])
	}
	var mine []Sample
	for i, sm := range samples {
		if i > 0 && sm.TakenAt.Before(samples[i-1].TakenAt) {
			t.Fatalf("samples not ascending at %d", i)
		}
		if !sm.TakenAt.Before(base) && !sm.TakenAt.After(s2) {
			mine = append(mine, sm)
		}
	}
	if len(mine) != 2 || !mine[0].TakenAt.Equal(s1) || !mine[1].TakenAt.Equal(s2) {
		t.Fatalf("own slots in result = %v, want [%v %v]", mine, s1, s2)
	}
	if mine[0].FleetStatus != "ok" || mine[0].ServicesDown == nil || *mine[0].ServicesDown != 0 {
		t.Errorf("sample s1 = %+v, want ok,0", mine[0])
	}
	if mine[1].FleetStatus != "unknown" || mine[1].ServicesDown != nil {
		t.Errorf("sample s2 = %+v, want unknown,NULL", mine[1])
	}
	assertFloor(t, mine[0].JobCounts)
	if mine[0].JobCounts["cancelled"] < 2 {
		t.Errorf("staff sample cancelled = %d, want >= 2", mine[0].JobCounts["cancelled"])
	}
}
