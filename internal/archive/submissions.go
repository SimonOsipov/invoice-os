package archive

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// submissionsCSVHeader is the only CSV that carries poll_ref (D-10, AC-5).
var submissionsCSVHeader = []string{
	"invoice_id", "invoice_number", "submission_job_id", "idempotency_key",
	"state", "attempts", "adapter", "adapter_version", "poll_ref", "last_error",
	"created_at", "updated_at",
}

// selectSubmissionsSQL: invoice_number comes from invoiceNumbers, never a JOIN
// against invoices. id breaks a created_at tie within one transaction.
const selectSubmissionsSQL = `
SELECT id, invoice_id, idempotency_key, adapter, adapter_version, state,
       attempts, poll_ref, last_error, created_at, updated_at
  FROM submission_jobs
 WHERE invoice_id = ANY($1::uuid[])
 ORDER BY invoice_id, created_at, id`

// selectSubmissions writes submissions.csv, one row per submission_jobs row -- the
// only place poll_ref appears in any CSV (AC-5). A resubmitted invoice carries more
// than one row, each keyed by its own submission_job_id.
func selectSubmissions(ctx context.Context, tx pgx.Tx, ids []string, w csvWriter) error {
	if err := w.Write(submissionsCSVHeader); err != nil {
		return fmt.Errorf("archive: write submissions.csv header: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	numbers, err := invoiceNumbers(ctx, tx, ids)
	if err != nil {
		return err
	}

	for _, batch := range chunk(ids, 500) {
		rows, err := tx.Query(ctx, selectSubmissionsSQL, batch)
		if err != nil {
			return fmt.Errorf("archive: select submission_jobs: %w", err)
		}
		for rows.Next() {
			var id, invoiceID, idempotencyKey, adapter, adapterVersion, state string
			var attempts int
			var pollRef, lastError *string
			var createdAt, updatedAt time.Time
			if err := rows.Scan(&id, &invoiceID, &idempotencyKey, &adapter, &adapterVersion, &state,
				&attempts, &pollRef, &lastError, &createdAt, &updatedAt); err != nil {
				rows.Close()
				return fmt.Errorf("archive: scan submission_jobs row: %w", err)
			}
			record := []string{
				invoiceID,
				numbers[invoiceID],
				id,
				idempotencyKey,
				state,
				strconv.Itoa(attempts),
				adapter,
				adapterVersion,
				emptyIfNil(pollRef),
				emptyIfNil(lastError),
				createdAt.UTC().Format(time.RFC3339Nano),
				updatedAt.UTC().Format(time.RFC3339Nano),
			}
			if err := w.Write(record); err != nil {
				rows.Close()
				return fmt.Errorf("archive: write submissions.csv row: %w", err)
			}
		}
		iterErr := rows.Err()
		rows.Close()
		if iterErr != nil {
			return fmt.Errorf("archive: iterate submission_jobs: %w", iterErr)
		}
	}
	return nil
}
