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
	case StrandedDeleted, StrandedNoHolder:
		v = HolderReasonUnstaffed
	default:
		v = HolderReasonNobodyCanApprove
	}
	return &v
}

// SignerProbe is one invoice's pending approval step: its role key and its delegation.
type SignerProbe struct {
	InvoiceID       string
	RoleKey         *string
	AllowDelegation bool
	DelegateUserID  *string // nil with AllowDelegation: anyone
}

// signerStandingTx returns one reason per probe and whether subject ("" = none) signs it.
// decideTx is the spec: TestCanSign_AgreesWithTheApprovePath. Callers pass invoices they can see; RLS is the tenant scope.
func signerStandingTx(ctx context.Context, tx pgx.Tx, probes []SignerProbe, subject string) ([]string, []bool, error) {
	reasons := make([]string, 0, len(probes))
	signs := make([]bool, 0, len(probes))
	if len(probes) == 0 {
		return reasons, signs, nil
	}
	ids := make([]string, len(probes))
	keys := make([]*string, len(probes))
	allow := make([]bool, len(probes))
	delegates := make([]*string, len(probes))
	for i, p := range probes {
		ids[i], keys[i], allow[i], delegates[i] = p.InvoiceID, p.RoleKey, p.AllowDelegation, p.DelegateUserID
	}

	// No EXISTS on workflow_role_members: TestAxis2_ZeroWorkflowRoleMembersExistsPredicatesRemain.
	rows, err := tx.Query(ctx, unsignableReasonsSQL, ids, keys, allow, delegates, subject)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		var ok bool
		if err := rows.Scan(&reason, &ok); err != nil {
			return nil, nil, err
		}
		reasons = append(reasons, reason)
		signs = append(signs, ok)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return reasons, signs, nil
}

// unsignableReasonsTx returns one reason per probe, "" when someone can approve it. A delegate counts as
// DelegateMayDecide admits it.
func unsignableReasonsTx(ctx context.Context, tx pgx.Tx, probes []SignerProbe) ([]string, error) {
	reasons, _, err := signerStandingTx(ctx, tx, probes, "")
	return reasons, err
}

// The tenant pins keep the statement correct on a read that bypasses RLS (TestStranded_AnotherTenantsRoleDoesNotStaffThisRun).
const unsignableReasonsSQL = `-- unsignableReasonsTx
WITH p AS (
  SELECT o.ord, i.id AS inv_id, i.tenant_id AS tid, o.role_key, o.allow, o.delegate, i.entity_id, h.actor
    FROM unnest($1::uuid[], $2::text[], $3::bool[], $4::text[]) WITH ORDINALITY AS o(invoice_id, role_key, allow, delegate, ord)
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
), del AS (
  -- A delegate needs no role: DelegateMayDecide.
  SELECT r.ord, r.user_id, r.reaches
    FROM reach r JOIN p ON p.ord = r.ord
   WHERE p.allow AND (p.delegate IS NULL OR r.user_id::text = p.delegate)
), cand AS (
  SELECT ord, user_id, reaches FROM mem UNION ALL SELECT ord, user_id, reaches FROM del
)
SELECT CASE WHEN p.inv_id IS NULL THEN ''
            WHEN count(c.user_id) = 0 THEN CASE WHEN wr.id IS NULL THEN 'deleted' ELSE 'no holder' END
            WHEN count(*) FILTER (WHERE c.reaches) = 0 THEN 'out of client reach'
            WHEN count(*) FILTER (WHERE c.reaches
                   AND (c.user_id::text IS DISTINCT FROM p.actor OR coalesce(o.n, 0) = 0)) = 0 THEN 'creator only'
            ELSE '' END,
       coalesce(bool_or(c.user_id::text = $5::text AND c.reaches
                   AND (c.user_id::text IS DISTINCT FROM p.actor OR coalesce(o.n, 0) = 0)), false)
  FROM p
  LEFT JOIN workflow_roles wr ON wr.key = p.role_key AND wr.tenant_id = p.tid AND wr.deleted_at IS NULL
  LEFT JOIN cand c ON c.ord = p.ord
  LEFT JOIN others o ON o.ord = p.ord
 GROUP BY p.ord, p.inv_id, p.tid, p.actor, wr.id, o.n
 ORDER BY p.ord`
