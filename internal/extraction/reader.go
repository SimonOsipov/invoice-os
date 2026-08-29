// reader.go: the request seam over extraction_jobs. The tenant comes from the verified
// Identity in ctx, never from an argument — the opposite of store.go's worker seam, which is
// why this cannot live in store.go (TestExtractionStore_UsesTenantTxNotRequestTx).
//
// STUB — EXTR-07-01 Stage 2.5. Only the wire shape is real; JobsForDocument is not
// implemented, so reader_db_test.go is red on its own assertions rather than on a compile
// error. The executor replaces the method body.
package extraction

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	return JobsResponse{Jobs: []JobState{}}, errors.New("extraction: JobsForDocument not implemented")
}
