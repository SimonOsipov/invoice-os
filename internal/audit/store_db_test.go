// AUDIT-04-07: the store's one-transaction claim, against a real Postgres.
//
// Helpers use an st* prefix. The oracle is a pgx QueryTracer on the store's own pool:
// pgx.Conn.Begin issues "begin" through Exec, which the tracer sees, so BEGIN/COMMIT are
// countable without reaching into the store.
package audit_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// stTracer records every statement the pool issues.
type stTracer struct {
	mu   sync.Mutex
	sqls []string
}

func (tr *stTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	tr.sqls = append(tr.sqls, strings.TrimSpace(data.SQL))
	tr.mu.Unlock()
	return ctx
}

func (tr *stTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// count returns how many recorded statements begin with prefix, case-insensitively.
func (tr *stTracer) count(prefix string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for _, s := range tr.sqls {
		if strings.HasPrefix(strings.ToLower(s), prefix) {
			n++
		}
	}
	return n
}

// stTracedStore builds a store on its own single-connection traced pool, so nothing else
// in the suite can add statements to the count.
func stTracedStore(t *testing.T) (*audit.Store, *stTracer) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	tr := &stTracer{}
	cfg.ConnConfig.Tracer = tr
	cfg.MaxConns = 1

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("traced pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return audit.NewStore(pool), tr
}

// TestAuditStore_AllStatementsShareOneTransaction is AC #8. Split across transactions the
// page, the count and the three facets would each see a different snapshot, so a row
// inserted mid-request could be counted but not shown — a total that disagrees with the
// page it labels.
func TestAuditStore_AllStatementsShareOneTransaction(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	pageInsert(t, f, p, pageSeries("invoice.created", 3, 30))

	store, tr := stTracedStore(t)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{TenantID: p.tenant})

	got, err := store.List(ctx, audit.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Total == 0 {
		t.Fatalf("the seeded tenant read back 0 rows; the statement counts below would be from " +
			"a request that did no work")
	}

	if n := tr.count("begin"); n != 1 {
		t.Errorf("the request issued %d BEGINs, want exactly 1 (statements: %v)", n, tr.sqls)
	}
	if n := tr.count("commit"); n != 1 {
		t.Errorf("the request issued %d COMMITs, want exactly 1 (statements: %v)", n, tr.sqls)
	}
	// The floor: a request issuing only the transaction pair would satisfy both counts
	// above while doing nothing. Five statements read audit_log on a populated request —
	// the page, the three facets and the count. actor.Resolve adds a memberships query
	// only when a subject passes its uuid gate (these rows' actor does not), search adds
	// two fold-ins, and the empty probe runs only when nothing matched.
	if n := stAuditLogCount(tr); n != 5 {
		t.Errorf("the request issued %d statements against audit_log, want 5 (page, three facets, "+
			"count): %v", n, tr.sqls)
	}
	if n := stProbeCount(tr); n != 0 {
		t.Errorf("a populated request ran the empty probe %d times, want 0", n)
	}
}

// stAuditLogCount counts the statements reading audit_log, so the per-request budget is
// asserted rather than assumed.
func stAuditLogCount(tr *stTracer) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for _, s := range tr.sqls {
		if strings.Contains(s, "audit_log") {
			n++
		}
	}
	return n
}

