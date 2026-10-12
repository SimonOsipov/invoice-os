// GET /v1/rules: the golden set a customer's rules screen shows. Read in the request tenant's transaction.
package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
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

// GoldenRules reads the version in force and the rail of dated versions as of today (UTC).
func (s *Store) GoldenRules(ctx context.Context) (GoldenRules, error) {
	return s.goldenRulesAsOf(ctx, time.Now().UTC())
}

// goldenRulesAsOf takes today as an argument so a test can pin it.
func (s *Store) goldenRulesAsOf(ctx context.Context, asOf time.Time) (GoldenRules, error) {
	today := asOf.UTC().Format(time.DateOnly)
	out := GoldenRules{Today: today, Versions: []GoldenVersion{}, Rules: []GoldenRule{}}
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// State rule of staff_versions.go, minus draft and retired.
		rows, err := tx.Query(ctx, `
WITH t AS (SELECT $1::date AS today, rule_set_version_for($1::date) AS in_force)
SELECT v.id, v.version,
       CASE WHEN v.id = t.in_force THEN 'in_force'
            WHEN v.effective_from > t.today THEN 'scheduled'
            ELSE 'superseded' END,
       to_char(v.effective_from, 'YYYY-MM-DD'),
       (SELECT count(*) FROM rules r WHERE r.rule_set_version_id = v.id AND r.enabled)::int
  FROM rule_set_versions v, t
 WHERE v.sealed AND v.effective_from IS NOT NULL
 ORDER BY v.version DESC`, today)
		if err != nil {
			return fmt.Errorf("validation: read golden versions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var v GoldenVersion
			if err := rows.Scan(&v.RuleSetVersionID, &v.Version, &v.State, &v.EffectiveFrom, &v.RuleCount); err != nil {
				return fmt.Errorf("validation: scan golden version: %w", err)
			}
			if v.State == "in_force" {
				out.RuleSetVersionID, out.Version, out.EffectiveFrom = v.RuleSetVersionID, v.Version, v.EffectiveFrom
			}
			out.Versions = append(out.Versions, v)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("validation: read golden versions: %w", err)
		}
		if out.Version == 0 {
			return ErrNoActiveRuleSet
		}
		rr, err := tx.Query(ctx, `
SELECT key, type, target, params, severity, "when", message
  FROM rules
 WHERE rule_set_version_id = $1 AND enabled
 ORDER BY key`, out.RuleSetVersionID)
		if err != nil {
			return fmt.Errorf("validation: read golden rules: %w", err)
		}
		defer rr.Close()
		for rr.Next() {
			var g GoldenRule
			if err := rr.Scan(&g.Key, &g.Type, &g.Target, &g.Params, &g.Severity, &g.When, &g.Message); err != nil {
				return fmt.Errorf("validation: scan golden rule: %w", err)
			}
			out.Rules = append(out.Rules, g)
		}
		if err := rr.Err(); err != nil {
			return fmt.Errorf("validation: read golden rules: %w", err)
		}
		return nil
	})
	if err != nil {
		return GoldenRules{}, err
	}
	return out, nil
}

// GoldenRulesHandler serves GET /v1/rules.
func GoldenRulesHandler(read func(context.Context) (GoldenRules, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.IdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		g, err := read(r.Context())
		if err != nil {
			status, msg := statusForErr(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "validation: golden rules", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, g)
	}
}
