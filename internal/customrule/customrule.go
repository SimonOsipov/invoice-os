// Package customrule stores a workspace's own validation rules as append-only revisions.
package customrule

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNotPermitted     = errors.New("customrule: only a workspace admin can change custom rules")
	ErrNotFound         = errors.New("customrule: custom rule not found")
	ErrCheckUnavailable = errors.New("customrule: custom rules cannot be checked right now")
)

// InvalidError is a 400: Msg is the answer body.
type InvalidError struct{ Msg string }

func (e InvalidError) Error() string { return e.Msg }

// typeRange is validation.TypeRange; customrule does not import validation.
const typeRange = "range"

const rangeBoundsMsg = "The minimum (min) cannot be greater than the maximum (max)."

// rangeBoundsError refuses a range rule whose min exceeds its max; other shapes are the check route's.
func rangeBoundsError(typ string, params json.RawMessage) error {
	if typ != typeRange {
		return nil
	}
	var b struct {
		Min *float64 `json:"min"`
		Max *float64 `json:"max"`
	}
	if json.Unmarshal(params, &b) != nil || b.Min == nil || b.Max == nil {
		return nil
	}
	if *b.Min > *b.Max {
		return InvalidError{rangeBoundsMsg}
	}
	return nil
}

// Key is the violation key of a rule: "custom:" + ruleID.
func Key(ruleID string) string { return "custom:" + ruleID }

// RuleInput is the POST and PUT body.
type RuleInput struct {
	EntityID *string         `json:"entity_id"`
	Type     string          `json:"type"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params"`
	Severity string          `json:"severity"`
	When     *string         `json:"when"`
	Message  string          `json:"message"`
	Enabled  bool            `json:"enabled"`
}

// CheckInput is what the validation service's check route receives.
type CheckInput struct {
	Type     string          `json:"type"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params"`
	Severity string          `json:"severity"`
	When     *string         `json:"when"`
	Message  string          `json:"message"`
}

// Rule is the latest revision of a custom rule.
type Rule struct {
	ID         string          `json:"id"`
	Key        string          `json:"key"`
	Revision   int             `json:"revision"`
	RevisionID string          `json:"revision_id"`
	EntityID   *string         `json:"entity_id"`
	Type       string          `json:"type"`
	Target     string          `json:"target"`
	Params     json.RawMessage `json:"params"`
	Severity   string          `json:"severity"`
	When       *string         `json:"when"`
	Message    string          `json:"message"`
	Enabled    bool            `json:"enabled"`
	UpdatedAt  time.Time       `json:"updated_at"`
	UpdatedBy  string          `json:"updated_by"`
}
