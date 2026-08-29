// tier1.go: the shipped generic rule set -- what a tenant's FIRST document resolves against,
// before anything is learned. Every rule must be built through ParseRule: a composite-literal
// Rule has no compiled matcher and Resolve silently emits nothing for it
// (TestTier1_EveryRuleHasACompiledMatcher).
package extraction

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

// The set's only distance dials. Measured widest label-to-value gap is 0.2060 across the
// corpus; the furthest a stacked label must reach its own group is 0.0267, and the next group's
// label is no closer than 0.087.
const (
	tier1MaxDistanceRight = 0.35
	tier1MaxDistanceBelow = 0.06
)

// tier1TINSweepLabel matches a bare TIN token whole, so the label IS the value
// (TestResolve_SameTokenKeepsALabelThatIsItsOwnValue). Banded, never swept page-wide: an
// unscoped sweep sits at Distance 0 and outranks the correct label-anchored candidate.
const tier1TINSweepLabel = `^\s*[0-9]{8}-[0-9]{4}\s*$`

// Tier1Rules is the shipped set, in anchor-lexicon order.
var Tier1Rules []Tier1Rule
