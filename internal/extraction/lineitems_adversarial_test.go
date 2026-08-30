// lineitems_adversarial_test.go: edge, boundary and negative cases lineitems_test.go leaves
// open -- unsorted cells, a rejected (not absent) cell, determinism, and purity.
package extraction_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

func TestLineItems_IndexesContinuouslyAcrossTwoTablesOnOnePage(t *testing.T) {
	mk := func(total1, total2 string) extraction.Table {
		return extraction.Table{
			Rows: 3, Cols: 3,
			Cells: []extraction.TableCell{
				liCell(0, 0, "Qty", nil), liCell(0, 1, "Price", nil), liCell(0, 2, "Total", nil),
				liCell(1, 0, "1", nil), liCell(1, 1, "5.00", nil), liCell(1, 2, total1, nil),
				liCell(2, 0, "1", nil), liCell(2, 1, "6.00", nil), liCell(2, 2, total2, nil),
			},
		}
	}
	pages := []extraction.Page{{
		Number: 1,
		Tables: []extraction.Table{mk("1.00", "2.00"), mk("3.00", "4.00")},
	}}

	got := extraction.LineItems(pages)
	if len(got) != 4 {
		t.Fatalf("LineItems returned %d line(s), want 4", len(got))
	}
	wantTotal := []string{"1.00", "2.00", "3.00", "4.00"}
	for i, line := range got {
		if line.Index != i+1 {
			t.Errorf("line %d Index = %d, want %d", i, line.Index, i+1)
		}
		liWant(t, line.LineTotal, wantTotal[i], "line total")
	}
}

func TestLineItems_DropsACellWholeShapeRejects(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil), liCell(0, 1, "Price", nil), liCell(0, 2, "Total", nil),
			liCell(1, 0, "n/a", nil), // present, but the shape rejects the text
			liCell(1, 1, "8.00", nil),
			liCell(1, 2, "8.00", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1 -- a rejected cell drops the field, not the row", len(got))
	}
	liWantNil(t, got[0].Quantity, "Quantity")
	liWant(t, got[0].UnitPrice, "8.00", "UnitPrice")
	liWant(t, got[0].LineTotal, "8.00", "LineTotal")
}

// The line-total cell's box is positional provenance, independent of whether its text parses:
// a reviewer still needs to see where the rejected value came from.
func TestLineItems_RegionSurvivesALineTotalShapeRejection(t *testing.T) {
	box := liBox(1, 0.1, 0.5, 0.3, 0.55)
	tbl := extraction.Table{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil), liCell(0, 1, "Price", nil), liCell(0, 2, "Total", nil),
			liCell(1, 0, "1", nil),
			liCell(1, 1, "8.00", nil),
			{Row: 1, Col: 2, RowSpan: 1, ColSpan: 1, Text: "n/a", Region: box}, // boxed, but the shape rejects the text
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1", len(got))
	}
	liWantNil(t, got[0].LineTotal, "LineTotal")
	if got[0].Region == nil {
		t.Fatal("Region = nil, want the rejected cell's box (Region tracks the box, not value validity)")
	}
	if *got[0].Region != *box {
		t.Errorf("Region = %+v, want %+v", *got[0].Region, *box)
	}
}

func TestLineItems_IsDeterministic(t *testing.T) {
	tbl := extraction.Table{
		Rows: 3, Cols: 4,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil), liCell(0, 1, "Qty", nil), liCell(0, 2, "Unit Price", nil), liCell(0, 3, "Line Total", nil),
			liCell(1, 0, "Widget", nil), liCell(1, 1, "2", nil), liCell(1, 2, "50.00", nil), liCell(1, 3, "100.00", liBox(1, 0.1, 0.5, 0.3, 0.55)),
			liCell(2, 1, "3", nil), liCell(2, 2, "n/a", nil), liCell(2, 3, "75.00", nil),
		},
	}
	pages := []extraction.Page{
		{Number: 1, Tables: []extraction.Table{tbl}},
		{Number: 2, Tables: []extraction.Table{tbl}},
	}

	first := extraction.LineItems(pages)
	if len(first) == 0 {
		t.Fatal("LineItems returned 0 lines for a non-empty fixture; the determinism check below would be vacuous")
	}

	for i := 0; i < 200; i++ {
		got := extraction.LineItems(pages)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs from the first run:\nfirst: %+v\ngot:   %+v", i, first, got)
		}
	}
}

