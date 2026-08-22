package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

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

// issue_date needs an explicit ::text cast — pgx errors scanning date into *string
// without it. numeric and jsonb scan into *string/string fine, no cast needed.
const selectInvoicesSQL = `
SELECT id, invoice_number, status, issue_date::text, currency, subtotal, vat, total,
       supplier_tin, supplier_name, buyer_tin, buyer_name, irn, csid, qr_payload, rejection_reasons, created_at
  FROM invoices WHERE entity_id = $1 AND created_at >= $2 AND created_at <= $3 ORDER BY created_at, id`

// selectInvoices streams the entity's invoices for the period as invoices.csv, no
// WHERE tenant_id — FORCE RLS + app.current_tenant is the sole isolation.
func selectInvoices(ctx context.Context, tx pgx.Tx, r Request, w csvWriter) ([]string, error) {
	canonical, err := normalizeEntityID(r.EntityID)
	if err != nil {
		return nil, err
	}
	if err := w.Write(invoicesCSVHeader); err != nil {
		return nil, fmt.Errorf("archive: write invoices.csv header: %w", err)
	}
	rows, err := tx.Query(ctx, selectInvoicesSQL, canonical, r.From, r.To)
	if err != nil {
		return nil, fmt.Errorf("archive: select invoices: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id, invoiceNumber, status, rejectionReasons string
		var issueDate, currency, subtotal, vat, total, supplierTIN, supplierName, buyerTIN, buyerName, irn, csid, qrPayload *string
		var createdAt time.Time
		if err := rows.Scan(&id, &invoiceNumber, &status, &issueDate, &currency, &subtotal, &vat, &total,
			&supplierTIN, &supplierName, &buyerTIN, &buyerName, &irn, &csid, &qrPayload, &rejectionReasons, &createdAt); err != nil {
			return nil, fmt.Errorf("archive: scan invoice row: %w", err)
		}
		compact, err := compactJSON(rejectionReasons)
		if err != nil {
			return nil, fmt.Errorf("archive: compact rejection_reasons for invoice %s: %w", id, err)
		}
		record := []string{id, invoiceNumber, status, emptyIfNil(issueDate), emptyIfNil(currency),
			emptyIfNil(subtotal), emptyIfNil(vat), emptyIfNil(total), emptyIfNil(supplierTIN), emptyIfNil(supplierName),
			emptyIfNil(buyerTIN), emptyIfNil(buyerName), emptyIfNil(irn), emptyIfNil(csid), emptyIfNil(qrPayload),
			compact, createdAt.UTC().Format(time.RFC3339Nano)}
		if err := w.Write(record); err != nil {
			return nil, fmt.Errorf("archive: write invoices.csv row %s: %w", id, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("archive: iterate invoices: %w", err)
	}
	return ids, nil
}

// countInvoices returns the exact row count selectInvoices would return for the
// same entity+period (D-14 cap check, subtask-09 preview).
func countInvoices(ctx context.Context, tx pgx.Tx, r Request) (int, error) {
	canonical, err := normalizeEntityID(r.EntityID)
	if err != nil {
		return 0, err
	}
	var n int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE entity_id = $1 AND created_at >= $2 AND created_at <= $3`,
		canonical, r.From, r.To).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("archive: count invoices: %w", err)
	}
	return n, nil
}

// emptyIfNil: NULL -> empty CSV cell (D-8); encoding/csv never quotes a genuinely empty field.
func emptyIfNil(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// compactJSON strips jsonb's incidental spacing (Postgres prints a space after every
// ":"/","). Operates on raw bytes, never a Go slice round-trip, so the []T/omitempty
// nil-vs-null trap (AC-8) is structurally unreachable here.
func compactJSON(raw string) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		return "", fmt.Errorf("archive: compact json %q: %w", raw, err)
	}
	return buf.String(), nil
}
