// This file (evaluators.go) is the M3-04-03 subtask: the five
// presence/shape rule-type Evaluators -- required, format/regex, enum,
// range, date -- from the story's "9 rule-type evaluators (contracts)"
// table. The three arithmetic/relational evaluators (tax_math, cross_field,
// conditional) land in M3-04-04 (evaluators_math.go); the cel escape hatch
// in M3-04-05 (cel.go). Registry assembly (wiring these structs into the
// map[RuleType]Evaluator NewEngine takes) is deferred to M3-04-08 -- these
// are unit-tested directly against their Eval method here, no registry
// needed for this subtask.
//
// Each evaluator resolves Rule.Target via resolvePath (engine.go, rooted at
// p["invoice"], NO "invoice." prefix -- Decision N19) and decodes
// Rule.Params (json.RawMessage) into its own typed params struct. Contract,
// verbatim from the story's evaluator table:
//   - Absent target + non-required type => pass (nil, nil); presence is
//     required's job, not the other four's. A present-but-JSON-null value is
//     treated the same as absent for the non-required types (nothing to
//     check) and as a violation for required (a null is not present).
//   - A present value that fails the type's check => a non-nil *Violation
//     carrying RuleKey=r.Key, Severity=r.Severity, Message=r.Message,
//     Path=r.Target (the resolved dotted path).
//   - Malformed/undecodable Params => a non-nil error (an engine/config
//     fault -- Decision N15: fail loud on a broken rule, NEVER a silent
//     pass), not a violation. Params are validated FIRST, before the
//     absent-target short-circuit, so a rule with broken config fails loud
//     even when the data happens to omit the target (N15 > absent=>pass). A
//     present-but-not-the-right-shape VALUE (e.g. range's target resolving
//     to a non-numeric string) is the opposite case: that's a violation, not
//     a config error -- the rule itself is fine, the DATA is bad.
package validation

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// violationOption customizes a violation() call to attach an Expected and/or
// Actual value (D9, INVCR-01-12). Only format/regex, enum, range, tax_math,
// and line_sum pass one; required/date/cross_field/conditional/cel call
// violation(r) bare, so their Violation's Expected/Actual stay nil ->
// omitempty-absent on the wire (rule.go's Violation doc comment) -- never a
// fabricated "".
type violationOption func(*Violation)

// withExpected sets Violation.Expected to the given value: an exact decimal
// string (decimal.Decimal.String(), never a float -- [D13]) for the
// arithmetic/numeric types, or the rule's own natural-language expectation
// (a regex pattern, an allowed-values list) for format/enum.
func withExpected(expected string) violationOption {
	return func(v *Violation) { v.Expected = &expected }
}

// withActual sets Violation.Actual to the given value. Mirrors withExpected.
func withActual(actual string) violationOption {
	return func(v *Violation) { v.Actual = &actual }
}

// violation builds the *Violation a failed rule returns: the rule's key +
// severity + message, plus the resolved target path and,
// via opts, this evaluation's Expected/Actual (D9).
func violation(r Rule, opts ...violationOption) *Violation {
	v := &Violation{
		RuleKey:  r.Key,
		Severity: r.Severity,
		Message:  r.Message,
		Path:     r.Target,
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// decodeParams unmarshals a rule's type-specific Params into dst. An empty
// Params is treated as an empty object so callers can decode optional-only
// param shapes without a nil-guard; a genuinely malformed body surfaces as a
// decode error (a config fault -- Decision N15).
func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	return json.Unmarshal(raw, dst)
}

// toFloat coerces a resolved JSON value to float64. It accepts the numeric
// shapes a decoded payload can carry (float64 from encoding/json, json.Number
// from a UseNumber decoder, and the native Go int/uint/float widths); every
// other value -- notably a string -- reports ok=false, which the range
// evaluator maps to a violation (bad DATA), not a config error.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// requiredEval implements the `required` rule type: params `{}` (optional
// `allow_blank` bool). Passes when the target is present (and, unless
// allow_blank, non-blank); violates when absent, null, or blank.
type requiredEval struct{}

func (requiredEval) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		AllowBlank bool `json:"allow_blank"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: required rule %q params: %w", r.Key, err)
	}

	val, present := resolvePath(p, r.Target)
	if !present || val == nil {
		// Absent or JSON null: presence is required's whole job -- violate.
		return violation(r), nil
	}
	// A blank string (empty or whitespace-only) is not "present" for
	// required's purposes unless the rule explicitly opts in via allow_blank.
	if s, ok := val.(string); ok && !params.AllowBlank && strings.TrimSpace(s) == "" {
		return violation(r), nil
	}
	return nil, nil
}

// formatEval implements the `format/regex` rule type: params `{pattern}`.
// Passes when the present target value matches pattern (compiled once per
// Eval call); violates when it does not match. Absent/null target => pass
// (presence is required's job). A missing/non-string pattern, or a pattern
// that fails to compile, is a config fault => error.
type formatEval struct{}

func (formatEval) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		Pattern *string `json:"pattern"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: format rule %q params: %w", r.Key, err)
	}
	if params.Pattern == nil {
		return nil, fmt.Errorf("validation: format rule %q missing pattern", r.Key)
	}
	re, err := regexp.Compile(*params.Pattern)
	if err != nil {
		return nil, fmt.Errorf("validation: format rule %q bad pattern: %w", r.Key, err)
	}

	val, present := resolvePath(p, r.Target)
	if !present || val == nil {
		return nil, nil
	}
	if re.MatchString(stringify(val)) {
		return nil, nil
	}
	// Expected = the pattern; no natural Actual (population table, AC-3) --
	// the failing value is already on the invoice payload at Path.
	return violation(r, withExpected(*params.Pattern)), nil
}

