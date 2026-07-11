// This file (store.go) is the M3-04-06 DB-backed Store: LoadActiveRuleSet (the
// engine's "load" stage, materializing a RuleSet from the active rule_set_versions
// row + its rules) and ToggleRule (the M3-06 admin kill-switch, auditing the flip
// in the same transaction). It mirrors internal/portfolio/store.go's shape: a
// *pgxpool.Pool held over the app role (invoice_app), every method wrapped in
// db.WithinRequestTenantTx so the caller's identity (auth.IdentityFromContext)
// drives the tenant context audit.Record needs -- even though rule_set_versions/
// rules themselves are GLOBAL, untenanted tables (no tenant_id column, no RLS;
// the M3-04-01 migration's grant-level immutability is the isolation mechanism,
// not RLS). audit_log IS tenant-scoped (FORCE RLS), so a toggle's audit row lands
// under whichever tenant the caller's identity carries -- the rule flip itself
// applies globally regardless (Decision N1/N14; see store_test.go's
// TestStore_ToggleAppliesCrossTenant).
//
// This is the RED skeleton (Mode A, task M3-04-06): every method panics with
// "not implemented". store_test.go is the AC-derived DB-backed suite written
// against this contract; the executor fills in the real SQL next. See rule.go
// for the Rule/RuleSet wire shapes and schema_test.go for the migrated table
// shape + dbTestPools/seedVersion/seedRule fixtures this suite reuses.
package validation

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store persists/reads rule_set_versions + rules as the invoice_app role. It
// holds the app-role pool (DATABASE_URL); every method wraps
// db.WithinRequestTenantTx purely to thread the caller's identity through to
// audit.Record -- rule content itself is not RLS-scoped (see file header).
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps the app-role connection pool. The caller owns the pool's
// lifecycle.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

var (
	// ErrNoActiveRuleSet is returned by LoadActiveRuleSet when no
	// rule_set_versions row has is_active=true.
	ErrNoActiveRuleSet = errors.New("validation: no active rule-set")
	// ErrNotFound is returned by ToggleRule when key does not match any rule
	// under the active version.
	ErrNotFound = errors.New("validation: rule not found")
	// ErrRedundantTransition is returned by ToggleRule when the rule's
	// enabled column already equals the requested target -- no UPDATE, no
	// audit row (same guard shape as portfolio.Store.SetStatus).
	ErrRedundantTransition = errors.New("validation: redundant transition")
	// ErrValidation is returned for caller-input faults that are rejected
	// before any DB round-trip.
	ErrValidation = errors.New("validation: validation")
)

// LoadActiveRuleSet loads the active rule_set_versions row and its rules
// (inside db.WithinRequestTenantTx) and materializes a RuleSet -- the
// engine's "load" stage (story Core AC #1: the active published version is
// what gets evaluated). Returns ErrNoActiveRuleSet when no row has
// is_active=true.
func (s *Store) LoadActiveRuleSet(ctx context.Context) (RuleSet, error) {
	var rs RuleSet
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var versionID string
		var version int
		if err := tx.QueryRow(ctx,
			`SELECT id, version FROM rule_set_versions WHERE is_active LIMIT 1`,
		).Scan(&versionID, &version); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoActiveRuleSet
			}
			return err
		}

		// "when" is a reserved word -- quoted so it reads as the column, not the
		// CASE/WHEN keyword.
		rows, err := tx.Query(ctx,
			`SELECT key, type, target, params, severity, "when", message, scope, enabled
			 FROM rules WHERE rule_set_version_id = $1 ORDER BY key`, versionID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()

		rules := []Rule{}
		for rows.Next() {
			var r Rule
			// params (jsonb) scans into json.RawMessage; "when" (nullable text)
			// into *string. type/severity scan straight into their named string
			// types (pgx v5 resolves the underlying kind).
			if err := rows.Scan(
				&r.Key, &r.Type, &r.Target, &r.Params, &r.Severity, &r.When, &r.Message, &r.Scope, &r.Enabled,
			); err != nil {
				return err
			}
			rules = append(rules, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		rs.Version = version
		rs.Rules = rules
		return nil
	})
	if err != nil {
		return RuleSet{}, err
	}
	return rs, nil
}

// ToggleRule flips `enabled` on the rule identified by key within the
// active rule_set_versions row, and writes a "validation.rule.disabled" or
// "validation.rule.enabled" audit.Record row in the SAME transaction (M3-06
// admin kill-switch). A redundant transition (enabled already == target)
// returns ErrRedundantTransition before any UPDATE or audit write, same
// guard shape as portfolio.Store.SetStatus. An unknown key under the active
// version returns ErrNotFound.
func (s *Store) ToggleRule(ctx context.Context, key string, enabled bool) (Rule, error) {
	var rule Rule
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The identity is guaranteed present here (WithinRequestTenantTx resolved
		// it as the tenant id before this closure ran); Subject is the audit actor.
		callerID, _ := auth.IdentityFromContext(ctx)

		var versionID string
		var version int
		if err := tx.QueryRow(ctx,
			`SELECT id, version FROM rule_set_versions WHERE is_active LIMIT 1`,
		).Scan(&versionID, &version); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoActiveRuleSet
			}
			return err
		}

		var ruleID string
		var current bool
		if err := tx.QueryRow(ctx,
			`SELECT id, enabled FROM rules WHERE rule_set_version_id = $1 AND key = $2 FOR UPDATE`,
			versionID, key,
		).Scan(&ruleID, &current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		if current == enabled {
			return ErrRedundantTransition
		}

		if err := tx.QueryRow(ctx,
			`UPDATE rules SET enabled = $1 WHERE id = $2
			 RETURNING key, type, target, params, severity, "when", message, scope, enabled`,
			enabled, ruleID,
		).Scan(&rule.Key, &rule.Type, &rule.Target, &rule.Params, &rule.Severity, &rule.When, &rule.Message, &rule.Scope, &rule.Enabled); err != nil {
			return err
		}

		event := "validation.rule.enabled"
		if !enabled {
			event = "validation.rule.disabled"
		}
		// audit.Record runs in the SAME tx: a failed write rolls back the UPDATE
		// too (atomicity -- TestStore_AuditRollsBackWithToggle).
		return audit.Record(ctx, tx, callerID.Subject, event, map[string]any{
			"key":     key,
			"version": version,
			"from":    current,
			"to":      enabled,
		})
	})
	if err != nil {
		return Rule{}, err
	}
	return rule, nil
}
