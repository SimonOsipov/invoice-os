package erp

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// DocumentMark is what one linked ERP document says about its invoice.
type DocumentMark struct {
	ConnectionID, System, SourceDocID, State string
	DiffersFromERP                           bool
	ChangedAfterClearanceAt, DeletedInERPAt  *time.Time
}

// InvoiceMarks lists the marks of every document linked to the invoice, in the caller's tenant transaction.
func InvoiceMarks(ctx context.Context, tx pgx.Tx, invoiceID string) ([]DocumentMark, error) {
	rows, err := tx.Query(ctx, `
SELECT d.connection_id::text, c.system, d.source_doc_id, d.state, d.differs_from_erp,
       d.changed_after_clearance_at, d.deleted_in_erp_at
FROM erp_source_documents d JOIN erp_connections c ON c.id = d.connection_id
WHERE d.invoice_id = $1 ORDER BY d.created_at, d.id`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	marks := []DocumentMark{}
	for rows.Next() {
		var m DocumentMark
		if err := rows.Scan(&m.ConnectionID, &m.System, &m.SourceDocID, &m.State, &m.DiffersFromERP,
			&m.ChangedAfterClearanceAt, &m.DeletedInERPAt); err != nil {
			return nil, err
		}
		marks = append(marks, m)
	}
	return marks, rows.Err()
}
