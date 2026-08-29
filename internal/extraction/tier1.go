// tier1.go: the shipped generic rule type. Tier1Rules and its MaxDistance constants belong to
// the next subtask, which must not redeclare Tier1Rule.
package extraction

// Tier1Rule is a shipped generic rule. A struct, not a stored row: these are compiled in and a
// test proves every one parses, so a Tier-1 rule cannot fail validation at run time the way a
// stored row can.
type Tier1Rule struct {
	Key   string // stable, e.g. "t1.invoice_number.same_token"
	Field string
	Rule  Rule
}
