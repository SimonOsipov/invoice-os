package aicalls

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const devHouseTenant = "a5c0a5c0-a5c0-a5c0-a5c0-a5c0a5c0a5c0"

// farDay is a UTC midnight in 2000-2004, clear of any real row.
func farDay() time.Time {
	return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, rand.IntN(5*365))
}

func (e env) namedTenant(t *testing.T, name string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := e.super.Exec(context.Background(), `INSERT INTO tenants (id, name) VALUES ($1, $2)`, id, name); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.super.Exec(context.Background(), `DELETE FROM ai_calls WHERE tenant_id = $1`, id)
		_, _ = e.super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})
	return id
}

type seed struct {
	tenant, purpose, outcome, cost string
	in, out                        int64
	at                             time.Time
}

func (e env) insert(t *testing.T, s seed) {
	t.Helper()
	if s.cost == "" {
		s.cost = "0.001"
	}
	if _, err := e.super.Exec(context.Background(),
		`INSERT INTO ai_calls (tenant_id, client, purpose, model, input_tokens, output_tokens, cost, latency_ms, attempts, outcome, created_at)
		 VALUES ($1, 'ai', $2, 'vendor/model-a', $3, $4, $5::text::numeric, 120, 1, $6, $7)`,
		s.tenant, s.purpose, s.in, s.out, s.cost, s.outcome, s.at.UTC().Truncate(time.Microsecond)); err != nil {
		t.Fatalf("seed ai_calls: %v", err)
	}
}

func (e env) house(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var house string
	err := e.super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&house)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := e.super.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, 'ASComply (dev)') ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the dev house tenant: %v", err)
		}
		if _, err := e.super.Exec(ctx, `INSERT INTO public.house_company (tenant_id) VALUES ($1) ON CONFLICT DO NOTHING`, devHouseTenant); err != nil {
			t.Fatalf("seed the house row: %v", err)
		}
		err = e.super.QueryRow(ctx, `SELECT tenant_id::text FROM public.house_company`).Scan(&house)
	}
	if err != nil {
		t.Fatalf("read the house row: %v", err)
	}
	return house
}

func (e env) member(t *testing.T, tenant string) string {
	t.Helper()
	subject := uuid.NewString()
	if _, err := e.super.Exec(context.Background(),
		`INSERT INTO memberships (tenant_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active')`, tenant, subject); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.super.Exec(context.Background(), `DELETE FROM memberships WHERE tenant_id = $1 AND user_id = $2`, tenant, subject)
	})
	return subject
}

// staffCtx returns the context auth.RequireStaff builds for a staff caller: the key is private to auth.
func staffCtx(subject string) context.Context {
	var got context.Context
	h := auth.RequireStaff(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Context() }))
	ctx := auth.WithTenantlessCaller(context.Background(), auth.Identity{Subject: subject, Role: "authenticated", Staff: true})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/staff/ai-calls", nil).WithContext(ctx))
	return got
}

func (e env) staff(t *testing.T) context.Context {
	t.Helper()
	return staffCtx(e.member(t, e.house(t)))
}

func ids(calls []Call) []string {
	out := []string{}
	for _, c := range calls {
		out = append(out, c.ID)
	}
	return out
}