// Cells arrive sparse and unsorted (pagereader.go's own guarantee): row 2 is listed before row
// 1, and row 1's own cells are listed out of column order.
func TestLineItems_OutOfOrderCellsStillYieldAscendingRows(t *testing.T) {
	tbl := extraction.Table{
		Rows: 3, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil), liCell(0, 1, "Price", nil), liCell(0, 2, "Total", nil),
			liCell(2, 2, "20.00", nil), liCell(2, 1, "6.00", nil), liCell(2, 0, "2", nil),
			liCell(1, 2, "10.00", nil), liCell(1, 0, "1", nil), liCell(1, 1, "5.00", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 2 {
		t.Fatalf("LineItems returned %d line(s), want 2", len(got))
	}
	liWant(t, got[0].LineTotal, "10.00", "line 0 total (table row 1)")
	liWant(t, got[1].LineTotal, "20.00", "line 1 total (table row 2)")
	if got[0].Index != 1 || got[1].Index != 2 {
		t.Errorf("Index = [%d %d], want [1 2]", got[0].Index, got[1].Index)
	}
}

// The exact-match lexicon (liLexicon) exists to reject a header like "Total Weight" that only
// contains a role word as a substring; a substring-matching regression would misclassify it.
func TestLineItems_SubstringHeaderIsNotAFalsePositive(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 1,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Total Weight", nil), // contains "total" as substring, is not the line total
			liCell(1, 0, "12", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 0 {
		t.Fatalf("LineItems returned %d line(s) for a header that only substring-contains a role name, want 0", len(got))
	}
}

// Go's own sort.Ints uses insertion sort below n=12; liSortInts is hand-rolled and has no such
// threshold, but a fixture at or below 12 rows can't tell a correct sort from a lucky one that
// only works in that range. 20 rows, supplied in a scrambled (non-monotonic, non-reversed)
// order, forces liSortInts through swaps a small or already-mostly-sorted fixture would not.
func TestLineItems_SortsMoreThanTwelveOutOfOrderRows(t *testing.T) {
	const n = 20
	order := []int{13, 2, 19, 7, 1, 16, 4, 11, 20, 8, 5, 17, 3, 14, 9, 18, 6, 12, 10, 15}
	if len(order) != n {
		t.Fatalf("test setup: order has %d entries, want %d", len(order), n)
	}
	seen := make(map[int]bool, n)
	for _, r := range order {
		if r < 1 || r > n || seen[r] {
			t.Fatalf("test setup: order is not a permutation of 1..%d", n)
		}
		seen[r] = true
	}

	cells := []extraction.TableCell{liCell(0, 0, "Qty", nil), liCell(0, 1, "Total", nil)}
	wantTotal := make([]string, n)
	for _, row := range order {
		total := strconv.Itoa(row) + ".00"
		cells = append(cells, liCell(row, 0, "1", nil), liCell(row, 1, total, nil))
		wantTotal[row-1] = total
	}
	tbl := extraction.Table{Rows: n + 1, Cols: 2, Cells: cells}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != n {
		t.Fatalf("LineItems returned %d line(s), want %d", len(got), n)
	}
	for i, line := range got {
		if line.Index != i+1 {
			t.Errorf("line %d Index = %d, want %d", i, line.Index, i+1)
		}
		liWant(t, line.LineTotal, wantTotal[i], "line total")
	}
}

// A header naming only one of the three roles still proceeds (fail-closed is zero-of-three
// only); the two unmapped roles are nil on every row, not just the first.
func TestLineItems_HeaderNamesOnlyOneRoleLeavesOthersNilOnEveryRow(t *testing.T) {
	tbl := extraction.Table{
		Rows: 3, Cols: 2,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil),
			liCell(0, 1, "Line Total", nil), // the only recognised role
			liCell(1, 0, "Widget", nil), liCell(1, 1, "10.00", nil),
			liCell(2, 0, "Gadget", nil), liCell(2, 1, "20.00", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 2 {
		t.Fatalf("LineItems returned %d line(s), want 2 -- naming one of three roles still proceeds", len(got))
	}
	for i, line := range got {
		liWantNil(t, line.Quantity, "Quantity")
		liWantNil(t, line.UnitPrice, "UnitPrice")
		if line.LineTotal == nil {
			t.Errorf("line %d LineTotal = nil, want a value", i)
		}
	}
	liWant(t, got[0].LineTotal, "10.00", "line 0 LineTotal")
	liWant(t, got[1].LineTotal, "20.00", "line 1 LineTotal")
}

// --- purity scan --------------------------------------------------------------

// liParse parses one source. src nil reads the named file; a string is a needle/control.
func liParse(t *testing.T, name string, src any) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

func liImportPaths(f *ast.File) []string {
	out := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			p = imp.Path.Value
		}
		out = append(out, p)
	}
	return out
}

func liConcurrency(f *ast.File) []string {
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.GoStmt:
			hits = append(hits, "go statement")
		case *ast.SelectStmt:
			hits = append(hits, "select statement")
		case *ast.ChanType:
			hits = append(hits, "channel type")
		}
		return true
	})
	return hits
}

