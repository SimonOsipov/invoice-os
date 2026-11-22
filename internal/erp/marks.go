package erp

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// DocumentMark is what one linked ERP document says about its invoice. Stub: the executor replaces this file whole.
type DocumentMark struct {
	ConnectionID, System, SourceDocID, State string
	DiffersFromERP                           bool
	ChangedAfterClearanceAt, DeletedInERPAt  *time.Time
}

func InvoiceMarks(ctx context.Context, tx pgx.Tx, invoiceID string) ([]DocumentMark, error) {
	return nil, nil
}
