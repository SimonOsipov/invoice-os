package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
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

// staffPurge deletes rows of an append-only log as the superuser; the trigger refuses a plain DELETE, so it runs under 'replica'.
func staffPurge(t *testing.T, super *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := super.Begin(ctx)
	if err != nil {
		t.Errorf("purge: begin: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		t.Errorf("purge: set replica role: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		t.Errorf("purge %q: %v", query, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("purge: commit: %v", err)
	}
}

// staffSeedTenant seeds a tenant and purges it, its members and its log rows when the test ends.
func staffSeedTenant(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, "staff-read "+id[:8]); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		staffPurge(t, super, `DELETE FROM audit_log WHERE tenant_id = $1`, id)
		staffPurge(t, super, `DELETE FROM staff_audit_log WHERE tenant_id = $1`, id)
		for _, q := range []string{`DELETE FROM memberships WHERE tenant_id = $1`, `DELETE FROM tenants WHERE id = $1`} {
			if _, err := super.Exec(context.Background(), q, id); err != nil {
				t.Errorf("purge %q: %v", q, err)
			}
		}
	})
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
		staffPurge(t, super, `DELETE FROM staff_audit_log WHERE actor = $1`, subject)
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
	worker := "worker-" + uuid.NewString()
	for range 3 {
		staffSeedRow(t, f, a, worker, "staff.read.a")
	}
	for range 2 {
		staffSeedRow(t, f, b, worker, "staff.read.b")
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

	// No tenant, but the actor is this run's: newer rows elsewhere in audit_log cannot push these off the page.
	wide, err := store.StaffSearch(ctx, audit.StaffFilter{Actor: worker, Limit: 25})
	if err != nil {
		t.Fatalf("StaffSearch without a tenant: %v", err)
	}
	if len(wide.Events) != 5 {
		t.Fatalf("tenant-less search by the run's actor = %d rows, want 5", len(wide.Events))
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
	t.Cleanup(func() { staffPurge(t, super, `DELETE FROM staff_audit_log WHERE actor = $1`, actor) })
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

func TestRLS_StaffSearchAppliesEventActorAndInclusiveRange(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	ctx := staffContext(t, staffHouseMember(t, super))
	tenant := staffSeedTenant(t, super)
	ev, worker := "staff.filter."+uuid.NewString(), "worker-"+uuid.NewString()
	staffSeedRow(t, f, tenant, "system", ev+".a")
	staffSeedRow(t, f, tenant, worker, ev+".b")
	staffSeedRow(t, f, tenant, worker, ev+".a")
	store := audit.NewStore(f.app)
	search := func(name string, filter audit.StaffFilter) []audit.StaffRow {
		t.Helper()
		filter.Tenant, filter.Limit = tenant, 25
		p, err := store.StaffSearch(ctx, filter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return p.Events
	}

	all := search("all", audit.StaffFilter{})
	if len(all) != 3 {
		t.Fatalf("tenant holds %d rows, want 3", len(all))
	}
	if rows := search("event", audit.StaffFilter{Event: ev + ".a"}); len(rows) != 2 {
		t.Errorf("event filter returned %d rows, want 2", len(rows))
	}
	if rows := search("actor", audit.StaffFilter{Actor: worker}); len(rows) != 2 {
		t.Errorf("actor filter returned %d rows, want 2", len(rows))
	}
	if rows := search("event and actor", audit.StaffFilter{Event: ev + ".a", Actor: worker}); len(rows) != 1 || rows[0].Actor != worker || rows[0].Event != ev+".a" {
		t.Errorf("event and actor filter returned %+v, want the one worker row of .a", rows)
	}

	newest, middle, oldest := all[0], all[1], all[2]
	if !(newest.CreatedAt.After(middle.CreatedAt) && middle.CreatedAt.After(oldest.CreatedAt)) {
		t.Fatal("the three rows share a created_at: the range probes below prove nothing")
	}
	one := func(name string, rows []audit.StaffRow, want audit.StaffRow) {
		t.Helper()
		if len(rows) != 1 || rows[0].ID != want.ID {
			t.Errorf("%s returned %v, want only row %s", name, staffEventIDs(audit.StaffPage{Events: rows}), want.ID)
		}
	}
	one("from = to = the middle instant", search("both", audit.StaffFilter{From: middle.CreatedAt, To: middle.CreatedAt}), middle)
	one("from just after the middle", search("from", audit.StaffFilter{From: middle.CreatedAt.Add(time.Microsecond)}), newest)
	one("to just before the middle", search("to", audit.StaffFilter{To: middle.CreatedAt.Add(-time.Microsecond)}), oldest)
}

func TestRLS_StaffSearchReturnsThePayloadAndBreaksTiesOnID(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	ctx := staffContext(t, staffHouseMember(t, super))
	tenant := staffSeedTenant(t, super)
	event := "staff.tie." + uuid.NewString()
	if err := db.WithinTenantTx(context.Background(), f.app, tenant, func(tx pgx.Tx) error {
		for i := range 3 {
			if err := audit.Record(context.Background(), tx, "system", event, map[string]any{"n": i}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed three rows in one transaction: %v", err)
	}
	store := audit.NewStore(f.app)

	var seen []string
	var cursor *audit.Cursor
	for page := 0; page < 3; page++ {
		p, err := store.StaffSearch(ctx, audit.StaffFilter{Tenant: tenant, Limit: 1, Cursor: cursor})
		if err != nil || len(p.Events) != 1 {
			t.Fatalf("page %d = %d rows, err %v; want 1", page, len(p.Events), err)
		}
		row := p.Events[0]
		var payload map[string]any
		if err := json.Unmarshal(row.Payload, &payload); err != nil || payload["n"] == nil {
			t.Errorf("page %d payload = %s (%v), want the stored object", page, row.Payload, err)
		}
		seen = append(seen, row.ID)
		if wantMore := page < 2; p.Page.HasMore != wantMore {
			t.Fatalf("page %d has_more = %v, want %v", page, p.Page.HasMore, wantMore)
		}
		if p.Page.NextCursor == nil {
			break
		}
		c, err := audit.DecodeCursor(*p.Page.NextCursor)
		if err != nil {
			t.Fatalf("decode cursor: %v", err)
		}
		if cursor != nil && !c.CreatedAt.Equal(cursor.CreatedAt) {
			t.Fatal("the rows do not share a created_at: the tie-break is not exercised")
		}
		cursor = &c
	}
	ids := make([]int, len(seen))
	for i, id := range seen {
		ids[i], _ = strconv.Atoi(id)
	}
	if len(ids) != 3 || ids[0] <= ids[1] || ids[1] <= ids[2] {
		t.Errorf("walk saw ids %v, want three distinct ids strictly descending (ties break on id DESC)", ids)
	}
}

// RequireStaff is the gate platform.App puts on every /v1/staff path.
func TestRLS_StaffAuditRoutesThroughTheStaffGate(t *testing.T) {
	f, super := requireFixture(t), staffSuperPool(t)
	house := staffHouseMember(t, super)
	tenant := staffSeedTenant(t, super)
	customer := uuid.NewString()
	staffSeedMember(t, super, tenant, customer, nil, nil)
	event := "staff.http." + uuid.NewString()
	staffSeedRow(t, f, tenant, "system", event)

	store := audit.NewStore(f.app)
	routes := map[string]http.Handler{
		"/v1/staff/audit-log?tenant=" + tenant + "&event=" + event: auth.RequireStaff(audit.StaffSearchHandler(store.StaffSearch, nil)),
		"/v1/staff/staff-audit-log":                                auth.RequireStaff(audit.StaffChangesHandler(store.StaffChanges, nil)),
	}
	serve := func(h http.Handler, target string, id auth.Identity) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		h.ServeHTTP(rec, req.WithContext(auth.WithTenantlessCaller(context.Background(), id)))
		return rec
	}
	for target, h := range routes {
		rec := serve(h, target, auth.Identity{Subject: customer, Role: "authenticated", TenantID: tenant})
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), event) {
			t.Errorf("%s as a customer = %d %s, want 403 and no row", target, rec.Code, rec.Body)
		}
		rec = serve(h, target, auth.Identity{Subject: house, Role: "authenticated", Staff: true})
		if rec.Code != http.StatusOK {
			t.Errorf("%s as house staff = %d %s, want 200", target, rec.Code, rec.Body)
		}
	}
	rec := serve(routes["/v1/staff/audit-log?tenant="+tenant+"&event="+event], "/v1/staff/audit-log?tenant="+tenant+"&event="+event,
		auth.Identity{Subject: house, Role: "authenticated", Staff: true})
	var body struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Events) != 1 || body.Events[0]["tenant_id"] != tenant ||
		body.Events[0]["tenant_name"] == nil || body.Events[0]["actor_kind"] != "system" || body.Events[0]["payload"] == nil {
		t.Errorf("staff search body = %s (%v), want one row of the tenant with name, kind and payload", rec.Body, err)
	}

	// A staff flag on a non-house subject still gets the store's 403: the gate is not the only guard.
	rec = httptest.NewRecorder()
	h := audit.StaffChangesHandler(store.StaffChanges, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/staff/staff-audit-log", nil)
	ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: customer, Role: "authenticated", Staff: true})
	auth.RequireStaff(h).ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), event) {
		t.Errorf("staff flag on a customer member = %d %s, want 403 from the read function", rec.Code, rec.Body)
	}
}
