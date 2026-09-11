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
	"slices"
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
	if len(anchorLexicon) != 18 {
		t.Fatalf("len(anchorLexicon) = %d, want 18: the shipped generic label set", len(anchorLexicon))
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

// --- the party-scoped TIN's lexicon shape ------------------------------------

// alIDs is every anchorLexicon id whose pattern matches text, in lexicon order.
func alIDs(text string) []string {
	var out []string
	for _, m := range anchorLabelMatchers {
		if m.RE.MatchString(text) {
			out = append(out, m.ID)
		}
	}
	return out
}

// alSpan is the span id claims on text, or nil when it does not match.
func alSpan(text, id string) []int {
	for _, m := range anchorLabelMatchers {
		if m.ID == id {
			return m.RE.FindStringIndex(text)
		}
	}
	return nil
}

// "Invoice to" and "Deliver to" head the buyer's block AND label the buyer's fields. The
// control keeps the widening off the invoice number, which shares the word "Invoice".
func TestAnchorLexicon_TheBuyerVocabularyCarriesInvoiceToAndDeliverTo(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty; every assertion below would run over nothing")
	}

	for _, text := range []string{"Invoice to", "Deliver to", "INVOICE TO", "Deliver To:"} {
		ids := alIDs(text)
		for _, want := range []string{"buyer_name", "buyer_tin"} {
			if !slices.Contains(ids, want) {
				t.Errorf("%q matches %v, want it to name %q; the buyer vocabulary carries the two shipping phrases", text, ids, want)
			}
		}
	}

	const control = "Invoice No: INV-1001"
	ids := alIDs(control)
	if !slices.Contains(ids, "invoice_no") {
		t.Errorf("%q matches %v, want it to name invoice_no; the control is not exercising the overlap it exists for", control, ids)
	}
	if slices.Contains(ids, "buyer_name") {
		t.Errorf("%q matches %v and now names buyer_name; the phrase is \"invoice to\", not the word \"invoice\"", control, ids)
	}
}

// bare_tin is the party-LESS TIN label. On a party-bearing token the party entry claims the
// strictly wider span, so anchorOutranked suppresses bare_tin there -- the same mechanism that
// already suppresses supplier_name inside "Supplier TIN:".
func TestAnchorLexicon_ABareTINLabelIsOutrankedByAPartyBearingOne(t *testing.T) {
	const bare = "bare_tin"

	if !slices.ContainsFunc(anchorLexicon, func(e struct{ ID, Pattern string }) bool { return e.ID == bare }) {
		t.Fatalf("anchorLexicon holds no %q entry; the party-less TIN label has no id of its own and supplier_tin still claims it", bare)
	}

	for _, c := range []struct{ text, owner string }{
		{"Supplier TIN: 99999999-0101", "supplier_tin"},
		{"Buyer TIN ", "buyer_tin"},
	} {
		inner, outer := alSpan(c.text, bare), alSpan(c.text, c.owner)
		if inner == nil || outer == nil {
			t.Errorf("%q: %s span %v, %s span %v; both must match or the containment below compares nothing", c.text, bare, inner, c.owner, outer)
			continue
		}
		if !(outer[0] <= inner[0] && outer[1] >= inner[1] && outer[1]-outer[0] > inner[1]-inner[0]) {
			t.Errorf("%q: %s claims %v and %s claims %v; the party entry must claim the STRICTLY wider span or anchorOutranked leaves the bare label anchoring", c.text, bare, inner, c.owner, outer)
		}
		if !anchorOutranked(c.text, inner) {
			t.Errorf("%q: anchorOutranked left the %s span %v standing; the bare label would anchor a rule on a token a party owns", c.text, bare, inner)
		}
	}

	// The party-less token, where bare_tin is the whole point.
	const partyless = "TIN: "
	ids := alIDs(partyless)
	if !slices.Equal(ids, []string{bare}) {
		t.Errorf("%q matches %v, want exactly [%s]; supplier_tin's party word is required now, so a bare label belongs to no party", partyless, ids, bare)
	}
	loc := alSpan(partyless, bare)
	if loc == nil || anchorOutranked(partyless, loc) {
		t.Errorf("%q: %s span %v; nothing wider claims this token and the bare label must anchor its rules", partyless, bare, loc)
	}
}

