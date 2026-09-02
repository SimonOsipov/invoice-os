// lineitems_test.go: EXTR-05-02's line-item projection. External package: LineItems and
// DocLine are both exported. Cells are deliberately sparse and unsorted here, matching what
// PageReader.Read actually hands the caller -- see lineitems_adversarial_test.go for the
// out-of-order cases these fixtures don't cover.
package extraction_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// liCell builds a 1x1 cell at (row, col). region is nil for a boxless cell.
func liCell(row, col int, text string, region *extraction.Region) extraction.TableCell {
	return extraction.TableCell{Row: row, Col: col, RowSpan: 1, ColSpan: 1, Text: text, Region: region}
}

func liBox(page int, x0, y0, x1, y1 float64) *extraction.Region {
	return &extraction.Region{Page: page, X0: x0, Y0: y0, X1: x1, Y1: y1}
}

// liWant fails t unless got is non-nil and holds want.
func liWant(t *testing.T, got *string, want, field string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %q", field, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %q, want %q", field, *got, want)
	}
}

// liWantNil fails t unless got is nil.
func liWantNil(t *testing.T, got *string, field string) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %q, want nil", field, *got)
	}
}

func TestLineItems_NoTablesIsEmptyNotNil(t *testing.T) {
	pages := []extraction.Page{
		{Number: 1}, // nil Tables
		{Number: 2, Tables: []extraction.Table{}}, // non-nil, empty Tables
	}

	got := extraction.LineItems(pages)
	if got == nil {
		t.Fatal("LineItems returned nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("LineItems returned %d line(s) across pages with no tables, want 0", len(got))
	}
}

func TestLineItems_ReadsAFourColumnTable(t *testing.T) {
	tbl := extraction.Table{
		Rows: 3, Cols: 4,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil),
			liCell(0, 1, "QTY", nil),
			liCell(0, 2, " Unit Price ", nil),
			liCell(0, 3, "LINE TOTAL", nil),
			liCell(1, 0, "Widget", nil),
			liCell(1, 1, "2", nil),
			liCell(1, 2, "50.00", nil),
			liCell(1, 3, "100.00", liBox(1, 0.1, 0.5, 0.3, 0.55)),
			liCell(2, 0, "Gadget", nil),
			liCell(2, 1, "3", nil),
			liCell(2, 2, "25.00", nil),
			liCell(2, 3, "75.00", liBox(1, 0.1, 0.6, 0.3, 0.65)),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 2 {
		t.Fatalf("LineItems returned %d line(s), want 2", len(got))
	}

	if got[0].Index != 1 || got[1].Index != 2 {
		t.Errorf("Index = [%d %d], want [1 2]", got[0].Index, got[1].Index)
	}
	liWant(t, got[0].Quantity, "2", "row0 Quantity")
	liWant(t, got[0].UnitPrice, "50.00", "row0 UnitPrice")
	liWant(t, got[0].LineTotal, "100.00", "row0 LineTotal")
	liWant(t, got[1].Quantity, "3", "row1 Quantity")
	liWant(t, got[1].UnitPrice, "25.00", "row1 UnitPrice")
	liWant(t, got[1].LineTotal, "75.00", "row1 LineTotal")
}

func TestLineItems_NormalisesGroupedAmounts(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil),
			liCell(0, 1, "Rate", nil),
			liCell(0, 2, "Amount", nil),
			liCell(1, 0, "1", nil),
			liCell(1, 1, "1,000.00", nil),
			liCell(1, 2, " 1,234.56 ", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1", len(got))
	}
	liWant(t, got[0].UnitPrice, "1000.00", "UnitPrice")
	liWant(t, got[0].LineTotal, "1234.56", "LineTotal")
}

