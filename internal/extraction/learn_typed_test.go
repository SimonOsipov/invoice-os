package extraction_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const ltInvoiceDateRightBody = `{"label":"(?i)\\bInvoice Date\\b","relation":{"kind":"right","max_distance":0.14},"shape":"date"}`

func ltLearn(t *testing.T, pages []extraction.TokenPage, field, value string) (extraction.LearnedRule, extraction.TypedVerdict) {
	t.Helper()
	if pages[0].Number != 1 {
		t.Fatalf("pages[0].Number = %d, want 1", pages[0].Number)
	}
	return extraction.LearnTypedRule(field, value, pages[0], extraction.AnchorObservations(pages))
}

func ltZero(t *testing.T, what string, lr extraction.LearnedRule) {
	t.Helper()
	if lr.Field != "" || lr.Body != nil || lr.Anchor != (extraction.AnchorObservation{}) {
		t.Errorf("%s: rule = {Field:%q Body:%s}, want the zero LearnedRule", what, lr.Field, lr.Body)
	}
}

func TestLearnTypedRule_FindsThePrintedNairaAmountThroughTheShape(t *testing.T) {
	pages := rvCorpusPages(t, fxLearnedTypedTotal)
	lr, v := ltLearn(t, pages, "total", "14800000.00")
	if v != extraction.TypedLearned {
		t.Fatalf("verdict = %d, want TypedLearned", v)
	}
	pointed, ok := extraction.LearnRule("total", rvTokenByText(t, pages, "₦14,800,000.00").Region, extraction.AnchorObservations(pages))
	if !ok || string(pointed.Body) != lpTotalRuleBody {
		t.Fatalf("pointed ok=%v body=%s, want %s", ok, pointed.Body, lpTotalRuleBody)
	}
	if string(lr.Body) != string(pointed.Body) {
		t.Errorf("typed body = %s, want the pointed %s", lr.Body, pointed.Body)
	}
}

func TestLearnTypedRule_NormalisesTheTypedSideToo(t *testing.T) {
	pages := rvCorpusPages(t, fxLearnedTypedTotal)
	for _, value := range []string{"NGN 14,800,000.00", "₦14,800,000.00"} {
		lr, v := ltLearn(t, pages, "total", value)
		if v != extraction.TypedLearned || string(lr.Body) != lpTotalRuleBody {
			t.Errorf("%q: verdict = %d body = %s, want TypedLearned %s", value, v, lr.Body, lpTotalRuleBody)
		}
	}
}

func TestLearnTypedRule_ReadsTheRemainderAfterALabel(t *testing.T) {
	two := rvCorpusPages(t, fxLearnedTwoParty)
	rvTokenByText(t, two, "Total: NGN 3,225.00")
	lr, v := ltLearn(t, two, "total", "3225.00")
	const body = `{"label":"(?i)\\bTotal\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"amount"}`
	if v != extraction.TypedLearned || string(lr.Body) != body {
		t.Errorf("learned_two_party total: verdict = %d body = %s, want TypedLearned %s", v, lr.Body, body)
	}

	col := rvCorpusPages(t, "corpus_two_column.pdf")
	rvTokenByText(t, col, "TIN: 99999999-0402")
	lr, v = ltLearn(t, col, "buyer_tin", "99999999-0402")
	if v != extraction.TypedSelfCheckRefused {
		t.Errorf("corpus_two_column buyer_tin: verdict = %d, want TypedSelfCheckRefused", v)
	}
	ltZero(t, "corpus_two_column buyer_tin", lr)
}

func TestLearnTypedRule_AddsNoToleranceTheShapeLacks(t *testing.T) {
	fx := rvCorpusPages(t, fxLearnedTypedTotal)
	two := rvCorpusPages(t, fxLearnedTwoParty)
	for _, c := range []struct {
		name  string
		pages []extraction.TokenPage
		value string
	}{
		{"fixture no fraction", fx, "14800000"},
		{"fixture grouped no fraction", fx, "14,800,000"},
		{"two_party no fraction", two, "3225"},
	} {
		lr, v := ltLearn(t, c.pages, "total", c.value)
		if v != extraction.TypedNoToken {
			t.Errorf("%s %q: verdict = %d, want TypedNoToken", c.name, c.value, v)
		}
		ltZero(t, c.name, lr)
	}
	for _, c := range []struct {
		name  string
		pages []extraction.TokenPage
		value string
	}{{"fixture", fx, "14800000.00"}, {"two_party", two, "3225.00"}} {
		if _, v := ltLearn(t, c.pages, "total", c.value); v != extraction.TypedLearned {
			t.Errorf("control %s %q: verdict = %d, want TypedLearned", c.name, c.value, v)
		}
	}
}

