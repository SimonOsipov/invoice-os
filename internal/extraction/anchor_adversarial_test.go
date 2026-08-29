// anchor_adversarial_test.go: edge, boundary and negative cases beyond A-01..A-10.
package extraction

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"math"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
)

// ruleBody builds a rule body with label as a JSON string, so a pattern full of backslashes
// needs no hand-escaping.
func ruleBody(t *testing.T, label, kind string, dist float64, shape string) []byte {
	t.Helper()

	quoted, err := json.Marshal(label)
	if err != nil {
		t.Fatalf("marshal label: %v", err)
	}
	return []byte(fmt.Sprintf(`{"label":%s,"relation":{"kind":%q,"max_distance":%v},"shape":%q}`,
		quoted, kind, dist, shape))
}

// The cap must fire BEFORE regexp.Compile. A-05's label compiles, so moving the check after
// the compile leaves A-05 green; only a label that is both over-cap AND uncompilable tells
// the two branches apart.
func TestParseRule_CapFiresBeforeCompile(t *testing.T) {
	label := strings.Repeat("a", 600) + "("
	if _, err := regexp.Compile(label); err == nil {
		t.Fatalf("test fixture invalid: %q must be uncompilable", label)
	}

	_, err := ParseRule(ruleBody(t, label, "same_token", 0, "invoice_number"))
	if err == nil {
		t.Fatal("ParseRule() error = nil, want an over-cap error")
	}
	if !strings.Contains(err.Error(), "over the 512-byte cap") {
		t.Errorf("ParseRule() error = %q, want the cap error; the cap must precede regexp.Compile", err.Error())
	}
	var synErr *syntax.Error
	if errors.As(err, &synErr) {
		t.Errorf("ParseRule() error = %q wraps syntax.Error: regexp.Compile ran on an over-cap label", err.Error())
	}
}

// The cap is a cost bound, not a ReDoS defence: RE2 compiles a pathological alternation
// happily, so the cap is the only thing that turns it away.
func TestParseRule_CapStopsAPathologicalAlternation(t *testing.T) {
	var alts []string
	for len(strings.Join(alts, "|")) <= maxRuleLabelBytes {
		alts = append(alts, strings.Repeat("a", len(alts)+1))
	}
	label := "(" + strings.Join(alts, "|") + ")"
	if len(label) <= maxRuleLabelBytes {
		t.Fatalf("test fixture invalid: label is %d bytes, want over %d", len(label), maxRuleLabelBytes)
	}
	if _, err := regexp.Compile(label); err != nil {
		t.Fatalf("test fixture invalid: RE2 must accept this label, got %v", err)
	}

	_, err := ParseRule(ruleBody(t, label, "right", 0.2, "amount"))
	if err == nil {
		t.Fatal("ParseRule() error = nil, want the cap to reject a 512+ byte alternation")
	}
	if !strings.Contains(err.Error(), "over the 512-byte cap") {
		t.Errorf("ParseRule() error = %q, want the cap error", err.Error())
	}
}

// The cap counts BYTES, not runes: 512 is accepted, 513 is not, and 171 three-byte runes
// (513 bytes, 171 runes) is not.
func TestParseRule_LabelLengthBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		label   string
		wantErr bool
	}{
		{"512 ASCII bytes", strings.Repeat("a", 512), false},
		{"513 ASCII bytes", strings.Repeat("a", 513), true},
		{"171 three-byte runes, 513 bytes", strings.Repeat("€", 171), true},
		{"170 three-byte runes, 510 bytes", strings.Repeat("€", 170), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(tc.label); (got > maxRuleLabelBytes) != tc.wantErr {
				t.Fatalf("test fixture invalid: label is %d bytes, wantErr %v", got, tc.wantErr)
			}

			r, err := ParseRule(ruleBody(t, tc.label, "same_token", 0, "invoice_number"))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseRule() error = nil, want an over-cap error for %d bytes", len(tc.label))
				}
				if !strings.Contains(err.Error(), "over the 512-byte cap") {
					t.Errorf("ParseRule() error = %q, want the cap error", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRule() error = %v, want nil at %d bytes", err, len(tc.label))
			}
			if r.re == nil {
				t.Error("ParseRule() left re nil on an accepted label")
			}
		})
	}
}

// [0,1] is inclusive at both ends; the smallest float outside either end is rejected.
func TestParseRule_MaxDistanceBoundsAreInclusive(t *testing.T) {
	for _, tc := range []struct {
		dist    float64
		wantErr bool
	}{
		{0, false},
		{1, false},
		{math.Nextafter(1, 2), true},
		{math.Nextafter(0, -1), true},
	} {
		t.Run(fmt.Sprint(tc.dist), func(t *testing.T) {
			r, err := ParseRule(ruleBody(t, "x", "below", tc.dist, "amount"))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("max_distance=%v: ParseRule() error = nil, want an outside-[0,1] error", tc.dist)
				}
				if !strings.Contains(err.Error(), "outside [0,1]") {
					t.Errorf("max_distance=%v: ParseRule() error = %q, want it to name the range", tc.dist, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("max_distance=%v: ParseRule() error = %v, want nil: the range is inclusive", tc.dist, err)
			}
			if r.Relation.MaxDistance != tc.dist {
				t.Errorf("Relation.MaxDistance = %v, want %v unchanged", r.Relation.MaxDistance, tc.dist)
			}
		})
	}
}

