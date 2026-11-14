package invoice

// SECAUD-50-03 red shim: lets discard_test.go compile before the product code exists.
// DELETE THIS FILE when internal/invoice/discard.go declares DiscardBatchTx.

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func DiscardBatchTx(ctx context.Context, tx pgx.Tx, batchID string, actor Actor) (int, error) {
	return 0, nil
}