func TestLearnTypedRule_RefusesAValuePrintedOnSeveralTokens(t *testing.T) {
	fx := rvCorpusPages(t, fxLearnedTypedTotal)
	lr, v := ltLearn(t, fx, "currency", "NGN")
	if v != extraction.TypedSeveralTokens {
		t.Errorf("(a) fixture currency: verdict = %d, want TypedSeveralTokens", v)
	}
	ltZero(t, "(a)", lr)

	first := rvTok("Total: 4,300.00", 0.10, 0.30, 0.35, 0.33)
	middle := rvTok("Subtotal note", 0.10, 0.40, 0.30, 0.43)
	last := rvTok("4,300.00", 0.60, 0.50, 0.75, 0.53)
	lr, v = ltLearn(t, rvPage(first, middle, last), "total", "4300.00")
	if v != extraction.TypedSeveralTokens {
		t.Errorf("(b) synthetic: verdict = %d, want TypedSeveralTokens", v)
	}
	ltZero(t, "(b)", lr)
	if _, v := ltLearn(t, rvPage(first, middle), "total", "4300.00"); v != extraction.TypedLearned {
		t.Errorf("(b) control without the last token: verdict = %d, want TypedLearned", v)
	}

	inline := rvCorpusPages(t, rvCorpusInline)
	tok := rvTokenByText(t, inline, "Sub-total: 1,000.00")
	hits := 0
	for _, o := range extraction.AnchorObservations(inline) {
		if o.X0 == tok.Region.X0 && o.Y0 == tok.Region.Y0 && o.X1 == tok.Region.X1 && o.Y1 == tok.Region.Y1 {
			hits++
		}
	}
	if hits < 2 {
		t.Fatalf("(c) premise: %d lexicon hit(s) on %q, want at least 2", hits, tok.Text)
	}
	if _, v := ltLearn(t, inline, "subtotal", "1000.00"); v != extraction.TypedLearned {
		t.Errorf("(c) one token read through two labels: verdict = %d, want TypedLearned", v)
	}
}

func TestLearnTypedRule_RefusesAValueNoTokenCarries(t *testing.T) {
	fx := rvCorpusPages(t, fxLearnedTypedTotal)
	for _, value := range []string{"14800001.00", "abc"} {
		lr, v := ltLearn(t, fx, "total", value)
		if v != extraction.TypedNoToken {
			t.Errorf("%q: verdict = %d, want TypedNoToken", value, v)
		}
		ltZero(t, value, lr)
	}
	if _, v := ltLearn(t, fx, "total", "14800000.00"); v != extraction.TypedLearned {
		t.Errorf("control 14800000.00: verdict = %d, want TypedLearned", v)
	}
}

func TestLearnTypedRule_RefusesWhenNoAnchorRelatesToTheToken(t *testing.T) {
	page := rvPage(rvTok("4,300.00", 0.5, 0.5, 0.6, 0.52))[0]
	lr, v := extraction.LearnTypedRule("total", "4300.00", page, nil)
	if v != extraction.TypedNotDerived {
		t.Errorf("verdict = %d, want TypedNotDerived", v)
	}
	ltZero(t, "no anchors", lr)
}

func TestLearnTypedRule_TheZeroVerdictRefuses(t *testing.T) {
	var v extraction.TypedVerdict
	if v != extraction.TypedNoToken || v == extraction.TypedLearned {
		t.Errorf("zero verdict = %d, want TypedNoToken and never TypedLearned", v)
	}
}

func TestLearnTypedRule_WhereTheSelfCheckPassesItTeachesThePointedRule(t *testing.T) {
	rows := []struct {
		fixture, field, value, token, body string
	}{
		{fxLearnedTypedTotal, "total", "14800000.00", "₦14,800,000.00", lpTotalRuleBody},
		{fxLearnedTwoParty, "buyer_tin", "99999999-0702", "99999999-0702", lcBuyerRuleBody},
		{"corpus_split_labels.pdf", "issue_date", "2026-04-15", "15/04/2026", ltInvoiceDateRightBody},
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range rows {
		pages := rvCorpusPages(t, r.fixture)
		lr, v := ltLearn(t, pages, r.field, r.value)
		if v != extraction.TypedLearned {
			t.Errorf("%s %s: verdict = %d, want TypedLearned", r.fixture, r.field, v)
			continue
		}
		pointed, ok := extraction.LearnRule(r.field, rvTokenByText(t, pages, r.token).Region, extraction.AnchorObservations(pages))
		if !ok || string(pointed.Body) != r.body {
			t.Errorf("%s %s: pointed ok=%v body=%s, want %s", r.fixture, r.field, ok, pointed.Body, r.body)
		}
		if string(lr.Body) != r.body {
			t.Errorf("%s %s: typed body = %s, want %s", r.fixture, r.field, lr.Body, r.body)
		}
	}
}