// party_ref and signature are OWNING PHRASES: the whole phrase is the label, so the party word
// inside it must not anchor a value of its own. Same mechanism as bare_tin above, one layer out.
func TestAnchorLexicon_AnOwningPhraseOutranksTheNarrowPartyWord(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty; every assertion below would run over nothing")
	}

	for _, c := range []struct{ text, owner, narrow string }{
		{"Customer No.", "party_ref", "buyer_name"},
		{"Supplier No.", "party_ref", "supplier_name"},
		{"Buyer's Signature", "signature", "buyer_name"},
		{"Supplier's Signature", "signature", "supplier_name"},
	} {
		inner := alSpan(c.text, c.narrow)
		if inner == nil {
			t.Errorf("%q: %s does not match at all; the suppression below has nothing to suppress", c.text, c.narrow)
			continue
		}
		if !anchorOutranked(c.text, inner) {
			t.Errorf("%q: anchorOutranked left the %s span %v standing; the party word inside an owning phrase would anchor a rule on a token that names no party", c.text, c.narrow, inner)
		}

		outer := alSpan(c.text, c.owner)
		if outer == nil {
			t.Errorf("%q: anchorLexicon holds no %s span; nothing claims the phrase whole", c.text, c.owner)
			continue
		}
		if !(outer[0] <= inner[0] && outer[1] >= inner[1] && outer[1]-outer[0] > inner[1]-inner[0]) {
			t.Errorf("%q: %s claims %v and %s claims %v; the phrase must claim the STRICTLY wider span or anchorOutranked leaves the party word anchoring", c.text, c.narrow, inner, c.owner, outer)
		}
	}

	// The control: a bare party word, where nothing wider claims the token and the party entry
	// must still anchor its rules.
	const control = "Buyer "
	if ids := alIDs(control); !slices.Contains(ids, "buyer_name") {
		t.Errorf("%q matches %v, want it to name buyer_name; the control is not exercising the entry the cases above suppress", control, ids)
	}
	ctl := alSpan(control, "buyer_name")
	if ctl == nil || anchorOutranked(control, ctl) {
		t.Errorf("%q: buyer_name span %v was outranked; an owning phrase must not suppress the party word on a token it does not appear on", control, ctl)
	}
}

// reg_identifier and doc_title are OWNING PHRASES over the amount vocabulary: the whole phrase
// is the label, so the bare "vat"/"tax" inside it must not anchor an amount. Same mechanism as
// AnOwningPhraseOutranksTheNarrowPartyWord above, one vocabulary over.
func TestAnchorLexicon_AnOwningPhraseOutranksTheAmountLabel(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty; every assertion below would run over nothing")
	}

	for _, c := range []struct{ text, owner, narrow string }{
		{"VAT REG NO", "reg_identifier", "vat"},
		{"VAT REGISTRATION NUMBER", "reg_identifier", "vat"},
		{"TAX REGISTRATION NUMBER", "reg_identifier", "vat"},
		{"TAX INVOICE", "doc_title", "vat"},
		{"VAT INVOICE", "doc_title", "vat"},
		// The phrase does not open the token: an entry anchored to the start of a token passes
		// every other case here (TestTier1_AnOwningPhraseSuppressesWhereverItSitsOnTheToken).
		{"PROFORMA TAX INVOICE", "doc_title", "vat"},
		{"Statement VAT REG NO", "reg_identifier", "vat"},
	} {
		inner := alSpan(c.text, c.narrow)
		if inner == nil {
			t.Errorf("%q: %s does not match at all; the suppression below has nothing to suppress", c.text, c.narrow)
			continue
		}
		if !anchorOutranked(c.text, inner) {
			t.Errorf("%q: anchorOutranked left the %s span %v standing; the amount label inside an owning phrase would anchor a value on a token carrying no amount", c.text, c.narrow, inner)
		}

		outer := alSpan(c.text, c.owner)
		if outer == nil {
			t.Errorf("%q: anchorLexicon holds no %s span; nothing claims the phrase whole", c.text, c.owner)
			continue
		}
		if !(outer[0] <= inner[0] && outer[1] >= inner[1] && outer[1]-outer[0] > inner[1]-inner[0]) {
			t.Errorf("%q: %s claims %v and %s claims %v; the phrase must claim the STRICTLY wider span or anchorOutranked leaves the amount label anchoring", c.text, c.narrow, inner, c.owner, outer)
		}
	}

	// The control: an amount label carrying its own value, where nothing wider claims the token
	// and vat must still anchor.
	const control = "VAT: 75.00"
	if ids := alIDs(control); !slices.Contains(ids, "vat") {
		t.Errorf("%q matches %v, want it to name vat; the control is not exercising the entry the cases above suppress", control, ids)
	}
	ctl := alSpan(control, "vat")
	if ctl == nil || anchorOutranked(control, ctl) {
		t.Errorf("%q: vat span %v was outranked; an owning phrase must not suppress the amount label on a token it does not appear on", control, ctl)
	}
}

