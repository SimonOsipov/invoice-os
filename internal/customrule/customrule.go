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
