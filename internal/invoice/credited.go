package invoice

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// CreditedSoFarTx sums the credit notes on originalID that can still reach NRS, except excludeID ("" excludes none).
func CreditedSoFarTx(ctx context.Context, tx pgx.Tx, originalID, excludeID string) (string, error) {
	return "", nil
}