// "TAX IDENTIFICATION NUMBER" is a TIN label, and bare_tin already claims it whole. A second
// entry that ties with bare_tin here must leave that unchanged. Green before the amount-vocabulary
// owning phrases and green after: a regression guard, not their oracle.
func TestAnchorLexicon_TaxIdentificationNumberStaysSuppressed(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty; every assertion below would run over nothing")
	}
	const phrase = "TAX IDENTIFICATION NUMBER"

	if !isBareAnchorLabel(phrase) {
		t.Errorf("%q is not refused as a value; a TIN label readable as a value reaches a field of its own", phrase)
	}
	narrow := alSpan(phrase, "vat")
	if narrow == nil {
		t.Fatalf("%q: vat does not match at all; the suppression below has nothing to suppress", phrase)
	}
	if !anchorOutranked(phrase, narrow) {
		t.Errorf("%q: anchorOutranked left the vat span %v standing; an identification number would anchor an amount", phrase, narrow)
	}

	ids := alIDs(phrase)
	if !slices.Contains(ids, "bare_tin") {
		t.Errorf("%q matches %v, want it to name bare_tin; without bare_tin's tax-id alternative the phrase belongs to whatever else claims it", phrase, ids)
	}
	// Every claimant that SURVIVES suppression must claim the phrase whole. A surviving claimant
	// with a narrower span is a label reading part of an identifier.
	for _, id := range ids {
		loc := alSpan(phrase, id)
		if anchorOutranked(phrase, loc) {
			continue
		}
		if loc[0] != 0 || loc[1] != len(phrase) {
			t.Errorf("%q: %s survives suppression claiming %v, not the whole phrase; a partial claimant leaves part of the identifier readable as a value", phrase, id, loc)
		}
	}
}

// alBareTokenCases pairs each owning phrase with the bare token(s) it is built around: the head
// of its own alternation, stripped of the tail that makes it a phrase.
var alBareTokenCases = []struct {
	owner, whole string
	bare         []string
}{
	{"party_ref", "Customer No.", []string{"Customer", "Account"}},
	{"signature", "Buyer's Signature", []string{"Buyer", "Signature"}},
	{"reg_identifier", "VAT REG NO", []string{"VAT", "Tax", "V.A.T."}},
	{"rc_number", "RC NUMBER", []string{"RC", "CAC"}},
	{"doc_title", "TAX INVOICE", []string{"TAX", "VAT", "Invoice"}},
	{"due_date", "Due Date", []string{"Due", "Date"}},
	{"withholding_tax", "Withholding Tax", []string{"Withholding", "Tax"}},
}

