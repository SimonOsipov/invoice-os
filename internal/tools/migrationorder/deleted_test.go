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
	return "::error file=" + path + "::" + path + " was deleted. It is on the base branch, so deleting it breaks the Down chain and disagrees with any database that applied it. Restore it, or add to migrations/deleted.txt: " + name + ": <why the deletion is safe>"
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

func TestNewMarks_SubtractsBaseMarksAfterTrimming(t *testing.T) {
	head := []string{"b.sql: two\r", "a.sql: one", "  c.sql :  three  ", "a.sql: one", "d.sql: edited"}
	base := []string{"a.sql: one ", "c.sql: three", "d.sql: old", ""}
	got := NewMarks(head, base)
	want := []Mark{{"b.sql", "two"}, {"a.sql", "one"}, {"d.sql", "edited"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NewMarks = %+v, want %+v", got, want)
	}
}

func TestNewMarks_NoBaseKeepsEveryHeadMark(t *testing.T) {
	got := NewMarks([]string{"a.sql: r"}, nil)
	if !reflect.DeepEqual(got, []Mark{{"a.sql", "r"}}) {
		t.Fatalf("NewMarks = %+v", got)
	}
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

func TestParseChanges_ReadsNULSeparatedRecords(t *testing.T) {
	raw := ":100644 000000 aaa 000 D\x00migrations/a.sql\x00" +
		":000000 100644 000 bbb A\x00migrations/n.sql\x00" +
		":100644 100644 ccc ccc R100\x00migrations/o.sql\x00migrations/p.sql\x00" +
		":100644 000000 ddd 000 D\x00migrations/caf\u00e9\ttab.sql\x00"
	cs, del, err := parseChanges(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{New: "migrations/n.sql"}, {Old: "migrations/o.sql", New: "migrations/p.sql", Exact: true}}
	if !reflect.DeepEqual(cs, want) {
		t.Fatalf("changes = %+v, want %+v", cs, want)
	}
	wantStrings(t, "deleted", del, []string{"migrations/a.sql", "migrations/caf\u00e9\ttab.sql"})
}

func TestParseChanges_RejectsMalformedRecords(t *testing.T) {
	for _, raw := range []string{
		":100644 000000 aaa 000 D\x00",
		":100644 100644 ccc ccc R100\x00migrations/o.sql\x00",
		"migrations/a.sql\x00",
		"100644 000000 aaa 000 D\x00migrations/a.sql\x00",
	} {
		if _, _, err := parseChanges(raw); err == nil {
			t.Fatalf("parseChanges(%q) must error", raw)
		}
	}
}

func TestVacated_RenameToANonMigrationPathIsVacated(t *testing.T) {
	const x = "migrations/20260105_x.sql"
	changes := []Change{{Old: x, New: "migrations/20260105_x.sql.bak", Exact: true}}
	wantStrings(t, "Vacated", Vacated(changes, []string{x}, nil, true), []string{x})
	vsBase, vsMain := Split(changes, []string{x}, nil, true)
	wantStrings(t, "vsBase", vsBase, []string{"migrations/20260105_x.sql.bak"})
	wantStrings(t, "vsMain", vsMain, nil)
}
