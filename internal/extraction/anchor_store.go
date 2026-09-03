// anchor_store.go: the learned-rule read. The tenant is a parameter, never ctx -- the worker
// has no request identity. A row the parser rejects is an error, not a skip.
package extraction

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// LoadAnchorRules is the learned-rule read as a seam, satisfied by (*Store).AnchorRulesFor
// with no adapter. A named func type, matching every other seam in this package.
type LoadAnchorRules func(ctx context.Context, tenantID, fingerprint string) ([]AnchorRule, error)

// AnchorRule is one stored rule: its row identity plus its decoded body.
type AnchorRule struct {
	ID    string
	Field string
	Rule  Rule
}

// AnchorRulesFor returns the tenant's anchor rules for one layout fingerprint, newest first,
// never a nil slice.
func (s *Store) AnchorRulesFor(ctx context.Context, tenantID, fingerprint string) ([]AnchorRule, error) {
	out := []AnchorRule{}
	err := db.WithinTenantTx(ctx, s.Pool, tenantID, func(tx pgx.Tx) error {
		var err error
		out, err = anchorRulesForTx(ctx, tx, tenantID, fingerprint)
		return err
	})
	return out, err
}

// anchorRulesForTx errors on a row the parser rejects and returns an empty slice, never the
// partial one -- TestAnchorRulesFor_AParseFailureDiscardsTheRowsAlreadyRead.
// RLS isolates, not the tenant_id predicate: measured, the predicate only folds the RLS qual to
// a One-Time Filter; the qual reaches the same index without it.
// The ORDER BY carries no tiebreak: a `, id` reintroduces an Incremental Sort
// (TestAnchorRulesFor_OrdersFromTheIndexWithoutASort), and seq stays a total order because the
// writer never names it (TestExtractionAnchorStore_TheInsertNeverNamesSeq).
func anchorRulesForTx(ctx context.Context, tx pgx.Tx, tenantID, fingerprint string) ([]AnchorRule, error) {
	out := []AnchorRule{}

	rows, err := tx.Query(ctx,
		`SELECT id, field_name, rule, rule_schema_version
		   FROM extraction_anchor_rules
		  WHERE tenant_id = $1 AND layout_fingerprint = $2
		  ORDER BY seq DESC`,
		tenantID, fingerprint)
	if err != nil {
		return out, fmt.Errorf("extraction: read anchor rules for fingerprint %s: %w", fingerprint, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			a       AnchorRule
			body    []byte
			version int
		)
		if err := rows.Scan(&a.ID, &a.Field, &body, &version); err != nil {
			return []AnchorRule{}, fmt.Errorf("extraction: scan anchor rule for fingerprint %s: %w", fingerprint, err)
		}
		if version != RuleSchemaVersion {
			return []AnchorRule{}, fmt.Errorf("extraction: anchor rule %s: schema version %d, want %d", a.ID, version, RuleSchemaVersion)
		}
		r, err := ParseRule(body)
		if err != nil {
			return []AnchorRule{}, fmt.Errorf("extraction: anchor rule %s: %w", a.ID, err)
		}
		a.Rule = r
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return []AnchorRule{}, fmt.Errorf("extraction: read anchor rules for fingerprint %s: %w", fingerprint, err)
	}
	return out, nil
}

// JobLayout is what a job's own read recorded: the fingerprint its learned rules are keyed to,
// and the page-1 anchors a later correction can name. Anchors is never nil.
type JobLayout struct {
	Fingerprint string
	Anchors     []AnchorObservation
}

// appendAnchorRuleTx writes one learned rule and returns its id. Taking a LearnedRule makes a
// body ParseRule would reject unrepresentable at the call site. Append-only by GRANT:
// invoice_app holds INSERT and no UPDATE or DELETE. The column list never names seq --
// TestExtractionAnchorStore_TheInsertNeverNamesSeq.
func appendAnchorRuleTx(ctx context.Context, tx pgx.Tx, tenantID, fingerprint string, lr LearnedRule) (string, error) {
	var id string
	if err := tx.QueryRow(ctx,
		`INSERT INTO extraction_anchor_rules
		     (tenant_id, layout_fingerprint, field_name, rule, rule_schema_version)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id`,
		tenantID, fingerprint, lr.Field, string(lr.Body), RuleSchemaVersion).Scan(&id); err != nil {
		return "", fmt.Errorf("extraction: append anchor rule for field %s: %w", lr.Field, err)
	}
	return id, nil
}

// jobLayoutTx reads the layout one job recorded. ok is false for a job that has none, and
// equally for another tenant's job, which RLS makes indistinguishable from absent. Only
// pgx.ErrNoRows becomes ok=false -- a malformed jobID stays a 22P02 error
// (TestJobLayout_AMalformedJobIdIsAnErrorNotAnAbsence).
func jobLayoutTx(ctx context.Context, tx pgx.Tx, tenantID, jobID string) (JobLayout, bool, error) {
	var fp *string
	var anchors []byte
	err := tx.QueryRow(ctx,
		`SELECT layout_fingerprint, layout_anchors
		   FROM extraction_jobs
		  WHERE tenant_id = $1 AND id = $2`,
		tenantID, jobID).Scan(&fp, &anchors)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return JobLayout{}, false, nil
	case err != nil:
		return JobLayout{}, false, fmt.Errorf("extraction: read layout for job %s: %w", jobID, err)
	}
	if fp == nil {
		return JobLayout{}, false, nil
	}
	// The columns are independently nullable and UnmarshalAnchorObservations refuses nil, so a
	// fingerprint with no anchors is an empty list here, not an error.
	if anchors == nil {
		return JobLayout{Fingerprint: *fp, Anchors: []AnchorObservation{}}, true, nil
	}
	obs, err := UnmarshalAnchorObservations(anchors)
	if err != nil {
		return JobLayout{}, false, fmt.Errorf("extraction: read layout for job %s: %w", jobID, err)
	}
	return JobLayout{Fingerprint: *fp, Anchors: obs}, true, nil
}