// anchorOutranked needs a STRICTLY wider span, so an owning phrase that loses its required tail
// degenerates to exactly the span of the entry it exists to suppress -- suppressing nothing while
// every containment-shaped spec stays green. Each phrase must therefore refuse its own bare token.
func TestAnchorLexicon_AnOwningPhraseRefusesTheBareTokenInsideIt(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty; every assertion below would run over nothing")
	}
	// Set equality with the declared owning phrases, so neither list can grow or shrink alone and
	// leave a phrase degenerating unwatched.
	if len(alBareTokenCases) != len(t1OwningPhraseIDs) {
		t.Fatalf("%d bare-token case(s) for %d owning phrase(s): every owning phrase needs one", len(alBareTokenCases), len(t1OwningPhraseIDs))
	}
	seen := map[string]bool{}
	for _, c := range alBareTokenCases {
		if !slices.Contains(t1OwningPhraseIDs, c.owner) {
			t.Fatalf("%q carries a bare-token case yet is no declared owning phrase; the case is asserted against an entry that earns its place another way", c.owner)
		}
		// Without this the count above is length plus subset, not set equality: a duplicated
		// owner keeps the count and leaves another phrase degenerating unwatched.
		if seen[c.owner] {
			t.Fatalf("%q carries two bare-token cases; one declared owning phrase then has none", c.owner)
		}
		seen[c.owner] = true
	}

	for _, c := range alBareTokenCases {
		// The paired positive: without it the refusals below hold equally against a pattern
		// broken to recognise nothing.
		if alSpan(c.whole, c.owner) == nil {
			t.Errorf("%s does not match %q, the label it exists for", c.owner, c.whole)
			continue
		}
		for _, bare := range c.bare {
			if loc := alSpan(bare, c.owner); loc != nil {
				t.Errorf("%s claims %v on the bare %q; a phrase reaching its own bare token claims no more than the entry it exists to suppress, so anchorOutranked leaves that entry anchoring", c.owner, loc, bare)
			}
		}
	}
}

// alSuppressesARuleBearingEntry names the field-filling lexicon entry that phrase strictly
// contains, or "" when it contains none.
func alSuppressesARuleBearingEntry(phrase string, ruleBearing map[string]int) string {
	for _, e := range anchorLexicon {
		if ruleBearing[e.ID] == 0 {
			continue
		}
		if inner := alSpan(phrase, e.ID); inner != nil && anchorOutranked(phrase, inner) {
			return e.ID
		}
	}
	return ""
}

// An entry listed in t1OwningPhraseIDs is exempt from carrying a Tier-1 rule, so nothing else
// makes it earn its place: it ships in the fingerprint either way. Every such entry must be
// refused as a value whole, and must then earn the exemption on exactly ONE of two arms --
// suppression (it strictly contains an entry that DOES fill a field), or print (no rule-bearing
// entry sits inside it, and a shipped layout carries the phrase). The arms are exclusive, so a
// phrase cannot be parked in the weaker one; the print arm is
// TestWildLayouts_APrintedOwningPhraseIsObservedOnTheCorpus, which reads a PDF this package
// cannot.
func TestAnchorLexicon_AnOwningPhraseEarnsItsExemption(t *testing.T) {
	if len(t1OwningPhraseIDs) == 0 {
		t.Fatal("t1OwningPhraseIDs is empty; every assertion below would run over nothing")
	}
	for _, id := range t1PrintedPhraseIDs {
		if !slices.Contains(t1OwningPhraseIDs, id) {
			t.Fatalf("t1PrintedPhraseIDs names %q, which t1OwningPhraseIDs does not; only an entry exempt from carrying a rule needs an arm to earn that exemption", id)
		}
	}

	ruleBearing := map[string]int{}
	for _, r := range Tier1Rules {
		for _, e := range anchorLexicon {
			if r.Rule.Label == e.Pattern {
				ruleBearing[e.ID]++
			}
		}
	}
	if len(ruleBearing) == 0 {
		t.Fatal("no lexicon entry carries a Tier-1 rule; the containment below would find nothing for any phrase")
	}

	for _, id := range t1OwningPhraseIDs {
		phrase := alMatchRejectCases[id].match
		if phrase == "" {
			t.Errorf("%s has no alMatchRejectCases entry; its exemption is asserted against nothing", id)
			continue
		}
		if !isBareAnchorLabel(phrase) {
			t.Errorf("%s: %q is not refused as a value; an owning phrase readable as a name is not the label it claims to be", id, phrase)
		}

		suppressed := alSuppressesARuleBearingEntry(phrase, ruleBearing)
		if slices.Contains(t1PrintedPhraseIDs, id) {
			if suppressed != "" {
				t.Errorf("%s: %q strictly contains the rule-bearing entry %s, so suppression IS what it is for and it belongs in the containment arm", id, phrase, suppressed)
			}
			continue
		}
		if suppressed == "" {
			t.Errorf("%s: %q strictly contains no rule-bearing entry, so anchorOutranked suppresses nothing for it; the entry ships in the fingerprint and resolves nothing. An owning phrase that suppresses nothing belongs in t1PrintedPhraseIDs and must be printed by a shipped layout", id, phrase)
		}
	}

	// The control: a rule-bearing entry's own label suppresses nothing inside it, so the probe
	// above is not a test every entry in the table passes.
	const ctl = "supplier_name"
	if slices.Contains(t1OwningPhraseIDs, ctl) {
		t.Fatalf("%s is declared an owning phrase; the control below no longer contrasts with the cases above", ctl)
	}
	if got := alSuppressesARuleBearingEntry(alMatchRejectCases[ctl].match, ruleBearing); got != "" {
		t.Errorf("%s: %q suppresses %s; the probe passes for a plain label too and proves nothing about an owning phrase", ctl, alMatchRejectCases[ctl].match, got)
	}
}

