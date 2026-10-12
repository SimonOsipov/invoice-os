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

// A fixed three-line input: one line with every cell present (line_tax included, so the
// null-for-absent rule is proved on more than the all-nil case), one with two absent cells, one
// with every cell absent.
func TestLineItemsCorrection_ValueIsCanonicalJSON(t *testing.T) {
	desc, qty, price, total, tax := "Widget", "2", "10.00", "20.00", "1.50"
	partial := "Assembly fee"
	lines := []LineItemInput{
		{Description: &desc, Quantity: &qty, UnitPrice: &price, LineTotal: &total, LineTax: &tax},
		{Description: &partial, Quantity: nil, UnitPrice: nil, LineTotal: nil, LineTax: nil},
		{Description: nil, Quantity: nil, UnitPrice: nil, LineTotal: nil, LineTax: nil},
	}
	want := `[` +
		`{"description":"Widget","quantity":"2","unit_price":"10.00","line_total":"20.00","line_tax":"1.50","tax_category":null,"hsn_code":null,"isic_code":null,"sellers_item_identification":null,"price_unit":null},` +
		`{"description":"Assembly fee","quantity":null,"unit_price":null,"line_total":null,"line_tax":null,"tax_category":null,"hsn_code":null,"isic_code":null,"sellers_item_identification":null,"price_unit":null},` +
		`{"description":null,"quantity":null,"unit_price":null,"line_total":null,"line_tax":null,"tax_category":null,"hsn_code":null,"isic_code":null,"sellers_item_identification":null,"price_unit":null}` +
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
		{"LineItemInput", 10},
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

func TestLineItemInput_CellReadsEveryLineRole(t *testing.T) {
	in := LineItemInput{}
	fields := []**string{&in.Description, &in.Quantity, &in.UnitPrice, &in.LineTotal, &in.LineTax,
		&in.TaxCategory, &in.HSNCode, &in.ISICCode, &in.SellersItemIdentification, &in.PriceUnit}
	if len(fields) != len(LineRoles) {
		t.Fatalf("test lists %d fields, LineRoles has %d", len(fields), len(LineRoles))
	}
	for i, role := range LineRoles {
		v := "v-" + role
		*fields[i] = &v
	}
	for _, role := range LineRoles {
		if got := in.cell(role); got == nil || *got != "v-"+role {
			t.Errorf("cell(%q) = %v, want %q", role, got, "v-"+role)
		}
	}
}

func TestCanonicalLineJSON_EmitsTheTenKeys(t *testing.T) {
	code := "8471.30"
	got := canonicalLineJSON([]LineItemInput{{HSNCode: &code}})
	want := `[{"description":null,"quantity":null,"unit_price":null,"line_total":null,"line_tax":null,` +
		`"tax_category":null,"hsn_code":"8471.30","isic_code":null,"sellers_item_identification":null,"price_unit":null}]`
	if got != want {
		t.Errorf("canonicalLineJSON = %s, want %s", got, want)
	}
}

func TestParseLineItemsJSON_AFiveKeyStoredSetStillParses(t *testing.T) {
	stored := `[{"description":"Widget","quantity":"2","unit_price":"10.00","line_total":"20.00","line_tax":"1.50"}]`
	lines, ok := parseLineItemsJSON(stored)
	if !ok || len(lines) != 1 {
		t.Fatalf("parseLineItemsJSON = %v, %v, want one line", lines, ok)
	}
	if lines[0].Description == nil || *lines[0].Description != "Widget" {
		t.Errorf("description = %v, want Widget", lines[0].Description)
	}
	for _, role := range LineRoles[5:] {
		if got := lines[0].cell(role); got != nil {
			t.Errorf("cell(%q) = %q, want nil on a pre-change value", role, *got)
		}
	}
}
