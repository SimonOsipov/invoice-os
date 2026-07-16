// M4-03-04 (task-105): the importer's orchestration surface — map -> normalize
// -> group -> classify -> (dry-run classify-only | real CreateBatch/Create/
// Finalize). This is THE HEART of the bulk-import feature: it turns a decoded
// header + data rows (already produced by Decode, M4-03-02) into invoice
// drafts, one per invoice_number group.
package importer

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// BatchResult is Import's return shape, whether dry-run or real. For a
// dry-run, ID/Status stay "" (no import_batches row is ever written).
type BatchResult struct {
	ID                  string
	Status              string
	RowsTotal           int
	RowsValid           int
	RowsInvalid         int
	ReadyInvoices       int
	QuarantinedInvoices int
	Errors              []RowError
}

// Service orchestrates decode-output (a header + data rows, already produced
// by Decode) into invoice drafts, holding both the importer Store
// (import_batches) and the invoice Store (invoices/line_items) it writes
// through.
type Service struct {
	batch *Store
	inv   *invoice.Store
}

// NewService wraps the two stores the orchestration needs. The caller owns
// both stores' pool lifecycles.
func NewService(batch *Store, inv *invoice.Store) *Service {
	return &Service{batch: batch, inv: inv}
}

// numericFields are the 5 canonical fields that get [numeric-normalization]
// (ASCII grouping commas + surrounding whitespace stripped) before becoming a
// CreateInput string. Every other canonical field is passed through verbatim.
var numericFields = map[string]bool{
	"subtotal":        true,
	"vat":             true,
	"total":           true,
	"line_quantity":   true,
	"line_unit_price": true,
}

// headerFieldOrder is the set of canonical fields that must agree across
// every row of one invoice_number group ([dedup]) — repeated per-invoice
// header content, as opposed to per-line content. The order here is the
// order in-file conflicts are detected in (first disagreeing field wins),
// matching the Implementation Plan's own field listing.
var headerFieldOrder = []string{
	"issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total",
}

// decimalNumberRe is a best-effort "does this look like a plain decimal
// number" check, used only for post-Create-error field attribution
// (bestEffortBadNumericField) — never to pre-reject input (that's Postgres's
// job at Create, per [review-authority]).
var decimalNumberRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// invoiceGroup buffers the rows sharing one mapped invoice_number value
// ([grouping]), preserving file order in rowIdxs — non-contiguous rows of the
// same invoice_number still land in one group.
type invoiceGroup struct {
	number  string
	rowIdxs []int
}

// resolveMapping resolves mapping (canonical field -> header string) into
// canonical field -> column index against header (first match). An
// invoice_number-less mapping, or a mapped header string absent from header,
// is rejected as ErrValidation BEFORE any write.
func resolveMapping(mapping map[string]string, header []string) (map[string]int, error) {
	if _, ok := mapping["invoice_number"]; !ok {
		return nil, fmt.Errorf("%w: mapping is missing required field invoice_number", ErrValidation)
	}
	colIndex := make(map[string]int, len(mapping))
	for field, headerName := range mapping {
		idx := -1
		for i, h := range header {
			if h == headerName {
				idx = i
				break
			}
		}
		if idx == -1 {
			return nil, fmt.Errorf("%w: mapped header %q for field %q not found in header row", ErrValidation, headerName, field)
		}
		colIndex[field] = idx
	}
	return colIndex, nil
}

// normalizeNumeric strips ASCII grouping commas and surrounding whitespace
// ONLY ([numeric-normalization]) — this is un-formatting, not deriving.
// Letters/currency symbols/anything else survive untouched, so a genuinely
// non-numeric cell (e.g. "N/A") still fails ::numeric at Create time.
func normalizeNumeric(s string) string {
	s = strings.ReplaceAll(s, ",", "")
	return strings.TrimSpace(s)
}

// fieldValue reads field's raw cell from row via colIndex, normalizing it
// first if field is one of the 5 numeric fields. Returns nil when the field
// is not mapped at all (colIndex has no entry) or, for a numeric field, when
// the normalized value is blank (an empty numeric cell means "no value", not
// a literal ” that would fail Postgres's ::numeric cast) — a non-numeric
// field's blank cell is still returned as a pointer to "" (store-invalid-
// faithfully; a blank string is valid TEXT content).
func fieldValue(row []string, colIndex map[string]int, field string) *string {
	idx, ok := colIndex[field]
	if !ok {
		return nil
	}
	var v string
	if idx < len(row) {
		v = row[idx]
	}
	if numericFields[field] {
		v = normalizeNumeric(v)
		if v == "" {
			return nil
		}
	}
	return &v
}