func TestLineItems_AcceptsAThreeDecimalQuantity(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Quantity", nil),
			liCell(0, 1, "Price", nil),
			liCell(0, 2, "Total", nil),
			liCell(1, 0, "12.345", nil),
			liCell(1, 1, "10.00", nil),
			liCell(1, 2, "123.45", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1", len(got))
	}
	// numeric(14,3): quantity keeps a third fraction digit that money's numeric(14,2) would reject.
	liWant(t, got[0].Quantity, "12.345", "Quantity")
}

func TestLineItems_UnrecognisedHeaderYieldsNoLines(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil),
			liCell(0, 1, "Notes", nil),
			liCell(0, 2, "Comment", nil),
			liCell(1, 0, "Widget", nil),
			liCell(1, 1, "yes", nil),
			liCell(1, 2, "fine", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 0 {
		t.Fatalf("LineItems returned %d line(s) for a header naming none of the three roles, want 0", len(got))
	}
}

func TestLineItems_HeaderOnlyTableYieldsNoLines(t *testing.T) {
	tbl := extraction.Table{
		Rows: 1, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil),
			liCell(0, 1, "Unit Price", nil),
			liCell(0, 2, "Line Total", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 0 {
		t.Fatalf("LineItems returned %d line(s) for a header-only table, want 0", len(got))
	}
}

func TestLineItems_IndexesContinuouslyAcrossPages(t *testing.T) {
	mkTable := func(total1, total2 string) extraction.Table {
		return extraction.Table{
			Rows: 3, Cols: 3,
			Cells: []extraction.TableCell{
				liCell(0, 0, "Qty", nil),
				liCell(0, 1, "Price", nil),
				liCell(0, 2, "Total", nil),
				liCell(1, 0, "1", nil), liCell(1, 1, "10.00", nil), liCell(1, 2, total1, nil),
				liCell(2, 0, "1", nil), liCell(2, 1, "20.00", nil), liCell(2, 2, total2, nil),
			},
		}
	}
	pages := []extraction.Page{
		{Number: 1, Tables: []extraction.Table{mkTable("10.00", "20.00")}},
		{Number: 2, Tables: []extraction.Table{mkTable("30.00", "40.00")}},
	}

	got := extraction.LineItems(pages)
	if len(got) != 4 {
		t.Fatalf("LineItems returned %d line(s), want 4", len(got))
	}
	wantIndex := []int{1, 2, 3, 4}
	wantTotal := []string{"10.00", "20.00", "30.00", "40.00"}
	for i, line := range got {
		if line.Index != wantIndex[i] {
			t.Errorf("line %d Index = %d, want %d", i, line.Index, wantIndex[i])
		}
		liWant(t, line.LineTotal, wantTotal[i], "line total")
	}
}

func TestLineItems_KeepsARowMissingItsQuantityCell(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil),
			liCell(0, 1, "Unit Price", nil),
			liCell(0, 2, "Line Total", nil),
			// row 1 carries no cell at col 0 at all -- not a rejected value, an absent one.
			liCell(1, 1, "15.00", nil),
			liCell(1, 2, "15.00", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1", len(got))
	}
	liWantNil(t, got[0].Quantity, "Quantity")
	liWant(t, got[0].UnitPrice, "15.00", "UnitPrice")
	liWant(t, got[0].LineTotal, "15.00", "LineTotal")
}

func TestLineItems_CarriesTheLineTotalCellRegion(t *testing.T) {
	want := extraction.Region{Page: 1, X0: 0.12, Y0: 0.34, X1: 0.56, Y1: 0.38}
	tbl := extraction.Table{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil),
			liCell(0, 1, "Price", nil),
			liCell(0, 2, "Total", nil),
			liCell(1, 0, "1", nil),
			liCell(1, 1, "9.00", nil),
			{Row: 1, Col: 2, RowSpan: 1, ColSpan: 1, Text: "9.00", Region: &want},
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1", len(got))
	}
	if got[0].Region == nil {
		t.Fatal("Region = nil, want the line-total cell's box")
	}
	if *got[0].Region != want {
		t.Errorf("Region = %+v, want %+v", *got[0].Region, want)
	}
}

