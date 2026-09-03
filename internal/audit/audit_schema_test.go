// Schema cases for the audit_log read foundations: the entity_id column, the four
// tenant-leading read indexes, and the state the migration must NOT disturb.
//
// Two idioms live here on purpose. The catalog cases need a DB and use the shared
// fixture (fx.mig); the two file cases read migrations.FS only, so they must run
// unconditionally — calling requireFixture would skip them where they could run.
package audit_test

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/migrations"
)

// storyMigrationSuffix anchors the file cases on the slug, not on a timestamp
// (goose stamps local time) and not on a content grep (invoices and import_batches
// carry their own entity_id columns).
const storyMigrationSuffix = "_audit_log_entity_id_and_read_indexes.sql"

// newestPriorMigration is the newest migration on this branch. A reverse-order
// landing froze production deploys for nine days.
const newestPriorMigration = 20260813045539

// --- file cases (no DB) ---------------------------------------------------------------

// AC-1: exactly one new migration file, sorting after the newest prior one. Falsifiable
// at 0 (never created / renamed) and at 2 (a later subtask added a second file instead
// of extending this one).
func TestAudit_SingleMigrationForThisStory(t *testing.T) {
	name := requireStoryMigration(t)

	if len(name) < 14 {
		t.Fatalf("migration name %q is shorter than a 14-digit goose timestamp", name)
	}
	stamp, err := strconv.ParseInt(name[:14], 10, 64)
	if err != nil {
		t.Fatalf("leading 14 chars of %q are not a goose timestamp: %v", name, err)
	}
	if stamp <= newestPriorMigration {
		t.Errorf("migration timestamp = %d, want strictly greater than %d — a migration that "+
			"sorts before an already-applied one never runs on a live database", stamp, newestPriorMigration)
	}
}

// AC-6: the whole migration runs in one transaction, so AUDIT-01-02's trigger-disable
// bracket is crash-safe. The positive needle stops an empty or unreadable body from
// passing this absence assertion vacuously.
func TestAudit_MigrationRunsInOneTransaction(t *testing.T) {
	name := requireStoryMigration(t)

	raw, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("read %s from migrations.FS: %v", name, err)
	}
	body := string(raw)

	if !strings.Contains(body, "-- +goose Up") {
		t.Fatalf("%s does not contain %q (%d bytes read) — the body is not a goose migration, "+
			"so the NO TRANSACTION assertion below would pass vacuously", name, "-- +goose Up", len(raw))
	}
	if strings.Contains(body, "NO TRANSACTION") {
		t.Errorf("%s declares NO TRANSACTION, want the whole migration in one transaction", name)
	}
}

// requireStoryMigration returns the single migration file for this story, failing loudly
// at any count other than one. Callers may index the name only because this asserted the
// count first.
func requireStoryMigration(t *testing.T) string {
	t.Helper()

	all, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	if len(all) == 0 {
		t.Fatalf("migrations.FS contains no *.sql files — the embed is broken")
	}

	var matches []string
	for _, name := range all {
		if strings.HasSuffix(name, storyMigrationSuffix) {
			matches = append(matches, name)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching *%s = %d %v, want exactly 1 (scanned %d files)",
			storyMigrationSuffix, len(matches), matches, len(all))
	}
	return matches[0]
}

// --- catalog cases (DB) ---------------------------------------------------------------

