package archive

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
)

var lineItemsCSVHeader = []string{
	"invoice_id", "invoice_number", "line_no", "description", "quantity", "unit_price", "line_total", "line_tax",
	"tax_category", "hsn_code", "isic_code", "product_category", "service_category",
	"sellers_item_identification", "price_unit", "tax_percent", "base_quantity",
}

// lineItemsScope: the FROM/WHERE selectLineItemsSQL and countLineItemsSQL share.
const lineItemsScope = `
  FROM line_items
 WHERE invoice_id = ANY($1::uuid[])`

// selectLineItemsSQL: invoice_number comes from invoiceNumbers, never a JOIN against
// invoices (see TestLineItemsSQL_ContainsNoJoinAgainstInvoices). Numerics read ::text.
const selectLineItemsSQL = `
SELECT invoice_id, line_no, description, quantity::text, unit_price::text, line_total::text, line_tax::text,
       tax_category, hsn_code, isic_code, product_category, service_category,
       sellers_item_identification, price_unit, tax_percent::text, base_quantity::text` +
	lineItemsScope + `
 ORDER BY invoice_id, line_no`

// countLineItemsSQL backs the preview's line_items count.
const countLineItemsSQL = `SELECT count(*)` + lineItemsScope

// selectLineItems streams line_items for ids as line_items.csv, one row in memory at a time.
func selectLineItems(ctx context.Context, tx pgx.Tx, ids []string, w csvWriter) error {
	if err := w.Write(lineItemsCSVHeader); err != nil {
		return fmt.Errorf("archive: write line_items.csv header: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	numbers, err := invoiceNumbers(ctx, tx, ids)
	if err != nil {
		return err
	}

	// Canonical lowercase uuid text sorts like Postgres's byte order, so chunks concatenate in ORDER BY invoice_id order.
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	for _, batch := range chunk(sorted, 500) {
		if err := writeLineItemsBatch(ctx, tx, batch, numbers, w); err != nil {
			return err
		}
	}
	return nil
}

func writeLineItemsBatch(ctx context.Context, tx pgx.Tx, batch []string, numbers map[string]string, w csvWriter) error {
	rows, err := tx.Query(ctx, selectLineItemsSQL, batch)
	if err != nil {
		return fmt.Errorf("archive: select line_items: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var invoiceID string
		var lineNo int
		cells := make([]*string, 14)
		dest := []any{&invoiceID, &lineNo}
		for i := range cells {
			dest = append(dest, &cells[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("archive: scan line_items row: %w", err)
		}
		record := []string{invoiceID, numbers[invoiceID], strconv.Itoa(lineNo)}
		for _, c := range cells {
			record = append(record, emptyIfNil(c))
		}
		if err := w.Write(record); err != nil {
			return fmt.Errorf("archive: write line_items.csv row: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("archive: iterate line_items: %w", err)
	}
	return nil
}
