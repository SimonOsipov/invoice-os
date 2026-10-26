package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const staffReadAuditLogSig = "public.staff_read_audit_log(uuid, uuid, text, text, timestamptz, timestamptz, timestamptz, bigint, int)"

// requireStaffReadAuditLog fails by name while the audit-search migration is not applied.
func requireStaffReadAuditLog(t *testing.T) {
	t.Helper()
	var reg *string
	if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, staffReadAuditLogSig).Scan(&reg); err != nil {
		t.Fatalf("read to_regprocedure %s: %v", staffReadAuditLogSig, err)
	}
	if reg == nil {
		t.Fatalf("function %s not found: the staff_read_audit_log migration is not applied", staffReadAuditLogSig)
	}
}

// auditSearch holds the nine arguments after p_actor; nil is NULL.
type auditSearch struct {
	tenant, event, subject *string
	from, to, beforeAt     *time.Time
	beforeID               *int64
	limit                  *int
}

type auditSearchRow struct {
	id                      int64
	tenant                  string
	tenantName              *string
	at                      time.Time
	event, actor            string
	memberName, memberEmail *string
	payload                 string
}

func sp(s string) *string       { return &s }
func tp(t time.Time) *time.Time { return &t }
func ip(i int) *int             { return &i }
func i64p(i int64) *int64       { return &i }

// call runs the function as invoice_app's pool would; SELECT * pins the result column order and count.
func (a auditSearch) call(q rowQuerier, actor string) ([]auditSearchRow, error) {
	rows, err := q.Query(context.Background(),
		`SELECT * FROM public.staff_read_audit_log($1::uuid, $2::uuid, $3::text, $4::text, $5::timestamptz, $6::timestamptz, $7::timestamptz, $8::bigint, $9::int)`,
		actor, a.tenant, a.event, a.subject, a.from, a.to, a.beforeAt, a.beforeID, a.limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (auditSearchRow, error) {
		var x auditSearchRow
		return x, r.Scan(&x.id, &x.tenant, &x.tenantName, &x.at, &x.event, &x.actor, &x.memberName, &x.memberEmail, &x.payload)
	})
}

func (a auditSearch) mustCall(t *testing.T, q rowQuerier, actor string) []auditSearchRow {
	t.Helper()
	got, err := a.call(q, actor)
	if err != nil {
		t.Fatalf("staff_read_audit_log: %v", err)
	}
	return got
}

type auditSeed struct {
	id                   int64
	tenant, actor, event string
	at                   time.Time
}

