package customrule

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const (
	maxRules  = 100
	entityMsg = "entity_id is not a client company of this workspace"
	capMsg    = "a workspace can hold at most 100 custom rules"
)

// Store runs every call as invoice_app through db.WithinRequestTenantTx; RLS is the only
// tenant filter.
type Store struct {
	pool  *pgxpool.Pool
	check func(context.Context, CheckInput) error
}

func NewStore(pool *pgxpool.Pool, check func(context.Context, CheckInput) error) *Store {
	return &Store{pool: pool, check: check}
}

const ruleCols = `rule_id::text, id::text, revision, entity_id::text, type, target, params, severity,
	"when", message, enabled, created_at, created_by`

// latestRevisions is the newest revision of each rule that is not deleted.
const latestRevisions = `(SELECT DISTINCT ON (rule_id) * FROM custom_rule_revisions ORDER BY rule_id, revision DESC) cur`

func scanRule(row pgx.Row) (Rule, error) {
	var r Rule
	err := row.Scan(&r.ID, &r.RevisionID, &r.Revision, &r.EntityID, &r.Type, &r.Target, &r.Params, &r.Severity,
		&r.When, &r.Message, &r.Enabled, &r.UpdatedAt, &r.UpdatedBy)
	r.Key = Key(r.ID)
	return r, err
}

func collect(rows pgx.Rows) ([]Rule, error) {
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveLockKey is the text hashed into the tenant's save lock (D27).
func SaveLockKey(tenantID string) string { return "custom_rules:" + tenantID }

// CurrentTx returns the latest revision of each enabled, not-deleted rule that is firm-wide
// or scoped to one of entityIDs. Scope filters after DISTINCT ON, so a rule moved to another
// client leaves the old one.
func CurrentTx(ctx context.Context, tx pgx.Tx, entityIDs []string) ([]Rule, error) {
	if entityIDs == nil {
		entityIDs = []string{}
	}
	rows, err := tx.Query(ctx,
		`SELECT `+ruleCols+` FROM `+latestRevisions+`
		  WHERE NOT deleted AND enabled AND (entity_id IS NULL OR entity_id = ANY($1::uuid[]))
		  ORDER BY rule_id`, entityIDs)
	if err != nil {
		return nil, err
	}
	return collect(rows)
}

// List returns the current, not-deleted rules (disabled included) to any active member.
func (s *Store) List(ctx context.Context) ([]Rule, error) {
	var out []Rule
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+ruleCols+` FROM `+latestRevisions+` WHERE NOT deleted ORDER BY rule_id`)
		if err != nil {
			return err
		}
		out, err = collect(rows)
		return err
	})
	return out, err
}

func (s *Store) Create(ctx context.Context, in RuleInput) (Rule, error) {
	return s.save(ctx, "", in, false)
}

func (s *Store) Update(ctx context.Context, id string, in RuleInput) (Rule, error) {
	return s.save(ctx, id, in, false)
}

// Delete appends a deleted revision that carries the latest content.
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.save(ctx, id, RuleInput{}, true)
	return err
}

