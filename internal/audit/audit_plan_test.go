// Plan proofs for audit_log's five tenant-leading read indexes. Every case asserts the absence
// of a Seq Scan and never the node type -- a Bitmap plan over the right index is still the
// right index.
//
// The index NAME is asserted only where one index can serve the predicate. AUDIT-04-09's
// composed cases pass "" instead: measured, the planner picks whichever tenant-leading index
// carries the most selective equality predicate present, so a name there is a property of the
// filter set and pinning it would fail on a filter change that broke nothing.
//
// Nothing here forces a plan. `enable_seqscan = off` would only prove an index CAN be
// used, and on the wrong corpus the planner correctly seq-scans: measured on this schema,
// the keyset page flips from Seq Scan + Sort to an index scan between 100 and 200 rows per
// tenant, and both facet counts seq-scan when a single tenant owns ~95% of the table but
// not at four tenants. The corpus below is what makes the natural plan the right plan, so
// its shape is load-bearing and is asserted before any EXPLAIN.
package audit_test

import (
	"context"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Corpus geometry. 20 tenants keeps the RLS qual selective (~5% of the table), which is
// the multi-tenant shape production has; a single-tenant corpus seq-scans the facets.
// 1,000 rows per tenant is ~5x the measured flip point, and must stay a multiple of 1,000
// so planEventMix lands exactly once per per-mille bucket. created_at runs on one global
// grid that interleaves the tenants, so every tenant's own history covers the full span
// rather than clustering, and the table-wide correlation stays low -- which costs an
// index scan MORE, not less.
const (
	planTenants       = 20
	planRowsPerTenant = 1000
	planEntities      = 8
	planInvoices      = 80
	planActors        = 12
	planSecondsPerRow = 388 // 20,000 rows * 388s ~= 90 days of history
)

const (
	planIdxCreated = "audit_log_tenant_created_idx"
	planIdxEvent   = "audit_log_tenant_event_created_idx"
	planIdxActor   = "audit_log_tenant_actor_created_idx"
	planIdxEntity  = "audit_log_tenant_entity_created_idx"
	planIdxInvoice = "audit_log_tenant_invoice_created_idx" // AUDIT-04-11's, the fifth
)

// planSearchTerm is a substring of the five invoice.* arms of planEventMix, 82% of the corpus,
// so a search case matches rows in ANY window. A rarer term does not: measured, the corpus
// tenant holds 77 rows in the 7-day window and none of them is a submission.accepted, so
// "accepted" makes the bounded case match nothing and the comparison below prove nothing.
const planSearchTerm = "invoice"

// planEventMix is the corpus event distribution, per mille, and must sum to 1000. It is
// an assumption, not a fact: an audit reader's plans are only as trustworthy as the mix
// they were pinned against. This one says an e-invoicing workspace writes several rows
// per invoice (created -> updated -> validated -> transitioned -> submitted) and few
// workspace-level config rows, so no single event exceeds 30% and the rarest is 1%.
// document.created is held near 3% deliberately: internal/invoice's uploader-lookup plan
// test shares this table's statistics and flips when that event dominates.
var planEventMix = []struct {
	event    string
	perMille int
	payload  string // SQL expression, evaluated against the r CTE below
}{
	{"invoice.created", 300, `jsonb_build_object('id', r.invoice, 'invoice_number', r.number)`},
	{"invoice.updated", 200, `jsonb_build_object('id', r.invoice, 'invoice_number', r.number)`},
	{"invoice.validated", 150, `jsonb_build_object('id', r.invoice, 'invoice_number', r.number)`},
	{"invoice.transitioned", 120, `jsonb_build_object('id', r.invoice, 'invoice_number', r.number)`},
	{"submission.accepted", 80, `jsonb_build_object('invoice_id', r.invoice, 'invoice_number', r.number)`},
	{"invoice.approval_approved", 50, `jsonb_build_object('invoice_id', r.invoice, 'invoice_number', r.number)`},
	{"document.created", 30, `jsonb_build_object('id', gen_random_uuid())`},
	{"portfolio.entity.updated", 30, `jsonb_build_object('id', r.entity)`},
	{"approval_policy.published", 30, `jsonb_build_object('policy_id', gen_random_uuid())`},
	{"workflow_role.staffed", 10, `jsonb_build_object('key', 'approver')`},
}

// planRareEvent / planCommonEvent bracket the event filter: the index must win at 1% and
// still win at 30%, or the pin is knife-edge on selectivity.
const (
	planRareEvent   = "workflow_role.staffed"
	planCommonEvent = "invoice.created"
)

// planDateWindow is the reader's default range, ~8% of the 90-day corpus -- wide enough
// that a tenant has more than the 50-row page inside it.
const planDateWindow = 7 * 24 * time.Hour

type planFixture struct {
	tenant string // the tenant every case reads as
	actor  string
	entity string
}

var (
	planOnce sync.Once
	planData planFixture
	planErr  error
)

// requirePlanCorpus builds the corpus once per test binary, on first use. Its audit rows
// are permanent -- audit_log refuses DELETE and TRUNCATE -- so every run mints fresh
// throwaway tenants and leaves them, along with the entities and invoices its rows are
// attributed to. Deleting those would leave the surviving audit rows pointing at ids that
// no longer exist, which is worse than the clutter.
func requirePlanCorpus(t *testing.T) (*fixture, planFixture) {
	t.Helper()
	f := requireFixture(t)
	planOnce.Do(func() { planData, planErr = buildPlanCorpus(f) })
	if planErr != nil {
		t.Fatalf("build plan corpus: %v", planErr)
	}
	assertCorpusShape(t, f, planData)
	return f, planData
}

func buildPlanCorpus(f *fixture) (planFixture, error) {
	ctx := context.Background()
	tenants := make([]string, planTenants)
	for i := range tenants {
		tenants[i] = uuid.NewString()
	}

	for _, tenant := range tenants {
		// tenants is SELECT-only for invoice_app, so the tenant row goes in as the owner.
		if err := db.WithinTenantTx(ctx, f.mig, tenant, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `INSERT INTO tenants (id, name) VALUES ($1, $2)`,
				tenant, "audit-plan-"+tenant[:8])
			return e
		}); err != nil {
			return planFixture{}, err
		}
		if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `
				INSERT INTO business_entities (id, tenant_id, name)
				SELECT gen_random_uuid(), $1, 'ent-' || lpad(g::text, 2, '0')
				  FROM generate_series(1, $2) g`, tenant, planEntities); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `
				INSERT INTO invoices (id, tenant_id, entity_id, invoice_number)
				SELECT gen_random_uuid(), $1, be.id, 'INV-' || be.name || '-' || lpad(g::text, 2, '0')
				  FROM business_entities be, generate_series(1, $2) g
				 WHERE be.tenant_id = $1`, tenant, planInvoices/planEntities)
			return e
		}); err != nil {
			return planFixture{}, err
		}
	}

	insert := planCorpusInsertSQL()
	total := planTenants * planRowsPerTenant
	for ti, tenant := range tenants {
		if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, insert, tenant, planRowsPerTenant, ti+1, total)
			return e
		}); err != nil {
			return planFixture{}, err
		}
	}

	// VACUUM, not just ANALYZE: an index-only scan needs the visibility map set, and it
	// runs as the owner and outside any transaction.
	if _, err := f.mig.Exec(ctx, `VACUUM (ANALYZE) audit_log`); err != nil {
		return planFixture{}, err
	}

	out := planFixture{tenant: tenants[0]}
	err := db.WithinTenantTx(ctx, f.app, out.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT actor, entity_id FROM audit_log
			 WHERE tenant_id = $1 AND entity_id IS NOT NULL
			 ORDER BY id LIMIT 1`, out.tenant).Scan(&out.actor, &out.entity)
	})
	return out, err
}

// planCorpusInsertSQL renders planEventMix into the event and payload CASE arms. Neither
// CASE has an ELSE: a mix that does not sum to 1000 leaves a bucket unmatched and the
// NOT NULL event column rejects the insert, so the geometry cannot drift silently.
func planCorpusInsertSQL() string {
	var event, payload strings.Builder
	event.WriteString("CASE")
	payload.WriteString("CASE")
	hi := 0
	for _, m := range planEventMix {
		hi += m.perMille
		arm := " WHEN r.bucket < " + strconv.Itoa(hi) + " THEN "
		event.WriteString(arm + "'" + m.event + "'")
		payload.WriteString(arm + m.payload)
	}
	event.WriteString(" END")
	payload.WriteString(" END")

	// $1 tenant, $2 rows per tenant, $3 this tenant's 1-based slot on the created_at grid,
	// $4 the corpus row total. Row g of every tenant lands on grid position
	// (g-1)*planTenants + slot, so the tenants interleave in time without colliding.
	return `
		WITH inv AS (
			SELECT array_agg(id ORDER BY invoice_number)             AS ids,
			       array_agg(invoice_number ORDER BY invoice_number) AS numbers
			  FROM invoices WHERE tenant_id = $1
		), ent AS (
			SELECT array_agg(id ORDER BY name) AS ids FROM business_entities WHERE tenant_id = $1
		), r AS (
			SELECT (g % 1000)::int                                        AS bucket,
			       inv.ids[((g % ` + strconv.Itoa(planInvoices) + `) + 1)::int]     AS invoice,
			       inv.numbers[((g % ` + strconv.Itoa(planInvoices) + `) + 1)::int] AS number,
			       ent.ids[((g % ` + strconv.Itoa(planEntities) + `) + 1)::int] AS entity,
			       md5($1::text || 'actor' || (g % ` + strconv.Itoa(planActors) + `))::uuid::text AS actor,
			       now() - (($4 - ((g - 1) * ` + strconv.Itoa(planTenants) + ` + $3)) * ` +
		strconv.Itoa(planSecondsPerRow) + `) * interval '1 second' AS created_at
			  FROM generate_series(1, $2) g, inv, ent
		)
		INSERT INTO audit_log (tenant_id, actor, event, payload, created_at)
		SELECT $1, r.actor, ` + event.String() + `, ` + payload.String() + `, r.created_at FROM r`
}

// assertCorpusShape fails before any EXPLAIN when the corpus is not the one the pinned
// plans were chosen against. Every assertion below is an assumption about selectivity, so
// a corpus that silently changed shape would turn each of them into a different claim.
func assertCorpusShape(t *testing.T, f *fixture, p planFixture) {
	t.Helper()
	ctx := context.Background()

	sum := 0
	for _, m := range planEventMix {
		sum += m.perMille
	}
	if sum != 1000 {
		t.Fatalf("planEventMix sums to %d per mille, want 1000", sum)
	}
	if planRowsPerTenant%1000 != 0 {
		t.Fatalf("planRowsPerTenant = %d, want a multiple of 1000 so every per-mille bucket lands once",
			planRowsPerTenant)
	}

	counts := map[string]int{}
	var rows, attributed, entities, actors, numbered int
	var oldest, newest time.Time
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `
			SELECT count(*), count(entity_id), count(DISTINCT entity_id), count(DISTINCT actor),
			       count(*) FILTER (WHERE payload ? 'invoice_number'),
			       min(created_at), max(created_at)
			  FROM audit_log WHERE tenant_id = $1`, p.tenant).
			Scan(&rows, &attributed, &entities, &actors, &numbered, &oldest, &newest); e != nil {
			return e
		}
		cur, e := tx.Query(ctx,
			`SELECT event, count(*) FROM audit_log WHERE tenant_id = $1 GROUP BY event`, p.tenant)
		if e != nil {
			return e
		}
		defer cur.Close()
		for cur.Next() {
			var ev string
			var n int
			if e := cur.Scan(&ev, &n); e != nil {
				return e
			}
			counts[ev] = n
		}
		return cur.Err()
	}); err != nil {
		t.Fatalf("read back corpus shape: %v", err)
	}

	if rows != planRowsPerTenant {
		t.Fatalf("corpus rows for the read tenant = %d, want %d", rows, planRowsPerTenant)
	}
	for _, m := range planEventMix {
		want := planRowsPerTenant * m.perMille / 1000
		if counts[m.event] != want {
			t.Fatalf("corpus rows for %s = %d, want %d", m.event, counts[m.event], want)
		}
	}
	if entities != planEntities {
		t.Fatalf("distinct entity_id in the corpus = %d, want %d — the insert trigger did not attribute the rows",
			entities, planEntities)
	}
	if actors != planActors {
		t.Fatalf("distinct actors in the corpus = %d, want %d", actors, planActors)
	}
	if attributed == rows {
		t.Fatalf("every one of the %d rows carries an entity_id; the mix must leave the workspace-level events NULL", rows)
	}
	// AUDIT-11-07: the search pins below read a number out of the payload, so the widening
	// is corpus shape too. TestAudit_PlanCorpusCarriesAnInvoiceNumber owns the per-event
	// breakdown; this is the floor that runs before every EXPLAIN.
	if want := planNumberedRowsPerTenant(); numbered != want {
		t.Fatalf("corpus rows carrying invoice_number = %d, want %d — the invoice-scoped "+
			"payloads are not the widened ones the number pins measure", numbered, want)
	}
	if span := newest.Sub(oldest); span < 80*24*time.Hour {
		t.Fatalf("corpus spans %s, want ~90 days — a compressed span makes the date-range case select everything", span)
	}
}

// --- AC-1: the five designed filters ---------------------------------------------------

// The reader's first page and its cursor page: both must be served in index order, or the
// keyset degrades to a full sort of the tenant on every scroll.
func TestAudit_KeysetPageUsesTenantCreatedIndex(t *testing.T) {
	f, p := requirePlanCorpus(t)

	t.Run("first page", func(t *testing.T) {
		assertServedByIndex(t, f, p.tenant, planIdxCreated, nil,
			`SELECT id, created_at, actor, event, entity_id FROM audit_log
			 ORDER BY created_at DESC, id DESC LIMIT 50`)
	})

	// A cursor well down the corpus, so the page is neither the first nor empty.
	t.Run("cursor page", func(t *testing.T) {
		assertServedByIndex(t, f, p.tenant, planIdxCreated, []string{"created_at", "id"},
			`SELECT id, created_at FROM audit_log
			 WHERE (created_at, id) < ($1, $2)
			 ORDER BY created_at DESC, id DESC LIMIT 50`,
			time.Now().Add(-planDateWindow*3), int64(1)<<62)
	})
}

func TestAudit_DateRangeUsesTenantCreatedIndex(t *testing.T) {
	f, p := requirePlanCorpus(t)

	const sql = `SELECT id, created_at FROM audit_log WHERE created_at >= $1
	             ORDER BY created_at DESC, id DESC LIMIT 50`
	assertServedByIndex(t, f, p.tenant, planIdxCreated,
		[]string{"created_at"}, sql, time.Now().Add(-planDateWindow))
}

// TestAudit_DateRangeBoundaryIsInclusive is a mutation-verify gap closed during AUDIT-04-09
// QA: filter.go renders From as `a.created_at >= `, but nothing in the package exercised the
// operator against a real boundary through the actual Query path — TestAudit_DateRangeUsesTenantCreatedIndex
// above pins a hand-written `>=` literal, not filterPredicates' own. Measured, changing that
// `>=` to `>` left every other TestAudit_ case green (`go test -run TestAudit_ ./internal/audit/`, 46/46).
func TestAudit_DateRangeBoundaryIsInclusive(t *testing.T) {
	f, p := requirePlanCorpus(t)

	// The tenant's own newest row: LIMIT 1 with From set to its own created_at returns it
	// under >=, and returns nothing under a strict > (there is no fresher row to fall back to).
	var boundary time.Time
	var wantID string
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		var id int64
		if e := tx.QueryRow(ctx, `SELECT created_at, id FROM audit_log
			WHERE tenant_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, p.tenant).
			Scan(&boundary, &id); e != nil {
			return e
		}
		wantID = strconv.FormatInt(id, 10)
		return nil
	}); err != nil {
		t.Fatalf("find the tenant's newest row: %v", err)
	}

	resp := planQuery(t, f, p.tenant, audit.Filter{Limit: 1, From: boundary})

	if len(resp.Events) != 1 || resp.Events[0].ID != wantID {
		got := "no rows"
		if len(resp.Events) > 0 {
			got = resp.Events[0].ID
		}
		t.Errorf("From=%s (the row's own created_at) returned %s, want row %s — the bound "+
			"must be >=, not >", boundary, got, wantID)
	}
}