// AC-2: entity_id is a nullable uuid.
func TestAudit_EntityIDColumnShape(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()

	var n int
	if err := f.mig.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'entity_id'`,
	).Scan(&n); err != nil {
		t.Fatalf("count audit_log.entity_id: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log.entity_id rows in information_schema.columns = %d, want exactly 1", n)
	}

	var dataType, isNullable string
	if err := f.mig.QueryRow(ctx,
		`SELECT data_type, is_nullable FROM information_schema.columns
		   WHERE table_schema = 'public' AND table_name = 'audit_log' AND column_name = 'entity_id'`,
	).Scan(&dataType, &isNullable); err != nil {
		t.Fatalf("read audit_log.entity_id shape: %v", err)
	}
	if dataType != "uuid" {
		t.Errorf("audit_log.entity_id data_type = %q, want %q", dataType, "uuid")
	}
	if isNullable != "YES" {
		t.Errorf("audit_log.entity_id is_nullable = %q, want %q — rows predating the backfill "+
			"carry NULL", isNullable, "YES")
	}
}

// AC-2: entity_id carries no FK. An FK is incompatible with the append-only trigger under
// both referential actions, and tenant_id in this same table already has none.
//
// The invoices control proves the query can find a foreign key at all; without it a
// malformed predicate returns zero rows and this absence assertion passes vacuously.
func TestAudit_EntityIDHasNoForeignKey(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()

	const q = `SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND conrelid = $1::regclass`

	var control int
	if err := f.mig.QueryRow(ctx, q, "public.invoices").Scan(&control); err != nil {
		t.Fatalf("control: count foreign keys on invoices: %v", err)
	}
	if control == 0 {
		t.Fatalf("control: foreign keys on public.invoices = 0, want > 0 — the query cannot " +
			"detect a foreign key, so the audit_log assertion below proves nothing")
	}

	var got int
	if err := f.mig.QueryRow(ctx, q, "public.audit_log").Scan(&got); err != nil {
		t.Fatalf("count foreign keys on audit_log: %v", err)
	}
	if got != 0 {
		t.Errorf("foreign keys on public.audit_log = %d, want 0", got)
	}
}

// AC-3: the four read indexes, tenant_id leading every one so the RLS qual becomes an
// Index Cond. Compared against the canonicalised indexdef Postgres emits, not the source.
func TestAudit_ReadIndexesExistWithExactDefinitions(t *testing.T) {
	f := requireFixture(t)

	defs := auditLogIndexDefs(t, f)

	for _, c := range []struct{ name, want string }{
		{"audit_log_tenant_created_idx", "USING btree (tenant_id, created_at DESC, id DESC)"},
		{"audit_log_tenant_event_created_idx", "USING btree (tenant_id, event, created_at DESC, id DESC)"},
		{"audit_log_tenant_actor_created_idx", "USING btree (tenant_id, actor, created_at DESC, id DESC)"},
		{"audit_log_tenant_entity_created_idx", "USING btree (tenant_id, entity_id, created_at DESC, id DESC)"},
	} {
		def, ok := defs[c.name]
		if !ok {
			t.Errorf("index %s: not present in pg_indexes for audit_log (have %v)", c.name, indexNames(defs))
			continue
		}
		if !strings.Contains(def, c.want) {
			t.Errorf("index %s indexdef = %q, want it to contain %q — column order and the DESC "+
				"markers are the point", c.name, def, c.want)
		}
	}
}

// AC-3, second half: the four read indexes cover every row and are not unique. A partial
// or UNIQUE index keeps the exact column order the case above asserts, so that case alone
// passes on one — and a partial index serves almost none of the reads these four exist for.
//
// audit_log_document_created_idx is the positive control: it IS partial, so it proves the
// WHERE check can detect a predicate rather than never matching one.
func TestAudit_ReadIndexesAreTotalAndNonUnique(t *testing.T) {
	f := requireFixture(t)

	defs := auditLogIndexDefs(t, f)

	control, ok := defs["audit_log_document_created_idx"]
	if !ok || !strings.Contains(control, " WHERE ") {
		t.Fatalf("control: audit_log_document_created_idx indexdef = %q (present=%v), want a partial "+
			"index — without it the WHERE assertions below cannot detect a predicate", control, ok)
	}

	for _, name := range []string{
		"audit_log_tenant_created_idx",
		"audit_log_tenant_event_created_idx",
		"audit_log_tenant_actor_created_idx",
		"audit_log_tenant_entity_created_idx",
	} {
		def, ok := defs[name]
		if !ok {
			t.Errorf("index %s: not present in pg_indexes for audit_log (have %v)", name, indexNames(defs))
			continue
		}
		if strings.Contains(def, " WHERE ") {
			t.Errorf("index %s indexdef = %q, want no WHERE clause — a partial index skips the rows "+
				"the audit read paths filter on", name, def)
		}
		if !strings.HasPrefix(def, "CREATE INDEX ") {
			t.Errorf("index %s indexdef = %q, want it to start with %q — UNIQUE would reject "+
				"legitimate audit rows on an append-only table", name, def, "CREATE INDEX ")
		}
	}
}

// AC-5: the migration adds indexes and removes none.
func TestAudit_PreExistingIndexesSurvive(t *testing.T) {
	f := requireFixture(t)

	defs := auditLogIndexDefs(t, f)

	for _, name := range []string{"audit_log_pkey", "audit_log_document_created_idx"} {
		if _, ok := defs[name]; !ok {
			t.Errorf("index %s: gone from pg_indexes for audit_log (have %v)", name, indexNames(defs))
		}
	}

	const predicate = "WHERE (event = 'document.created'::text)"
	if def, ok := defs["audit_log_document_created_idx"]; ok && !strings.Contains(def, predicate) {
		t.Errorf("audit_log_document_created_idx indexdef = %q, want it to still contain %q — "+
			"it is partial and serves ORDER BY id ASC LIMIT 1", def, predicate)
	}
}

// AC-4: invoice_app still holds exactly SELECT and INSERT. Proving the negatives is the
// point — asserting only the two held privileges cannot catch an over-grant.
//
// has_table_privilege, not information_schema.role_table_grants: the latter shows only
// rows visible to the current role, so it cannot prove a negative.
func TestAudit_GrantsAreStillSelectInsertOnly(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()

	for _, c := range []struct {
		priv string
		want bool
	}{
		{"SELECT", true},
		{"INSERT", true},
		{"UPDATE", false},
		{"DELETE", false},
		{"TRUNCATE", false},
	} {
		var got bool
		if err := f.mig.QueryRow(ctx,
			`SELECT has_table_privilege('invoice_app', 'public.audit_log', $1)`, c.priv,
		).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege(invoice_app, audit_log, %s): %v", c.priv, err)
		}
		if got != c.want {
			t.Errorf("has_table_privilege(invoice_app, audit_log, %s) = %v, want %v — the grant "+
				"stays exactly SELECT, INSERT (append-only by grant)", c.priv, got, c.want)
		}
	}
}

// auditLogIndexDefs maps indexname -> indexdef for audit_log. Fails on an empty result so
// a broken catalog query cannot make the presence loops above pass vacuously.
func auditLogIndexDefs(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	ctx := context.Background()

	rows, err := f.mig.Query(ctx,
		`SELECT indexname, indexdef FROM pg_indexes
		   WHERE schemaname = 'public' AND tablename = 'audit_log'`)
	if err != nil {
		t.Fatalf("query pg_indexes for audit_log: %v", err)
	}
	defer rows.Close()

	defs := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan pg_indexes row: %v", err)
		}
		defs[name] = def
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pg_indexes rows: %v", err)
	}
	if len(defs) == 0 {
		t.Fatalf("pg_indexes returned no rows for audit_log — the table has at least a primary key, " +
			"so the query is wrong and every assertion over it would be vacuous")
	}
	return defs
}

func indexNames(defs map[string]string) []string {
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// --- restore-proof cases (DB) ----------------------------------------------------------

// AC-1: the backfill bracket disables audit_log_no_update_delete and must re-enable it. A
// left-disabled trigger silently ends append-only on an evidence table, and nothing else
// observes it. Asserting the expected names AND scanning the whole non-internal set means a
// trigger that vanished outright fails here too, instead of passing on an empty scan.
func TestAudit_AppendOnlyTriggersAreEnabledAfterMigration(t *testing.T) {
	f := requireFixture(t)

	states := auditLogTriggerStates(t, f)

	for _, name := range []string{
		"audit_log_no_truncate",
		"audit_log_no_update_delete",
		"audit_log_entity_on_insert",
	} {
		if _, ok := states[name]; !ok {
			t.Errorf("trigger %s: absent from pg_trigger for audit_log (have %v)",
				name, triggerNames(states))
		}
	}

	for _, name := range triggerNames(states) {
		if states[name] != "O" {
			t.Errorf("trigger %s tgenabled = %q, want %q — a disabled append-only trigger is a "+
				"silent integrity loss with no other oracle", name, states[name], "O")
		}
	}
}

// AC-2: the backfill lifts FORCE ROW LEVEL SECURITY so the size guard and the tenant list
// can survey every tenant at once. Left standing, NO FORCE means the table owner's reads
// and writes stop being tenant-scoped.
func TestAudit_ForceRowSecurityIsRestoredAfterMigration(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()

	var force, enabled bool
	if err := f.mig.QueryRow(ctx,
		`SELECT relforcerowsecurity, relrowsecurity FROM pg_class WHERE oid = 'public.audit_log'::regclass`,
	).Scan(&force, &enabled); err != nil {
		t.Fatalf("read audit_log row-security flags: %v", err)
	}
	if !enabled {
		t.Errorf("audit_log relrowsecurity = false, want true — RLS is off entirely, so FORCE means nothing")
	}
	if !force {
		t.Errorf("audit_log relforcerowsecurity = false, want true — the backfill's NO FORCE is still " +
			"standing, so the table owner bypasses tenant isolation")
	}
}

// AC-4: entity_id is a new column, and the append-only trigger must refuse a write to it
// exactly as it refuses one to any older column. The owner holds full DML privilege, so
// 42501 cannot fire — 23001 proves the trigger, not a missing grant, is what blocks it.
func TestAudit_EntityIDUpdateRefusedForOwner(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()
	tenant, event := uuid.NewString(), uuid.NewString()
	seedAudit(t, f.app, tenant, event)

	err := db.WithinTenantTx(ctx, f.mig, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE audit_log SET entity_id = $1 WHERE event = $2`,
			uuid.NewString(), event)
		return e
	})
	assertSQLState(t, err, "23001")

	if n := auditCount(t, f.app, tenant, event); n != 1 {
		t.Errorf("audit rows after the blocked owner UPDATE = %d, want 1", n)
	}
}

