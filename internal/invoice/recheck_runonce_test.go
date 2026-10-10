package invoice

// Rechecker.RunOnce: orchestration over replaced func fields, plus one DB-backed case with the
// real functions. No t.Parallel (shared DB); CI runs this package unfiltered in the rls job.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
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
				return RevalidateResult{}, fmt.Errorf("upstream down for %s", tenantID)
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
// due wrongly returns it.
func TestRechecker_NeverMarksAFutureDatedVersion(t *testing.T) {
	future := DueVersion{ID: "F", Version: 9, EffectiveFrom: day(3001, 6, 2)}
	f := newFakeRecheck([]DueVersion{future}, "T1")
	if err := f.rechecker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(f.calls) != 0 || len(f.marks) != 0 {
		t.Errorf("recheck calls %v, marks %v; want none for a version dated tomorrow", f.calls, f.marks)
	}
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
