// The Down half of the audit_log entity_id migration: it must drop the eight objects the
// Up added, and leave every pre-existing audit_log object -- including the rows -- alone.
//
// CI's `migrations` job proves reset -> up round-trips; nothing there looks at what Down
// leaves behind. These cases replay the shipped Down body (read out of migrations.FS) on
// one invoice_migrator connection inside a transaction that is always rolled back, so the
// assertions see the real body's effect without committing schema churn.
//
// Every absence assertion is paired with a positive control taken from the SAME query, so
// a catalog query that silently returns nothing cannot pass by finding nothing.
package db_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The eight objects the Up adds and the Down must remove.
var (
	auditDownAddedIndexes = []string{
		"audit_log_tenant_created_idx",
		"audit_log_tenant_event_created_idx",
		"audit_log_tenant_actor_created_idx",
		"audit_log_tenant_entity_created_idx",
	}
	auditDownAddedFunctions = []string{"audit_log_entity_for", "audit_log_set_entity"}
)

const (
	auditDownAddedColumn  = "entity_id"
	auditDownAddedTrigger = "audit_log_entity_on_insert"

	// Pre-existing: created by the audit_log table migration and the document.created
	// index migration, neither of which this migration's Down may touch.
	auditDownKeptPartialIndex = "audit_log_document_created_idx"
	auditDownKeptPartialPred  = "(event = 'document.created'::text)"
	auditDownKeptFunction     = "audit_log_append_only"
	auditDownKeptPolicy       = "tenant_isolation"
)

var (
	auditDownKeptIndexes  = []string{"audit_log_pkey", auditDownKeptPartialIndex}
	auditDownKeptTriggers = []string{"audit_log_no_truncate", "audit_log_no_update_delete"}
	auditDownKeptColumns  = []string{"id", "tenant_id", "actor", "event", "payload", "created_at"}
	auditDownAppGrants    = []string{"INSERT", "SELECT"}
)

// --- catalog snapshot ---------------------------------------------------------------

// auditDownCatalog is everything about audit_log this subtask asserts on, read inside an
// open transaction so it sees the migration body's uncommitted DDL.
type auditDownCatalog struct {
	columns   []string          // ordinal order
	indexes   map[string]string // indexname -> partial predicate ("" when unconditional)
	triggers  map[string]string // tgname -> tgenabled
	functions []string          // audit_log%-named functions
	policies  map[string]string // polname -> USING expression
	rowSec    bool
	forceSec  bool
	appGrants []string // invoice_app's privileges on audit_log, sorted
}

