// tier1.go: the shipped generic rule set -- what a tenant's FIRST document resolves against,
// before anything is learned. Every rule must be built through ParseRule: a composite-literal
// Rule has no compiled matcher and Resolve silently emits nothing for it
// (TestTier1_EveryRuleHasACompiledMatcher).
package extraction

import "strings"

// Tier1Rule is a shipped generic rule. Band scopes it to part of a page and lives on this type
// only, so the persisted rule body and EXTR-14's contract are untouched.
type Tier1Rule struct {
	Key   string // stable, e.g. "t1.invoice_number.same_token"
	Field string
	Rule  Rule
	Band  PageBand // BandAnywhere unless the rule matches by format alone
}

// tier1RuleCount is the shipped set's size: three relations over each of the ten anchor-lexicon
// labels, plus the two banded TIN sweeps.
const tier1RuleCount = 32

// The set's only distance dials; nothing else reads a distance. Distance is the box GAP, so
// both are bounded on both sides: right must reach 0.2060 and must not reach
// 0.465497 (two_column's buyer column); below must reach 0.009111 and must not reach 0.107212
// (the next stacked group's value, with the buyer's name behind it at 0.321571). Both upper
// bounds used to be a bare LABEL, which since EXTR-16 is not a value; the corpus keeps no
// rightward merge at any width, so 0.465497 is measured on a synthetic page carrying
// two_column's own edges. TestTier1_DialsStayInsideTheirMeasuredWindow holds both.
//
// Each float is paired with its JSON spelling because strconv is outside this file's import
// allowlist. TestTier1_EveryRuleHasACompiledMatcher checks every shipped rule's parsed distance
// against the float, so the pair cannot drift.
const (
	tier1MaxDistanceRight     = 0.35
	tier1MaxDistanceRightJSON = "0.35"
	tier1MaxDistanceBelow     = 0.06
	tier1MaxDistanceBelowJSON = "0.06"
)

// tier1TINSweepLabel matches a bare TIN token whole, so the label IS the value
// (TestResolve_SameTokenKeepsALabelThatIsItsOwnValue). Banded, never swept page-wide: an
// unscoped sweep sits at Distance 0 and outranks the correct label-anchored candidate.
const tier1TINSweepLabel = `^\s*[0-9]{8}-[0-9]{4}\s*$`

// tier1MaxDistanceSameToken is unread by same_token, but ParseRule range-checks it regardless.
const tier1MaxDistanceSameTokenJSON = "0"

// tier1Specs pairs an anchorLexicon id with the field it fills and the shape that field takes.
// It follows the LEXICON's order, not HeaderFields' -- the two disagree on where buyer_tin
// sits, which is what lets TestResolve_ReturnsFieldsInVocabularyOrder tell rule order from
// vocabulary order (TestTier1_RulesAreNotInVocabularyOrder).
var tier1Specs = []struct {
	labelID, field string
	shape          Shape
}{
	{"invoice_no", "invoice_number", ShapeInvoiceNumber},
	{"issue_date", "issue_date", ShapeDate},
	{"supplier_tin", "supplier_tin", ShapeTIN},
	{"buyer_tin", "buyer_tin", ShapeTIN},
	{"supplier_name", "supplier_name", ShapeName},
	{"buyer_name", "buyer_name", ShapeName},
	{"currency", "currency", ShapeCurrency},
	{"subtotal", "subtotal", ShapeAmount},
	{"vat", "vat", ShapeAmount},
	{"total", "total", ShapeAmount},
}

// Tier1Rules is the shipped set, in anchor-lexicon order.
var Tier1Rules = buildTier1Rules()

// buildTier1Rules gives every field all three relations, not the two a field's usual layout
// needs: measured on the corpus, invoice_number, issue_date and total reach their value only
// through below on a stacked layout, and supplier_name only through same_token on an inline one.
func buildTier1Rules() []Tier1Rule {
	out := make([]Tier1Rule, 0, tier1RuleCount)
	for _, s := range tier1Specs {
		label := anchorPattern(s.labelID)
		out = append(out,
			mustTier1Rule("t1."+s.field+".same_token", s.field, label, RelSameToken, tier1MaxDistanceSameTokenJSON, s.shape, BandAnywhere),
			mustTier1Rule("t1."+s.field+".right", s.field, label, RelRight, tier1MaxDistanceRightJSON, s.shape, BandAnywhere),
			mustTier1Rule("t1."+s.field+".below", s.field, label, RelBelow, tier1MaxDistanceBelowJSON, s.shape, BandAnywhere),
		)
	}

	// The sweeps recognise a TIN by format alone, so only the band tells the supplier's from the
	// buyer's (TestTier1_TINSweepSeparatesSupplierFromBuyerByPageHalf).
	return append(out,
		mustTier1Rule("t1.supplier_tin.sweep", "supplier_tin", tier1TINSweepLabel, RelSameToken, tier1MaxDistanceSameTokenJSON, ShapeTIN, BandPage1Top),
		mustTier1Rule("t1.buyer_tin.sweep", "buyer_tin", tier1TINSweepLabel, RelSameToken, tier1MaxDistanceSameTokenJSON, ShapeTIN, BandPage1Bottom),
	)
}

// anchorPattern is the lexicon's own pattern for id, never a copy: a forked label drifts from
// the fingerprint silently (TestTier1_ReusesTheAnchorLexiconPatterns). A miss panics, because an
// empty Label compiles and then matches every token.
func anchorPattern(id string) string {
	for _, entry := range anchorLexicon {
		if entry.ID == id {
			return entry.Pattern
		}
	}
	panic("extraction: no anchorLexicon entry " + id)
}

// mustTier1Rule builds one rule through ParseRule and panics on failure: a rule that failed to
// build would resolve nothing and say nothing. maxDistance is the JSON spelling of the constant
// the caller means.
func mustTier1Rule(key, field, label string, kind RelationKind, maxDistance string, shape Shape, band PageBand) Tier1Rule {
	body := `{"label":` + jsonString(label) +
		`,"relation":{"kind":` + jsonString(string(kind)) +
		`,"max_distance":` + maxDistance +
		`},"shape":` + jsonString(string(shape)) + `}`

	rule, err := ParseRule([]byte(body))
	if err != nil {
		panic("extraction: tier-1 rule " + key + ": " + err.Error())
	}
	return Tier1Rule{Key: key, Field: field, Rule: rule, Band: band}
}

// jsonString quotes s as a JSON string: encoding/json is outside this file's import allowlist
// (TestResolve_ImportsOnlyPureStdlib), and a label pattern needs no escape beyond these two.
func jsonString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
