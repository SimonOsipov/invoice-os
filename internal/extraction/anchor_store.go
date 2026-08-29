// anchor_store.go: the learned-rule read. The tenant is a parameter, never ctx -- the worker
// has no request identity. A row the parser rejects is an error, not a skip.
package extraction

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

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
func anchorRulesForTx(ctx context.Context, tx pgx.Tx, tenantID, fingerprint string) ([]AnchorRule, error) {
	out := []AnchorRule{}

	rows, err := tx.Query(ctx,
		`SELECT id, field_name, rule, rule_schema_version
		   FROM extraction_anchor_rules
		  WHERE tenant_id = $1 AND layout_fingerprint = $2
		  ORDER BY created_at DESC, id`,
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