// Unknown keys are ignored, not rejected: EXTR-14 generates rule bodies and a forward-
// compatible key must not become an error. No json.Decoder.DisallowUnknownFields.
func TestParseRule_IgnoresUnknownKeys(t *testing.T) {
	const body = `{"label":"x","relation":{"kind":"right","max_distance":0.5,"units":"px"},` +
		`"shape":"amount","confidence":0.9,"re":"injected"}`

	r, err := ParseRule([]byte(body))
	if err != nil {
		t.Fatalf("ParseRule() error = %v, want nil: an unknown key is forward compatibility, not a defect", err)
	}
	if r.Label != "x" || r.Relation.Kind != RelRight || r.Relation.MaxDistance != 0.5 || r.Shape != ShapeAmount {
		t.Errorf("ParseRule() = %+v, want the known fields undisturbed by the unknown ones", r)
	}
	if r.re == nil {
		t.Error("ParseRule() left re nil")
	}
	if r.re.String() != "x" {
		t.Errorf(`re = %q, want "x": the JSON "re" key must not reach the unexported field`, r.re.String())
	}
}

// An empty label is accepted on purpose -- a seventh error case could reject an EXTR-14
// generated rule. It matches every token; maxCandidatesPerField is the downstream bound.
func TestParseRule_AcceptsAnEmptyLabel(t *testing.T) {
	r, err := ParseRule(ruleBody(t, "", "same_token", 0, "name"))
	if err != nil {
		t.Fatalf("ParseRule() error = %v, want nil: an empty label is deliberately accepted", err)
	}
	if r.re == nil {
		t.Fatal("ParseRule() left re nil on an empty label")
	}
	for _, in := range []string{"", "Invoice No", "1,234.00"} {
		if !r.re.MatchString(in) {
			t.Errorf("empty label does not match %q; it is documented as matching every token", in)
		}
	}
}

// Label is RE2, not Perl: backreferences and lookaround are compile errors, which is why
// the byte cap is a cost bound rather than a ReDoS defence.
func TestParseRule_LabelIsRE2NotPerl(t *testing.T) {
	for _, label := range []string{`(a)\1`, `(?=a)`, `(?!a)`, `(?<=a)`} {
		t.Run(label, func(t *testing.T) {
			_, err := ParseRule(ruleBody(t, label, "same_token", 0, "invoice_number"))
			if err == nil {
				t.Fatalf("ParseRule(%q) error = nil, want a compile error: RE2 has no backreferences or lookaround", label)
			}
			if !strings.Contains(err.Error(), "anchor rule: label:") {
				t.Errorf("ParseRule(%q) error = %q, want it prefixed \"anchor rule: label:\"", label, err.Error())
			}
		})
	}
}

// A non-ASCII label compiles and matches, and (?i) is RE2 simple case folding, not a locale
// aware ToLower: it folds K to the Kelvin sign but leaves Turkish dotless i alone.
func TestParseRule_NonASCIILabelAndCaseFolding(t *testing.T) {
	r, err := ParseRule(ruleBody(t, `(?i)№\s*`, "same_token", 0, "invoice_number"))
	if err != nil {
		t.Fatalf("ParseRule() error = %v, want nil on a non-ASCII label", err)
	}
	if !r.re.MatchString("№ 42") {
		t.Errorf("label %q does not match %q", r.Label, "№ 42")
	}

	for _, tc := range []struct {
		label, in string
		want      bool
	}{
		{`(?i)k`, "K", true},  // Kelvin sign folds to k
		{`(?i)i`, "ı", false}, // Turkish dotless i does not fold to i
		{`(?i)i`, "İ", false}, // Turkish dotted capital i does not fold to i
		{`(?i)i`, "I", true},
	} {
		t.Run(fmt.Sprintf("%s_%U", tc.label, []rune(tc.in)[0]), func(t *testing.T) {
			rr, err := ParseRule(ruleBody(t, tc.label, "same_token", 0, "name"))
			if err != nil {
				t.Fatalf("ParseRule() error = %v, want nil", err)
			}
			if got := rr.re.MatchString(tc.in); got != tc.want {
				t.Errorf("%q.MatchString(%q) = %v, want %v", tc.label, tc.in, got, tc.want)
			}
		})
	}
}

// anchorLexicon's iteration order is fingerprint input, so the order is pinned, not just the
// slice-ness A-08 asserts.
func TestAnchorLexicon_OrderIsPinned(t *testing.T) {
	want := []string{
		"invoice_no", "issue_date", "supplier_tin", "buyer_tin", "supplier_name",
		"buyer_name", "currency", "subtotal", "vat", "total",
	}

	got := make([]string, 0, len(anchorLexicon))
	for _, entry := range anchorLexicon {
		got = append(got, entry.ID)
	}
	if len(got) == 0 {
		t.Fatal("anchorLexicon is empty")
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("anchorLexicon order = %v, want %v: iteration order is fingerprint input", got, want)
	}
}