// enumEval implements the `enum` rule type: params `{values:[...]}`.
// Passes when the present target value is a member of values; violates
// when it is not. Absent/null target => pass. Missing/non-array values is a
// config fault => error.
type enumEval struct{}

func (enumEval) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		Values *[]any `json:"values"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: enum rule %q params: %w", r.Key, err)
	}
	if params.Values == nil {
		return nil, fmt.Errorf("validation: enum rule %q missing values", r.Key)
	}

	val, present := resolvePath(p, r.Target)
	if !present || val == nil {
		return nil, nil
	}
	for _, want := range *params.Values {
		if reflect.DeepEqual(want, val) {
			return nil, nil
		}
	}
	// Expected = the allowed values, joined " · " (enumLabel); Actual = the
	// resolved (non-matching) value, stringified the same way formatEval's
	// match target is (AC-3).
	labels := make([]string, len(*params.Values))
	for i, w := range *params.Values {
		labels[i] = stringify(w)
	}
	return violation(r, withExpected(strings.Join(labels, " · ")), withActual(stringify(val))), nil
}

// rangeEval implements the `range` rule type: params
// `{min?,max?,exclusive_min?,exclusive_max?}`. Passes when the present
// target value is numeric and within bounds; violates when it is outside
// bounds OR not numeric at all (a bad VALUE is a violation, not a config
// error -- see file-header contract). Absent/null target => pass. A
// non-numeric bound param is a config fault => error.
type rangeEval struct{}

func (rangeEval) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		Min          *float64 `json:"min"`
		Max          *float64 `json:"max"`
		ExclusiveMin *float64 `json:"exclusive_min"`
		ExclusiveMax *float64 `json:"exclusive_max"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: range rule %q params: %w", r.Key, err)
	}

	val, present := resolvePath(p, r.Target)
	if !present || val == nil {
		return nil, nil
	}
	f, ok := toFloat(val)
	if !ok {
		// Present but non-numeric DATA -> violation, not a config error.
		// No natural Expected/Actual: f never resolved to a comparable number.
		return violation(r), nil
	}
	// Expected names ALL of this rule's configured bounds (not just whichever
	// one fires below) -- AC-4's "whichever bounds exist" describes the
	// rule's whole valid range, e.g. a min+max rule reports both even when
	// only the min arm is violated. Actual is the resolved value, as an
	// exact decimal string ([D13]), never the raw float.
	expected := rangeBoundLabel(params.Min, params.ExclusiveMin, params.Max, params.ExclusiveMax)
	actual := decimal.NewFromFloat(f).String()
	switch {
	case params.Min != nil && f < *params.Min:
		return violation(r, withExpected(expected), withActual(actual)), nil
	case params.Max != nil && f > *params.Max:
		return violation(r, withExpected(expected), withActual(actual)), nil
	case params.ExclusiveMin != nil && f <= *params.ExclusiveMin:
		return violation(r, withExpected(expected), withActual(actual)), nil
	case params.ExclusiveMax != nil && f >= *params.ExclusiveMax:
		return violation(r, withExpected(expected), withActual(actual)), nil
	}
	return nil, nil
}

