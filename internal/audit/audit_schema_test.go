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
	"sort"
	"strconv"
	"strings"
	"testing"

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
