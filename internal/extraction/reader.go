// reader.go: the request seam over extraction_jobs, its page-image and field-result children,
// and the documents row they hang from. The tenant comes from the verified Identity in ctx,
// never from an argument — the opposite of store.go's worker seam, which is why this cannot
// live in store.go (TestExtractionStore_UsesTenantTxNotRequestTx).
package extraction

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
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

// ExtractionCorrected is the human layer over one field: how it was settled, the reading the
// correction superseded, and the anchor label it was taken from. Named, never inline:
// wireMirrors' goStructKeys reads a brace-free body only. Was and Where are both pointers --
// the superseded reading may be a field the extractor never read, and most corrections carry
// no anchor label.
type ExtractionCorrected struct {
	Method string  `json:"method"`
	Was    *string `json:"was"`
	Where  *string `json:"where"`
}

// ExtractionFieldState is one decided reading plus the alternatives an ambiguous field kept,
// with the human layer over both. Region is nil when the extractor could point at nothing.
// Reason is "" for a clean field and Alternatives is never nil
// (TestExtractionDetail_AlternativesAreNeverNil). Corrected is nil, never an empty object, for
// a field no human has touched (TestExtractionDetail_UncorrectedFieldHasNullCorrected).
type ExtractionFieldState struct {
	Name         string                `json:"name"`
	Value        *string               `json:"value"`
	Region       *ExtractionRegion     `json:"region"`
	Reason       string                `json:"reason"`
	Alternatives []ExtractionCandidate `json:"alternatives"`
	Corrected    *ExtractionCorrected  `json:"corrected"`
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

// Detail returns one job with its document, pages and merged fields. All four statements
// share one transaction (TestRLS_ExtractionDetailUsesRequestTxNotTenantTx), and a successful
// read audits on that same transaction (TestRLS_ExtractionDetailWritesOneDocumentReadAuditRow).
func (r *Reader) Detail(ctx context.Context, jobID string) (ExtractionDetail, error) {
	out := emptyDetail()
	if err := db.WithinRequestTenantTx(ctx, r.Pool, func(tx pgx.Tx) error {
		var err error
		if out, err = detailTx(ctx, tx, jobID); err != nil {
			return err
		}
		// A nil recorder writes nothing, which is what lets a bare Reader{Pool} stay at four
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
	// Fourth and last, on the same transaction: the human layer laid over the readings above.
	corrections, err := latestCorrectionsPerFieldTx(ctx, tx, jobID)
	if err != nil {
		return emptyDetail(), err
	}
	out.Fields = mergeCorrections(out.Fields, corrections)
	return out, nil
}

// mergeCorrections lays the latest correction per field over the decided readings, one entry per
// field name (TestExtractionMerge_ResolvesEachMethodWithoutADatabase). A settled field is no
// longer flagged, so its reason goes to "" and its alternatives to empty. The line-items row is
// the one correction that also settles fields it does not name: see expandLineCorrection.
func mergeCorrections(fields []ExtractionFieldState, corrections []Correction) []ExtractionFieldState {
	byName := map[string]int{}
	for i, f := range fields {
		byName[f.Name] = i
	}
	out := make([]ExtractionFieldState, len(fields))
	copy(out, fields)

	// A correction may name a field the extractor never read: refuseField admits names
	// mockDefaultResult never emits, and the value already reached the invoice.
	added := []ExtractionFieldState{}
	for _, c := range corrections {
		// An undo is a full reset to the extractor's reading, its own value ignored
		// (TestExtractionDetail_UndoneIgnoresItsOwnValueAndRestoresTheReading).
		if c.Method == MethodUndone {
			continue
		}

		f := ExtractionFieldState{Name: c.FieldName, Alternatives: []ExtractionCandidate{}}
		idx, read := byName[c.FieldName]
		if read {
			f = out[idx]
		}

		// Read before Value is overwritten: the first correction on a field supersedes the
		// reading, which may itself be a missing field with no value.
		was := c.Superseded
		if was == nil {
			was = f.Value
		}

		value := c.Value
		f.Value = &value
		switch c.Method {
		case MethodChosen:
			// _pointed_has_region forbids a box on a chosen row, so the alternative is re-derived by
			// value — before the alternatives are emptied below, or it is unrecoverable
			// (TestExtractionDetail_ChosenCorrectionTakesTheAlternativesRegion).
			for _, a := range f.Alternatives {
				if a.Value != nil && *a.Value == c.Value {
					f.Region = a.Region
					break
				}
			}
		case MethodPointed:
			if c.Region != nil { // _pointed_has_region ties the stored box to this method exactly
				f.Region = &ExtractionRegion{
					Page: c.Region.Page, X0: c.Region.X0, Y0: c.Region.Y0, X1: c.Region.X1, Y1: c.Region.Y1,
				}
			}
		}
		f.Reason = ""
		f.Alternatives = []ExtractionCandidate{}

		// "" is no label, not an empty one: the wire key is nullable so subtask 06 never renders a
		// dangling "Taken from " (TestExtractionDetail_WhereCarriesTheAnchorLabelAndIsNullWithoutOne).
		var where *string
		if c.AnchorLabel != "" {
			label := c.AnchorLabel
			where = &label
		}
		f.Corrected = &ExtractionCorrected{Method: string(c.Method), Was: was, Where: where}

		if read {
			out[idx] = f
			continue
		}
		added = append(added, f)
	}

	// Appended after the read fields and ordered here, not by the query's ORDER BY, so the output
	// order does not rest on the caller. The corrections read is one row per field name, so no tie.
	slices.SortFunc(added, func(a, b ExtractionFieldState) int { return cmp.Compare(a.Name, b.Name) })
	return expandLineCorrection(append(out, added...), corrections)
}

// expandLineCorrection projects the line-items correction -- one row carrying the whole set as
// canonical JSON, appended under the block name "line_items" -- onto the per-cell readings named
// line_items[N].<role>, which no correction ever names directly. Without it a saved line edit
// reads back as the extractor's own reading on every reopen. The block row itself is merged
// above like any other field.
//
// Accepted residual, EXTR-14's to close: the posted set is positional, so after a removal the
// rows below inherit the box of the reading at their NEW position -- right values, one stale
// highlight per shifted row. The POST carries no per-cell identity to fix it with.
func expandLineCorrection(fields []ExtractionFieldState, corrections []Correction) []ExtractionFieldState {
	var corr *Correction
	for i := range corrections {
		if corrections[i].FieldName == "line_items" && corrections[i].Method != MethodUndone {
			corr = &corrections[i]
		}
	}
	if corr == nil {
		return fields
	}
	// A value this read cannot parse leaves every reading untouched: a stale grid is visible,
	// a silently emptied one is not.
	lines, ok := parseLineItemsJSON(corr.Value)
	if !ok {
		return fields
	}
	var prior []LineItemInput
	if corr.Superseded != nil {
		prior, _ = parseLineItemsJSON(*corr.Superseded)
	}

	// The replaced readings are kept by name for their boxes, and the expansion goes back in
	// where the first of them sat -- head, expansion, tail -- so an edit that changes no name
	// leaves the wire order as it was (mock_lines_qa_db_test.go is about what that order costs).
	read := map[string]ExtractionFieldState{}
	head := make([]ExtractionFieldState, 0, len(fields))
	tail := []ExtractionFieldState{}
	sawCell := false
	for _, f := range fields {
		if _, _, isCell := ParseLineFieldName(f.Name); isCell {
			read[f.Name] = f
			sawCell = true
			continue
		}
		if sawCell {
			tail = append(tail, f)
			continue
		}
		head = append(head, f)
	}

	expanded := make([]ExtractionFieldState, 0, len(lines)*len(LineRoles))
	for i, line := range lines {
		for _, role := range LineRoles {
			cell := line.cell(role)
			if cell == nil {
				continue // a null cell is an absence, never a row carrying an empty value
			}
			name := LineFieldName(i+1, role)
			value := *cell
			reading := read[name]
			// The same rule the per-field merge follows: the superseded blob's own cell, and
			// the reading only when this is the field's first correction.
			was := priorLineCell(prior, i, role)
			if was == nil {
				was = reading.Value
			}
			expanded = append(expanded, ExtractionFieldState{
				Name:         name,
				Value:        &value,
				Region:       reading.Region,
				Reason:       "",
				Alternatives: []ExtractionCandidate{},
				Corrected:    &ExtractionCorrected{Method: string(corr.Method), Was: was},
			})
		}
	}
	out := make([]ExtractionFieldState, 0, len(head)+len(expanded)+len(tail))
	out = append(out, head...)
	out = append(out, expanded...)
	return append(out, tail...)
}

// priorLineCell is the cell the correction before this one carried at the same position, nil
// when there was no prior set or it was shorter.
func priorLineCell(prior []LineItemInput, i int, role string) *string {
	if i >= len(prior) {
		return nil
	}
	return prior[i].cell(role)
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

// detailFieldsTx returns one entry per field_name: candidate_rank 0 is the decision and ranks
// 1..N nest under it, so the count is per field and never per row
// (TestExtractionDetail_AlternativesDoNotBecomeTopLevelFields). Every row of one job ties on
// created_at in production, so field_name, candidate_rank and id break it
// (TestExtractionDetail_AlternativeOrderSurvivesACreatedAtTie).
func detailFieldsTx(ctx context.Context, tx pgx.Tx, jobID string) ([]ExtractionFieldState, error) {
	type fieldRow struct {
		name           string
		value          *string
		page           *int
		x0, y0, x1, y1 *float64
		reason         *string
		rank           int
	}

	out := []ExtractionFieldState{}

	rows, err := tx.Query(ctx,
		`SELECT field_name, value, page, bbox_x0, bbox_y0, bbox_x1, bbox_y1, reason_code, candidate_rank
		   FROM extraction_field_results
		  WHERE extraction_job_id = $1
		  ORDER BY created_at, field_name, candidate_rank, id`,
		jobID)
	if err != nil {
		return out, fmt.Errorf("extraction: read field results for job %s: %w", jobID, err)
	}
	defer rows.Close()

	buf := []fieldRow{}
	for rows.Next() {
		var r fieldRow
		if err := rows.Scan(&r.name, &r.value, &r.page,
			&r.x0, &r.y0, &r.x1, &r.y1, &r.reason, &r.rank); err != nil {
			return []ExtractionFieldState{}, fmt.Errorf("extraction: scan field result for job %s: %w", jobID, err)
		}
		buf = append(buf, r)
	}
	if err := rows.Err(); err != nil {
		return []ExtractionFieldState{}, fmt.Errorf("extraction: read field results for job %s: %w", jobID, err)
	}

	// extraction_field_results_region_complete makes the five box columns all-or-none, so page
	// alone decides whether there is a box.
	region := func(r fieldRow) *ExtractionRegion {
		if r.page == nil {
			return nil
		}
		return &ExtractionRegion{Page: *r.page, X0: *r.x0, Y0: *r.y0, X1: *r.x1, Y1: *r.y1}
	}

	// Two passes so a rank-0 row need not precede its own alternatives in the buffer.
	byName := map[string]int{}
	for _, r := range buf {
		if r.rank != 0 {
			continue
		}
		reason := ""
		if r.reason != nil {
			reason = *r.reason
		}
		byName[r.name] = len(out)
		out = append(out, ExtractionFieldState{
			Name:   r.name,
			Value:  r.value,
			Region: region(r),
			Reason: reason,
			// Coercion is at construction, not by a tag: a nil slice marshals to null.
			Alternatives: []ExtractionCandidate{},
		})
	}
	for _, r := range buf {
		if r.rank == 0 {
			continue
		}
		// An alternative with no rank-0 sibling is dropped, never promoted
		// (TestExtractionDetail_OrphanAlternativeIsDropped).
		idx, ok := byName[r.name]
		if !ok {
			continue
		}
		out[idx].Alternatives = append(out[idx].Alternatives,
			ExtractionCandidate{Value: r.value, Region: region(r)})
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
