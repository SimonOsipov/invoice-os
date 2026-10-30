package dashboard

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type snapRow struct {
	Total, ReadinessNum, NeedsAttention, NotSubmitted, VATKobo int64
}

// snapRows reads one tenant's snapshots as the superuser: entity id -> day -> values.
func snapRows(t *testing.T, super *pgxpool.Pool, tenant string) map[string]map[string]snapRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT entity_id::text, to_char(day, 'YYYY-MM-DD'), total, readiness_num, needs_attention, not_submitted, vat_kobo
		 FROM rollup_snapshots WHERE tenant_id = $1`, tenant)
	if err != nil {
		t.Fatalf("read rollup_snapshots: %v", err)
	}
	defer rows.Close()
	out := map[string]map[string]snapRow{}
	for rows.Next() {
		var ent, day string
		var r snapRow
		if err := rows.Scan(&ent, &day, &r.Total, &r.ReadinessNum, &r.NeedsAttention, &r.NotSubmitted, &r.VATKobo); err != nil {
			t.Fatalf("scan rollup_snapshots: %v", err)
		}
		if out[ent] == nil {
			out[ent] = map[string]snapRow{}
		}
		out[ent][day] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rollup_snapshots rows: %v", err)
	}
	return out
}

func snapCount(rows map[string]map[string]snapRow) int {
	n := 0
	for _, days := range rows {
		n += len(days)
	}
	return n
}

func snapAt(now time.Time, tenants ...string) *Snapshotter {
	return &Snapshotter{
		Tenants: func(context.Context) ([]string, error) { return tenants, nil },
		Now:     func() time.Time { return now },
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func snapRun(t *testing.T, s *Snapshotter, app *pgxpool.Pool) {
	t.Helper()
	s.Pool = app
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
}

func TestSnapshot_RowsEqualTheRollupOfEachCompany(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot equals rollup")
	entA := seedEntity(t, super, tenant, "Snap A")
	entB := seedEntity(t, super, tenant, "Snap B")
	seedInvoiceWithViolations(t, super, tenant, entA, "SN-A-1", "draft", `[{"rule_key":"sn-r","severity":"error","message":"x"}]`)
	seedInvoiceAtStatus(t, super, tenant, entA, "SN-A-2", "validated")
	accepted := seedInvoiceAtStatus(t, super, tenant, entA, "SN-A-3", "accepted")
	if _, err := super.Exec(context.Background(), `UPDATE invoices SET vat = 7.50 WHERE id = $1`, accepted); err != nil {
		t.Fatalf("seed vat: %v", err)
	}
	seedInvoiceAtStatus(t, super, tenant, entB, "SN-B-1", "rejected")
	seedInvoiceAtStatus(t, super, tenant, entB, "SN-B-2", "draft")

	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenant), app)

	rollup, err := NewStore(app).Rollup(csMember(t, super, tenant, "admin"))
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(rollup.Clients) != 2 {
		t.Fatalf("Rollup clients = %d, want 2", len(rollup.Clients))
	}
	got := snapRows(t, super, tenant)
	if n := snapCount(got); n != 2 {
		t.Fatalf("snapshot rows = %d, want 2 (one per company)", n)
	}
	for _, c := range rollup.Clients {
		want := snapRow{
			Total:          c.Metrics[MetricReadiness].Den,
			ReadinessNum:   c.Metrics[MetricReadiness].Num,
			NeedsAttention: int64(c.NeedsAttention),
			NotSubmitted:   int64(c.Counts.Draft + c.Counts.Validated),
			VATKobo:        c.Metrics[MetricVATTracked].Num,
		}
		if g := got[c.EntityID]["2026-10-20"]; !reflect.DeepEqual(g, want) {
			t.Errorf("%s: snapshot = %+v, Rollup bucket = %+v", c.EntityName, g, want)
		}
	}
	// Literal anchors: a Rollup-vs-snapshot comparison alone cannot tell a wrong column mapping from a wrong query.
	if want := (snapRow{Total: 3, ReadinessNum: 2, NeedsAttention: 1, NotSubmitted: 2, VATKobo: 750}); got[entA]["2026-10-20"] != want {
		t.Errorf("company A = %+v, want %+v", got[entA]["2026-10-20"], want)
	}
	if want := (snapRow{Total: 2, ReadinessNum: 0, NeedsAttention: 1, NotSubmitted: 1, VATKobo: 0}); got[entB]["2026-10-20"] != want {
		t.Errorf("company B = %+v, want %+v", got[entB]["2026-10-20"], want)
	}
}

func TestSnapshot_WritesTheNetVATIncludingANegative(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot net vat")
	entA := seedEntity(t, super, tenant, "Snap A")
	entB := seedEntity(t, super, tenant, "Snap B")
	seedDesignFixture(t, super, tenant, entA, "SNV-A")
	invB := seedInvoiceWithVAT(t, super, tenant, entB, "SNV-B-INV", "10.00")
	seedNote(t, super, tenant, entB, "SNV-B-CN", "credit_note", invB, "draft", "30.00")

	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenant), app)

	got := snapRows(t, super, tenant)
	if n := snapCount(got); n != 2 {
		t.Fatalf("snapshot rows = %d, want 2 (one per company)", n)
	}
	if g := got[entA]["2026-10-20"]; g.VATKobo != 5625 || g.Total != 3 {
		t.Errorf("company A = %+v, want vat_kobo 5625 and total 3", g)
	}
	if g := got[entB]["2026-10-20"]; g.VATKobo != -2000 || g.Total != 2 {
		t.Errorf("company B = %+v, want vat_kobo -2000 and total 2", g)
	}
}

func TestSnapshot_EarlierDayKeepsItsValueWhenANoteArrives(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot earlier day")
	ent := seedEntity(t, super, tenant, "Snap A")
	inv := seedInvoiceWithVAT(t, super, tenant, ent, "SED-INV", "75.00")

	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenant), app)
	if g := snapRows(t, super, tenant)[ent]["2026-10-20"]; g.VATKobo != 7500 {
		t.Fatalf("day 1 vat_kobo = %d, want 7500", g.VATKobo)
	}

	seedNote(t, super, tenant, ent, "SED-CN", "credit_note", inv, "draft", "22.50")
	snapRun(t, snapAt(time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC), tenant), app)

	days := snapRows(t, super, tenant)[ent]
	if g := days["2026-10-20"]; g.VATKobo != 7500 {
		t.Errorf("day 1 vat_kobo = %d, want 7500 (an earlier day keeps its stored value)", g.VATKobo)
	}
	if g := days["2026-10-21"]; g.VATKobo != 5250 {
		t.Errorf("day 2 vat_kobo = %d, want 5250", g.VATKobo)
	}
}

func TestSnapshot_CompanyWithNoInvoicesGetsNoRow(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot empty company")
	entA := seedEntity(t, super, tenant, "Snap A")
	entC := seedEntity(t, super, tenant, "Snap C empty")
	seedInvoiceAtStatus(t, super, tenant, entA, "SN-A-1", "draft")

	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenant), app)

	got := snapRows(t, super, tenant)
	if len(got[entA]) != 1 {
		t.Fatalf("company A rows = %d, want 1", len(got[entA]))
	}
	if len(got[entC]) != 0 {
		t.Errorf("company with no invoices has %d row(s), want none", len(got[entC]))
	}
}

func TestSnapshot_SecondRunTheSameDayReplacesTheRow(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot second run")
	entA := seedEntity(t, super, tenant, "Snap A")
	seedInvoiceAtStatus(t, super, tenant, entA, "SN-A-1", "draft")

	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenant), app)
	if r := snapRows(t, super, tenant)[entA]["2026-10-20"]; r.Total != 1 || r.NotSubmitted != 1 {
		t.Fatalf("first run row = %+v, want total 1 and not_submitted 1", r)
	}

	seedInvoiceAtStatus(t, super, tenant, entA, "SN-A-2", "validated")
	snapRun(t, snapAt(time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC), tenant), app)

	got := snapRows(t, super, tenant)
	if n := snapCount(got); n != 1 {
		t.Fatalf("rows after the second run = %d, want 1 (replaced, not added)", n)
	}
	if r := got[entA]["2026-10-20"]; r.Total != 2 || r.NotSubmitted != 2 {
		t.Errorf("second run row = %+v, want total 2 and not_submitted 2", r)
	}
}

func TestSnapshot_WATDayTurnsAt2300UTC(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot WAT turn")
	entA := seedEntity(t, super, tenant, "Snap A")
	seedInvoiceAtStatus(t, super, tenant, entA, "SN-A-1", "draft")

	snapRun(t, snapAt(time.Date(2026, 10, 20, 22, 59, 59, 0, time.UTC), tenant), app)
	snapRun(t, snapAt(time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC), tenant), app)

	days := snapRows(t, super, tenant)[entA]
	if len(days) != 2 {
		t.Fatalf("company A has days %v, want exactly 2026-10-20 and 2026-10-21", days)
	}
	for _, d := range []string{"2026-10-20", "2026-10-21"} {
		if _, ok := days[d]; !ok {
			t.Errorf("no row for %s", d)
		}
	}
}

func TestWATDay_Boundaries(t *testing.T) {
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 20, 22, 59, 59, 0, time.UTC), "2026-10-20"},
		{time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC), "2026-10-21"},
		{time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC), "2026-10-21"},
		{time.Date(2026, 12, 31, 22, 59, 59, 0, time.UTC), "2026-12-31"},
		{time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC), "2027-01-01"},
		{time.Date(2026, 10, 20, 18, 30, 0, 0, time.FixedZone("UTC-5", -5*3600)), "2026-10-21"}, // 23:30 UTC
		{time.Date(2026, 10, 21, 0, 30, 0, 0, time.FixedZone("UTC+2", 2*3600)), "2026-10-20"},   // 22:30 UTC
	} {
		if got := WATDay(tc.at); got != tc.want {
			t.Errorf("WATDay(%s) = %q, want %q", tc.at.Format(time.RFC3339), got, tc.want)
		}
	}
}

func TestSnapshotInterval_AtMostOneHour(t *testing.T) {
	if SnapshotInterval <= 0 || SnapshotInterval > time.Hour {
		t.Errorf("SnapshotInterval = %s, want > 0 and <= 1h", SnapshotInterval)
	}
}

func TestSnapshot_TenantFailingInsideItsTxRollsBackAlone(t *testing.T) {
	super, app := dbTestPools(t)
	tenA := seedTenant(t, super, "snapshot rollback A")
	tenB := seedTenant(t, super, "snapshot rollback B")
	entA := seedEntity(t, super, tenA, "Snap A")
	entB := seedEntity(t, super, tenB, "Snap B")
	seedInvoiceAtStatus(t, super, tenA, entA, "SN-A-1", "draft")
	seedInvoiceAtStatus(t, super, tenB, entB, "SN-B-1", "draft")

	boom := errors.New("hook boom")
	seen := map[string]bool{}
	s := snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenA, tenB)
	s.Pool = app
	s.afterTenant = func(id string) error {
		seen[id] = true
		if id == tenA {
			return boom
		}
		return nil
	}

	err := s.RunOnce(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("RunOnce error = %v, want it to wrap the hook error", err)
	}
	if !strings.Contains(err.Error(), tenA) {
		t.Errorf("error %q does not name failed tenant %s", err, tenA)
	}
	if !seen[tenA] || !seen[tenB] {
		t.Errorf("hook ran for %v, want both tenants (the loop must go on)", seen)
	}
	if n := snapCount(snapRows(t, super, tenA)); n != 0 {
		t.Errorf("failed tenant A kept %d row(s), want 0 (tx rolled back)", n)
	}
	if r := snapRows(t, super, tenB)[entB]["2026-10-20"]; r.Total != 1 {
		t.Errorf("tenant B row = %+v, want total 1 (written after A failed)", r)
	}
}

func TestSnapshot_FailingTenantDoesNotStopTheLoop(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot bad tenant first")
	ent := seedEntity(t, super, tenant, "Snap A")
	seedInvoiceAtStatus(t, super, tenant, ent, "SN-A-1", "draft")

	s := snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), "not-a-uuid", tenant)
	s.Pool = app
	err := s.RunOnce(context.Background())
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("RunOnce error = %v, want it to satisfy errors.Is(db.ErrNoTenant)", err)
	}
	if !strings.Contains(err.Error(), "not-a-uuid") {
		t.Errorf("error %q does not name the failed tenant", err)
	}
	if r := snapRows(t, super, tenant)[ent]["2026-10-20"]; r.Total != 1 {
		t.Errorf("real tenant row = %+v, want total 1 (written after the bad tenant)", r)
	}
}

func TestSnapshot_CancelledContextStopsTheLoopQuietly(t *testing.T) {
	super, app := dbTestPools(t)
	tenA := seedTenant(t, super, "snapshot cancel A")
	tenB := seedTenant(t, super, "snapshot cancel B")
	seedInvoiceAtStatus(t, super, tenA, seedEntity(t, super, tenA, "Snap A"), "SN-A-1", "draft")
	seedInvoiceAtStatus(t, super, tenB, seedEntity(t, super, tenB, "Snap B"), "SN-B-1", "draft")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs strings.Builder
	seen := map[string]bool{}
	s := snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenA, tenB)
	s.Pool = app
	s.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	s.afterTenant = func(id string) error {
		seen[id] = true
		cancel()
		return nil
	}

	err := s.RunOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce error = %v, want it to wrap context.Canceled", err)
	}
	if seen[tenB] {
		t.Errorf("tenant B was attempted after cancel")
	}
	if logs.Len() != 0 {
		t.Errorf("cancel logged %q, want no error lines", logs.String())
	}
}

func TestSnapshot_EnumerationErrorWritesNothing(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot enumeration error")
	ent := seedEntity(t, super, tenant, "Snap A")
	seedInvoiceAtStatus(t, super, tenant, ent, "SN-A-1", "draft")

	enumErr := errors.New("enumerate boom")
	s := snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC))
	s.Pool = app
	s.Tenants = func(context.Context) ([]string, error) { return nil, enumErr }
	if err := s.RunOnce(context.Background()); !errors.Is(err, enumErr) {
		t.Fatalf("RunOnce error = %v, want it to wrap the enumeration error", err)
	}
	if n := snapCount(snapRows(t, super, tenant)); n != 0 {
		t.Errorf("rows after an enumeration error = %d, want 0", n)
	}

	// Control: the same tenant is written once enumeration works, so the 0 above is not a seeding gap.
	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenant), app)
	if n := snapCount(snapRows(t, super, tenant)); n != 1 {
		t.Errorf("control run wrote %d row(s), want 1", n)
	}
}

func TestSnapshot_ReplaceUpdatesEveryValue(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot replace all values")
	ent := seedEntity(t, super, tenant, "Snap A")
	inv := seedInvoiceAtStatus(t, super, tenant, ent, "SN-A-1", "draft")

	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenant), app)
	if want, got := (snapRow{Total: 1, ReadinessNum: 0, NeedsAttention: 0, NotSubmitted: 1, VATKobo: 0}), snapRows(t, super, tenant)[ent]["2026-10-20"]; got != want {
		t.Fatalf("first run row = %+v, want %+v", got, want)
	}

	// Move all five values off their first-run numbers.
	if _, err := super.Exec(context.Background(), `UPDATE invoices SET status = 'accepted', vat = 12.34 WHERE id = $1`, inv); err != nil {
		t.Fatalf("update invoice: %v", err)
	}
	seedInvoiceAtStatus(t, super, tenant, ent, "SN-A-2", "rejected")
	seedInvoiceAtStatus(t, super, tenant, ent, "SN-A-3", "validated")
	seedInvoiceAtStatus(t, super, tenant, ent, "SN-A-4", "draft")
	snapRun(t, snapAt(time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC), tenant), app)

	got := snapRows(t, super, tenant)
	if n := snapCount(got); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	want := snapRow{Total: 4, ReadinessNum: 2, NeedsAttention: 1, NotSubmitted: 2, VATKobo: 1234}
	if g := got[ent]["2026-10-20"]; g != want {
		t.Errorf("second run row = %+v, want %+v", g, want)
	}
}

func TestSnapshot_CoversEveryCompanyWhateverAMembersScope(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot ignores member scope")
	entA := seedEntity(t, super, tenant, "Snap A")
	entB := seedEntity(t, super, tenant, "Snap B")
	seedInvoiceAtStatus(t, super, tenant, entA, "SN-A-1", "draft")
	seedInvoiceAtStatus(t, super, tenant, entB, "SN-B-1", "draft")

	limited, err := NewStore(app).Rollup(csMember(t, super, tenant, "reviewer", entA))
	if err != nil {
		t.Fatalf("limited Rollup: %v", err)
	}
	if len(limited.Clients) != 1 {
		t.Fatalf("limited Rollup clients = %d, want 1 (control: the scope is real)", len(limited.Clients))
	}

	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenant), app)
	got := snapRows(t, super, tenant)
	if len(got[entA]) != 1 || len(got[entB]) != 1 {
		t.Errorf("rows per company = A:%d B:%d, want 1 each (the snapshot is the whole tenant)", len(got[entA]), len(got[entB]))
	}
}

func TestSnapshot_EachTenantWritesOnlyItsOwnCompanies(t *testing.T) {
	super, app := dbTestPools(t)
	tenA := seedTenant(t, super, "snapshot isolation A")
	tenB := seedTenant(t, super, "snapshot isolation B")
	entA := seedEntity(t, super, tenA, "Snap A")
	entB := seedEntity(t, super, tenB, "Snap B")
	seedInvoiceAtStatus(t, super, tenA, entA, "SN-A-1", "draft")
	seedInvoiceAtStatus(t, super, tenB, entB, "SN-B-1", "draft")
	seedInvoiceAtStatus(t, super, tenB, entB, "SN-B-2", "validated")

	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenA, tenB), app)

	a, b := snapRows(t, super, tenA), snapRows(t, super, tenB)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("companies with rows: A=%d B=%d, want 1 each", len(a), len(b))
	}
	if r := a[entA]["2026-10-20"]; r.Total != 1 {
		t.Errorf("tenant A row = %+v, want total 1", r)
	}
	if r := b[entB]["2026-10-20"]; r.Total != 2 {
		t.Errorf("tenant B row = %+v, want total 2", r)
	}
}

func TestSnapshot_ConcurrentRunsLeaveOneRow(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot concurrent")
	ent := seedEntity(t, super, tenant, "Snap A")
	seedInvoiceAtStatus(t, super, tenant, ent, "SN-A-1", "draft")

	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			s := snapAt(now, tenant)
			s.Pool = app
			errs <- s.RunOnce(context.Background())
		}()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Errorf("concurrent RunOnce: %v", err)
		}
	}
	got := snapRows(t, super, tenant)
	if n := snapCount(got); n != 1 {
		t.Errorf("rows after 4 concurrent runs = %d, want 1", n)
	}
	if r := got[ent]["2026-10-20"]; r.Total != 1 {
		t.Errorf("row = %+v, want total 1", r)
	}
}

func TestSnapshot_TenantWithoutInvoicesIsANoop(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "snapshot no invoices at all")
	seedEntity(t, super, tenant, "Snap empty")

	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), tenant), app)
	if n := snapCount(snapRows(t, super, tenant)); n != 0 {
		t.Errorf("rows for a tenant without invoices = %d, want 0", n)
	}
	// No tenants at all is also a clean run.
	snapRun(t, snapAt(time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)), app)
}
