// reader.go: the request seam over extraction_jobs, its page-image and field-result children,
// and the documents row they hang from. The tenant comes from the verified Identity in ctx,
// never from an argument — the opposite of store.go's worker seam, which is why this cannot
// live in store.go (TestExtractionStore_UsesTenantTxNotRequestTx).
package extraction

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// maxJobsPerDocument bounds a response a client polls every 2s
// (frontend/app/src/lib/invoices.ts LIVE_POLL_MS): D-6 permits many jobs
// per document and nothing in the schema caps them.
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

// RecordDocumentRead writes one document.read audit row on the transaction it is handed, so
// the row shares the read's fate. A func value, not an internal/audit import: deps_test.go
// fences this package off everything outside internal/platform/*, and the event name is
// spelled in cmd/submission (TestNewDocumentReadAuditor_SpellsTheEventInCmd).
type RecordDocumentRead func(ctx context.Context, tx pgx.Tx, subject, documentID string) error

// Reader holds the invoice_app pool and the audit recorder Detail writes through. Exported
// fields and no constructor, matching Store.
type Reader struct {
	Pool  *pgxpool.Pool
	Audit RecordDocumentRead
}

// JobsForDocument returns every extraction job for one document, newest first.
func (r *Reader) JobsForDocument(ctx context.Context, documentID string) (JobsResponse, error) {
	jobs := []JobState{}
	if err := db.WithinRequestTenantTx(ctx, r.Pool, func(tx pgx.Tx) error {
		var err error
		jobs, err = jobsForDocumentTx(ctx, tx, documentID)
		return err
	}); err != nil {
		// Discarded on every error, the seam's own commit included —
		// TestRLS_ExtractionReaderDiscardsRowsWhenTheCommitFails.
		return JobsResponse{Jobs: []JobState{}}, err
	}
	return JobsResponse{Jobs: jobs}, nil
}

// jobsForDocumentTx names no tenant_id: the tenant_isolation policy supplies that predicate, and
// a hand-written one would leave TestRLS_ExtractionJobsCrossTenantReadRefused proving nothing —
// TestExtractionReader_QueryNamesDocumentIDNotTenantID. Returns an empty slice rather than nil
// on every path: nil marshals to JSON null.
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

// ErrNotFound is the one answer for a job that is absent and for one belonging to another
// tenant, so a refused read never confirms that an id exists
// (TestExtractionDetail_AbsentJobAndForeignJobAreIndistinguishable). JobsForDocument never
// returns it (TestExtractionJobsForDocument_NeverReturnsErrNotFound).
var ErrNotFound = errors.New("extraction: not found")

// ExtractionRegion is one normalised box on the wire. Top-left origin, page 1-based; a canvas
// scales it by the page's stored width_px/height_px.
type ExtractionRegion struct {
	Page int     `json:"page"`
	X0   float64 `json:"x0"`
	Y0   float64 `json:"y0"`
	X1   float64 `json:"x1"`
	Y1   float64 `json:"y1"`
}

// ExtractionPage is one extraction_page_images row minus its storage key: the key is
// server-side only, and the byte route addresses a page by number.
type ExtractionPage struct {
	Page     int `json:"page"`
	WidthPx  int `json:"width_px"`
	HeightPx int `json:"height_px"`
}

// ExtractionCandidate is one alternative reading kept beside an ambiguous field's decision. No
// Name, Reason or Alternatives of its own: FieldResult (reconcile.go:27-34) gives an alternative
// none of the three, and a recursive ExtractionFieldState would let the wire express a nesting
// the domain never produces.
type ExtractionCandidate struct {
	Value  *string           `json:"value"`
	Region *ExtractionRegion `json:"region"`
}

// ExtractionFieldState is one decided reading plus the alternatives an ambiguous field kept.
// Region is nil when the extractor could point at nothing. Reason is "" for a clean field and
// Alternatives is never nil (TestExtractionDetail_AlternativesAreNeverNil).
type ExtractionFieldState struct {
	Name         string                `json:"name"`
	Value        *string               `json:"value"`
	Region       *ExtractionRegion     `json:"region"`
	Reason       string                `json:"reason"`
	Alternatives []ExtractionCandidate `json:"alternatives"`
}

