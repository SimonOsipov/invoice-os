package approval

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The refusal sentences, verbatim from the story's [message copy].
const (
	CreatorRefusedReason      = "You created this invoice, so another approver must approve it."
	EditorRefusedReason       = "You changed this invoice, so another approver must approve it."
	CreatorNoRoleHolderReason = "No other approver holds the role this step is waiting on. An admin must give one of them that role."

	EarlierApproverRefusedReason = "You approved an earlier step of this invoice, so another approver must approve this step."
)

// ErrCreatorRefused: a person rule refuses the approve (author, or earlier approver of the run).
var ErrCreatorRefused = errors.New("approval: the creator cannot approve their own invoice")

// creatorRefusal carries the sentence of whichever person rule refused.
type creatorRefusal struct{ reason string }

func (e *creatorRefusal) Error() string { return ErrCreatorRefused.Error() }
func (e *creatorRefusal) Unwrap() error { return ErrCreatorRefused }

// newCreatorRefused builds the error decisionStatusForErr maps to 403 with reason.
func newCreatorRefused(reason string) error { return &creatorRefusal{reason: reason} }

// CreatorProbe is one invoice and its pending step: the role key (nil: none) and the delegation.
type CreatorProbe struct {
	InvoiceID       string
	RoleKey         *string
	AllowDelegation bool
	DelegateUserID  *string
}

type CreatorStanding struct {
	IsCreator bool
	// IsEditor: the subject is the actor of an invoice.updated audit row of the invoice.
	IsEditor        bool
	OtherApprover   bool
	OtherRoleHolder bool
	// OtherDelegate: another approver the step's delegation lets decide (DelegateMayDecide).
	OtherDelegate bool
	// EarlierApprover: the subject approved a step of the invoice's open run.
	EarlierApprover bool
	// OtherNew*: another approver who reaches the client, is no author and approved no step of the run.
	OtherNewApprover, OtherNewRoleHolder, OtherNewDelegate bool
}

// Refusal is the person rung: an author is refused when an approver who is not an author reaches
// the client; failing that, an earlier approver of the run is refused when a fresh approver reaches it.
func (s CreatorStanding) Refusal() (reason string, refused bool) {
	if (s.IsCreator || s.IsEditor) && s.OtherApprover {
		first := EditorRefusedReason
		if s.IsCreator {
			first = CreatorRefusedReason
		}
		if !s.OtherRoleHolder && !s.OtherDelegate {
			return first + " " + CreatorNoRoleHolderReason, true
		}
		return first, true
	}
	if s.EarlierApprover && s.OtherNewApprover {
		if !s.OtherNewRoleHolder && !s.OtherNewDelegate {
			return EarlierApproverRefusedReason + " " + CreatorNoRoleHolderReason, true
		}
		return EarlierApproverRefusedReason, true
	}
	return "", false
}

// RepeatApproved reports an earlier approver approving with no fresh approver on the client.
func (s CreatorStanding) RepeatApproved() bool { return s.EarlierApprover && !s.OtherNewApprover }

// ApprovedAs is the audit marker; self_approved wins.
func (s CreatorStanding) ApprovedAs() string {
	switch {
	case s.SelfApproved():
		return "self_approved"
	case s.RepeatApproved():
		return "repeat_approved"
	}
	return "approver"
}

// SelfApproved reports an author approving with no other approver on the client.
func (s CreatorStanding) SelfApproved() bool { return (s.IsCreator || s.IsEditor) && !s.OtherApprover }

