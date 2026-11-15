package invoice

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/approval"
)

// DiscardBatchTx discards every draft or validated invoice of the batch in the caller's tx:
// cancels live approval runs, clears the kept marks, writes one history row each. It returns
// the count. The caller holds the invoice locks and has refused a batch with a sent invoice.
func DiscardBatchTx(ctx context.Context, tx pgx.Tx, batchID string, actor Actor) (int, error) {
	rows, err := tx.Query(ctx,
		`UPDATE invoices i SET status = 'discarded', kept_as_is_at = NULL, kept_as_is_by = NULL, kept_as_is_reason = NULL
		   FROM (SELECT id, status FROM invoices WHERE import_batch_id = $1 AND status IN ('draft', 'validated') ORDER BY id) p
		  WHERE i.id = p.id
		RETURNING i.id, i.invoice_number, p.status`, batchID)
	if err != nil {
		return 0, err
	}
	type discarded struct{ id, number, from string }
	var done []discarded
	for rows.Next() {
		var d discarded
		if err := rows.Scan(&d.id, &d.number, &d.from); err != nil {
			rows.Close()
			return 0, err
		}
		done = append(done, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, d := range done {
		if _, err := approval.CancelLiveRunTx(ctx, tx, d.id, d.number, actor.Subject); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor)
			 VALUES ($1, $2, $3, 'discarded', $4)`,
			actor.TenantID, d.id, d.from, actor.Subject); err != nil {
			return 0, err
		}
	}
	return len(done), nil
}
