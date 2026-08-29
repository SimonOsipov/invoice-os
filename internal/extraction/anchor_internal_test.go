// anchor_internal_test.go: A-01..A-10 (AC #6). Package extraction, not extraction_test: A-01
// reads Rule.re, A-04/A-10 compare against the unexported zero Rule, and A-08/A-09 read
// anchorLexicon directly, as do the two lexicon/matcher guards at the end of the file.
package extraction

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
)

// A-01: the schema body, copied character-for-character from anchor.go's package comment,
// parses into the documented fields with Label compiled.
func TestParseRule_AcceptsTheDocumentedShape(t *testing.T) {
	const body = `{
  "label":    "(?i)\\b(invoice|inv|bill|doc(ument)?)\\.?\\s*((no|num(ber)?)\\b|#)",
  "relation": { "kind": "same_token", "max_distance": 0.0 },
  "shape":    "invoice_number"
}`

	r, err := ParseRule([]byte(body))
	if err != nil {
		t.Fatalf("ParseRule() error = %v, want nil", err)
	}
	if r.re == nil {
		t.Errorf("ParseRule() left re nil; Label must be compiled before Resolve can use it")
	}
	if want := `(?i)\b(invoice|inv|bill|doc(ument)?)\.?\s*((no|num(ber)?)\b|#)`; r.Label != want {
		t.Errorf("Label = %q, want %q", r.Label, want)
	}
	if r.Relation.Kind != RelSameToken {
		t.Errorf("Relation.Kind = %q, want %q", r.Relation.Kind, RelSameToken)
	}
	if r.Relation.MaxDistance != 0.0 {
		t.Errorf("Relation.MaxDistance = %v, want 0", r.Relation.MaxDistance)
	}
	if r.Shape != ShapeInvoiceNumber {
		t.Errorf("Shape = %q, want %q", r.Shape, ShapeInvoiceNumber)
	}
}

// A-02: an unrecognised relation.kind is rejected, naming the offending value.
func TestParseRule_RejectsAnUnknownRelationKind(t *testing.T) {
	const body = `{"label":"x","relation":{"kind":"diagonal","max_distance":0},"shape":"invoice_number"}`

	r, err := ParseRule([]byte(body))
	if err == nil {
		t.Fatal("ParseRule() error = nil, want an unknown-relation-kind error")
	}
	if !strings.Contains(err.Error(), "unknown relation kind") || !strings.Contains(err.Error(), "diagonal") {
		t.Errorf(`ParseRule() error = %q, want it to name "unknown relation kind" and the value "diagonal"`, err.Error())
	}
	if r != (Rule{}) {
		t.Errorf("ParseRule() returned %+v on error, want the zero Rule", r)
	}
}

// A-03: an unrecognised shape is rejected, naming the offending value.
func TestParseRule_RejectsAnUnknownShape(t *testing.T) {
	const body = `{"label":"x","relation":{"kind":"same_token","max_distance":0},"shape":"phone_number"}`

	r, err := ParseRule([]byte(body))
	if err == nil {
		t.Fatal("ParseRule() error = nil, want an unknown-shape error")
	}
	if !strings.Contains(err.Error(), "unknown shape") || !strings.Contains(err.Error(), "phone_number") {
		t.Errorf(`ParseRule() error = %q, want it to name "unknown shape" and the value "phone_number"`, err.Error())
	}
	if r != (Rule{}) {
		t.Errorf("ParseRule() returned %+v on error, want the zero Rule", r)
	}
}

// A-04: a Label RE2 refuses is rejected, wrapping the regexp package's own error, and no
// failure branch returns a partially-populated Rule.
func TestParseRule_RejectsAnUncompilablePattern(t *testing.T) {
	const body = `{"label":"(unclosed","relation":{"kind":"same_token","max_distance":0},"shape":"invoice_number"}`

	r, err := ParseRule([]byte(body))
	if err == nil {
		t.Fatal("ParseRule() error = nil, want a regexp compile error")
	}
	var synErr *syntax.Error
	if !errors.As(err, &synErr) {
		t.Errorf("ParseRule() error = %q, want it to wrap regexp/syntax.Error (the underlying regexp.Compile failure)", err.Error())
	}
	if !strings.Contains(err.Error(), "anchor rule: label:") {
		t.Errorf(`ParseRule() error = %q, want it prefixed "anchor rule: label:"`, err.Error())
	}
	if r != (Rule{}) {
		t.Errorf("ParseRule() returned %+v on a compile error, want the zero Rule", r)
	}
}

