// main_test.go: QA adversarial coverage for the backfill-source-rows CLI
// (DOC-02-08, task-364 Stage 4) -- the tool had zero tests. Covers the
// repeatable --tenant flag.Value contract and run()'s fail-before-any-query
// config errors; main() itself (os.Exit) is deliberately left untested, same
// as the other tools/ packages.
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

func TestTenantFlag_SetAppendsValidUUIDsRepeatably(t *testing.T) {
	var f tenantFlag
	const a, b = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	if err := f.Set(a); err != nil {
		t.Fatalf("Set(%q): %v", a, err)
	}
	if err := f.Set(b); err != nil {
		t.Fatalf("Set(%q): %v", b, err)
	}
	if len(f) != 2 || f[0] != a || f[1] != b {
		t.Fatalf("tenantFlag = %v, want [%s %s] in Set order", f, a, b)
	}
	if got, want := f.String(), a+","+b; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestRun_MissingDatabaseURLFailsBeforeAnyConnection(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	err := run(context.Background(), []string{"11111111-1111-1111-1111-111111111111"}, true)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run() err = %v, want a DATABASE_URL-is-required error", err)
	}
}

// TestRun_MissingDocumentConfigFailsBeforeAnyQuery: DATABASE_URL parses (pgxpool
// dials lazily, so an unreachable host never blocks this) but the DOCUMENT_*
// vars are unset -- run() must fail at document.ConfigFromEnv, never reaching
// the tenant loop or issuing a query.
func TestRun_MissingDocumentConfigFailsBeforeAnyQuery(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://baduser:badpass@localhost:1/nonexistentdb?sslmode=disable")
	t.Setenv("DOCUMENT_BUCKET", "")
	t.Setenv("DOCUMENT_ENDPOINT", "")
	t.Setenv("DOCUMENT_REGION", "")
	t.Setenv("DOCUMENT_ACCESS_KEY_ID", "")
	t.Setenv("DOCUMENT_SECRET_ACCESS_KEY", "")

	err := run(context.Background(), []string{"11111111-1111-1111-1111-111111111111"}, true)
	if err == nil || !strings.Contains(err.Error(), "DOCUMENT_") {
		t.Fatalf("run() err = %v, want a DOCUMENT_* config error", err)
	}
}