// Bracketed at 1% and 30% of the corpus: an index that only wins on the rarest event is
// not an index the reader can rely on.
func TestAudit_SelectiveEventFilterUsesEventIndex(t *testing.T) {
	f, p := requirePlanCorpus(t)

	const sql = `SELECT id, created_at FROM audit_log WHERE event = $1
	             ORDER BY created_at DESC, id DESC LIMIT 50`
	for _, event := range []string{planRareEvent, planCommonEvent} {
		t.Run(event, func(t *testing.T) {
			assertServedByIndex(t, f, p.tenant, planIdxEvent, []string{"event"}, sql, event)
		})
	}
}

func TestAudit_ActorFilterUsesActorIndex(t *testing.T) {
	f, p := requirePlanCorpus(t)

	const sql = `SELECT id, created_at FROM audit_log WHERE actor = $1
	             ORDER BY created_at DESC, id DESC LIMIT 50`
	assertServedByIndex(t, f, p.tenant, planIdxActor, []string{"actor"}, sql, p.actor)
}

func TestAudit_NamedCompanyFilterUsesEntityIndex(t *testing.T) {
	f, p := requirePlanCorpus(t)

	const sql = `SELECT id, created_at FROM audit_log WHERE entity_id = $1
	             ORDER BY created_at DESC, id DESC LIMIT 50`
	assertServedByIndex(t, f, p.tenant, planIdxEntity, []string{"entity_id"}, sql, p.entity)
}

