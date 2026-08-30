// enqueue.go: the package's ONE sanctioned enqueue surface (EXTR-09).
package extraction

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/queue"
)

// EnqueueExtraction records the business key and inserts the extraction job on tx, so both
// share the caller's transaction and its fate. tx must be tenant-scoped to tenantID.
//
// The key is per-document and the dedupe behind it is permanent, not in-flight: a document
// whose extraction dead-letters is never re-enqueued here
// (TestRLS_EnqueueExtractionRefusesEvenAfterTheJobDeadLetters). Re-extraction is EXTR-17's.
//
// opts stays nil: extractArgs.InsertOpts already routes the job to QueueName, and a non-nil
// opts would replace it.
func EnqueueExtraction(ctx context.Context, tx pgx.Tx, q *queue.Client, tenantID, documentID string) (skipped bool, err error) {
	key := "extract:" + documentID
	return q.EnqueueTx(ctx, tx, tenantID, key, extractArgs{
		TenantID:       tenantID,
		DocumentID:     documentID,
		IdempotencyKey: key,
	}, nil)
}
