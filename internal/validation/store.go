// This file (store.go) is the DB-backed Store: LoadActiveRuleSet and
// LoadActiveRuleSetGlobal materialize a RuleSet from the active
// rule_set_versions row + its rules. Both tables are GLOBAL (no tenant_id, no
// RLS); the Store is read-only over the app role.
package validation

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Store reads rule_set_versions + rules as the invoice_app role. It holds the
// app-role pool (DATABASE_URL).
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
// identity-carrying, tenant-scoped loader. It now also populates rs.ID -- the
// versionID it always scanned and, until M4-04-03, silently discarded
// ([uuid-stamp]).
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
// (platform/db/tenant.go, WithinRequestTenantTx), so an identity-less s2s caller structurally
// cannot use LoadActiveRuleSet -- it would hard-fail every batch. Hence the
// plain pool.Begin here.
//
// This does NOT route around RLS, because there is no RLS here to route
// around: rule_set_versions and rules are GLOBAL, untenanted tables with no
// tenant_id column and relrowsecurity=false on both (verified live). It
// still runs as invoice_app (NOBYPASSRLS) over the same
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
