// The register-export resolver replacement, executed as the migrator in a rolled-back
// transaction. The migration is function-only, so its Up replays (unlike the custom_rules definer).
package db_test

import (
	"context"
	"io/fs"
	"testing"

	"github.com/SimonOsipov/invoice-os/migrations"
)

const registerExportMigrationGlob = "*_audit_log_entity_for_register_export.sql"

func TestRLS_RegisterExportResolverReplacementIsReversible(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()

	matches, err := fs.Glob(migrations.FS, registerExportMigrationGlob)
	if err != nil {
		t.Fatalf("glob %s: %v", registerExportMigrationGlob, err)
	}
	if len(matches) != 1 {
		t.Fatalf("migrations matching %s = %v, want exactly 1", registerExportMigrationGlob, matches)
	}
	migration := matches[0]

	f := seedAuditEntityFixture(t)
	tx := migratorTx(t, ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, f.tenant); err != nil {
		t.Fatalf("set tenant context: %v", err)
	}
	resolve := func(event, key, value, stage string) *string {
		t.Helper()
		var got *string
		if err := tx.QueryRow(ctx, `SELECT audit_log_entity_for($1, $2::jsonb)::text`,
			event, auditPayloadJSON(key, value)).Scan(&got); err != nil {
			t.Fatalf("resolve %s after the %s body: %v", event, stage, err)
		}
		return got
	}
	wantEntity := func(event, key, value, stage string) {
		t.Helper()
		switch got := resolve(event, key, value, stage); {
		case got == nil:
			t.Errorf("after the %s body, %s resolves to NULL, want %s", stage, event, f.entity)
		case *got != f.entity:
			t.Errorf("after the %s body, %s resolves to %s, want %s", stage, event, *got, f.entity)
		}
	}
	// One event of every earlier arm: a dropped resolver errors, a wrong-body Down returns NULL.
	controls := func(stage string) {
		t.Helper()
		wantEntity("invoice.created", "id", f.invoice, stage)
		wantEntity("submission.failed", "invoice_id", f.invoice, stage)
		wantEntity("portfolio.entity.created", "id", f.entity, stage)
		wantEntity("extraction.field_corrected", "invoice_id", f.invoice, stage)
		wantEntity("custom_rule.created", "entity_id", f.entity, stage)
	}

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Up")); err != nil {
		t.Fatalf("Up body of %s failed: %v", migration, err)
	}
	wantEntity("invoice_register.exported", "entity_id", f.entity, "Up")
	controls("Up")

	if _, err := tx.Exec(ctx, auditEntitySectionOf(t, migration, "Down")); err != nil {
		t.Fatalf("Down body of %s failed: %v", migration, err)
	}
	if got := resolve("invoice_register.exported", "entity_id", f.entity, "Down"); got != nil {
		t.Errorf("after the Down body, invoice_register.exported resolves to %s, want NULL", *got)
	}
	controls("Down")
}