// purgeAuditRows deletes audit_log rows as the superuser; the append-only trigger refuses a plain DELETE, so it runs under 'replica'.
func purgeAuditRows(t *testing.T, where string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := h.super.Begin(ctx)
	if err != nil {
		t.Errorf("purge audit rows: begin: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = 'replica'`); err != nil {
		t.Errorf("purge audit rows: set replica role: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM audit_log WHERE `+where, args...); err != nil {
		t.Errorf("purge audit rows: %v", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("purge audit rows: commit: %v", err)
	}
}

// seedAuditRow commits one audit row at an explicit time and purges it when the test ends.
func seedAuditRow(t *testing.T, tenant, actor, event string, at time.Time) auditSeed {
	t.Helper()
	s := auditSeed{tenant: tenant, actor: actor, event: event, at: at.UTC().Truncate(time.Microsecond)}
	if err := h.super.QueryRow(context.Background(),
		`INSERT INTO audit_log (tenant_id, actor, event, payload, created_at) VALUES ($1, $2, $3, jsonb_build_object('marker', $3::text), $4) RETURNING id`,
		tenant, actor, event, s.at).Scan(&s.id); err != nil {
		t.Fatalf("seed an audit row: %v", err)
	}
	t.Cleanup(func() { purgeAuditRows(t, `id = $1`, s.id) })
	return s
}

func auditRowIDs(rows []auditSearchRow) []int64 {
	ids := []int64{}
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	return ids
}

// newestFirst orders seeds by created_at DESC, id DESC, the function's order.
func newestFirst(seeds []auditSeed) []int64 {
	s := append([]auditSeed(nil), seeds...)
	sort.Slice(s, func(i, j int) bool {
		if !s[i].at.Equal(s[j].at) {
			return s[i].at.After(s[j].at)
		}
		return s[i].id > s[j].id
	})
	ids := []int64{}
	for _, x := range s {
		ids = append(ids, x.id)
	}
	return ids
}

func freshName(prefix string) string { return prefix + "-" + uuid.NewString()[:8] }

// auditFutureBase is above any row another test or an earlier run leaves, so a NULL-filter call lists these rows first.
func auditFutureBase() time.Time {
	return time.Now().UTC().Add(72 * time.Hour).Truncate(time.Microsecond)
}

func TestRLS_StaffReadAuditLogExists(t *testing.T) {
	requireHarness(t)
	requireStaffReadAuditLog(t)
	var listed bool
	for _, fn := range staffReadFunctions(t) {
		if fn.name == "staff_read_audit_log" && len(fn.argTypes) == 9 {
			listed = true
		}
	}
	if !listed {
		t.Error("staff_read_audit_log with nine arguments is not among the public.staff_read_ functions in pg_proc")
	}
}

func TestRLS_StaffReadAuditLogListsEveryTenantNewestFirst(t *testing.T) {
	requireHarness(t)
	requireStaffReadAuditLog(t)
	staff := seedHouseMember(t, "reviewer", "active")
	nameA, nameB := freshName("audit list A"), freshName("audit list B")
	a, b := seedDetailTenant(t, nameA), seedDetailTenant(t, nameB)
	base := auditFutureBase()
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Microsecond) }
	ev := freshName("audit.list")

	// A holds a tie at +3 (a3 has the higher id); B's rows interleave with A's.
	a1 := seedAuditRow(t, a, "list-actor", ev, at(1))
	a2 := seedAuditRow(t, a, "list-actor", ev, at(3))
	a3 := seedAuditRow(t, a, "list-actor", ev, at(3))
	a4 := seedAuditRow(t, a, "list-actor", ev, at(5))
	b1 := seedAuditRow(t, b, "list-actor", ev, at(2))
	b2 := seedAuditRow(t, b, "list-actor", ev, at(4))

	for _, c := range []struct {
		tenant string
		name   string
		want   []int64
	}{
		{a, nameA, []int64{a4.id, a3.id, a2.id, a1.id}},
		{b, nameB, []int64{b2.id, b1.id}},
	} {
		got := auditSearch{tenant: sp(c.tenant)}.mustCall(t, h.app, staff)
		if !reflect.DeepEqual(auditRowIDs(got), c.want) {
			t.Errorf("tenant %s rows = %v, want %v (created_at DESC, id DESC)", c.name, auditRowIDs(got), c.want)
		}
		for _, r := range got {
			if r.tenant != c.tenant || r.tenantName == nil || *r.tenantName != c.name {
				t.Errorf("row %d: tenant = %s name %v, want %s %q", r.id, r.tenant, r.tenantName, c.tenant, c.name)
			}
			var p map[string]string
			if err := json.Unmarshal([]byte(r.payload), &p); err != nil || p["marker"] != ev {
				t.Errorf("row %d: payload = %s (err %v), want the stored marker %q", r.id, r.payload, err, ev)
			}
		}
	}

	all := auditSearch{}.mustCall(t, h.app, staff)
	want := []int64{a4.id, b2.id, a3.id, a2.id, b1.id, a1.id}
	if len(all) < len(want) || !reflect.DeepEqual(auditRowIDs(all[:len(want)]), want) {
		t.Fatalf("NULL filters: first rows = %v, want both tenants newest first %v", auditRowIDs(all), want)
	}
	for i := 1; i < len(all); i++ {
		if all[i].at.After(all[i-1].at) {
			t.Errorf("NULL filters: row %d created_at %v is after row %d %v, want non-increasing", i, all[i].at, i-1, all[i-1].at)
		}
	}
}

func TestRLS_StaffReadAuditLogCapsTheLimit(t *testing.T) {
	requireHarness(t)
	requireStaffReadAuditLog(t)
	staff := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, freshName("audit cap"))
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO audit_log (tenant_id, actor, event, created_at)
		 SELECT $1, 'cap-actor', 'audit.cap', $2::timestamptz + g * interval '1 second' FROM generate_series(1, 105) g`,
		x, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("seed 105 audit rows: %v", err)
	}
	t.Cleanup(func() { purgeAuditRows(t, `tenant_id = $1`, x) })
	if n := mustCount(t, h.super, `SELECT count(*) FROM audit_log WHERE tenant_id = $1`, x); n != 105 {
		t.Fatalf("seeded rows = %d, want 105", n)
	}
	var newest3 []int64
	rows, err := h.super.Query(context.Background(), `SELECT id FROM audit_log WHERE tenant_id = $1 ORDER BY created_at DESC, id DESC LIMIT 3`, x)
	if err == nil {
		newest3, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	}
	if err != nil || len(newest3) != 3 {
		t.Fatalf("read the newest three rows: %d rows, err %v", len(newest3), err)
	}

	for _, c := range []struct {
		name  string
		limit *int
		want  int
	}{
		{"p_limit 0", ip(0), 0}, {"p_limit 1", ip(1), 1}, {"p_limit 3", ip(3), 3}, {"p_limit 100", ip(100), 100},
		{"p_limit 101", ip(101), 101}, {"p_limit 500", ip(500), 101}, {"p_limit NULL", nil, 101},
	} {
		got := auditSearch{tenant: sp(x), limit: c.limit}.mustCall(t, h.app, staff)
		if len(got) != c.want {
			t.Errorf("%s: %d rows, want %d", c.name, len(got), c.want)
		}
		if c.name == "p_limit 3" && !reflect.DeepEqual(auditRowIDs(got), newest3) {
			t.Errorf("p_limit 3: rows = %v, want the newest three %v", auditRowIDs(got), newest3)
		}
	}
}

func TestRLS_StaffReadAuditLogFiltersNarrowAloneAndCombined(t *testing.T) {
	requireHarness(t)
	requireStaffReadAuditLog(t)
	staff := seedHouseMember(t, "reviewer", "active")
	a, b := seedDetailTenant(t, freshName("audit filter A")), seedDetailTenant(t, freshName("audit filter B"))
	e1, e2 := freshName("audit.filter.one"), freshName("audit.filter.two")
	x, y := freshName("actor-x"), freshName("actor-y")

	// A random hour in 2000-2005 keeps the closed windows below free of other rows.
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rand.Int63n(5*365*24)) * time.Hour)
	var times [4]time.Time
	for i := range times {
		times[i] = base.Add(time.Duration(i) * time.Hour)
	}
	var mine []auditSeed
	for _, tenant := range []string{a, b} {
		for _, event := range []string{e1, e2} {
			for _, actor := range []string{x, y} {
				for _, at := range times {
					mine = append(mine, seedAuditRow(t, tenant, actor, event, at))
				}
			}
		}
	}

	want := func(keep func(auditSeed) bool) []int64 {
		var sel []auditSeed
		for _, s := range mine {
			if keep(s) {
				sel = append(sel, s)
			}
		}
		return newestFirst(sel)
	}
	for _, c := range []struct {
		name string
		args auditSearch
		keep func(auditSeed) bool
	}{
		{"tenant A", auditSearch{tenant: sp(a)}, func(s auditSeed) bool { return s.tenant == a }},
		{"tenant B", auditSearch{tenant: sp(b)}, func(s auditSeed) bool { return s.tenant == b }},
		{"event 1", auditSearch{event: sp(e1)}, func(s auditSeed) bool { return s.event == e1 }},
		{"event 2", auditSearch{event: sp(e2)}, func(s auditSeed) bool { return s.event == e2 }},
		{"actor X", auditSearch{subject: sp(x)}, func(s auditSeed) bool { return s.actor == x }},
		{"actor Y", auditSearch{subject: sp(y)}, func(s auditSeed) bool { return s.actor == y }},
		{"tenant B and actor Y", auditSearch{tenant: sp(b), subject: sp(y)}, func(s auditSeed) bool { return s.tenant == b && s.actor == y }},
		{"from t1 to t2 (both inclusive)", auditSearch{from: tp(times[1]), to: tp(times[2])}, func(s auditSeed) bool { return !s.at.Before(times[1]) && !s.at.After(times[2]) }},
		{"from = to = t1", auditSearch{from: tp(times[1]), to: tp(times[1])}, func(s auditSeed) bool { return s.at.Equal(times[1]) }},
		{"tenant A, event 1, actor X, t1 to t2", auditSearch{tenant: sp(a), event: sp(e1), subject: sp(x), from: tp(times[1]), to: tp(times[2])},
			func(s auditSeed) bool {
				return s.tenant == a && s.event == e1 && s.actor == x && !s.at.Before(times[1]) && !s.at.After(times[2])
			}},
	} {
		w := want(c.keep)
		if len(w) == 0 {
			t.Fatalf("%s: the expected set is empty, the case proves nothing", c.name)
		}
		if got := auditRowIDs(c.args.mustCall(t, h.app, staff)); !reflect.DeepEqual(got, w) {
			t.Errorf("%s: rows = %v, want %v", c.name, got, w)
		}
	}

	// A lone bound also matches rows of other tests, so the oracle is the same predicate in plain SQL.
	for _, c := range []struct {
		name  string
		args  auditSearch
		where string
		at    time.Time
	}{
		{"from t3 alone", auditSearch{from: tp(times[3])}, `created_at >= $1`, times[3]},
		{"to t0 alone", auditSearch{to: tp(times[0])}, `created_at <= $1`, times[0]},
	} {
		got := c.args.mustCall(t, h.app, staff)
		if len(got) == 0 {
			t.Errorf("%s: no rows", c.name)
		}
		rows, err := h.super.Query(context.Background(), `SELECT id FROM audit_log WHERE `+c.where+` ORDER BY created_at DESC, id DESC LIMIT 101`, c.at)
		var oracle []int64
		if err == nil {
			oracle, err = pgx.CollectRows(rows, pgx.RowTo[int64])
		}
		if err != nil {
			t.Fatalf("%s oracle: %v", c.name, err)
		}
		if !reflect.DeepEqual(auditRowIDs(got), oracle) {
			t.Errorf("%s: rows = %v, want the oracle %v", c.name, auditRowIDs(got), oracle)
		}
	}
	if all := (auditSearch{tenant: sp(a), event: sp(e1)}).mustCall(t, h.app, staff); len(all) != 8 {
		t.Errorf("tenant A and event 1: %d rows, want 8 (2 actors x 4 times)", len(all))
	}

	// Event and actor match the stored text exactly: case, prefix, wildcard and quote variants find nothing.
	for _, c := range []struct {
		name string
		args auditSearch
	}{
		{"event in upper case", auditSearch{tenant: sp(a), event: sp(strings.ToUpper(e1))}},
		{"event prefix", auditSearch{tenant: sp(a), event: sp(e1[:len(e1)-1])}},
		{"event wildcard", auditSearch{tenant: sp(a), event: sp("%")}},
		{"event quote", auditSearch{tenant: sp(a), event: sp("' OR true --")}},
		{"actor in upper case", auditSearch{tenant: sp(a), subject: sp(strings.ToUpper(x))}},
		{"actor prefix", auditSearch{tenant: sp(a), subject: sp(x[:len(x)-1])}},
		{"actor wildcard", auditSearch{tenant: sp(a), subject: sp("%")}},
		{"actor trailing space", auditSearch{tenant: sp(a), subject: sp(x + " ")}},
		{"from after to", auditSearch{tenant: sp(a), from: tp(times[2]), to: tp(times[1])}},
	} {
		if got := c.args.mustCall(t, h.app, staff); len(got) != 0 {
			t.Errorf("%s: %d rows, want 0", c.name, len(got))
		}
	}
}

func TestRLS_StaffReadAuditLogKeysetTiesBreakById(t *testing.T) {
	requireHarness(t)
	requireStaffReadAuditLog(t)
	staff := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, freshName("audit keyset"))
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	r2 := seedAuditRow(t, x, "keyset", "audit.keyset", t1)
	r3 := seedAuditRow(t, x, "keyset", "audit.keyset", t1)
	r1 := seedAuditRow(t, x, "keyset", "audit.keyset", t1.Add(time.Minute))
	if r3.id <= r2.id {
		t.Fatalf("seeded ids r2 %d, r3 %d, want r3 > r2 (the tie must be settled by id)", r2.id, r3.id)
	}
	order := []auditSeed{r1, r3, r2}

	// Walk the log one row at a time from each returned row.
	var walked []int64
	args := auditSearch{tenant: sp(x), limit: ip(1)}
	for i := 0; i < 5; i++ {
		page := args.mustCall(t, h.app, staff)
		if len(page) == 0 {
			break
		}
		if len(page) != 1 {
			t.Fatalf("page %d: %d rows, want 1", i, len(page))
		}
		walked = append(walked, page[0].id)
		args.beforeAt, args.beforeID = tp(page[0].at), i64p(page[0].id)
	}
	if want := []int64{r1.id, r3.id, r2.id}; !reflect.DeepEqual(walked, want) {
		t.Errorf("walk of 1-row pages = %v, want %v with no repeat and no gap", walked, want)
	}

	for _, c := range []struct {
		name   string
		at     time.Time
		id     int64
		wantID []int64
	}{
		{"cursor at the newest row", r1.at, r1.id, []int64{r3.id, r2.id}},
		{"cursor at the higher tied id", r3.at, r3.id, []int64{r2.id}},
		{"cursor at the lower tied id", r2.at, r2.id, []int64{}},
		{"cursor above both tied ids", r2.at, r3.id + 1000, []int64{r3.id, r2.id}},
	} {
		got := auditSearch{tenant: sp(x), beforeAt: tp(c.at), beforeID: i64p(c.id)}.mustCall(t, h.app, staff)
		if !reflect.DeepEqual(auditRowIDs(got), c.wantID) {
			t.Errorf("%s: rows = %v, want %v (strictly before the position)", c.name, auditRowIDs(got), c.wantID)
		}
	}
	if full := auditRowIDs(auditSearch{tenant: sp(x)}.mustCall(t, h.app, staff)); !reflect.DeepEqual(full, newestFirst(order)) {
		t.Errorf("control: full page = %v, want %v", full, newestFirst(order))
	}
}

func TestRLS_StaffReadAuditLogNamesMembersOfTheRowsTenantOnly(t *testing.T) {
	requireHarness(t)
	requireStaffReadAuditLog(t)
	staff := seedHouseMember(t, "reviewer", "active")
	a, b := seedDetailTenant(t, freshName("audit names A")), seedDetailTenant(t, freshName("audit names B"))
	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	uEmail, vEmail := freshName("ada")+"@example.test", freshName("v")+"@example.test"
	u := seedMemberAt(t, a, "admin", "active", now, sp("Ada"), sp(uEmail))
	v := seedMemberAt(t, b, "admin", "active", now, nil, sp(vEmail))
	upperU := strings.ToUpper(u)
	if upperU == u {
		t.Fatalf("member id %s has no letter to upper-case", u)
	}

	ev := freshName("audit.names")
	at := now
	for _, r := range []struct{ tenant, actor string }{
		{a, u}, {a, v}, {a, "system"}, {a, "extraction-worker"}, {a, upperU}, {b, v},
	} {
		at = at.Add(time.Second)
		seedAuditRow(t, r.tenant, r.actor, ev, at)
	}

	byActor := func(tenant string) map[string]auditSearchRow {
		m := map[string]auditSearchRow{}
		for _, r := range (auditSearch{tenant: sp(tenant), event: sp(ev)}).mustCall(t, h.app, staff) {
			m[r.actor] = r
		}
		return m
	}
	inA, inB := byActor(a), byActor(b)
	if len(inA) != 5 || len(inB) != 1 {
		t.Fatalf("rows by actor: tenant A %d, tenant B %d, want 5 and 1", len(inA), len(inB))
	}

	check := func(what string, r auditSearchRow, name, email *string) {
		t.Helper()
		if !reflect.DeepEqual(r.memberName, name) || !reflect.DeepEqual(r.memberEmail, email) {
			t.Errorf("%s: member_name %v, member_email %v, want %v and %v", what, derefOrNil(r.memberName), derefOrNil(r.memberEmail), derefOrNil(name), derefOrNil(email))
		}
	}
	check("A row by its member U", inA[u], sp("Ada"), sp(uEmail))
	check("A row by V, a member only of B", inA[v], nil, nil)
	check("A row by system", inA["system"], nil, nil)
	check("A row by a free-text actor", inA["extraction-worker"], nil, nil)
	check("A row by U in upper case (not the stored text)", inA[upperU], nil, nil)
	check("B row by V", inB[v], nil, sp(vEmail))
}

func TestRLS_StaffReadAuditLogKeepsARowWhoseTenantIsGone(t *testing.T) {
	requireHarness(t)
	requireStaffReadAuditLog(t)
	staff := seedHouseMember(t, "reviewer", "active")
	gone, live := uuid.NewString(), seedDetailTenant(t, freshName("audit gone control"))
	if n := mustCount(t, h.super, `SELECT count(*) FROM tenants WHERE id = $1`, gone); n != 0 {
		t.Fatalf("tenant %s exists, want none", gone)
	}
	seedAuditRow(t, gone, "gone-actor", "audit.gone", time.Now().UTC().Add(-time.Hour))
	seedAuditRow(t, live, "gone-actor", "audit.gone", time.Now().UTC().Add(-time.Hour))

	got := auditSearch{tenant: sp(gone)}.mustCall(t, h.app, staff)
	if len(got) != 1 {
		t.Fatalf("rows of a tenant with no tenants row = %d, want 1 (an inner join drops it)", len(got))
	}
	if got[0].tenantName != nil || got[0].tenant != gone {
		t.Errorf("row = tenant %s name %v, want %s and NULL", got[0].tenant, derefOrNil(got[0].tenantName), gone)
	}
	if control := (auditSearch{tenant: sp(live)}).mustCall(t, h.app, staff); len(control) != 1 || control[0].tenantName == nil {
		t.Errorf("control: a live tenant's row has name %v over %d rows, want 1 row with a name", control, len(control))
	}
}

// Plan test for the real statement, captured with auto_explain: a hand-copied EXPLAIN would drift from the function.
var (
	planStartRE   = regexp.MustCompile(`(?m)^\s*Limit\b`)
	planSortRE    = regexp.MustCompile(`\bSort\b`)
	planAuditScan = regexp.MustCompile(`(?:Index Scan|Index Only Scan) using audit_log_\w+ on audit_log|Bitmap Index Scan on audit_log_\w+`)
	planTenantIdx = regexp.MustCompile(`(?:Index Scan|Index Only Scan) using audit_log_tenant_\w+ on audit_log|Bitmap Index Scan on audit_log_tenant_\w+`)
	planAuditStmt = regexp.MustCompile(`public\.audit_log\b`)
)

const (
	planTenants       = 20
	planRowsPerTenant = 1000
	planMembers       = 5
	planThinTenants   = 1480
)

type auditPlanCorpus struct {
	tenants      []string
	member       string
	common, rare string
}

// seedAuditPlanCorpus inserts 20 tenants x 1,000 audit rows over 90 days, plus 1,480 one-row tenants, in tx and analyzes the tables.
// The thin tenants take the table past 1,500 distinct tenant ids, where the planner stops using the tenant-leading skip scan.
// The caller rolls tx back: audit_log is append-only, and leftover rows would push the table past the audit migrations' 500,000-row ceiling.
func seedAuditPlanCorpus(t *testing.T, tx pgx.Tx) auditPlanCorpus {
	t.Helper()
	ctx := context.Background()
	c := auditPlanCorpus{common: freshName("audit.plan.common"), rare: freshName("audit.plan.rare")}
	others := []string{freshName("audit.plan.o1"), freshName("audit.plan.o2"), freshName("audit.plan.o3"), freshName("audit.plan.o4")}
	for n := 0; n < planTenants; n++ {
		tenant := uuid.NewString()
		txExec(t, tx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`, tenant, fmt.Sprintf("plan corpus %02d", n))
		var members []string
		for k := 0; k < planMembers; k++ {
			user := uuid.NewString()
			txExec(t, tx, `INSERT INTO memberships (tenant_id, user_id, role, status, display_name, email) VALUES ($1, $2, 'admin', 'active', $3, $4)`,
				tenant, user, fmt.Sprintf("Plan %d.%d", n, k), fmt.Sprintf("p%d-%d@example.test", n, k))
			members = append(members, user)
		}
		if n == 0 {
			c.member = members[0]
		}
		c.tenants = append(c.tenants, tenant)
		// g=1 is the one rare row; 4 of every 10 rows are the common event; every 6th row is system.
		txExec(t, tx,
			`INSERT INTO audit_log (tenant_id, actor, event, payload, created_at)
			 SELECT $1::uuid,
			        CASE WHEN g % 6 = 0 THEN 'system' ELSE ($2::text[])[1 + g % 5] END,
			        CASE WHEN g = 1 THEN $4::text WHEN g % 10 < 4 THEN $3::text ELSE ($5::text[])[1 + g % 4] END,
			        '{}'::jsonb,
			        now() - make_interval(secs => g * 7776 + $6::int)
			   FROM generate_series(1, $7::int) g`,
			tenant, members, c.common, c.rare, others, n*37, planRowsPerTenant)
	}

	txExec(t, tx,
		`INSERT INTO audit_log (tenant_id, actor, event, payload, created_at)
		 SELECT gen_random_uuid(), 'plan-thin', ($1::text[])[1 + k % 4], '{}'::jsonb, now() - make_interval(secs => k * 5254)
		   FROM generate_series(1, $2::int) k`, others, planThinTenants)

	var rows, tenants, common, rare, system, members, allTenants int
	if err := tx.QueryRow(ctx,
		`SELECT count(*), count(DISTINCT tenant_id), count(*) FILTER (WHERE event = $2), count(*) FILTER (WHERE event = $3),
		        count(*) FILTER (WHERE actor = 'system'), (SELECT count(*) FROM memberships WHERE tenant_id = ANY ($1)),
		        (SELECT count(DISTINCT tenant_id) FROM audit_log)
		   FROM audit_log WHERE tenant_id = ANY ($1)`, c.tenants, c.common, c.rare).Scan(&rows, &tenants, &common, &rare, &system, &members, &allTenants); err != nil {
		t.Fatalf("read the corpus size: %v", err)
	}
	if rows != planTenants*planRowsPerTenant || tenants != planTenants || members != planTenants*planMembers {
		t.Fatalf("corpus = %d rows, %d tenants, %d members, want %d, %d, %d", rows, tenants, members, planTenants*planRowsPerTenant, planTenants, planTenants*planMembers)
	}
	if common*10 < rows*3 || rare != planTenants || system*100 < rows*15 {
		t.Fatalf("corpus event mix = common %d, rare %d, system %d of %d rows, want common >= 30%%, rare %d, system >= 15%%", common, rare, system, rows, planTenants)
	}
	if allTenants < planTenants+planThinTenants {
		t.Fatalf("corpus holds %d distinct tenants in audit_log, want >= %d", allTenants, planTenants+planThinTenants)
	}
	txExec(t, tx, `ANALYZE audit_log`)
	txExec(t, tx, `ANALYZE memberships, tenants`)
	return c
}