// TestLineItems_DescriptionColumnIsProjected pins Core AC 1: a header naming a description
// column populates DocLine.Description on every data row.
func TestLineItems_DescriptionColumnIsProjected(t *testing.T) {
	tbl := extraction.Table{
		Rows: 3, Cols: 4,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil),
			liCell(0, 1, "Qty", nil),
			liCell(0, 2, "Rate", nil),
			liCell(0, 3, "Amount", nil),
			liCell(1, 0, "Widget", nil),
			liCell(1, 1, "2", nil),
			liCell(1, 2, "50.00", nil),
			liCell(1, 3, "100.00", nil),
			liCell(2, 0, "Gadget", nil),
			liCell(2, 1, "3", nil),
			liCell(2, 2, "25.00", nil),
			liCell(2, 3, "75.00", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 2 {
		t.Fatalf("LineItems returned %d line(s), want 2", len(got))
	}
	liWant(t, got[0].Description, "Widget", "row0 Description")
	liWant(t, got[1].Description, "Gadget", "row1 Description")
}

// TestLineItems_NoDescriptionColumnLeavesItNil pins Core AC 1's other half: a missing column
// is absence, never an empty string, and the other three roles still populate.
func TestLineItems_NoDescriptionColumnLeavesItNil(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil),
			liCell(0, 1, "Unit Price", nil),
			liCell(0, 2, "Line Total", nil),
			liCell(1, 0, "2", nil),
			liCell(1, 1, "50.00", nil),
			liCell(1, 2, "100.00", nil),
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1", len(got))
	}
	liWantNil(t, got[0].Description, "Description")
	liWant(t, got[0].Quantity, "2", "Quantity")
	liWant(t, got[0].UnitPrice, "50.00", "UnitPrice")
	liWant(t, got[0].LineTotal, "100.00", "LineTotal")
}

// TestLineItems_EachCellCarriesItsOwnRegion pins Core AC 6: description/quantity/unit_price
// each carry their own box via Regions, distinct from each other and from the line-total
// cell's Region -- not silently aliased to it, which is all the old one-Region shape allowed.
func TestLineItems_EachCellCarriesItsOwnRegion(t *testing.T) {
	boxDesc := liBox(1, 0.05, 0.50, 0.20, 0.55)
	boxQty := liBox(1, 0.22, 0.50, 0.30, 0.55)
	boxPrice := liBox(1, 0.32, 0.50, 0.45, 0.55)
	boxTotal := liBox(1, 0.47, 0.50, 0.60, 0.55)
	tbl := extraction.Table{
		Rows: 2, Cols: 4,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Description", nil),
			liCell(0, 1, "Qty", nil),
			liCell(0, 2, "Unit Price", nil),
			liCell(0, 3, "Line Total", nil),
			{Row: 1, Col: 0, RowSpan: 1, ColSpan: 1, Text: "Widget", Region: boxDesc},
			{Row: 1, Col: 1, RowSpan: 1, ColSpan: 1, Text: "2", Region: boxQty},
			{Row: 1, Col: 2, RowSpan: 1, ColSpan: 1, Text: "50.00", Region: boxPrice},
			{Row: 1, Col: 3, RowSpan: 1, ColSpan: 1, Text: "100.00", Region: boxTotal},
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1", len(got))
	}
	line := got[0]

	descR, qtyR, priceR := line.Regions[extraction.LineRoleDescription], line.Regions[extraction.LineRoleQuantity], line.Regions[extraction.LineRoleUnitPrice]
	if descR == nil || qtyR == nil || priceR == nil {
		t.Fatalf("Regions = %+v, want description/quantity/unit_price all populated", line.Regions)
	}
	if line.Region == nil {
		t.Fatal("Region = nil, want the line-total cell's box (shipped meaning unchanged)")
	}

	names := []string{"description", "quantity", "unit_price", "line_total"}
	regions := []extraction.Region{*descR, *qtyR, *priceR, *line.Region}
	wantBoxes := []*extraction.Region{boxDesc, boxQty, boxPrice, boxTotal}
	for i, r := range regions {
		if r != *wantBoxes[i] {
			t.Errorf("%s region = %+v, want %+v", names[i], r, *wantBoxes[i])
		}
	}
	for i := 0; i < len(regions); i++ {
		for j := i + 1; j < len(regions); j++ {
			if regions[i] == regions[j] {
				t.Errorf("%s and %s carry the same region %+v, want four distinct boxes", names[i], names[j], regions[i])
			}
		}
	}
}

