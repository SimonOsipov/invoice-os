// This file (store.go) is the DB-backed Store: it materializes a RuleSet from
// the rule_set_versions row in force on a date + its rules. Both tables are
// GLOBAL (no tenant_id, no RLS); the Store is read-only over the app role.
package validation

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	// ErrNoActiveRuleSet is returned by the loaders when no version is in
	// force on a requested date (rule_set_version_for returns NULL).
	ErrNoActiveRuleSet = errors.New("validation: no active rule-set")
	// ErrEmptyRuleSet is returned by the loaders when the in-force
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

// loadRuleSetByIDTx materializes one rule_set_versions row + its rules over an
// already-open transaction. Every loader goes through it, so they cannot drift
// on the [uuid-stamp] (rs.ID), the ErrEmptyRuleSet guard or attachCodeLists.
// Both SELECTs read inside the caller's tx, so the rules belong to the version
// row that was read.
func loadRuleSetByIDTx(ctx context.Context, tx pgx.Tx, versionID string) (RuleSet, error) {
	var version int
	if err := tx.QueryRow(ctx,
		`SELECT version FROM rule_set_versions WHERE id = $1`, versionID,
	).Scan(&version); err != nil {
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

	// Fail LOUD, never fail open: zero rules means unreadable, not compliant.
	// See ErrEmptyRuleSet.
	if len(rules) == 0 {
		return RuleSet{}, fmt.Errorf("%w (version %d, id %s)", ErrEmptyRuleSet, version, versionID)
	}

	if err := attachCodeLists(ctx, tx, rules); err != nil {
		return RuleSet{}, err
	}

	return RuleSet{ID: versionID, Version: version, Rules: rules}, nil
}

// loadForDatesTx resolves each date (YYYY-MM-DD) through rule_set_version_for
// in one query and loads each distinct version once. The map is total over
// dates; a date with no version in force fails the whole load with
// ErrNoActiveRuleSet.
func loadForDatesTx(ctx context.Context, tx pgx.Tx, dates []string) (map[string]RuleSet, error) {
	rows, err := tx.Query(ctx,
		`SELECT d, rule_set_version_for(d::date) FROM unnest($1::text[]) AS d`, dates)
	if err != nil {
		return nil, err
	}
	idByDate := make(map[string]string, len(dates))
	for rows.Next() {
		var d string
		var id *string
		if err := rows.Scan(&d, &id); err != nil {
			rows.Close()
			return nil, err
		}
		if id == nil {
			rows.Close()
			return nil, ErrNoActiveRuleSet
		}
		idByDate[d] = *id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	byID := make(map[string]RuleSet)
	out := make(map[string]RuleSet, len(idByDate))
	for d, id := range idByDate {
		rs, ok := byID[id]
		if !ok {
			if rs, err = loadRuleSetByIDTx(ctx, tx, id); err != nil {
				return nil, err
			}
			byID[id] = rs
		}
		out[d] = rs
	}
	return out, nil
}

// LoadForDates returns the rule-set version in force on each date, keyed by
// date, in one read-only transaction. Plain pool.Begin: no caller identity is
// needed -- rule_set_versions and rules are GLOBAL, untenanted tables (no
// RLS), so the S2S batch path (no identity in context) can use it. Fails
// closed: ErrNoActiveRuleSet for a date with no version, ErrEmptyRuleSet
// (wraps it) for a version with no rules.
func (s *Store) LoadForDates(ctx context.Context, dates []string) (map[string]RuleSet, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out, err := loadForDatesTx(ctx, tx, dates)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func todayUTC() string { return time.Now().UTC().Format(time.DateOnly) }

// loadTodayTx loads the version in force today (UTC).
func loadTodayTx(ctx context.Context, tx pgx.Tx) (RuleSet, error) {
	d := todayUTC()
	m, err := loadForDatesTx(ctx, tx, []string{d})
	if err != nil {
		return RuleSet{}, err
	}
	return m[d], nil
}

// LoadActiveRuleSet loads the version in force today (UTC) inside
// db.WithinRequestTenantTx, the identity-carrying loader. Errors as LoadForDates.
func (s *Store) LoadActiveRuleSet(ctx context.Context) (RuleSet, error) {
	var rs RuleSet
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		loaded, err := loadTodayTx(ctx, tx)
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

// LoadActiveRuleSetGlobal is LoadActiveRuleSet for a caller with NO identity
// (the S2S peer path behind POST /v1/validate/batch): WithinRequestTenantTx
// returns db.ErrNoTenant without one. No RLS is bypassed -- the tables are
// global and untenanted.
func (s *Store) LoadActiveRuleSetGlobal(ctx context.Context) (RuleSet, error) {
	d := todayUTC()
	m, err := s.LoadForDates(ctx, []string{d})
	if err != nil {
		return RuleSet{}, err
	}
	return m[d], nil
}
