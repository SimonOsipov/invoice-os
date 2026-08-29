// reader.go: the request seam over extraction_jobs. The tenant comes from the verified
// Identity in ctx, never from an argument — the opposite of store.go's worker seam, which is
// why this cannot live in store.go (TestExtractionStore_UsesTenantTxNotRequestTx).
package extraction

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// maxJobsPerDocument bounds a response a client polls every 2s: D-6 permits many jobs per
// document and nothing in the schema caps them.
const maxJobsPerDocument = 50

// JobState is one extraction_jobs row as the progress screen reads it. Every field is a
// column; nothing is derived, so no stage can advance on a timer.
type JobState struct {
	ID         string    `json:"id"`
	DocumentID string    `json:"document_id"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
	LastError  *string   `json:"last_error"`
}

// JobsResponse is the envelope. Jobs is never nil: a nil slice marshals to JSON null, and a
// caller looping over the result breaks on it.
type JobsResponse struct {
	Jobs []JobState `json:"jobs"`
}

// Reader holds the invoice_app pool. Exported field and no constructor, matching Store.
type Reader struct{ Pool *pgxpool.Pool }

// JobsForDocument returns every extraction job for one document, newest first.
func (r *Reader) JobsForDocument(ctx context.Context, documentID string) (JobsResponse, error) {
	jobs := []JobState{}
	if err := db.WithinRequestTenantTx(ctx, r.Pool, func(tx pgx.Tx) error {
		var err error
		jobs, err = jobsForDocumentTx(ctx, tx, documentID)
		return err
	}); err != nil {
		// Discarded on every error, including a commit that failed after the scan: a failed
		// read answers with an empty list, never a partial or uncommitted one.
		return JobsResponse{Jobs: []JobState{}}, err
	}
	return JobsResponse{Jobs: jobs}, nil
}

// jobsForDocumentTx names no tenant_id: the tenant_isolation policy supplies that predicate,
// so writing one by hand would make TestRLS_ExtractionJobsCrossTenantReadRefused prove
// nothing. Returns an empty slice rather than nil on every path — nil marshals to JSON null.
func jobsForDocumentTx(ctx context.Context, tx pgx.Tx, documentID string) ([]JobState, error) {
	out := []JobState{}

	rows, err := tx.Query(ctx,
		`SELECT id, document_id, state, created_at, last_error
		   FROM extraction_jobs
		  WHERE document_id = $1
		  ORDER BY created_at DESC, id DESC
		  LIMIT $2`,
		documentID, maxJobsPerDocument)
	if err != nil {
		return out, fmt.Errorf("extraction: read jobs for document %s: %w", documentID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var j JobState
		if err := rows.Scan(&j.ID, &j.DocumentID, &j.State, &j.CreatedAt, &j.LastError); err != nil {
			return out, fmt.Errorf("extraction: scan job for document %s: %w", documentID, err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("extraction: read jobs for document %s: %w", documentID, err)
	}
	return out, nil
}