// liAllowedImports: LineItems normalises table-cell text, so string/number packages are fine,
// but no clock, no network, no database. A sibling of resolve_internal_test.go's
// rvAllowedImports -- lineitems.go is deliberately not added to rvPureFiles, it gets its own
// scan over its own file.
var liAllowedImports = []string{"regexp", "strconv", "strings", "unicode"}

func TestLineItems_StartsNoGoroutineAndReadsNoClock(t *testing.T) {
	f := liParse(t, "lineitems.go", nil)
	if len(f.Decls) == 0 {
		t.Fatal("lineitems.go declares nothing; the scan below reported all-clear over an empty AST")
	}

	for _, p := range liImportPaths(f) {
		if !slices.Contains(liAllowedImports, p) {
			t.Errorf("lineitems.go imports %q; LineItems is pure and takes no clock, no network and no database", p)
		}
	}
	if hits := liConcurrency(f); len(hits) != 0 {
		t.Errorf("lineitems.go carries %v; LineItems is pure and starts nothing", hits)
	}

	// Needle/control: a banned import.
	const importNeedle = `package p

import "time"

var _ = time.Now
`
	const importControl = `package p

import "strings"

var _ = strings.TrimSpace
`
	gotNeedle := liImportPaths(liParse(t, "needle.go", importNeedle))
	if len(gotNeedle) == 0 {
		t.Fatal(`the needle source imports "time" and liImportPaths found no import at all`)
	}
	banned := false
	for _, p := range gotNeedle {
		if !slices.Contains(liAllowedImports, p) {
			banned = true
		}
	}
	if !banned {
		t.Errorf("the allowlist accepted the needle's imports %v; the clean result above proves nothing", gotNeedle)
	}
	for _, p := range liImportPaths(liParse(t, "control.go", importControl)) {
		if !slices.Contains(liAllowedImports, p) {
			t.Errorf("the control source only imports %q, an allowed package, and the scan flagged it; the scan is not specific", p)
		}
	}

	// Needle/control: a goroutine.
	const goNeedle = `package p

func g() {}

func f() {
	go g()
}
`
	const goControl = `package p

func g() {}

func f() {
	g()
}
`
	if hits := liConcurrency(liParse(t, "needle.go", goNeedle)); len(hits) == 0 {
		t.Error("the needle source starts a goroutine and the scan did not report it; the all-clear above proves nothing")
	}
	if hits := liConcurrency(liParse(t, "control.go", goControl)); len(hits) != 0 {
		t.Errorf("the control source only calls a function and the scan reported %v; the scan is not specific", hits)
	}
}
