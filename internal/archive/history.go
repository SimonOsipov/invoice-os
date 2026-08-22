package archive

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// historyCSVHeader: pinned by AUDIT-05-04's Stage 1 architecture (D-9). Left empty
// here on purpose (Mode A, RED) -- the real 8-column header lands with the
// implementation. TestHistoryHeader_CarriesNoRawSubjectColumn fails on this.
var historyCSVHeader []string

// selectHistorySQL: left empty on purpose (Mode A, RED) -- invoice_number comes
// from invoiceNumbers, never a JOIN against invoices (see
// TestHistorySQL_ContainsNoJoinAgainstInvoices). The real query lands with the
// implementation.
const selectHistorySQL = ""

// selectHistory streams invoice_status_history for ids as status_history.csv.
// Stub for Stage 2.5 (Mode A) -- body lands with the implementation subtask.
func selectHistory(ctx context.Context, tx pgx.Tx, ids []string, w csvWriter) error {
	return errors.New("archive: selectHistory not implemented")
}

// invoiceNumbers looks up id -> invoice_number by primary key, one query, not
// chunked. Stub for Stage 2.5 (Mode A) -- body lands with the implementation
// subtask.
func invoiceNumbers(ctx context.Context, tx pgx.Tx, ids []string) (map[string]string, error) {
	return nil, errors.New("archive: invoiceNumbers not implemented")
}
