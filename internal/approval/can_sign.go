package approval

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Wire values of pending_holder_reason and holder.reason.
const (
	HolderReasonUnstaffed        = "unstaffed"
	HolderReasonNobodyCanApprove = "nobody_can_approve"
)

// holderReason maps a ladder reason to its wire value; nil when someone can approve.
func holderReason(reason string) *string {
	var v string
	switch reason {
	case "":
		return nil
	case "deleted", "no holder":
		v = HolderReasonUnstaffed
	default:
		v = HolderReasonNobodyCanApprove
	}
	return &v
}

// unsignableReasonsTx returns one reason per probe, "" when someone can approve it. decideTx is the spec:
// TestCanSign_AgreesWithTheApprovePath. Callers pass invoices they can see; RLS is the tenant scope.
func unsignableReasonsTx(ctx context.Context, tx pgx.Tx, probes []CreatorProbe) ([]string, error) {
	out := make([]string, 0, len(probes))
	if len(probes) == 0 {
		return out, nil
	}
	ids := make([]string, len(probes))
	keys := make([]*string, len(probes))
	for i, p := range probes {
		ids[i], keys[i] = p.InvoiceID, p.RoleKey
	}

	// No EXISTS on workflow_role_members: TestAxis2_ZeroWorkflowRoleMembersExistsPredicatesRemain.
	rows, err := tx.Query(ctx, unsignableReasonsSQL, ids, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			return nil, err
		}
		out = append(out, reason)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// The tenant pins keep the statement correct on a read that bypasses RLS (TestStranded_AnotherTenantsRoleDoesNotStaffThisRun).
const unsignableReasonsSQL = `-- unsignableReasonsTx
WITH p AS (
  SELECT o.ord, i.id AS inv_id, i.tenant_id AS tid, o.role_key, i.entity_id, h.actor
    FROM unnest($1::uuid[], $2::text[]) WITH ORDINALITY AS o(invoice_id, role_key, ord)
    LEFT JOIN invoices i ON i.id = o.invoice_id
    LEFT JOIN LATERAL (
          SELECT sh.actor FROM invoice_status_history sh
           WHERE sh.invoice_id = o.invoice_id AND sh.from_status IS NULL LIMIT 1) h ON true
), reach AS (
  SELECT p.ord, m.user_id, (t.kind <> 'firm' OR m.role = 'admin' OR m.all_clients OR mc.user_id IS NOT NULL) AS reaches
    FROM p
    JOIN memberships m ON m.tenant_id = p.tid AND m.status = 'active' AND m.role IN ('admin', 'reviewer')
    JOIN tenants t ON t.id = m.tenant_id
    LEFT JOIN membership_clients mc ON mc.tenant_id = m.tenant_id AND mc.user_id = m.user_id AND mc.entity_id = p.entity_id
), others AS (
  SELECT r.ord, count(*) AS n
    FROM reach r JOIN p ON p.ord = r.ord
   WHERE r.reaches AND r.user_id::text IS DISTINCT FROM p.actor
   GROUP BY r.ord
), mem AS (
  SELECT p.ord, wrm.user_id, r.reaches
    FROM p
    JOIN workflow_roles wr ON wr.key = p.role_key AND wr.tenant_id = p.tid AND wr.deleted_at IS NULL
    JOIN workflow_role_members wrm ON wrm.tenant_id = wr.tenant_id AND wrm.workflow_role_id = wr.id
    JOIN reach r ON r.ord = p.ord AND r.user_id = wrm.user_id
)
SELECT CASE WHEN p.inv_id IS NULL THEN ''
            WHEN wr.id IS NULL THEN 'deleted'
            WHEN count(mem.user_id) = 0 THEN 'no holder'
            WHEN count(*) FILTER (WHERE mem.reaches) = 0 THEN 'out of client reach'
            WHEN count(*) FILTER (WHERE mem.reaches
                   AND (mem.user_id::text IS DISTINCT FROM p.actor OR coalesce(o.n, 0) = 0)) = 0 THEN 'creator only'
            ELSE '' END
  FROM p
  LEFT JOIN workflow_roles wr ON wr.key = p.role_key AND wr.tenant_id = p.tid AND wr.deleted_at IS NULL
  LEFT JOIN mem ON mem.ord = p.ord
  LEFT JOIN others o ON o.ord = p.ord
 GROUP BY p.ord, p.inv_id, p.tid, p.actor, wr.id, o.n
 ORDER BY p.ord`
