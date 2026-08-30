// document.go: the settled-extraction read (EXTR-06-01, task-761) and the field-to-CreateInput
// mapper (EXTR-06-02, task-762). See .ralph/EXTR-06-finalized.md, "The settled-extraction input
// type".
package importer

import (
	"context"
	"errors"
	"strings"
	"time"

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

// mapperFieldNames is internal/importer's own copy of extraction.HeaderFields, in the same
// order -- internal/importer cannot import internal/extraction (document_deps_test.go /
// SX-09), so nothing compiler-links the two lists; TestDocumentCreateInput_
// MapperFieldNamesMatchesHeaderFieldsInOrder (MAP-11) is the drift guard.
var mapperFieldNames = []string{
	"invoice_number", "issue_date", "supplier_tin", "supplier_name",
	"buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total",
}

// documentCreateInput maps one SettledExtraction's decided readings to invoice.CreateInput.
// Pure. supplier_tin/supplier_name are never set -- Store.Create overwrites both from the
// entity on every write (store.go:220-221, Q11), so writing a value here would state a claim
// the store then silently discards. SourceRows and LineItems stay nil: SourceRows because the
// column CHECKs reject both '{}' and any element < 2, so NULL is the only legal value here;
// LineItems because nothing extracted feeds it yet (D-13).
func documentCreateInput(entityID, documentID string, ex SettledExtraction) (invoice.CreateInput, *RowError) {
	values := make(map[string]*string, len(ex.Fields))
	for _, f := range ex.Fields {
		values[f.Name] = f.Value
	}

	invoiceNumber := ""
	if v := values["invoice_number"]; v != nil {
		invoiceNumber = strings.TrimSpace(*v)
	}
	if invoiceNumber == "" {
		return invoice.CreateInput{}, &RowError{
			Field:   "invoice_number",
			Message: "invoice_number is missing or blank",
		}
	}

	var issueDate *time.Time
	if v := values["issue_date"]; v != nil {
		parsed, err := parseIssueDate(*v)
		if err != nil {
			return invoice.CreateInput{}, &RowError{
				Field:   "issue_date",
				Message: err.Error(),
			}
		}
		issueDate = parsed
	}

	docID := documentID
	return invoice.CreateInput{
		EntityID:         entityID,
		InvoiceNumber:    invoiceNumber,
		IssueDate:        issueDate,
		BuyerTIN:         values["buyer_tin"],
		BuyerName:        values["buyer_name"],
		Currency:         values["currency"],
		Subtotal:         values["subtotal"],
		VAT:              values["vat"],
		Total:            values["total"],
		SourceDocumentID: &docID,
	}, nil
}

// ImportDocument is the document-import orchestration entrypoint (EXTR-06-03, task-763):
// read -> map -> mint batch -> dedup precheck -> create -> finalize. rows_total is always 1
// (D-5, one document = one invoice); no gate runs (RuleSetVersion stays nil, AC #6).
//
// SettledExtraction precedes CreateBatch so a document with nothing to import mints no batch
// (D-10). CreateBatch runs BEFORE the mapper's RowError is checked, so an unreadable
// invoice_number still leaves an auditable, completed-and-quarantined batch (D-17/D-9) rather
// than a silent no-op.
func (s *Service) ImportDocument(ctx context.Context, entityID, documentID string) (BatchResult, error) {
	ex, err := s.batch.SettledExtraction(ctx, documentID)
	if err != nil {
		return BatchResult{}, err
	}

	in, mapErr := documentCreateInput(entityID, documentID, ex)

	batchID, err := s.batch.CreateBatch(ctx, entityID, ex.Filename, documentID)
	if err != nil {
		return BatchResult{}, err
	}

	if mapErr != nil {
		errs := []RowError{*mapErr}
		if err := s.batch.Finalize(ctx, batchID, 1, 0, 1, errs, "completed"); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{
			ID:                  batchID,
			Status:              "completed",
			RowsTotal:           1,
			RowsInvalid:         1,
			QuarantinedInvoices: 1,
			Errors:              errs,
			InvoiceViolations:   []InvoiceViolations{},
		}, nil
	}
	in.ImportBatchID = &batchID

	// ExistingNumbers only resolves the colliding id for a nicer message in the non-racing
	// case (D-12) -- Create's own unique-constraint 23505 below is the real guard. Its own
	// operational failure has no test fixture (no fault-injection seam reaches it, see
	// TestServiceImportDocument_MintFailurePropagatesRawErrorClosestInducibleForExistingNumbersGap's
	// doc comment) but the batch is already minted, so it must still best-effort finalize
	// 'failed' rather than strand the batch at 'processing'.
	existing, err := s.batch.ExistingNumbers(ctx, entityID, []string{in.InvoiceNumber})
	if err != nil {
		_ = s.batch.Finalize(ctx, batchID, 1, 1, 0, nil, "failed")
		return BatchResult{}, err
	}

	if _, createErr := s.inv.Create(ctx, in); createErr != nil {
		msg, isDomainErr := domainCreateErrorMessage(createErr)
		if !isDomainErr {
			_ = s.batch.Finalize(ctx, batchID, 1, 1, 0, nil, "failed")
			return BatchResult{}, createErr
		}

		var quarantineErr RowError
		if errors.Is(createErr, invoice.ErrDuplicateNumber) {
			quarantineErr = storeDuplicateRowError(nil, existing[in.InvoiceNumber])
		} else {
			quarantineErr = RowError{Message: msg}
		}
		errs := []RowError{quarantineErr}
		if err := s.batch.Finalize(ctx, batchID, 1, 0, 1, errs, "completed"); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{
			ID:                  batchID,
			Status:              "completed",
			RowsTotal:           1,
			RowsInvalid:         1,
			QuarantinedInvoices: 1,
			Errors:              errs,
			InvoiceViolations:   []InvoiceViolations{},
		}, nil
	}

	if err := s.batch.Finalize(ctx, batchID, 1, 1, 0, []RowError{}, "completed"); err != nil {
		return BatchResult{}, err
	}
	return BatchResult{
		ID:                batchID,
		Status:            "completed",
		RowsTotal:         1,
		RowsValid:         1,
		ReadyInvoices:     1,
		Errors:            []RowError{},
		InvoiceViolations: []InvoiceViolations{},
	}, nil
}