func TestRLS_StaffReadAuditLogPlansReadAnIndex(t *testing.T) {
	requireHarness(t)
	requireStaffReadAuditLog(t)
	ctx := context.Background()
	superDSN, _ := migratedScratchDB(t, migrations.FS)

	cfg, err := pgx.ParseConfig(superDSN)
	if err != nil {
		t.Fatalf("parse the scratch superuser dsn: %v", err)
	}
	var notices []string
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) { notices = append(notices, n.Message) }
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect as the superuser: %v", err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A migrated scratch database has no house_company row; the singleton key allows this one.
	house := uuid.NewString()
	txExec(t, tx, `INSERT INTO tenants (id, name) VALUES ($1, 'house (plan scratch)')`, house)
	txExec(t, tx, `INSERT INTO public.house_company (tenant_id) VALUES ($1)`, house)
	staff, _ := insertMembership(t, tx, house, "reviewer", "active")

	c := seedAuditPlanCorpus(t, tx)
	for _, stmt := range []string{
		`LOAD 'auto_explain'`, `SET auto_explain.log_min_duration = 0`, `SET auto_explain.log_nested_statements = on`,
		`SET auto_explain.log_level = notice`, `SET ROLE invoice_app`,
	} {
		txExec(t, tx, stmt)
	}

	// run returns the rows and the plan of the function's own statement.
	run := func(name string, a auditSearch) ([]auditSearchRow, string) {
		t.Helper()
		notices = nil
		rows, err := a.call(tx, staff)
		if err != nil {
			t.Fatalf("%s: staff_read_audit_log: %v", name, err)
		}
		if len(notices) == 0 {
			t.Fatalf("%s: auto_explain captured nothing", name)
		}
		var houseCheck bool
		var plans []string
		for _, n := range notices {
			houseCheck = houseCheck || strings.Contains(n, "house_company")
			if planAuditStmt.MatchString(n) {
				plans = append(plans, n)
			}
		}
		if !houseCheck {
			t.Fatalf("%s: auto_explain captured nothing of the is_house_member statement, so it did not capture nested statements:\n%s", name, strings.Join(notices, "\n"))
		}
		if len(plans) != 1 {
			t.Fatalf("%s: %d captured statements read public.audit_log, want 1:\n%s", name, len(plans), strings.Join(notices, "\n"))
		}
		loc := planStartRE.FindStringIndex(plans[0])
		if loc == nil {
			t.Fatalf("%s: the captured statement has no Limit node:\n%s", name, plans[0])
		}
		return rows, plans[0][loc[0]:]
	}

	first, _ := run("first page", auditSearch{limit: ip(51)})
	if len(first) != 51 {
		t.Fatalf("first page = %d rows, want 51", len(first))
	}
	row51 := first[50]
	now := time.Now().UTC()
	weekAgo := now.Add(-7 * 24 * time.Hour)

	for _, tc := range []struct {
		name       string
		args       auditSearch
		wantRows   int
		createdIdx bool
		tenantIdx  bool
		cond       *regexp.Regexp
	}{
		{"a: no filter, limit 51", auditSearch{limit: ip(51)}, 51, true, false, nil},
		{"b: cursor at row 51", auditSearch{limit: ip(51), beforeAt: tp(row51.at), beforeID: i64p(row51.id)}, 51, true, false, nil},
		{"c: last seven days", auditSearch{limit: ip(51), from: tp(weekAgo), to: tp(now)}, 51, true, false, nil},
		{"d: one tenant", auditSearch{limit: ip(51), tenant: sp(c.tenants[3])}, 51, false, true, nil},
		{"e: the common event", auditSearch{limit: ip(51), event: sp(c.common)}, 51, false, false, nil},
		{"f: the rare event", auditSearch{limit: ip(51), event: sp(c.rare)}, planTenants, false, false, regexp.MustCompile(`Index Cond: \(event = `)},
		{"g: one member's uuid", auditSearch{limit: ip(51), subject: sp(c.member)}, 51, false, false, regexp.MustCompile(`Index Cond: \(actor = `)},
		{"h: system", auditSearch{limit: ip(51), subject: sp("system")}, 51, false, false, nil},
		{"i: one tenant and the rare event", auditSearch{limit: ip(51), tenant: sp(c.tenants[3]), event: sp(c.rare)}, 1, false, true,
			regexp.MustCompile(`Index Cond: \(\(tenant_id = '[0-9a-f-]+'::uuid\) AND \(event = `)},
		{"j: one tenant and a member", auditSearch{limit: ip(51), tenant: sp(c.tenants[0]), subject: sp(c.member)}, 51, false, true,
			regexp.MustCompile(`Index Cond: \(\(tenant_id = '[0-9a-f-]+'::uuid\) AND \(actor = `)},
	} {
		rows, plan := run(tc.name, tc.args)
		if len(rows) != tc.wantRows {
			t.Errorf("%s: %d rows, want %d", tc.name, len(rows), tc.wantRows)
		}
		if strings.Contains(plan, "Seq Scan on audit_log") {
			t.Errorf("%s: plan has a Seq Scan on audit_log:\n%s", tc.name, plan)
		}
		switch {
		case tc.createdIdx:
			if !strings.Contains(plan, "Index Scan using audit_log_created_idx on audit_log a") {
				t.Errorf("%s: plan does not read audit_log_created_idx:\n%s", tc.name, plan)
			}
			if planSortRE.MatchString(plan) {
				t.Errorf("%s: plan has a Sort node:\n%s", tc.name, plan)
			}
		case tc.tenantIdx:
			if !planTenantIdx.MatchString(plan) {
				t.Errorf("%s: plan does not read a tenant-leading audit_log index:\n%s", tc.name, plan)
			}
		default:
			if !planAuditScan.MatchString(plan) {
				t.Errorf("%s: plan does not read an audit_log_ index:\n%s", tc.name, plan)
			}
		}
		if tc.cond != nil && !tc.cond.MatchString(plan) {
			t.Errorf("%s: plan has no %s:\n%s", tc.name, tc.cond, plan)
		}
	}
}