// parseIssueDate parses s (already the raw, un-normalized issue_date cell —
// issue_date is not one of the 5 numeric fields) as the one canonical
// YYYY-MM-DD date format this importer accepts. A blank (whitespace-only) s
// is not an error: it returns (nil, nil), the faithful "they wrote nothing"
// reading (store-invalid-faithfully). A NON-EMPTY s that fails to parse
// returns (nil, err) — distinct from blank, so the classify step can tell
// "wrote nothing" apart from "wrote something we can't understand" and
// quarantine the latter (Core AC#7: silently NULLing a firm-written but
// badly-formatted date would be an uncorrected-looking correction, and would
// launder a "date format wrong" error into a misleading "date missing").
func parseIssueDate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, fmt.Errorf("issue_date %q is not in YYYY-MM-DD format", s)
	}
	return &t, nil
}

// issueDateParseError reports the parse error (if any) for a group's
// issue_date value, read off rowIdxs[0]. By the time classify calls this,
// headerConflictField has already confirmed the group's rows agree on
// issue_date (it's a header field), so checking the first row once is
// sufficient. Returns nil when issue_date is unmapped, blank, or parses
// cleanly.
func issueDateParseError(rows [][]string, colIndex map[string]int, rowIdxs []int) error {
	p := fieldValue(rows[rowIdxs[0]], colIndex, "issue_date")
	if p == nil {
		return nil
	}
	_, err := parseIssueDate(*p)
	return err
}

// sheetRow converts a 0-based rows[] index into its 1-based spreadsheet row:
// header is row 1, so rows[i] is sheet row i+2.
func sheetRow(i int) int {
	return i + 2
}

// sheetRows converts rowIdxs (0-based) into sorted 1-based sheet rows, for a
// RowError's plural Rows field ([errors-shape]).
func sheetRows(rowIdxs []int) []int {
	out := make([]int, len(rowIdxs))
	for i, ri := range rowIdxs {
		out[i] = sheetRow(ri)
	}
	sort.Ints(out)
	return out
}

// headerConflictField reports the FIRST field (in headerFieldOrder) whose
// normalized value disagrees between rowIdxs[0] and any other row in the
// group, or "" if all header fields agree. Numeric header fields are
// compared post-normalization, so "1,000" vs "1000" is not a spurious
// conflict.
func headerConflictField(rows [][]string, colIndex map[string]int, rowIdxs []int) string {
	first := rows[rowIdxs[0]]
	for _, field := range headerFieldOrder {
		idx, ok := colIndex[field]
		if !ok {
			continue // field not mapped at all -- nothing to compare
		}
		want := cellAt(first, idx, field)
		for _, ri := range rowIdxs[1:] {
			got := cellAt(rows[ri], idx, field)
			if got != want {
				return field
			}
		}
	}
	return ""
}

// cellAt reads row[idx] (or "" if out of range), normalizing it if field is
// numeric — a shared helper for headerConflictField and
// bestEffortBadNumericField, which both need the raw (not nil-on-blank)
// normalized string rather than fieldValue's *string/nil-on-blank shape.
func cellAt(row []string, idx int, field string) string {
	var v string
	if idx < len(row) {
		v = row[idx]
	}
	if numericFields[field] {
		v = normalizeNumeric(v)
	}
	return v
}

// bestEffortBadNumericField does a post-Create-error diagnostic scan of the
// group's 5 numeric fields (header fields off rowIdxs[0], line fields off
// every row), returning the FIRST whose normalized value doesn't parse as a
// plain decimal number. This is diagnostic only (nothing is stored for a
// quarantined invoice) — it just gives RowError.Field a best-effort value
// when a Create error's SQLSTATE (e.g. 22P02) doesn't itself disambiguate
// which column broke. Returns "" if no numeric field is clearly bad.
func bestEffortBadNumericField(rows [][]string, colIndex map[string]int, rowIdxs []int) string {
	first := rows[rowIdxs[0]]
	for _, field := range []string{"subtotal", "vat", "total"} {
		idx, ok := colIndex[field]
		if !ok {
			continue
		}
		v := cellAt(first, idx, field)
		if v != "" && !decimalNumberRe.MatchString(v) {
			return field
		}
	}
	for _, field := range []string{"line_quantity", "line_unit_price"} {
		idx, ok := colIndex[field]
		if !ok {
			continue
		}
		for _, ri := range rowIdxs {
			v := cellAt(rows[ri], idx, field)
			if v != "" && !decimalNumberRe.MatchString(v) {
				return field
			}
		}
	}
	return ""
}

