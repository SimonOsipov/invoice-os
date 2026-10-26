package db_test

import (
	"context"
	"io/fs"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/migrations"
)

// auditEventActorIndexesMigrationVersion is the one migration that creates audit_log_event_created_idx.
func auditEventActorIndexesMigrationVersion(t *testing.T) int64 {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("glob migrations.FS: %v", err)
	}
	for _, f := range files {
		raw, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(raw), "CREATE INDEX audit_log_event_created_idx") {
			v, err := strconv.ParseInt(strings.SplitN(f, "_", 2)[0], 10, 64)
			if err != nil {
				t.Fatalf("parse goose version out of %q: %v", f, err)
			}
			return v
		}
	}
	t.Fatal("no migration holds CREATE INDEX audit_log_event_created_idx: the event and actor index migration is not shipped")
	return 0
}

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
	version := auditEventActorIndexesMigrationVersion(t)
	newIdx := map[string]string{
		"audit_log_event_created_idx": "USING btree (event, created_at DESC, id DESC)",
		"audit_log_actor_created_idx": "USING btree (actor, created_at DESC, id DESC)",
	}
	kept := []string{
		"audit_log_created_idx", "audit_log_tenant_created_idx", "audit_log_tenant_entity_created_idx",
		"audit_log_tenant_event_created_idx", "audit_log_tenant_actor_created_idx", "audit_log_tenant_invoice_created_idx",
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
	for _, name := range kept {
		if _, ok := down[name]; !ok {
			t.Errorf("after the Down: %s is gone, the Down must drop only the two new indexes", name)
		}
	}

	reapply()
	again := auditLogIndexDefs(t)
	for name := range newIdx {
		if !reflect.DeepEqual(again[name], up[name]) {
			t.Errorf("after the Up: %s = %q, want %q", name, again[name], up[name])
		}
	}
}
