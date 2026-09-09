// reconcile_doubt_internal_test.go: doubtfulFields itself. Package extraction -- the list is
// unexported and the behavioural walk over it cannot see an entry that names nothing.
package extraction

import (
	"slices"
	"testing"
)

// The behavioural partition (TestReconcile_TheDoubtCoversExactlyFourOfTheTenHeaderFields)
// quantifies over HeaderFields, so an entry naming no header field is invisible there: it
// widens nothing and reds nothing, and the next reader takes it for a covered field. This is
// the set-equality pin the list did not have.
func TestReconcile_TheDoubtfulFieldsAreFourRealHeaderFields(t *testing.T) {
	// Compared as a set: uncorroborated reads it through slices.Contains, so the order carries
	// no meaning and pinning it would red on an edit that changes nothing.
	want := []string{"buyer_name", "buyer_tin", "total", "vat"}
	got := slices.Clone(doubtfulFields)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("doubtfulFields = %q, want the four %q", doubtfulFields, want)
	}
	for i, f := range doubtfulFields {
		if !slices.Contains(HeaderFields, f) {
			t.Errorf("doubtfulFields[%d] = %q, which HeaderFields does not name; uncorroborated reads Candidate.Field, so this entry can never match", i, f)
		}
		if slices.Contains(doubtfulFields[i+1:], f) {
			t.Errorf("doubtfulFields names %q twice", f)
		}
	}
}

// AC-3.9. currency is outside doubtfulFields: TWO arms. Arm 1 is the design pin above, restated
// here by name since AC-3.9 cites it directly. Arm 2 is behavioural -- a split Currency/USD
// pair (adjacent, TierGeneric head) beside a bare ₦ token must still decide USD, uncontested,
// because an uncorroborated-adjacent head only widens on a field this list names.
func TestReconcile_CurrencyIsOutsideTheDoubtScope(t *testing.T) {
	if slices.Contains(doubtfulFields, "currency") {
		t.Errorf("doubtfulFields names currency; a symbol-only candidate would then compete with an adjacent labelled head through the widened path")
	}
	if len(doubtfulFields) == 0 {
		t.Fatalf("doubtfulFields is empty; the absence asserted above holds over no list at all")
	}

	pages := []TokenPage{{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []Token{
		{Text: "Currency:", Region: Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.13}},
		{Text: "USD", Region: Region{Page: 1, X0: 0.22, Y0: 0.10, X1: 0.28, Y1: 0.13}},
		{Text: "₦500.00", Region: Region{Page: 1, X0: 0.10, Y0: 0.40, X1: 0.20, Y1: 0.43}},
	}}}

	cands := Resolve(pages, RuleSet{Tier1: Tier1Rules})
	sweep := false
	for _, c := range cands {
		if c.Field == "currency" && c.RuleID == "t1.currency.sweep" {
			sweep = true
		}
	}
	if !sweep {
		t.Fatalf("no t1.currency.sweep candidate on the ₦ token; the widening below has nothing to compete against")
	}

	result := decideField(cands, "currency")
	value := ""
	if result.Value != nil {
		value = *result.Value
	}
	if value != "USD" {
		t.Errorf("currency = %q, want %q", value, "USD")
	}
	if result.Reason != ReasonNone {
		t.Errorf("currency reason = %q, want %q", result.Reason, ReasonNone)
	}
	if len(result.Alternatives) != 0 {
		t.Errorf("currency alternatives = %v, want none", result.Alternatives)
	}
}
