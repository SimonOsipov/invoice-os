// enqueue.go: the package's ONE sanctioned enqueue surface (EXTR-09).
package extraction

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// EnqueueExtraction records the business key and inserts the extraction job on tx, so both
// share the caller's transaction and its fate. tx must be tenant-scoped to tenantID.
//
// Stage-2.5 stub: the body lands with the implementation.
func EnqueueExtraction(ctx context.Context, tx pgx.Tx, q *queue.Client, tenantID, documentID string) (skipped bool, err error) {
	return false, errors.New("extraction: EnqueueExtraction not implemented")
}
