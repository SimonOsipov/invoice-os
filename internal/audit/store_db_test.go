// AUDIT-04-07: the store's one-transaction claim, against a real Postgres.
//
// Helpers use an st* prefix. The oracle is a pgx QueryTracer on the store's own pool:
// pgx.Conn.Begin issues "begin" through Exec, which the tracer sees, so BEGIN/COMMIT are
// countable without reaching into the store.
package audit_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

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