// TestAuditStore_SkipsTheEmptyProbeWhenRowsMatched pins the probe as conditional. log_is_empty
// asks whether the log holds ANY row, so a non-zero total already answers it and the probe
// is dead work on every populated request.
func TestAuditStore_SkipsTheEmptyProbeWhenRowsMatched(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	pageInsert(t, f, p, pageSeries("invoice.created", 2, 20))

	store, tr := stTracedStore(t)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{TenantID: p.tenant})

	populated, err := store.List(ctx, audit.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if populated.Total == 0 {
		t.Fatalf("the seeded tenant read back 0 rows")
	}
	if populated.LogIsEmpty {
		t.Errorf("log_is_empty is true on a tenant with %d rows", populated.Total)
	}
	if n := stProbeCount(tr); n != 0 {
		t.Errorf("a populated request ran the empty probe %d times, want 0", n)
	}

	// A filter that matches nothing must run it, or log_is_empty cannot be answered.
	tr.mu.Lock()
	tr.sqls = nil
	tr.mu.Unlock()

	filtered, err := store.List(ctx, audit.Filter{Limit: 10, Events: []string{"no.such.event"}})
	if err != nil {
		t.Fatalf("List (filtered): %v", err)
	}
	if filtered.Total != 0 {
		t.Fatalf("the excluding filter matched %d rows, want 0", filtered.Total)
	}
	if n := stProbeCount(tr); n != 1 {
		t.Errorf("a request matching nothing ran the empty probe %d times, want 1 (statements: %v)",
			n, tr.sqls)
	}
}

// stProbeCount counts the unfiltered LIMIT 1 probe. It is the only statement selecting a
// bare literal from audit_log.
func stProbeCount(tr *stTracer) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for _, s := range tr.sqls {
		if strings.Contains(strings.ToLower(s), "select 1 from audit_log") {
			n++
		}
	}
	return n
}

// TestAuditStore_ListRefusesARequestWithNoIdentity keeps the store fail-closed: without an
// identity there is no tenant to scope to, and db.ErrNoTenant is what the handler turns
// into a 401 rather than serving an unscoped read.
func TestAuditStore_ListRefusesARequestWithNoIdentity(t *testing.T) {
	requireFixture(t)
	store, tr := stTracedStore(t)

	if _, err := store.List(context.Background(), audit.Filter{Limit: 10}); err == nil {
		t.Fatalf("List with no identity returned no error, want db.ErrNoTenant")
	}
	if n := tr.count("begin"); n != 0 {
		t.Errorf("List with no identity opened %d transactions, want 0", n)
	}
}

