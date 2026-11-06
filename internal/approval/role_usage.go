package approval

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// RoleUse is one policy in force that names a role.
type RoleUse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Steps int    `json:"steps"`
	Scope bool   `json:"scope"`
}

// RoleUsageLister reads the policies in force per role key.
type RoleUsageLister func(ctx context.Context) (map[string][]RoleUse, error)

var _ RoleUsageLister = new(Store).ListRoleUsage

// ListRoleUsage returns, per role key, each live policy whose active version names the
// role in a scope or an approval step. Its predicate copies DeleteRole's;
// TestRoleUsage_AgreesWithDeleteRole pins the two together. The map is never nil.
func (s *Store) ListRoleUsage(ctx context.Context) (map[string][]RoleUse, error) {
	usage := map[string][]RoleUse{}
	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT r.key, p.id, p.name,
			        (r.id = ANY(v.scope_workflow_role_ids)) AS in_scope,
			        (SELECT count(*) FROM approval_policy_steps s
			          WHERE s.version_id = v.id AND s.kind = 'approval'
			            AND s.workflow_role_key = r.key) AS steps
			   FROM approval_policies p
			   JOIN approval_policy_versions v ON v.policy_id = p.id AND v.is_active
			   JOIN workflow_roles r ON r.deleted_at IS NULL
			  WHERE p.deleted_at IS NULL
			    AND (r.id = ANY(v.scope_workflow_role_ids)
			         OR EXISTS (SELECT 1 FROM approval_policy_steps s
			                     WHERE s.version_id = v.id AND s.kind = 'approval'
			                       AND s.workflow_role_key = r.key))
			  ORDER BY r.key, p.position, p.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var u RoleUse
			if err := rows.Scan(&key, &u.ID, &u.Name, &u.Scope, &u.Steps); err != nil {
				return err
			}
			usage[key] = append(usage[key], u)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return usage, nil
}