// staffAuditSearchMigrationVersion is the one migration that creates staff_read_audit_log.
func staffAuditSearchMigrationVersion(t *testing.T) int64 {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	for _, f := range files {
		raw, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(raw), "CREATE FUNCTION public.staff_read_audit_log") {
			v, err := strconv.ParseInt(strings.SplitN(f, "_", 2)[0], 10, 64)
			if err != nil {
				t.Fatalf("parse goose version out of %q: %v", f, err)
			}
			return v
		}
	}
	t.Fatal("no migration holds CREATE FUNCTION public.staff_read_audit_log: the audit-search migration is not shipped")
	return 0
}

type auditSearchFootprint struct {
	function     bool
	indexDef     string
	grants       []string
	policies     int
	invoiceAppEx bool
}

func readAuditSearchFootprint(t *testing.T) auditSearchFootprint {
	t.Helper()
	ctx := context.Background()
	var f auditSearchFootprint
	var def *string
	if err := h.super.QueryRow(ctx,
		`SELECT to_regprocedure($1) IS NOT NULL, (SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'audit_log_created_idx'),
		        (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'audit_log' AND policyname = 'staff_read_lookup'),
		        coalesce(has_function_privilege('invoice_app', to_regprocedure($1), 'EXECUTE'), false)`,
		staffReadAuditLogSig).Scan(&f.function, &def, &f.policies, &f.invoiceAppEx); err != nil {
		t.Fatalf("read the audit-search footprint: %v", err)
	}
	if def != nil {
		f.indexDef = *def
	}
	f.grants = staffReaderColumnGrants(t)["public.audit_log"]
	return f
}

