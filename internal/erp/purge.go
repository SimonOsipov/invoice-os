package erp

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TenantSecretRefs is every secret id the workspace purge must delete, distinct and sorted: the tenant's
// grants (RLS scopes them to tx) and its erp_secret_delete jobs in any state (river_job has no RLS, so
// the tenant filter is explicit; a finished job's secret answers NOT_FOUND).
func TenantSecretRefs(ctx context.Context, tx pgx.Tx, tenantID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT secret_ref FROM (
			SELECT secret_ref FROM erp_grants WHERE secret_ref IS NOT NULL
			UNION
			SELECT args->>'secret_ref' FROM river_job
			WHERE kind = 'erp_secret_delete' AND args @> jsonb_build_object('tenant_id', $1::text)
		) refs WHERE secret_ref IS NOT NULL ORDER BY secret_ref`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("erp: tenant secret refs: %w", err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, fmt.Errorf("erp: tenant secret refs: %w", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("erp: tenant secret refs: %w", err)
	}
	return refs, nil
}