// ExtractionDocument is what the document toolbar renders. Filename and ContentType are
// nullable columns; StoredAt is RFC3339 text, not a time, so the wire shape is fixed here
// rather than by the marshaller.
type ExtractionDocument struct {
	Filename    *string `json:"filename"`
	ContentType *string `json:"content_type"`
	SizeBytes   int64   `json:"size_bytes"`
	StoredAt    string  `json:"stored_at"`
}

// ExtractionDetail is one job with the document metadata, page inventory and decided readings
// the review screen draws. These are wire structs, not the domain Field/Region: those carry no
// tags and FieldResult embeds Field, which wireMirrors.test.ts's extractor cannot read.
type ExtractionDetail struct {
	ID         string                 `json:"id"`
	DocumentID string                 `json:"document_id"`
	State      string                 `json:"state"`
	Document   ExtractionDocument     `json:"document"`
	Pages      []ExtractionPage       `json:"pages"`
	Fields     []ExtractionFieldState `json:"fields"`
}

// emptyDetail is what every failure path returns: a nil slice marshals to JSON null and every
// consumer loops over these (TestExtractionDetail_PagesAndFieldsAreNeverNil).
func emptyDetail() ExtractionDetail {
	return ExtractionDetail{Pages: []ExtractionPage{}, Fields: []ExtractionFieldState{}}
}

// Detail returns one job with its document, pages and decided fields. All three statements
// share one transaction (TestRLS_ExtractionDetailUsesRequestTxNotTenantTx), and a successful
// read audits on that same transaction (TestRLS_ExtractionDetailWritesOneDocumentReadAuditRow).
func (r *Reader) Detail(ctx context.Context, jobID string) (ExtractionDetail, error) {
	out := emptyDetail()
	if err := db.WithinRequestTenantTx(ctx, r.Pool, func(tx pgx.Tx) error {
		var err error
		if out, err = detailTx(ctx, tx, jobID); err != nil {
			return err
		}
		// A nil recorder writes nothing, which is what lets a bare Reader{Pool} stay at three
		// statements (TestRLS_ExtractionDetailIssuesNoStatementBeyondBeginSelectCommit).
		// TestSubmissionMain_WiresTheDocumentReadAuditorOntoAReader is what keeps production
		// from being one.
		if r.Audit == nil {
			return nil
		}
		// WithinRequestTenantTx already refused a ctx carrying no Identity.
		caller, _ := auth.IdentityFromContext(ctx)
		return r.Audit(ctx, tx, caller.Subject, out.DocumentID)
	}); err != nil {
		return emptyDetail(), err
	}
	return out, nil
}

// detailTx names no tenant_id anywhere: the tenant_isolation policy is the only predicate, and
// a hand-written one would leave TestRLS_ExtractionDetailCrossTenantReadRefused proving nothing
// (TestRLS_ExtractionDetailDocumentJoinNamesNoTenantId). The join is INNER because
// extraction_jobs.document_id is NOT NULL with a composite FK, so the row always exists.
func detailTx(ctx context.Context, tx pgx.Tx, jobID string) (ExtractionDetail, error) {
	out := emptyDetail()

	var storedAt time.Time
	err := tx.QueryRow(ctx,
		`SELECT j.id, j.document_id, j.state,
		        d.filename, d.declared_content_type, d.size_bytes, d.created_at
		   FROM extraction_jobs j
		   JOIN documents d ON d.id = j.document_id
		  WHERE j.id = $1`,
		jobID).Scan(&out.ID, &out.DocumentID, &out.State,
		&out.Document.Filename, &out.Document.ContentType, &out.Document.SizeBytes, &storedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return emptyDetail(), ErrNotFound
	case err != nil:
		return emptyDetail(), fmt.Errorf("extraction: read job %s: %w", jobID, err)
	}
	out.Document.StoredAt = storedAt.UTC().Format(time.RFC3339Nano)

	// Page images are keyed on the document, not the job: two jobs over one document render
	// byte-identical pixels to the same objects.
	if out.Pages, err = detailPagesTx(ctx, tx, out.DocumentID); err != nil {
		return emptyDetail(), err
	}
	if out.Fields, err = detailFieldsTx(ctx, tx, jobID); err != nil {
		return emptyDetail(), err
	}
	return out, nil
}

