package audit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const staffDevHouseTenant = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"

// staffSuperPool seeds tenants and members; the test skips without DATABASE_SUPERUSER_URL.
func staffSuperPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_SUPERUSER_URL")
	if url == "" {
		t.Skip("staff audit suite skipped: set DATABASE_SUPERUSER_URL (or run `make test-audit`)")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect superuser pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func staffSeedTenant(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, "staff-read "+id[:8]); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}

func staffSeedMember(t *testing.T, super *pgxpool.Pool, tenant, user string, name, email *string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status, display_name, email) VALUES ($1, $2, 'admin', 'active', $3, $4)`,
		tenant, user, name, email); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// staffHouseMember adopts the committed house tenant (a dev one when none) and seeds an active house member.
func staffHouseMember(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var house string
	err := super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&house)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'ASComply (dev)') ON CONFLICT DO NOTHING`, staffDevHouseTenant); err != nil {
			t.Fatalf("seed the dev house tenant: %v", err)
		}
		if _, err := super.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, staffDevHouseTenant); err != nil {
			t.Fatalf("seed the house row: %v", err)
		}
		err = super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&house)
	}
	if err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	subject := uuid.NewString()
	staffSeedMember(t, super, house, subject, nil, nil)
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, house, subject)
	})
	return subject
}

// staffContext is the context auth.RequireStaff builds for a staff caller: the key is private to auth.
func staffContext(t *testing.T, subject string) context.Context {
	t.Helper()
	var got context.Context
	h := auth.RequireStaff(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Context() }))
	ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", Staff: true})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/staff/audit-log", nil).WithContext(ctx))
	if got == nil {
		t.Fatal("RequireStaff refused the staff caller")
	}
	return got
}

// staffSeedRow commits one audit_log row, as its own transaction so created_at differs per row.
func staffSeedRow(t *testing.T, f *fixture, tenant, actor, event string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, actor, event, map[string]any{"k": event})
	}); err != nil {
		t.Fatalf("seed audit row: %v", err)
	}
}

func staffEventIDs(p audit.StaffPage) []string {
	var ids []string
	for _, r := range p.Events {
		ids = append(ids, r.ID)
	}
	return ids
}

func TestRLS_StaffSearchReadsEveryTenantAndPages(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	ctx := staffContext(t, staffHouseMember(t, super))
	a, b := staffSeedTenant(t, super), staffSeedTenant(t, super)
	for range 3 {
		staffSeedRow(t, f, a, "system", "staff.read.a")
	}
	for range 2 {
		staffSeedRow(t, f, b, "system", "staff.read.b")
	}
	store := audit.NewStore(f.app)

	first, err := store.StaffSearch(ctx, audit.StaffFilter{Tenant: a, Limit: 2})
	if err != nil {
		t.Fatalf("StaffSearch: %v", err)
	}
	if len(first.Events) != 2 || !first.Page.HasMore || first.Page.NextCursor == nil || first.Page.Limit != 2 {
		t.Fatalf("first page = %d events, page %+v, want 2 with has_more and a cursor", len(first.Events), first.Page)
	}
	cur, err := audit.DecodeCursor(*first.Page.NextCursor)
	if err != nil {
		t.Fatalf("decode next_cursor: %v", err)
	}
	second, err := store.StaffSearch(ctx, audit.StaffFilter{Tenant: a, Limit: 2, Cursor: &cur})
	if err != nil {
		t.Fatalf("StaffSearch with cursor: %v", err)
	}
	if len(second.Events) != 1 || second.Page.HasMore || second.Page.NextCursor != nil {
		t.Fatalf("second page = %d events, page %+v, want the third row and no more", len(second.Events), second.Page)
	}
	all := append(staffEventIDs(first), staffEventIDs(second)...)
	slices.Sort(all)
	if len(slices.Compact(all)) != 3 {
		t.Errorf("pages overlap or leave a gap: ids %v", all)
	}
	for _, r := range append(first.Events, second.Events...) {
		if r.TenantID != a || r.TenantName == nil || r.Event != "staff.read.a" {
			t.Errorf("row %+v is not tenant A's", r)
		}
	}

	wide, err := store.StaffSearch(ctx, audit.StaffFilter{Limit: 25})
	if err != nil {
		t.Fatalf("StaffSearch without a tenant: %v", err)
	}
	seen := map[string]bool{}
	for i, r := range wide.Events {
		seen[r.TenantID] = true
		if i > 0 && r.CreatedAt.After(wide.Events[i-1].CreatedAt) {
			t.Errorf("row %d created_at %v is after row %d's %v: not newest first", i, r.CreatedAt, i-1, wide.Events[i-1].CreatedAt)
		}
	}
	if !seen[a] || !seen[b] {
		t.Errorf("tenant-less search holds tenants A=%v B=%v, want both", seen[a], seen[b])
	}
}

