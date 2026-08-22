package archive

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// submissionsCSVHeader: left nil (Mode A, RED) -- the pinned 12-column header
// (invoice_id, invoice_number, submission_job_id, idempotency_key, state, attempts,
// adapter, adapter_version, poll_ref, last_error, created_at, updated_at) lands with
// the implementation. poll_ref appears ONLY here across every CSV (D-10, AC-5).
var submissionsCSVHeader []string

// selectSubmissionsSQL: left empty (Mode A, RED) -- the pinned query
// (invoice_id = ANY($1::uuid[]), ordered invoice_id, created_at, id) lands with the
// implementation.
const selectSubmissionsSQL = ""

// selectSubmissions writes submissions.csv, one row per submission_jobs row -- the
// only place poll_ref appears in any CSV (AC-5). A resubmitted invoice carries more
// than one row, each keyed by its own submission_job_id. Stub for Stage 2.5 (Mode A).
func selectSubmissions(ctx context.Context, tx pgx.Tx, ids []string, w csvWriter) error {
	return errors.New("archive: selectSubmissions not implemented")
}
