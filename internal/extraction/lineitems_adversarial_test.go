// lineitems_adversarial_test.go: edge, boundary and negative cases lineitems_test.go leaves
// open -- unsorted cells, a rejected (not absent) cell, determinism, and purity.
package extraction_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
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

// A header naming only one of the gate's three roles still proceeds (fail-closed is
// zero-of-three only); the two unmapped roles are nil on every row, not just the first.
func TestLineItems_HeaderNamesOnlyOneRoleLeavesOthersNilOnEveryRow(t *testing.T) {
	tbl := extraction.Table{
		Rows: 3, Cols: 2,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil),
			liCell(0, 1, "Line Total", nil), // the only role the fail-closed gate counts
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

// TestLineItems_DescriptionOnlyHeaderIsStillSkipped pins that description alone does not widen
// the fail-closed gate (edge case 3): a header naming only description is prose, not a
// line-item table.
func TestLineItems_DescriptionOnlyHeaderIsStillSkipped(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 1,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil),
			liCell(1, 0, "Terms and conditions apply.", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 0 {
		t.Fatalf("LineItems returned %d line(s) for a header naming only description, want 0 -- description alone does not widen the fail-closed gate", len(got))
	}
}

// TestLineItems_BlankDescriptionCellIsAbsentNotEmpty pins edge case 1: a blank description
// cell leaves Description nil, never "" -- extraction_field_results.value's CHECK forbids an
// empty string. The other three roles populating in the same row is the positive control.
func TestLineItems_BlankDescriptionCellIsAbsentNotEmpty(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 4,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil),
			liCell(0, 1, "Qty", nil),
			liCell(0, 2, "Unit Price", nil),
			liCell(0, 3, "Line Total", nil),
			liCell(1, 0, "   ", nil), // whitespace only
			liCell(1, 1, "1", nil),
			liCell(1, 2, "10.00", nil),
			liCell(1, 3, "10.00", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1", len(got))
	}
	liWantNil(t, got[0].Description, "Description")
	liWant(t, got[0].Quantity, "1", "Quantity")
	liWant(t, got[0].UnitPrice, "10.00", "UnitPrice")
	liWant(t, got[0].LineTotal, "10.00", "LineTotal")
}

// --- purity scan --------------------------------------------------------------

// TestLineItems_PurityScanUnchanged guards the fence itself: TestLineItems_StartsNoGoroutineAndReadsNoClock
// already scans lineitems.go's actual imports, so this only pins that the allowlist has not
// quietly widened.
func TestLineItems_PurityScanUnchanged(t *testing.T) {
	want := []string{"regexp", "strconv", "strings", "unicode"}
	if !slices.Equal(liAllowedImports, want) {
		t.Errorf("liAllowedImports = %v, want %v -- lineitems.go's purity fence must not widen", liAllowedImports, want)
	}
}

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

// --- EXTR-13-01 QA: the projection's own guarantees --------------------------

// TestLineItemResults_EachRowCarriesItsOwnCellRegion closes Core AC 6 on the projection.
// TestLineItems_EachCellCarriesItsOwnRegion only reaches DocLine.Regions; nothing asserted the
// emitted FieldResult carried it, so LineItemResults could hand every row the line-total box
// and the suite stayed green.
func TestLineItemResults_EachRowCarriesItsOwnCellRegion(t *testing.T) {
	boxDesc := liBox(1, 0.05, 0.50, 0.20, 0.55)
	boxQty := liBox(1, 0.22, 0.50, 0.30, 0.55)
	boxPrice := liBox(1, 0.32, 0.50, 0.45, 0.55)
	boxTotal := liBox(1, 0.47, 0.50, 0.60, 0.55)
	tbl := extraction.Table{
		Rows: 2, Cols: 4,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil), liCell(0, 1, "Qty", nil),
			liCell(0, 2, "Unit Price", nil), liCell(0, 3, "Line Total", nil),
			liCell(1, 0, "Widget", boxDesc), liCell(1, 1, "2", boxQty),
			liCell(1, 2, "50.00", boxPrice), liCell(1, 3, "100.00", boxTotal),
		},
	}

	rows := extraction.LineItemResults(extraction.LineItems([]extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}))
	if len(rows) != 4 {
		t.Fatalf("the round trip emitted %d row(s) %v, want 4 -- the region checks below need a populated set", len(rows), rcNames(rows))
	}

	want := map[string]*extraction.Region{
		"line_items[1].description": boxDesc,
		"line_items[1].quantity":    boxQty,
		"line_items[1].unit_price":  boxPrice,
		"line_items[1].line_total":  boxTotal,
	}
	seen := make(map[extraction.Region][]string, len(rows))
	for _, r := range rows {
		w, ok := want[r.Name]
		if !ok {
			t.Errorf("unexpected row %q", r.Name)
			continue
		}
		if r.Region == nil {
			t.Errorf("%s Region = nil, want %+v", r.Name, *w)
			continue
		}
		if *r.Region != *w {
			t.Errorf("%s Region = %+v, want that cell's own box %+v", r.Name, *r.Region, *w)
		}
		seen[*r.Region] = append(seen[*r.Region], r.Name)
	}
	for box, names := range seen {
		if len(names) > 1 {
			t.Errorf("rows %v all carry region %+v, want four distinct cell boxes", names, box)
		}
	}
}

