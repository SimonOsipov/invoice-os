// Staff rules routes: GET /v1/staff/rules and PATCH /v1/staff/rules/{key}. The platform
// admits only a rules-role caller (auth.RequireRulesRole); the handlers repeat no role check.
package validation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	maxSwitchBodyBytes = 8 << 10
	maxReasonRunes     = 500
)

var (
	// ErrRuleNotInForce: no such key in the version in force, or no version in force.
	ErrRuleNotInForce = errors.New("validation: no such rule in the version in force")
	// ErrRuleAlreadyInState: the rule already has the requested state.
	ErrRuleAlreadyInState = errors.New("validation: rule already in the requested state")
)

// recordStaff is a var so a test can force an audit failure.
var recordStaff = audit.RecordStaff

// StaffRule is one rule on the GET /v1/staff/rules wire.
type StaffRule struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Target   string `json:"target"`
	Severity string `json:"severity"`
	Scope    string `json:"scope"`
	Message  string `json:"message"`
	Enabled  bool   `json:"enabled"`
}

// InForceRules is the GET /v1/staff/rules body.
type InForceRules struct {
	RuleSetVersionID uuid.UUID   `json:"rule_set_version_id"`
	Version          int         `json:"version"`
	Rules            []StaffRule `json:"rules"`
}

// SwitchResult is the PATCH /v1/staff/rules/{key} body.
type SwitchResult struct {
	Key              string    `json:"key"`
	Enabled          bool      `json:"enabled"`
	RuleSetVersion   int       `json:"rule_set_version"`
	RuleSetVersionID uuid.UUID `json:"rule_set_version_id"`
}

// RulesInForce returns the version in force today and its rules ordered by key.
func (s *Store) RulesInForce(ctx context.Context) (InForceRules, error) {
	var out InForceRules
	err := db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT v.id, v.version FROM rule_set_versions v WHERE v.id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date)`,
		).Scan(&out.RuleSetVersionID, &out.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoActiveRuleSet
		}
		if err != nil {
			return fmt.Errorf("validation: read version in force: %w", err)
		}
		rows, err := tx.Query(ctx,
			`SELECT key, type, target, severity, scope, message, enabled FROM rules WHERE rule_set_version_id = $1 ORDER BY key`,
			out.RuleSetVersionID)
		if err != nil {
			return fmt.Errorf("validation: read rules in force: %w", err)
		}
		defer rows.Close()
		out.Rules = []StaffRule{}
		for rows.Next() {
			var r StaffRule
			if err := rows.Scan(&r.Key, &r.Type, &r.Target, &r.Severity, &r.Scope, &r.Message, &r.Enabled); err != nil {
				return fmt.Errorf("validation: scan rule: %w", err)
			}
			out.Rules = append(out.Rules, r)
		}
		return rows.Err()
	})
	if err != nil {
		return InForceRules{}, err
	}
	return out, nil
}

// SwitchRule flips key in the version in force and records the staff audit row in one tx.
// A same-state request or a failed audit write rolls the tx back.
func (s *Store) SwitchRule(ctx context.Context, key string, enabled bool, reason string) (SwitchResult, error) {
	id, ok := auth.StaffFromContext(ctx)
	if !ok {
		return SwitchResult{}, db.ErrNotStaff
	}
	actor, err := uuid.Parse(id.Subject)
	if err != nil {
		return SwitchResult{}, db.ErrNotStaff
	}
	event := "validation.rule.disabled"
	if enabled {
		event = "validation.rule.enabled"
	}
	res := SwitchResult{Key: key, Enabled: enabled}
	err = db.WithinStaffTx(ctx, s.pool, func(tx pgx.Tx) error {
		var was bool
		err := tx.QueryRow(ctx,
			`SELECT rule_set_version_id, version, was_enabled FROM public.set_rule_enabled($1, $2, $3)`,
			actor, key, enabled,
		).Scan(&res.RuleSetVersionID, &res.RuleSetVersion, &was)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRuleNotInForce
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42501" {
			return db.ErrNotStaff
		}
		if err != nil {
			return fmt.Errorf("validation: switch rule: %w", err)
		}
		if was == enabled {
			return ErrRuleAlreadyInState
		}
		return recordStaff(ctx, tx, actor, res.RuleSetVersionID, event, map[string]any{
			"key": key, "enabled": enabled, "version": res.RuleSetVersion, "reason": reason,
		})
	})
	if err != nil {
		return SwitchResult{}, err
	}
	return res, nil
}

func staffRulesError(err error) (int, string) {
	switch {
	case errors.Is(err, db.ErrNotStaff):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, ErrNoActiveRuleSet):
		return http.StatusServiceUnavailable, "no rule set in force"
	case errors.Is(err, ErrRuleNotInForce):
		return http.StatusNotFound, "no such rule in the version in force"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// StaffListRulesHandler serves GET /v1/staff/rules.
func StaffListRulesHandler(list func(ctx context.Context) (InForceRules, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		rules, err := list(r.Context())
		if err != nil {
			status, msg := staffRulesError(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "validation: staff list rules", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, rules)
	}
}

type switchRuleRequest struct {
	Enabled *bool  `json:"enabled"`
	Reason  string `json:"reason"`
}

// StaffSwitchRuleHandler serves PATCH /v1/staff/rules/{key}.
func StaffSwitchRuleHandler(sw func(ctx context.Context, key string, enabled bool, reason string) (SwitchResult, error), log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxSwitchBodyBytes)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var req switchRuleRequest
		err := dec.Decode(&req)
		if err == nil {
			// A second value, or an oversize tail, must not slip through.
			if err = dec.Decode(&struct{}{}); errors.Is(err, io.EOF) {
				err = nil
			} else if err == nil {
				err = errors.New("trailing data")
			}
		}
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Enabled == nil {
			writeError(w, http.StatusBadRequest, "enabled is required")
			return
		}
		reason := strings.TrimSpace(req.Reason)
		if n := utf8.RuneCountInString(reason); n < 1 || n > maxReasonRunes {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("reason must be 1 to %d characters", maxReasonRunes))
			return
		}
		res, err := sw(r.Context(), r.PathValue("key"), *req.Enabled, reason)
		if errors.Is(err, ErrRuleAlreadyInState) {
			state := "disabled"
			if *req.Enabled {
				state = "enabled"
			}
			writeError(w, http.StatusConflict, "rule is already "+state)
			return
		}
		if err != nil {
			status, msg := staffRulesError(err)
			if status == http.StatusInternalServerError {
				log.ErrorContext(r.Context(), "validation: staff switch rule", slog.Any("err", err))
			}
			writeError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}