// --- the licence for the boundary having no not-outranked qualifier ----------

// alQualifierBases are label-ish and value-ish token texts. The value-ish half is what makes the
// hit population below smaller than the grid.
var alQualifierBases = []string{
	"VAT", "TAX", "V.A.T.", "Total", "Grand Total", "Sub-total", "Subtotal", "Net Amount",
	"Goods Value", "Amount Due", "Balance Due", "Currency", "CCY", "Invoice No", "Invoice Number",
	"Inv No", "Bill No", "Document No", "Date", "Invoice Date", "Issue Date", "Date of Issue",
	"Supplier", "Seller", "Vendor", "Buyer", "Customer", "Client", "Bill To", "Sold To",
	"Invoice To", "Deliver To", "Supplier TIN", "Buyer TIN", "TIN", "T.I.N.", "Tax ID",
	"Tax Identification Number", "Customer No", "Client Code", "Account Ref", "Vendor Number",
	"Buyer's Signature", "Supplier Signature", "VAT REG NO", "VAT Registration Number",
	"TAX REGISTRATION NUMBER", "RC NUMBER", "CAC NO", "TAX INVOICE", "VAT INVOICE",
	"Honeywell Group", "Adeyemi Trading Limited", "99999999-0101", "2,687.50", "NGN",
	"2026-08-14", "INV-2103", "::::", "7 AWOLOWO ROAD",
}

// alQualifierForms decorate each base. Nine of the thirteen put it past position 0, which is
// where a claim can start at an offset and a wider entry can wrap it.
var alQualifierForms = []string{
	"%s", "%s ", " %s", "%s:", "%s: 1,500.00", "1,500.00 %s", "Sub-total %s", "%s / VAT REG NO",
	"VAT REG NO / %s", "TIN %s", "%s TIN", "PROFORMA %s", "%s INVOICE",
}

// The floors. Each is a population the implication below needs to be non-vacuous, set well
// under the measured figure so lexicon churn moves them rather than breaking them:
// 780 strings, 708 with a hit, 339 carrying an outranked hit, 389 whose surviving hit starts
// past position 0.
const (
	alQualifierStringFloor    = 500
	alQualifierHitFloor       = 400
	alQualifierOutrankedFloor = 150
	alQualifierOffsetFloor    = 150
)

// anchorOutranked can never empty a token's label set: it needs a STRICTLY wider containing
// span, and the widest of a token's leftmost hits (at most one per lexicon entry) has none. So "carries a hit that is
// not itself outranked" and "carries a hit" are the same predicate, and the rightward boundary
// ships without the qualifier. Deleting this leaves the missing clause looking like an
// oversight.
func TestAnchorLexicon_OutrankingNeverEmptiesATokensLabelSet(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty; every assertion below would run over nothing")
	}

	var grid, hits, outranked, offsets int
	for _, base := range alQualifierBases {
		for _, form := range alQualifierForms {
			text := fmt.Sprintf(form, base)
			grid++

			var anyHit, survives, survivesPastStart, someOutranked bool
			for _, m := range anchorLabelMatchers {
				loc := m.RE.FindStringIndex(text)
				if loc == nil {
					continue
				}
				anyHit = true
				if anchorOutranked(text, loc) {
					someOutranked = true
					continue
				}
				survives = true
				if loc[0] > 0 {
					survivesPastStart = true
				}
			}

			if anyHit {
				hits++
			}
			if someOutranked {
				outranked++
			}
			if survivesPastStart {
				offsets++
			}
			if anyHit != survives {
				t.Errorf("%q carries a lexicon hit but every hit is outranked; the boundary's dropped qualifier would have made this token stop blocking a rightward read", text)
			}
		}
	}

	if grid < alQualifierStringFloor {
		t.Fatalf("the grid is %d string(s), want at least %d", grid, alQualifierStringFloor)
	}
	if hits < alQualifierHitFloor {
		t.Fatalf("%d of %d strings carry a lexicon hit, want at least %d; the implication above is satisfied by a grid nothing matches", hits, grid, alQualifierHitFloor)
	}
	if outranked < alQualifierOutrankedFloor {
		t.Fatalf("%d of %d strings carry a hit that IS outranked, want at least %d; with no suppression anywhere the equivalence above is trivially true", outranked, grid, alQualifierOutrankedFloor)
	}
	if offsets < alQualifierOffsetFloor {
		t.Fatalf("%d of %d strings keep a surviving hit that starts past position 0, want at least %d; an entry anchored at ^ would satisfy the equivalence on position-0 claims alone", offsets, grid, alQualifierOffsetFloor)
	}
}

