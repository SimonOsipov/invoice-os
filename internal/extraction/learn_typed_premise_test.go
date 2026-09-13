package extraction_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// lpTotalRuleBody is the rule LearnRule derives from the amount token in
// fxLearnedTypedTotal -- pinned so a shape/relation regression reds here first.
const lpTotalRuleBody = `{"label":"(?i)\\bVAT\\b","relation":{"kind":"right","max_distance":0.13},"shape":"amount"}`

func lpFieldByName(t *testing.T, results []extraction.FieldResult, name string) extraction.FieldResult {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("field %s not found in results", name)
	return extraction.FieldResult{}
}

// AC-3: neither document's printed amount carries a label Tier-1's total shape reaches --
// the amount reads as vat, and total is left for a human. Floors invoice_number and currency
// so the fixture pair's identity can't drift under later edits.
func TestLearnedTypedTotal_Tier1LeavesTotalMissingOnBothDocuments(t *testing.T) {
	a := rvCorpusPages(t, fxLearnedTypedTotal)
	b := rvCorpusPages(t, fxLearnedTypedTotalTwin)
	tier1 := extraction.RuleSet{Tier1: extraction.Tier1Rules}

	ra := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(a, tier1)})
	rb := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(b, tier1)})

	for _, tc := range []struct {
		name    string
		results []extraction.FieldResult
	}{{"a", ra}, {"b", rb}} {
		total := lpFieldByName(t, tc.results, "total")
		if total.Value != nil {
			t.Errorf("%s: total.Value = %q, want nil", tc.name, *total.Value)
		}
		if total.Reason != extraction.ReasonMissing {
			t.Errorf("%s: total.Reason = %q, want %q", tc.name, total.Reason, extraction.ReasonMissing)
		}
	}

	invA := lpFieldByName(t, ra, "invoice_number")
	if invA.Value == nil || *invA.Value != "INV-1009" {
		t.Fatalf("a: invoice_number = %v, want INV-1009", invA.Value)
	}
	invB := lpFieldByName(t, rb, "invoice_number")
	if invB.Value == nil || *invB.Value != "INV-1010" {
		t.Fatalf("b: invoice_number = %v, want INV-1010", invB.Value)
	}

	curA := lpFieldByName(t, ra, "currency")
	if curA.Value == nil || *curA.Value != "NGN" {
		t.Fatalf("a: currency = %v, want NGN", curA.Value)
	}
	curB := lpFieldByName(t, rb, "currency")
	if curB.Value == nil || *curB.Value != "NGN" {
		t.Fatalf("b: currency = %v, want NGN", curB.Value)
	}
}

// AC-7: one layout, two suppliers -- the fingerprint is geometry-only and must not move when
// only the supplier name's text changes.
func TestLearnedTypedTotal_BothDocumentsShareOneLayoutFingerprint(t *testing.T) {
	a := rvCorpusPages(t, fxLearnedTypedTotal)
	b := rvCorpusPages(t, fxLearnedTypedTotalTwin)

	fpA := extraction.Fingerprint(a)
	fpB := extraction.Fingerprint(b)
	if fpA != fpB {
		t.Fatalf("Fingerprint(a) = %q, Fingerprint(b) = %q, want equal", fpA, fpB)
	}
	if !strings.HasPrefix(fpA, extraction.FingerprintVersion+":") {
		t.Errorf("Fingerprint(a) = %q, want prefix %q", fpA, extraction.FingerprintVersion+":")
	}

	tier1 := extraction.RuleSet{Tier1: extraction.Tier1Rules}
	ra := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(a, tier1)})
	rb := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(b, tier1)})

	supA := lpFieldByName(t, ra, "supplier_name")
	if supA.Value == nil || *supA.Value != "Adeyemi Trading Limited" {
		t.Fatalf("a: supplier_name = %v, want Adeyemi Trading Limited", supA.Value)
	}
	supB := lpFieldByName(t, rb, "supplier_name")
	if supB.Value == nil || *supB.Value != "Okafor Industries Limited" {
		t.Fatalf("b: supplier_name = %v, want Okafor Industries Limited", supB.Value)
	}
}

