package invoice

// Rechecker.RunOnce: orchestration over replaced func fields, plus one DB-backed case with the
// real functions. No t.Parallel (shared DB); CI runs this package unfiltered in the rls job.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type fakeRecheck struct {
	dues      []DueVersion
	tenants   []string
	failing   map[string]bool // "version/tenant" -> fails
	todays    []time.Time
	calls     []string // "version/tenant"
	marks     []string
	tenantsN  int
	dueErr    error
	markErr   error
	rechecker *Rechecker
}

func newFakeRecheck(dues []DueVersion, tenants ...string) *fakeRecheck {
	f := &fakeRecheck{dues: dues, tenants: tenants, failing: map[string]bool{}}
	f.rechecker = &Rechecker{
		Now:    func() time.Time { return day(3001, 6, 1).Add(10 * time.Hour) },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tenants: func(context.Context) ([]string, error) {
			f.tenantsN++
			return f.tenants, nil
		},
		due: func(_ context.Context, today time.Time) ([]DueVersion, error) {
			f.todays = append(f.todays, today)
			return f.dues, f.dueErr
		},
		recheck: func(_ context.Context, tenantID string, v DueVersion, _ time.Time) (RevalidateResult, error) {
			key := v.ID + "/" + tenantID
			f.calls = append(f.calls, key)
			if f.failing[key] {
				return RevalidateResult{}, errors.New("upstream down")
			}
			return RevalidateResult{Examined: 1}, nil
		},
		mark: func(_ context.Context, id string) error {
			f.marks = append(f.marks, id)
			return f.markErr
		},
	}
	return f
}

func dueV(id string, n int) DueVersion {
	return DueVersion{ID: id, Version: n, EffectiveFrom: day(3001, 5, n)}
}

func TestRechecker_NothingDueTouchesNoTenant(t *testing.T) {
	f := newFakeRecheck(nil, "T1", "T2")
	if err := f.rechecker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(f.calls) != 0 || f.tenantsN != 0 || len(f.marks) != 0 {
		t.Errorf("recheck calls %v, Tenants calls %d, marks %v; want none", f.calls, f.tenantsN, f.marks)
	}
}

func TestRechecker_EachDueVersionCoversEveryTenantInOrder(t *testing.T) {
	f := newFakeRecheck([]DueVersion{dueV("A", 1), dueV("B", 2)}, "T1", "T2")
	if err := f.rechecker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got, want := strings.Join(f.calls, ","), "A/T1,A/T2,B/T1,B/T2"; got != want {
		t.Errorf("recheck calls = %s, want %s", got, want)
	}
	if got, want := strings.Join(f.marks, ","), "A,B"; got != want {
		t.Errorf("marks = %s, want %s", got, want)
	}
}

func TestRechecker_OneTenantFailingLeavesTheVersionDue(t *testing.T) {
	f := newFakeRecheck([]DueVersion{dueV("A", 1)}, "T1", "T2")
	f.failing["A/T1"] = true
	err := f.rechecker.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "T1") {
		t.Fatalf("err = %v, want an error naming T1", err)
	}
	if got, want := strings.Join(f.calls, ","), "A/T1,A/T2"; got != want {
		t.Errorf("recheck calls = %s, want %s (T2 still runs after T1 fails)", got, want)
	}
	if len(f.marks) != 0 {
		t.Errorf("marks = %v, want none", f.marks)
	}
}

