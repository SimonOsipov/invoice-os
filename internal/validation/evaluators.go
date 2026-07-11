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
//     Path=r.Target (the resolved dotted path, for the M3-09 UI).
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
)

// violation builds the *Violation a failed rule returns: the rule's key +
// severity + message, plus the resolved target path (for the M3-09 UI).
func violation(r Rule) *Violation {
	return &Violation{
		RuleKey:  r.Key,
		Severity: r.Severity,
		Message:  r.Message,
		Path:     r.Target,
	}
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
	return violation(r), nil
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
	return violation(r), nil
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
		return violation(r), nil
	}
	switch {
	case params.Min != nil && f < *params.Min:
		return violation(r), nil
	case params.Max != nil && f > *params.Max:
		return violation(r), nil
	case params.ExclusiveMin != nil && f <= *params.ExclusiveMin:
		return violation(r), nil
	case params.ExclusiveMax != nil && f >= *params.ExclusiveMax:
		return violation(r), nil
	}
	return nil, nil
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

	now := time.Now()
	if params.NotBefore != "" {
		nb, err := resolveDateBound(params.NotBefore, layout, now)
		if err != nil {
			return nil, fmt.Errorf("validation: date rule %q not_before: %w", r.Key, err)
		}
		if d.Before(nb) {
			return violation(r), nil
		}
	}
	if params.NotAfter != "" {
		na, err := resolveDateBound(params.NotAfter, layout, now)
		if err != nil {
			return nil, fmt.Errorf("validation: date rule %q not_after: %w", r.Key, err)
		}
		if d.After(na) {
			return violation(r), nil
		}
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
