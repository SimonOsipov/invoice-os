// handlers_lineitems_internal_test.go: canonicalLineJSON's exact bytes and normalizeLines'
// never-nil guarantee -- both unexported, so this file stays in package extraction.
package extraction

import (
	"encoding/json"
	"strings"
	"testing"
)

// A fixed three-line input: one line with every cell present, one with two absent cells, one
// with every cell absent -- so the null-for-absent rule is proved on more than the all-nil case.
func TestLineItemsCorrection_ValueIsCanonicalJSON(t *testing.T) {
	desc, qty, price, total := "Widget", "2", "10.00", "20.00"
	partial := "Assembly fee"
	lines := []LineItemInput{
		{Description: &desc, Quantity: &qty, UnitPrice: &price, LineTotal: &total},
		{Description: &partial, Quantity: nil, UnitPrice: nil, LineTotal: nil},
		{Description: nil, Quantity: nil, UnitPrice: nil, LineTotal: nil},
	}
	want := `[` +
		`{"description":"Widget","quantity":"2","unit_price":"10.00","line_total":"20.00"},` +
		`{"description":"Assembly fee","quantity":null,"unit_price":null,"line_total":null},` +
		`{"description":null,"quantity":null,"unit_price":null,"line_total":null}` +
		`]`

	if got := canonicalLineJSON(lines); got != want {
		t.Errorf("canonicalLineJSON(...) = %q, want %q", got, want)
	}

	// An empty set still clears the value CHECK (char_length(value) > 0): both a nil slice and
	// a present-but-empty one collapse to the two-character array.
	if got := canonicalLineJSON(nil); got != "[]" {
		t.Errorf("canonicalLineJSON(nil) = %q, want %q", got, "[]")
	}
	if got := canonicalLineJSON([]LineItemInput{}); got != "[]" {
		t.Errorf("canonicalLineJSON([]LineItemInput{}) = %q, want %q", got, "[]")
	}
}

// The M4-16 gate's trap, again: a nil []LineItemInput marshals to null, not []. A 201 body must
// read as an array even for an invoice left with no lines.
func TestLineItemsResponse_EmptySetIsAnArrayNotNull(t *testing.T) {
	resp := LineItemsResponse{ID: "job-1", InvoiceID: "inv-1", Lines: normalizeLines(nil)}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"lines":null`) {
		t.Errorf("the response carries %s, want \"lines\":[] -- a nil slice marshals as null", b)
	}
	if !strings.Contains(string(b), `"lines":[]`) {
		t.Errorf("the response carries %s, want it to contain \"lines\":[]", b)
	}

	// The control: a populated set is untouched by normalizeLines, so the assertions above are
	// exercising the empty-set case specifically, not stripping every line.
	desc := "Widget"
	populated := normalizeLines([]LineItemInput{{Description: &desc}})
	if len(populated) != 1 || populated[0].Description != &desc {
		t.Errorf("normalizeLines dropped or replaced a populated set: got %+v", populated)
	}
}