var (
	auditSearchGrantsUp   = []string{"actor/SELECT", "created_at/SELECT", "event/SELECT", "id/SELECT", "payload/SELECT", "tenant_id/SELECT"}
	auditSearchGrantsDown = []string{"created_at/SELECT", "tenant_id/SELECT"}
)

// Rolls back this migration and any newer staff_read_ one, newest first: the audit_log grants and policy must survive.
func TestRLS_StaffReadAuditLogDownAndUp(t *testing.T) {
	requireHarness(t)
	version := staffAuditSearchMigrationVersion(t)
	var versions []int64
	for _, v := range staffReadMigrationVersions(t) {
		if v >= version {
			versions = append(versions, v)
		}
	}
	staff := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, freshName("audit down and up"))
	row := seedAuditRow(t, x, "du-actor", "audit.downup", time.Now().UTC().Add(-time.Hour))

	requireUp := func(when string) {
		t.Helper()
		f := readAuditSearchFootprint(t)
		if !f.function || !f.invoiceAppEx || !strings.Contains(f.indexDef, "(created_at DESC, id DESC)") ||
			!reflect.DeepEqual(f.grants, auditSearchGrantsUp) || f.policies != 1 {
			t.Errorf("%s: footprint = %+v, want the function with invoice_app EXECUTE, index (created_at DESC, id DESC), six audit_log grants and one staff_read_lookup policy", when, f)
		}
	}
	requireUp("before the Down")

	reapply := rollBackMigrations(t, staffMigrationProvider(t), versions...)
	if f := readAuditSearchFootprint(t); f.function || f.indexDef != "" || !reflect.DeepEqual(f.grants, auditSearchGrantsDown) || f.policies != 1 {
		t.Errorf("after the Down: footprint = %+v, want no function, no audit_log_created_idx, grants %v and the staff_read_lookup policy kept", f, auditSearchGrantsDown)
	}
	if f, p, g := staffDetailFootprintOf(t); f != 3 || p != 3 || g != 14 {
		t.Errorf("after the Down: staff audit footprint = %d functions, %d policies, %d column grants, want 3, 3, 14", f, p, g)
	}

	reapply()
	requireUp("after the Up")
	var found bool
	for _, r := range (auditSearch{tenant: sp(x)}).mustCall(t, h.app, staff) {
		found = found || r.id == row.id
	}
	if !found {
		t.Error("after the Up: staff_read_audit_log does not list the seeded row for a house member")
	}
}