// rangeBoundLabel renders rangeEval's configured bounds as a single
// Expected string: whichever of min/exclusive_min/max/exclusive_max are
// set, in that order (lower bounds then upper bounds), each as "<op>
// <bound>" (an exact decimal string, never a raw float), joined by " · " --
// the same joiner enumEval uses for its allowed-values list (one separator
// convention, AC-4). min/max use inclusive operators (>=, <=);
// exclusive_min/exclusive_max use strict operators (>, <) -- no AC or
// seeded rule exercises the exclusive params (Stage 2 correction C1's
// seeded-params note), but rangeEval already evaluates them, so this
// generalizes the same "whichever bounds exist" rendering to all four
// rather than leaving two of rangeEval's four branches with no natural
// Expected at all (product-advisor consult, this subtask).
func rangeBoundLabel(min, exclusiveMin, max, exclusiveMax *float64) string {
	var parts []string
	if min != nil {
		parts = append(parts, ">= "+decimal.NewFromFloat(*min).String())
	}
	if exclusiveMin != nil {
		parts = append(parts, "> "+decimal.NewFromFloat(*exclusiveMin).String())
	}
	if max != nil {
		parts = append(parts, "<= "+decimal.NewFromFloat(*max).String())
	}
	if exclusiveMax != nil {
		parts = append(parts, "< "+decimal.NewFromFloat(*exclusiveMax).String())
	}
	return strings.Join(parts, " · ")
}

// dateEval implements the `date` rule type: params
// `{format?, not_before?, not_after?, relative_to?}`. Passes when the
// present target value parses (per format, default ISO date 2006-01-02) and
// falls within the temporal bounds; violates when unparseable or out of
// bounds. Absent/null target => pass. Comparison is at date (day) granularity
// in UTC so it never flakes on time-of-day. The literal "today" is supported
// for not_before/not_after (resolves to time.Now(), truncated to date). A
// bound string that is neither "today" nor parseable under the layout is a
// config fault => error.
type dateEval struct{}

func (dateEval) Eval(p Payload, r Rule) (*Violation, error) {
	var params struct {
		Format    string `json:"format"`
		NotBefore string `json:"not_before"`
		NotAfter  string `json:"not_after"`
		// RelativeTo is accepted for shape completeness but not yet
		// interpreted; the only relative token wired here is "today".
		RelativeTo string `json:"relative_to"`
	}
	if err := decodeParams(r.Params, &params); err != nil {
		return nil, fmt.Errorf("validation: date rule %q params: %w", r.Key, err)
	}
	layout := "2006-01-02"
	if params.Format != "" {
		layout = params.Format
	}

	// Resolve the temporal bound params FIRST, before the absent-target
	// short-circuit -- a not_before/not_after that is neither "today" nor
	// parseable under the layout is a config fault that must fail loud even
	// when the data omits the target (Decision N15: a broken ruleset must
	// not quietly validate everything). This mirrors formatEval compiling
	// its pattern before resolvePath. A bad `format` layout is surfaced here
	// too, via resolveDateBound parsing each bound under it.
	now := time.Now()
	var notBefore, notAfter *time.Time
	if params.NotBefore != "" {
		nb, err := resolveDateBound(params.NotBefore, layout, now)
		if err != nil {
			return nil, fmt.Errorf("validation: date rule %q not_before: %w", r.Key, err)
		}
		notBefore = &nb
	}
	if params.NotAfter != "" {
		na, err := resolveDateBound(params.NotAfter, layout, now)
		if err != nil {
			return nil, fmt.Errorf("validation: date rule %q not_after: %w", r.Key, err)
		}
		notAfter = &na
	}

	val, present := resolvePath(p, r.Target)
	if !present || val == nil {
		return nil, nil
	}
	s, ok := val.(string)
	if !ok {
		// A non-string value is not a parseable date -> violation.
		return violation(r), nil
	}
	parsed, err := time.Parse(layout, s)
	if err != nil {
		// Unparseable DATA -> violation, not a config error.
		return violation(r), nil
	}
	d := dateOnly(parsed)

	if notBefore != nil && d.Before(*notBefore) {
		return violation(r), nil
	}
	if notAfter != nil && d.After(*notAfter) {
		return violation(r), nil
	}
	return nil, nil
}

// stringify renders a resolved value for regex matching: a string is used
// verbatim, any other value via its default %v formatting.
func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// dateOnly truncates a time to its calendar date in UTC, so comparisons are
// pure day-vs-day (no time-of-day / timezone drift).
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// resolveDateBound resolves a not_before/not_after bound to a date: the
// literal "today" maps to now truncated to date; anything else is parsed
// with the rule's layout. A non-"today", unparseable bound is a config fault.
func resolveDateBound(s, layout string, now time.Time) (time.Time, error) {
	if s == "today" {
		return dateOnly(now), nil
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return time.Time{}, err
	}
	return dateOnly(t), nil
}
