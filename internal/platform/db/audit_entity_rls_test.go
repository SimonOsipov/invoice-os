// Read-path isolation over audit_log's entity_id column and the four read indexes.
//
// Every "zero rows" claim is paired, in the same test, with the SAME query returning the
// caller's own seeded ids. Without the pair, a predicate broken by a wrong column, a bad
// cast or a dead policy passes vacuously: "no rows for this tenant" and "this query can
// never return rows" are indistinguishable.
//
// Two roles, because they fail differently: invoice_app is the runtime identity and is
// stopped by the policy alone, while invoice_migrator OWNS the table and is stopped only
// because RLS is FORCEd. A suite that tested one would miss the other's regression.
//
// Fixtures let the BEFORE INSERT trigger fill entity_id. It assigns unconditionally, so an
// explicitly-set column is silently discarded — a workspace-level row inserted WITH an
// entity_id reads back NULL.
//
// audit_log rows can never be deleted, so each fixture mints a throwaway tenant and leaves
// its rows behind; only the tenant's domain rows are cleaned up.
package db_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The three events the fixture writes, one per attribution outcome.
const (
	auditReadEventE1   = "portfolio.entity.created" // resolves to entity1
	auditReadEventE2   = "portfolio.entity.updated" // resolves to entity2
	auditReadEventNull = "workflow_role.created"    // workspace-level, stays NULL
)

// The four new indexes, by the predicate each one leads on after tenant_id.
const (
	auditIdxCreated = "audit_log_tenant_created_idx"
	auditIdxEvent   = "audit_log_tenant_event_created_idx"
	auditIdxActor   = "audit_log_tenant_actor_created_idx"
	auditIdxEntity  = "audit_log_tenant_entity_created_idx"
)

// --- fixture ------------------------------------------------------------------------

type auditReadFixture struct {
	tenant    string
	entity1   string
	entity2   string
	actorMain string // the 5 entity-attributed rows
	actorAlt  string // the 4 workspace-level rows
	base      time.Time

	ids1    []int64 // 3 rows at entity1, created_at base+0s..+2s
	ids2    []int64 // 2 rows at entity2, created_at base+3s..+4s
	idsNull []int64 // 4 workspace-level rows, created_at base+5s..+8s
}

// entityIDs are the rows actorMain wrote — the 5 with a non-NULL entity_id.
func (f auditReadFixture) entityIDs() []int64 {
	return append(append([]int64{}, f.ids1...), f.ids2...)
}

func (f auditReadFixture) allIDs() []int64 {
	return append(f.entityIDs(), f.idsNull...)
}

// auditReadBase is the shared created_at anchor. Both tenants in a test seed from the
// same base so a window covering one covers the other, making a window's emptiness
// attributable to RLS and nothing else.
func auditReadBase() time.Time {
	return time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
}

