package db_test

import (
	"context"
	"io/fs"
	"math/rand"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const (
	staffReadAICallsSig      = "public.staff_read_ai_calls(uuid, uuid, text, text, boolean, timestamptz, timestamptz, timestamptz, uuid, int)"
	staffReadAICallTotalsSig = "public.staff_read_ai_call_totals(uuid, uuid, text, text, timestamptz, timestamptz, date, text, uuid, int)"
)

var staffAICallsGrants = []string{
	"attempts/SELECT", "client/SELECT", "cost/SELECT", "created_at/SELECT", "id/SELECT", "input_tokens/SELECT",
	"latency_ms/SELECT", "model/SELECT", "outcome/SELECT", "output_tokens/SELECT", "purpose/SELECT", "tenant_id/SELECT",
}

// requireStaffReadAICalls fails by name while the staff_read_ai_calls migration is not applied.
func requireStaffReadAICalls(t *testing.T) {
	t.Helper()
	for _, sig := range []string{staffReadAICallsSig, staffReadAICallTotalsSig} {
		var reg *string
		if err := h.super.QueryRow(context.Background(), `SELECT to_regprocedure($1)::text`, sig).Scan(&reg); err != nil {
			t.Fatalf("read to_regprocedure %s: %v", sig, err)
		}
		if reg == nil {
			t.Fatalf("function %s not found: the staff_read_ai_calls migration is not applied", sig)
		}
	}
}

// staffAIFarBase is a random hour in 2000-2005: no other test writes ai_calls rows there, so closed windows are exact.
func staffAIFarBase() time.Time {
	return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(rand.Int63n(5*365*24)) * time.Hour)
}

func staffAIFarDay() time.Time {
	return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, rand.Intn(5*365))
}

func staffAIBool(b bool) *bool { return &b }

type staffAISeed struct {
	id, tenant, client, purpose, model, cost, outcome string
	in, out, latency                                  int64
	attempts                                          int
	at                                                time.Time
}

func staffAIRow(tenant, purpose, outcome string, at time.Time) staffAISeed {
	return staffAISeed{tenant: tenant, client: "ai", purpose: purpose, model: "vendor/model-a", cost: "0.001",
		outcome: outcome, in: 10, out: 5, latency: 120, attempts: 1, at: at.UTC().Truncate(time.Microsecond)}
}

