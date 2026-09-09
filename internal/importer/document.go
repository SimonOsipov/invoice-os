// document.go: the settled-extraction read (EXTR-06-01, task-761), the field-to-CreateInput
// mapper (EXTR-06-02, task-762), and the document-import orchestration entrypoint
// (EXTR-06-03, task-763) -- a second entry into internal/importer alongside Import()'s
// spreadsheet path (service.go). See .ralph/EXTR-06-finalized.md, "The settled-extraction input
// type".
package importer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// extractedField is one rank-0 extraction_field_results row: the decided reading.
// Alternatives (candidate_rank >= 1) are never read here -- a human resolves those on the
// review screen (EXTR-15/EXTR-16), not the writer. The rank-0 filter below is deliberate and
// no longer shared: extraction.detailFieldsTx reads every rank and nests the alternatives.
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
//
// Correction-blind on purpose: a human correction is written to the invoice in the same
// transaction as the extraction_field_corrections row, so reading that table here would apply
// the same value twice through two paths (TestSettledExtraction_IgnoresCorrectionsAndReadsRankZero).
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

// mapperLineRoles is internal/importer's own copy of extraction.LineRoles, in the same order --
// internal/importer cannot import internal/extraction (document_deps_test.go / SX-09), so
// nothing compiler-links the two lists; TestImporterLineRoles_MatchesExtractionLineRoles is the
// drift guard.
var mapperLineRoles = []string{"description", "quantity", "unit_price", "line_total", "line_tax"}

// lineFieldPrefix is line_items[N].<role>'s opening.
const lineFieldPrefix = "line_items["

// parseLineFieldName is a local copy of extraction.ParseLineFieldName's grammar (SX-09 fence):
// a 1-based index with no leading zero and one of mapperLineRoles.
func parseLineFieldName(name string) (index int, role string, ok bool) {
	rest, found := strings.CutPrefix(name, lineFieldPrefix)
	if !found {
		return 0, "", false
	}
	digits, role, found := strings.Cut(rest, "].")
	if !found {
		return 0, "", false
	}
	index, err := strconv.Atoi(digits)
	// Itoa back: Atoi admits "+1" and "01", neither of which the wire name can carry.
	if err != nil || index < 1 || strconv.Itoa(index) != digits {
		return 0, "", false
	}
	for _, r := range mapperLineRoles {
		if r == role {
			return index, role, true
		}
	}
	return 0, "", false
}

// The two sentences the mapper quarantines a document with. Written down here, not assembled
// at call time, so TestOldMapperMessageIsGoneAndTheNewOnesAreLiterals can find them.
const (
	poorScanMessage = "The scan of this document was too poor to read, so no invoice fields could be taken from it. Ask the supplier whether they can send the original PDF, or enter this invoice manually to carry on."

	noInvoiceNumberMessage = "This document was read, but no invoice number was found on it. Enter this invoice manually to carry on."
)

// isPoorScan reports the field set the extraction worker writes when a document yields no text
// at all: one document_text_layer row, reason unreadable. The predicate is over the whole set,
// never one field's reason code -- an unreadable invoice_number among ten read fields is a read
// document (TestDocumentCreateInput_OnlyTheExactPoorScanFieldSetTakesTheScanBranch).
func isPoorScan(fields []extractedField) bool {
	if len(fields) != 1 {
		return false
	}
	f := fields[0]
	return f.Name == "document_text_layer" && f.Reason != nil && *f.Reason == "unreadable"
}

// documentCreateInput maps one SettledExtraction's decided readings to invoice.CreateInput.
// Pure. supplier_tin/supplier_name are never set -- Store.Create overwrites both from the
// entity on every write (store.go:220-221, Q11), so writing a value here would state a claim
// the store then silently discards. SourceRows stays nil: the column CHECKs reject both '{}'
// and any element < 2, so NULL is the only legal value here. Line grouping runs LAST, after the
// invoice_number quarantine branch below, so a document with lines but no invoice number still
// quarantines whole.
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
		// Field stays the wire key either way -- the review screen machine-reads it
		// (TestDocumentCreateInput_EveryQuarantineBranchKeepsItsMachineFieldKey).
		message := noInvoiceNumberMessage
		if isPoorScan(ex.Fields) {
			message = poorScanMessage
		}
		return invoice.CreateInput{}, &RowError{
			Field:   "invoice_number",
			Message: message,
		}
	}

	var issueDate *time.Time
	if v := values["issue_date"]; v != nil {
		parsed, err := parseIssueDate(*v)
		if err != nil {
			// parseIssueDate's own text names the wire field and is shaped for the
			// spreadsheet path; the only failure it has is a non-YYYY-MM-DD value, so the
			// sentence below says the same thing to a person
			// (TestDocumentCreateInput_IssueDateFailureWrapsTheParseErrorNamingTheValue).
			return invoice.CreateInput{}, &RowError{
				Field:   "issue_date",
				Message: fmt.Sprintf("The issue date on this document reads %q, which is not a date this importer can read. It accepts dates written as YYYY-MM-DD, such as 2026-03-01. Enter this invoice manually to carry on.", strings.TrimSpace(*v)),
			}
		}
		issueDate = parsed
	}

	// Group line_items[N].<role> cells by index, then sort NUMERICALLY: SettledExtraction's
	// ORDER BY created_at, id does not guarantee index order, and Store.Create assigns
	// line_no = 1..N by array position (D10), so a hole in the indices closes ordinally.
	groups := make(map[int]*invoice.LineItemInput)
	for _, f := range ex.Fields {
		idx, role, ok := parseLineFieldName(f.Name)
		if !ok {
			continue
		}
		g, exists := groups[idx]
		if !exists {
			g = &invoice.LineItemInput{}
			groups[idx] = g
		}
		switch role {
		case "description":
			g.Description = f.Value
		case "quantity":
			g.Quantity = f.Value
		case "unit_price":
			g.UnitPrice = f.Value
		case "line_total":
			g.LineTotal = f.Value
		case "line_tax":
			g.LineTax = f.Value
		}
	}
	var lineItems []invoice.LineItemInput
	if len(groups) > 0 {
		indices := make([]int, 0, len(groups))
		for idx := range groups {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		lineItems = make([]invoice.LineItemInput, 0, len(indices))
		for _, idx := range indices {
			lineItems = append(lineItems, *groups[idx])
		}
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
		LineItems:        lineItems,
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