// alJoins glue two bases into one token text. The separators are what a real reader emits
// between a phrase and the next word.
var alJoins = []string{"", " ", " / ", ": ", " - ", "  "}

// The floors for the mechanism grid below. Measured: 21 600 strings, 18 984 with a hit, 13 949
// carrying two or more distinct hits, 7 743 carrying an outranked one. Set well under, so
// lexicon churn moves them rather than breaking them.
const (
	alWidestGridFloor      = 15000
	alWidestHitFloor       = 12000
	alWidestMultiFloor     = 8000
	alWidestOutrankedFloor = 5000
)

// The MECHANISM behind the dropped qualifier, not just its conclusion. anchorOutranked needs a
// STRICTLY wider containing span, so the widest of a token's leftmost hits has none and always
// survives. Asserting that directly is what reds if anchorOutranked is ever loosened to accept
// an equal span -- the point at which "carries a hit" and "carries a hit not itself outranked"
// stop being the same predicate and the boundary's missing clause starts to matter.
func TestAnchorLexicon_TheWidestLexiconHitIsNeverOutranked(t *testing.T) {
	if len(anchorLabelMatchers) == 0 {
		t.Fatal("anchorLabelMatchers is empty; every assertion below would run over nothing")
	}

	var grid, hits, multi, outranked, reported int
	for _, a := range alQualifierBases {
		for _, j := range alJoins {
			for _, b := range alQualifierBases {
				text := a + j + b
				grid++

				var widest []int
				seen := map[string]bool{}
				someOutranked := false
				for _, m := range anchorLabelMatchers {
					loc := m.RE.FindStringIndex(text)
					if loc == nil {
						continue
					}
					seen[text[loc[0]:loc[1]]] = true
					if anchorOutranked(text, loc) {
						someOutranked = true
					}
					if widest == nil || loc[1]-loc[0] > widest[1]-widest[0] {
						widest = loc
					}
				}
				if widest == nil {
					continue
				}
				hits++
				if len(seen) > 1 {
					multi++
				}
				if someOutranked {
					outranked++
				}
				if anchorOutranked(text, widest) && reported < 5 {
					reported++
					t.Errorf("%q: the widest lexicon hit %q is outranked; containment is no longer strict and the boundary's dropped qualifier could change an answer", text, text[widest[0]:widest[1]])
				}
			}
		}
	}

	if grid < alWidestGridFloor {
		t.Fatalf("the grid is %d string(s), want at least %d", grid, alWidestGridFloor)
	}
	if hits < alWidestHitFloor {
		t.Fatalf("%d of %d strings carry a hit, want at least %d; the assertion above ran over almost nothing", hits, grid, alWidestHitFloor)
	}
	if multi < alWidestMultiFloor {
		t.Fatalf("%d of %d strings carry two or more distinct hits, want at least %d; with one hit each nothing can contain anything and the claim is trivial", multi, grid, alWidestMultiFloor)
	}
	if outranked < alWidestOutrankedFloor {
		t.Fatalf("%d of %d strings carry a hit that IS outranked, want at least %d; with no suppression anywhere the widest hit surviving says nothing", outranked, grid, alWidestOutrankedFloor)
	}
}
