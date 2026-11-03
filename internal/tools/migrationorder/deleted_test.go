package main

import (
	"reflect"
	"strings"
	"testing"
)

const (
	delA   = "migrations/20260101000000_a.sql"
	nameA  = "20260101000000_a.sql"
	delB   = "migrations/20260101000001_b.sql"
	nameB  = "20260101000001_b.sql"
	noRsn  = "::error file=migrations/deleted.txt::migrations/deleted.txt: the mark for " + nameA + " has no reason. Write why the deletion is safe."
	orphan = "::error file=migrations/deleted.txt::migrations/deleted.txt: the mark for %s names no deleted migration in this change. Remove it or correct the name."
)

func unmarked(path, name string) string {
	return "::error file=" + path + "::" + path + " was deleted. Production has applied every migration on main, so its applied versions and the files now disagree. Restore it, or add to migrations/deleted.txt: " + name + ": <why the deletion is safe>"
}

func orphanOf(name string) string { return strings.Replace(orphan, "%s", name, 1) }

func wantStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

func TestEvaluateDeleted_UnmarkedFailsWithExactCopy(t *testing.T) {
	f, r := EvaluateDeleted([]string{delA}, nil)
	wantStrings(t, "failures", f, []string{unmarked(delA, nameA)})
	wantStrings(t, "released", r, nil)
}

func TestEvaluateDeleted_MarkWithReasonReleases(t *testing.T) {
	f, r := EvaluateDeleted([]string{delA}, []Mark{{nameA, "never applied"}})
	wantStrings(t, "failures", f, nil)
	wantStrings(t, "released", r, []string{delA + ": never applied"})
}

func TestEvaluateDeleted_EmptyReasonFails(t *testing.T) {
	f, r := EvaluateDeleted([]string{delA}, []Mark{{nameA, ""}})
	wantStrings(t, "failures", f, []string{noRsn})
	wantStrings(t, "released", r, nil)
}

func TestEvaluateDeleted_MarkNamingNoDeletedFileIsOrphan(t *testing.T) {
	f, _ := EvaluateDeleted(nil, []Mark{{"x.sql", "why"}})
	wantStrings(t, "failures", f, []string{orphanOf("x.sql")})
}

func TestParseMarks_LineWithoutColonIsAMarkWithNoReason(t *testing.T) {
	got := ParseMarks([]string{nameA})
	if !reflect.DeepEqual(got, []Mark{{Name: nameA}}) {
		t.Fatalf("marks = %+v", got)
	}
}

func TestEvaluateDeleted_OneMarkReleasesOneFile(t *testing.T) {
	f, r := EvaluateDeleted([]string{delA, delB}, []Mark{{nameA, "r"}})
	wantStrings(t, "failures", f, []string{unmarked(delB, nameB)})
	wantStrings(t, "released", r, []string{delA + ": r"})
}

func TestEvaluateDeleted_DuplicateMarkIsOrphan(t *testing.T) {
	f, r := EvaluateDeleted([]string{delA}, []Mark{{nameA, "r"}, {nameA, "r2"}})
	wantStrings(t, "failures", f, []string{orphanOf(nameA)})
	wantStrings(t, "released", r, []string{delA + ": r"})
}

func TestEvaluateDeleted_NameMustBeExact(t *testing.T) {
	f, r := EvaluateDeleted([]string{"migrations/a.sql"}, []Mark{{"migrations/a.sql", "r"}, {"A.SQL", "r"}})
	wantStrings(t, "failures", f, []string{
		unmarked("migrations/a.sql", "a.sql"), orphanOf("migrations/a.sql"), orphanOf("A.SQL"),
	})
	wantStrings(t, "released", r, nil)
}

func TestDeletedOnBase_KeepsOnlyMigrationSQLOnBase(t *testing.T) {
	deleted := []string{"migrations/a.sql", "migrations/embed.go", "migrations/sub/x.sql", "migrations/gone.sql"}
	onBase := []string{"migrations/a.sql", "migrations/embed.go", "migrations/sub/x.sql"}
	wantStrings(t, "DeletedOnBase", DeletedOnBase(deleted, onBase), []string{"migrations/a.sql"})
}

func TestAddedLines_ReadsContentAfterTheFirstHunk(t *testing.T) {
	diff := "diff --git a/migrations/deleted.txt b/migrations/deleted.txt\nindex 1..2 100644\n--- a/migrations/deleted.txt\n+++ b/migrations/deleted.txt\n@@ -0,0 +1,2 @@\n+a.sql: r\n++odd\n"
	wantStrings(t, "AddedLines", AddedLines(diff), []string{"a.sql: r", "+odd"})
}

