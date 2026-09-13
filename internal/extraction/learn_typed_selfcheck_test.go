package extraction_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

type ltRow struct{ fixture, field, value, token string }

// ltRefusedAtTheSelfCheck asserts the verdict and that LearnRule derives over the same token,
// so the refusal is the self-check's, not TypedNotDerived.
func ltRefusedAtTheSelfCheck(t *testing.T, rows []ltRow) {
	t.Helper()
	for _, r := range rows {
		pages := rvCorpusPages(t, r.fixture)
		lr, v := ltLearn(t, pages, r.field, r.value)
		if v != extraction.TypedSelfCheckRefused {
			t.Errorf("%s %s %q: verdict = %d, want TypedSelfCheckRefused", r.fixture, r.field, r.value, v)
		}
		ltZero(t, r.fixture+" "+r.field, lr)
		if _, ok := extraction.LearnRule(r.field, rvTokenByText(t, pages, r.token).Region, extraction.AnchorObservations(pages)); !ok {
			t.Errorf("%s %s: LearnRule over %q refused, want derived", r.fixture, r.field, r.token)
		}
	}
}

func TestLearnTypedRule_TheSelfCheckRefusesEveryReachableMisfire(t *testing.T) {
	rows := []ltRow{
		{"corpus_split_labels.pdf", "total", "2150.00", "2,150.00"},
		{"corpus_totals_block.pdf", "total", "5375.00", "5,375.00"},
		{"rich_invoice.pdf", "total", "1612.50", "1,612.50"},
		{"corpus_split_labels.pdf", "buyer_name", "Honeywell Group", "Honeywell Group"},
		{"corpus_inline_labels.pdf", "buyer_name", "Honeywell Group", "Buyer: Honeywell Group"},
		{"corpus_two_column.pdf", "buyer_tin", "99999999-0402", "TIN: 99999999-0402"},
		{"advisory_register.pdf", "buyer_tin", "99999999-1312", "TIN 99999999-1312"},
	}
	if len(rows) != 7 {
		t.Fatalf("%d rows, want 7", len(rows))
	}
	ltRefusedAtTheSelfCheck(t, rows)
}

func TestLearnTypedRule_TheAcceptedCostRefusals(t *testing.T) {
	rows := []ltRow{
		{"corpus_inline_labels.pdf", "total", "1075.00", "Total: 1,075.00"},
		{"corpus_ambiguous_date.pdf", "issue_date", "2026-03-12", "Invoice Date: 12/03/2026"},
		{"corpus_ambiguous_date.pdf", "issue_date", "2026-12-03", "Invoice Date: 12/03/2026"},
		{"rich_invoice.pdf", "issue_date", "2026-03-12", "Issue Date: 12/03/2026"},
		{"rich_invoice.pdf", "issue_date", "2026-12-03", "Issue Date: 12/03/2026"},
	}
	if len(rows) != 5 {
		t.Fatalf("%d rows, want 5", len(rows))
	}
	ltRefusedAtTheSelfCheck(t, rows)

	inline := rvCorpusPages(t, "corpus_inline_labels.pdf")
	lr, ok := extraction.LearnRule("total", rvTokenByText(t, inline, "Total: 1,075.00").Region, extraction.AnchorObservations(inline))
	if !ok {
		t.Fatal("cost arm: LearnRule refused the inline total token")
	}
	rs := extraction.RuleSet{Learned: []extraction.AnchorRule{{ID: "cost", Field: "total", Rule: lr.Rule}}, Tier1: extraction.Tier1Rules}
	for _, fr := range extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(inline, rs)}) {
		if fr.Name != "total" {
			continue
		}
		if fr.Value == nil || *fr.Value != "1075.00" || fr.Reason != "" {
			t.Errorf("cost arm: total = %s reason %q, want 1075.00 reason \"\"", lpValue(fr.Value), fr.Reason)
		}
		return
	}
	t.Fatal("cost arm: no total field result")
}