// seedStaffAICall commits one ai_calls row as the superuser and deletes it when the test ends.
func seedStaffAICall(t *testing.T, s staffAISeed) staffAISeed {
	t.Helper()
	if err := h.super.QueryRow(context.Background(),
		`INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::text::numeric, $8, $9, $10, $11) RETURNING id::text`,
		s.tenant, s.client, s.purpose, s.model, s.in, s.out, s.cost, s.latency, s.attempts, s.outcome, s.at).Scan(&s.id); err != nil {
		t.Fatalf("seed an ai_calls row: %v", err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM ai_calls WHERE id = $1`, s.id) })
	return s
}

func staffAINewestFirst(seeds []staffAISeed) []string {
	s := append([]staffAISeed(nil), seeds...)
	sort.Slice(s, func(i, j int) bool {
		if !s[i].at.Equal(s[j].at) {
			return s[i].at.After(s[j].at)
		}
		return s[i].id > s[j].id
	})
	ids := []string{}
	for _, x := range s {
		ids = append(ids, x.id)
	}
	return ids
}

func staffAISelect(seeds []staffAISeed, keep func(staffAISeed) bool) []staffAISeed {
	var out []staffAISeed
	for _, s := range seeds {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

type staffAICallRow struct {
	id, tenant, tenantName, client, purpose, model, cost, outcome string
	at                                                            time.Time
	in, out, latency                                              int64
	attempts                                                      int
}

func staffAIRowIDs(rows []staffAICallRow) []string {
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	return ids
}

// staffAICallsArgs holds the nine arguments after p_actor; nil is NULL.
type staffAICallsArgs struct {
	tenant, purpose, outcome *string
	failed                   *bool
	from, to, beforeAt       *time.Time
	beforeID                 *string
	limit                    *int
}

// call names the result columns: a rename or a dropped column fails the scan.
func (a staffAICallsArgs) mustCall(t *testing.T, q rowQuerier, actor string) []staffAICallRow {
	t.Helper()
	rows, err := q.Query(context.Background(),
		`SELECT id::text, tenant_id::text, tenant_name, created_at, client, purpose, model, input_tokens, output_tokens, cost::text, latency_ms, attempts, outcome
		   FROM public.staff_read_ai_calls($1::uuid, $2::uuid, $3::text, $4::text, $5::boolean, $6::timestamptz, $7::timestamptz, $8::timestamptz, $9::uuid, $10::int)`,
		actor, a.tenant, a.purpose, a.outcome, a.failed, a.from, a.to, a.beforeAt, a.beforeID, a.limit)
	if err != nil {
		t.Fatalf("staff_read_ai_calls: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (staffAICallRow, error) {
		var x staffAICallRow
		return x, r.Scan(&x.id, &x.tenant, &x.tenantName, &x.at, &x.client, &x.purpose, &x.model, &x.in, &x.out, &x.cost, &x.latency, &x.attempts, &x.outcome)
	})
	if err != nil {
		t.Fatalf("collect staff_read_ai_calls rows: %v", err)
	}
	return got
}

type staffAITotalRow struct {
	day, tenant, tenantName, cost string
	calls, in, out, failed        int64
}

// staffAITotalsArgs holds the nine arguments after p_actor; nil is NULL and beforeDay is YYYY-MM-DD.
type staffAITotalsArgs struct {
	tenant, purpose, outcome            *string
	from, to                            *time.Time
	beforeDay, beforeName, beforeTenant *string
	limit                               *int
}

func (a staffAITotalsArgs) mustCall(t *testing.T, q rowQuerier, actor string) []staffAITotalRow {
	t.Helper()
	rows, err := q.Query(context.Background(),
		`SELECT day::text, tenant_id::text, tenant_name, calls, input_tokens, output_tokens, cost::text, failed
		   FROM public.staff_read_ai_call_totals($1::uuid, $2::uuid, $3::text, $4::text, $5::timestamptz, $6::timestamptz, $7::text::date, $8::text, $9::uuid, $10::int)`,
		actor, a.tenant, a.purpose, a.outcome, a.from, a.to, a.beforeDay, a.beforeName, a.beforeTenant, a.limit)
	if err != nil {
		t.Fatalf("staff_read_ai_call_totals: %v", err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (staffAITotalRow, error) {
		var x staffAITotalRow
		return x, r.Scan(&x.day, &x.tenant, &x.tenantName, &x.calls, &x.in, &x.out, &x.cost, &x.failed)
	})
	if err != nil {
		t.Fatalf("collect staff_read_ai_call_totals rows: %v", err)
	}
	return got
}

func staffAIDay(d time.Time) string { return d.Format("2006-01-02") }

func TestRLS_StaffReadAICallsFunctionsExist(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	listed := map[string]bool{}
	for _, fn := range staffReadFunctions(t) {
		if len(fn.argTypes) == 10 {
			listed[fn.name] = true
		}
	}
	for _, name := range []string{"staff_read_ai_calls", "staff_read_ai_call_totals"} {
		if !listed[name] {
			t.Errorf("%s with ten arguments is not among the public.staff_read_ functions in pg_proc", name)
		}
	}
}

func TestRLS_StaffReadAICallsListsEveryTenantNewestFirst(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	staff := seedHouseMember(t, "reviewer", "active")
	nameA, nameB := freshName("ai list A"), freshName("ai list B")
	a, b := seedDetailTenant(t, nameA), seedDetailTenant(t, nameB)
	base := staffAIFarBase()
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Second) }
	purpose := freshName("staff-list")

	mk := func(tenant, client, model, cost, outcome string, in, out, latency int64, attempts int, when time.Time) staffAISeed {
		s := staffAIRow(tenant, purpose, outcome, when)
		s.client, s.model, s.cost, s.in, s.out, s.latency, s.attempts = client, model, cost, in, out, latency, attempts
		return seedStaffAICall(t, s)
	}
	a1 := mk(a, "ai", "vendor/model-a", "0.0014741", "ok", 3447, 176, 812, 1, at(1))
	a2 := mk(a, "jev", "vendor/model-b", "0.25", "unavailable", 9000000000, 0, 15000, 3, at(3))
	a3 := mk(a, "ai", "", "0", "fake", 0, 0, 0, 1, at(5))
	b1 := mk(b, "jev", "vendor/model-c", "1.5", "ok", 12, 34, 56, 2, at(2))
	b2 := mk(b, "ai", "vendor/model-a", "0.0000001", "refused", 1, 1, 1, 1, at(4))
	all := []staffAISeed{a1, a2, a3, b1, b2}
	byID := map[string]staffAISeed{}
	for _, s := range all {
		byID[s.id] = s
	}

	for _, c := range []struct {
		tenant, name string
		want         []string
	}{
		{a, nameA, []string{a3.id, a2.id, a1.id}},
		{b, nameB, []string{b2.id, b1.id}},
	} {
		got := staffAICallsArgs{tenant: sp(c.tenant)}.mustCall(t, h.app, staff)
		if !reflect.DeepEqual(staffAIRowIDs(got), c.want) {
			t.Errorf("tenant %s rows = %v, want %v (created_at DESC, id DESC)", c.name, staffAIRowIDs(got), c.want)
		}
		for _, r := range got {
			s := byID[r.id]
			if r.tenant != c.tenant || r.tenantName != c.name {
				t.Errorf("row %s: tenant = %s name %q, want %s %q", r.id, r.tenant, r.tenantName, c.tenant, c.name)
			}
			if r.client != s.client || r.purpose != s.purpose || r.model != s.model || r.in != s.in || r.out != s.out ||
				r.cost != s.cost || r.latency != s.latency || r.attempts != s.attempts || r.outcome != s.outcome || !r.at.Equal(s.at) {
				t.Errorf("row %s = %+v, want the inserted values %+v", r.id, r, s)
			}
		}
	}

	bracket := staffAICallsArgs{from: tp(at(0)), to: tp(at(10))}.mustCall(t, h.app, staff)
	if want := staffAINewestFirst(all); !reflect.DeepEqual(staffAIRowIDs(bracket), want) {
		t.Errorf("rows bracketed by p_from/p_to = %v, want both tenants newest first %v", staffAIRowIDs(bracket), want)
	}
}

func TestRLS_StaffReadAICallsCapsTheLimit(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	staff := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, freshName("ai cap"))
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome, created_at)
		 SELECT $1, 'ai', 'staff-cap', '', 0, 0, 0, 0, 1, 'ok', $2::timestamptz + g * interval '1 second' FROM generate_series(1, 105) g`,
		x, staffAIFarBase()); err != nil {
		t.Fatalf("seed 105 ai_calls rows: %v", err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM ai_calls WHERE tenant_id = $1`, x) })
	if n := mustCount(t, h.super, `SELECT count(*) FROM ai_calls WHERE tenant_id = $1`, x); n != 105 {
		t.Fatalf("seeded rows = %d, want 105", n)
	}
	rows, err := h.super.Query(context.Background(), `SELECT id::text FROM ai_calls WHERE tenant_id = $1 ORDER BY created_at DESC, id DESC LIMIT 3`, x)
	var newest3 []string
	if err == nil {
		newest3, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil || len(newest3) != 3 {
		t.Fatalf("read the newest three rows: %d rows, err %v", len(newest3), err)
	}

	for _, c := range []struct {
		name  string
		limit *int
		want  int
	}{
		{"p_limit 1", ip(1), 1}, {"p_limit 3", ip(3), 3}, {"p_limit 100", ip(100), 100},
		{"p_limit 101", ip(101), 101}, {"p_limit 500", ip(500), 101}, {"p_limit NULL", nil, 101},
	} {
		got := staffAICallsArgs{tenant: sp(x), limit: c.limit}.mustCall(t, h.app, staff)
		if len(got) != c.want {
			t.Errorf("%s: %d rows, want %d", c.name, len(got), c.want)
		}
		if c.name == "p_limit 3" && !reflect.DeepEqual(staffAIRowIDs(got), newest3) {
			t.Errorf("p_limit 3: rows = %v, want the newest three %v", staffAIRowIDs(got), newest3)
		}
	}
}

func TestRLS_StaffReadAICallsFiltersNarrowAloneAndCombined(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	staff := seedHouseMember(t, "reviewer", "active")
	a, b := seedDetailTenant(t, freshName("ai filter A")), seedDetailTenant(t, freshName("ai filter B"))
	p1, p2 := freshName("staff-p1"), freshName("staff-p2")
	outcomes := []string{"ok", "unavailable", "skipped_refused", "fake"}
	base := staffAIFarBase()
	var times [4]time.Time
	for i := range times {
		times[i] = base.Add(time.Duration(i) * time.Hour)
	}
	var mine []staffAISeed
	for _, tenant := range []string{a, b} {
		for _, purpose := range []string{p1, p2} {
			for _, outcome := range outcomes {
				for _, at := range times {
					mine = append(mine, seedStaffAICall(t, staffAIRow(tenant, purpose, outcome, at)))
				}
			}
		}
	}
	inWindow := func(s staffAISeed, from, to time.Time) bool { return !s.at.Before(from) && !s.at.After(to) }

	for _, c := range []struct {
		name string
		args staffAICallsArgs
		keep func(staffAISeed) bool
	}{
		{"tenant A", staffAICallsArgs{tenant: sp(a)}, func(s staffAISeed) bool { return s.tenant == a }},
		{"tenant B", staffAICallsArgs{tenant: sp(b)}, func(s staffAISeed) bool { return s.tenant == b }},
		{"purpose 1", staffAICallsArgs{purpose: sp(p1)}, func(s staffAISeed) bool { return s.purpose == p1 }},
		{"purpose 2", staffAICallsArgs{purpose: sp(p2)}, func(s staffAISeed) bool { return s.purpose == p2 }},
		{"from t1 to t2 (both inclusive)", staffAICallsArgs{from: tp(times[1]), to: tp(times[2])}, func(s staffAISeed) bool { return inWindow(s, times[1], times[2]) }},
		{"from = to = t1", staffAICallsArgs{from: tp(times[1]), to: tp(times[1])}, func(s staffAISeed) bool { return s.at.Equal(times[1]) }},
		{"outcome unavailable in the window", staffAICallsArgs{outcome: sp("unavailable"), from: tp(times[0]), to: tp(times[3])},
			func(s staffAISeed) bool { return s.outcome == "unavailable" }},
		{"tenant B, purpose 2, outcome fake, t1 to t2", staffAICallsArgs{tenant: sp(b), purpose: sp(p2), outcome: sp("fake"), from: tp(times[1]), to: tp(times[2])},
			func(s staffAISeed) bool {
				return s.tenant == b && s.purpose == p2 && s.outcome == "fake" && inWindow(s, times[1], times[2])
			}},
	} {
		want := staffAINewestFirst(staffAISelect(mine, c.keep))
		if len(want) == 0 {
			t.Fatalf("%s: the expected set is empty, the case proves nothing", c.name)
		}
		if got := staffAIRowIDs(c.args.mustCall(t, h.app, staff)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: rows = %v, want %v", c.name, got, want)
		}
	}

	// A lone outcome or a lone bound also matches rows of other tests, so the oracle is the same predicate in plain SQL.
	for _, c := range []struct {
		name  string
		args  staffAICallsArgs
		where string
		arg   any
	}{
		{"outcome skipped_refused alone", staffAICallsArgs{outcome: sp("skipped_refused")}, `outcome = $1`, "skipped_refused"},
		{"p_from alone", staffAICallsArgs{from: tp(times[2])}, `created_at >= $1`, times[2]},
		{"p_to alone", staffAICallsArgs{to: tp(times[1])}, `created_at <= $1`, times[1]},
	} {
		rows, err := h.super.Query(context.Background(), `SELECT id::text FROM ai_calls WHERE `+c.where+` ORDER BY created_at DESC, id DESC LIMIT 101`, c.arg)
		var want []string
		if err == nil {
			want, err = pgx.CollectRows(rows, pgx.RowTo[string])
		}
		if err != nil || len(want) == 0 {
			t.Fatalf("%s: oracle read: %d rows, err %v", c.name, len(want), err)
		}
		if got := staffAIRowIDs(c.args.mustCall(t, h.app, staff)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: rows = %v, want the oracle's %v", c.name, got, want)
		}
	}
}

func TestRLS_StaffReadAICallsFailedKeepsEveryOutcomeButOk(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	staff := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, freshName("ai failed"))
	base := staffAIFarBase()
	purpose := freshName("staff-failed")
	byOutcome := map[string]staffAISeed{}
	var all []staffAISeed
	for i, outcome := range []string{"ok", "unavailable", "skipped_refused", "fake"} {
		s := seedStaffAICall(t, staffAIRow(x, purpose, outcome, base.Add(time.Duration(i)*time.Second)))
		byOutcome[outcome] = s
		all = append(all, s)
	}
	failed := []staffAISeed{byOutcome["unavailable"], byOutcome["skipped_refused"], byOutcome["fake"]}

	if got, want := staffAIRowIDs(staffAICallsArgs{tenant: sp(x), failed: staffAIBool(true)}.mustCall(t, h.app, staff)), staffAINewestFirst(failed); !reflect.DeepEqual(got, want) {
		t.Errorf("p_failed true: rows = %v, want the three non-ok rows %v (fake included)", got, want)
	}
	for name, f := range map[string]*bool{"false": staffAIBool(false), "NULL": nil} {
		if got, want := staffAIRowIDs(staffAICallsArgs{tenant: sp(x), failed: f}.mustCall(t, h.app, staff)), staffAINewestFirst(all); !reflect.DeepEqual(got, want) {
			t.Errorf("p_failed %s: rows = %v, want all four %v", name, got, want)
		}
	}
}

func TestRLS_StaffReadAICallsKeysetTiesBreakById(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	staff := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, freshName("ai keyset"))
	base := staffAIFarBase()
	purpose := freshName("staff-keyset")
	r1 := seedStaffAICall(t, staffAIRow(x, purpose, "ok", base))
	r2 := seedStaffAICall(t, staffAIRow(x, purpose, "ok", base.Add(time.Second)))
	r3 := seedStaffAICall(t, staffAIRow(x, purpose, "ok", base.Add(time.Second)))
	want := staffAINewestFirst([]staffAISeed{r1, r2, r3})

	var got []string
	args := staffAICallsArgs{tenant: sp(x), limit: ip(1)}
	for page := 0; page < 5; page++ {
		rows := args.mustCall(t, h.app, staff)
		if len(rows) == 0 {
			break
		}
		if len(rows) != 1 {
			t.Fatalf("page %d holds %d rows, want 1", page, len(rows))
		}
		got = append(got, rows[0].id)
		args.beforeAt, args.beforeID = tp(rows[0].at), sp(rows[0].id)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keyset walk = %v, want the three rows once each in order %v", got, want)
	}
}

// Two fixtures share one fresh purpose, so a call scoped by it and a window sees nothing else.
type staffAITotalsFixture struct {
	purpose, a, b, nameA, nameB string
	day, prevDay                time.Time
}

func seedStaffAITotalsFixture(t *testing.T) staffAITotalsFixture {
	t.Helper()
	f := staffAITotalsFixture{purpose: freshName("staff-totals"), nameA: freshName("ai totals A"), nameB: freshName("ai totals B"), day: staffAIFarDay()}
	f.a, f.b = seedDetailTenant(t, f.nameA), seedDetailTenant(t, f.nameB)
	f.prevDay = f.day.AddDate(0, 0, -1)
	row := func(tenant, outcome, cost string, in, out int64, at time.Time) {
		s := staffAIRow(tenant, f.purpose, outcome, at)
		s.cost, s.in, s.out = cost, in, out
		seedStaffAICall(t, s)
	}
	row(f.a, "ok", "0.0014741", 3447, 176, f.day.Add(-500*time.Millisecond))
	row(f.a, "unavailable", "0.0010054", 1000, 0, f.day)
	row(f.a, "fake", "0", 0, 0, f.day.Add(8*time.Hour))
	row(f.b, "ok", "0.0002", 500, 50, f.day.Add(9*time.Hour))
	return f
}

func (f staffAITotalsFixture) window() (from, to *time.Time) {
	return tp(f.prevDay), tp(f.day.Add(24*time.Hour - time.Second))
}

func TestRLS_StaffReadAICallTotalsGroupsByUTCDayAndTenant(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	staff := seedHouseMember(t, "reviewer", "active")
	f := seedStaffAITotalsFixture(t)
	from, to := f.window()
	d, pd := staffAIDay(f.day), staffAIDay(f.prevDay)

	aDay := staffAITotalRow{day: d, tenant: f.a, tenantName: f.nameA, calls: 2, in: 1000, out: 0, cost: "0.0010054", failed: 2}
	aPrev := staffAITotalRow{day: pd, tenant: f.a, tenantName: f.nameA, calls: 1, in: 3447, out: 176, cost: "0.0014741", failed: 0}
	bDay := staffAITotalRow{day: d, tenant: f.b, tenantName: f.nameB, calls: 1, in: 500, out: 50, cost: "0.0002", failed: 0}

	for _, c := range []struct {
		name string
		args staffAITotalsArgs
		want []staffAITotalRow
	}{
		{"both days, both tenants", staffAITotalsArgs{purpose: sp(f.purpose), from: from, to: to}, []staffAITotalRow{aDay, bDay, aPrev}},
		{"tenant A", staffAITotalsArgs{tenant: sp(f.a)}, []staffAITotalRow{aDay, aPrev}},
		{"tenant B", staffAITotalsArgs{tenant: sp(f.b)}, []staffAITotalRow{bDay}},
	} {
		if got := c.args.mustCall(t, h.app, staff); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: groups =\n%+v\nwant (day DESC, tenant_name, tenant_id)\n%+v", c.name, got, c.want)
		}
	}
}

func TestRLS_StaffReadAICallTotalsFiltersNarrow(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	staff := seedHouseMember(t, "reviewer", "active")
	f := seedStaffAITotalsFixture(t)
	from, to := f.window()
	d, pd := staffAIDay(f.day), staffAIDay(f.prevDay)
	other := freshName("staff-totals-other")
	s := staffAIRow(f.a, other, "ok", f.day.Add(12*time.Hour))
	s.cost, s.in, s.out = "0.5", 7, 7
	seedStaffAICall(t, s)

	for _, c := range []struct {
		name string
		args staffAITotalsArgs
		want []staffAITotalRow
	}{
		{"purpose of the fixture", staffAITotalsArgs{purpose: sp(f.purpose)}, []staffAITotalRow{
			{d, f.a, f.nameA, "0.0010054", 2, 1000, 0, 2},
			{d, f.b, f.nameB, "0.0002", 1, 500, 50, 0},
			{pd, f.a, f.nameA, "0.0014741", 1, 3447, 176, 0},
		}},
		{"the other purpose", staffAITotalsArgs{purpose: sp(other)}, []staffAITotalRow{{d, f.a, f.nameA, "0.5", 1, 7, 7, 0}}},
		{"outcome unavailable", staffAITotalsArgs{outcome: sp("unavailable"), from: from, to: to}, []staffAITotalRow{{d, f.a, f.nameA, "0.0010054", 1, 1000, 0, 1}}},
		{"outcome fake", staffAITotalsArgs{outcome: sp("fake"), from: from, to: to}, []staffAITotalRow{{d, f.a, f.nameA, "0", 1, 0, 0, 1}}},
		{"tenant B", staffAITotalsArgs{tenant: sp(f.b)}, []staffAITotalRow{{d, f.b, f.nameB, "0.0002", 1, 500, 50, 0}}},
		{"tenant A, fixture purpose, outcome ok", staffAITotalsArgs{tenant: sp(f.a), purpose: sp(f.purpose), outcome: sp("ok")}, []staffAITotalRow{{pd, f.a, f.nameA, "0.0014741", 1, 3447, 176, 0}}},
	} {
		if len(c.want) == 0 {
			t.Fatalf("%s: the expected set is empty, the case proves nothing", c.name)
		}
		if got := c.args.mustCall(t, h.app, staff); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: groups =\n%+v\nwant\n%+v", c.name, got, c.want)
		}
	}
}

func TestRLS_StaffReadAICallTotalsCapsTheLimit(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	staff := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, freshName("ai totals cap"))
	first := staffAIFarDay()
	if _, err := h.super.Exec(context.Background(),
		`INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome, created_at)
		 SELECT $1, 'ai', 'staff-cap', '', 0, 0, 0, 0, 1, 'ok', $2::timestamptz + g * interval '1 day' FROM generate_series(1, 105) g`,
		x, first); err != nil {
		t.Fatalf("seed 105 ai_calls rows: %v", err)
	}
	t.Cleanup(func() { _, _ = h.super.Exec(context.Background(), `DELETE FROM ai_calls WHERE tenant_id = $1`, x) })
	if n := mustCount(t, h.super, `SELECT count(DISTINCT created_at::date) FROM ai_calls WHERE tenant_id = $1`, x); n != 105 {
		t.Fatalf("seeded distinct days = %d, want 105", n)
	}

	for _, c := range []struct {
		name  string
		limit *int
		want  int
	}{
		{"p_limit 3", ip(3), 3}, {"p_limit 100", ip(100), 100}, {"p_limit 500", ip(500), 101}, {"p_limit NULL", nil, 101},
	} {
		got := staffAITotalsArgs{tenant: sp(x), limit: c.limit}.mustCall(t, h.app, staff)
		if len(got) != c.want {
			t.Errorf("%s: %d groups, want %d", c.name, len(got), c.want)
		}
		if c.name == "p_limit 3" {
			var days []string
			for _, r := range got {
				days = append(days, r.day)
			}
			want := []string{staffAIDay(first.AddDate(0, 0, 105)), staffAIDay(first.AddDate(0, 0, 104)), staffAIDay(first.AddDate(0, 0, 103))}
			if !reflect.DeepEqual(days, want) {
				t.Errorf("p_limit 3: days = %v, want the newest three %v", days, want)
			}
		}
	}
}

func TestRLS_StaffReadAICallTotalsKeysetReachesEveryGroup(t *testing.T) {
	requireHarness(t)
	requireStaffReadAICalls(t)
	staff := seedHouseMember(t, "reviewer", "active")
	purpose := freshName("staff-keyset-totals")
	alphaName, sameName := freshName("Alpha"), freshName("Same")
	alpha := seedDetailTenant(t, alphaName)
	s1, s2 := seedDetailTenant(t, sameName), seedDetailTenant(t, sameName)
	low, high := s1, s2
	if low > high {
		low, high = high, low
	}
	day1 := staffAIFarDay()
	day2 := day1.AddDate(0, 0, 1)
	for _, tenant := range []string{alpha, low, high} {
		for _, day := range []time.Time{day1, day2} {
			seedStaffAICall(t, staffAIRow(tenant, purpose, "ok", day.Add(10*time.Hour)))
		}
	}
	g := func(day time.Time, tenant, name string) string { return staffAIDay(day) + "/" + name + "/" + tenant }
	order := []string{
		g(day2, alpha, alphaName), g(day2, low, sameName), g(day2, high, sameName),
		g(day1, alpha, alphaName), g(day1, low, sameName), g(day1, high, sameName),
	}
	key := func(r staffAITotalRow) string { return g(mustDay(t, r.day), r.tenant, r.tenantName) }
	keys := func(rows []staffAITotalRow) []string {
		out := []string{}
		for _, r := range rows {
			out = append(out, key(r))
		}
		return out
	}
	after := func(r staffAITotalRow) staffAITotalsArgs {
		return staffAITotalsArgs{purpose: sp(purpose), beforeDay: sp(r.day), beforeName: sp(r.tenantName), beforeTenant: sp(r.tenant)}
	}

	page1 := staffAITotalsArgs{purpose: sp(purpose), limit: ip(4)}.mustCall(t, h.app, staff)
	if !reflect.DeepEqual(keys(page1), order[:4]) {
		t.Fatalf("page 1 = %v, want %v", keys(page1), order[:4])
	}
	page2 := after(page1[3]).mustCall(t, h.app, staff)
	if !reflect.DeepEqual(keys(page2), order[4:]) {
		t.Errorf("page 2 after %s = %v, want %v", key(page1[3]), keys(page2), order[4:])
	}

	// A page of one cuts between the two tenants that share a name, where a cursor without the tenant id repeats or skips one.
	var walk []string
	args := staffAITotalsArgs{purpose: sp(purpose), limit: ip(1)}
	for i := 0; i < 8; i++ {
		rows := args.mustCall(t, h.app, staff)
		if len(rows) == 0 {
			break
		}
		walk = append(walk, key(rows[0]))
		args = after(rows[0])
		args.limit = ip(1)
	}
	if !reflect.DeepEqual(walk, order) {
		t.Errorf("walk of pages of one = %v, want all six groups once each %v", walk, order)
	}
}

func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("parse day %q: %v", s, err)
	}
	return d
}

func TestRLS_StaffReaderWritesNothingOnAICalls(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tenant := aiTenants(t, 1)[0]
	row := seedAICall(t, tenant)
	tx := revokeProbeTx(t)
	txExec(t, tx, `SELECT set_config('app.current_tenant', '', true)`)
	txExec(t, tx, `SET LOCAL ROLE staff_reader`)

	// Positive half: without the grant and the policy every statement below is refused for the wrong reason.
	rows, err := tx.Query(ctx, `SELECT id::text FROM ai_calls WHERE tenant_id = $1`, tenant)
	if err != nil {
		t.Fatalf("staff_reader SELECT id FROM ai_calls: %v", err)
	}
	seen, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || !reflect.DeepEqual(seen, []string{row}) {
		t.Fatalf("staff_reader sees ai_calls rows %v (err %v), want the seeded row [%s]", seen, err, row)
	}

	for _, w := range []struct{ what, sql string }{
		{"INSERT", `INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome)
			VALUES ('` + tenant + `', 'ai', 'probe', '', 0, 0, 0, 0, 1, 'ok')`},
		{"UPDATE", `UPDATE ai_calls SET outcome = 'probe' WHERE id = '` + row + `'`},
		{"DELETE", `DELETE FROM ai_calls WHERE id = '` + row + `'`},
	} {
		txExec(t, tx, `SAVEPOINT w`)
		_, err := tx.Exec(ctx, w.sql)
		txExec(t, tx, `ROLLBACK TO SAVEPOINT w`)
		assertPgRefusal(t, "staff_reader "+w.what+" on ai_calls", err, sqlstateInsufficientPrivilege, "permission denied for table ai_calls")
	}
}

// staffAICallsMigrationVersion is the one migration that creates staff_read_ai_calls.
func staffAICallsMigrationVersion(t *testing.T) int64 {
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
		if strings.Contains(string(raw), "CREATE FUNCTION public.staff_read_ai_calls(") {
			v, err := strconv.ParseInt(strings.SplitN(f, "_", 2)[0], 10, 64)
			if err != nil {
				t.Fatalf("parse goose version out of %q: %v", f, err)
			}
			return v
		}
	}
	t.Fatal("no migration holds CREATE FUNCTION public.staff_read_ai_calls: the staff_read_ai_calls migration is not shipped")
	return 0
}

type staffAICallsFootprint struct {
	functions, appExecute           int
	createdIdx, tenantCreatedIdx    string
	grants                          []string
	lookupPolicies, isolationPolicy int
	appSelect, appInsert            bool
	appUpdate, appDelete            bool
}

func readStaffAICallsFootprint(t *testing.T) staffAICallsFootprint {
	t.Helper()
	ctx := context.Background()
	var f staffAICallsFootprint
	if err := h.super.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM unnest($1::text[]) s WHERE to_regprocedure(s) IS NOT NULL),
		       (SELECT count(*) FROM unnest($1::text[]) s WHERE to_regprocedure(s) IS NOT NULL AND has_function_privilege('invoice_app', to_regprocedure(s), 'EXECUTE')),
		       coalesce((SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'ai_calls_created_idx'), ''),
		       coalesce((SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = 'ai_calls_tenant_created_idx'), ''),
		       (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'ai_calls' AND policyname = 'staff_read_lookup'),
		       (SELECT count(*) FROM pg_policies WHERE schemaname = 'public' AND tablename = 'ai_calls' AND policyname = 'tenant_isolation'),
		       has_table_privilege('invoice_app', 'public.ai_calls', 'SELECT'), has_table_privilege('invoice_app', 'public.ai_calls', 'INSERT'),
		       has_table_privilege('invoice_app', 'public.ai_calls', 'UPDATE'), has_table_privilege('invoice_app', 'public.ai_calls', 'DELETE')`,
		[]string{staffReadAICallsSig, staffReadAICallTotalsSig}).Scan(&f.functions, &f.appExecute, &f.createdIdx, &f.tenantCreatedIdx,
		&f.lookupPolicies, &f.isolationPolicy, &f.appSelect, &f.appInsert, &f.appUpdate, &f.appDelete); err != nil {
		t.Fatalf("read the staff_read_ai_calls footprint: %v", err)
	}
	f.grants = staffReaderColumnGrants(t)["public.ai_calls"]
	return f
}