func TestAddedLines_EmptyDiffGivesNothing(t *testing.T) {
	if got := AddedLines(""); len(got) != 0 {
		t.Fatalf("AddedLines(\"\") = %q", got)
	}
}

func TestAddedLines_IgnoresRemovedLinesAndNoNewlineMarker(t *testing.T) {
	diff := "--- a/migrations/deleted.txt\n+++ b/migrations/deleted.txt\n@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n"
	wantStrings(t, "AddedLines", AddedLines(diff), []string{"new"})
}

func TestParseMarks_BlankAndWhitespaceOnlyLinesAreSkipped(t *testing.T) {
	if got := ParseMarks([]string{"", "   ", "\t", "\r"}); len(got) != 0 {
		t.Fatalf("marks = %+v, want none", got)
	}
}

func TestParseMarks_CRLFLineIsTrimmed(t *testing.T) {
	if got := ParseMarks([]string{"a.sql: why\r"}); !reflect.DeepEqual(got, []Mark{{"a.sql", "why"}}) {
		t.Fatalf("marks = %+v", got)
	}
}

func TestParseMarks_FirstColonSplitsSoReasonMayHoldColon(t *testing.T) {
	if got := ParseMarks([]string{"a.sql: see https://x: y"}); !reflect.DeepEqual(got, []Mark{{"a.sql", "see https://x: y"}}) {
		t.Fatalf("marks = %+v", got)
	}
}

func TestParseMarks_NameAndReasonAreTrimmed(t *testing.T) {
	if got := ParseMarks([]string{"  a.sql  :   why  "}); !reflect.DeepEqual(got, []Mark{{"a.sql", "why"}}) {
		t.Fatalf("marks = %+v", got)
	}
}

func TestEvaluateDeleted_ReasonOfSpacesOnlyFails(t *testing.T) {
	f, _ := EvaluateDeleted([]string{delA}, ParseMarks([]string{nameA + ":    "}))
	wantStrings(t, "failures", f, []string{noRsn})
}

func TestEvaluateDeleted_HashLineAndBOMNameAreOrphans(t *testing.T) {
	marks := ParseMarks([]string{"# note", "\ufeff" + nameA + ": r"})
	f, r := EvaluateDeleted([]string{delA}, marks)
	wantStrings(t, "failures", f, []string{
		unmarked(delA, nameA), orphanOf("# note"), orphanOf("\ufeff" + nameA),
	})
	wantStrings(t, "released", r, nil)
}

func TestVacated_ReturnsOldOfEveryRenameSplitDoesNotExempt(t *testing.T) {
	const x, a, y = "migrations/20260105_x.sql", "migrations/20260101_a.sql", "migrations/20260106_y.sql"
	onBase := []string{a, x, y}
	onMain := []string{a}
	changes := []Change{
		{Old: x, New: "migrations/20260210_x.sql", Exact: true}, // base-only, exact
		{Old: a, New: "migrations/20260211_a.sql", Exact: true}, // main holds it
		{Old: y, New: "migrations/20260212_y.sql"},              // base-only, inexact
		{New: "migrations/20260214_new.sql"},                    // plain add
	}
	wantStrings(t, "Vacated(exempt)", Vacated(changes, onBase, onMain, true), []string{a, y})
	wantStrings(t, "Vacated(not exempt)", Vacated(changes, onBase, onMain, false), []string{x, a, y})

	vsBase, vsMain := Split(changes, onBase, onMain, true)
	wantStrings(t, "vsBase", vsBase, []string{"migrations/20260211_a.sql", "migrations/20260212_y.sql", "migrations/20260214_new.sql"})
	wantStrings(t, "vsMain", vsMain, []string{"migrations/20260210_x.sql"})
}

func TestParseChanges_ReadsADeletedLine(t *testing.T) {
	raw := ":100644 000000 aaa 000 D\tmigrations/a.sql\n:000000 100644 000 bbb A\tmigrations/n.sql"
	cs, del, err := parseChanges(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].New != "migrations/n.sql" {
		t.Fatalf("changes = %+v", cs)
	}
	wantStrings(t, "deleted", del, []string{"migrations/a.sql"})
	if _, _, err := parseChanges(":100644 000000 aaa 000 D\tmigrations/a.sql\tmigrations/b.sql"); err == nil {
		t.Fatal("a D line with three fields must error")
	}
}