// AC-4: for the app role the missing UPDATE grant refuses entity_id at 42501, before RLS or
// the trigger is consulted — so no tenant context is needed.
func TestAudit_EntityIDUpdateRefusedForApp(t *testing.T) {
	f := requireFixture(t)
	ctx := context.Background()

	_, err := f.app.Exec(ctx, `UPDATE audit_log SET entity_id = $1`, uuid.NewString())
	assertSQLState(t, err, "42501")
}

// auditLogTriggerStates maps tgname -> tgenabled for audit_log's non-internal triggers.
// tgenabled is "char", which has no pgx codec, hence the ::text cast. Fails on an empty
// result so a broken catalog query cannot make the loops above pass vacuously.
func auditLogTriggerStates(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	ctx := context.Background()

	rows, err := f.mig.Query(ctx,
		`SELECT tgname, tgenabled::text FROM pg_trigger
		   WHERE tgrelid = 'public.audit_log'::regclass AND NOT tgisinternal`)
	if err != nil {
		t.Fatalf("query pg_trigger for audit_log: %v", err)
	}
	defer rows.Close()

	states := map[string]string{}
	for rows.Next() {
		var name, enabled string
		if err := rows.Scan(&name, &enabled); err != nil {
			t.Fatalf("scan pg_trigger row: %v", err)
		}
		states[name] = enabled
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pg_trigger rows: %v", err)
	}
	if len(states) == 0 {
		t.Fatalf("pg_trigger returned no non-internal rows for audit_log — the append-only triggers " +
			"are gone or the query is wrong, and every assertion over it would be vacuous")
	}
	return states
}