// TestLineItemResults_NamesRowsByIndexAndRole pins the projection's naming and emit order:
// two lines yield exactly the eight line_items[1..2].<role> names, in reading order.
func TestLineItemResults_NamesRowsByIndexAndRole(t *testing.T) {
	desc1, desc2 := "Widget", "Gadget"
	lines := []extraction.DocLine{
		{Index: 1, Description: &desc1, Quantity: rcStr("2"), UnitPrice: rcStr("50.00"), LineTotal: rcStr("100.00")},
		{Index: 2, Description: &desc2, Quantity: rcStr("3"), UnitPrice: rcStr("25.00"), LineTotal: rcStr("75.00")},
	}

	got := extraction.LineItemResults(lines)
	want := []string{
		"line_items[1].description", "line_items[1].quantity", "line_items[1].unit_price", "line_items[1].line_total",
		"line_items[2].description", "line_items[2].quantity", "line_items[2].unit_price", "line_items[2].line_total",
	}
	if len(got) != len(want) {
		t.Fatalf("LineItemResults returned %d row(s) %v, want exactly %d: %v", len(got), rcNames(got), len(want), want)
	}
	if gotNames := rcNames(got); !rcNamesEqual(gotNames, want) {
		t.Errorf("row names = %v, want %v in exactly this order", gotNames, want)
	}
}

// TestLineItemResults_OmitsACellWithNoValue pins that an absent cell emits no row at all -- not
// a row carrying an empty value. The present three are asserted first, so the absence check
// below cannot pass over a set the projection never actually populated.
func TestLineItemResults_OmitsACellWithNoValue(t *testing.T) {
	desc := "Widget"
	lines := []extraction.DocLine{
		{Index: 1, Description: &desc, Quantity: rcStr("2"), UnitPrice: nil, LineTotal: rcStr("100.00")},
	}

	got := extraction.LineItemResults(lines)
	want := []string{"line_items[1].description", "line_items[1].quantity", "line_items[1].line_total"}
	if len(got) != len(want) {
		t.Fatalf("LineItemResults returned %d row(s) %v, want exactly %d: %v -- a missing cell emits no row", len(got), rcNames(got), len(want), want)
	}
	if gotNames := rcNames(got); !rcNamesEqual(gotNames, want) {
		t.Errorf("row names = %v, want %v", gotNames, want)
	}
	for _, r := range got {
		if r.Name == "line_items[1].unit_price" {
			t.Errorf("found a row named %q, want none for a missing cell", r.Name)
		}
	}
}

func TestLineItems_ABoxlessCellCarriesNoRegion(t *testing.T) {
	tbl := extraction.Table{
		Rows: 2, Cols: 3,
		Cells: []extraction.TableCell{
			liCell(0, 0, "Qty", nil),
			liCell(0, 1, "Price", nil),
			liCell(0, 2, "Total", nil),
			liCell(1, 0, "1", nil),
			liCell(1, 1, "9.00", nil),
			liCell(1, 2, "9.00", nil), // no Region: an empty cell, or any DOCX table
		},
	}
	pages := []extraction.Page{{Number: 1, Tables: []extraction.Table{tbl}}}

	got := extraction.LineItems(pages)
	if len(got) != 1 {
		t.Fatalf("LineItems returned %d line(s), want 1", len(got))
	}
	if got[0].Region != nil {
		t.Errorf("Region = %+v, want nil", *got[0].Region)
	}
}
