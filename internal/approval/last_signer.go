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

// RefuseUnsignedWaitingRolesTx returns a lastSigner *roleInUse when a live role in keys has no signer
// while an open run waits on it. APSC-14 changes the definition in unsignedWaitingInvoicesTx, not here.
func RefuseUnsignedWaitingRolesTx(ctx context.Context, tx pgx.Tx, keys []string) error {
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
		rows, err := tx.Query(ctx,
			`SELECT m.user_id::text
			   FROM workflow_role_members m
			   JOIN workflow_roles w ON w.id = m.workflow_role_id
			  WHERE w.key = $1 AND w.deleted_at IS NULL
			  ORDER BY m.ord`, key)
		if err != nil {
			return err
		}
		members, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		refs, n, err := unsignedWaitingInvoicesTx(ctx, tx, key, members)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			ref.RoleKey, ref.RoleTitle = key, title
			invoices = append(invoices, ref)
		}
		total += n
	}
	if total == 0 {
		return nil
	}
	if len(invoices) > roleInRunListCap {
		invoices = invoices[:roleInRunListCap]
	}
	return &roleInUse{lastSigner: true, invoices: invoices, invoicesTotal: total}
}
