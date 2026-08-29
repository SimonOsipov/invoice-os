// anchor.go: the anchor rule -- a label to find, where the value sits relative to it, and the
// shape the value must take. ParseRule is the only constructor that compiles Label, so no path
// resolves against an uncompiled rule.
//
// One rule body, as stored in extraction_anchor_rules.rule. Field name, layout fingerprint and
// schema version are COLUMNS, not keys here, so nothing in the body can disagree with the row
// it sits in:
//
//	{
//	  "label":    "(?i)\\b(invoice|inv|bill|doc(ument)?)\\.?\\s*((no|num(ber)?)\\b|#)",
//	  "relation": { "kind": "same_token", "max_distance": 0.0 },
//	  "shape":    "invoice_number"
//	}
//
// label is an RE2 pattern of at most 512 bytes. relation.kind is one of "same_token", "right"
// or "below". relation.max_distance is in normalised page units, within [0,1], and is unread
// by "same_token". shape is one of "invoice_number", "date", "amount", "tin", "currency" or
// "name" -- a NAME, never a caller-supplied pattern, because a generated value regexp would be
// an unreviewed parser.
package extraction

import (
	"errors"
	"regexp"
)

// maxRuleLabelBytes caps Label so a pathological pattern cannot inflate compile cost.
const maxRuleLabelBytes = 512

// RuleSchemaVersion is the JSON shape's version, persisted per row. ParseRule never reads it --
// the version is a COLUMN (rule_schema_version); the unknown-version error belongs to
// EXTR-04-05's AnchorRulesFor.
const RuleSchemaVersion = 1

// Rule is one anchor rule: a label to find, where the value sits relative to it, and the
// shape the value must take.
type Rule struct {
	Label    string   `json:"label"`
	Relation Relation `json:"relation"`
	Shape    Shape    `json:"shape"`

	// re is the compiled Label. Unexported and set by ParseRule, so no path can resolve
	// against an uncompiled rule.
	re *regexp.Regexp
}

// Relation is where the value sits relative to its label.
type Relation struct {
	Kind        RelationKind `json:"kind"`
	MaxDistance float64      `json:"max_distance"` // normalised page units; ignored by same_token
}

type RelationKind string

const (
	// RelSameToken: the value is the remainder of the anchor token itself, after the label
	// match and after leading ":", "-", en/em dash and whitespace are trimmed.
	RelSameToken RelationKind = "same_token"
	// RelRight: a later token on the same line band, to the right of the anchor.
	RelRight RelationKind = "right"
	// RelBelow: a token in the same column band, below the anchor.
	RelBelow RelationKind = "below"
)

// Shape names a value normaliser. A NAME, never a caller-supplied pattern: EXTR-14 generates
// rules from clicks, and a generated value regexp would be an unreviewed parser.
type Shape string

const (
	ShapeInvoiceNumber Shape = "invoice_number"
	ShapeDate          Shape = "date"
	ShapeAmount        Shape = "amount"
	ShapeTIN           Shape = "tin"
	ShapeCurrency      Shape = "currency"
	ShapeName          Shape = "name"
)

// ParseRule decodes and validates one rule body, compiling Label. Every failure is an error:
// an unknown kind, an unknown shape, a MaxDistance outside [0,1], a Label over
// maxRuleLabelBytes, or a Label RE2 refuses.
func ParseRule(raw []byte) (Rule, error) {
	return Rule{}, errors.New("ParseRule: not implemented")
}

// anchorLexicon maps a canonical label id to the pattern that recognises it. Ordered, never
// ranged as a map: iteration order is fingerprint input.
var anchorLexicon = []struct{ ID, Pattern string }{}