func TestRechecker_RetriesOnlyTheFailedTenant(t *testing.T) {
	f := newFakeRecheck([]DueVersion{dueV("A", 1)}, "T1", "T2")
	f.failing["A/T1"] = true
	if err := f.rechecker.RunOnce(context.Background()); err == nil {
		t.Fatal("first tick: err = nil, want T1's failure")
	}
	f.calls, f.failing["A/T1"] = nil, false
	if err := f.rechecker.RunOnce(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got, want := strings.Join(f.calls, ","), "A/T1"; got != want {
		t.Errorf("second tick recheck calls = %s, want %s only", got, want)
	}
	if got, want := strings.Join(f.marks, ","), "A"; got != want {
		t.Errorf("marks = %s, want %s", got, want)
	}
}

func TestRechecker_NewTenantJoinsAnOpenVersion(t *testing.T) {
	f := newFakeRecheck([]DueVersion{dueV("A", 1)}, "T1", "T2")
	f.failing["A/T2"] = true
	if err := f.rechecker.RunOnce(context.Background()); err == nil {
		t.Fatal("first tick: err = nil, want T2's failure")
	}
	f.calls, f.failing["A/T2"], f.tenants = nil, false, []string{"T1", "T2", "T3"}
	if err := f.rechecker.RunOnce(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if got, want := strings.Join(f.calls, ","), "A/T2,A/T3"; got != want {
		t.Errorf("second tick recheck calls = %s, want %s", got, want)
	}
	if got, want := strings.Join(f.marks, ","), "A"; got != want {
		t.Errorf("marks = %s, want %s", got, want)
	}
}

func TestRechecker_TodayIsTheUTCDate(t *testing.T) {
	lagos := time.FixedZone("WAT", 3600)
	for _, tc := range []struct {
		now  time.Time
		want time.Time
	}{
		{time.Date(3001, 6, 1, 0, 30, 0, 0, lagos), day(3001, 5, 31)},
		{time.Date(3001, 6, 1, 0, 30, 0, 0, time.UTC), day(3001, 6, 1)},
	} {
		f := newFakeRecheck(nil)
		f.rechecker.Now = func() time.Time { return tc.now }
		if err := f.rechecker.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		if len(f.todays) != 1 || !f.todays[0].Equal(tc.want) {
			t.Errorf("Now %s: due received %v, want %s", tc.now, f.todays, tc.want)
		}
	}
}

// A marker suppresses the re-check, so a version that has not started is never marked, even if
// due wrongly returns it. A version dated today is the control: it runs and is marked.
func TestRechecker_NeverMarksAFutureDatedVersion(t *testing.T) {
	future := DueVersion{ID: "F", Version: 9, EffectiveFrom: day(3001, 6, 2)}
	today := DueVersion{ID: "D", Version: 8, EffectiveFrom: day(3001, 6, 1)}
	f := newFakeRecheck([]DueVersion{future, today}, "T1")
	if err := f.rechecker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got, want := strings.Join(f.calls, ","), "D/T1"; got != want {
		t.Errorf("recheck calls = %s, want %s (the version dated tomorrow is skipped)", got, want)
	}
	if got, want := strings.Join(f.marks, ","), "D"; got != want {
		t.Errorf("marks = %s, want %s", got, want)
	}
}

func TestRechecker_EnumerationOrDueFailureMarksNothing(t *testing.T) {
	t.Run("due fails", func(t *testing.T) {
		f := newFakeRecheck([]DueVersion{dueV("A", 1)}, "T1")
		f.dueErr = errors.New("due down")
		if err := f.rechecker.RunOnce(context.Background()); err == nil {
			t.Fatal("err = nil, want the due failure")
		}
		if f.tenantsN != 0 || len(f.calls) != 0 || len(f.marks) != 0 {
			t.Errorf("Tenants calls %d, recheck calls %v, marks %v; want none", f.tenantsN, f.calls, f.marks)
		}
	})
	t.Run("tenant enumeration fails", func(t *testing.T) {
		f := newFakeRecheck([]DueVersion{dueV("A", 1)}, "T1")
		f.rechecker.Tenants = func(context.Context) ([]string, error) { return nil, errors.New("reader down") }
		if err := f.rechecker.RunOnce(context.Background()); err == nil {
			t.Fatal("err = nil, want the enumeration failure")
		}
		if len(f.calls) != 0 || len(f.marks) != 0 {
			t.Errorf("recheck calls %v, marks %v; want none", f.calls, f.marks)
		}
	})
}

func TestRechecker_MarkFailureKeepsTheVersionDueAndItsTenantsDone(t *testing.T) {
	f := newFakeRecheck([]DueVersion{dueV("A", 1)}, "T1")
	f.markErr = errors.New("db down")
	if err := f.rechecker.RunOnce(context.Background()); err == nil {
		t.Fatal("err = nil, want the mark failure")
	}
	f.calls, f.markErr = nil, nil
	if err := f.rechecker.RunOnce(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(f.calls) != 0 || len(f.marks) != 2 {
		t.Errorf("second tick: recheck calls %v, marks %v; want no re-check and the mark retried", f.calls, f.marks)
	}
}

// --- AC 6: the start date is the trigger, with the real functions --------------

func TestRechecker_DB_StartDatePassingTriggersTheRecheck(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, _ := seedBuyerNameVersion(t, super, "3001-06-01")
	t1 := seedTenant(t, super, "RO-DB T1")
	t2 := seedTenant(t, super, "RO-DB T2")
	e1 := seedEntity(t, super, t1, "RO-DB e1")
	e2 := seedEntity(t, super, t2, "RO-DB e2")
	requireVersionFor(t, super, "3001-07-01", bID)
	inv1 := seedRecheckInvoice(t, super, t1, e1, "RO-DB-1", StatusValidated, strPtr("3001-07-01"), nil)
	inv2 := seedRecheckInvoice(t, super, t2, e2, "RO-DB-2", StatusValidated, strPtr("3001-07-01"), nil)
	markers := func() int {
		return mustCount(t, super, `SELECT count(*) FROM rule_set_version_rechecks WHERE rule_set_version_id = $1`, bID)
	}

	now := time.Date(3001, 5, 31, 12, 0, 0, 0, time.UTC)
	r := &Rechecker{
		Pool: app, Store: store, Gate: realRecheckGate(t, app, store),
		Tenants: func(context.Context) ([]string, error) { return []string{t1, t2}, nil },
		Now:     func() time.Time { return now },
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	// The dev DB may hold real unmarked dated versions; keep the run on the fixture.
	r.due = func(ctx context.Context, today time.Time) ([]DueVersion, error) {
		all, err := DueRechecks(ctx, app, today)
		var only []DueVersion
		for _, v := range all {
			if v.ID == bID {
				only = append(only, v)
			}
		}
		return only, err
	}

	if err := r.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce on the day before: %v", err)
	}
	for _, id := range []string{inv1, inv2} {
		if got := readInvoiceStatus(t, super, id); got != StatusValidated {
			t.Errorf("invoice %s = %q the day before the start date, want validated", id, got)
		}
	}
	if n := markers(); n != 0 {
		t.Fatalf("markers = %d the day before, want 0", n)
	}

	now = time.Date(3001, 6, 1, 0, 5, 0, 0, time.UTC)
	if err := r.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce on the start date: %v", err)
	}
	for _, id := range []string{inv1, inv2} {
		if got := readInvoiceStatus(t, super, id); got != StatusDraft {
			t.Errorf("invoice %s = %q on the start date, want draft", id, got)
		}
		if n := demotionHistoryRows(t, super, id); n != 1 {
			t.Errorf("invoice %s demotion history rows = %d, want 1", id, n)
		}
	}
	if n := markers(); n != 1 {
		t.Fatalf("markers = %d on the start date, want 1", n)
	}

	if err := r.RunOnce(ctx); err != nil {
		t.Fatalf("third RunOnce: %v", err)
	}
	for _, id := range []string{inv1, inv2} {
		if n := demotionHistoryRows(t, super, id); n != 1 {
			t.Errorf("invoice %s demotion history rows = %d after the third tick, want 1", id, n)
		}
	}
}

// --- QA: real functions beyond the single-fixture case ---------------------------

// onlyVersions keeps a run on the fixture versions: the dev DB may hold real unmarked dated ones.
func onlyVersions(pool *pgxpool.Pool, ids ...string) func(context.Context, time.Time) ([]DueVersion, error) {
	return func(ctx context.Context, today time.Time) ([]DueVersion, error) {
		all, err := DueRechecks(ctx, pool, today)
		var only []DueVersion
		for _, v := range all {
			if slices.Contains(ids, v.ID) {
				only = append(only, v)
			}
		}
		return only, err
	}
}

func markerCount(t *testing.T, super *pgxpool.Pool, versionID string) int {
	t.Helper()
	return mustCount(t, super, `SELECT count(*) FROM rule_set_version_rechecks WHERE rule_set_version_id = $1`, versionID)
}

// due is the real DueRechecks, unfiltered: RunOnce must take two due versions oldest start date
// first (the older one has the higher version number) and leave a later-dated one alone. Only
// mark is wrapped, so that a real unmarked version in the dev DB is not marked by the test.
func TestRechecker_DB_DueVersionsRunOldestStartDateFirst(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, _ := seedBuyerNameVersion(t, super, "3001-06-01")
	aID, _ := seedBuyerNameVersion(t, super, "3001-05-01")
	cID, _ := seedBuyerNameVersion(t, super, "3001-06-05")
	requireVersionFor(t, super, "3001-05-15", aID)
	requireVersionFor(t, super, "3001-06-02", bID)
	requireVersionFor(t, super, "3001-06-10", cID)
	t1 := seedTenant(t, super, "RO-ORD T1")
	t2 := seedTenant(t, super, "RO-ORD T2")
	var invA, invB, invC []string
	for i, tn := range []string{t1, t2} {
		e := seedEntity(t, super, tn, fmt.Sprintf("RO-ORD e%d", i))
		invA = append(invA, seedRecheckInvoice(t, super, tn, e, fmt.Sprintf("RO-ORD-A%d", i), StatusValidated, strPtr("3001-05-15"), nil))
		invB = append(invB, seedRecheckInvoice(t, super, tn, e, fmt.Sprintf("RO-ORD-B%d", i), StatusValidated, strPtr("3001-06-02"), nil))
		invC = append(invC, seedRecheckInvoice(t, super, tn, e, fmt.Sprintf("RO-ORD-C%d", i), StatusValidated, strPtr("3001-06-10"), nil))
	}
	fixture := map[string]string{aID: "A", bID: "B", cID: "C"}

	var rechecks, marks []string
	gate := realRecheckGate(t, app, store)
	r := &Rechecker{
		Pool: app, Store: store, Gate: gate,
		Tenants: func(context.Context) ([]string, error) { return []string{t1, t2}, nil },
		Now:     func() time.Time { return time.Date(3001, 6, 1, 12, 0, 0, 0, time.UTC) },
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r.recheck = func(ctx context.Context, tenantID string, v DueVersion, today time.Time) (RevalidateResult, error) {
		if name, ok := fixture[v.ID]; ok {
			rechecks = append(rechecks, name+"/"+map[string]string{t1: "t1", t2: "t2"}[tenantID])
		}
		return RecheckCovered(ctx, app, store, gate, tenantID, v, today)
	}
	r.mark = func(ctx context.Context, id string) error {
		name, ok := fixture[id]
		if !ok {
			return nil
		}
		marks = append(marks, name)
		return MarkRechecked(ctx, app, id)
	}
	realMarkers := mustCount(t, super, `SELECT count(*) FROM rule_set_version_rechecks`)

	if err := r.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got, want := strings.Join(rechecks, ","), "A/t1,A/t2,B/t1,B/t2"; got != want {
		t.Errorf("fixture re-checks = %s, want %s", got, want)
	}
	if got, want := strings.Join(marks, ","), "A,B"; got != want {
		t.Errorf("fixture marks = %s, want %s", got, want)
	}
	if n := markerCount(t, super, aID) + markerCount(t, super, bID); n != 2 {
		t.Errorf("markers on A and B = %d, want 2", n)
	}
	if n := markerCount(t, super, cID); n != 0 {
		t.Errorf("markers on C (dated 3001-06-05) = %d, want 0", n)
	}
	for i := range invA {
		for _, id := range []string{invA[i], invB[i]} {
			if got := readInvoiceStatus(t, super, id); got != StatusDraft {
				t.Errorf("invoice %s = %q, want draft", id, got)
			}
		}
		if got := readInvoiceStatus(t, super, invC[i]); got != StatusValidated {
			t.Errorf("invoice %s covered by the not-yet-started C = %q, want validated", invC[i], got)
		}
	}
	if got, want := mustCount(t, super, `SELECT count(*) FROM rule_set_version_rechecks`), realMarkers+2; got != want {
		t.Errorf("marker rows = %d, want %d: the run marked a version the test does not own", got, want)
	}
}

// A restart loses the in-memory done map; the version repeats once. The repeat must write no
// second demotion, history row or audit row.
func TestRechecker_DB_ARestartRepeatsTheVersionWithoutASecondDemotion(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	bID, _ := seedBuyerNameVersion(t, super, "3001-06-01")
	t1 := seedTenant(t, super, "RO-RST T1")
	t2 := seedTenant(t, super, "RO-RST T2")
	e1 := seedEntity(t, super, t1, "RO-RST e1")
	e2 := seedEntity(t, super, t2, "RO-RST e2")
	requireVersionFor(t, super, "3001-07-01", bID)
	inv := map[string]string{
		t1: seedRecheckInvoice(t, super, t1, e1, "RO-RST-1", StatusValidated, strPtr("3001-07-01"), nil),
		t2: seedRecheckInvoice(t, super, t2, e2, "RO-RST-2", StatusValidated, strPtr("3001-07-01"), nil),
	}
	gate := realRecheckGate(t, app, store)
	newRechecker := func() *Rechecker {
		return &Rechecker{
			Pool: app, Store: store, Gate: gate,
			Tenants: func(context.Context) ([]string, error) { return []string{t1, t2}, nil },
			Now:     func() time.Time { return time.Date(3001, 6, 1, 0, 5, 0, 0, time.UTC) },
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			due:     onlyVersions(app, bID),
		}
	}

	before := newRechecker()
	before.mark = func(context.Context, string) error { return errors.New("db down") }
	if err := before.RunOnce(ctx); err == nil {
		t.Fatal("first run: err = nil, want the mark failure")
	}
	if n := markerCount(t, super, bID); n != 0 {
		t.Fatalf("markers = %d after the failed mark, want 0", n)
	}
	for tn, id := range inv {
		if got := readInvoiceStatus(t, super, id); got != StatusDraft {
			t.Fatalf("tenant %s invoice = %q after the first run, want draft", tn, got)
		}
	}

	after := newRechecker() // the restarted process: an empty done map
	var passes int
	after.recheck = func(ctx context.Context, tenantID string, v DueVersion, today time.Time) (RevalidateResult, error) {
		passes++
		return RecheckCovered(ctx, app, store, gate, tenantID, v, today)
	}
	if err := after.RunOnce(ctx); err != nil {
		t.Fatalf("run after the restart: %v", err)
	}
	if passes != 2 {
		t.Fatalf("re-checks after the restart = %d, want 2 (the version repeats once)", passes)
	}
	if n := markerCount(t, super, bID); n != 1 {
		t.Errorf("markers = %d after the restart run, want 1", n)
	}
	for tn, id := range inv {
		if got := readInvoiceStatus(t, super, id); got != StatusDraft {
			t.Errorf("tenant %s invoice = %q, want draft", tn, got)
		}
		if n := demotionHistoryRows(t, super, id); n != 1 {
			t.Errorf("tenant %s demotion history rows = %d after the repeat, want 1", tn, n)
		}
		if n := len(demotedAuditPayloads(t, app, tn, id)); n != 1 {
			t.Errorf("tenant %s demoted audit rows = %d after the repeat, want 1", tn, n)
		}
	}
}

// RecheckCovered lists every tenant's invoices for a role that bypasses RLS, and the reader role
// cannot read the versions: RunOnce on either pool must fail and mark nothing.
func TestRechecker_DB_AWrongRolePoolFailsAndMarksNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	readerURL := os.Getenv("DATABASE_READER_URL")
	if readerURL == "" {
		t.Fatal("DATABASE_READER_URL (the invoice_tenant_reader DSN) is unset; the rls job sets it")
	}
	reader, err := db.NewPool(ctx, readerURL)
	if err != nil {
		t.Fatalf("open reader pool: %v", err)
	}
	t.Cleanup(reader.Close)

	bID, _ := seedBuyerNameVersion(t, super, "3001-06-01")
	t1 := seedTenant(t, super, "RO-POOL T1")
	e1 := seedEntity(t, super, t1, "RO-POOL e1")
	requireVersionFor(t, super, "3001-07-01", bID)
	invID := seedRecheckInvoice(t, super, t1, e1, "RO-POOL-1", StatusValidated, strPtr("3001-07-01"), nil)

	run := func(pool *pgxpool.Pool) error {
		store := NewStore(pool)
		return (&Rechecker{
			Pool: pool, Store: store, Gate: realRecheckGate(t, app, store),
			Tenants: func(context.Context) ([]string, error) { return []string{t1}, nil },
			Now:     func() time.Time { return time.Date(3001, 6, 1, 0, 5, 0, 0, time.UTC) },
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			due:     onlyVersions(pool, bID),
		}).RunOnce(ctx)
	}

	if err := run(super); !errors.Is(err, ErrRevalidatePrivilegedRole) {
		t.Errorf("superuser pool: err = %v, want ErrRevalidatePrivilegedRole", err)
	}
	if err := run(reader); err == nil {
		t.Error("reader pool: err = nil, want a refusal")
	}
	if n := markerCount(t, super, bID); n != 0 {
		t.Errorf("markers = %d after the two refused runs, want 0", n)
	}
	if got := readInvoiceStatus(t, super, invID); got != StatusValidated {
		t.Errorf("invoice = %q after the refused runs, want validated", got)
	}

	// Control: the invoice_app pool does the work.
	if err := run(app); err != nil {
		t.Fatalf("invoice_app pool: %v", err)
	}
	if got := readInvoiceStatus(t, super, invID); got != StatusDraft {
		t.Errorf("invoice = %q after the invoice_app run, want draft", got)
	}
	if n := markerCount(t, super, bID); n != 1 {
		t.Errorf("markers = %d after the invoice_app run, want 1", n)
	}
}
