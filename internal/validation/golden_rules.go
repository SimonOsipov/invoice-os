package validation

// QA stub: types fix the wire contract, the bodies are not implemented. ENGI-15-01 replaces this file.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// GoldenRule is one enabled rule of the version in force, as a customer sees it.
type GoldenRule struct {
	Key      string          `json:"key"`
	Type     string          `json:"type"`
	Target   string          `json:"target"`
	Params   json.RawMessage `json:"params"`
	Severity string          `json:"severity"`
	When     *string         `json:"when"`
	Message  string          `json:"message"`
}

// GoldenVersion is one sealed, dated rule-set version in the rail.
type GoldenVersion struct {
	RuleSetVersionID uuid.UUID `json:"rule_set_version_id"`
	Version          int       `json:"version"`
	State            string    `json:"state"`
	EffectiveFrom    string    `json:"effective_from"`
	RuleCount        int       `json:"rule_count"`
}

// GoldenRules is the GET /v1/rules body.
type GoldenRules struct {
	Today            string          `json:"today"`
	Versions         []GoldenVersion `json:"versions"`
	RuleSetVersionID uuid.UUID       `json:"rule_set_version_id"`
	Version          int             `json:"version"`
	EffectiveFrom    string          `json:"effective_from"`
	Rules            []GoldenRule    `json:"rules"`
}

// GoldenRules is not implemented.
func (s *Store) GoldenRules(context.Context) (GoldenRules, error) {
	return GoldenRules{}, errors.New("validation: GoldenRules not implemented")
}

// GoldenRulesHandler is not implemented.
func GoldenRulesHandler(func(context.Context) (GoldenRules, error), *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, "not implemented")
	}
}