func TestRLS_StaffSearchHasMoreAtTheExactLimit(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	ctx := staffContext(t, staffHouseMember(t, super))
	tenant := staffSeedTenant(t, super)
	staffSeedRow(t, f, tenant, "system", "staff.exact")
	staffSeedRow(t, f, tenant, "system", "staff.exact")
	store := audit.NewStore(f.app)

	p, err := store.StaffSearch(ctx, audit.StaffFilter{Tenant: tenant, Limit: 2})
	if err != nil || len(p.Events) != 2 || p.Page.HasMore || p.Page.NextCursor != nil {
		t.Fatalf("exact fit = %d events, page %+v, err %v; want 2, no more, no cursor", len(p.Events), p.Page, err)
	}
	staffSeedRow(t, f, tenant, "system", "staff.exact")
	if p, err = store.StaffSearch(ctx, audit.StaffFilter{Tenant: tenant, Limit: 2}); err != nil || !p.Page.HasMore {
		t.Errorf("a third row: page %+v, err %v; want has_more", p.Page, err)
	}
}

func TestRLS_StaffSearchEmptyAndUnknownTenant(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	ctx := staffContext(t, staffHouseMember(t, super))
	store := audit.NewStore(f.app)
	for name, tenant := range map[string]string{"fresh tenant": staffSeedTenant(t, super), "unknown tenant": uuid.NewString()} {
		p, err := store.StaffSearch(ctx, audit.StaffFilter{Tenant: tenant, Limit: 25})
		if err != nil || p.Events == nil || len(p.Events) != 0 || p.Page.HasMore {
			t.Errorf("%s: events %v (nil %v), page %+v, err %v; want an empty non-nil page", name, p.Events, p.Events == nil, p.Page, err)
		}
	}
}

func TestRLS_StaffSearchLabelsActors(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	ctx := staffContext(t, staffHouseMember(t, super))
	tenant := staffSeedTenant(t, super)
	named, mailed := uuid.NewString(), uuid.NewString()
	name, mail := "Ada Staff-Read", "ada."+named[:8]+"@example.test"
	staffSeedMember(t, super, tenant, named, &name, &mail)
	staffSeedMember(t, super, tenant, mailed, nil, &mail)
	for _, actor := range []string{named, mailed, "system", "extraction-worker"} {
		staffSeedRow(t, f, tenant, actor, "staff.label")
	}

	p, err := audit.NewStore(f.app).StaffSearch(ctx, audit.StaffFilter{Tenant: tenant, Event: "staff.label", Limit: 10})
	if err != nil {
		t.Fatalf("StaffSearch: %v", err)
	}
	type label struct{ name, kind string }
	want := map[string]label{
		named:               {name, "person"},
		mailed:              {mail, "person"},
		"system":            {"System", "system"},
		"extraction-worker": {"extraction-worker", "raw"},
	}
	if len(p.Events) != len(want) {
		t.Fatalf("got %d rows, want %d", len(p.Events), len(want))
	}
	for _, r := range p.Events {
		if got, ok := want[r.Actor]; !ok || (label{r.ActorName, r.ActorKind}) != got {
			t.Errorf("actor %q labelled %q/%q, want %v", r.Actor, r.ActorName, r.ActorKind, got)
		}
	}
}

func TestRLS_StaffChangesListsEveryRowWithTenantNames(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	staff := staffHouseMember(t, super)
	ctx := staffContext(t, staff)
	tenant := staffSeedTenant(t, super)
	actor, version := uuid.New(), uuid.New()
	globalEvent, tenantEvent := "staff.global."+actor.String(), "staff.tenant."+actor.String()

	if err := withAppTx(t, f, func(tx pgx.Tx) error {
		return audit.RecordStaff(context.Background(), tx, audit.StaffEvent{Actor: actor, Event: globalEvent, RuleSetVersionID: version})
	}); err != nil {
		t.Fatalf("record global change: %v", err)
	}
	if err := db.WithinTenantTx(context.Background(), f.app, tenant, func(tx pgx.Tx) error {
		return audit.RecordStaff(context.Background(), tx, audit.StaffEvent{Actor: actor, Event: tenantEvent, TenantID: uuid.MustParse(tenant)})
	}); err != nil {
		t.Fatalf("record tenant change: %v", err)
	}

	changes, err := audit.NewStore(f.app).StaffChanges(ctx)
	if err != nil {
		t.Fatalf("StaffChanges: %v", err)
	}
	idx := map[string]int{}
	for i, c := range changes {
		if i > 0 && c.CreatedAt.After(changes[i-1].CreatedAt) {
			t.Errorf("change %d is newer than change %d: not newest first", i, i-1)
		}
		idx[c.Event] = i
	}
	gi, gok := idx[globalEvent]
	ti, tok := idx[tenantEvent]
	if !gok || !tok {
		t.Fatalf("changes miss a row: global %v tenant %v", gok, tok)
	}
	if ti > gi {
		t.Errorf("the tenant row (written second) sits at %d after the global row at %d", ti, gi)
	}
	g, tr := changes[gi], changes[ti]
	if g.TenantID != nil || g.TenantName != nil || g.RuleSetVersionID == nil || *g.RuleSetVersionID != version.String() {
		t.Errorf("global row = %+v, want tenant nil and version %s", g, version)
	}
	if tr.TenantID == nil || *tr.TenantID != tenant || tr.TenantName == nil || *tr.TenantName != "staff-read "+tenant[:8] {
		t.Errorf("tenant row = %+v, want tenant %s with its name", tr, tenant)
	}
}

