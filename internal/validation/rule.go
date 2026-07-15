// Package validation is the 04 Rules-as-Data Validation Engine context: a
// versioned, stateless engine that evaluates a submitted Nigerian invoice
// payload against an immutable published rule-set version and returns every
// applicable violation with a severity, a human-actionable message, and a
// rule key — stamped with the rule-set version used (story M3-04).
//
// This file (rule.go) is the domain-type contract for the engine core
// (subtask M3-04-02): the rule-row shape, the RuleSet/Result wire shapes,
// and the two interfaces (Evaluator, GuardFunc) the pipeline in engine.go
// dispatches through. It is pure Go — no DB import; schema_test.go's
// DB-backed suite (M3-04-01) exercises the migrated table shape directly
// over SQL and does not depend on these Go types.
//
// Concrete Evaluators (required/format/enum/range/tax_math/cross_field/
// conditional/date) land in M3-04-03/04; the CEL evaluator + guard backend
// in M3-04-05; the DB-backed Store (LoadActiveRuleSet/ToggleRule) in
// M3-04-06. This subtask's own tests (engine_test.go) exercise the pipeline
// with fake Evaluators/GuardFunc only.
package validation

import "encoding/json"

// RuleType is the rule-row "type" column value — one of the nine
// parameterized types plus the "cel" escape hatch (story Core AC #3). The
// DB CHECK constraint (M3-04-01 migration, extended for line_sum) enumerates
// the identical set.
type RuleType string

const (
	TypeRequired    RuleType = "required"
	TypeFormat      RuleType = "format/regex" // slash preserved verbatim -- NOT a path separator
	TypeEnum        RuleType = "enum"
	TypeRange       RuleType = "range"
	TypeTaxMath     RuleType = "tax_math"
	TypeCrossField  RuleType = "cross_field"
	TypeConditional RuleType = "conditional"
	TypeDate        RuleType = "date"
	TypeCEL         RuleType = "cel"
	// TypeLineSum aggregates a per-line-item amount (optionally weighted by a
	// per-item quantity) across a list and compares the total to a scalar
	// target with a tolerance — the one rule type that folds over a list
	// rather than resolving a single scalar path (evaluators_math.go's
	// lineSumEval). Added with the M3-05 seed's line-reconciliation rule.
	TypeLineSum RuleType = "line_sum"
)

// Severity is the rule-row "severity" column value: "error" | "warning" |
// "info" (DB CHECK constraint, M3-04-01). The engine treats it as an opaque
// passthrough -- collect-ALL semantics (story Core AC #4) mean severity
// never gates evaluation, it only travels through to Violation for the
// caller to interpret.
type Severity string

// Rule is one row of an active RuleSet -- the verbatim shape
// {key, type, target, params, severity, when, message, scope, enabled} the
// story's Constraints section pins, and that the M3-04-06 Store's
// LoadActiveRuleSet materializes from the `rules` table.
type Rule struct {
	Key    string          `json:"key"`
	Type   RuleType        `json:"type"`
	Target string          `json:"target"` // dotted path into the invoice payload, e.g. "supplier.tin" -- NO "invoice." prefix (Decision N19)
	Params json.RawMessage `json:"params"` // type-specific; decoded by the type's own Evaluator

	Severity Severity `json:"severity"`
	When     *string  `json:"when,omitempty"` // optional CEL guard (select-stage); nil => always applicable. CEL-rooted => MUST prefix "invoice." (Decision N19)
	Message  string   `json:"message"`
	Scope    string   `json:"scope"` // "document" only in v1 (Decision N10)
	Enabled  bool     `json:"enabled"`
}

// RuleSet is the engine's evaluation input: the active published version's
// number plus its rules, as returned by the (M3-04-06) Store's
// LoadActiveRuleSet. Published versions are immutable (story Core AC #1) --
// a content change always ships as a new RuleSet.Version.
type RuleSet struct {
	Version int
	Rules   []Rule
}

// Violation is one failed rule, in the exact JSON shape the M3-09
// playground UI and the /v1/validate response (story API spec) expect.
type Violation struct {
	RuleKey  string   `json:"rule_key"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Path     string   `json:"path,omitempty"` // resolved target, for the M3-09 UI
}

// Result is the /v1/validate response body: the rule-set version actually
// evaluated plus every collected violation (collect-ALL, never fail-fast --
// story Core AC #2, #4). Violations is never nil -- a clean payload
// marshals as "violations":[], not "violations":null.
type Result struct {
	RuleSetVersion int         `json:"rule_set_version"`
	Violations     []Violation `json:"violations"`
}

// Payload is the {"invoice": {...}} request body the engine evaluates
// against -- an arbitrary, ad-hoc JSON object (invoice persistence is M4+;
// v1 validates a submitted payload, not a stored row).
type Payload = map[string]any

// Evaluator runs one rule's check against a payload. A nil *Violation means
// the rule passed. A non-nil error is an engine/config fault (bad params,
// bad expression) -- surfaced as a 500, NEVER treated as a violation
// (Decision N15: fail loud on a broken rule, don't silently pass).
type Evaluator interface {
	Eval(p Payload, r Rule) (*Violation, error)
}

// GuardFunc evaluates a rule's optional `when` CEL guard at the select
// stage: true means the rule is applicable, false means it is skipped with
// no violation. Injected as a field on Engine (rather than hard-wired to
// the CEL backend) so the pipeline built in this subtask is unit-testable
// with a fake before the real CEL guard backend lands in M3-04-05.
type GuardFunc func(expr string, p Payload) (bool, error)