func TestStaffCalls_ReadsEveryTenantAndPages(t *testing.T) {
	e := newEnv(t)
	st := NewStore(e.app)
	ctx := e.staff(t)
	purpose := uniquePurpose()
	nameA, nameB := "aicalls staff A "+uuid.NewString(), "aicalls staff B "+uuid.NewString()
	a, b := e.namedTenant(t, nameA), e.namedTenant(t, nameB)
	day := farDay()
	for i := 0; i < 3; i++ {
		s := seed{tenant: a, purpose: purpose, outcome: "ok", at: day.Add(time.Duration(i) * time.Hour)}
		if i == 0 {
			s.cost = "0.0014741"
		}
		e.insert(t, s)
	}
	for i := 0; i < 2; i++ {
		e.insert(t, seed{tenant: b, purpose: purpose, outcome: "ok", at: day.Add(time.Duration(i)*time.Hour + 30*time.Minute)})
	}

	first, err := st.StaffCalls(ctx, Filter{Tenant: a, Limit: 2})
	if err != nil {
		t.Fatalf("StaffCalls: %v", err)
	}
	if len(first.Calls) != 2 || !first.Page.HasMore || first.Page.NextCursor == nil || first.Page.Limit != 2 {
		t.Fatalf("first page = %+v, want 2 rows, has_more and a cursor", first)
	}
	cur, err := DecodeCursor(*first.Page.NextCursor)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	second, err := st.StaffCalls(ctx, Filter{Tenant: a, Limit: 2, Cursor: &cur})
	if err != nil {
		t.Fatalf("StaffCalls with cursor: %v", err)
	}
	if len(second.Calls) != 1 || second.Page.HasMore || second.Page.NextCursor != nil {
		t.Fatalf("second page = %+v, want the third row and no more", second)
	}
	paged := append(ids(first.Calls), ids(second.Calls)...)
	if len(paged) != 3 || paged[0] == paged[1] || paged[1] == paged[2] || paged[0] == paged[2] {
		t.Errorf("pages overlap: %v", paged)
	}
	if !first.Calls[0].CreatedAt.After(first.Calls[1].CreatedAt) || !first.Calls[1].CreatedAt.After(second.Calls[0].CreatedAt) {
		t.Errorf("pages are not newest first: %v %v %v", first.Calls[0].CreatedAt, first.Calls[1].CreatedAt, second.Calls[0].CreatedAt)
	}

	all, err := st.StaffCalls(ctx, Filter{Purpose: purpose, From: day, To: day.Add(24 * time.Hour), Limit: 100})
	if err != nil {
		t.Fatalf("StaffCalls across tenants: %v", err)
	}
	if len(all.Calls) != 5 || all.Page.HasMore {
		t.Fatalf("across tenants = %d rows, has_more %v, want 5 and false", len(all.Calls), all.Page.HasMore)
	}
	names := map[string]int{}
	var costSeen bool
	for i, c := range all.Calls {
		names[c.TenantName]++
		if i > 0 && c.CreatedAt.After(all.Calls[i-1].CreatedAt) {
			t.Errorf("row %d is newer than row %d", i, i-1)
		}
		if c.Cost == "0.0014741" {
			costSeen = true
		}
		if c.Client != "ai" || c.Purpose != purpose || c.Model != "vendor/model-a" || c.InputTokens != 0 || c.LatencyMS != 120 || c.Attempts != 1 || c.Outcome != "ok" {
			t.Errorf("row %d = %+v", i, c)
		}
	}
	if names[nameA] != 3 || names[nameB] != 2 {
		t.Errorf("tenant names = %v, want 3 of A and 2 of B", names)
	}
	if !costSeen {
		t.Error(`no row has cost "0.0014741"`)
	}
}

func TestStaffCalls_HasMoreAtTheExactLimit(t *testing.T) {
	e := newEnv(t)
	st := NewStore(e.app)
	ctx := e.staff(t)
	tenant, purpose, day := e.namedTenant(t, "aicalls exact "+uuid.NewString()), uniquePurpose(), farDay()
	for i := 0; i < 2; i++ {
		e.insert(t, seed{tenant: tenant, purpose: purpose, outcome: "ok", at: day.Add(time.Duration(i) * time.Hour)})
	}
	p, err := st.StaffCalls(ctx, Filter{Tenant: tenant, Limit: 2})
	if err != nil || len(p.Calls) != 2 || p.Page.HasMore || p.Page.NextCursor != nil {
		t.Fatalf("exact limit = %+v, %v, want 2 rows, no more, no cursor", p, err)
	}
	e.insert(t, seed{tenant: tenant, purpose: purpose, outcome: "ok", at: day.Add(2 * time.Hour)})
	p, err = st.StaffCalls(ctx, Filter{Tenant: tenant, Limit: 2})
	if err != nil || len(p.Calls) != 2 || !p.Page.HasMore || p.Page.NextCursor == nil {
		t.Fatalf("one past the limit = %+v, %v, want 2 rows and more", p, err)
	}
}

func TestStaffCalls_EmptyAndUnknownTenant(t *testing.T) {
	e := newEnv(t)
	st := NewStore(e.app)
	ctx := e.staff(t)
	for _, tenant := range []string{e.namedTenant(t, "aicalls empty "+uuid.NewString()), uuid.NewString()} {
		p, err := st.StaffCalls(ctx, Filter{Tenant: tenant, Limit: 25})
		if err != nil || p.Calls == nil || len(p.Calls) != 0 || p.Page.HasMore {
			t.Errorf("tenant %s: %+v, %v, want an empty non-nil page", tenant, p, err)
		}
	}
}