// CreatorStandingTx reads subject's author standing for each probe. A non-author
// stops after one statement and may be absent from the map. RLS is the tenant scope.
func CreatorStandingTx(ctx context.Context, tx pgx.Tx, subject string, probes []CreatorProbe) (map[string]CreatorStanding, error) {
	out := make(map[string]CreatorStanding, len(probes))
	if len(probes) == 0 {
		return out, nil
	}
	ids := make([]string, len(probes))
	keys := make([]*string, len(probes))
	allows := make([]bool, len(probes))
	delegates := make([]*string, len(probes))
	for i, p := range probes {
		ids[i], keys[i], allows[i], delegates[i] = p.InvoiceID, p.RoleKey, p.AllowDelegation, p.DelegateUserID
	}

	rows, err := tx.Query(ctx,
		`SELECT x.id::text, bool_or(x.kind = 'c'), bool_or(x.kind = 'e'), bool_or(x.kind = 'a')
		   FROM (SELECT h.invoice_id AS id, 'c' AS kind
		           FROM invoice_status_history h
		          WHERE h.invoice_id = ANY($1::uuid[]) AND h.from_status IS NULL AND h.actor = $2
		         UNION ALL
		         SELECT a.invoice_id, 'e'
		           FROM audit_log a
		          WHERE a.invoice_id = ANY($1::uuid[]) AND a.event = 'invoice.updated' AND a.actor = $2
		         UNION ALL
		         -- ceiling: no run_id index on approval_decisions; add (tenant_id, run_id) above ~100k rows per tenant
		         SELECT r.invoice_id, 'a'
		           FROM approval_decisions d
		           JOIN approval_runs r ON r.tenant_id = d.tenant_id AND r.id = d.run_id
		          WHERE r.invoice_id = ANY($1::uuid[]) AND r.state = 'open' AND d.decision = 'approved' AND d.actor = $2) x
		  GROUP BY x.id`,
		ids, subject)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var creator, editor, earlier bool
		if err := rows.Scan(&id, &creator, &editor, &earlier); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = CreatorStanding{IsCreator: creator, IsEditor: editor, EarlierApprover: earlier}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	var cIDs []string
	var cKeys, cDelegates []*string
	var cAllows []bool
	for i, id := range ids {
		if _, ok := out[id]; ok {
			cIDs, cKeys = append(cIDs, id), append(cKeys, keys[i])
			cAllows, cDelegates = append(cAllows, allows[i]), append(cDelegates, delegates[i])
		}
	}

	// Reach and holder are LEFT JOINs: no EXISTS may name workflow_role_members
	// (TestAxis2_ZeroWorkflowRoleMembersExistsPredicatesRemain).
	rows, err = tx.Query(ctx,
		`SELECT i.id::text, bool_or(wrm.user_id IS NOT NULL),
		        bool_or(p.allow_delegation AND (p.delegate_user_id IS NULL OR p.delegate_user_id = m.user_id::text)),
		        bool_or(NOT x.decided),
		        bool_or(NOT x.decided AND wrm.user_id IS NOT NULL),
		        bool_or(NOT x.decided AND p.allow_delegation AND (p.delegate_user_id IS NULL OR p.delegate_user_id = m.user_id::text))
		   FROM unnest($1::uuid[], $2::text[], $3::boolean[], $4::text[]) AS p(invoice_id, role_key, allow_delegation, delegate_user_id)
		   JOIN invoices i ON i.id = p.invoice_id
		   JOIN memberships m ON m.status = 'active' AND m.role IN ('admin', 'reviewer')
		   JOIN tenants t ON t.id = m.tenant_id
		   CROSS JOIN LATERAL (SELECT m.user_id::text IN (SELECT d.actor FROM approval_decisions d
		                                                    JOIN approval_runs r ON r.tenant_id = d.tenant_id AND r.id = d.run_id
		                                                   WHERE r.invoice_id = i.id AND r.state = 'open' AND d.decision = 'approved') AS decided) x
		   LEFT JOIN membership_clients mc ON mc.user_id = m.user_id AND mc.entity_id = i.entity_id
		   LEFT JOIN workflow_roles wr ON wr.key = p.role_key AND wr.deleted_at IS NULL
		   LEFT JOIN workflow_role_members wrm ON wrm.workflow_role_id = wr.id AND wrm.user_id = m.user_id
		  WHERE (t.kind <> 'firm' OR m.role = 'admin' OR m.all_clients OR mc.user_id IS NOT NULL)
		    AND m.user_id::text NOT IN (SELECT h.actor FROM invoice_status_history h
		                                 WHERE h.invoice_id = i.id AND h.from_status IS NULL AND h.actor IS NOT NULL)
		    AND m.user_id::text NOT IN (SELECT a.actor FROM audit_log a
		                                 WHERE a.tenant_id = i.tenant_id AND a.invoice_id = i.id AND a.event = 'invoice.updated')
		  GROUP BY i.id`,
		cIDs, cKeys, cAllows, cDelegates)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var holder, delegate, newAny, newHolder, newDelegate bool
		if err := rows.Scan(&id, &holder, &delegate, &newAny, &newHolder, &newDelegate); err != nil {
			rows.Close()
			return nil, err
		}
		st := out[id]
		st.OtherApprover, st.OtherRoleHolder, st.OtherDelegate = true, holder, delegate
		st.OtherNewApprover, st.OtherNewRoleHolder, st.OtherNewDelegate = newAny, newHolder, newDelegate
		out[id] = st
	}
	rows.Close()
	return out, rows.Err()
}
