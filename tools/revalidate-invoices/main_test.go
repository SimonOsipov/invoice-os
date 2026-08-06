// main_test.go: task-412 (BUG-05-03) Stage 2.5 RED -- AC-11's 4 specs,
// near-verbatim adaptations of tools/backfill-source-rows/main_test.go's own
// TestTenantFlag_SetRejectsNonUUID / TestRun_MissingDatabaseURLFailsBefore-
// AnyConnection. Plain Go, no DB env. main() itself (os.Exit) is
// deliberately left untested, same as the other tools/ packages.
package main

import (
	"context"
	"strings"
	"testing"
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