func TestStaffCalls_FailedAndFiltersNarrow(t *testing.T) {
	e := newEnv(t)
	st := NewStore(e.app)
	ctx := e.staff(t)
	tenant, day := e.namedTenant(t, "aicalls failed "+uuid.NewString()), farDay()
	p1, p2 := uniquePurpose(), uniquePurpose()
	for i, s := range []seed{
		{purpose: p1, outcome: "ok"}, {purpose: p1, outcome: "refused"},
		{purpose: p2, outcome: "skipped_unavailable"}, {purpose: p2, outcome: "fake"},
	} {
		s.tenant, s.at = tenant, day.Add(time.Duration(i)*time.Hour)
		e.insert(t, s)
	}
	outcomes := func(f Filter) []string {
		f.Tenant, f.Limit = tenant, 100
		p, err := st.StaffCalls(ctx, f)
		if err != nil {
			t.Fatalf("StaffCalls(%+v): %v", f, err)
		}
		out := []string{}
		for _, c := range p.Calls {
			out = append(out, c.Outcome)
		}
		slices.Sort(out)
		return out
	}
	for name, tc := range map[string]struct {
		f    Filter
		want []string
	}{
		"failed":          {Filter{Failed: true}, []string{"fake", "refused", "skipped_unavailable"}},
		"purpose":         {Filter{Purpose: p1}, []string{"ok", "refused"}},
		"outcome":         {Filter{Outcome: "refused"}, []string{"refused"}},
		"outcome ok":      {Filter{Outcome: "ok"}, []string{"ok"}},
		"failed, purpose": {Filter{Failed: true, Purpose: p2}, []string{"fake", "skipped_unavailable"}},
	} {
		if got := outcomes(tc.f); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: outcomes = %v, want %v", name, got, tc.want)
		}
	}
}

func TestStaffTotals_GroupsByUTCDayAndTenant(t *testing.T) {
	e := newEnv(t)
	st := NewStore(e.app)
	ctx := e.staff(t)
	purpose, day := uniquePurpose(), farDay()
	prev := day.AddDate(0, 0, -1)
	nameA, nameB := "aicalls totals A "+uuid.NewString(), "aicalls totals B "+uuid.NewString()
	a, b := e.namedTenant(t, nameA), e.namedTenant(t, nameB)
	e.insert(t, seed{tenant: a, purpose: purpose, outcome: "ok", cost: "0.0014741", in: 3447, out: 176, at: day.Add(-500 * time.Millisecond)})
	e.insert(t, seed{tenant: a, purpose: purpose, outcome: "unavailable", cost: "0.0010054", in: 1000, at: day})
	e.insert(t, seed{tenant: a, purpose: purpose, outcome: "fake", cost: "0", at: day.Add(8 * time.Hour)})
	e.insert(t, seed{tenant: b, purpose: purpose, outcome: "ok", cost: "0.0002", in: 500, out: 50, at: day.Add(9 * time.Hour)})

	p, err := st.StaffTotals(ctx, Filter{Purpose: purpose, From: prev, To: day.Add(24*time.Hour - time.Second), Limit: 25})
	if err != nil {
		t.Fatalf("StaffTotals: %v", err)
	}
	d, pd := day.Format(dayLayout), prev.Format(dayLayout)
	want := []Total{
		{Day: d, TenantID: a, TenantName: nameA, Calls: 2, InputTokens: 1000, Cost: "0.0010054", Failed: 2},
		{Day: d, TenantID: b, TenantName: nameB, Calls: 1, InputTokens: 500, OutputTokens: 50, Cost: "0.0002"},
		{Day: pd, TenantID: a, TenantName: nameA, Calls: 1, InputTokens: 3447, OutputTokens: 176, Cost: "0.0014741"},
	}
	if !reflect.DeepEqual(p.Totals, want) || p.Page.HasMore || p.Page.NextCursor != nil {
		t.Errorf("totals =\n%+v\nwant\n%+v", p, want)
	}
}

