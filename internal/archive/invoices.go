package archive

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// csvWriter is the minimal surface selectInvoices needs; *encoding/csv.Writer
// satisfies it directly.
type csvWriter interface {
	Write(record []string) error
}

// invoicesCSVHeader: the 17 invoices.csv columns (D-3 keeps issue_date for a
// regulator to re-filter by; D-8 governs the three fiscal-outcome columns).
var invoicesCSVHeader = []string{
	"invoice_id", "invoice_number", "status", "issue_date", "currency",
	"subtotal", "vat", "total", "supplier_tin", "supplier_name", "buyer_tin", "buyer_name",
	"irn", "csid", "qr_payload", "rejection_reasons", "created_at",
}

// selectInvoices: RED stub (Mode A). Real body lands in Stage 3: normalizeEntityID,
// write the CSV header, stream the period's rows in created_at,id order, emptyIfNil
// and compactJSON per cell.
func selectInvoices(ctx context.Context, tx pgx.Tx, r Request, w csvWriter) ([]string, error) {
	return nil, errRedNotImplemented
}

// countInvoices: RED stub (Mode A). Real body -- normalizeEntityID then a row count
// scoped to the entity and the inclusive period -- lands in Stage 3.
func countInvoices(ctx context.Context, tx pgx.Tx, r Request) (int, error) {
	return -1, errRedNotImplemented
}

// emptyIfNil: RED stub (Mode A). Real body -- nil becomes empty string (D-8), else
// the pointed-to value -- lands in Stage 3.
func emptyIfNil(s *string) string {
	return "__STUB_NOT_IMPLEMENTED__"
}

// compactJSON: RED stub (Mode A). Real body -- json.Compact over raw bytes, never a
// Go slice round-trip, which is what makes the AC-8 nil/omitempty trap unreachable --
// lands in Stage 3.
func compactJSON(raw string) (string, error) {
	return "", errRedNotImplemented
}
