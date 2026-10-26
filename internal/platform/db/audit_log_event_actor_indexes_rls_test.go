package db_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/SimonOsipov/invoice-os/migrations"
)

func auditLogIndexDefs(t *testing.T) map[string]string {
	t.Helper()
	rows, err := h.super.Query(context.Background(),
		`SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'audit_log'`)
	if err != nil {
		t.Fatalf("read pg_indexes for audit_log: %v", err)
	}
	defer rows.Close()
	defs := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan pg_indexes: %v", err)
		}
		defs[name] = def
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	return defs
}

func TestRLS_AuditLogEventAndActorIndexesDownAndUp(t *testing.T) {
	requireHarness(t)
	version := migrationVersion(t, "*_audit_log_event_and_actor_created_indexes.sql")
	newIdx := map[string]string{
		"audit_log_event_created_idx": "USING btree (event, created_at DESC, id DESC)",
		"audit_log_actor_created_idx": "USING btree (actor, created_at DESC, id DESC)",
	}

	up := auditLogIndexDefs(t)
	for name, want := range newIdx {
		def, ok := up[name]
		if !ok {
			t.Errorf("before the Down: %s is missing", name)
			continue
		}
		if !strings.Contains(def, want) || !strings.HasPrefix(def, "CREATE INDEX ") || strings.Contains(def, " WHERE ") {
			t.Errorf("before the Down: %s = %q, want a total non-unique %s", name, def, want)
		}
	}

	reapply := rollBackMigrations(t, staffMigrationProvider(t), version)
	down := auditLogIndexDefs(t)
	for name := range newIdx {
		if _, ok := down[name]; ok {
			t.Errorf("after the Down: %s still exists", name)
		}
	}
	for name := range up {
		if _, isNew := newIdx[name]; isNew {
			continue
		}
		if _, ok := down[name]; !ok {
			t.Errorf("after the Down: %s is gone, the Down must drop only the two new indexes", name)
		}
	}
	if len(down) != len(up)-len(newIdx) {
		t.Errorf("after the Down: %d indexes, want %d (the Up set minus the two new ones)", len(down), len(up)-len(newIdx))
	}

	reapply()
	again := auditLogIndexDefs(t)
	for name := range newIdx {
		if again[name] != up[name] {
			t.Errorf("after the Up: %s = %q, want %q", name, again[name], up[name])
		}
	}
}

func TestRLS_AuditLogEventAndActorIndexesRefuseAboveTheRowCeiling(t *testing.T) {
	requireHarness(t)
	ctx := context.Background()
	superDSN, migDSN := migratedScratchDB(t, migrations.FS)
	version := migrationVersion(t, "*_audit_log_event_and_actor_created_indexes.sql")

	sqlDB, err := sql.Open("pgx", migDSN)
	if err != nil {
		t.Fatalf("open scratch migrator connection: %v", err)
	}
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		t.Fatalf("build migration provider: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
		t.Fatalf("roll back migration %d: %v", version, err)
	}

	super, err := sql.Open("pgx", superDSN)
	if err != nil {
		t.Fatalf("open scratch superuser connection: %v", err)
	}
	defer super.Close()
	if _, err := super.ExecContext(ctx,
		`INSERT INTO audit_log (tenant_id, actor, event, payload)
		 SELECT gen_random_uuid(), 'ceiling', 'ceiling.seed', '{}'::jsonb FROM generate_series(1, 500001)`); err != nil {
		t.Fatalf("seed 500001 audit rows: %v", err)
	}

	_, err = provider.ApplyVersion(ctx, version, true)
	if err == nil || !strings.Contains(err.Error(), "500000 in-migration ceiling") {
		t.Fatalf("Up above the ceiling = %v, want the in-migration ceiling refusal", err)
	}
	var n int
	if err := super.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_indexes WHERE tablename = 'audit_log' AND indexname IN ('audit_log_event_created_idx', 'audit_log_actor_created_idx')`).Scan(&n); err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	if n != 0 {
		t.Errorf("%d new indexes exist after the refused Up, want 0", n)
	}
}