// A-05: the 512-byte cap must fire before regexp.Compile is ever called. A 513-byte flat
// pattern compiles fine under RE2, so a test that only checks "returns an error" would pass
// on the wrong branch -- the fixture self-check below proves that.
func TestParseRule_RejectsAnOversizeLabel(t *testing.T) {
	label := strings.Repeat("a", 513)
	if _, err := regexp.Compile(label); err != nil {
		t.Fatalf("test fixture invalid: %q must compile as RE2, got %v", label, err)
	}

	body := []byte(`{"label":"` + label + `","relation":{"kind":"same_token","max_distance":0},"shape":"invoice_number"}`)

	r, err := ParseRule(body)
	if err == nil {
		t.Fatal("ParseRule() error = nil, want an over-cap error")
	}
	if !strings.Contains(err.Error(), "512") {
		t.Errorf("ParseRule() error = %q, want it to name the 512-byte cap", err.Error())
	}
	if r != (Rule{}) {
		t.Errorf("ParseRule() returned %+v on error, want the zero Rule", r)
	}
}

// A-06: max_distance outside [0,1] is rejected unconditionally, not only for relations that read it.
func TestParseRule_RejectsMaxDistanceOutsideTheUnitRange(t *testing.T) {
	for _, dist := range []float64{-0.1, 1.5} {
		body := []byte(fmt.Sprintf(`{"label":"x","relation":{"kind":"right","max_distance":%v},"shape":"invoice_number"}`, dist))

		r, err := ParseRule(body)
		if err == nil {
			t.Errorf("max_distance=%v: ParseRule() error = nil, want an outside-[0,1] error", dist)
			continue
		}
		if !strings.Contains(err.Error(), "outside [0,1]") {
			t.Errorf("max_distance=%v: ParseRule() error = %q, want it to name the [0,1] range", dist, err.Error())
		}
		if r != (Rule{}) {
			t.Errorf("max_distance=%v: ParseRule() returned %+v on error, want the zero Rule", dist, r)
		}
	}
}

// A-07: same_token never reads max_distance, so a value inside [0,1] is stored, not rejected
// and not zeroed.
func TestParseRule_SameTokenIgnoresMaxDistance(t *testing.T) {
	const body = `{"label":"x","relation":{"kind":"same_token","max_distance":0.9},"shape":"invoice_number"}`

	r, err := ParseRule([]byte(body))
	if err != nil {
		t.Fatalf("ParseRule() error = %v, want nil: max_distance is unread by same_token, not a reason to reject", err)
	}
	if r.Relation.Kind != RelSameToken {
		t.Errorf("Relation.Kind = %q, want %q", r.Relation.Kind, RelSameToken)
	}
	if r.Relation.MaxDistance != 0.9 {
		t.Errorf("Relation.MaxDistance = %v, want 0.9 unchanged", r.Relation.MaxDistance)
	}
}

// A-08: anchorLexicon is a slice, never a map -- iteration order is fingerprint input.
func TestAnchorLexicon_IsOrderedAndUnique(t *testing.T) {
	// The strongest slice-not-map oracle: fails to compile if the declared type ever
	// becomes a map.
	var _ []struct{ ID, Pattern string } = anchorLexicon

	if got := reflect.TypeOf(anchorLexicon).Kind(); got != reflect.Slice {
		t.Fatalf("anchorLexicon is a %s, want a slice", got)
	}
	if len(anchorLexicon) != 10 {
		t.Fatalf("len(anchorLexicon) = %d, want 10: the shipped generic label set", len(anchorLexicon))
	}

	seen := make(map[string]bool, len(anchorLexicon))
	for _, entry := range anchorLexicon {
		if entry.ID == "" {
			t.Errorf("anchorLexicon has an empty ID for pattern %q", entry.Pattern)
			continue
		}
		if seen[entry.ID] {
			t.Errorf("anchorLexicon.ID %q repeats", entry.ID)
		}
		seen[entry.ID] = true

		if _, err := regexp.Compile(entry.Pattern); err != nil {
			t.Errorf("anchorLexicon[%q].Pattern = %q does not compile as RE2: %v", entry.ID, entry.Pattern, err)
		}
	}
}

