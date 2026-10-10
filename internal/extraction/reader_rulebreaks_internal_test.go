// reader_rulebreaks_internal_test.go: applyRuleBreaks and its interplay with mergeCorrections,
// over crafted inputs.
package extraction

import (
	"encoding/json"
	"strings"
	"testing"
)

func rbBreak(field, key, msg string) ruleBreakRow {
	return ruleBreakRow{Field: field, ExtractionRuleBreak: ExtractionRuleBreak{Key: key, Message: msg}}
}

func rbVat() []ExtractionFieldState {
	return []ExtractionFieldState{{
		Name: "vat", Value: mgStr("50.00"), Alternatives: []ExtractionCandidate{}, Rules: []ExtractionRuleBreak{},
	}}
}

func TestApplyRuleBreaks_FlagsACleanValuedField(t *testing.T) {
	got := applyRuleBreaks(rbVat(), []ruleBreakRow{
		rbBreak("vat", "vat-rate", "VAT is not 7.5%."),
		rbBreak("vat", "vat-sum", "VAT does not add up."),
	})[0]
	if got.Reason != string(ReasonRuleBreak) {
		t.Errorf("reason = %q, want %q", got.Reason, ReasonRuleBreak)
	}
	want := []ExtractionRuleBreak{{Key: "vat-rate", Message: "VAT is not 7.5%."}, {Key: "vat-sum", Message: "VAT does not add up."}}
	if len(got.Rules) != 2 || got.Rules[0] != want[0] || got.Rules[1] != want[1] {
		t.Errorf("rules = %v, want %v in input order", got.Rules, want)
	}
}

func TestApplyRuleBreaks_AnExtractorReasonWins(t *testing.T) {
	in := []ExtractionFieldState{{
		Name: "buyer_tin", Value: mgStr("X"), Reason: "ambiguous", Rules: []ExtractionRuleBreak{},
		Alternatives: []ExtractionCandidate{{Value: mgStr("Y")}},
	}}
	got := applyRuleBreaks(in, []ruleBreakRow{rbBreak("buyer_tin", "buyer-tin-format", "bad")})[0]
	if got.Reason != "ambiguous" || len(got.Alternatives) != 1 || got.Rules == nil || len(got.Rules) != 0 {
		t.Errorf("got reason %q, %d alternative(s), rules %v; want ambiguous, 1, []", got.Reason, len(got.Alternatives), got.Rules)
	}
}

func TestApplyRuleBreaks_ANilValueIsNeverFlagged(t *testing.T) {
	in := []ExtractionFieldState{{Name: "total", Alternatives: []ExtractionCandidate{}, Rules: []ExtractionRuleBreak{}}}
	got := applyRuleBreaks(in, []ruleBreakRow{rbBreak("total", "total-positive", "bad")})[0]
	if got.Reason != "" || got.Rules == nil || len(got.Rules) != 0 {
		t.Errorf("got reason %q, rules %v; want \"\", []", got.Reason, got.Rules)
	}
}

func TestMergeCorrections_ATypedCorrectionClearsARuleBreak(t *testing.T) {
	flagged := applyRuleBreaks(rbVat(), []ruleBreakRow{rbBreak("vat", "vat-rate", "bad")})
	c := Correction{FieldName: "vat", Value: "75.00", Method: MethodTyped, Actor: "operator"}
	got := mergeCorrections(flagged, []Correction{c})[0]
	if got.Reason != "" || got.Rules == nil || len(got.Rules) != 0 {
		t.Errorf("got reason %q, rules %v; want \"\", []", got.Reason, got.Rules)
	}
	if got.Corrected == nil || got.Corrected.Method != "typed" {
		t.Errorf("corrected = %+v, want method typed", got.Corrected)
	}
}

func TestMergeCorrections_AnUndoRestoresARuleBreak(t *testing.T) {
	flagged := applyRuleBreaks(rbVat(), []ruleBreakRow{rbBreak("vat", "vat-rate", "bad")})
	c := Correction{FieldName: "vat", Value: "", Method: MethodUndone, Actor: "operator"}
	got := mergeCorrections(flagged, []Correction{c})[0]
	if got.Reason != string(ReasonRuleBreak) || len(got.Rules) != 1 || got.Rules[0].Key != "vat-rate" {
		t.Errorf("got reason %q, rules %v; want rule_break with vat-rate", got.Reason, got.Rules)
	}
}

func TestExtractionDetail_RulesIsNeverNil(t *testing.T) {
	lines := `[{"description":"Pen","quantity":"1","unit_price":"2.00","line_total":"2.00"}]`
	fields := []ExtractionFieldState{
		{Name: "invoice_number", Value: mgStr("A1"), Alternatives: []ExtractionCandidate{}, Rules: []ExtractionRuleBreak{}},
		{Name: "line_items", Reason: "missing", Alternatives: []ExtractionCandidate{}, Rules: []ExtractionRuleBreak{}},
	}
	got := mergeCorrections(fields, []Correction{
		{FieldName: "note", Value: "n", Method: MethodTyped, Actor: "operator"},
		{FieldName: "line_items", Value: lines, Method: MethodTyped, Actor: "operator"},
	})
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"rules":[]`); n != len(got) || len(got) < 4 {
		t.Errorf("%d empty rules array(s) over %d field(s) in %s, want one each and at least 4 fields", n, len(got), b)
	}
	if strings.Contains(string(b), `"rules":null`) {
		t.Errorf("rules marshalled null: %s", b)
	}
}

func TestApplyRuleBreaks_QAEdges(t *testing.T) {
	in := []ExtractionFieldState{
		{Name: "vat", Value: mgStr("50.00"), Alternatives: []ExtractionCandidate{}, Rules: []ExtractionRuleBreak{}},
		{Name: "total", Value: mgStr("100.00"), Alternatives: []ExtractionCandidate{}, Rules: []ExtractionRuleBreak{}},
	}
	got := applyRuleBreaks(in, []ruleBreakRow{
		rbBreak("ghost", "g-rule", "no such field"),
		rbBreak("total", "t-b", "second key first"),
		rbBreak("vat", "v-1", "one"),
		rbBreak("total", "t-a", "after"),
	})
	if len(got) != 2 {
		t.Fatalf("got %d field(s), want 2: a break for a field not in the response must not add one", len(got))
	}
	if r := got[1].Rules; len(r) != 2 || r[0].Key != "t-b" || r[1].Key != "t-a" {
		t.Errorf("total rules = %v, want input order t-b, t-a", r)
	}
	if r := got[0].Rules; len(r) != 1 || r[0].Key != "v-1" {
		t.Errorf("vat rules = %v, want only v-1 (breaks interleaved across fields)", r)
	}
	if in[0].Reason != "" || len(in[0].Rules) != 0 || in[1].Reason != "" {
		t.Errorf("the input slice was mutated: %+v", in)
	}
}

func TestApplyRuleBreaks_NoBreaksKeepsEmptyRules(t *testing.T) {
	got := applyRuleBreaks(rbVat(), nil)[0]
	if got.Reason != "" || got.Rules == nil || len(got.Rules) != 0 {
		t.Errorf("got reason %q, rules %v; want \"\", []", got.Reason, got.Rules)
	}
}