// --- AC-2: the two facet counts --------------------------------------------------------

func TestAudit_EventFacetCountsUseAnIndexOnlyScan(t *testing.T) {
	f, p := requirePlanCorpus(t)

	plan := assertServedByIndex(t, f, p.tenant, planIdxEvent, nil,
		`SELECT event, count(*) FROM audit_log GROUP BY event`)
	assertIndexOnly(t, plan)
}

func TestAudit_CompanyFacetCountsUseAnIndexOnlyScan(t *testing.T) {
	f, p := requirePlanCorpus(t)

	plan := assertServedByIndex(t, f, p.tenant, planIdxEntity, nil,
		`SELECT entity_id, count(*) FROM audit_log GROUP BY entity_id`)
	assertIndexOnly(t, plan)
}

// --- helpers ---------------------------------------------------------------------------

// assertServedByIndex is the whole AC-3 vacuity floor plus the AC-1 assertion: the query
// returns rows, the plan is non-empty and mentions audit_log, it names the index, it does
// not seq-scan, and every named column is an Index Cond rather than a post-scan Filter.
// The node type is deliberately not asserted.
//
// tenant_id is required in every Index Cond because the name alone does not pin the shape:
// an index that keeps its name but loses its tenant_id lead is still chosen for the cursor
// page, and only the missing tenant cond gives it away.
func assertServedByIndex(t *testing.T, f *fixture, tenant, index string, condCols []string, sql string, args ...any) string {
	t.Helper()
	condCols = append([]string{"tenant_id"}, condCols...)

	label := index
	if label == "" {
		label = "the audit_log indexes"
	}
	if n := countAsApp(t, f, tenant, sql, args...); n == 0 {
		t.Fatalf("the query returns no rows, so its plan proves nothing about %s", label)
	}

	plan := explainAsApp(t, f, tenant, sql, args...)
	if plan == "" || !strings.Contains(plan, "audit_log") {
		t.Fatalf("plan = %q, want a non-empty plan mentioning audit_log", plan)
	}
	// An EMPTY index means "assert the shape, not the name" (AUDIT-04-09). Measured, the
	// planner picks whichever tenant-leading index carries the most selective equality
	// predicate present, so a composed query's index name is a property of the filter set and
	// pinning it would make the test fail on a filter change that broke nothing.
	if index != "" && !strings.Contains(plan, index) {
		t.Errorf("plan = %s\nwant it to use %s", plan, index)
	}
	if strings.Contains(plan, "Seq Scan on audit_log") {
		t.Errorf("plan = %s\nmust not Seq Scan audit_log", plan)
	}
	cond := indexCond(plan)
	for _, col := range condCols {
		if !strings.Contains(cond, col) {
			t.Errorf("Index Cond = %q, want %s in it (a post-scan Filter reads the whole tenant)\nplan = %s",
				cond, col, plan)
		}
	}
	return plan
}

func assertIndexOnly(t *testing.T, plan string) {
	t.Helper()
	if !strings.Contains(plan, "Index Only Scan") {
		t.Errorf("plan = %s\nwant an Index Only Scan — a facet count that visits the heap reads every row of the tenant", plan)
	}
}

// indexCond joins the Index Cond lines emitted by the audit_log nodes ONLY, so a column
// asserted against it cannot be satisfied by a Filter line, by a node's own name, or by the
// business_entities node the reader LEFT JOINs.
//
// That last exclusion is the load-bearing one and it is not hypothetical: measured, the join
// node reads `Bitmap Index Scan on business_entities_tenant_id_id_uq / Index Cond: (tenant_id
// = ...)`, so concatenating every node would let "tenant_id is in the Index Cond" pass on the
// joined table alone even if audit_log had lost its tenant lead outright — which is the exact
// regression this check exists to catch. TestAudit_IndexCondIgnoresTheJoinedTable holds it.
func indexCond(plan string) string { return planCondLines(plan, "audit_log", "Index Cond:") }

// planCondLines joins the marker lines emitted by nodes scanning a relation or index whose
// name starts with relPrefix. The marker is matched against the TRIMMED line's prefix, not
// as a substring, so a Nested Loop's "Join Filter:" never lands in a "Filter:" collection.
func planCondLines(plan, relPrefix, marker string) string {
	var out []string
	on := false
	for _, line := range strings.Split(plan, "\n") {
		if target, ok := scanTarget(line); ok {
			on = strings.HasPrefix(target, relPrefix)
		}
		trimmed := strings.TrimSpace(line)
		if on && strings.HasPrefix(trimmed, marker) {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, "\n")
}

// scanTarget reports the relation or index a plan line scans: "Seq Scan on X" and "Bitmap
// Index Scan on X" name it after "on", "Index Scan using X" after "using". A non-scan node
// returns false, which leaves the caller's current node in force — Index Cond lines only ever
// follow a scan node.
func scanTarget(line string) (string, bool) {
	for _, marker := range []string{"Scan using ", "Scan on "} {
		if i := strings.Index(line, marker); i >= 0 {
			return strings.TrimSpace(line[i+len(marker):]), true
		}
	}
	return "", false
}

func explainAsApp(t *testing.T, f *fixture, tenant, sql string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	var lines []string
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			lines = append(lines, line)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	return strings.Join(lines, "\n")
}

func countAsApp(t *testing.T, f *fixture, tenant, sql string, args ...any) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM ("+sql+") s", args...).Scan(&n)
	}); err != nil {
		t.Fatalf("count rows returned by the query under test: %v", err)
	}
	return n
}

// --- AUDIT-04-09: composed filters, filtered facets, and the two predicates no index serves.
//
// Every case below EXPLAINs the SQL the reader ACTUALLY ISSUED, captured off a pgx
// QueryTracer, rather than a hand-written copy of it. The cases AUDIT-01 shipped pin a bare
// audit_log query with no LEFT JOIN business_entities, so they do not pin the query that
// ships — which is also why the unscoped indexCond they used was never false-green.

// planStmt is one captured statement: the text pgx sent, and the arguments it sent with it.
type planStmt struct {
	sql  string
	args []any
}

// planTracer records every statement issued on its connection.
type planTracer struct {
	mu    sync.Mutex
	stmts []planStmt
}

func (tr *planTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	tr.mu.Lock()
	tr.stmts = append(tr.stmts, planStmt{sql: strings.TrimSpace(d.SQL), args: d.Args})
	tr.mu.Unlock()
	return ctx
}

