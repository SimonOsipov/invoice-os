package main

import (
	"os"
	"path/filepath"
	"testing"
)

// mainAtPR143 is the tail of migrations/ on main after PR #143 merged, the base PR #142 landed on.
var mainAtPR143 = []string{
	"migrations/20260804210704_demo_portfolio_repair.sql",
	"migrations/20260805075045_invoices_failure_kind.sql",
	"migrations/20260806184800_invoices_kept_as_is_failed.sql",
	"migrations/embed.go",
}

func mustCheck(t *testing.T, added, onBase []string) []Violation {
	t.Helper()
	got, err := Check(added, onBase)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return got
}

func TestCheck_PassesAMigrationNewerThanBase(t *testing.T) {
	got := mustCheck(t, []string{"migrations/20260807000000_later.sql"}, mainAtPR143)
	if len(got) != 0 {
		t.Fatalf("want no violation, got %+v", got)
	}
}

// The real break: PR #142 merged 20260806131239 after main already held 20260806184800.
func TestCheck_FailsPR142RuleSetV4(t *testing.T) {
	got := mustCheck(t, []string{"migrations/20260806131239_rule_set_v4.sql"}, mainAtPR143)
	if len(got) != 1 {
		t.Fatalf("want 1 violation, got %+v", got)
	}
	want := Violation{
		File:       "migrations/20260806131239_rule_set_v4.sql",
		Version:    20260806131239,
		NewestFile: "migrations/20260806184800_invoices_kept_as_is_failed.sql",
		Newest:     20260806184800,
	}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
}

// goose rejects two migrations with one version, so equal fails too.
func TestCheck_FailsAVersionEqualToBaseNewest(t *testing.T) {
	got := mustCheck(t, []string{"migrations/20260806184800_same_second.sql"}, mainAtPR143)
	if len(got) != 1 || got[0].Version != 20260806184800 {
		t.Fatalf("want the equal version flagged, got %+v", got)
	}
}

func TestCheck_MultipleAddedFlagsOnlyTheStaleOnes(t *testing.T) {
	added := []string{
		"migrations/20260901000000_after.sql",
		"migrations/20260801000000_before.sql",
		"migrations/20260902000000_after_too.sql",
		"migrations/20260806184800_equal.sql",
	}
	got := mustCheck(t, added, mainAtPR143)
	if len(got) != 2 {
		t.Fatalf("want 2 violations, got %+v", got)
	}
	if got[0].File != "migrations/20260801000000_before.sql" || got[1].File != "migrations/20260806184800_equal.sql" {
		t.Fatalf("wrong violations, got %+v", got)
	}
}

// Comparison is numeric, like goose: a string compare would rank 9_ after 10_.
func TestCheck_ComparesVersionsNumerically(t *testing.T) {
	got := mustCheck(t, []string{"migrations/9_old.sql"}, []string{"migrations/10_new.sql"})
	if len(got) != 1 {
		t.Fatalf("want 9 flagged against 10, got %+v", got)
	}
}

func TestCheck_EmptyBasePasses(t *testing.T) {
	if got := mustCheck(t, []string{"migrations/20260706153625_init.sql"}, nil); len(got) != 0 {
		t.Fatalf("want no violation, got %+v", got)
	}
}

func TestCheck_IgnoresNonSQLFiles(t *testing.T) {
	if got := mustCheck(t, []string{"migrations/embed.go"}, mainAtPR143); len(got) != 0 {
		t.Fatalf("want embed.go ignored, got %+v", got)
	}
}

func TestCheck_RejectsAnUnversionedMigration(t *testing.T) {
	if _, err := Check([]string{"migrations/rule_set_v5.sql"}, mainAtPR143); err == nil {
		t.Fatal("want an error for a migration with no numeric version")
	}
}

// Every real migration must parse, or Check would error on it; the floor catches a moved dir.
func TestVersion_ParsesEveryRealMigration(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", Dir))
	if err != nil {
		t.Fatalf("read %s: %v", Dir, err)
	}
	parsed := 0
	for _, e := range entries {
		_, ok, err := Version(e.Name())
		if err != nil {
			t.Errorf("real migration does not parse: %v", err)
		}
		if ok {
			parsed++
		}
	}
	if parsed < 50 {
		t.Fatalf("parsed %d migrations in %s, want at least 50", parsed, Dir)
	}
}
