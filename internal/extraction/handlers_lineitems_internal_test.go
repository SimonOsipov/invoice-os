// handlers_lineitems_internal_test.go: canonicalLineJSON's exact bytes and normalizeLines'
// never-nil guarantee -- both unexported, so this file stays in package extraction.
package extraction

import (
	"encoding/json"
	"os"
	"regexp"
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

// wireMirrors' goStructKeys reads a struct body with type\s+NAME\s+struct\s*\{([^{}]*)\} -- not
// brace-balanced, so one inline anonymous struct truncates the match to "" and the mirror row
// passes vacuously. This applies the same regex to the source and holds each type to its key
// count, so a nested brace or a dropped field fails here rather than silently in TypeScript.
func TestLineItemsWireTypes_HaveBraceFreeBodies(t *testing.T) {
	src, err := os.ReadFile("handlers_lineitems.go")
	if err != nil {
		t.Fatalf("read handlers_lineitems.go: %v", err)
	}
	jsonKey := regexp.MustCompile("`json:\"([^\"]+)\"`")

	for _, tc := range []struct {
		name string
		want int
	}{
		{"LineItemInput", 4},
		{"LineItemsRequest", 1},
		{"LineItemsResponse", 4},
	} {
		body := regexp.MustCompile(`type\s+` + tc.name + `\s+struct\s*\{([^{}]*)\}`).FindSubmatch(src)
		if body == nil {
			t.Errorf("goStructKeys' regex finds no body for %s -- a nested brace truncates the match and the wire mirror then compares nothing", tc.name)
			continue
		}
		if got := len(jsonKey.FindAllSubmatch(body[1], -1)); got != tc.want {
			t.Errorf("%s exposes %d json key(s) to goStructKeys, want %d", tc.name, got, tc.want)
		}
	}

	// The control: the same regex reads '' for a type whose body does carry a nested brace, so
	// the assertions above are exercising the regex's blind spot rather than always matching.
	nested := []byte("type lixNested struct {\n\tInner struct{ A string } `json:\"inner\"`\n}\n")
	if m := regexp.MustCompile(`type\s+lixNested\s+struct\s*\{([^{}]*)\}`).FindSubmatch(nested); m != nil && len(jsonKey.FindAllSubmatch(m[1], -1)) > 0 {
		t.Errorf("the control matched a nested body, so this test cannot catch what it claims to")
	}
}
