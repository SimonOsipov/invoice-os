// resolve.go: rule application. Every candidate every rule produced, grouped in HeaderFields
// order and ordered within a field, never a nil slice. Pure -- no database, no clock, no
// network, no goroutine, and no map on the path (resolve_internal_test.go scans for all four).
package extraction

// Tier is which rule produced a candidate. An ordering input, never a displayed confidence.
type Tier int

const (
	TierLearned Tier = iota // a stored rule for this layout fingerprint
	TierGeneric             // a shipped Tier-1 rule
)

// Candidate is one possible value for one field. Not a Field: law E07 makes Field.Name unique
// within a result, and a field may have more than one answer.
type Candidate struct {
	Field    string  // a HeaderFields member
	Value    string  // non-empty, already normalised by its Shape
	Region   *Region // nil when the source token carried no usable box
	Reason   Reason  // always ReasonNone here; the doubt pass fills the slot
	RuleID   string  // AnchorRule.ID or Tier1Rule.Key
	Tier     Tier
	Distance float64 // gap along the relation's axis, normalised; 0 for same_token
}

// RuleSet is what Resolve reads. Neither slice is ever a map: iteration order is output order.
type RuleSet struct {
	Learned []AnchorRule
	Tier1   []Tier1Rule
}

// maxCandidatesPerField bounds a pathological document. It truncates AFTER ordering, so it is
// a deterministic cut and never a pick between two plausible values.
const maxCandidatesPerField = 8

// Resolve returns every candidate every rule produced. It makes no decision: a field with two
// plausible values keeps both, and a field with none is simply absent.
func Resolve(pages []TokenPage, rules RuleSet) []Candidate {
	return nil // stub
}

// compareCandidates is the total order within one field: tier, distance, region, value, rule id.
func compareCandidates(a, b Candidate) int {
	return 0 // stub
}
