package invoice

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// CreditedSoFarTx sums the credit notes on originalID, except excludeID ("" excludes none).
func CreditedSoFarTx(ctx context.Context, tx pgx.Tx, originalID, excludeID string) (string, error) {
	var sum string
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(total), 0)::numeric(20,2)::text
		   FROM invoices
		  WHERE original_invoice_id = $1
		    AND document_type = 'credit_note'
		    AND status NOT IN ('draft', 'rejected', 'failed')
		    AND ($2 = '' OR id <> NULLIF($2, '')::uuid)`,
		originalID, excludeID).Scan(&sum)
	return sum, err
}
