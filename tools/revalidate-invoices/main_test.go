// main_test.go: task-412 (BUG-05-03) Stage 2.5 RED -- AC-11's 4 specs,
// near-verbatim adaptations of tools/backfill-source-rows/main_test.go's own
// TestTenantFlag_SetRejectsNonUUID / TestRun_MissingDatabaseURLFailsBefore-
// AnyConnection. Plain Go, no DB env. main() itself (os.Exit) is
// deliberately left untested, same as the other tools/ packages.
package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

func TestTenantFlag_SetRejectsNonUUID(t *testing.T) {
	var f tenantFlag
	if err := f.Set("not-a-uuid"); err == nil {
		t.Fatal("Set(non-uuid) = nil error, want a parse failure")
	}
	if len(f) != 0 {
		t.Errorf("tenantFlag = %v, want untouched after a rejected value", f)
	}
}

func TestRun_MissingDatabaseURLFailsBeforeAnyConnection(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	err := run(context.Background(), []string{"11111111-1111-1111-1111-111111111111"}, false, true, false)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run() err = %v, want a DATABASE_URL-is-required error", err)
	}
}

// TestRun_AllTenantsRequiresReaderURL: DATABASE_URL parses (pgxpool dials
// lazily, so an unreachable host never blocks this -- see the identical note
// on backfill's TestRun_MissingDocumentConfigFailsBeforeAnyQuery), but
// --all-tenants is set and DATABASE_READER_URL is unset -- run() must fail
// before ever opening the reader pool or issuing a query.
func TestRun_AllTenantsRequiresReaderURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://baduser:badpass@localhost:1/nonexistentdb?sslmode=disable")
	t.Setenv("DATABASE_READER_URL", "")

	err := run(context.Background(), nil, true, true, false)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_READER_URL") {
		t.Fatalf("run() err = %v, want a DATABASE_READER_URL-is-required error", err)
	}
}

// TestRun_MissingValidationConfigFailsBeforeAnyQuery: DATABASE_URL parses,
// --all-tenants is false (so DATABASE_READER_URL is never checked), but
// VALIDATION_URL/S2S_TOKEN are unset -- run() must fail at that config check,
// never reaching the tenant loop or issuing a query.
func TestRun_MissingValidationConfigFailsBeforeAnyQuery(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://baduser:badpass@localhost:1/nonexistentdb?sslmode=disable")
	t.Setenv("DATABASE_READER_URL", "")
	t.Setenv("VALIDATION_URL", "")
	t.Setenv("S2S_TOKEN", "")

	err := run(context.Background(), []string{"11111111-1111-1111-1111-111111111111"}, false, true, false)
	if err == nil || !strings.Contains(err.Error(), "VALIDATION_URL") {
		t.Fatalf("run() err = %v, want a VALIDATION_URL/S2S_TOKEN config error", err)
	}
}

// TestEnumerateTenants_ReturnsEveryTenant (QA gap-close): internal/invoice's
// TestRevalidateAllTenants_CoversEveryEnumeratedTenant proves the underlying
// SQL property (the tenant_enumerate RLS policy is total for
// invoice_tenant_reader) by re-running the SAME query directly against the
// reader pool -- it never calls this package's own enumerateTenants.
// Mutation-verified that gap is real: adding "LIMIT 1" to enumerateTenants's
// query here passed every one of the 14 shipped specs silently. DB-gated
// like the rest of the DB-backed suite; skips without DATABASE_READER_URL/
// DATABASE_SUPERUSER_URL rather than running unscoped against a real DB.
func TestEnumerateTenants_ReturnsEveryTenant(t *testing.T) {
	readerURL := os.Getenv("DATABASE_READER_URL")
	superURL := os.Getenv("DATABASE_SUPERUSER_URL")
	if readerURL == "" || superURL == "" {
		t.Skip("enumerateTenants db-integration test skipped: set DATABASE_READER_URL and DATABASE_SUPERUSER_URL")
	}
	ctx := context.Background()

	super, err := db.NewPool(ctx, superURL)
	if err != nil {
		t.Fatalf("connect superuser: %v", err)
	}
	t.Cleanup(super.Close)

	reader, err := db.NewPool(ctx, readerURL)
	if err != nil {
		t.Fatalf("connect reader: %v", err)
	}
	t.Cleanup(reader.Close)

	id := uuid.NewString()
	if _, err := super.Exec(ctx, `INSERT INTO tenants (id, name, kind) VALUES ($1, $2, 'firm')`, id, "enumerateTenants QA tenant"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = super.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id) })

	got, err := enumerateTenants(ctx, reader)
	if err != nil {
		t.Fatalf("enumerateTenants: %v", err)
	}

	rows, err := super.Query(ctx, `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		t.Fatalf("superuser enumerate: %v", err)
	}
	var superIDs []string
	for rows.Next() {
		var tid string
		if err := rows.Scan(&tid); err != nil {
			t.Fatalf("scan tenant id: %v", err)
		}
		superIDs = append(superIDs, tid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("superuser enumerate rows: %v", err)
	}

	if len(got) != len(superIDs) {
		t.Fatalf("enumerateTenants returned %d tenant(s), want %d (superuser SELECT id FROM tenants)", len(got), len(superIDs))
	}
	found := false
	for _, tid := range got {
		if tid == id {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("enumerateTenants did not include the freshly seeded tenant %s", id)
	}
}