// AC-3: a rule learned from a's printed amount, applied to b's identical layout, fills the
// total b's own Tier-1 pass left missing -- and touches no other field's decision.
func TestLearnedTypedTotal_TheAmountTokenTeachesARuleTheTwinReads(t *testing.T) {
	a := rvCorpusPages(t, fxLearnedTypedTotal)
	b := rvCorpusPages(t, fxLearnedTypedTotalTwin)

	tok := rvTokenByText(t, a, "₦14,800,000.00")
	lr, ok := extraction.LearnRule("total", tok.Region, extraction.AnchorObservations(a))
	if !ok {
		t.Fatalf("LearnRule(total, tok.Region, AnchorObservations(a)) ok = false, want true")
	}
	if string(lr.Body) != lpTotalRuleBody {
		t.Errorf("lr.Body = %s, want %s", lr.Body, lpTotalRuleBody)
	}

	tier1 := extraction.RuleSet{Tier1: extraction.Tier1Rules}
	taught := extraction.RuleSet{
		Learned: []extraction.AnchorRule{{ID: "learned", Field: "total", Rule: lr.Rule}},
		Tier1:   extraction.Tier1Rules,
	}

	untaughtResults := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(b, tier1)})
	taughtResults := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(b, taught)})

	// Control: without the taught rule, b's total is still missing.
	untaughtTotal := lpFieldByName(t, untaughtResults, "total")
	if untaughtTotal.Value != nil {
		t.Fatalf("control: total.Value = %q, want nil", *untaughtTotal.Value)
	}
	if untaughtTotal.Reason != extraction.ReasonMissing {
		t.Fatalf("control: total.Reason = %q, want %q", untaughtTotal.Reason, extraction.ReasonMissing)
	}

	taughtTotal := lpFieldByName(t, taughtResults, "total")
	if taughtTotal.Value == nil || *taughtTotal.Value != "9250000.00" {
		t.Fatalf("taught: total.Value = %v, want 9250000.00", taughtTotal.Value)
	}
	if taughtTotal.Reason != "" {
		t.Fatalf("taught: total.Reason = %q, want \"\"", taughtTotal.Reason)
	}

	// The taught rule claims only total: every other field's decision is byte-identical.
	compared := 0
	for _, name := range extraction.HeaderFields {
		if name == "total" {
			continue
		}
		taughtField := lpFieldByName(t, taughtResults, name)
		untaughtField := lpFieldByName(t, untaughtResults, name)
		tb, err := json.Marshal(taughtField)
		if err != nil {
			t.Fatalf("json.Marshal(taught %s) error = %v", name, err)
		}
		ub, err := json.Marshal(untaughtField)
		if err != nil {
			t.Fatalf("json.Marshal(untaught %s) error = %v", name, err)
		}
		if string(tb) != string(ub) {
			t.Errorf("%s: taught = %s, untaught = %s, want equal", name, tb, ub)
		}
		compared++
	}
	if compared < 2 {
		t.Fatalf("compared %d fields besides total, want at least 2", compared)
	}
}

// AC-4, AC-5: the exact tokens subtask 02's refusal specs read, including PDFium's measured
// trailing spaces -- a trim anywhere upstream would make those specs pass for the wrong reason.
func TestLearnedTypedTotal_CarriesTheTokensTheRefusalSpecsNeed(t *testing.T) {
	a := rvCorpusPages(t, fxLearnedTypedTotal)

	if a[0].Number != 1 {
		t.Fatalf("a[0].Number = %d, want 1", a[0].Number)
	}

	for _, text := range []string{
		"Total ",
		"VAT inclusive ",
		"₦14,800,000.00",
		"Paid: ₦0.00",
		"Currency: NGN",
	} {
		rvTokenByText(t, a, text)
	}
}