func TestRLS_StaffChangeLinksToTheTenantRow(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	staff := staffHouseMember(t, super)
	ctx := staffContext(t, staff)
	tenant := staffSeedTenant(t, super)
	event := "staff.link." + uuid.NewString()
	if err := db.WithinTenantTx(context.Background(), f.app, tenant, func(tx pgx.Tx) error {
		return audit.RecordStaff(context.Background(), tx, audit.StaffEvent{
			Actor: uuid.MustParse(staff), Event: event, TenantID: uuid.MustParse(tenant), Payload: map[string]any{"k": "v"}})
	}); err != nil {
		t.Fatalf("record tenant change: %v", err)
	}

	store := audit.NewStore(f.app)
	changes, err := store.StaffChanges(ctx)
	if err != nil {
		t.Fatalf("StaffChanges: %v", err)
	}
	var r *audit.StaffChange
	for i := range changes {
		if changes[i].Event == event {
			r = &changes[i]
		}
	}
	if r == nil {
		t.Fatal("the staff row is not in StaffChanges")
	}
	day := r.CreatedAt.UTC().Truncate(24 * time.Hour)
	p, err := store.StaffSearch(ctx, audit.StaffFilter{Tenant: tenant, Event: event, From: day, To: day.Add(24*time.Hour - time.Millisecond), Limit: 25})
	if err != nil {
		t.Fatalf("StaffSearch: %v", err)
	}
	if len(p.Events) != 1 || !p.Events[0].CreatedAt.Equal(r.CreatedAt) || p.Events[0].ActorName != audit.SupportActor {
		t.Fatalf("search = %+v, want one row with created_at %v by %q", p.Events, r.CreatedAt, audit.SupportActor)
	}
}

func TestRLS_StaffSearchNonHouseActorIsNotStaff(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	tenant := staffSeedTenant(t, super)
	customer := uuid.NewString()
	staffSeedMember(t, super, tenant, customer, nil, nil)
	staffSeedRow(t, f, tenant, "system", "staff.nothouse")
	store := audit.NewStore(f.app)

	for name, ctx := range map[string]context.Context{"customer member": staffContext(t, customer), "no staff identity": context.Background()} {
		if p, err := store.StaffSearch(ctx, audit.StaffFilter{Tenant: tenant, Limit: 25}); !errors.Is(err, db.ErrNotStaff) || len(p.Events) != 0 {
			t.Errorf("%s: StaffSearch = %d rows, err %v; want db.ErrNotStaff and no row", name, len(p.Events), err)
		}
		if c, err := store.StaffChanges(ctx); !errors.Is(err, db.ErrNotStaff) || len(c) != 0 {
			t.Errorf("%s: StaffChanges = %d rows, err %v; want db.ErrNotStaff and no row", name, len(c), err)
		}
	}
}

func TestRLS_StaffReadsWriteNoAuditRow(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	staff := staffHouseMember(t, super)
	ctx := staffContext(t, staff)
	tenant := staffSeedTenant(t, super)
	staffSeedRow(t, f, tenant, "system", "staff.nowrite")
	var started time.Time
	if err := super.QueryRow(context.Background(), `SELECT clock_timestamp()`).Scan(&started); err != nil {
		t.Fatalf("read the database clock: %v", err)
	}

	store := audit.NewStore(f.app)
	if _, err := store.StaffSearch(ctx, audit.StaffFilter{Tenant: tenant, Limit: 25}); err != nil {
		t.Fatalf("StaffSearch: %v", err)
	}
	if _, err := store.StaffChanges(ctx); err != nil {
		t.Fatalf("StaffChanges: %v", err)
	}

	var staffRows, tenantRows int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM public.staff_audit_log WHERE actor = $1`, staff).Scan(&staffRows); err != nil {
		t.Fatalf("count staff_audit_log: %v", err)
	}
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM public.audit_log WHERE tenant_id = $1 AND created_at >= $2`, tenant, started).Scan(&tenantRows); err != nil {
		t.Fatalf("count audit_log: %v", err)
	}
	if staffRows != 0 || tenantRows != 0 {
		t.Errorf("reads wrote %d staff_audit_log and %d audit_log rows, want none", staffRows, tenantRows)
	}
}