func auditDownReadCatalog(t *testing.T, ctx context.Context, tx pgx.Tx) auditDownCatalog {
	t.Helper()
	c := auditDownCatalog{
		indexes:  map[string]string{},
		triggers: map[string]string{},
		policies: map[string]string{},
	}

	c.columns = auditDownQueryStrings(t, ctx, tx, "audit_log columns",
		`SELECT column_name FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'audit_log'
		   ORDER BY ordinal_position`)

	auditDownQueryPairs(t, ctx, tx, "audit_log indexes", c.indexes,
		`SELECT i.relname, coalesce(pg_get_expr(x.indpred, x.indrelid), '')
		   FROM pg_index x JOIN pg_class i ON i.oid = x.indexrelid
		   WHERE x.indrelid = 'audit_log'::regclass`)

	auditDownQueryPairs(t, ctx, tx, "audit_log triggers", c.triggers,
		`SELECT tgname, tgenabled FROM pg_trigger
		   WHERE tgrelid = 'audit_log'::regclass AND NOT tgisinternal`)

	c.functions = auditDownQueryStrings(t, ctx, tx, "audit_log functions",
		`SELECT proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		   WHERE n.nspname = 'public' AND p.proname LIKE 'audit\_log%' ORDER BY proname`)

	// Unguarded: this map only ever backs a PRESENCE assertion, and "no policies at all"
	// is the defect to report, not a broken query.
	auditDownQueryPairsAllowEmpty(t, ctx, tx, "audit_log policies", c.policies,
		`SELECT polname, pg_get_expr(polqual, polrelid) FROM pg_policy
		   WHERE polrelid = 'audit_log'::regclass`)

	if err := tx.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'audit_log'::regclass`,
	).Scan(&c.rowSec, &c.forceSec); err != nil {
		t.Fatalf("read audit_log RLS flags: %v", err)
	}

	// Unguarded for the same reason as the policies above: a revoked-to-nothing grant set
	// is the defect the equality assertion must report, not a broken query.
	c.appGrants = auditDownQueryStringsAllowEmpty(t, ctx, tx, "invoice_app grants on audit_log",
		`SELECT DISTINCT privilege_type FROM information_schema.role_table_grants
		   WHERE table_schema = 'public' AND table_name = 'audit_log' AND grantee = 'invoice_app'
		   ORDER BY privilege_type`)

	return c
}

// auditDownQueryStrings backs the absence assertions, so an empty result would make every
// "X is gone" assertion below pass vacuously.
func auditDownQueryStrings(t *testing.T, ctx context.Context, tx pgx.Tx, what, sql string) []string {
	t.Helper()
	out := auditDownQueryStringsAllowEmpty(t, ctx, tx, what, sql)
	if len(out) == 0 {
		t.Fatalf("%s came back empty -- the catalog query is broken, so any absence assertion on it is vacuous", what)
	}
	return out
}

func auditDownQueryStringsAllowEmpty(t *testing.T, ctx context.Context, tx pgx.Tx, what, sql string) []string {
	t.Helper()
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query %s: %v", what, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan %s: %v", what, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", what, err)
	}
	return out
}

// auditDownQueryPairs backs the absence assertions, so an empty result is a broken query.
func auditDownQueryPairs(t *testing.T, ctx context.Context, tx pgx.Tx, what string, dst map[string]string, sql string) {
	t.Helper()
	auditDownQueryPairsAllowEmpty(t, ctx, tx, what, dst, sql)
	if len(dst) == 0 {
		t.Fatalf("%s came back empty -- the catalog query is broken, so any absence assertion on it is vacuous", what)
	}
}

func auditDownQueryPairsAllowEmpty(t *testing.T, ctx context.Context, tx pgx.Tx, what string, dst map[string]string, sql string) {
	t.Helper()
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query %s: %v", what, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("scan %s: %v", what, err)
		}
		dst[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", what, err)
	}
}

// auditDownExec replays the shipped Down body on tx. An argument-less Exec goes out over
// pgx's simple protocol, which is what lets a multi-statement body travel as one call.
func auditDownExec(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	if _, err := tx.Exec(ctx, auditEntitySection(t, "Down")); err != nil {
		t.Fatalf("Down body failed (is the migration applied? run `make migrate-up`): %v", err)
	}
}

func auditDownExecUp(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	if _, err := tx.Exec(ctx, auditEntitySection(t, "Up")); err != nil {
		t.Fatalf("Up body failed: %v", err)
	}
}

// auditDownAssertUpApplied is the positive control every case opens with: without it a
// database sitting BELOW this migration would satisfy the absence assertions for free.
func auditDownAssertUpApplied(t *testing.T, c auditDownCatalog) {
	t.Helper()
	if !slices.Contains(c.columns, auditDownAddedColumn) {
		t.Fatalf("audit_log has no %s before the Down runs -- the migration is not applied, so this case would pass vacuously", auditDownAddedColumn)
	}
	for _, idx := range auditDownAddedIndexes {
		if _, ok := c.indexes[idx]; !ok {
			t.Fatalf("%s is missing before the Down runs -- the migration is not applied", idx)
		}
	}
	if _, ok := c.triggers[auditDownAddedTrigger]; !ok {
		t.Fatalf("%s is missing before the Down runs -- the migration is not applied", auditDownAddedTrigger)
	}
	for _, fn := range auditDownAddedFunctions {
		if !slices.Contains(c.functions, fn) {
			t.Fatalf("function %s is missing before the Down runs -- the migration is not applied", fn)
		}
	}
}

// --- AC-1: the Down drops the eight objects the Up added ----------------------------

func TestRLS_AuditEntityMigrationDownDropsExactlyWhatUpAdded(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	auditDownAssertUpApplied(t, auditDownReadCatalog(t, ctx, tx))
	auditDownExec(t, ctx, tx)
	after := auditDownReadCatalog(t, ctx, tx)

	if slices.Contains(after.columns, auditDownAddedColumn) {
		t.Errorf("after Down audit_log still has %s (columns: %v)", auditDownAddedColumn, after.columns)
	}
	// Positive control on the same column list: the table's own columns are untouched.
	for _, want := range auditDownKeptColumns {
		if !slices.Contains(after.columns, want) {
			t.Errorf("after Down audit_log lost pre-existing column %s (columns: %v)", want, after.columns)
		}
	}

	for _, idx := range auditDownAddedIndexes {
		if _, ok := after.indexes[idx]; ok {
			t.Errorf("after Down index %s still exists", idx)
		}
	}
	// Positive control on the same index list.
	for _, idx := range auditDownKeptIndexes {
		if _, ok := after.indexes[idx]; !ok {
			t.Errorf("after Down pre-existing index %s is gone (indexes: %v)", idx, auditDownSortedKeys(after.indexes))
		}
	}

	if _, ok := after.triggers[auditDownAddedTrigger]; ok {
		t.Errorf("after Down trigger %s still exists", auditDownAddedTrigger)
	}
	for _, tg := range auditDownKeptTriggers {
		if _, ok := after.triggers[tg]; !ok {
			t.Errorf("after Down pre-existing trigger %s is gone (triggers: %v)", tg, auditDownSortedKeys(after.triggers))
		}
	}

	// DROP TRIGGER leaves its function behind, so both functions are named in the Down.
	for _, fn := range auditDownAddedFunctions {
		if slices.Contains(after.functions, fn) {
			t.Errorf("after Down function %s still exists (functions: %v)", fn, after.functions)
		}
	}
	if !slices.Contains(after.functions, auditDownKeptFunction) {
		t.Errorf("after Down pre-existing function %s is gone (functions: %v)", auditDownKeptFunction, after.functions)
	}
}

// Re-applying Up after the Down restores all eight, so the Down is reversible rather than
// merely destructive.
func TestRLS_AuditEntityMigrationUpRestoresWhatDownDropped(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	before := auditDownReadCatalog(t, ctx, tx)
	auditDownAssertUpApplied(t, before)
	auditDownExec(t, ctx, tx)
	auditDownExecUp(t, ctx, tx)
	after := auditDownReadCatalog(t, ctx, tx)

	auditDownAssertUpApplied(t, after)
	// Set comparison, not ordered: ordinal_position is attnum, which Postgres never
	// reuses, so a re-added column lands after every burned slot, not where it started.
	afterCols, beforeCols := slices.Clone(after.columns), slices.Clone(before.columns)
	sort.Strings(afterCols)
	sort.Strings(beforeCols)
	if !slices.Equal(afterCols, beforeCols) {
		t.Errorf("columns after the Down/Up round-trip = %v, want the same set as %v", after.columns, before.columns)
	}
	if got, want := auditDownSortedKeys(after.indexes), auditDownSortedKeys(before.indexes); !slices.Equal(got, want) {
		t.Errorf("indexes after the Down/Up round-trip = %v, want %v", got, want)
	}
	if got, want := auditDownSortedKeys(after.triggers), auditDownSortedKeys(before.triggers); !slices.Equal(got, want) {
		t.Errorf("triggers after the Down/Up round-trip = %v, want %v", got, want)
	}
	if !slices.Equal(after.functions, before.functions) {
		t.Errorf("functions after the Down/Up round-trip = %v, want %v", after.functions, before.functions)
	}
}

// --- AC-2: the Down leaves every pre-existing object alone --------------------------

func TestRLS_AuditEntityMigrationDownLeavesPreExistingObjects(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	before := auditDownReadCatalog(t, ctx, tx)
	auditDownAssertUpApplied(t, before)
	auditDownExec(t, ctx, tx)
	after := auditDownReadCatalog(t, ctx, tx)

	// Negative control on the same catalog read: something DID change.
	if slices.Contains(after.columns, auditDownAddedColumn) {
		t.Fatalf("the Down left %s in place -- this case cannot tell 'left the rest alone' from 'did nothing'", auditDownAddedColumn)
	}

	if _, ok := after.indexes["audit_log_pkey"]; !ok {
		t.Errorf("after Down audit_log_pkey is gone (indexes: %v)", auditDownSortedKeys(after.indexes))
	}
	// Checked BY ITS PREDICATE: a Down that dropped and recreated this index unqualified
	// would keep the name and lose the point of it.
	pred, ok := after.indexes[auditDownKeptPartialIndex]
	switch {
	case !ok:
		t.Errorf("after Down %s is gone -- the Down over-reached into a pre-existing index (indexes: %v)",
			auditDownKeptPartialIndex, auditDownSortedKeys(after.indexes))
	case pred != auditDownKeptPartialPred:
		t.Errorf("after Down %s predicate = %q, want %q", auditDownKeptPartialIndex, pred, auditDownKeptPartialPred)
	}

	for _, tg := range auditDownKeptTriggers {
		state, ok := after.triggers[tg]
		if !ok {
			t.Errorf("after Down trigger %s is gone (triggers: %v)", tg, auditDownSortedKeys(after.triggers))
			continue
		}
		// 'O' is origin-enabled. The Up's backfill suspends audit_log_no_update_delete and
		// restores it; leaving it disabled would silently un-protect the table.
		if state != "O" {
			t.Errorf("after Down trigger %s tgenabled = %q, want %q", tg, state, "O")
		}
	}

	if !slices.Contains(after.functions, auditDownKeptFunction) {
		t.Errorf("after Down function %s is gone (functions: %v)", auditDownKeptFunction, after.functions)
	}

	qual, ok := after.policies[auditDownKeptPolicy]
	if !ok {
		t.Errorf("after Down policy %s is gone (policies: %v)", auditDownKeptPolicy, auditDownSortedKeys(after.policies))
	} else if !strings.Contains(qual, "app.current_tenant") {
		t.Errorf("after Down policy %s USING = %q, want it to still read app.current_tenant", auditDownKeptPolicy, qual)
	}

	if !after.rowSec {
		t.Errorf("after Down audit_log relrowsecurity = false, want true")
	}
	if !after.forceSec {
		t.Errorf("after Down audit_log relforcerowsecurity = false, want true")
	}

	if !slices.Equal(after.appGrants, auditDownAppGrants) {
		t.Errorf("after Down invoice_app's grants on audit_log = %v, want %v", after.appGrants, auditDownAppGrants)
	}
}

// --- the highest-stakes case: the rows survive --------------------------------------

// A Down that deleted or truncated audit_log would be permanent evidence loss on an
// append-only table. Asserted WITH DATA PRESENT: rows are seeded first, then counted
// before the Down, between Down and Up, and after Up.
func TestRLS_AuditEntityMigrationDownUpPreservesAuditRows(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	f := seedAuditEntityFixture(t)
	seedAuditEntityRow(t, f.tenant, "invoice.created", auditPayloadJSON("id", f.invoice))
	seedAuditEntityRow(t, f.tenant, "portfolio.entity.created", auditPayloadJSON("id", f.entity))
	seedAuditEntityRow(t, f.tenant, "rules.published", auditPayloadJSON("key", "ng-fmt-v4"))
	const seeded = 3

	tx := migratorTx(t, ctx)
	auditDownAssertUpApplied(t, auditDownReadCatalog(t, ctx, tx))

	before := auditEntityCountAll(t, ctx, tx, "true")
	if before < seeded {
		t.Fatalf("audit_log holds %d rows before the Down, want at least the %d just seeded -- the count is not seeing the fixture", before, seeded)
	}
	if got := auditEntityCountAll(t, ctx, tx, "tenant_id = '"+f.tenant+"'"); got != seeded {
		t.Fatalf("the fixture tenant holds %d audit rows, want %d", got, seeded)
	}

	auditDownExec(t, ctx, tx)
	if got := auditEntityCountAll(t, ctx, tx, "true"); got != before {
		t.Errorf("audit_log holds %d rows after the Down, want %d -- the Down destroyed audit evidence", got, before)
	}

	auditDownExecUp(t, ctx, tx)
	if got := auditEntityCountAll(t, ctx, tx, "true"); got != before {
		t.Errorf("audit_log holds %d rows after the Down/Up round-trip, want %d", got, before)
	}
	if got := auditEntityCountAll(t, ctx, tx, "tenant_id = '"+f.tenant+"'"); got != seeded {
		t.Errorf("the fixture tenant holds %d audit rows after the round-trip, want %d", got, seeded)
	}
}

// --- AC-3: the Down runs clean against an empty audit_log ---------------------------

// The DELETE below is only reachable by suspending the append-only trigger as the table
// owner, and the whole transaction is rolled back before it can commit.
func TestRLS_AuditEntityMigrationDownRunsCleanOnAnEmptyAuditLog(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	tx := migratorTx(t, ctx)

	auditDownAssertUpApplied(t, auditDownReadCatalog(t, ctx, tx))
	if n := auditEntityCountAll(t, ctx, tx, "true"); n == 0 {
		t.Fatalf("audit_log is already empty -- emptying it below would prove nothing")
	}

	for _, stmt := range []string{
		`ALTER TABLE audit_log NO FORCE ROW LEVEL SECURITY`,
		`ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_update_delete`,
		`DELETE FROM audit_log`,
		`ALTER TABLE audit_log ENABLE TRIGGER audit_log_no_update_delete`,
		`ALTER TABLE audit_log FORCE ROW LEVEL SECURITY`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("empty audit_log (%s): %v", stmt, err)
		}
	}
	if n := auditEntityCountAll(t, ctx, tx, "true"); n != 0 {
		t.Fatalf("audit_log still holds %d rows, want 0", n)
	}

	if _, err := tx.Exec(ctx, auditEntitySection(t, "Down")); err != nil {
		t.Errorf("Down body failed against an empty audit_log: %v", err)
	}
	after := auditDownReadCatalog(t, ctx, tx)
	if slices.Contains(after.columns, auditDownAddedColumn) {
		t.Errorf("Down reported no error against an empty audit_log but %s survives", auditDownAddedColumn)
	}
	// Close the destructive window as early as the test allows; the harness cleanup
	// rollback is a no-op after this.
	_ = tx.Rollback(ctx)
}

// --- AC-4: the file declares a real Down --------------------------------------------

// goose reports MIGRATE OK and exits 0 for a missing or statement-less `-- +goose Down`,
// so "the rollback ran" is on its own no evidence that anything rolled back.
func TestRLS_AuditEntityMigrationDeclaresARealDown(t *testing.T) {
	down := strings.TrimSpace(auditEntityStripComments(auditEntitySection(t, "Down")))
	if down == "" {
		t.Fatalf("%s: the -- +goose Down section holds only comments; goose would report MIGRATE OK and roll back nothing",
			auditEntityMigrationName(t))
	}
	if !strings.Contains(down, ";") {
		t.Fatalf("the Down section holds no terminated statement: %q", down)
	}
	// Positive control: the same splitter finds a non-empty Up, so an Up/Down mix-up
	// cannot make the check above pass on the wrong half.
	if up := strings.TrimSpace(auditEntityStripComments(auditEntitySection(t, "Up"))); up == "" {
		t.Fatalf("the Up section stripped to nothing -- the section splitter is broken")
	}

	// DROP COLUMN auto-drops only the index that depends on the column, so the other three
	// indexes, the trigger and both functions have to be named.
	wants := append([]string{auditDownAddedColumn, auditDownAddedTrigger}, auditDownAddedIndexes...)
	wants = append(wants, auditDownAddedFunctions...)
	for _, want := range wants {
		if !strings.Contains(down, want) {
			t.Errorf("the Down section never names %s", want)
		}
	}
}

// --- shared helper ------------------------------------------------------------------

func auditDownSortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
