// document.go: the settled-extraction read (EXTR-06-01, task-761). See
// .ralph/EXTR-06-finalized.md, "The settled-extraction input type".
package importer

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// extractedField is one rank-0 extraction_field_results row: the decided reading.
// Alternatives (candidate_rank >= 1) are never read here -- a human resolves those on the
// review screen (EXTR-15/EXTR-16), not the writer.
type extractedField struct {
	Name   string
	Value  *string // NULL for an unreadable/missing field
	Reason *string // extraction_field_results.reason_code; NULL when the field is clean
}

// SettledExtraction is the newest succeeded extraction job for one document, plus that job's
// decided readings. Fields is never nil.
type SettledExtraction struct {
	JobID    string
	Filename string // documents.filename, "" when the row carries none
	Fields   []extractedField
}

// SettledExtraction reads the newest succeeded extraction_jobs row for documentID plus its
// rank-0 field results, in one db.WithinRequestTenantTx. Neither query names tenant_id --
// tenant_isolation FORCE RLS supplies it (mirrors extraction.jobsForDocumentTx), so a caller in
// another tenant sees zero rows and gets ErrNotFound, not another tenant's data
// (TestRLS_SettledExtractionCrossTenantReadReturnsErrNotFound).
//
// `ORDER BY created_at DESC, id DESC` totalizes the job pick even when two jobs share one
// created_at (TestSettledExtraction_TiedCreatedAtResolvesStablyAcross20Calls). No import of
// internal/extraction (TestImporterPackage_DoesNotImportExtractionPackage) -- that edge would
// drag go-pdfium into cmd/invoice.
func (s *Store) SettledExtraction(ctx context.Context, documentID string) (SettledExtraction, error) {
	ex := SettledExtraction{Fields: []extractedField{}}

	err := db.WithinRequestTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT j.id, coalesce(d.filename, '')
			   FROM extraction_jobs j
			   JOIN documents d ON d.id = j.document_id
			  WHERE j.document_id = $1 AND j.state = 'succeeded'
			  ORDER BY j.created_at DESC, j.id DESC
			  LIMIT 1`,
			documentID,
		).Scan(&ex.JobID, &ex.Filename); err != nil {
			return err
		}

		rows, err := tx.Query(ctx,
			`SELECT field_name, value, reason_code
			   FROM extraction_field_results
			  WHERE extraction_job_id = $1 AND candidate_rank = 0
			  ORDER BY created_at, id`,
			ex.JobID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f extractedField
			if err := rows.Scan(&f.Name, &f.Value, &f.Reason); err != nil {
				return err
			}
			ex.Fields = append(ex.Fields, f)
		}
		return rows.Err()
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SettledExtraction{Fields: []extractedField{}}, ErrNotFound
		}
		return SettledExtraction{Fields: []extractedField{}}, err
	}
	return ex, nil
}

// documentCreateInput maps one SettledExtraction's decided readings to invoice.CreateInput.
// STUB (Mode A, task-762): body not yet implemented -- returns zero values so this package
// compiles while document_map_test.go's RED specs pin the real mapping (EXTR-06-02).
func documentCreateInput(entityID, documentID string, ex SettledExtraction) (invoice.CreateInput, *RowError) {
	return invoice.CreateInput{}, nil
}
