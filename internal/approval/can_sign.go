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
	Ord             int // the step's ord; the earlier-approver terms apply only on the run's lowest pending one
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
	ords := make([]int32, len(probes))
	for i, p := range probes {
		ids[i], keys[i], allow[i], delegates[i], ords[i] = p.InvoiceID, p.RoleKey, p.AllowDelegation, p.DelegateUserID, int32(p.Ord)
	}

	// No EXISTS on workflow_role_members: TestAxis2_ZeroWorkflowRoleMembersExistsPredicatesRemain.
	rows, err := tx.Query(ctx, unsignableReasonsSQL, ids, keys, allow, delegates, subject, ords)
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
  SELECT o.ord, i.id AS inv_id, i.tenant_id AS tid, o.role_key, o.allow, o.delegate, i.entity_id,
         -- cur: the probe is the run's lowest-ord pending approval step.
         coalesce(o.step_ord = (SELECT min(s.ord) FROM approval_runs r
                                 JOIN approval_run_steps s ON s.tenant_id = r.tenant_id AND s.run_id = r.id
                                      AND s.kind = 'approval' AND s.state = 'pending'
                                WHERE r.tenant_id = i.tenant_id AND r.invoice_id = i.id AND r.state = 'open'), false) AS cur
    FROM unnest($1::uuid[], $2::text[], $3::bool[], $4::text[], $6::int[]) WITH ORDINALITY AS o(invoice_id, role_key, allow, delegate, step_ord, ord)
    LEFT JOIN invoices i ON i.id = o.invoice_id
), dec AS (
  -- ceiling: no run_id index on approval_decisions; add (tenant_id, run_id) above ~100k rows per tenant
  SELECT p.ord, d.actor::text AS actor
    FROM p
    JOIN approval_runs r ON r.tenant_id = p.tid AND r.invoice_id = p.inv_id AND r.state = 'open'
    JOIN approval_decisions d ON d.tenant_id = r.tenant_id AND d.run_id = r.id AND d.decision = 'approved'
   WHERE p.cur
), reach AS (
  SELECT p.ord, m.user_id, (t.kind <> 'firm' OR m.role = 'admin' OR m.all_clients OR mc.user_id IS NOT NULL) AS reaches,
         -- An author is the creator or the actor of an invoice.updated row.
         (m.user_id::text IN (SELECT sh.actor FROM invoice_status_history sh
                               WHERE sh.invoice_id = p.inv_id AND sh.from_status IS NULL)
          OR m.user_id::text IN (SELECT a.actor FROM audit_log a
                                  WHERE a.tenant_id = p.tid AND a.invoice_id = p.inv_id AND a.event = 'invoice.updated')) AS is_author,
         m.user_id::text IN (SELECT dec.actor FROM dec WHERE dec.ord = p.ord) AS is_decider
    FROM p
    JOIN memberships m ON m.tenant_id = p.tid AND m.status = 'active' AND m.role IN ('admin', 'reviewer')
    JOIN tenants t ON t.id = m.tenant_id
    LEFT JOIN membership_clients mc ON mc.tenant_id = m.tenant_id AND mc.user_id = m.user_id AND mc.entity_id = p.entity_id
), others AS (
  SELECT r.ord, count(*) AS n
    FROM reach r JOIN p ON p.ord = r.ord
   WHERE r.reaches AND NOT r.is_author
   GROUP BY r.ord
), fresh AS (
  SELECT r.ord, count(*) AS n
    FROM reach r
   WHERE r.reaches AND NOT r.is_author AND NOT r.is_decider
   GROUP BY r.ord
), mem AS (
  SELECT p.ord, wrm.user_id, r.reaches, r.is_author, r.is_decider
    FROM p
    JOIN workflow_roles wr ON wr.key = p.role_key AND wr.tenant_id = p.tid AND wr.deleted_at IS NULL
    JOIN workflow_role_members wrm ON wrm.tenant_id = wr.tenant_id AND wrm.workflow_role_id = wr.id
    JOIN reach r ON r.ord = p.ord AND r.user_id = wrm.user_id
), del AS (
  -- A delegate needs no role: DelegateMayDecide.
  SELECT r.ord, r.user_id, r.reaches, r.is_author, r.is_decider
    FROM reach r JOIN p ON p.ord = r.ord
   WHERE p.allow AND (p.delegate IS NULL OR r.user_id::text = p.delegate)
), cand AS (
  SELECT ord, user_id, reaches, is_author, is_decider FROM mem UNION ALL SELECT ord, user_id, reaches, is_author, is_decider FROM del
)
SELECT CASE WHEN p.inv_id IS NULL THEN ''
            WHEN count(c.user_id) = 0 THEN CASE WHEN wr.id IS NULL THEN 'deleted' ELSE 'no holder' END
            WHEN count(*) FILTER (WHERE c.reaches) = 0 THEN 'out of client reach'
            WHEN count(*) FILTER (WHERE c.reaches
                   AND (NOT c.is_author OR coalesce(o.n, 0) = 0)) = 0 THEN 'creator only'
            WHEN count(*) FILTER (WHERE c.reaches
                   AND (NOT c.is_author OR coalesce(o.n, 0) = 0)
                   AND (NOT c.is_decider OR coalesce(f.n, 0) = 0)) = 0 THEN 'earlier approver only'
            ELSE '' END,
       coalesce(bool_or(c.user_id::text = $5::text AND c.reaches
                   AND (NOT c.is_author OR coalesce(o.n, 0) = 0)
                   AND (NOT c.is_decider OR coalesce(f.n, 0) = 0)), false)
  FROM p
  LEFT JOIN workflow_roles wr ON wr.key = p.role_key AND wr.tenant_id = p.tid AND wr.deleted_at IS NULL
  LEFT JOIN cand c ON c.ord = p.ord
  LEFT JOIN others o ON o.ord = p.ord
  LEFT JOIN fresh f ON f.ord = p.ord
 GROUP BY p.ord, p.inv_id, p.tid, wr.id, o.n, f.n
 ORDER BY p.ord`