func (tr *planTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// The five statements Query issues against audit_log, keyed by the fragment that identifies
// each. Kept as constants because a rename in reader.go or facets.go must break the lookup
// loudly rather than silently yield an empty capture.
const (
	planKindPage         = "page"
	planKindCount        = "count"
	planKindFacetEvent   = "facet-event"
	planKindFacetActor   = "facet-actor"
	planKindFacetCompany = "facet-company"
)

// planTrace runs the real Query as the corpus tenant on its own single-connection traced
// pool and returns every statement it issued, in order.
func planTrace(t *testing.T, filter audit.Filter) []planStmt {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	tr := &planTracer{}
	cfg.ConnConfig.Tracer = tr
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("traced pool: %v", err)
	}
	defer pool.Close()

	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, pool, planData.tenant, func(tx pgx.Tx) error {
		_, e := audit.Query(ctx, tx, filter)
		return e
	}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	return tr.stmts
}

// planCapture runs the real Query as the corpus tenant and returns the statements it issued
// against audit_log, keyed by kind. It fails when a kind is missing, so a reader that stopped
// issuing one cannot leave a case silently unasserted.
func planCapture(t *testing.T, filter audit.Filter) map[string]planStmt {
	t.Helper()
	out := map[string]planStmt{}
	for _, s := range planTrace(t, filter) {
		// The GROUP BY arms come FIRST and the count matches on `SELECT count(*)`, not on
		// `count(*)`: every facet selects count(*) from audit_log too, so a looser count arm
		// swallows all three and the missing-kind check below fires on the facets instead.
		switch {
		case strings.Contains(s.sql, "GROUP BY a.event"):
			out[planKindFacetEvent] = s
		case strings.Contains(s.sql, "GROUP BY a.actor"):
			out[planKindFacetActor] = s
		case strings.Contains(s.sql, "GROUP BY a.entity_id"):
			out[planKindFacetCompany] = s
		case strings.Contains(s.sql, "SELECT count(*) FROM audit_log a"):
			out[planKindCount] = s
		case strings.Contains(s.sql, "FROM audit_log a"):
			out[planKindPage] = s
		}
	}
	for _, kind := range []string{planKindPage, planKindCount, planKindFacetEvent, planKindFacetActor, planKindFacetCompany} {
		if _, ok := out[kind]; !ok {
			t.Fatalf("Query issued no %s statement; the capture cannot assert anything about it", kind)
		}
	}
	return out
}

// planDenseTriple is the (actor, entity, event) combination with the most rows inside the date
// window. planFixture's own actor and entity come from the corpus's OLDEST row, so a filter
// built from them has an empty intersection with a recent date bound — which the rows>0 floor
// correctly refuses to plan against.
func planDenseTriple(t *testing.T, f *fixture, tenant string, since time.Time) (actor, entity, event string) {
	t.Helper()
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT actor, entity_id::text, event
			  FROM audit_log
			 WHERE entity_id IS NOT NULL AND created_at >= $1
			 GROUP BY actor, entity_id, event
			 ORDER BY count(*) DESC, actor, entity_id, event
			 LIMIT 1`, since).Scan(&actor, &entity, &event)
	}); err != nil {
		t.Fatalf("no (actor, entity, event) triple inside the window; a composed filter built "+
			"from the corpus would match nothing: %v", err)
	}
	return actor, entity, event
}

// planComposed is AC #1's five-predicate filter: date, event, actor, named company, cursor.
//
// The cursor sits at the TOP of the range rather than mid-list, so the page returns every row
// the other four predicates match. A mid-list cursor taken from an unfiltered page excludes
// almost all of them — the window holds ~78 rows and one triple holds a handful — and the
// rows>0 floor would then refuse the case. The Index Cond is the same shape at any position:
// what this test pins is that ROW(created_at, id) folds INTO it alongside the other terms,
// not which position it carries.
func planComposed(t *testing.T, f *fixture, p planFixture) audit.Filter {
	t.Helper()
	since := time.Now().Add(-planDateWindow)
	actor, entity, event := planDenseTriple(t, f, p.tenant, since)

	return audit.Filter{
		Limit:   50,
		From:    since,
		Events:  []string{event},
		Actors:  []string{actor},
		Company: audit.NamedCompany(entity),
		Cursor:  &audit.Cursor{CreatedAt: time.Now().Add(time.Hour), ID: math.MaxInt64},
	}
}

// planHasSort reports whether the plan contains a Sort node.
func planHasSort(plan string) bool { return strings.Contains(plan, "Sort") }

// TestAudit_ComposedPageIsIndexServedAndUnsorted is AC #1. Measured, the composed page keeps
// tenant_id, entity_id, the date bound AND the cursor's ROW comparison together in ONE Index
// Cond, with event and actor as heap Filters and no Sort node — so the keyset still
// short-circuits under a full filter set. The architecture's fear that extra AND terms would
// demote the cursor to a post-scan filter is disproved.
//
// No index NAME is asserted: the planner picks whichever tenant-leading index carries the
// most selective equality predicate present, so the name is a property of the filter set.
func TestAudit_ComposedPageIsIndexServedAndUnsorted(t *testing.T) {
	f, p := requirePlanCorpus(t)
	stmts := planCapture(t, planComposed(t, f, p))
	page := stmts[planKindPage]

	plan := assertServedByIndex(t, f, p.tenant, "", []string{"entity_id", "created_at"},
		page.sql, page.args...)

	if !strings.Contains(indexCond(plan), "ROW(created_at, id)") {
		t.Errorf("the cursor's row-value comparison is not in audit_log's Index Cond, so the "+
			"page re-reads the tenant and filters after the fact:\n%s", plan)
	}
	if planHasSort(plan) {
		t.Errorf("the composed page has a Sort node; the index no longer supplies the ordering "+
			"and LIMIT cannot short-circuit:\n%s", plan)
	}
}

// TestAudit_FilteredFacetsAreStillIndexServed is AC #2. D-14 is confirmed and bites earlier
// than it claims: ONE date filter already costs the actor and company facets their Index Only
// Scan. So this asserts index-SERVED, never Index Only — and never "no Sort", because every
// facet sorts on count(*), which no index can supply.
func TestAudit_FilteredFacetsAreStillIndexServed(t *testing.T) {
	f, p := requirePlanCorpus(t)
	since := time.Now().Add(-planDateWindow)
	actor, entity, _ := planDenseTriple(t, f, p.tenant, since)

	stmts := planCapture(t, audit.Filter{
		Limit:   50,
		From:    since,
		Actors:  []string{actor},
		Company: audit.NamedCompany(entity),
	})

	for _, kind := range []string{planKindFacetEvent, planKindFacetActor, planKindFacetCompany} {
		t.Run(kind, func(t *testing.T) {
			st := stmts[kind]
			assertServedByIndex(t, f, p.tenant, "", nil, st.sql, st.args...)
		})
	}
}

// TestAudit_UnfilteredFacetsRemainIndexOnly is AC #3 — AUDIT-01's claim, re-asserted through
// the SHIPPED facet SQL rather than the bare GROUP BY those tests pin, so this story cannot
// weaken it by changing the statement the reader issues.
func TestAudit_UnfilteredFacetsRemainIndexOnly(t *testing.T) {
	f, p := requirePlanCorpus(t)
	stmts := planCapture(t, audit.Filter{Limit: 50})

	for _, kind := range []string{planKindFacetEvent, planKindFacetActor, planKindFacetCompany} {
		t.Run(kind, func(t *testing.T) {
			st := stmts[kind]
			plan := assertServedByIndex(t, f, p.tenant, "", nil, st.sql, st.args...)
			assertIndexOnly(t, plan)
		})
	}
}

// TestAudit_WorkspaceLevelPageIsPinnedToTheMeasuredPlan is AC #4, whose job is to pin the plan
// that ACTUALLY OCCURS rather than the one the contract doc asserts.
//
// D-15 says entity_id IS NULL is a post-scan Filter, not an Index Cond. That was measured on a
// single 200k-row tenant. On the corpus this test runs on — 20 tenants of 1,000 rows — the
// opposite happens: the planner takes the entity index, puts entity_id IS NULL IN the Index
// Cond, and adds a Sort. Both measurements are real; the claim is corpus-dependent, and D-15
// stated it as though it were not.
//
// This is also one of exactly two shapes that carry a Sort (the other is free-text search), so
// it is exempt from AC #1's no-Sort rule by measurement, not by exception.
func TestAudit_WorkspaceLevelPageIsPinnedToTheMeasuredPlan(t *testing.T) {
	f, p := requirePlanCorpus(t)
	stmts := planCapture(t, audit.Filter{Limit: 50, Company: audit.WorkspaceOnly()})
	page := stmts[planKindPage]

	plan := assertServedByIndex(t, f, p.tenant, "", []string{"entity_id IS NULL"},
		page.sql, page.args...)

	if !planHasSort(plan) {
		t.Errorf("the workspace page has no Sort node. That is BETTER than what was measured, "+
			"not worse — but this test pins the measured plan, so re-measure and update the "+
			"comment above rather than deleting this check:\n%s", plan)
	}
}

// TestAudit_ScopedInvoiceReadUsesTheInvoiceIndex is AC #5, pinned strictly, which D-19 says is
// impossible. D-19 is stale: it was written against the payload->>'id' form, where the match
// could only ever be a heap Filter because jsonb_object_field_text is not LEAKPROOF.
// AUDIT-04-11 replaced that with a STORED generated column, so the predicate is now a plain
// column comparison and reaches the Index Cond.
func TestAudit_ScopedInvoiceReadUsesTheInvoiceIndex(t *testing.T) {
	f, p := requirePlanCorpus(t)

	// A real invoice of the corpus tenant, so the query returns rows — assertServedByIndex
	// floors that, and a scoped read of a random uuid would prove nothing.
	var invoice string
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT invoice_id FROM audit_log
			WHERE invoice_id IS NOT NULL ORDER BY created_at DESC LIMIT 1`).Scan(&invoice)
	}); err != nil {
		t.Fatalf("find a scoped invoice in the corpus: %v", err)
	}

	stmts := planCapture(t, audit.Filter{Limit: 50, InvoiceID: invoice})
	page := stmts[planKindPage]

	plan := assertServedByIndex(t, f, p.tenant, planIdxInvoice, []string{"invoice_id"},
		page.sql, page.args...)

	// The index name IS asserted here, unlike the composed cases: this predicate has exactly
	// one index that can serve it, so the name is the claim rather than an accident of
	// selectivity.
	if strings.Contains(plan, "payload") {
		t.Errorf("the scoped plan still touches payload; the generated column is not being "+
			"used:\n%s", plan)
	}
}