// TestLineItemResults_CopiesCellValues pins that an emitted row owns its value: a caller
// mutating the DocLine it passed in, through the very pointer it passed, must not reach the
// output.
func TestLineItemResults_CopiesCellValues(t *testing.T) {
	desc, qty, price, total := "Widget", "2", "50.00", "100.00"
	lines := []extraction.DocLine{{Index: 1, Description: &desc, Quantity: &qty, UnitPrice: &price, LineTotal: &total}}

	rows := extraction.LineItemResults(lines)
	if len(rows) != 4 {
		t.Fatalf("LineItemResults emitted %d row(s) %v, want 4", len(rows), rcNames(rows))
	}
	before := map[string]string{}
	for _, r := range rows {
		if r.Value == nil {
			t.Fatalf("%s Value = nil, want a value -- the mutation check below needs one", r.Name)
		}
		before[r.Name] = *r.Value
	}

	desc, qty, price, total = "MUTATED", "MUTATED", "MUTATED", "MUTATED"

	for _, r := range rows {
		if *r.Value != before[r.Name] {
			t.Errorf("%s Value = %q after the caller mutated its DocLine, want the copy %q", r.Name, *r.Value, before[r.Name])
		}
	}
}

// TestLineItemResults_IndexHolesDoNotRenumber pins edge case 4: a line whose every cell is
// absent emits nothing, and the surrounding lines keep their own Index -- the emitted N values
// skip rather than closing up.
func TestLineItemResults_IndexHolesDoNotRenumber(t *testing.T) {
	a, c := "Widget", "Gadget"
	rows := extraction.LineItemResults([]extraction.DocLine{
		{Index: 1, Description: &a},
		{Index: 2}, // every cell absent
		{Index: 3, Description: &c},
	})

	want := []string{"line_items[1].description", "line_items[3].description"}
	if len(rows) == 0 {
		t.Fatal("LineItemResults emitted nothing; the hole check below would be vacuous")
	}
	if got := rcNames(rows); !rcNamesEqual(got, want) {
		t.Errorf("row names = %v, want %v -- a hole must not shift or renumber its neighbours", got, want)
	}
	for _, r := range rows {
		if r.Name == "line_items[2].description" {
			t.Errorf("found %q, want none -- line 2 has no populated cell", r.Name)
		}
	}
}

// TestLineItemResults_EmptyInputIsEmptyNotNil pins the projection's zero value, and that every
// emitted row carries non-nil Alternatives (TestReconcile_AlternativesAreNeverNil's rule).
func TestLineItemResults_EmptyInputIsEmptyNotNil(t *testing.T) {
	for _, in := range [][]extraction.DocLine{nil, {}} {
		got := extraction.LineItemResults(in)
		if got == nil {
			t.Errorf("LineItemResults(%v) = nil, want an empty non-nil slice", in)
		}
		if len(got) != 0 {
			t.Errorf("LineItemResults(%v) emitted %d row(s), want 0", in, len(got))
		}
	}

	d := "Widget"
	rows := extraction.LineItemResults([]extraction.DocLine{{Index: 1, Description: &d, LineTotal: rcStr("10.00")}})
	if len(rows) != 2 {
		t.Fatalf("LineItemResults emitted %d row(s) %v, want 2 -- the Alternatives check needs a populated set", len(rows), rcNames(rows))
	}
	for _, r := range rows {
		if r.Alternatives == nil {
			t.Errorf("%s Alternatives = nil, want an empty non-nil slice", r.Name)
		}
	}
}

// TestLineFieldName_IsThePackagesOnlyNameSource pins that nothing hand-builds the
// line_items[N].<role> string beside LineFieldName. Floor: the scan reads a non-empty set of
// package files and finds LineFieldName's own literal, so a scan matching nothing cannot read
// as a pass. Needle/control per this file's own scan convention.
func TestLineFieldName_IsThePackagesOnlyNameSource(t *testing.T) {
	const needle = `"line_items["`

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	hits := map[string]int{}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if n := strings.Count(string(b), needle); n > 0 {
			hits[name] = n
		}
	}
	if scanned == 0 {
		t.Fatal("the scan read zero non-test .go files; it would report a clean package either way")
	}
	if hits["lineitems.go"] != 1 {
		t.Errorf("lineitems.go holds %d %s literal(s), want exactly 1 (LineFieldName's own); the scan's floor is broken", hits["lineitems.go"], needle)
	}
	delete(hits, "lineitems.go")
	if len(hits) != 0 {
		t.Errorf("%s hand-built outside LineFieldName in %v -- the flag row and the value rows must not drift apart", needle, hits)
	}

	if got := extraction.LineFieldName(999, extraction.LineRoleDescription); got != "line_items[999].description" {
		t.Errorf("LineFieldName(999, description) = %q, want \"line_items[999].description\"", got)
	}
}
