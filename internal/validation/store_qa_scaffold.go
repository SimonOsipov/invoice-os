// This file is a QA Mode-A compile scaffold for task-109 / M4-04-03 ("04:
// batch validate endpoint, s2s peer auth, tenant-free rule-set load") -- NOT
// the real LoadActiveRuleSetGlobal, and NOT meant to be reused, patched, or
// extended. It exists for exactly one reason: VB-14 (store_test.go) and
// VB-12/13/17 (batch_db_test.go) reference Store.LoadActiveRuleSetGlobal,
// and Go cannot compile a test file whose symbols don't exist. The RALPH
// orchestrator's Mode-A brief for this subtask is explicit -- "You do NOT
// write the endpoint. The executor drives your reds to green." -- so this
// scaffold is deliberately NOT added to store.go, and is deliberately NOT a
// good-faith attempt at the real method.
//
// This scaffold gets the tenant-free PLUMBING right on purpose (a plain
// pool.Begin(), NOT db.WithinRequestTenantTx) -- it genuinely runs with no
// identity in ctx, rather than reproducing the tempting-but-wrong first
// draft [tenant-free-ruleset-load] warns against ("just wrap the existing
// tenant-tx method", which would return db.ErrNoTenant for every peer
// caller and defeat the entire point of this subtask). It also correctly
// loads every rule row for the active version (all 19, live-verified) --
// this is deliberate too: Stage-1 addendum G3's concern (an RLS-on-`rules`-
// alone scenario silently yielding rs.Rules=[] and every invoice validating
// clean) is a DISTINCT risk from [uuid-stamp]'s ID-stamping, and conflating
// the two here would make VB-14's len(rs.Rules)==19 assertion pass or fail
// for the wrong reason.
//
// It is deliberately wrong on exactly ONE axis, mirroring the omission
// already sitting in the real LoadActiveRuleSet today (store.go:72-76 scans
// versionID and then throws it away): this scaffold ALSO never assigns
// rs.ID. VB-14 must fail on that one field -- not on err, not on Version,
// not on len(Rules).
//
// The executor deletes this ENTIRE file when authoring the real
// LoadActiveRuleSetGlobal in internal/validation/store.go (task-109 /
// M4-04-03), per the plan's (b): "the same two SELECTs as LoadActiveRuleSet,
// in a plain pool.Begin transaction ... assigning BOTH rs.ID and
// rs.Version".
package validation

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// LoadActiveRuleSetGlobal -- QA Mode-A scaffold. See file header: correct
// tenant-free plumbing, correct version+rules load, deliberately never
// assigns RuleSet.ID.
func (s *Store) LoadActiveRuleSetGlobal(ctx context.Context) (RuleSet, error) {
	var rs RuleSet

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RuleSet{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

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
	// WRONG (deliberate, see file header): versionID is scanned but never
	// assigned to rs.ID -- VB-14 must catch this.

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

	rs.Version = version
	rs.Rules = rules

	if err := tx.Commit(ctx); err != nil {
		return RuleSet{}, err
	}
	return rs, nil
}