func triggerNames(states map[string]string) []string {
	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// --- AUDIT-11-05 Core AC 8 (no DB, no git) ---------------------------------------------

// auditNumberMigrationCount and auditNumberNewestMigration pin the migration inventory. A
// migration added and not accounted for here raises the count and, because goose stamps
// ascending and TestAudit_SingleMigrationForThisStory enforces that rule, sorts after the
// newest name -- so either half fires. A later story that legitimately adds one moves both
// pins.
//
// CF-31: the obvious oracle, a diff of migrations/ against main, CANNOT run in CI. Every job
// checks out at fetch-depth 1, where `git diff main...HEAD` exits 128 with "ambiguous argument".
// This is the shallow-safe form, and it is the idiom requireStoryMigration above already uses.
const (
	auditNumberMigrationCount  = 56
	auditNumberNewestMigration = "20260902235137_extraction_learned_rule_writer.sql"
)

// auditReaderFiles is internal/audit's whole non-test surface. AUDIT-11 touches exactly one of
// them, AUDIT-11-09's filter.go, and adds none.
var auditReaderFiles = []string{
	"audit.go", "facets.go", "filter.go", "handlers.go", "reader.go", "store.go",
}

// auditReaderImports is that surface's import set, measured on this branch. It is the half a
// file count cannot see: an existing file widened to reach a new dependency.
var auditReaderImports = []string{
	"context",
	"encoding/base64",
	"encoding/json",
	"errors",
	"fmt",
	"github.com/SimonOsipov/invoice-os/internal/actor",
	"github.com/SimonOsipov/invoice-os/internal/platform/auth",
	"github.com/SimonOsipov/invoice-os/internal/platform/db",
	"github.com/google/uuid",
	"github.com/jackc/pgx/v5",
	"github.com/jackc/pgx/v5/pgxpool",
	"log/slog",
	"net/http",
	"net/url",
	"strconv",
	"strings",
	"time",
}

// The migration inventory is pinned by count and by newest name. Nothing else owns that
// claim -- the append-only enforcement is green but no test says an unrelated story left
// migrations/ alone. A story that adds one moves the two constants above deliberately.
func TestAuditNumber_MigrationInventoryIsPinned(t *testing.T) {
	all, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	if len(all) == 0 {
		t.Fatalf("migrations.FS contains no *.sql files -- the embed is broken, so both assertions below would pass vacuously")
	}
	if len(all) != auditNumberMigrationCount {
		t.Errorf("migrations.FS holds %d *.sql files, want %d -- move auditNumberMigrationCount only when a story deliberately adds one", len(all), auditNumberMigrationCount)
	}
	sorted := append([]string(nil), all...)
	sort.Strings(sorted)
	if got := sorted[len(sorted)-1]; got != auditNumberNewestMigration {
		t.Errorf("the newest migration is %q, want %q -- goose stamps ascending, so an unaccounted migration sorts after it", got, auditNumberNewestMigration)
	}
}

// Core AC 8: no file under internal/audit/ other than tests and AUDIT-11-09's filter.go moves.
// The file list catches a new file; the import set catches an existing file widened to reach
// past the fence. Both read the package itself, so neither needs a main ref (CF-31).
func TestAuditNumber_ReaderSurfaceIsStillSixFiles(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	if len(files) == 0 {
		t.Fatalf("internal/audit holds no non-test .go files -- the scan is wrong, so the comparison below would pass vacuously")
	}
	sort.Strings(files)
	if strings.Join(files, ",") != strings.Join(auditReaderFiles, ",") {
		t.Errorf("internal/audit non-test files = [%s], want [%s] -- AUDIT-11 adds none and removes none",
			strings.Join(files, ","), strings.Join(auditReaderFiles, ","))
	}

	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Fatalf("go list the audit package imports: %v", err)
	}
	var imports []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			imports = append(imports, line)
		}
	}
	if len(imports) == 0 {
		t.Fatalf("go list reported no imports for internal/audit -- the comparison below would pass vacuously")
	}
	sort.Strings(imports)
	want := append([]string(nil), auditReaderImports...)
	sort.Strings(want)
	if strings.Join(imports, ",") != strings.Join(want, ",") {
		t.Errorf("internal/audit imports = [%s], want [%s] -- AUDIT-11-09's filter.go edits reach for nothing new, and this story adds no import",
			strings.Join(imports, ","), strings.Join(want, ","))
	}
}