// A-09: the invoice_no entry must recognise all five Q7 synonyms, including the two the
// story's original pattern misses ("Inv #", "Invoice #") because a trailing \b sat outside
// the alternation and "#" is not a word character. It must still reject near-misses that
// share a prefix.
func TestAnchorLexicon_RecognisesTheQ7SynonymSet(t *testing.T) {
	var pattern string
	for _, entry := range anchorLexicon {
		if entry.ID == "invoice_no" {
			pattern = entry.Pattern
			break
		}
	}
	if pattern == "" {
		t.Fatal(`anchorLexicon has no "invoice_no" entry`)
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("invoice_no pattern %q does not compile: %v", pattern, err)
	}

	for _, want := range []string{"Invoice No", "Inv #", "Document No", "Bill No", "INVOICE NUMBER"} {
		if !re.MatchString(want) {
			t.Errorf("invoice_no pattern %q does not match %q, want a match", pattern, want)
		}
	}
	for _, reject := range []string{"Invoice Note", "Invoice Date", "Bill To"} {
		if re.MatchString(reject) {
			t.Errorf("invoice_no pattern %q matches %q, want no match", pattern, reject)
		}
	}
}

// A-10: malformed JSON is rejected before any other validation runs, wrapping the decoder's
// own error.
func TestParseRule_RejectsMalformedJSON(t *testing.T) {
	r, err := ParseRule([]byte(`{"label": "x",`))
	if err == nil {
		t.Fatal("ParseRule() error = nil, want a decode error")
	}
	var synErr *json.SyntaxError
	if !errors.As(err, &synErr) {
		t.Errorf("ParseRule() error = %q, want it to wrap json.SyntaxError (the underlying json.Unmarshal failure)", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "anchor rule:") {
		t.Errorf(`ParseRule() error = %q, want it prefixed "anchor rule:"`, err.Error())
	}
	if r != (Rule{}) {
		t.Errorf("ParseRule() returned %+v on malformed JSON, want the zero Rule", r)
	}
}

// The compiled companion must stay element-for-element parallel to anchorLexicon: Fingerprint
// reads only anchorLabelMatchers, so a reorder or a dropped entry would silently change every
// fingerprint.
func TestAnchorLabelMatchers_ParallelsTheLexicon(t *testing.T) {
	if len(anchorLexicon) == 0 {
		t.Fatal("anchorLexicon is empty; every assertion below would pass over nothing")
	}
	if len(anchorLabelMatchers) != len(anchorLexicon) {
		t.Fatalf("len(anchorLabelMatchers) = %d, len(anchorLexicon) = %d, want equal",
			len(anchorLabelMatchers), len(anchorLexicon))
	}

	for i, entry := range anchorLexicon {
		if got := anchorLabelMatchers[i].ID; got != entry.ID {
			t.Errorf("anchorLabelMatchers[%d].ID = %q, anchorLexicon[%d].ID = %q, want equal in order",
				i, got, i, entry.ID)
		}
		if anchorLabelMatchers[i].RE == nil {
			t.Errorf("anchorLabelMatchers[%d] (%q) has a nil RE", i, entry.ID)
		}
	}
}

// Fingerprint joins elements as labelID + ":" + band with "|". Neither separator may occur
// inside a label id, or two different observation lists could encode to one string.
func TestAnchorLexicon_IDsAreSeparatorSafe(t *testing.T) {
	if len(anchorLexicon) == 0 {
		t.Fatal("anchorLexicon is empty; the loop below would pass over nothing")
	}

	safe := regexp.MustCompile(`^[a-z_]+$`)
	for _, entry := range anchorLexicon {
		if !safe.MatchString(entry.ID) {
			t.Errorf("anchorLexicon ID %q is not [a-z_]+; a %q or %q inside an id makes the joined element string ambiguous",
				entry.ID, ":", "|")
		}
	}
}
