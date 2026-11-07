package approval

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// StaffedRoleKeysTx lists, in key order, the live roles subject has a member row in, whatever its status or access role.
func StaffedRoleKeysTx(ctx context.Context, tx pgx.Tx, subject string) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT w.key
		   FROM workflow_role_members m
		   JOIN workflow_roles w ON w.id = m.workflow_role_id
		  WHERE m.user_id = $1 AND w.deleted_at IS NULL
		  ORDER BY w.key`, subject)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if keys == nil {
		keys = []string{}
	}
	return keys, nil
}

// SignedStep is one waiting invoice step the target could sign before a member write.
type SignedStep struct {
	invoiceID, number, roleKey, roleTitle string
	allow                                 bool
	delegate                              *string
}

// signedWaitingSQL lists the pending approval steps the target could sign by kind: a member of the live role
// (a join, never EXISTS), or a delegate. Keys in $2 are judged by the role loop.
const signedWaitingSQL = `-- SignedWaitingStepsTx
SELECT i.id, i.invoice_number, s.workflow_role_key, COALESCE(wr.title, ''), s.allow_delegation, s.delegate_user_id::text
  FROM approval_runs r
  JOIN invoices i ON i.tenant_id = r.tenant_id AND i.id = r.invoice_id
  JOIN approval_run_steps s ON s.tenant_id = r.tenant_id AND s.run_id = r.id
       AND s.kind = 'approval' AND s.state = 'pending'
  LEFT JOIN workflow_roles wr ON wr.tenant_id = s.tenant_id AND wr.key = s.workflow_role_key
  LEFT JOIN workflow_role_members wrm ON wrm.tenant_id = wr.tenant_id AND wrm.workflow_role_id = wr.id
       AND wrm.user_id = $1::uuid AND wr.deleted_at IS NULL
 WHERE r.state = 'open' AND i.status = 'validated'
   AND (s.workflow_role_key IS NULL OR NOT (s.workflow_role_key = ANY($2::text[])))
 GROUP BY r.id, r.opened_at, i.id, s.id, wr.id
HAVING bool_or(wrm.user_id IS NOT NULL)
    OR (s.allow_delegation AND (s.delegate_user_id IS NULL OR s.delegate_user_id::text = $1::text))
 ORDER BY r.opened_at, i.id, s.ord`

// SignedWaitingStepsTx collects, before a member write, the waiting steps outside keys that target can sign
// as a role member, a named delegate or an "anyone" delegate. Neither read locks.
// ceiling: reads every waiting "anyone" step in the workspace; filter by the target's clients above ~1k waiting runs
func SignedWaitingStepsTx(ctx context.Context, tx pgx.Tx, target string, keys []string) ([]SignedStep, error) {
	if keys == nil {
		keys = []string{}
	}
	rows, err := tx.Query(ctx, signedWaitingSQL, target, keys)
	if err != nil {
		return nil, err
	}
	steps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SignedStep, error) {
		var st SignedStep
		var key *string
		err := row.Scan(&st.invoiceID, &st.number, &key, &st.roleTitle, &st.allow, &st.delegate)
		if key != nil {
			st.roleKey = *key
		}
		return st, err
	})
	if err != nil || len(steps) == 0 {
		return nil, err
	}
	probes := make([]SignerProbe, len(steps))
	for i, st := range steps {
		probes[i] = SignerProbe{InvoiceID: st.invoiceID, AllowDelegation: st.allow, DelegateUserID: st.delegate}
		if st.roleKey != "" {
			k := st.roleKey
			probes[i].RoleKey = &k
		}
	}
	_, signs, err := signerStandingTx(ctx, tx, probes, target)
	if err != nil {
		return nil, err
	}
	var kept []SignedStep
	for i, ok := range signs {
		if ok {
			kept = append(kept, steps[i])
		}
	}
	return kept, nil
}

// RefuseUnsignedWaitingRolesTx returns a lastSigner *roleInUse when an open run waits on a live role in keys
// and nobody can approve that invoice. Who can sign is judged by unsignableReasonsTx, via unsignedWaitingInvoicesTx.
func RefuseUnsignedWaitingRolesTx(ctx context.Context, tx pgx.Tx, keys []string, signed []SignedStep) error {
	var invoices []InvoiceRef
	var total int
	// ceiling: one unsignedWaitingInvoicesTx per staffed role; batch it above ~20 roles per member
	for _, key := range keys {
		var title string
		err := tx.QueryRow(ctx,
			`SELECT title FROM workflow_roles WHERE key = $1 AND deleted_at IS NULL`, key).Scan(&title)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		refs, n, err := unsignedWaitingInvoicesTx(ctx, tx, key)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			ref.RoleKey, ref.RoleTitle = key, title
			invoices = append(invoices, ref)
		}
		total += n
	}
	// Steps the target could sign outside its roles: one item per invoice and role key.
	if len(signed) > 0 {
		probes := make([]SignerProbe, len(signed))
		for i, st := range signed {
			probes[i] = SignerProbe{InvoiceID: st.invoiceID, AllowDelegation: st.allow, DelegateUserID: st.delegate}
			if st.roleKey != "" {
				k := st.roleKey
				probes[i].RoleKey = &k
			}
		}
		reasons, err := unsignableReasonsTx(ctx, tx, probes)
		if err != nil {
			return err
		}
		seen := map[[2]string]bool{}
		for i, r := range reasons {
			st := signed[i]
			if r == "" || seen[[2]string{st.invoiceID, st.roleKey}] {
				continue
			}
			seen[[2]string{st.invoiceID, st.roleKey}] = true
			invoices = append(invoices, InvoiceRef{ID: st.invoiceID, Number: st.number, RoleKey: st.roleKey, RoleTitle: st.roleTitle})
			total++
		}
	}
	if total == 0 {
		return nil
	}
	return &roleInUse{lastSigner: true, invoices: capInvoiceRefs(invoices), invoicesTotal: total}
}