// TestAuditStore_ListIsScopedToTheCallersTenant is the store-level half of Core AC 2: the
// store issues no tenant predicate of its own, so if RLS were not doing the work this
// would return the other tenant's rows.
func TestAuditStore_ListIsScopedToTheCallersTenant(t *testing.T) {
	f := requireFixture(t)
	a := pageSeedTenant(t, f)
	b := pageSeedTenant(t, f)
	pageInsert(t, f, a, pageSeries("invoice.created", 3, 30))
	pageInsert(t, f, b, pageSeries("invoice.updated", 5, 30))

	store, _ := stTracedStore(t)
	got, err := store.List(auth.WithIdentity(context.Background(), auth.Identity{TenantID: a.tenant}),
		audit.Filter{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Total != 3 {
		t.Errorf("total = %d, want 3 — tenant A's own rows only", got.Total)
	}
	for _, e := range got.Events {
		if e.Event == "invoice.updated" {
			t.Errorf("tenant A's read returned %q, which only tenant B wrote", e.Event)
		}
	}
	if len(got.Events) == 0 {
		t.Errorf("tenant A's read returned nothing; the isolation claim above would be vacuous")
	}
}

// TestAuditStore_ListPreservesCompanyScopeFromQuery is AC #1's company_scope claim,
// closing a gap the existing suite leaves open: reader_db_test.go proves the VALUE against
// Query directly, and the handler's envelope test proves the JSON KEY against a spy-built
// Response, but nothing calls the real List with a company-scoped row and reads
// company_scope back off it. A line between Query and the returned Response that dropped
// or blanked the field would still pass every other case in this package.
func TestAuditStore_ListPreservesCompanyScopeFromQuery(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Scope Survives Ltd")
	pageInsert(t, f, p, []pageRow{
		{event: "portfolio.entity.created", payload: `{"id":"` + entity + `"}`, ageSeconds: 10},
	})

	store, _ := stTracedStore(t)
	got := empList(t, store, p.tenant, audit.Filter{Limit: 10})

	if len(got.Events) != 1 {
		t.Fatalf("List returned %d rows, want 1", len(got.Events))
	}
	if got.Events[0].CompanyScope != audit.ScopeCompany {
		t.Errorf("List's row has company_scope = %q, want %q — Query computed it correctly, "+
			"List must not lose it on the way out", got.Events[0].CompanyScope, audit.ScopeCompany)
	}
}

// TestAuditStore_EmptyProbeSharesThePageTransaction closes a gap
// TestAuditStore_AllStatementsShareOneTransaction cannot see: that test's tenant is
// populated, so the probe never runs and its BEGIN/COMMIT count says nothing about the
// probe's own transaction. Here the filter matches nothing, forcing the probe to run, and
// BEGIN must still be 1 — a probe split into its own db.WithinRequestTenantTx call would
// still pass every other case in this file.
func TestAuditStore_EmptyProbeSharesThePageTransaction(t *testing.T) {
	f := requireFixture(t)
	p := pageSeedTenant(t, f)
	pageInsert(t, f, p, pageSeries("invoice.created", 2, 20))

	store, tr := stTracedStore(t)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{TenantID: p.tenant})

	got, err := store.List(ctx, audit.Filter{Limit: 10, Events: []string{"no.such.event"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Total != 0 {
		t.Fatalf("the excluding filter matched %d rows, want 0 — this case cannot make its claim",
			got.Total)
	}
	if n := stProbeCount(tr); n != 1 {
		t.Fatalf("the empty probe ran %d times, want 1 — this case cannot make its claim", n)
	}
	if n := tr.count("begin"); n != 1 {
		t.Errorf("a request whose page AND probe both ran issued %d BEGINs, want exactly 1 — the "+
			"probe must share the page's transaction, not open its own (statements: %v)", n, tr.sqls)
	}
	if n := tr.count("commit"); n != 1 {
		t.Errorf("a request whose page AND probe both ran issued %d COMMITs, want exactly 1 "+
			"(statements: %v)", n, tr.sqls)
	}
}

// --- AUDIT-11-09: the third fold-in and the arm it feeds -----------------------------------

// stFoldInCount counts the statements resolving free text against table. actor.Resolve
// also reads memberships, so the ILIKE is what separates a fold-in lookup from it; the
// page and the company facet reach business_entities through a LEFT JOIN, not a FROM.
func stFoldInCount(tr *stTracer, table string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for _, s := range tr.sqls {
		if strings.Contains(s, "FROM "+table) && strings.Contains(s, "ILIKE") {
			n++
		}
	}
	return n
}

// stFilteredStatements returns the statements carrying the built predicate set — the page,
// the count and the three facets, all aliased `a` — and how many of them contain needle.
// The empty probe reads audit_log unaliased and is deliberately excluded: it carries no
// predicate.
func stFilteredStatements(tr *stTracer, needle string) (total, withNeedle int) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, s := range tr.sqls {
		if !strings.Contains(s, "FROM audit_log a") {
			continue
		}
		total++
		if strings.Contains(s, needle) {
			withNeedle++
		}
	}
	return total, withNeedle
}

// stNumberFixture seeds one tenant where all three fold-ins resolve on the term "Foldin":
// a company, a membership display name and an invoice number.
func stNumberFixture(t *testing.T, f *fixture) pageFixture {
	t.Helper()
	p := pageSeedTenant(t, f)
	entity := pageSeedEntity(t, f, p, "Foldin Freight")
	invoice := filtSeedNumberedInvoice(t, f, p, entity, "FOLDIN-1")
	filtSeedMembership(t, f, p, uuid.NewString(), "Foldin Person")
	filtInsert(t, f, p, []filtRow{
		{event: "invoice.created", actor: uuid.NewString(),
			payload: fmt.Sprintf(`{"id":%q}`, invoice), ageSeconds: 10},
	})
	return p
}

// TestAuditStore_SearchIssuesThreeFoldIns is AUDIT-11-09 AC #7. Free text cannot reach an
// actor, a company or an invoice number through a column predicate under FORCE RLS, so each
// is resolved first and folded back in as ids. The read contract's per-request budget moves
// from 5-10 to 5-11 because of the third lookup; a budget that drifts unasserted is how
// §10.1's statement count went wrong once already.
func TestAuditStore_SearchIssuesThreeFoldIns(t *testing.T) {
	f := requireFixture(t)
	p := stNumberFixture(t, f)

	store, tr := stTracedStore(t)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{TenantID: p.tenant})

	got, err := store.List(ctx, audit.Filter{Limit: 10, Q: "Foldin"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Total == 0 {
		t.Fatalf("the search matched 0 rows; the statement counts below would be from a request " +
			"that did no work")
	}
	for _, table := range []string{"memberships", "business_entities", "invoices"} {
		if n := stFoldInCount(tr, table); n != 1 {
			t.Errorf("a free-text search ran %d fold-in lookups against %s, want exactly 1 "+
				"(statements: %v)", n, table, tr.sqls)
		}
	}

	tr.mu.Lock()
	tr.sqls = nil
	tr.mu.Unlock()

	if _, err := store.List(ctx, audit.Filter{Limit: 10}); err != nil {
		t.Fatalf("List (no search): %v", err)
	}
	for _, table := range []string{"memberships", "business_entities", "invoices"} {
		if n := stFoldInCount(tr, table); n != 0 {
			t.Errorf("a search-free request ran %d fold-in lookups against %s, want 0 — the "+
				"lookups belong to q alone (statements: %v)", n, table, tr.sqls)
		}
	}
}

// TestAuditStore_NumberArmIsDroppedWhenNothingResolves is AUDIT-11-09 AC #2. The two text
// arms always appear, so the OR-group can never collapse; the fold-in arms are added only
// when their lookup found something, and an unconditional one would bind an empty array —
// a disjunct that can never be true. The arm must also reach the count and the facets, not
// only the page: a predicate applied to the page alone makes total a lie with every other
// case still green.
func TestAuditStore_NumberArmIsDroppedWhenNothingResolves(t *testing.T) {
	f := requireFixture(t)
	p := stNumberFixture(t, f)

	const arm = "a.invoice_id = ANY("
	store, tr := stTracedStore(t)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{TenantID: p.tenant})

	if _, err := store.List(ctx, audit.Filter{Limit: 10, Q: "FOLDIN-1"}); err != nil {
		t.Fatalf("List (resolving): %v", err)
	}
	total, withArm := stFilteredStatements(tr, arm)
	if total != 5 {
		t.Fatalf("the request built %d filtered statements, want 5 (page, count, three facets): %v",
			total, tr.sqls)
	}
	if withArm != total {
		t.Errorf("%d of %d filtered statements carry %q, want all of them (statements: %v)",
			withArm, total, arm, tr.sqls)
	}

	tr.mu.Lock()
	tr.sqls = nil
	tr.mu.Unlock()

	if _, err := store.List(ctx, audit.Filter{Limit: 10, Q: "zzzznomatchzzzz"}); err != nil {
		t.Fatalf("List (unresolving): %v", err)
	}
	total, withArm = stFilteredStatements(tr, arm)
	if total != 5 {
		t.Fatalf("the unresolving request built %d filtered statements, want 5: %v", total, tr.sqls)
	}
	if withArm != 0 {
		t.Errorf("%d filtered statements carry %q on a term resolving no invoice, want 0 "+
			"(statements: %v)", withArm, arm, tr.sqls)
	}
	// The two text arms are unconditional, or a search that resolves nothing drops its
	// whole fragment and falls back to the unfiltered set.
	for _, want := range []string{"a.event ILIKE", "jsonb_each_text(a.payload)"} {
		if _, n := stFilteredStatements(tr, want); n != total {
			t.Errorf("%d of %d filtered statements carry %q, want all of them", n, total, want)
		}
	}
}
