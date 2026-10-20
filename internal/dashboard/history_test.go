package dashboard

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// seedSnap inserts one rollup_snapshots row as the superuser; total doubles as a row marker.
func seedSnap(t *testing.T, super *pgxpool.Pool, tenant, entity, day string, total int) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO rollup_snapshots (tenant_id, entity_id, day, total, readiness_num, needs_attention, not_submitted, vat_kobo)
		 VALUES ($1, $2, $3::date, $4, 0, 0, 0, 0)`, tenant, entity, day, total); err != nil {
		t.Fatalf("seed rollup_snapshots: %v", err)
	}
}

func historyKeys(rows []HistoryRow) [][2]string {
	out := [][2]string{}
	for _, r := range rows {
		out = append(out, [2]string{r.Day, r.EntityID})
	}
	return out
}

func TestStoreHistory_OldestFirstThenEntity(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "history order")
	a, b := seedEntity(t, super, tenant, "H A"), seedEntity(t, super, tenant, "H B")
	x, y := a, b
	if x > y {
		x, y = y, x
	}
	seedSnap(t, super, tenant, y, "2026-10-20", 4)
	seedSnap(t, super, tenant, x, "2026-10-20", 3)
	seedSnap(t, super, tenant, y, "2026-10-19", 2)
	seedSnap(t, super, tenant, x, "2026-10-19", 1)

	got, err := NewStore(app).History(csMember(t, super, tenant, "admin"), "2026-10-19")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	want := [][2]string{{"2026-10-19", x}, {"2026-10-19", y}, {"2026-10-20", x}, {"2026-10-20", y}}
	if !reflect.DeepEqual(historyKeys(got), want) {
		t.Errorf("order = %v, want %v", historyKeys(got), want)
	}
	if got[0].Total != 1 || got[3].Total != 4 {
		t.Errorf("totals = %d..%d, want 1..4", got[0].Total, got[3].Total)
	}
}

func TestStoreHistory_SinceBoundIsInclusive(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "history since")
	e := seedEntity(t, super, tenant, "H A")
	for _, d := range []string{"2026-10-18", "2026-10-19", "2026-10-20"} {
		seedSnap(t, super, tenant, e, d, 1)
	}
	got, err := NewStore(app).History(csMember(t, super, tenant, "admin"), "2026-10-19")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	want := [][2]string{{"2026-10-19", e}, {"2026-10-20", e}}
	if !reflect.DeepEqual(historyKeys(got), want) {
		t.Errorf("rows = %v, want %v", historyKeys(got), want)
	}
}

func TestStoreHistory_OtherTenantsRowsInvisible(t *testing.T) {
	super, app := dbTestPools(t)
	ta, tb := seedTenant(t, super, "history tenant A"), seedTenant(t, super, "history tenant B")
	ea, eb := seedEntity(t, super, ta, "H A"), seedEntity(t, super, tb, "H B")
	seedSnap(t, super, ta, ea, "2026-10-20", 1)
	seedSnap(t, super, tb, eb, "2026-10-20", 1)

	got, err := NewStore(app).History(csMember(t, super, ta, "admin"), "2026-10-01")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if want := [][2]string{{"2026-10-20", ea}}; !reflect.DeepEqual(historyKeys(got), want) {
		t.Errorf("rows = %v, want %v", historyKeys(got), want)
	}
}

func TestStoreHistory_LimitedMemberSeesOnlyTheirClients(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "history limited")
	x, y := seedEntity(t, super, tenant, "H X"), seedEntity(t, super, tenant, "H Y")
	seedSnap(t, super, tenant, x, "2026-10-20", 1)
	seedSnap(t, super, tenant, y, "2026-10-20", 1)
	st := NewStore(app)

	got, err := st.History(csMember(t, super, tenant, "reviewer", x), "2026-10-01")
	if err != nil {
		t.Fatalf("History limited: %v", err)
	}
	if want := [][2]string{{"2026-10-20", x}}; !reflect.DeepEqual(historyKeys(got), want) {
		t.Errorf("limited rows = %v, want %v", historyKeys(got), want)
	}
	all, err := st.History(csMember(t, super, tenant, "admin"), "2026-10-01")
	if err != nil {
		t.Fatalf("History admin: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("admin rows = %d, want 2", len(all))
	}
}

func TestStoreHistory_UnassignedMemberReadsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	tenant := seedTenant(t, super, "history unassigned")
	seedSnap(t, super, tenant, seedEntity(t, super, tenant, "H X"), "2026-10-20", 1)
	seedSnap(t, super, tenant, seedEntity(t, super, tenant, "H Y"), "2026-10-20", 1)

	got, err := NewStore(app).History(csMember(t, super, tenant, "preparer"), "2026-10-01")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("rows = %#v, want empty non-nil", got)
	}
}

func TestStoreHistory_NoIdentityFailsClosed(t *testing.T) {
	_, app := dbTestPools(t)
	_, err := NewStore(app).History(context.Background(), "2026-10-01")
	if !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("err = %v, want ErrNoTenant", err)
	}
}
