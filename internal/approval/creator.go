package approval

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The two refusal sentences, verbatim from the story's [message copy].
const (
	CreatorRefusedReason      = "You created this invoice, so another approver must approve it."
	CreatorNoRoleHolderReason = "No other approver holds the role this step is waiting on. An admin must give one of them that role."
)

var ErrCreatorRefused = errors.New("approval: the creator cannot approve their own invoice")

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
	IsCreator       bool
	OtherApprover   bool
	OtherRoleHolder bool
	// OtherDelegate: another approver the step's delegation lets decide (DelegateMayDecide).
	OtherDelegate bool
}

// Refusal is the one rule: the creator is refused when another approver reaches the client.
func (s CreatorStanding) Refusal() (reason string, refused bool) {
	if !s.IsCreator || !s.OtherApprover {
		return "", false
	}
	if !s.OtherRoleHolder && !s.OtherDelegate {
		return CreatorRefusedReason + " " + CreatorNoRoleHolderReason, true
	}
	return CreatorRefusedReason, true
}

// SelfApproved reports a creator approving with no other approver on the client.
func (s CreatorStanding) SelfApproved() bool { return s.IsCreator && !s.OtherApprover }

// CreatorStandingTx reads subject's creator standing for each probe. A non-creator
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
		`SELECT h.invoice_id::text
		   FROM invoice_status_history h
		  WHERE h.invoice_id = ANY($1::uuid[]) AND h.from_status IS NULL AND h.actor = $2`,
		ids, subject)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = CreatorStanding{IsCreator: true}
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
		        bool_or(p.allow_delegation AND (p.delegate_user_id IS NULL OR p.delegate_user_id = m.user_id::text))
		   FROM unnest($1::uuid[], $3::text[], $4::boolean[], $5::text[]) AS p(invoice_id, role_key, allow_delegation, delegate_user_id)
		   JOIN invoices i ON i.id = p.invoice_id
		   JOIN memberships m ON m.status = 'active' AND m.role IN ('admin', 'reviewer') AND m.user_id <> $2
		   JOIN tenants t ON t.id = m.tenant_id
		   LEFT JOIN membership_clients mc ON mc.user_id = m.user_id AND mc.entity_id = i.entity_id
		   LEFT JOIN workflow_roles wr ON wr.key = p.role_key AND wr.deleted_at IS NULL
		   LEFT JOIN workflow_role_members wrm ON wrm.workflow_role_id = wr.id AND wrm.user_id = m.user_id
		  WHERE t.kind <> 'firm' OR m.role = 'admin' OR m.all_clients OR mc.user_id IS NOT NULL
		  GROUP BY i.id`,
		cIDs, subject, cKeys, cAllows, cDelegates)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var holder, delegate bool
		if err := rows.Scan(&id, &holder, &delegate); err != nil {
			rows.Close()
			return nil, err
		}
		out[id] = CreatorStanding{IsCreator: true, OtherApprover: true, OtherRoleHolder: holder, OtherDelegate: delegate}
	}
	rows.Close()
	return out, rows.Err()
}