// TestAudit_SearchIsTheOnlyPredicateNoIndexCanServe is AC #6, and it corrects the AC's own
// premise. The AC expects a "no Seq Scan" assertion to FAIL on search. Measured, it PASSES:
// the page is an ordinary Index Scan on the tenant/created index, because the ORDER BY still
// rides the index and only the text match falls to the heap. So "no Seq Scan" is not a weaker
// claim here, it is a VACUOUS one.
//
// The falsifiable claim is that the text predicate is a FILTER and never an Index Cond, and
// that the reason still holds: under FORCE RLS a non-LEAKPROOF operator can never be pushed
// down, and every text-matching operator is non-leakproof. If texticlike ever becomes
// leakproof this test fails and the exception should be revisited.
//
// AUDIT-11-09 added a fourth arm, a.invoice_id = ANY(...), whose uuid_eq IS leakproof — so
// non-leakproofness is now only half the reason. The other half: searchFragment always emits
// the two text arms, so the leakproof arm can never stand alone, and an OR-group holding a
// non-leakproof term is non-leakproof whole. That half is structural, not corpus-dependent.
//
// This case runs planSearchTerm, which resolves no invoice, so the resolved arm is dropped
// and this pin speaks for two of the search's up-to-five arms.
// TestAudit_NoArmOfTheSearchReachesAnIndexCond covers the group with the resolved arm present.
func TestAudit_SearchIsTheOnlyPredicateNoIndexCanServe(t *testing.T) {
	f, p := requirePlanCorpus(t)
	stmts := planCapture(t, audit.Filter{Limit: 50, Q: planSearchTerm})
	page := stmts[planKindPage]

	plan := assertServedByIndex(t, f, p.tenant, "", nil, page.sql, page.args...)

	if strings.Contains(indexCond(plan), "~~*") {
		t.Errorf("the ILIKE match reached audit_log's Index Cond. That contradicts the "+
			"non-leakproof rule this exception rests on — re-measure pg_proc before "+
			"believing it:\n%s", plan)
	}
	if !strings.Contains(plan, "~~*") {
		t.Fatalf("the plan carries no ILIKE at all, so the check above proves nothing:\n%s", plan)
	}

	// The reason, not just the symptom.
	for _, fn := range []string{"texticlike", "ts_match_vq"} {
		var leakproof bool
		var found bool
		if err := db.WithinTenantTx(context.Background(), f.app, p.tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`SELECT proleakproof, true FROM pg_proc WHERE proname = $1 LIMIT 1`, fn).
				Scan(&leakproof, &found)
		}); err != nil {
			t.Fatalf("read pg_proc for %s: %v", fn, err)
		}
		if !found {
			t.Fatalf("pg_proc has no %s; the leakproof claim cannot be checked", fn)
		}
		if leakproof {
			t.Errorf("%s is now LEAKPROOF. Under FORCE RLS it could be pushed into an Index "+
				"Cond, so the story's one Core AC 12 exception should be revisited", fn)
		}
	}
}

// TestAudit_SearchScanIsBoundedByTheDateRange is the other half of AC #6: search cannot be
// index-served, so the only thing that bounds it is the date range. Asserted with EXPLAIN
// ANALYZE row counts rather than timings, which are not reproducible in CI.
func TestAudit_SearchScanIsBoundedByTheDateRange(t *testing.T) {
	f, p := requirePlanCorpus(t)

	since := time.Now().Add(-planDateWindow)
	unbounded := planCapture(t, audit.Filter{Limit: 50, Q: planSearchTerm})[planKindCount]
	bounded := planCapture(t, audit.Filter{
		Limit: 50, Q: planSearchTerm, From: since,
	})[planKindCount]

	wide := planRowsExamined(t, f, p.tenant, unbounded)
	narrow := planRowsExamined(t, f, p.tenant, bounded)

	if wide == 0 || narrow == 0 {
		t.Fatalf("one of the runs examined no rows at all (wide=%d narrow=%d); a comparison "+
			"between them proves nothing", wide, narrow)
	}
	if narrow >= wide {
		t.Errorf("the 7-day bound examined %d rows and the unbounded search %d; the date range "+
			"is the only thing bounding a search this index cannot serve", narrow, wide)
	}

	// Bounding it must not change the answer inside the window. Without this the test would
	// reward a bound that scans less by returning less.
	widePage := planQuery(t, f, p.tenant, audit.Filter{Limit: 50, Q: planSearchTerm})
	narrowPage := planQuery(t, f, p.tenant, audit.Filter{
		Limit: 50, Q: planSearchTerm, From: since,
	})
	// Compared window-relative rather than page-relative: the unbounded page is the newest 50
	// matches OVERALL and may reach back past the window, so a same-length comparison would
	// have to skip on some corpora — and a skip asserts nothing. The rows of the unbounded
	// page that fall inside the window must be exactly the front of the bounded page.
	var expected []string
	for _, e := range widePage.Events {
		if !e.CreatedAt.Before(since) {
			expected = append(expected, e.ID)
		}
	}
	if len(expected) == 0 {
		t.Fatalf("no row of the unbounded page falls inside the window, so there is nothing to "+
			"compare (page held %d rows)", len(widePage.Events))
	}
	if len(narrowPage.Events) < len(expected) {
		t.Fatalf("the bounded search returned %d rows but the unbounded page alone has %d "+
			"inside the window; the bound is dropping matches",
			len(narrowPage.Events), len(expected))
	}
	for i, want := range expected {
		if narrowPage.Events[i].ID != want {
			t.Errorf("row %d differs: bounded %s, unbounded %s — bounding the scan changed the "+
				"answer inside the window", i, narrowPage.Events[i].ID, want)
		}
	}
}

// planQuery runs the real Query as the given tenant.
func planQuery(t *testing.T, f *fixture, tenant string, filter audit.Filter) audit.Response {
	t.Helper()
	ctx := context.Background()
	var out audit.Response
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		var e error
		out, e = audit.Query(ctx, tx, filter)
		return e
	}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	return out
}