// Rolls back this migration and any newer staff_read_ one, newest first: tenant_isolation and invoice_app's privileges must survive.
func TestRLS_StaffReadAICallsDownAndUp(t *testing.T) {
	requireHarness(t)
	version := staffAICallsMigrationVersion(t)
	var versions []int64
	for _, v := range staffReadMigrationVersions(t) {
		if v >= version {
			versions = append(versions, v)
		}
	}
	staff := seedHouseMember(t, "reviewer", "active")
	x := seedDetailTenant(t, freshName("ai down and up"))
	row := seedStaffAICall(t, staffAIRow(x, freshName("staff-downup"), "ok", staffAIFarBase()))

	requireUp := func(when string) {
		t.Helper()
		f := readStaffAICallsFootprint(t)
		if f.functions != 2 || f.appExecute != 2 || !strings.Contains(f.createdIdx, "(created_at DESC, id DESC)") ||
			!strings.Contains(f.tenantCreatedIdx, "(tenant_id, created_at DESC, id DESC)") ||
			!reflect.DeepEqual(sortedCopy(f.grants), staffAICallsGrants) || f.lookupPolicies != 1 || f.isolationPolicy != 1 ||
			!f.appSelect || !f.appInsert || f.appUpdate || f.appDelete {
			t.Errorf("%s: footprint = %+v, want 2 functions with invoice_app EXECUTE, both indexes, 12 grants, one staff_read_lookup, tenant_isolation, invoice_app SELECT and INSERT only", when, f)
		}
	}
	requireUp("before the Down")

	reapply := rollBackMigrations(t, staffMigrationProvider(t), versions...)
	f := readStaffAICallsFootprint(t)
	if f.functions != 0 || f.appExecute != 0 || f.createdIdx != "" || f.tenantCreatedIdx != "" || len(f.grants) != 0 || f.lookupPolicies != 0 ||
		f.isolationPolicy != 1 || !f.appSelect || !f.appInsert {
		t.Errorf("after the Down: footprint = %+v, want no function, no index, no staff_reader grant, no staff_read_lookup, and tenant_isolation with invoice_app SELECT and INSERT kept", f)
	}

	reapply()
	requireUp("after the Up")
	var found bool
	for _, r := range (staffAICallsArgs{tenant: sp(x)}).mustCall(t, h.app, staff) {
		found = found || r.id == row.id
	}
	if !found {
		t.Error("after the Up: staff_read_ai_calls does not list the seeded row for a house member")
	}
}