// Every entry must match a realistic label and reject a near-miss, so no entry can rot into
// a pattern matching everything or nothing.
func TestAnchorLexicon_EveryEntryMatchesAndRejects(t *testing.T) {
	cases := map[string]struct{ match, reject string }{
		"invoice_no":    {"Invoice No:", "Invoice Date"},
		"issue_date":    {"Invoice Date", "Dated"},
		"supplier_tin":  {"Supplier TIN", "Supplier Name"},
		"buyer_tin":     {"Buyer TIN", "Supplier TIN"},
		"supplier_name": {"Supplier", "Buyer"},
		"buyer_name":    {"Bill To", "Supplier"},
		"currency":      {"Currency", "Concurrency"},
		"subtotal":      {"Sub-total", "Grand Total"},
		"vat":           {"VAT", "Vatican"},
		// "Sub-total" DOES match total -- the hyphen is a word boundary. That overlap is
		// deliberate; "Subtotal" unhyphenated is the near-miss.
		"total": {"Grand Total", "Subtotal"},
	}

	if len(anchorLexicon) == 0 {
		t.Fatal("anchorLexicon is empty")
	}
	if len(cases) != len(anchorLexicon) {
		t.Fatalf("%d cases for %d lexicon entries: every entry needs one", len(cases), len(anchorLexicon))
	}

	for _, entry := range anchorLexicon {
		tc, ok := cases[entry.ID]
		if !ok {
			t.Errorf("anchorLexicon entry %q has no match/reject case", entry.ID)
			continue
		}
		re, err := regexp.Compile(entry.Pattern)
		if err != nil {
			t.Errorf("%s: pattern %q does not compile: %v", entry.ID, entry.Pattern, err)
			continue
		}
		if !re.MatchString(tc.match) {
			t.Errorf("%s: pattern %q does not match %q -- the entry recognises nothing realistic", entry.ID, entry.Pattern, tc.match)
		}
		if re.MatchString(tc.reject) {
			t.Errorf("%s: pattern %q matches %q -- the entry has widened past its own label", entry.ID, entry.Pattern, tc.reject)
		}
	}
}

// AC-4's oracle: the JSON block in anchor.go's package comment is real, parseable input, and
// the prose beneath it names the same kinds, shapes and byte cap the code enforces.
func TestPackageComment_DocumentsTheShippedSchema(t *testing.T) {
	doc := packageDoc(t)

	body := indentedBlock(t, doc)
	if !json.Valid([]byte(body)) {
		t.Fatalf("the documented JSON block is not valid JSON:\n%s", body)
	}
	r, err := ParseRule([]byte(body))
	if err != nil {
		t.Fatalf("ParseRule(documented body) error = %v, want nil", err)
	}
	if r.re == nil {
		t.Error("ParseRule(documented body) left re nil")
	}

	kinds := []RelationKind{RelSameToken, RelRight, RelBelow}
	shapes := []Shape{ShapeInvoiceNumber, ShapeDate, ShapeAmount, ShapeTIN, ShapeCurrency, ShapeName}
	for _, kind := range kinds {
		if !strings.Contains(doc, fmt.Sprintf("%q", string(kind))) {
			t.Errorf("the package comment does not name relation kind %q", kind)
		}
		if _, err := ParseRule(ruleBody(t, "x", string(kind), 0, "amount")); err != nil {
			t.Errorf("ParseRule rejects documented kind %q: %v", kind, err)
		}
	}
	for _, shape := range shapes {
		if !strings.Contains(doc, fmt.Sprintf("%q", string(shape))) {
			t.Errorf("the package comment does not name shape %q", shape)
		}
		if _, err := ParseRule(ruleBody(t, "x", "right", 0, string(shape))); err != nil {
			t.Errorf("ParseRule rejects documented shape %q: %v", shape, err)
		}
	}
	if !strings.Contains(doc, fmt.Sprint(maxRuleLabelBytes)) {
		t.Errorf("the package comment does not name the %d-byte cap", maxRuleLabelBytes)
	}
	if !strings.Contains(doc, "[0,1]") {
		t.Error("the package comment does not name the [0,1] max_distance range")
	}
}

// packageDoc returns anchor.go's package comment. The test binary's CWD is its own package
// directory, so the relative path resolves.
func packageDoc(t *testing.T) string {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), "anchor.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse anchor.go: %v", err)
	}
	if f.Doc == nil {
		t.Fatal("anchor.go has no package comment; AC-4 requires the schema documented there")
	}
	return f.Doc.Text()
}

// indentedBlock returns the one tab-indented code block in doc, dedented.
func indentedBlock(t *testing.T, doc string) string {
	t.Helper()

	var block []string
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "\t") {
			block = append(block, strings.TrimPrefix(line, "\t"))
		}
	}
	if len(block) == 0 {
		t.Fatal("the package comment has no indented JSON block")
	}
	return strings.Join(block, "\n")
}