// planRowsExamined is how many rows the audit_log scan node READ, from EXPLAIN ANALYZE:
// the rows it returned PLUS the rows its Filter threw away. `actual rows` alone is the wrong
// metric — it counts what survived the filter, so a search matching nothing reports 0 however
// much of the table it read, which is the opposite of what a scan-bound test wants to know.
func planRowsExamined(t *testing.T, f *fixture, tenant string, st planStmt) int {
	t.Helper()
	ctx := context.Background()
	var total int
	if err := db.WithinTenantTx(ctx, f.app, tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) "+st.sql, st.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			lines = append(lines, line)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		total = planSumRows(strings.Join(lines, "\n"))
		return nil
	}); err != nil {
		t.Fatalf("EXPLAIN ANALYZE: %v", err)
	}
	return total
}

// planSumRows is planRowsExamined's arithmetic, split out so it can be proven against a
// synthetic plan text (TestAudit_RowsExaminedCountsBothSurvivedAndRemovedRows) rather than
// only ever running against whatever the live corpus happens to produce.
func planSumRows(plan string) int {
	var total int
	onAuditLog := false
	for _, line := range strings.Split(plan, "\n") {
		if target, ok := scanTarget(line); ok {
			onAuditLog = strings.HasPrefix(target, "audit_log")
		}
		if !onAuditLog {
			continue
		}
		if m := planActualRows.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			loops := 1
			if l := planActualLoops.FindStringSubmatch(line); l != nil {
				loops, _ = strconv.Atoi(l[1])
			}
			total += n * loops
		}
		if m := planRemovedByFilter.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			total += n
		}
	}
	return total
}

var (
	planActualRows      = regexp.MustCompile(`actual rows=([0-9]+)`)
	planActualLoops     = regexp.MustCompile(`loops=([0-9]+)`)
	planRemovedByFilter = regexp.MustCompile(`Rows Removed by Filter: ([0-9]+)`)
)

// TestAudit_IndexCondIgnoresTheJoinedTable is the planted positive for indexCond's node
// scoping. Without it the helper concatenates every node, and "tenant_id is in the Index Cond"
// passes on the business_entities node alone — while audit_log, the table the assertion is
// about, has lost its tenant lead entirely. Synthetic on purpose: the real planner will not
// produce this plan on demand, and the helper's correctness must not depend on it doing so.
func TestAudit_IndexCondIgnoresTheJoinedTable(t *testing.T) {
	const plan = `Limit
  ->  Nested Loop Left Join
        ->  Bitmap Heap Scan on audit_log a
              Recheck Cond: (event = 'x'::text)
              ->  Bitmap Index Scan on audit_log_tenant_event_created_idx
                    Index Cond: (event = 'x'::text)
        ->  Bitmap Index Scan on business_entities_tenant_id_id_uq
              Index Cond: (tenant_id = 'deadbeef'::uuid)`

	got := indexCond(plan)
	if got == "" {
		t.Fatalf("indexCond returned nothing for a plan that has two Index Cond lines; the " +
			"scoping is dropping audit_log's own condition")
	}
	if strings.Contains(got, "tenant_id") {
		t.Errorf("indexCond picked up the business_entities node's tenant_id. audit_log has "+
			"none in this plan, so every 'tenant_id is in the Index Cond' assertion would "+
			"pass on a query that reads the whole tenant:\n%s", got)
	}
	if !strings.Contains(got, "event = 'x'") {
		t.Errorf("indexCond lost audit_log's own Index Cond: %s", got)
	}
}

// TestAudit_SeqScanLeavesNoIndexCondToSatisfy closes a mutation-verify gap found during
// AUDIT-04-09 QA: assertServedByIndex's explicit "must not Seq Scan" check is the ONLY guard
// disabled for an empty index argument (""), and on the pinned corpus none of the empty-index
// cases (the composed page, the filtered facets) ever Seq Scan, so disabling that one check
// left all 46 TestAudit_ cases green.
//
// The reason that survives anyway: a Seq Scan node emits Filter lines, never Index Cond, so
// indexCond() returns "" for one — and every case, empty index or not, still requires
// tenant_id inside that string. Synthetic, because a live Seq Scan is not reproducible on
// demand against the pinned corpus.
func TestAudit_SeqScanLeavesNoIndexCondToSatisfy(t *testing.T) {
	const plan = `Limit
  ->  Seq Scan on audit_log a
        Filter: ((tenant_id = 'deadbeef'::uuid) AND (event = 'x'::text))`

	if got := indexCond(plan); got != "" {
		t.Errorf("indexCond(%q) = %q, want empty — a Seq Scan node has no Index Cond line, so "+
			"a query that degrades to one fails the tenant_id-in-cond requirement on its own",
			plan, got)
	}
}

// TestAudit_RowsExaminedCountsBothSurvivedAndRemovedRows closes a mutation-verify gap found
// during AUDIT-04-09 QA: dropping the Rows Removed by Filter term left
// TestAudit_SearchScanIsBoundedByTheDateRange green (measured: wide 2000->1820, narrow
// 154->78 rows examined — narrow stayed below wide either way, because this corpus's search
// term matches at a roughly uniform rate across the date axis, so matched-row counts alone
// already scale with window size on THIS corpus). Synthetic, so the arithmetic is proven
// without depending on that corpus property holding.
func TestAudit_RowsExaminedCountsBothSurvivedAndRemovedRows(t *testing.T) {
	const plan = `Limit
  ->  Bitmap Heap Scan on audit_log a (actual rows=7 loops=3)
        Rows Removed by Filter: 5
        ->  Bitmap Index Scan on audit_log_tenant_created_idx (actual rows=4 loops=1)`

	// (7 actual rows * 3 loops) + 5 removed by the heap filter + (4 actual rows * 1 loop) on
	// the index sub-node = 30.
	if got, want := planSumRows(plan), 30; got != want {
		t.Errorf("planSumRows = %d, want %d — actual rows*loops plus Rows Removed by Filter, "+
			"summed across every line the audit_log node attributes to itself", got, want)
	}
}

// --- AUDIT-11-07: the corpus payloads, and the number search ----------------------------
//
// D-22: every pin in this file is evidence about a 20-tenant x 1,000-row synthetic corpus
// and nothing else. Postgres picks a scan strategy from table-wide reltuples/relpages/
// pg_stats, all computed before RLS filters anything, and audit_log grows without bound by
// design -- its production row count has never been probed. Read a green pin as "the plan
// is the one this corpus was designed for", never as a production guarantee.

// planNumberedEvents are the arms of planEventMix whose payload names an invoice. The insert
// trigger resolves invoice_id from exactly these, so they are the rows a number can reach.
var planNumberedEvents = []string{
	"invoice.created",
	"invoice.updated",
	"invoice.validated",
	"invoice.transitioned",
	"submission.accepted",
	"invoice.approval_approved",
}

// planNumberedRowsPerTenant is how many of a tenant's rows planEventMix leaves numbered.
func planNumberedRowsPerTenant() int {
	numbered := map[string]bool{}
	for _, ev := range planNumberedEvents {
		numbered[ev] = true
	}
	n := 0
	for _, m := range planEventMix {
		if numbered[m.event] {
			n += planRowsPerTenant * m.perMille / 1000
		}
	}
	return n
}

