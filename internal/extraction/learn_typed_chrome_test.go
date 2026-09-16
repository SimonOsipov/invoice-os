// learn_typed_chrome_test.go: a typed correction only learns from the merged Chrome print --
// pre-merge, per-glyph rects give LearnTypedRule nothing to find. No DB; helpers use a cht*
// prefix.
package extraction_test

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	chtField    = "buyer_name"
	chtValue    = "Honeywell Group Nigeria Plc"
	chtRuleBody = `{"label":"(?i)\\bBILLED TO\\b","relation":{"kind":"below","max_distance":0.02},"shape":"name"}`
)

// chtPreMergePage1 rebuilds page 1 from the raw, un-merged rects -- one token per glyph, the
// reader's shape before the word stage.
func chtPreMergePage1(t *testing.T, name string) extraction.TokenPage {
	t.Helper()
	var page1 []extraction.Token
	for _, tok := range pdwPreMergeTokens(t, name) {
		if tok.Region.Page == 1 {
			page1 = append(page1, tok)
		}
	}
	return extraction.TokenPage{Number: 1, Tokens: page1}
}

// AC-1 + AC-2: the word stage is what makes the layout learnable at all -- 1 usable anchor and
// no token carrying the typed value before it, 14 anchors and a derived rule after.
func TestChromeRegister_TheWordStageMakesItsLayoutLearnable(t *testing.T) {
	post := rvCorpusPages(t, chrRegister)
	postAnchors := extraction.AnchorObservations(post)
	if len(postAnchors) != 14 {
		t.Fatalf("post-merge anchor count = %d, want 14", len(postAnchors))
	}
	lr, v := ltLearn(t, post, chtField, chtValue)
	if v != extraction.TypedLearned {
		t.Fatalf("post-merge LearnTypedRule(%s, %q) verdict = %d, want TypedLearned", chtField, chtValue, v)
	}
	if string(lr.Body) != chtRuleBody {
		t.Errorf("post-merge derived body = %s, want %s", lr.Body, chtRuleBody)
	}

	pre := chtPreMergePage1(t, chrRegister)
	preAnchors := extraction.AnchorObservations([]extraction.TokenPage{pre})
	if len(preAnchors) != 1 {
		t.Fatalf("pre-merge anchor count = %d, want 1", len(preAnchors))
	}
	if _, v := extraction.LearnTypedRule(chtField, chtValue, pre, preAnchors); v != extraction.TypedNoToken {
		t.Errorf("pre-merge LearnTypedRule(%s, %q) verdict = %d, want TypedNoToken", chtField, chtValue, v)
	}
}

// AC-1 + AC-5: every refusal names its own clause, never a bare ok/nil check. A far-right amount
// sits past capDistance(RelRight), so no amount field on this page is derivable.
func TestChromeRegister_EachTypedRefusalNamesItsClause(t *testing.T) {
	pages := rvCorpusPages(t, chrRegister)
	for _, c := range []struct {
		field, value string
		want         extraction.TypedVerdict
	}{
		{"total", "14430000.00", extraction.TypedNotDerived},
		{"total", "99999999.00", extraction.TypedNoToken},
		{chtField, "4th Floor, Alfred Rewane Road", extraction.TypedSelfCheckRefused},
	} {
		lr, v := ltLearn(t, pages, c.field, c.value)
		if v != c.want {
			t.Errorf("%s %q: verdict = %d, want %d", c.field, c.value, v, c.want)
		}
		ltZero(t, c.field+" "+c.value, lr)
	}
}