// detailPagesTx returns the page inventory in page order. The stored grid is read, never
// recomputed from the page size (pdfium.go:154-159).
func detailPagesTx(ctx context.Context, tx pgx.Tx, documentID string) ([]ExtractionPage, error) {
	out := []ExtractionPage{}

	rows, err := tx.Query(ctx,
		`SELECT page_number, width_px, height_px
		   FROM extraction_page_images
		  WHERE document_id = $1
		  ORDER BY page_number`,
		documentID)
	if err != nil {
		return out, fmt.Errorf("extraction: read page images for document %s: %w", documentID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var p ExtractionPage
		if err := rows.Scan(&p.Page, &p.WidthPx, &p.HeightPx); err != nil {
			return []ExtractionPage{}, fmt.Errorf("extraction: scan page image for document %s: %w", documentID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return []ExtractionPage{}, fmt.Errorf("extraction: read page images for document %s: %w", documentID, err)
	}
	return out, nil
}

// detailFieldsTx returns one entry per field_name: candidate_rank 0 is the decision, which is
// what TestExtractionDetail_ExcludesAlternativeCandidates pins -- one wire entry per field, not
// per row. The ordering is fieldResultsTx's.
func detailFieldsTx(ctx context.Context, tx pgx.Tx, jobID string) ([]ExtractionFieldState, error) {
	out := []ExtractionFieldState{}

	rows, err := tx.Query(ctx,
		`SELECT field_name, value, page, bbox_x0, bbox_y0, bbox_x1, bbox_y1
		   FROM extraction_field_results
		  WHERE extraction_job_id = $1 AND candidate_rank = 0
		  ORDER BY created_at, field_name`,
		jobID)
	if err != nil {
		return out, fmt.Errorf("extraction: read field results for job %s: %w", jobID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			f              ExtractionFieldState
			page           *int
			x0, y0, x1, y1 *float64
		)
		if err := rows.Scan(&f.Name, &f.Value, &page, &x0, &y0, &x1, &y1); err != nil {
			return []ExtractionFieldState{}, fmt.Errorf("extraction: scan field result for job %s: %w", jobID, err)
		}
		// extraction_field_results_region_complete makes the five box columns all-or-none, so
		// page alone decides whether there is a box.
		if page != nil {
			f.Region = &ExtractionRegion{Page: *page, X0: *x0, Y0: *y0, X1: *x1, Y1: *y1}
		}
		// Coercion is at construction, not by a tag: a nil slice marshals to null.
		f.Alternatives = []ExtractionCandidate{}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return []ExtractionFieldState{}, fmt.Errorf("extraction: read field results for job %s: %w", jobID, err)
	}
	return out, nil
}

// PageImageKey returns the object key for one page of a job's document. The key is SELECTed off
// the row, never rebuilt with PageKey: a rebuilt key would reach object storage with no
// RLS-visible row proving the caller may see it
// (TestRLS_ExtractionPageImageKeySelectsTheStoredKey). An absent page and another tenant's job
// are one answer, the way Detail's are.
//
// Nothing is audited here: one open screen owes one document.read row, from Detail, not one per
// page (TestRLS_ExtractionPageImageWritesNoAuditRow).
func (r *Reader) PageImageKey(ctx context.Context, jobID string, page int) (string, error) {
	var key string
	if err := db.WithinRequestTenantTx(ctx, r.Pool, func(tx pgx.Tx) error {
		// Names no tenant_id: the tenant_isolation policy on both tables is the only predicate
		// (TestRLS_ExtractionDetailDocumentJoinNamesNoTenantId). The join is what turns the
		// route's job id into the document the pages hang from.
		err := tx.QueryRow(ctx,
			`SELECT p.storage_key
			   FROM extraction_page_images p
			   JOIN extraction_jobs j ON j.document_id = p.document_id
			  WHERE j.id = $1 AND p.page_number = $2`,
			jobID, page).Scan(&key)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return fmt.Errorf("extraction: read page %d image key for job %s: %w", page, jobID, err)
		}
		return nil
	}); err != nil {
		return "", err
	}
	return key, nil
}