// planNumberTerm derives the search term every number pin below uses: the invoice_number of
// the NEWEST invoice-scoped row inside the date window.
//
// Derived, never hardcoded. planEventMix is laid out monotone in created_at, so the 7-day
// window holds exactly one invoice-scoped row, and which invoice that is falls out of
// planRowsPerTenant % planInvoices. A literal would be an accident of the geometry and would
// silently strand the bounded-page comparison the moment any geometry constant moved.
func planNumberTerm(t *testing.T, f *fixture, p planFixture, since time.Time) string {
	t.Helper()
	ctx := context.Background()
	var term string
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT i.invoice_number
			  FROM audit_log a JOIN invoices i ON i.id = a.invoice_id
			 WHERE a.tenant_id = $1 AND a.created_at >= $2
			 ORDER BY a.created_at DESC, a.id DESC LIMIT 1`, p.tenant, since).Scan(&term)
	}); err != nil {
		t.Fatalf("no invoice-scoped row inside the window, so a bounded number search would "+
			"match nothing and prove nothing: %v", err)
	}
	return term
}

// planSearchArms are the three arms searchFragment always emits for a term that resolves an
// invoice. Matched as plan text: the two ILIKEs render as ~~*, the payload arm as a SubPlan
// reference, and the resolved arm as an array comparison.
var planSearchArms = []string{"~~*", "EXISTS(SubPlan", "invoice_id = ANY ("}

// planArmsIn reports which of planSearchArms appear in a slice of plan text.
func planArmsIn(section string) []string {
	var out []string
	for _, arm := range planSearchArms {
		if strings.Contains(section, arm) {
			out = append(out, arm)
		}
	}
	return out
}

// TestAudit_NoArmOfTheSearchReachesAnIndexCond is AC #6, and it replaces a specified pin that
// claimed the opposite. AUDIT-11-09's a.invoice_id = ANY(...) is leakproof and WOULD be
// index-servable alone, but searchFragment always OR-s it with the two text arms, and an
// OR-group holding a non-leakproof term is non-leakproof whole. So under FORCE RLS no part of
// the group is ever pushed down: measured, all three arms land in one heap Filter on both the
// page and the count.
//
// The assertion is on node SHAPE, never on an index name. The count plan picks a
// tenant-leading index whichever way the corpus falls -- audit_log_tenant_invoice_created_idx
// when this was specified, audit_log_tenant_entity_created_idx when it was built -- with
// tenant_id alone in its Index Cond either way. TestAudit_IndexNameAloneCannotProveAnArmIsServed
// holds that a name is not evidence.
func TestAudit_NoArmOfTheSearchReachesAnIndexCond(t *testing.T) {
	f, p := requirePlanCorpus(t)
	term := planNumberTerm(t, f, p, time.Now().Add(-planDateWindow))
	stmts := planCapture(t, audit.Filter{Limit: 50, Q: term})

	for _, kind := range []string{planKindPage, planKindCount} {
		t.Run(kind, func(t *testing.T) {
			st := stmts[kind]
			if n := countAsApp(t, f, p.tenant, st.sql, st.args...); n == 0 {
				t.Fatalf("the %s statement returns nothing for %q, so its plan proves nothing", kind, term)
			}
			plan := explainAsApp(t, f, p.tenant, st.sql, st.args...)

			cond := indexCond(plan)
			if got := planArmsIn(cond); len(got) != 0 {
				t.Errorf("search arms %v reached audit_log's Index Cond. Under FORCE RLS that "+
					"should be impossible while a non-leakproof term is OR-ed into the group -- "+
					"re-measure pg_proc, and check the group still holds a text arm:\n%s", got, plan)
			}
			if !strings.Contains(cond, "tenant_id") {
				t.Errorf("audit_log has no tenant_id in its Index Cond, so the scan reads more "+
					"than the tenant:\n%s", plan)
			}
			// Vacuity floor, checked last so a real violation above is reported first: all three
			// arms must be IN the heap filter, or the claim is about a plan with no arms at all.
			if got := planArmsIn(planCondLines(plan, "audit_log", "Filter:")); len(got) != len(planSearchArms) {
				t.Fatalf("audit_log's Filter carries only %v of the search arms %v; the checks "+
					"above would prove nothing:\n%s", got, planSearchArms, plan)
			}
		})
	}
}

// TestAudit_IndexNameAloneCannotProveAnArmIsServed is the planted control for the pin above.
// The count plan the architect measured NAMES audit_log_tenant_invoice_created_idx while the
// resolved arm sits in the heap Filter, so a pin asserting the index by name would have passed
// on it while proving nothing. Synthetic on purpose: which tenant-leading index the planner
// picks is an accident of corpus size, so the false green cannot be reproduced on demand.
func TestAudit_IndexNameAloneCannotProveAnArmIsServed(t *testing.T) {
	// Measured shape: the invoice index is named, tenant_id alone is the Index Cond.
	const named = `Aggregate
  ->  Bitmap Heap Scan on audit_log a
        Recheck Cond: (tenant_id = 'deadbeef'::uuid)
        Filter: ((event ~~* '%INV-x%'::text) OR EXISTS(SubPlan 1) OR (invoice_id = ANY ('{f0}'::uuid[])))
        ->  Bitmap Index Scan on audit_log_tenant_invoice_created_idx
              Index Cond: (tenant_id = 'deadbeef'::uuid)
        SubPlan 1
          ->  Function Scan on jsonb_each_text kv
                Filter: ((key <> 'invoice_number'::text) AND (value ~~* '%INV-x%'::text))`

	if !strings.Contains(named, planIdxInvoice) {
		t.Fatalf("the control plan does not name %s, so it cannot demonstrate the false green", planIdxInvoice)
	}
	if got := planArmsIn(indexCond(named)); len(got) != 0 {
		t.Errorf("planArmsIn found %v in an Index Cond that holds tenant_id alone; the shape "+
			"check would red on the very plan the name-only check passes", got)
	}
	if got := planArmsIn(planCondLines(named, "audit_log", "Filter:")); len(got) != len(planSearchArms) {
		t.Errorf("the arms are in the heap Filter but planArmsIn saw only %v; the vacuity floor "+
			"cannot tell a served arm from a missing one", got)
	}

	// Served shape: the same arm folded INTO the Index Cond. Without this the check above
	// passes on a helper that always returns nothing.
	const served = `Aggregate
  ->  Bitmap Heap Scan on audit_log a
        Recheck Cond: ((tenant_id = 'deadbeef'::uuid) AND (invoice_id = ANY ('{f0}'::uuid[])))
        ->  Bitmap Index Scan on audit_log_tenant_invoice_created_idx
              Index Cond: ((tenant_id = 'deadbeef'::uuid) AND (invoice_id = ANY ('{f0}'::uuid[])))`

	if got := planArmsIn(indexCond(served)); len(got) != 1 || got[0] != "invoice_id = ANY (" {
		t.Errorf("planArmsIn = %v for a plan whose Index Cond carries the resolved arm, want "+
			"exactly the resolved arm — the pin above cannot detect what it exists to catch", got)
	}
}

// TestAudit_NumberTermReachesOnlyTheResolvedArm is AC #4. A number reaches the corpus through
// a.invoice_id = ANY(...) and through nothing else: it is in no event name, and AUDIT-11-09
// fenced the generic payload arm off the invoice_number key.
//
// The third count is the vacuity floor, and it is why AUDIT-11-01's widening was not
// bookkeeping: before it, the term matched nothing through the generic arm with OR without
// the fence, so "the fence holds" was a claim about an empty set. It now matches 12 rows
// unfenced and none fenced.
//
// This pin measures the CORPUS, not filter.go. Removing the fence from searchFragment leaves
// it green -- measured -- because the resolved arm already matches the same rows here. The
// behavioural claim belongs to AUDIT-11-09: TestAuditSearch_NumberKeyIsNotReachableThroughTheGenericArm,
// TestAuditSearch_NumberResolvesThroughTheLiveTableNotTheFrozenPayload and
// TestAuditFilter_GenericValueArmSkipsTheNumberKey are the three that red on that mutation.
func TestAudit_NumberTermReachesOnlyTheResolvedArm(t *testing.T) {
	f, p := requirePlanCorpus(t)
	term := planNumberTerm(t, f, p, time.Now().Add(-planDateWindow))

	var viaEvent, viaGeneric, viaGenericUnfenced int
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE a.event ILIKE '%' || $2 || '%' ESCAPE '\'),
			       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM jsonb_each_text(a.payload) kv
			                                       WHERE kv.key <> 'invoice_number'
			                                         AND kv.value ILIKE '%' || $2 || '%' ESCAPE '\')),
			       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM jsonb_each_text(a.payload) kv
			                                       WHERE kv.value ILIKE '%' || $2 || '%' ESCAPE '\'))
			  FROM audit_log a WHERE a.tenant_id = $1`, p.tenant, term).
			Scan(&viaEvent, &viaGeneric, &viaGenericUnfenced)
	}); err != nil {
		t.Fatalf("count the arms a number term reaches: %v", err)
	}

	found := planQuery(t, f, p.tenant, audit.Filter{Limit: 50, Q: term})
	if len(found.Events) == 0 {
		t.Fatalf("searching %q returns nothing, so which arm carried it is not a question", term)
	}
	if viaEvent != 0 {
		t.Errorf("%q matches %d corpus event names, so the event arm could carry it and this "+
			"case no longer isolates the resolved arm", term, viaEvent)
	}
	if viaGeneric != 0 {
		t.Errorf("%q matches %d rows through the fenced generic arm; AUDIT-11-09's exclusion "+
			"is not holding", term, viaGeneric)
	}
	if viaGenericUnfenced == 0 {
		t.Fatalf("%q matches nothing through the UNFENCED generic arm either, so the exclusion "+
			"is vacuous here and the check above proves nothing", term)
	}
}

