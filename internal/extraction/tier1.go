// tier1.go: the shipped generic rule type. Tier1Rules and its MaxDistance constants belong to
// the next subtask, which must not redeclare Tier1Rule.
package extraction

// Tier1Rule is a shipped generic rule. A struct, not a stored row, so nothing validates one at
// run time: Tier1Rules must build every Rule through ParseRule, because Resolve silently emits
// nothing for a rule with no compiled matcher (TestResolve_IgnoresAnUncompiledRule).
type Tier1Rule struct {
	Key   string // stable, e.g. "t1.invoice_number.same_token"
	Field string
	Rule  Rule
}
