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
	"fmt"

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
	// ErrNoActiveRuleSet is returned by the loaders when no
	// rule_set_versions row has is_active=true.
	ErrNoActiveRuleSet = errors.New("validation: no active rule-set")
	// ErrEmptyRuleSet is returned by both loaders when the active
	// rule_set_versions row EXISTS but carries zero rules. It WRAPS
	// ErrNoActiveRuleSet, so statusForErr answers 503 unchanged and callers
	// that only care "the gate cannot evaluate" need no new branch -- while
	// errors.Is(err, ErrEmptyRuleSet) still discriminates the cause.
	//
	// This is a fail-LOUD guard against a silent fail-OPEN (M4-04-03 Stage-1
	// addendum G3). An active version with zero rules is never legitimate --
	// a published version always ships with its rules -- so zero rows means
	// the rules are UNREADABLE, not that compliance is trivially satisfied.
	// Without the guard the loader returns rs.Rules=[] with err=nil, Evaluate
	// finds nothing to check, and EVERY invoice validates clean with HTTP 200:
	// the worst failure available to a compliance gate. The reachable path is
	// RLS being added to `rules` ALONE (the likelier target -- it holds the
	// content): the house policy idiom
	// `nullif(current_setting('app.current_tenant', true), '')` passes
	// missing_ok=true, so an unset GUC yields zero rows with NO error --
	// unlike the version SELECT, whose zero rows surface as pgx.ErrNoRows and
	// already fail closed. Verified reachable against the live dev DB (rules
	// deleted for the active version inside a rolled-back tx: 0 rules visible,
	// no error raised).
	//
	// Note the rules SELECT deliberately does NOT filter on `enabled`, so an
	// all-rules-disabled version (the M3-06 kill-switch taken to its limit)
	// still loads every row and does NOT trip this guard.
	ErrEmptyRuleSet = fmt.Errorf("%w: active version carries no readable rules", ErrNoActiveRuleSet)
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

// loadActiveRuleSetTx materializes the active rule_set_versions row + its
// rules over an already-open transaction -- the one place the engine's "load"
// stage is expressed. Both loaders below delegate here so they cannot drift
// apart on the two things that must hold identically for either caller: the
// [uuid-stamp] (rs.ID) and the ErrEmptyRuleSet fail-loud guard. The tx is the
// ONLY difference between them (a tenant-threaded one vs a plain one), and it
// is the caller's to open, own, and finish.
//
// Both SELECTs read inside that single transaction, so the rules are always
// the ones belonging to the version row that was read -- a concurrent publish
// cannot interleave a v2 version number with v1's rules.
func loadActiveRuleSetTx(ctx context.Context, tx pgx.Tx) (RuleSet, error) {
	var versionID string
	var version int
	if err := tx.QueryRow(ctx,
		`SELECT id, version FROM rule_set_versions WHERE is_active LIMIT 1`,
	).Scan(&versionID, &version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RuleSet{}, ErrNoActiveRuleSet
		}
		return RuleSet{}, err
	}

	// "when" is a reserved word -- quoted so it reads as the column, not the
	// CASE/WHEN keyword.
	rows, err := tx.Query(ctx,
		`SELECT key, type, target, params, severity, "when", message, scope, enabled
		 FROM rules WHERE rule_set_version_id = $1 ORDER BY key`, versionID,
	)
	if err != nil {
		return RuleSet{}, err
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
			return RuleSet{}, err
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return RuleSet{}, err
	}

	// Fail LOUD, never fail open: an active version with zero rules means the
	// rules are unreadable, not that every invoice is compliant. See
	// ErrEmptyRuleSet's doc for why zero rows here can arrive with err == nil.
	if len(rules) == 0 {
		return RuleSet{}, fmt.Errorf("%w (version %d, id %s)", ErrEmptyRuleSet, version, versionID)
	}

	return RuleSet{ID: versionID, Version: version, Rules: rules}, nil
}

// LoadActiveRuleSet loads the active rule_set_versions row and its rules
// (inside db.WithinRequestTenantTx) and materializes a RuleSet -- the
// engine's "load" stage (story Core AC #1: the active published version is
// what gets evaluated). Returns ErrNoActiveRuleSet when no row has
// is_active=true, and ErrEmptyRuleSet (which wraps it, so still a 503) when
// the active row carries no rules.
//
// Signature and tenant wrap are unchanged (M4-04-03): this remains the
// identity-carrying path behind POST /v1/validate (the M3-09 playground
// contract). It now also populates rs.ID -- the versionID it always scanned
// and, until M4-04-03, silently discarded ([uuid-stamp]).
func (s *Store) LoadActiveRuleSet(ctx context.Context) (RuleSet, error) {
	var rs RuleSet
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		loaded, err := loadActiveRuleSetTx(ctx, tx)
		if err != nil {
			return err
		}
		rs = loaded
		return nil
	})
	if err != nil {
		return RuleSet{}, err
	}
	return rs, nil
}

// LoadActiveRuleSetGlobal is LoadActiveRuleSet for a caller that carries NO
// identity -- the tenant-free peer path behind POST /v1/validate/batch
// ([tenant-free-ruleset-load], [s2s-identity]).
//
// It is functionally REQUIRED, not a stylistic variant: db.WithinRequestTenantTx
// returns db.ErrNoTenant when no identity is in context
// (platform/db/tenant.go:21-27), so an identity-less s2s caller structurally
// cannot use LoadActiveRuleSet -- it would hard-fail every batch. Hence the
// plain pool.Begin here.
//
// This does NOT route around RLS, because there is no RLS here to route
// around: rule_set_versions and rules are GLOBAL, untenanted tables with no
// tenant_id column and relrowsecurity=false on both (verified live), and this
// file's header already records that the tenant wrap exists "purely to thread
// the caller's identity through to audit.Record" -- which the load path never
// calls. It still runs as invoice_app (NOBYPASSRLS) over the same
// least-privilege GRANT SELECT: no superuser, no BYPASSRLS, no new grant. The
// SET LOCAL app.current_tenant that WithinRequestTenantTx would issue is a
// no-op for both SELECTs, which is exactly why skipping it changes no result.
//
// On the fail-closed claim: it holds for the version SELECT (zero rows ->
// pgx.ErrNoRows -> ErrNoActiveRuleSet -> 503) and is enforced for the rules
// SELECT by loadActiveRuleSetTx's ErrEmptyRuleSet guard, which is what turns
// the otherwise-silent zero-rows-no-error case into a loud 503. See
// ErrEmptyRuleSet.
func (s *Store) LoadActiveRuleSetGlobal(ctx context.Context) (RuleSet, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RuleSet{}, err
	}
	// Read-only: Rollback is the normal ending. It is a no-op after Commit.
	defer func() { _ = tx.Rollback(ctx) }()

	rs, err := loadActiveRuleSetTx(ctx, tx)
	if err != nil {
		return RuleSet{}, err
	}
	if err := tx.Commit(ctx); err != nil {
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