func TestStaffTotals_PurposeAndOutcomeNarrowAndFailedIsIgnored(t *testing.T) {
	e := newEnv(t)
	st := NewStore(e.app)
	ctx := e.staff(t)
	tenant, day := e.namedTenant(t, "aicalls narrow "+uuid.NewString()), farDay()
	p1, p2 := uniquePurpose(), uniquePurpose()
	for i, s := range []seed{
		{purpose: p1, outcome: "ok"}, {purpose: p1, outcome: "unavailable"},
		{purpose: p2, outcome: "ok"}, {purpose: p2, outcome: "unavailable"},
	} {
		s.tenant, s.at = tenant, day.Add(time.Duration(i)*time.Hour)
		e.insert(t, s)
	}
	one := func(f Filter) Total {
		f.Tenant, f.Limit = tenant, 25
		p, err := st.StaffTotals(ctx, f)
		if err != nil || len(p.Totals) != 1 {
			t.Fatalf("StaffTotals(%+v) = %+v, %v, want one group", f, p, err)
		}
		return p.Totals[0]
	}
	if g := one(Filter{Purpose: p1}); g.Calls != 2 || g.Failed != 1 {
		t.Errorf("purpose P1: %+v, want 2 calls, 1 failed", g)
	}
	if g := one(Filter{Outcome: "ok"}); g.Calls != 2 || g.Failed != 0 {
		t.Errorf("outcome ok: %+v, want 2 calls, 0 failed", g)
	}
	plain, failed := one(Filter{}), one(Filter{Failed: true})
	if plain.Calls != 4 || plain.Failed != 2 || plain != failed {
		t.Errorf("failed=true changed the totals: %+v vs %+v", failed, plain)
	}
}

func TestStaffTotals_PagesWithoutOverlapOrGap(t *testing.T) {
	e := newEnv(t)
	st := NewStore(e.app)
	ctx := e.staff(t)
	purpose, day := uniquePurpose(), farDay()
	prev := day.AddDate(0, 0, -1)
	same := "pg-b-" + uuid.NewString()
	tenants := map[string]string{} // id -> name
	for _, name := range []string{"pg-a-" + uuid.NewString(), same, same} {
		tenants[e.namedTenant(t, name)] = name
	}
	var want []string
	for _, d := range []time.Time{day, prev} {
		for id := range tenants {
			e.insert(t, seed{tenant: id, purpose: purpose, outcome: "ok", at: d.Add(time.Hour)})
			want = append(want, d.Format(dayLayout)+"|"+id)
		}
	}
	slices.SortFunc(want, func(x, y string) int {
		dx, dy := x[:10], y[:10]
		if dx != dy {
			return -compareStr(dx, dy) // day DESC
		}
		nx, ny := tenants[x[11:]], tenants[y[11:]]
		if nx != ny {
			return compareStr(nx, ny)
		}
		return compareStr(x[11:], y[11:])
	})

	var got []string
	f := Filter{Purpose: purpose, From: prev, To: day.Add(24 * time.Hour), Limit: 2}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		p, err := st.StaffTotals(ctx, f)
		if err != nil {
			t.Fatalf("StaffTotals: %v", err)
		}
		for _, g := range p.Totals {
			got = append(got, g.Day+"|"+g.TenantID)
		}
		if !p.Page.HasMore {
			if p.Page.NextCursor != nil {
				t.Errorf("last page has a cursor")
			}
			break
		}
		c, err := DecodeTotalsCursor(*p.Page.NextCursor)
		if err != nil {
			t.Fatalf("DecodeTotalsCursor: %v", err)
		}
		f.TotalsCursor = &c
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("paged groups =\n%v\nwant six distinct groups in order\n%v", got, want)
	}
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestStaffCalls_NonHouseActorIsNotStaff(t *testing.T) {
	e := newEnv(t)
	st := NewStore(e.app)
	customer := e.namedTenant(t, "aicalls customer "+uuid.NewString())
	for name, ctx := range map[string]context.Context{
		"customer member": staffCtx(e.member(t, customer)),
		"no staff":        context.Background(),
	} {
		if _, err := st.StaffCalls(ctx, Filter{Limit: 25}); !errors.Is(err, db.ErrNotStaff) {
			t.Errorf("%s: StaffCalls err = %v, want ErrNotStaff", name, err)
		}
		if _, err := st.StaffTotals(ctx, Filter{Limit: 25}); !errors.Is(err, db.ErrNotStaff) {
			t.Errorf("%s: StaffTotals err = %v, want ErrNotStaff", name, err)
		}
	}
}