// buildCreateInput assembles one invoice.CreateInput for a READY group:
// header fields (issue_date/buyer_tin/buyer_name/currency/subtotal/vat/total)
// come from the group's first row (they agree across the group, by
// classification); line items are one LineItemInput per row, in group (file)
// order. supplierName/supplierTIN come from EntitySupplier
// ([supplier-from-entity]); batchID is the ONE minted id for this whole
// import run — the guardrail is trivially satisfied since Import never
// accepts a caller-supplied batch id.
func buildCreateInput(entityID string, rows [][]string, colIndex map[string]int, g *invoiceGroup, batchID, supplierName string, supplierTIN *string) invoice.CreateInput {
	firstRow := rows[g.rowIdxs[0]]

	issueDateStr := ""
	if p := fieldValue(firstRow, colIndex, "issue_date"); p != nil {
		issueDateStr = *p
	}
	// classify (issueDateParseError) already rejected any non-empty,
	// unparseable issue_date for this group before it ever reached
	// readyGroups, so the error here is always nil — a blank cell (nil,
	// nil) is the only remaining case, which is the correct NULL.
	issueDate, _ := parseIssueDate(issueDateStr)

	in := invoice.CreateInput{
		EntityID:      entityID,
		InvoiceNumber: g.number,
		IssueDate:     issueDate,
		SupplierTIN:   supplierTIN,
		SupplierName:  &supplierName,
		BuyerTIN:      fieldValue(firstRow, colIndex, "buyer_tin"),
		BuyerName:     fieldValue(firstRow, colIndex, "buyer_name"),
		Currency:      fieldValue(firstRow, colIndex, "currency"),
		Subtotal:      fieldValue(firstRow, colIndex, "subtotal"),
		VAT:           fieldValue(firstRow, colIndex, "vat"),
		Total:         fieldValue(firstRow, colIndex, "total"),
		ImportBatchID: &batchID,
	}
	for _, ri := range g.rowIdxs {
		row := rows[ri]
		in.LineItems = append(in.LineItems, invoice.LineItemInput{
			Description: fieldValue(row, colIndex, "line_description"),
			Quantity:    fieldValue(row, colIndex, "line_quantity"),
			UnitPrice:   fieldValue(row, colIndex, "line_unit_price"),
		})
	}
	return in
}

