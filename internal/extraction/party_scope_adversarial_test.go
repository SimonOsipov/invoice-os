// party_scope_adversarial_test.go: the edges EXTR-22-02's own ACs leave open -- the two routing
// rules resolve.go states in prose and no spec asserted, and the boxless-learn ceiling the
// subtask shipped deliberately.
//
// External package: every spec here reaches only exported symbols. Helpers use a psX prefix.
package extraction_test

import (
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	psSupplierTIN = "99999999-0401"
	psBuyerTIN    = "99999999-0402"
)

// psByRule is every candidate a named rule produced for field.
func psByRule(got []extraction.Candidate, field, ruleID string) []string {
	var out []string
	for _, c := range got {
		if c.Field == field && c.RuleID == ruleID {
			out = append(out, c.Value)
		}
	}
	return out
}

// A below relation reads the ANCHOR's party, never the value's. resolve.go states this and
// nothing asserted it: the value can sit past a block boundary, and reading its party there
// would let it steal the other party's field.
//
// The sweep reaches the same token under its own party, so both assertions name a RuleID: a
// field-level check cannot tell t1.tin.below's routing from t1.tin.sweep's.
func TestResolve_APartyScopedRelationRoutesByTheAnchorNotTheValue(t *testing.T) {
	// The Buyer heading sits between anchor and value in TOKEN order while the value stays
	// directly below the anchor in geometry, which is the only shape that separates the two
	// readings.
	pages := []extraction.TokenPage{{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
		rvTok("Supplier", 0.10, 0.05, 0.30, 0.08),
		rvTok("TIN:", 0.10, 0.10, 0.30, 0.13),
		rvTok("Buyer", 0.60, 0.05, 0.80, 0.08),
		rvTok(psBuyerTIN, 0.10, 0.15, 0.30, 0.18),
	}}}

	got := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rvFloor(t, got, "a supplier-owned TIN label over a value in the buyer's block")

	// The floor: the value token really does sit in the buyer's block, or the routing below
	// would be asserted over a page with only one party on it.
	if v := psByRule(got, "buyer_tin", "t1.tin.sweep"); !slices.Contains(v, psBuyerTIN) {
		t.Fatalf("t1.tin.sweep gave buyer_tin = %v, want it to hold %s; the value token is not in the buyer's block and the routing below tests nothing", v, psBuyerTIN)
	}

	if v := psByRule(got, "supplier_tin", "t1.tin.below"); !slices.Contains(v, psBuyerTIN) {
		t.Errorf("t1.tin.below gave supplier_tin = %v, want it to hold %s; the anchor sits in the supplier's block and the anchor is what routes", v, psBuyerTIN)
	}
	if v := psByRule(got, "buyer_tin", "t1.tin.below"); len(v) != 0 {
		t.Errorf("t1.tin.below gave buyer_tin = %v, want none; routing by the VALUE's party lets a below read steal the other party's field", v)
	}
}

// A learned rule is never party-scoped: re-routing one by heading would overrule the reviewer
// who pointed at the field. resolve.go states this and nothing asserted it.
func TestResolve_ALearnedRuleKeepsItsFieldInsideTheOtherPartysBlock(t *testing.T) {
	const body = `{"label":"(?i)\\btin\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
	rule, err := extraction.ParseRule([]byte(body))
	if err != nil {
		t.Fatalf("ParseRule(%s): %v", body, err)
	}

	// Every token on this page belongs to the buyer, so a party-scoped reading of the learned
	// rule could only produce buyer_tin.
	pages := rvPage(
		rvTok("Buyer", 0.10, 0.05, 0.30, 0.08),
		rvTok("TIN: "+psBuyerTIN, 0.10, 0.10, 0.50, 0.13),
	)

	got := extraction.Resolve(pages, extraction.RuleSet{
		Learned: []extraction.AnchorRule{{ID: "ps-learned", Field: "supplier_tin", Rule: rule}},
		Tier1:   extraction.Tier1Rules,
	})
	rvFloor(t, got, "a supplier_tin learned rule over a token in the buyer's block")

	// The floor: Tier-1 does read this token as the buyer's, so the learned rule's field is a
	// choice and not the only reading available.
	generic := false
	for _, c := range got {
		if c.Tier == extraction.TierGeneric && c.Field == "buyer_tin" && c.Value == psBuyerTIN {
			generic = true
		}
	}
	if !generic {
		t.Fatalf("Tier-1 reached no buyer_tin %s on this page (%v); the token is not in the buyer's block and the assertion below tests nothing", psBuyerTIN, got)
	}

	var learned []extraction.Candidate
	for _, c := range got {
		if c.Tier == extraction.TierLearned {
			learned = append(learned, c)
		}
	}
	if len(learned) != 1 {
		t.Fatalf("the learned rule produced %d candidate(s) %v, want exactly 1", len(learned), rvValues(learned))
	}
	if learned[0].Field != "supplier_tin" || learned[0].Value != psBuyerTIN {
		t.Errorf("the learned candidate is %s = %q, want supplier_tin = %q -- a learned rule names its own field and no heading may re-route it",
			learned[0].Field, learned[0].Value, psBuyerTIN)
	}
}

// ceiling: LearnBoxlessRule refuses an inline party-bearing TIN, so that pointed correction
// teaches nothing on a DOCX. Revisit when the learn path applies anchorOutranked as Resolve
// does; this spec goes red on the fix and is the flag to delete then.
//
// Measured at db396656: the supplier spellings DID derive there and the buyer ones already did
// not, so the refusal below is this subtask's own and covers only the supplier class.
func TestLearnBoxlessRule_RefusesAnInlinePartyBearingTIN(t *testing.T) {
	const value = "12345678-0001"

	// The control first: the party-LESS spelling still derives, so each refusal below is the
	// party word's doing and not the fixture family's.
	const bare = "tin: " + value
	lr, ok := extraction.LearnBoxlessRule("supplier_tin", value, []string{bare})
	if !ok {
		t.Fatalf("control: LearnBoxlessRule(supplier_tin, %q, [%q]) ok = false; every refusal below would be about a family nothing derives from", value, bare)
	}
	const wantBody = `{"label":"(?i)\\btin\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
	if string(lr.Body) != wantBody {
		t.Fatalf("control: body = %s, want %s", lr.Body, wantBody)
	}

	for _, c := range []struct{ field, token string }{
		{"supplier_tin", "supplier tin: " + value},
		{"supplier_tin", "seller tin: " + value},
		{"supplier_tin", "vendor tax id: " + value},
		{"buyer_tin", "buyer tin: " + value},
		{"buyer_tin", "bill to tin: " + value},
	} {
		if got, ok := extraction.LearnBoxlessRule(c.field, value, []string{c.token}); ok {
			t.Errorf("LearnBoxlessRule(%s, %q, [%q]) derived %s; bare_tin claims a second, distinct body on this token and the pair is refused -- if this now derives, drop this spec with the fix",
				c.field, value, c.token, got.Body)
		}
	}
}