// seedAuditReadFixture commits a throwaway tenant, two entities and nine audit rows as the
// superuser (BYPASSRLS, so no tenant context needed). Ids are generated here, never read
// from db/seed.dev.sql, whose rows default theirs to gen_random_uuid().
func seedAuditReadFixture(t *testing.T, base time.Time) auditReadFixture {
	t.Helper()
	ctx := context.Background()
	f := auditReadFixture{
		tenant:  uuid.NewString(),
		entity1: uuid.NewString(),
		entity2: uuid.NewString(),
		base:    base,
	}
	f.actorMain = "audit-read-main-" + f.tenant[:8]
	f.actorAlt = "audit-read-alt-" + f.tenant[:8]

	exec := func(sql string, args ...any) {
		if _, err := h.super.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed audit-read fixture (%s): %v", sql, err)
		}
	}
	exec(`INSERT INTO tenants (id, name) VALUES ($1, $2)`, f.tenant, "audit-read-"+f.tenant[:8])
	for _, e := range []string{f.entity1, f.entity2} {
		exec(`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
			e, f.tenant, "entity-"+e[:8])
	}

	row := func(offset int, actor, event, payload string) int64 {
		var id int64
		if err := h.super.QueryRow(ctx,
			`INSERT INTO audit_log (tenant_id, actor, event, payload, created_at)
			 VALUES ($1, $2, $3, $4::jsonb, $5) RETURNING id`,
			f.tenant, actor, event, payload, base.Add(time.Duration(offset)*time.Second),
		).Scan(&id); err != nil {
			t.Fatalf("seed audit row %s: %v", event, err)
		}
		return id
	}
	for i := 0; i < 3; i++ {
		f.ids1 = append(f.ids1, row(i, f.actorMain, auditReadEventE1, auditPayloadJSON("id", f.entity1)))
	}
	for i := 3; i < 5; i++ {
		f.ids2 = append(f.ids2, row(i, f.actorMain, auditReadEventE2, auditPayloadJSON("id", f.entity2)))
	}
	for i := 5; i < 9; i++ {
		f.idsNull = append(f.idsNull, row(i, f.actorAlt, auditReadEventNull, `{"key":"approver"}`))
	}

	t.Cleanup(func() {
		for _, sql := range []string{
			`DELETE FROM business_entities WHERE tenant_id = $1`,
			`DELETE FROM tenants WHERE id = $1`,
		} {
			_, _ = h.super.Exec(context.Background(), sql, f.tenant)
		}
	})

	assertAuditReadTriggerAttributed(t, f)
	return f
}

// assertAuditReadTriggerAttributed fails the seed if the write-time trigger did not fill
// entity_id as expected. Without it, a suite running against an unattributed table would
// report four confusing downstream failures instead of one clear one.
func assertAuditReadTriggerAttributed(t *testing.T, f auditReadFixture) {
	t.Helper()
	for _, want := range []struct {
		entity string
		ids    []int64
	}{{f.entity1, f.ids1}, {f.entity2, f.ids2}} {
		n := mustCount(t, h.super,
			`SELECT count(*) FROM audit_log WHERE id = ANY($1) AND entity_id = $2`, want.ids, want.entity)
		if n != len(want.ids) {
			t.Fatalf("seed: %d of %d rows carry entity_id %s — the insert trigger did not attribute them",
				n, len(want.ids), want.entity)
		}
	}
	if n := mustCount(t, h.super,
		`SELECT count(*) FROM audit_log WHERE id = ANY($1) AND entity_id IS NULL`, f.idsNull); n != len(f.idsNull) {
		t.Fatalf("seed: %d of %d workspace-level rows have a NULL entity_id", n, len(f.idsNull))
	}
}

// --- read helpers -------------------------------------------------------------------

func auditScanIDs(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]int64, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// auditReadIDsErr runs sql as role/tenant and returns the ids AND the error, so a caller
// can assert that a cross-tenant read filters silently instead of raising.
func auditReadIDsErr(pool *pgxpool.Pool, tenant, sql string, args ...any) ([]int64, error) {
	ctx := context.Background()
	var out []int64
	err := db.WithinTenantTx(ctx, pool, tenant, func(tx pgx.Tx) error {
		var e error
		out, e = auditScanIDs(ctx, tx, sql, args...)
		return e
	})
	return out, err
}

func auditReadIDs(t *testing.T, pool *pgxpool.Pool, tenant, sql string, args ...any) []int64 {
	t.Helper()
	out, err := auditReadIDsErr(pool, tenant, sql, args...)
	if err != nil {
		t.Fatalf("read %q: %v", sql, err)
	}
	return out
}

// assertAuditReadIDs is the positive control: exact ids, never mere non-emptiness. An
// empty want is rejected outright — it would assert nothing.
func assertAuditReadIDs(t *testing.T, what string, got, want []int64) {
	t.Helper()
	if len(want) == 0 {
		t.Fatalf("%s: positive control has an empty want set, so it would pass vacuously", what)
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want exactly %v", what, got, want)
	}
}

func assertAuditReadEmpty(t *testing.T, what string, got []int64) {
	t.Helper()
	if len(got) != 0 {
		t.Errorf("%s returned %v, want no rows", what, got)
	}
}

// assertAuditReadDisjoint is the absence half for a predicate whose value both tenants
// share (event, created_at): the caller's own rows come back, the other tenant's must not.
func assertAuditReadDisjoint(t *testing.T, what string, got, foreign []int64) {
	t.Helper()
	if len(foreign) == 0 {
		t.Fatalf("%s: nothing to be disjoint from, so the check would pass vacuously", what)
	}
	var leaked []int64
	for _, id := range got {
		if slices.Contains(foreign, id) {
			leaked = append(leaked, id)
		}
	}
	// One error for the whole set: a total-isolation regression leaks every foreign row,
	// and per-id reporting buries the result in thousands of lines.
	if leaked != nil {
		t.Errorf("%s leaked %d of the other tenant's rows: %v", what, len(leaked), leaked)
	}
}

const auditReadByEntity = `SELECT id FROM audit_log WHERE entity_id = $1 ORDER BY id`

// --- AC-1, AC-2: the entity_id predicate --------------------------------------------

// The entity_id read is tenant-scoped for invoice_app: own entities resolve to exactly
// their seeded ids, the workspace-level rows come back as their own bucket, and the SAME
// query with another tenant's entity returns nothing without raising. B reading that very
// argument successfully is what proves the empty result was isolation, not a dead
// predicate.
func TestRLS_AuditReadEntityIDIsTenantScopedForApp(t *testing.T) {
	requireHarness(t)
	base := auditReadBase()
	a := seedAuditReadFixture(t, base)
	b := seedAuditReadFixture(t, base)

	assertAuditReadIDs(t, "A reading its own entity1",
		auditReadIDs(t, h.app, a.tenant, auditReadByEntity, a.entity1), a.ids1)
	assertAuditReadIDs(t, "A reading its own entity2",
		auditReadIDs(t, h.app, a.tenant, auditReadByEntity, a.entity2), a.ids2)
	assertAuditReadIDs(t, "A reading its workspace-level rows",
		auditReadIDs(t, h.app, a.tenant, `SELECT id FROM audit_log WHERE entity_id IS NULL ORDER BY id`), a.idsNull)

	got, err := auditReadIDsErr(h.app, a.tenant, auditReadByEntity, b.entity1)
	if err != nil {
		t.Fatalf("A reading B's entity1 raised %v; RLS must filter the rows out, not error", err)
	}
	assertAuditReadEmpty(t, "A reading B's entity1", got)

	assertAuditReadIDs(t, "B reading its own entity1 (the same argument A saw nothing for)",
		auditReadIDs(t, h.app, b.tenant, auditReadByEntity, b.entity1), b.ids1)
}

// --- AC-3: no tenant context -------------------------------------------------------

// With no app.current_tenant the policy's qual is NULL and every new index reads empty.
// The second transaction runs on the SAME pooled connection with A's context set and gets
// the rows back, so the emptiness above is the missing GUC and not the connection.
func TestRLS_AuditReadFailsClosedWithoutTenantContext(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a := seedAuditReadFixture(t, auditReadBase())

	probes := []struct {
		what string
		sql  string
		args []any
		want []int64
	}{
		{auditIdxEntity, auditReadByEntity, []any{a.entity1}, a.ids1},
		{auditIdxEvent, `SELECT id FROM audit_log WHERE event = $1 ORDER BY id`, []any{auditReadEventE1}, a.ids1},
		{auditIdxActor, `SELECT id FROM audit_log WHERE actor = $1 ORDER BY id`, []any{a.actorMain}, a.entityIDs()},
		{auditIdxCreated, `SELECT id FROM audit_log WHERE created_at BETWEEN $1 AND $2 ORDER BY id`,
			[]any{a.base, a.base.Add(2 * time.Second)}, a.ids1},
	}

	conn, err := h.app.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire app conn: %v", err)
	}
	defer conn.Release()

	noCtx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin without tenant context: %v", err)
	}
	for _, p := range probes {
		got, err := auditScanIDs(ctx, noCtx, p.sql, p.args...)
		if err != nil {
			t.Fatalf("%s with no tenant context raised %v; RLS must filter, not error", p.what, err)
		}
		assertAuditReadEmpty(t, p.what+" with no tenant context", got)
	}
	_ = noCtx.Rollback(ctx)

	withCtx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin with tenant context: %v", err)
	}
	defer withCtx.Rollback(ctx)
	if _, err := withCtx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, a.tenant); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	for _, p := range probes {
		got, err := auditScanIDs(ctx, withCtx, p.sql, p.args...)
		if err != nil {
			t.Fatalf("%s with A's tenant context: %v", p.what, err)
		}
		assertAuditReadIDs(t, p.what+" with A's tenant context on the same connection", got, p.want)
	}
}

// --- AC-1, AC-2 across all four indexes, for both roles -----------------------------

// assertAuditReadPredicatesAreTenantScoped drives one role through every new index's lead
// predicate. entity_id and actor carry per-tenant values, so the cross-tenant form is an
// exact-zero assertion; event and created_at are shared by both fixtures, so their absence
// half is that none of the other tenant's ids come back.
func assertAuditReadPredicatesAreTenantScoped(t *testing.T, pool *pgxpool.Pool, who string, a, b auditReadFixture) {
	t.Helper()

	perTenant := []struct {
		index        string
		sql          string
		aArg, bArg   any
		aWant, bWant []int64
	}{
		{auditIdxEntity, auditReadByEntity, a.entity1, b.entity1, a.ids1, b.ids1},
		{auditIdxActor, `SELECT id FROM audit_log WHERE actor = $1 ORDER BY id`,
			a.actorMain, b.actorMain, a.entityIDs(), b.entityIDs()},
	}
	for _, c := range perTenant {
		assertAuditReadIDs(t, who+" A own "+c.index, auditReadIDs(t, pool, a.tenant, c.sql, c.aArg), c.aWant)
		assertAuditReadIDs(t, who+" B own "+c.index, auditReadIDs(t, pool, b.tenant, c.sql, c.bArg), c.bWant)

		got, err := auditReadIDsErr(pool, a.tenant, c.sql, c.bArg)
		if err != nil {
			t.Fatalf("%s A reading B's %s raised %v; RLS must filter, not error", who, c.index, err)
		}
		assertAuditReadEmpty(t, who+" A reading B's "+c.index, got)
	}

	shared := []struct {
		index        string
		sql          string
		args         []any
		aWant, bWant []int64
	}{
		{auditIdxEvent, `SELECT id FROM audit_log WHERE event = $1 ORDER BY id`,
			[]any{auditReadEventE1}, a.ids1, b.ids1},
		{auditIdxCreated, `SELECT id FROM audit_log WHERE created_at BETWEEN $1 AND $2 ORDER BY id`,
			[]any{a.base.Add(-time.Minute), a.base.Add(time.Minute)}, a.allIDs(), b.allIDs()},
	}
	for _, c := range shared {
		gotA := auditReadIDs(t, pool, a.tenant, c.sql, c.args...)
		assertAuditReadIDs(t, who+" A "+c.index, gotA, c.aWant)
		assertAuditReadDisjoint(t, who+" A "+c.index, gotA, c.bWant)

		gotB := auditReadIDs(t, pool, b.tenant, c.sql, c.args...)
		assertAuditReadIDs(t, who+" B "+c.index, gotB, c.bWant)
		assertAuditReadDisjoint(t, who+" B "+c.index, gotB, c.aWant)
	}

	// created_at is a live predicate, not a pass-through: a window over the first three
	// rows must exclude the other six.
	assertAuditReadIDs(t, who+" A narrow "+auditIdxCreated,
		auditReadIDs(t, pool, a.tenant,
			`SELECT id FROM audit_log WHERE created_at BETWEEN $1 AND $2 ORDER BY id`,
			a.base, a.base.Add(2*time.Second)), a.ids1)
}

// invoice_app is the runtime identity; the policy alone is what scopes it.
func TestRLS_AuditReadEveryIndexPredicateIsTenantScopedForApp(t *testing.T) {
	requireHarness(t)
	base := auditReadBase()
	a := seedAuditReadFixture(t, base)
	b := seedAuditReadFixture(t, base)
	assertAuditReadPredicatesAreTenantScoped(t, h.app, "app", a, b)
}

// invoice_migrator OWNS audit_log, so only FORCE ROW LEVEL SECURITY subjects it to the
// policy. Drop the FORCE and this case sees both tenants while the app case still passes.
func TestRLS_AuditReadEveryIndexPredicateIsTenantScopedForTheOwner(t *testing.T) {
	requireHarness(t)
	base := auditReadBase()
	a := seedAuditReadFixture(t, base)
	b := seedAuditReadFixture(t, base)
	assertAuditReadPredicatesAreTenantScoped(t, h.mig, "owner", a, b)
}

// --- AC-4: the tenant qual's position in the plan ------------------------------------

// auditReadIndexCond returns the plan's single Index Cond line, or "".
func auditReadIndexCond(plan string) string {
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "Index Cond:") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func auditReadPlan(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args ...any) string {
	t.Helper()
	rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
	if err != nil {
		t.Fatalf("EXPLAIN %q: %v", sql, err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan line: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN %q: %v", sql, err)
	}
	return strings.Join(lines, "\n")
}

// An index changes the plan, and a plan change is where an isolation regression hides: the
// RLS qual must stay an Index Cond on the leading tenant_id column rather than degrade to a
// post-scan Filter. enable_seqscan is off because nine rows would seq-scan on cost alone —
// this case asks WHERE the tenant qual lands once an index is used, not whether the planner
// picks one (that is AUDIT-01-05's question).
func TestRLS_AuditReadTenantQualIsAnIndexCondOnEveryNewIndex(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	a := seedAuditReadFixture(t, auditReadBase())

	const order = ` ORDER BY created_at DESC, id DESC LIMIT 50`
	cases := []struct {
		index string
		sql   string
		args  []any
	}{
		{auditIdxCreated, `SELECT id FROM audit_log WHERE created_at >= $1` + order, []any{a.base}},
		{auditIdxEvent, `SELECT id FROM audit_log WHERE event = $1` + order, []any{auditReadEventE1}},
		{auditIdxActor, `SELECT id FROM audit_log WHERE actor = $1` + order, []any{a.actorMain}},
		{auditIdxEntity, `SELECT id FROM audit_log WHERE entity_id = $1` + order, []any{a.entity1}},
	}

	err := db.WithinTenantTx(ctx, h.app, a.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			return err
		}
		for _, c := range cases {
			plan := auditReadPlan(t, ctx, tx, c.sql, c.args...)
			if !strings.Contains(plan, "audit_log") {
				t.Errorf("%s: plan does not mention audit_log, so nothing below asserts anything:\n%s", c.index, plan)
				continue
			}
			if !strings.Contains(plan, c.index) {
				t.Errorf("%s: plan does not use it:\n%s", c.index, plan)
				continue
			}
			cond := auditReadIndexCond(plan)
			if cond == "" {
				t.Errorf("%s: plan has no Index Cond line:\n%s", c.index, plan)
				continue
			}
			if !strings.Contains(cond, "tenant_id") {
				t.Errorf("%s: Index Cond = %q, want the tenant_id qual inside it — the RLS "+
					"predicate must not degrade to a post-scan Filter", c.index, cond)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("plan-shape transaction: %v", err)
	}
}