// Import is the importer's orchestration entrypoint (THE HEART): map ->
// normalize -> group -> classify -> (dry-run classify-only | real
// CreateBatch/Create/Finalize).
//
//  1. Resolve mapping -> column indices against header (ErrValidation before
//     any write if invoice_number is unmapped, or a mapped header string is
//     absent from header).
//  2. Group data rows by their mapped invoice_number value
//     ([grouping], non-contiguous OK); a blank/empty invoice_number is
//     ungroupable -> quarantined with a scalar-Row RowError citing its own
//     sheet row.
//  3. Classify each group: an in-file header-field disagreement quarantines
//     it (RowError.Rows = every one of the group's sheet rows, [dedup]/
//     [errors-shape]); else a non-empty issue_date that doesn't parse as
//     YYYY-MM-DD quarantines it too (RowError.Field "issue_date" -- Core
//     AC#7: a badly-formatted date must never be silently NULLed, only a
//     genuinely blank cell reads as NULL); else an against-stored hit (one
//     ExistingNumbers call for the whole file, entity-scoped --
//     [dedup-boundary]) quarantines it too; else it's READY.
//  4. Look up the entity's (name, tin) once ([supplier-from-entity]) --
//     ErrNotFound propagates (the handler 404s), even for a dry run, since
//     this also serves as the entity-exists check a dry run would otherwise
//     skip entirely (it makes no other DB write).
//  5. Count independently so RowsValid+RowsInvalid==RowsTotal by
//     construction: every row is in exactly one of {ungroupable, a
//     quarantined group, a ready group}.
//  6. Dry-run stops here: same BatchResult shape, ID/Status empty, nothing
//     written.
//  7. Real import: CreateBatch mints the ONE batch id used for every
//     CreateInput.ImportBatchID this run (never a caller-supplied id). Per
//     READY group, invoice.Store.Create; ANY error (not just a duplicate
//     number -- a residual non-numeric cell surviving normalization surfaces
//     here as Postgres's 22P02, [review-authority]) quarantines just that
//     group and the run continues ([batch semantics], partial success).
//     Finalize records the terminal counts/status/errors.
func (s *Service) Import(ctx context.Context, entityID string, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
	colIndex, err := resolveMapping(mapping, header)
	if err != nil {
		return BatchResult{}, err
	}

	groups := map[string]*invoiceGroup{}
	var order []string
	var ungroupableRows []int

	invNumIdx := colIndex["invoice_number"]
	for i, row := range rows {
		var raw string
		if invNumIdx < len(row) {
			raw = row[invNumIdx]
		}
		if strings.TrimSpace(raw) == "" {
			ungroupableRows = append(ungroupableRows, i)
			continue
		}
		g, ok := groups[raw]
		if !ok {
			g = &invoiceGroup{number: raw}
			groups[raw] = g
			order = append(order, raw)
		}
		g.rowIdxs = append(g.rowIdxs, i)
	}

	existing, err := s.batch.ExistingNumbers(ctx, entityID, order)
	if err != nil {
		return BatchResult{}, err
	}

	var errorsList []RowError
	var readyGroups []*invoiceGroup
	quarantinedInvoices := 0
	invalidRows := 0

	for _, num := range order {
		g := groups[num]
		if field := headerConflictField(rows, colIndex, g.rowIdxs); field != "" {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(g.rowIdxs),
				Field:   field,
				Message: fmt.Sprintf("rows disagree on %s", field),
			})
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		if dateErr := issueDateParseError(rows, colIndex, g.rowIdxs); dateErr != nil {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(g.rowIdxs),
				Field:   "issue_date",
				Message: dateErr.Error(),
			})
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		if existing[num] {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(g.rowIdxs),
				Field:   "invoice_number",
				Message: "already imported",
			})
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		readyGroups = append(readyGroups, g)
	}

	for _, i := range ungroupableRows {
		errorsList = append(errorsList, RowError{
			Row:     sheetRow(i),
			Message: "blank invoice number: row cannot be grouped",
		})
		quarantinedInvoices++
		invalidRows++
	}

	supplierName, supplierTIN, err := s.batch.EntitySupplier(ctx, entityID)
	if err != nil {
		return BatchResult{}, err
	}

	rowsTotal := len(rows)
	rowsInvalid := invalidRows
	rowsValid := rowsTotal - rowsInvalid

	if dryRun {
		return BatchResult{
			RowsTotal:           rowsTotal,
			RowsValid:           rowsValid,
			RowsInvalid:         rowsInvalid,
			ReadyInvoices:       len(readyGroups),
			QuarantinedInvoices: quarantinedInvoices,
			Errors:              errorsList,
		}, nil
	}

	// The run itself can't report anything for a header with zero data
	// rows: mint the batch (so the attempt is auditable) and finalize it
	// straight to 'failed' — never CreateBatch/Create for a real group,
	// never a partial-split status for this case.
	if rowsTotal == 0 {
		batchID, err := s.batch.CreateBatch(ctx, entityID)
		if err != nil {
			return BatchResult{}, err
		}
		if err := s.batch.Finalize(ctx, batchID, 0, 0, 0, nil, "failed"); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{ID: batchID, Status: "failed"}, nil
	}

	batchID, err := s.batch.CreateBatch(ctx, entityID)
	if err != nil {
		return BatchResult{}, err
	}

	readyCount := 0
	for _, g := range readyGroups {
		in := buildCreateInput(entityID, rows, colIndex, g, batchID, supplierName, supplierTIN)
		if _, createErr := s.inv.Create(ctx, in); createErr != nil {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(g.rowIdxs),
				Field:   bestEffortBadNumericField(rows, colIndex, g.rowIdxs),
				Message: createErr.Error(),
			})
			quarantinedInvoices++
			rowsInvalid += len(g.rowIdxs)
			rowsValid -= len(g.rowIdxs)
			continue
		}
		readyCount++
	}

	if err := s.batch.Finalize(ctx, batchID, rowsTotal, rowsValid, rowsInvalid, errorsList, "completed"); err != nil {
		return BatchResult{}, err
	}

	return BatchResult{
		ID:                  batchID,
		Status:              "completed",
		RowsTotal:           rowsTotal,
		RowsValid:           rowsValid,
		RowsInvalid:         rowsInvalid,
		ReadyInvoices:       readyCount,
		QuarantinedInvoices: quarantinedInvoices,
		Errors:              errorsList,
	}, nil
}
