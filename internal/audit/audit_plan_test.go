// Plan proofs for audit_log's four tenant-leading read indexes. Every case asserts the
// index NAME and the absence of a Seq Scan, never the node type -- a Bitmap plan over the
// right index is still the right index.
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
)

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
	{"invoice.created", 300, `jsonb_build_object('id', r.invoice)`},
	{"invoice.updated", 200, `jsonb_build_object('id', r.invoice)`},
	{"invoice.validated", 150, `jsonb_build_object('id', r.invoice)`},
	{"invoice.transitioned", 120, `jsonb_build_object('id', r.invoice)`},
	{"submission.accepted", 80, `jsonb_build_object('invoice_id', r.invoice)`},
	{"invoice.approval_approved", 50, `jsonb_build_object('invoice_id', r.invoice)`},
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
			SELECT array_agg(id ORDER BY invoice_number) AS ids FROM invoices WHERE tenant_id = $1
		), ent AS (
			SELECT array_agg(id ORDER BY name) AS ids FROM business_entities WHERE tenant_id = $1
		), r AS (
			SELECT (g % 1000)::int                                        AS bucket,
			       inv.ids[((g % ` + strconv.Itoa(planInvoices) + `) + 1)::int] AS invoice,
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
	var rows, attributed, entities, actors int
	var oldest, newest time.Time
	if err := db.WithinTenantTx(ctx, f.app, p.tenant, func(tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `
			SELECT count(*), count(entity_id), count(DISTINCT entity_id), count(DISTINCT actor),
			       min(created_at), max(created_at)
			  FROM audit_log WHERE tenant_id = $1`, p.tenant).
			Scan(&rows, &attributed, &entities, &actors, &oldest, &newest); e != nil {
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

	if n := countAsApp(t, f, tenant, sql, args...); n == 0 {
		t.Fatalf("the query returns no rows, so its plan proves nothing about %s", index)
	}

	plan := explainAsApp(t, f, tenant, sql, args...)
	if plan == "" || !strings.Contains(plan, "audit_log") {
		t.Fatalf("plan = %q, want a non-empty plan mentioning audit_log", plan)
	}
	if !strings.Contains(plan, index) {
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

// indexCond joins the plan's Index Cond lines, so a column asserted against it cannot be
// satisfied by a Filter line or by the node's own name.
func indexCond(plan string) string {
	var out []string
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "Index Cond:") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, "\n")
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