// TestAudit_NumberSearchIsBoundedByTheDateRange is AC #4's other half, and the same claim
// TestAudit_SearchScanIsBoundedByTheDateRange makes for a term that rides the event arm: the
// only thing bounding a search no index can serve is the date range.
func TestAudit_NumberSearchIsBoundedByTheDateRange(t *testing.T) {
	f, p := requirePlanCorpus(t)
	since := time.Now().Add(-planDateWindow)
	term := planNumberTerm(t, f, p, since)

	wide := planRowsExamined(t, f, p.tenant, planCapture(t, audit.Filter{Limit: 50, Q: term})[planKindCount])
	narrow := planRowsExamined(t, f, p.tenant,
		planCapture(t, audit.Filter{Limit: 50, Q: term, From: since})[planKindCount])

	if wide == 0 || narrow == 0 {
		t.Fatalf("one of the runs examined no rows at all (wide=%d narrow=%d); a comparison "+
			"between them proves nothing", wide, narrow)
	}
	if narrow >= wide {
		t.Errorf("the 7-day bound examined %d rows and the unbounded number search %d; the date "+
			"range is the only thing bounding it", narrow, wide)
	}

	// And the bound must not change the answer inside the window. The term is derived from the
	// newest in-window invoice-scoped row precisely so this half has something to compare.
	widePage := planQuery(t, f, p.tenant, audit.Filter{Limit: 50, Q: term})
	narrowPage := planQuery(t, f, p.tenant, audit.Filter{Limit: 50, Q: term, From: since})

	var expected []string
	for _, e := range widePage.Events {
		if !e.CreatedAt.Before(since) {
			expected = append(expected, e.ID)
		}
	}
	if len(expected) == 0 {
		t.Fatalf("no row of the unbounded page falls inside the window, so there is nothing to "+
			"compare (page held %d rows)", len(widePage.Events))
	}
	if len(narrowPage.Events) < len(expected) {
		t.Fatalf("the bounded search returned %d rows but the unbounded page alone has %d inside "+
			"the window; the bound is dropping matches", len(narrowPage.Events), len(expected))
	}
	for i, want := range expected {
		if narrowPage.Events[i].ID != want {
			t.Errorf("row %d differs: bounded %s, unbounded %s — bounding the scan changed the "+
				"answer inside the window", i, narrowPage.Events[i].ID, want)
		}
	}
}

// TestAudit_NumberFoldInIsTenantLeadingWithTheIlikeAsAFilter is AC #7. The number fold-in is
// the one lookup this story adds to the request path, and it is a per-tenant scan of invoices:
// tenant_id leads the index, the ILIKE follows as a heap filter. Node type, buffer counts and
// timings are deliberately unasserted -- a Bitmap plan over the same index is the same claim.
//
// EXPLAINs the statement resolveSearchTargets actually issued, captured off a tracer, not a
// hand-written copy of it.
func TestAudit_NumberFoldInIsTenantLeadingWithTheIlikeAsAFilter(t *testing.T) {
	f, p := requirePlanCorpus(t)
	term := planNumberTerm(t, f, p, time.Now().Add(-planDateWindow))
	st := planFoldInStmt(t, term)

	if n := countAsApp(t, f, p.tenant, st.sql, st.args...); n == 0 {
		t.Fatalf("the fold-in resolves no invoice for %q, so its plan proves nothing", term)
	}
	plan := explainAsApp(t, f, p.tenant, st.sql, st.args...)

	if !strings.Contains(plan, "invoices_tenant_id_id_uq") {
		t.Errorf("the fold-in does not use invoices_tenant_id_id_uq:\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on invoices") {
		t.Errorf("the fold-in Seq Scans invoices; the tenant lead is gone:\n%s", plan)
	}
	cond := planCondLines(plan, "invoices", "Index Cond:") + "\n" +
		planCondLines(plan, "invoices", "Recheck Cond:")
	if !strings.Contains(cond, "tenant_id") {
		t.Errorf("tenant_id is not in the fold-in's Index or Recheck Cond, so the lookup reads "+
			"past the tenant before filtering:\n%s", plan)
	}
	if filter := planCondLines(plan, "invoices", "Filter:"); !strings.Contains(filter, "invoice_number ~~*") {
		t.Errorf("the invoice_number ILIKE is not a heap Filter on invoices; re-measure before "+
			"believing it reached an index:\n%s", plan)
	}
}

// planFoldInStmt captures the invoices lookup resolveSearchTargets issues for q. Keyed on
// FROM invoices, so a rename that stops the lookup happening fails loudly here rather than
// leaving the case silently unasserted.
func planFoldInStmt(t *testing.T, q string) planStmt {
	t.Helper()
	for _, st := range planTrace(t, audit.Filter{Limit: 50, Q: q}) {
		if strings.Contains(st.sql, "FROM invoices") {
			return st
		}
	}
	t.Fatalf("Query issued no statement against invoices for q=%q; the fold-in is not running", q)
	return planStmt{}
}

// TestAudit_PlanCorpusCarriesAnInvoiceNumber is the floor under every pin below it. Before
// AUDIT-11-01 the corpus payloads carried an id and nothing else, so a search for a number
// matched nothing through any arm and a pin phrased against one would be vacuous.
func TestAudit_PlanCorpusCarriesAnInvoiceNumber(t *testing.T) {
	f, p := requirePlanCorpus(t)

	withKey := map[string]int{}
	total := map[string]int{}
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `
			SELECT event, count(*) FILTER (WHERE payload ? 'invoice_number'), count(*)
			  FROM audit_log WHERE tenant_id = $1 GROUP BY event`, p.tenant)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var ev string
			var n, all int
			if e := rows.Scan(&ev, &n, &all); e != nil {
				return e
			}
			withKey[ev] = n
			total[ev] = all
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("count payloads carrying invoice_number: %v", err)
	}

	numbered := map[string]bool{}
	for _, ev := range planNumberedEvents {
		numbered[ev] = true
		if total[ev] == 0 {
			t.Fatalf("the corpus holds no %s rows at all, so its payload shape proves nothing", ev)
		}
		if withKey[ev] != total[ev] {
			t.Errorf("%s: %d of %d rows carry invoice_number, want all of them", ev, withKey[ev], total[ev])
		}
	}
	for _, m := range planEventMix {
		if numbered[m.event] {
			continue
		}
		if withKey[m.event] != 0 {
			t.Errorf("%s is workspace-level but %d of its rows carry invoice_number; the mix "+
				"must leave those payloads unnumbered", m.event, withKey[m.event])
		}
	}
}

// TestAudit_PlanCorpusNumberMatchesTheResolvedInvoice stops the widening from writing a
// constant. A constant would leave every pin below green while the number in the payload
// named a different invoice than the row's own invoice_id.
func TestAudit_PlanCorpusNumberMatchesTheResolvedInvoice(t *testing.T) {
	f, p := requirePlanCorpus(t)

	var joined, disagree int
	ctx := context.Background()
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE a.payload->>'invoice_number' IS DISTINCT FROM i.invoice_number)
			  FROM audit_log a JOIN invoices i ON i.id = a.invoice_id
			 WHERE a.tenant_id = $1 AND a.payload ? 'invoice_number'`, p.tenant).
			Scan(&joined, &disagree)
	}); err != nil {
		t.Fatalf("join corpus payloads to invoices: %v", err)
	}

	if joined == 0 {
		t.Fatalf("no corpus row joins an invoice through invoice_id while carrying a number, " +
			"so the agreement check proves nothing")
	}
	if disagree != 0 {
		t.Errorf("%d of %d numbered rows name an invoice_number that is not their own "+
			"invoice_id's; payload and column must agree", disagree, joined)
	}
}