// save is create (id empty), update, or delete. The first pass is advisory and runs before
// the check call; the second repeats it under the tenant save lock and writes.
func (s *Store) save(ctx context.Context, id string, in RuleInput, del bool) (Rule, error) {
	caller, ok := auth.IdentityFromContext(ctx)
	if !ok {
		return Rule{}, db.ErrNoTenant
	}
	err := db.WithinRequestTenantTxOpts(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		_, err := guard(ctx, tx, caller.Subject, id, in, del)
		return err
	})
	if err != nil {
		return Rule{}, err
	}
	if !del {
		if err := s.check(ctx, CheckInput{Type: in.Type, Target: in.Target, Params: in.Params,
			Severity: in.Severity, When: in.When, Message: in.Message}); err != nil {
			var ie InvalidError
			if errors.As(err, &ie) {
				return Rule{}, ie
			}
			return Rule{}, fmt.Errorf("%w: %v", ErrCheckUnavailable, err)
		}
	}

	var saved Rule
	err = db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, SaveLockKey(caller.TenantID)); err != nil {
			return err
		}
		latest, err := guard(ctx, tx, caller.Subject, id, in, del)
		if err != nil {
			return err
		}
		ruleID, revision := id, latest.Revision+1
		if id == "" {
			ruleID, revision = uuid.NewString(), 1
		}
		if del {
			in = RuleInput{EntityID: latest.EntityID, Type: latest.Type, Target: latest.Target, Params: latest.Params,
				Severity: latest.Severity, When: latest.When, Message: latest.Message, Enabled: latest.Enabled}
		}
		params := in.Params
		if len(params) == 0 {
			params = []byte(`{}`)
		}
		// D16: only a revision that can block waits for the re-check.
		pending := in.Enabled && in.Severity == "error" && !del
		saved, err = scanRule(tx.QueryRow(ctx,
			`INSERT INTO custom_rule_revisions
			     (tenant_id, rule_id, revision, entity_id, type, target, params, severity, "when", message,
			      enabled, deleted, created_by, rechecked_at)
			 VALUES (current_setting('app.current_tenant')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
			         CASE WHEN $13 THEN NULL ELSE now() END)
			 RETURNING `+ruleCols,
			ruleID, revision, in.EntityID, in.Type, in.Target, string(params), in.Severity, in.When, in.Message,
			in.Enabled, del, caller.Subject, pending))
		if err != nil {
			return err
		}
		if del {
			return audit.Record(ctx, tx, caller.Subject, "custom_rule.deleted", map[string]any{
				"rule_id": saved.ID, "revision_id": saved.RevisionID, "revision": saved.Revision, "entity_id": saved.EntityID})
		}
		if id == "" {
			return audit.Record(ctx, tx, caller.Subject, "custom_rule.created", map[string]any{
				"rule_id": saved.ID, "revision_id": saved.RevisionID, "revision": saved.Revision, "entity_id": saved.EntityID,
				"rule": snapshot(saved)})
		}
		return audit.Record(ctx, tx, caller.Subject, "custom_rule.updated", map[string]any{
			"rule_id": saved.ID, "revision_id": saved.RevisionID, "revision": saved.Revision, "entity_id": saved.EntityID,
			"rule": snapshot(saved)})
	})
	if err != nil {
		return Rule{}, err
	}
	return saved, nil
}

// snapshot is the audited content of a revision.
func snapshot(r Rule) map[string]any {
	return map[string]any{"type": r.Type, "target": r.Target, "params": r.Params, "severity": r.Severity,
		"when": r.When, "message": r.Message, "enabled": r.Enabled}
}

// guard refuses a non-admin, a bad body, a foreign entity and a missing rule, in that order,
// and on create the 101st rule. It returns the rule's latest revision for update and delete.
func guard(ctx context.Context, tx pgx.Tx, subject, id string, in RuleInput, del bool) (Rule, error) {
	if err := requireActiveAdmin(ctx, tx, subject); err != nil {
		return Rule{}, err
	}
	if !del {
		if in.Severity != "error" && in.Severity != "warning" {
			return Rule{}, InvalidError{"severity must be error or warning"}
		}
		if in.Type == "" || in.Message == "" {
			return Rule{}, InvalidError{"type and message are required"}
		}
		if in.EntityID != nil {
			if err := entityVisible(ctx, tx, *in.EntityID); err != nil {
				return Rule{}, err
			}
		}
	}
	if id == "" {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+latestRevisions+` WHERE NOT deleted`).Scan(&n); err != nil {
			return Rule{}, err
		}
		if n >= maxRules {
			return Rule{}, InvalidError{capMsg}
		}
		return Rule{}, nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return Rule{}, ErrNotFound
	}
	var deleted bool
	var latest Rule
	err := tx.QueryRow(ctx,
		`SELECT `+ruleCols+`, deleted FROM custom_rule_revisions WHERE rule_id = $1 ORDER BY revision DESC LIMIT 1`, id).
		Scan(&latest.ID, &latest.RevisionID, &latest.Revision, &latest.EntityID, &latest.Type, &latest.Target,
			&latest.Params, &latest.Severity, &latest.When, &latest.Message, &latest.Enabled, &latest.UpdatedAt,
			&latest.UpdatedBy, &deleted)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && deleted) {
		return Rule{}, ErrNotFound
	}
	return latest, err
}

// entityVisible answers InvalidError for an unknown id and for another tenant's (RLS hides it).
func entityVisible(ctx context.Context, tx pgx.Tx, entityID string) error {
	if _, err := uuid.Parse(entityID); err != nil {
		return InvalidError{entityMsg}
	}
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM business_entities WHERE id = $1`, entityID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvalidError{entityMsg}
	}
	return err
}

// requireActiveAdmin has the shape of approval's: the seam has already refused a suspended
// caller, so a missing or non-admin row is a preparer or reviewer.
func requireActiveAdmin(ctx context.Context, tx pgx.Tx, subject string) error {
	var role string
	if err := tx.QueryRow(ctx,
		`SELECT role FROM memberships WHERE user_id = $1 AND status = 'active'`, subject,
	).Scan(&role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotPermitted
		}
		return err
	}
	if role != "admin" {
		return ErrNotPermitted
	}
	return nil
}
