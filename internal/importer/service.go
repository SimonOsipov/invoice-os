// M4-03-04 (task-105): the importer's orchestration surface — map -> normalize
// -> group -> classify -> (dry-run classify-only | real CreateBatch/Create/
// Finalize). This is THE HEART of the bulk-import feature: it turns a decoded
// header + data rows (already produced by Decode, M4-03-02) into invoice
// drafts, one per invoice_number group.
package importer

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
)

// BatchResult is Import's return shape, whether dry-run or real. For a
// dry-run, ID/Status stay "" (no import_batches row is ever written).
//
// RuleSetVersion/InvoicesClean/InvoicesWithViolations/InvoiceViolations are
// M4-04-07's additive rule-outcome fields ([import-report-shape]) -- purely
// ADDITIVE alongside the five M4-03 fields above them, which keep their
// EXACT existing meaning (Core AC#5, M4-03's [counters]) and are NOT
// touched by this addition. RuleSetVersion is a pointer so it can render
// JSON null when NOTHING was evaluated (an all-quarantined batch, Stage-1
// F2 / IMPV-16) -- a returned int 0 would be indistinguishable from a
// genuine version 0, so Import guards on WHETHER ANYTHING WAS EVALUATED
// (len(created)==0 real / len(readyGroups)==0 dry-run), never on
// Evaluate/ValidateBatch's returned RuleSetVersion value. Only the CALLER
// knows the batch was empty; the returned 0 cannot tell it.
//
// InvoiceViolations REPORTS every invoice carrying at least one violation
// of ANY severity, while InvoicesClean/InvoicesWithViolations COUNT by the
// blocking predicate (invoice.HasBlockingViolation) that actually decides
// promotion. The two therefore disagree, on purpose and only for a
// warning-only invoice: it is counted CLEAN (it promotes -- [error
// semantics]: warnings are advisory and never block) yet still LISTED, so
// the firm can see the advisory. A non-empty InvoiceViolations alongside
// InvoicesWithViolations==0 is that case, not a bug.
type BatchResult struct {
	ID                  string
	Status              string
	RowsTotal           int
	RowsValid           int
	RowsInvalid         int
	ReadyInvoices       int
	QuarantinedInvoices int
	Errors              []RowError

	RuleSetVersion         *int
	InvoicesClean          int
	InvoicesWithViolations int
	InvoiceViolations      []InvoiceViolations
}

// InvoiceViolations is one entry of BatchResult.InvoiceViolations: one
// invoice that carried at least one rule violation (blocking or not, per
// [error semantics] -- warnings are reported too, they just don't block),
// citing the spreadsheet rows it came from so the firm can find them
// ([import-report-shape]). InvoiceID is omitempty because the dry-run path
// has no id yet (ref = invoice_number, per gate.go's EvalItem doc) -- it
// must be ABSENT on a dry-run response, never emitted as "" [Stage-1 F7].
type InvoiceViolations struct {
	InvoiceNumber string              `json:"invoice_number"`
	InvoiceID     string              `json:"invoice_id,omitempty"`
	Rows          []int               `json:"rows"`
	Violations    []invoice.Violation `json:"violations"`
}

// gate is the importer's OWN, minimal view of internal/invoice.Gate
// ([Stage-1 addendum F3]) -- a consumer-side interface (idiomatic Go:
// accept interfaces, return structs), declared HERE rather than depending
// on a concrete *invoice.Gate field, so IMPV-08/09/10/11 can drive call
// counts and injected faults with a test double instead of needing a real
// DB fault to reach ApplyValidation (M4-03's own precedent -- an empty
// auth.Identity.Subject -- cannot reach it: Store.Create writes its own
// history row with the same actor and aborts FIRST, per Stage-1 F3).
// *invoice.Gate satisfies this interface STRUCTURALLY (zero change to
// package invoice): its Evaluate/ValidateBatch signatures match exactly.
type gate interface {
	Evaluate(ctx context.Context, items []invoice.EvalItem) (invoice.EvalResult, error)
	ValidateBatch(ctx context.Context, invs []invoice.Invoice) (invoice.BatchOutcome, error)
}

// Service orchestrates decode-output (a header + data rows, already produced
// by Decode) into invoice drafts, holding both the importer Store
// (import_batches), the invoice Store (invoices/line_items) it writes
// through, and the validate gate ([import-validates]/[dry-run-evaluates],
// M4-04-07) every batch runs through.
type Service struct {
	batch *Store
	inv   *invoice.Store
	gate  gate
}

// NewService wraps the three dependencies the orchestration needs. The
// caller owns both stores' pool lifecycles and the gate's own dependencies
// (its store/validator).
//
// g must be non-nil: Import dereferences it on BOTH paths (dry-run
// Evaluate, real ValidateBatch) for any file with at least one READY group.
// Not guarded here (Simplicity First) -- production's one call site
// (cmd/invoice/main.go) always passes a real *invoice.Gate, and a nil gate
// in a test fails loudly at the call, not silently.
func NewService(batch *Store, inv *invoice.Store, g gate) *Service {
	return &Service{batch: batch, inv: inv, gate: g}
}

func isNumericType(t invoicefields.Type) bool {
	return t == invoicefields.Money || t == invoicefields.Quantity || t == invoicefields.Percent
}

// numericImportKeys lists the money, quantity and percent import keys in field-list order
// that pass keep.
func numericImportKeys(keep func(invoicefields.Field) bool) []string {
	var keys []string
	for _, f := range invoicefields.All {
		if f.Import && isNumericType(f.Type) && keep(f) {
			keys = append(keys, f.ImportKey())
		}
	}
	return keys
}

// Scan orders of commaDecimalField (all) and bestEffortBadNumericField (header, then line).
var (
	numericOrder       = numericImportKeys(func(invoicefields.Field) bool { return true })
	headerNumericOrder = numericImportKeys(func(f invoicefields.Field) bool { return !f.Line })
	lineNumericOrder   = numericImportKeys(func(f invoicefields.Field) bool { return f.Line })
)

// importFields maps each import key to its field, so cells are read by type.
var importFields = func() map[string]invoicefields.Field {
	m := map[string]invoicefields.Field{}
	for _, f := range invoicefields.All {
		if f.Import {
			m[f.ImportKey()] = f
		}
	}
	return m
}()

// dateOrder is the import Date keys in list order.
var dateOrder = func() []string {
	var keys []string
	for _, f := range invoicefields.All {
		if f.Import && f.Type == invoicefields.Date {
			keys = append(keys, f.ImportKey())
		}
	}
	return keys
}()

// numericRange is the digits a new numeric column holds (D17): numeric(14,2) and numeric(14,3).
var numericRange = map[string]struct{ intDigits, scale int }{
	"line_total": {12, 2}, "line_tax": {12, 2}, "line_tax_percent": {12, 2}, "line_base_quantity": {11, 3},
}

// headerFieldOrder is the import header keys that must agree across every row of one
// invoice_number group ([dedup]), in the order in-file conflicts are detected (first
// disagreeing field wins).
var headerFieldOrder = func() []string {
	var keys []string
	for _, f := range invoicefields.All {
		if f.Import && !f.Line && f.Key != "invoice_number" {
			keys = append(keys, f.Key)
		}
	}
	return keys
}()

// decimalNumberRe is a best-effort "does this look like a plain decimal
// number" check.
var decimalNumberRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// invoiceGroup buffers the rows sharing one mapped invoice_number value
// ([grouping]), preserving file order in rowIdxs — non-contiguous rows of the
// same invoice_number still land in one group.
type invoiceGroup struct {
	number  string
	rowIdxs []int
}

// canonicalFields is the closed set of column keys a mapping is allowed to
// use -- the import fields (invoicefields.ImportKeys). A mapping key outside
// this set (e.g. a typo like "totla") is rejected in resolveMapping, by
// exact symmetry with the mapped-header-absent check just below
// it: [mapping]'s guarantee is that the server structurally cannot mis-map,
// which requires rejecting an unrecognized KEY just as firmly as it rejects
// a mapped HEADER string that doesn't exist -- silently ignoring an unknown
// key would import that canonical field as NULL with no error at all.
var canonicalFields = func() map[string]bool {
	m := make(map[string]bool, len(mappingFields))
	for _, k := range mappingFields {
		m[k] = true
	}
	return m
}()

// resolveMapping resolves mapping (canonical field -> header string) into
// canonical field -> column index against header (first match). An
// invoice_number-less mapping, a mapping key outside canonicalFields, or a
// mapped header string absent from header, is rejected as ErrValidation
// BEFORE any write.
func resolveMapping(mapping map[string]string, header []string) (map[string]int, error) {
	if _, ok := mapping["invoice_number"]; !ok {
		return nil, fmt.Errorf("%w: mapping is missing required field invoice_number", ErrValidation)
	}
	colIndex := make(map[string]int, len(mapping))
	for field, headerName := range mapping {
		if !canonicalFields[field] {
			return nil, fmt.Errorf("%w: mapping key %q is not a recognized canonical field", ErrValidation, field)
		}
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

// issueTimeShortRe matches the H:MM, HH:MM and H:MM:SS shapes that padIssueTime completes.
var issueTimeShortRe = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{2})(?::([0-9]{2}))?$`)

// padIssueTime completes H:MM, HH:MM and H:MM:SS to HH:MM:SS (D18); any other shape is returned as is.
func padIssueTime(s string) string {
	m := issueTimeShortRe.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	sec := m[3]
	if sec == "" {
		sec = "00"
	}
	return strings.Repeat("0", 2-len(m[1])) + m[1] + ":" + m[2] + ":" + sec
}

// normalizeCell reads a raw cell by its field's type. Text keeps the raw cell.
func normalizeCell(field, raw string) string {
	switch importFields[field].Type {
	case invoicefields.Money, invoicefields.Quantity:
		return normalizeNumeric(raw)
	case invoicefields.Percent:
		return normalizeNumeric(strings.TrimSuffix(strings.TrimSpace(raw), "%"))
	case invoicefields.Code, invoicefields.Date:
		return strings.TrimSpace(raw)
	case invoicefields.Time:
		return padIssueTime(strings.TrimSpace(raw))
	}
	return raw
}

// fieldValue reads field's cell from row via colIndex, normalized by type.
// It returns nil when the field is unmapped or the cell is blank, except for
// the five original Text keys, which keep a blank cell as "" (D7).
func fieldValue(row []string, colIndex map[string]int, field string) *string {
	idx, ok := colIndex[field]
	if !ok {
		return nil
	}
	var v string
	if idx < len(row) {
		v = row[idx]
	}
	v = normalizeCell(field, v)
	f := importFields[field]
	if strings.TrimSpace(v) == "" && !(f.Type == invoicefields.Text && f.Lead) {
		return nil
	}
	return &v
}

// parseDate parses s as YYYY-MM-DD. A blank s is (nil, nil); a non-blank one
// that fails is an error, so classify can quarantine it rather than NULL it.
func parseDate(field, s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, fmt.Errorf("%s %q is not in YYYY-MM-DD format", field, s)
	}
	return &t, nil
}

// parseIssueDate is parseDate for issue_date; the document-reading import calls it too.
func parseIssueDate(s string) (*time.Time, error) { return parseDate("issue_date", s) }

// dateParseError reports the first import Date field (in dateOrder) whose first-row
// cell is non-blank and unparseable. headerConflictField has already made the group's
// header cells agree.
func dateParseError(rows [][]string, colIndex map[string]int, rowIdxs []int) (string, error) {
	for _, field := range dateOrder {
		p := fieldValue(rows[rowIdxs[0]], colIndex, field)
		if p == nil {
			continue
		}
		if _, err := parseDate(field, *p); err != nil {
			return field, err
		}
	}
	return "", nil
}

// issueTimeError reports a non-blank issue_time that fails the API's own check.
func issueTimeError(rows [][]string, colIndex map[string]int, rowIdxs []int) error {
	p := fieldValue(rows[rowIdxs[0]], colIndex, "issue_time")
	if p == nil || invoice.ValidIssueTime(*p) {
		return nil
	}
	return errors.New(invoice.IssueTimeMsg)
}

// exceedsRange reports whether the plain decimal v does not fit numericRange[field].
// Leading zeros and trailing zero decimals do not count.
func exceedsRange(field, v string) bool {
	lim := numericRange[field]
	whole, frac, _ := strings.Cut(strings.TrimPrefix(v, "-"), ".")
	return len(strings.TrimLeft(whole, "0")) > lim.intDigits || len(strings.TrimRight(frac, "0")) > lim.scale
}

// numericRangeField returns the first new numeric line field with a value that
// overflows its column on any row of the group, or "". It runs after the decimal check.
func numericRangeField(rows [][]string, colIndex map[string]int, rowIdxs []int) string {
	for _, field := range lineNumericOrder {
		idx, ok := colIndex[field]
		if _, limited := numericRange[field]; !ok || !limited {
			continue
		}
		for _, ri := range rowIdxs {
			if exceedsRange(field, cellAt(rows[ri], idx, field)) {
				return field
			}
		}
	}
	return ""
}

// sheetRow converts a 0-based rows[] index into its 1-based file row.
func sheetRow(headerRow, i int) int {
	return headerRow + 1 + i
}

// sheetRows converts rowIdxs (0-based) into sorted 1-based sheet rows, for a
// RowError's plural Rows field ([errors-shape]).
func sheetRows(headerRow int, rowIdxs []int) []int {
	out := make([]int, len(rowIdxs))
	for i, ri := range rowIdxs {
		out[i] = sheetRow(headerRow, ri)
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

// cellAt reads row[idx] (or "" if out of range), normalized by field type. It
// serves headerConflictField and bestEffortBadNumericField, which need the
// string even when blank.
func cellAt(row []string, idx int, field string) string {
	var v string
	if idx < len(row) {
		v = row[idx]
	}
	return normalizeCell(field, v)
}

// bestEffortBadNumericField scans the group's numeric fields (header
// fields off rowIdxs[0], line fields off every row), returning the FIRST
// whose normalized value doesn't parse as a plain decimal number. It serves
// two callers: (1) Import's classify step, where it is now authoritative —
// promoted from a post-Create diagnostic per Core AC#2 ("the same file +
// mapping dry-run gets the EXACT verdict the real import will produce"):
// numeric validity used to be deferred entirely to Postgres's ::numeric cast
// at Create time, which a dry-run never reaches, so a non-numeric cell (e.g.
// "N/A") wrongly reported READY in dry-run but quarantined for real. Checking
// it here, in BOTH dry-run and real, closes that gap; (2) Create's error path (Import,
// below), where it is still a best-effort diagnostic: if Create returns
// invoice.ErrValidation for a reason THIS scan didn't catch, its SQLSTATE
// (22P02) doesn't itself disambiguate which column broke, so this gives
// RowError.Field a best guess. Returns "" if no numeric field is clearly bad.
func bestEffortBadNumericField(rows [][]string, colIndex map[string]int, rowIdxs []int) string {
	first := rows[rowIdxs[0]]
	for _, field := range headerNumericOrder {
		idx, ok := colIndex[field]
		if !ok {
			continue
		}
		v := cellAt(first, idx, field)
		if v != "" && !decimalNumberRe.MatchString(v) {
			return field
		}
	}
	for _, field := range lineNumericOrder {
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

// leadGroupRe is the only valid text before a grouping comma.
var leadGroupRe = regexp.MustCompile(`^-?[0-9]{1,3}$`)

// isCommaDecimal reports whether raw has a comma that is not thousands
// grouping. Grouping means the text before the first comma is an optional
// sign and 1-3 digits, and every comma is followed by exactly three digits.
func isCommaDecimal(raw string) bool {
	s := strings.TrimSpace(raw)
	c := strings.IndexByte(s, ',')
	if c < 0 {
		return false
	}
	if !leadGroupRe.MatchString(s[:c]) {
		return true
	}
	for i := 0; i < len(s); i++ {
		if s[i] != ',' {
			continue
		}
		n := 0
		for j := i + 1; j < len(s) && s[j] >= '0' && s[j] <= '9'; j++ {
			n++
		}
		if n != 3 {
			return true
		}
	}
	return false
}

// commaDecimalField returns the first numeric field with a comma-decimal cell
// on any row of the group, or "". It reads raw cells: normalizeNumeric would
// turn "12,50" into "1250" and hide it.
func commaDecimalField(rows [][]string, colIndex map[string]int, rowIdxs []int) string {
	for _, field := range numericOrder {
		idx, ok := colIndex[field]
		if !ok {
			continue
		}
		for _, ri := range rowIdxs {
			if idx < len(rows[ri]) && isCommaDecimal(rows[ri][idx]) {
				return field
			}
		}
	}
	return ""
}

// buildCreateInput assembles one invoice.CreateInput for a READY group:
// header fields come from the group's first row (they agree across the group, by
// classification); line items are one LineItemInput per row, in group (file)
// order. supplierName/supplierTIN come from EntitySupplier
// ([supplier-from-entity]); batchID is the ONE minted id for this whole
// import run — the guardrail is trivially satisfied since Import never
// accepts a caller-supplied batch id.
func buildCreateInput(entityID string, rows [][]string, colIndex map[string]int, g *invoiceGroup, batchID, documentID string, headerRow int, supplierName string, supplierTIN *string) invoice.CreateInput {
	firstRow := rows[g.rowIdxs[0]]

	// classify (dateParseError) already rejected any unparseable date, so a nil
	// here is a blank cell.
	date := func(field string) *time.Time {
		p := fieldValue(firstRow, colIndex, field)
		if p == nil {
			return nil
		}
		t, _ := parseDate(field, *p)
		return t
	}
	issueDate := date("issue_date")

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

		InvoiceKind:     fieldValue(firstRow, colIndex, "invoice_kind"),
		TaxCurrencyCode: fieldValue(firstRow, colIndex, "tax_currency_code"),
		DueDate:         date("due_date"),
		IssueTime:       fieldValue(firstRow, colIndex, "issue_time"),
		TaxPointDate:    date("tax_point_date"),
		PaymentStatus:   fieldValue(firstRow, colIndex, "payment_status"),
		BuyerEmail:      fieldValue(firstRow, colIndex, "buyer_email"),
		BuyerTelephone:  fieldValue(firstRow, colIndex, "buyer_telephone"),
		BuyerStreet:     fieldValue(firstRow, colIndex, "buyer_street"),
		BuyerCity:       fieldValue(firstRow, colIndex, "buyer_city"),
		BuyerPostalZone: fieldValue(firstRow, colIndex, "buyer_postal_zone"),
		BuyerCountry:    fieldValue(firstRow, colIndex, "buyer_country"),
		BuyerState:      fieldValue(firstRow, colIndex, "buyer_state"),
		BuyerLGA:        fieldValue(firstRow, colIndex, "buyer_lga"),
	}
	// Nil, not &"": source_document_id is a uuid column, so "" is a 22P02.
	// SourceRows belongs INSIDE this guard: source_rows without a document
	// violates invoices_source_rows_requires_document, and that 23514 is not a
	// domain error -- it aborts the whole run with a 500.
	if documentID != "" {
		in.SourceDocumentID = &documentID
		in.SourceRows = sheetRows(headerRow, g.rowIdxs)
	}
	for _, ri := range g.rowIdxs {
		row := rows[ri]
		in.LineItems = append(in.LineItems, invoice.LineItemInput{
			Description: fieldValue(row, colIndex, "line_description"),
			Quantity:    fieldValue(row, colIndex, "line_quantity"),
			UnitPrice:   fieldValue(row, colIndex, "line_unit_price"),
			LineTotal:   fieldValue(row, colIndex, "line_total"),
			LineTax:     fieldValue(row, colIndex, "line_tax"),

			TaxCategory:               fieldValue(row, colIndex, "line_tax_category"),
			HSNCode:                   fieldValue(row, colIndex, "line_hsn_code"),
			ISICCode:                  fieldValue(row, colIndex, "line_isic_code"),
			ProductCategory:           fieldValue(row, colIndex, "line_product_category"),
			ServiceCategory:           fieldValue(row, colIndex, "line_service_category"),
			SellersItemIdentification: fieldValue(row, colIndex, "line_sellers_item_identification"),
			PriceUnit:                 fieldValue(row, colIndex, "line_price_unit"),
			TaxPercent:                fieldValue(row, colIndex, "line_tax_percent"),
			BaseQuantity:              fieldValue(row, colIndex, "line_base_quantity"),
		})
	}
	return in
}

// invoiceFromCreateInput projects the CreateInput buildCreateInput just
// assembled onto the in-memory invoice.Invoice the DRY-RUN path evaluates
// ([payload-mapper]). ONE mapper feeds both paths: the real path evaluates
// the Invoice Store.Create RETURNS (already hydrated), the dry-run path
// evaluates this projection of the very same CreateInput. Two mappers that
// must agree forever is how dry-run and real drift.
//
// LineNo is i+1, NOT the slice index: Store.Create assigns line_no = i+1
// (store.go, the INSERT's third bind), and LineItemInput's own doc pins it
// as "system-assigned 1..N by the slice's array position" ([D10]). A 0-based
// LineNo here would make the dry-run emit line_no 0 where the real run emits
// 1 -- a gratuitous, reportable divergence in the exact field [payload-mapper]
// exists to keep identical.
//
// ID is deliberately LEFT EMPTY. mbsLine omits an empty id ([payload-line-id]),
// which is precisely what no-duplicate-line-items' `!has(x.id)` guard needs to
// skip a dry-run line. Emitting "" instead would give every dry-run line the
// SAME id and fire that rule on every multi-line dry-run invoice.
//
// Status/Violations/RuleSetVersionID/CreatedAt are likewise left at their zero
// value: MBSPayload reads none of them, so a dry-run needs no id and no
// persisted state to be evaluated faithfully.
//
// KNOWN INCOMPLETENESS (recorded, deliberately NOT fixed -- Stage-1 F5), the
// second of two on this path alongside M4-06's store-level duplicate rule,
// which cannot be evaluated against rows that are not there:
//
//	Money written with a LEADING ZERO ("0100", "007", "-0100") diverges. This
//	mapper carries the RAW CreateInput text, while the real path's money makes
//	a round trip through Postgres ('0100'::text::numeric -> RETURNING ::text ->
//	"100"). classify does NOT quarantine it: decimalNumberRe accepts "0100", so
//	the group is READY. But jsonNumberRe REJECTS it -- JSON forbids leading
//	zeros where Postgres numeric accepts them -- so jsonNumber falls back to the
//	raw STRING, and 04's toFloat rejects strings: range/tax_math VIOLATE on the
//	dry-run and PASS on the real run. Same invoice, two verdicts.
//
//	This is NOT fixed by tightening bestEffortBadNumericField: that would
//	quarantine a row M4-03 ACCEPTS, moving rows_valid/rows_invalid/
//	quarantined_invoices -- redefining shipped counters M4-08 is being built
//	against ([import-report-shape], Core AC#5). Nor by normalizing here: a
//	second normalizer that must agree with Postgres forever is the very drift
//	[payload-mapper] exists to prevent.
//
//	The direction of the error is what makes it acceptable: a FALSE VIOLATION
//	in an advisory preview. It never launders a real failure into "clean".
func invoiceFromCreateInput(in invoice.CreateInput) invoice.Invoice {
	inv := invoice.Invoice{
		EntityID:      in.EntityID,
		ImportBatchID: in.ImportBatchID,
		InvoiceNumber: in.InvoiceNumber,
		IssueDate:     in.IssueDate,
		SupplierTIN:   in.SupplierTIN,
		SupplierName:  in.SupplierName,
		BuyerTIN:      in.BuyerTIN,
		BuyerName:     in.BuyerName,
		Currency:      in.Currency,
		Subtotal:      in.Subtotal,
		VAT:           in.VAT,
		Total:         in.Total,

		InvoiceKind:        in.InvoiceKind,
		TaxCurrencyCode:    in.TaxCurrencyCode,
		DueDate:            in.DueDate,
		IssueTime:          in.IssueTime,
		TaxPointDate:       in.TaxPointDate,
		PaymentStatus:      in.PaymentStatus,
		SupplierEmail:      in.SupplierEmail,
		SupplierTelephone:  in.SupplierTelephone,
		SupplierStreet:     in.SupplierStreet,
		SupplierCity:       in.SupplierCity,
		SupplierPostalZone: in.SupplierPostalZone,
		SupplierCountry:    in.SupplierCountry,
		SupplierState:      in.SupplierState,
		SupplierLGA:        in.SupplierLGA,
		BuyerEmail:         in.BuyerEmail,
		BuyerTelephone:     in.BuyerTelephone,
		BuyerStreet:        in.BuyerStreet,
		BuyerCity:          in.BuyerCity,
		BuyerPostalZone:    in.BuyerPostalZone,
		BuyerCountry:       in.BuyerCountry,
		BuyerState:         in.BuyerState,
		BuyerLGA:           in.BuyerLGA,
	}
	for i, li := range in.LineItems {
		inv.LineItems = append(inv.LineItems, invoice.LineItem{
			LineNo:      i + 1,
			Description: li.Description,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
			LineTotal:   li.LineTotal,
			LineTax:     li.LineTax,

			TaxCategory:               li.TaxCategory,
			HSNCode:                   li.HSNCode,
			ISICCode:                  li.ISICCode,
			ProductCategory:           li.ProductCategory,
			ServiceCategory:           li.ServiceCategory,
			SellersItemIdentification: li.SellersItemIdentification,
			PriceUnit:                 li.PriceUnit,
			TaxPercent:                li.TaxPercent,
			BaseQuantity:              li.BaseQuantity,
		})
	}
	return inv
}

// M4-06-01: an against-store `(entity, invoice_number)` collision reports as
// a first-class, rule-shaped violation (RuleKey + Severity set) rather than
// the bare pre-M4-06 RowError{Message:"already imported"} shape -- see
// storeDuplicateRowError's own doc comment.
const (
	ruleKeyDuplicateInvoiceNumber = "no-duplicate-invoice-number"
	msgDuplicateInvoiceNumber     = "An invoice with this number already exists for this entity."
)

// storeDuplicateRowError builds the RowError for an against-store duplicate
// (both the upfront precheck at ExistingNumbers time and the racing-INSERT
// backstop at Create time), so both emit sites report the IDENTICAL enriched
// shape (DUP-03). invoiceID is the collided invoice's stored id, or "" when
// none is resolvable (the racing-INSERT backstop) -- omitempty then omits
// the key entirely.
func storeDuplicateRowError(headerRow int, rowIdxs []int, invoiceID string) RowError {
	return RowError{
		Rows:      sheetRows(headerRow, rowIdxs),
		Field:     "invoice_number",
		RuleKey:   ruleKeyDuplicateInvoiceNumber,
		Severity:  "error",
		InvoiceID: invoiceID,
		Message:   msgDuplicateInvoiceNumber,
	}
}

// domainCreateErrorMessage reports whether createErr is one of the DOMAIN
// errors invoice.Store.Create can return for genuinely bad input --
// invoice.ErrDuplicateNumber (a 23505 racing past ExistingNumbers's upfront
// precheck, [dedup]) or invoice.ErrValidation (a residual bad value the
// classify step above didn't catch) -- and, if so, a sanitized,
// human-readable message naming the reason (never createErr.Error()'s raw
// Postgres text, which can leak internals). Any OTHER error (a connection
// failure, a context cancellation, an unexpected bug) is NOT a domain error:
// ok is false, and the caller must abort the run rather than quarantine it
// as bad data.
func domainCreateErrorMessage(createErr error) (msg string, ok bool) {
	switch {
	case errors.Is(createErr, invoice.ErrDuplicateNumber):
		return "invoice number already imported", true
	case errors.Is(createErr, invoice.ErrValidation):
		return "one or more fields failed validation", true
	default:
		return "", false
	}
}

// Import is the spreadsheet-path orchestration entrypoint (THE HEART): map ->
// normalize -> group -> classify -> (dry-run classify-only | real
// CreateBatch/Create/Finalize). ImportDocument (document.go) is the second
// entrypoint, for the document path.
//
//  1. Resolve mapping -> column indices against header (ErrValidation before
//     any write if invoice_number is unmapped, or a mapped header string is
//     absent from header).
//  2. Group data rows by their mapped invoice_number value
//     ([grouping], non-contiguous OK); a blank/empty invoice_number is
//     ungroupable -> quarantined with a scalar-Row RowError citing its own
//     sheet row.
//  3. Classify each group: a numeric cell on any row that uses a comma as
//     the decimal mark quarantines it first (RowError.Field the offending
//     field); else an in-file header-field disagreement quarantines
//     it (RowError.Rows = every one of the group's sheet rows, [dedup]/
//     [errors-shape]); else a non-empty issue_date that doesn't parse as
//     YYYY-MM-DD quarantines it too (RowError.Field "issue_date" -- Core
//     AC#7: a badly-formatted date must never be silently NULLed, only a
//     genuinely blank cell reads as NULL); else a non-empty numeric-mapped
//     cell that doesn't
//     parse as a plain decimal quarantines it too (RowError.Field the
//     offending field -- Core AC#2: dry-run must report the EXACT same
//     verdict the real import produces, so numeric validity is checked HERE,
//     not deferred to Postgres's ::numeric cast at Create time, which a
//     dry-run never reaches); else an against-stored hit (one
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
//     READY group, invoice.Store.Create; only a DOMAIN error (ErrDuplicateNumber
//     -- a concurrent 23505 racing past the upfront ExistingNumbers check,
//     [dedup]; or ErrValidation -- a residual bad value the classify step
//     above didn't catch) quarantines just that group with a sanitized
//     message, and the run continues ([batch semantics], partial success).
//     Any OTHER error is operational, not bad input (e.g. a DB outage): the
//     whole run aborts, Finalize best-effort records 'failed', and the raw
//     error propagates (the handler 500s) rather than being laundered into a
//     fake RowError. Finalize records the terminal counts/status/errors.
//
// filename (already sanitized by the caller) is passed straight through to
// BOTH the early-finalize CreateBatch below and the main-path CreateBatch --
// a zero-row file's 'failed' batch must be attributable too (BULK-01-11). It
// is unused on the dry-run path: a dry run never creates a batch.
//
// documentID travels with it and lands on both the batch and every invoice the
// run creates ([pointer-on-invoice]). It is likewise unused on the dry-run
// path, which writes nothing to point at anything. "" is legal and persists as
// NULL: a caller with no source document is still a caller.
//
// headerRow is the file row the header was read from; every row number counts
// from it.
func (s *Service) Import(ctx context.Context, entityID, filename, documentID string, headerRow int, mapping map[string]string, header []string, rows [][]string, dryRun bool) (BatchResult, error) {
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
		if field := commaDecimalField(rows, colIndex, g.rowIdxs); field != "" {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(headerRow, g.rowIdxs),
				Field:   field,
				Message: fmt.Sprintf("%s uses a comma as the decimal mark; write it with a dot, e.g. 1234.56", field),
			})
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		if field := headerConflictField(rows, colIndex, g.rowIdxs); field != "" {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(headerRow, g.rowIdxs),
				Field:   field,
				Message: fmt.Sprintf("rows disagree on %s", field),
			})
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		if field, dateErr := dateParseError(rows, colIndex, g.rowIdxs); dateErr != nil {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(headerRow, g.rowIdxs),
				Field:   field,
				Message: dateErr.Error(),
			})
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		if timeErr := issueTimeError(rows, colIndex, g.rowIdxs); timeErr != nil {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(headerRow, g.rowIdxs),
				Field:   "issue_time",
				Message: timeErr.Error(),
			})
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		if field := bestEffortBadNumericField(rows, colIndex, g.rowIdxs); field != "" {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(headerRow, g.rowIdxs),
				Field:   field,
				Message: fmt.Sprintf("%s is not a valid number", field),
			})
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		if field := numericRangeField(rows, colIndex, g.rowIdxs); field != "" {
			lim := numericRange[field]
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(headerRow, g.rowIdxs),
				Field:   field,
				Message: fmt.Sprintf("%s must have at most %d digits before the decimal point and %d after", field, lim.intDigits, lim.scale),
			})
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		if invoiceID, ok := existing[num]; ok {
			errorsList = append(errorsList, storeDuplicateRowError(headerRow, g.rowIdxs, invoiceID))
			quarantinedInvoices++
			invalidRows += len(g.rowIdxs)
			continue
		}
		readyGroups = append(readyGroups, g)
	}

	for _, i := range ungroupableRows {
		errorsList = append(errorsList, RowError{
			Row:     sheetRow(headerRow, i),
			Message: "blank invoice number: row cannot be grouped",
		})
		quarantinedInvoices++
		invalidRows++
	}

	supplierName, supplierTIN, err := s.batch.EntitySupplier(ctx, entityID)
	if err != nil {
		return BatchResult{}, err
	}
	// Restore the entity TIN's MBS wire spelling ONCE, here at the entity ->
	// invoice boundary, so the real (buildCreateInput below) and dry-run paths
	// -- which both read this single variable -- structurally cannot disagree.
	// EntitySupplier itself keeps returning the row EXACTLY as stored.
	//
	// invoice.MBSSupplierTIN, not a local copy (INVCR-01-17, C7 fix: SHARED
	// with internal/invoice.Store.Create, which restores the identical way
	// for the manual POST /v1/invoices path -- one owner, not two). For the
	// REAL (non-dry-run) path below, Store.Create now re-derives and
	// OVERWRITES buildCreateInput's supplierName/supplierTIN from the SAME
	// entity anyway, so this call's result is authoritative ONLY for the
	// dry-run preview (invoiceFromCreateInput), which never reaches
	// Store.Create at all -- the real path's recompute is a harmless no-op,
	// not a second, possibly-diverging decision.
	supplierTIN = invoice.MBSSupplierTIN(supplierTIN)

	rowsTotal := len(rows)
	rowsInvalid := invalidRows
	rowsValid := rowsTotal - rowsInvalid

	if dryRun {
		res := BatchResult{
			RowsTotal:           rowsTotal,
			RowsValid:           rowsValid,
			RowsInvalid:         rowsInvalid,
			ReadyInvoices:       len(readyGroups),
			QuarantinedInvoices: quarantinedInvoices,
			Errors:              errorsList,
		}

		// [Stage-1 F2] Guard on the CALLER's knowledge, never on the
		// returned value. Gate.Evaluate short-circuits an empty batch to a
		// ZERO-VALUE RuleSetVersion (it needs no round trip to know nothing
		// violates nothing), and a returned 0 is indistinguishable from a
		// genuine version 0 -- only this caller knows the batch was empty.
		// Nothing evaluated => rule_set_version stays nil => JSON null, not
		// a false "0" stamp ([import-report-shape], IMPV-16).
		if len(readyGroups) == 0 {
			return res, nil
		}

		// Everything below is REPORT-ONLY and writes NOTHING
		// ([dry-run-evaluates]): no CreateBatch, no Create, no
		// ApplyValidation. Evaluate is the same no-write call the real
		// path's ValidateBatch wraps, so the preview runs the SAME rules
		// against the SAME payload shape the real run will.
		items := make([]invoice.EvalItem, len(readyGroups))
		for i, g := range readyGroups {
			// batchID is "" -- no batch exists on a dry-run and none is
			// minted. MBSPayload never reads ImportBatchID, so it cannot
			// reach 04 or affect a single verdict.
			in := buildCreateInput(entityID, rows, colIndex, g, "", "", headerRow, supplierName, supplierTIN)
			// Ref is the invoice_number, not an id: no id exists yet
			// pre-Create. 04 echoes Ref back untouched and never interprets
			// it, and group numbers are unique by construction (groups is
			// keyed by them), so the refs cannot collide.
			items[i] = invoice.EvalItem{Ref: g.number, Invoice: invoiceFromCreateInput(in)}
		}

		eval, err := s.gate.Evaluate(ctx, items)
		if err != nil {
			// An unreachable 04 is an OUTAGE, not "everything is clean" --
			// propagate raw (the handler 502/503s) rather than report a
			// clean preview nobody evaluated ([create-error-classification]).
			return BatchResult{}, err
		}

		version := eval.RuleSetVersion
		res.RuleSetVersion = &version
		for _, g := range readyGroups {
			// ByRef is TOTAL over the sent refs (Validator.Validate refuses
			// any response that is not), so a nil here means genuinely no
			// violations -- never an invoice 04 silently skipped.
			vs := eval.ByRef[g.number]
			// invoice.HasBlockingViolation is the SAME predicate
			// ApplyValidation promotes on ([Stage-1 F1]) -- so this preview's
			// count is identical BY CONSTRUCTION to what the real run then
			// does, not merely intended to agree. len(vs)==0 would be WRONG:
			// a warning-only invoice carries violations and still promotes.
			if invoice.HasBlockingViolation(vs) {
				res.InvoicesWithViolations++
			} else {
				res.InvoicesClean++
			}
			if len(vs) > 0 {
				res.InvoiceViolations = append(res.InvoiceViolations, InvoiceViolations{
					InvoiceNumber: g.number,
					// InvoiceID stays "" -> omitempty omits it: no id
					// exists yet ([Stage-1 F7]).
					Rows:       sheetRows(headerRow, g.rowIdxs),
					Violations: vs,
				})
			}
		}
		return res, nil
	}

	// The run itself can't report anything for a header with zero data
	// rows: mint the batch (so the attempt is auditable) and finalize it
	// straight to 'failed' — never CreateBatch/Create for a real group,
	// never a partial-split status for this case.
	if rowsTotal == 0 {
		batchID, err := s.batch.CreateBatch(ctx, entityID, filename, documentID, headerRow)
		if err != nil {
			return BatchResult{}, err
		}
		if err := s.batch.Finalize(ctx, batchID, 0, 0, 0, nil, "failed"); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{ID: batchID, Status: "failed"}, nil
	}

	batchID, err := s.batch.CreateBatch(ctx, entityID, filename, documentID, headerRow)
	if err != nil {
		return BatchResult{}, err
	}

	// created pairs each successfully-created invoice with the sheet rows it
	// came from. Store.Create RETURNS the Invoice with its LineItems ALREADY
	// HYDRATED (store.go's Create tx appends every RETURNING-ed line item),
	// so the gate below re-reads NOTHING for the whole batch (AC#13) -- and
	// must not: Store.List leaves LineItems nil by design ([D7]), and
	// MBSPayload is pure and CANNOT tell a nil LineItems from a genuinely
	// line-less invoice, so a List-sourced batch would omit line_items and
	// make line-items-required violate every PERFECTLY VALID invoice of a
	// 500-row import. The rowIdxs travel WITH the invoice rather than in a
	// parallel slice because a group can drop out mid-loop on a domain error:
	// index alignment would be an invariant waiting to break.
	type createdInvoice struct {
		inv     invoice.Invoice
		rowIdxs []int
	}
	var created []createdInvoice

	readyCount := 0
	for _, g := range readyGroups {
		in := buildCreateInput(entityID, rows, colIndex, g, batchID, documentID, headerRow, supplierName, supplierTIN)
		inv, createErr := s.inv.Create(ctx, in)
		if createErr == nil {
			readyCount++
			created = append(created, createdInvoice{inv: inv, rowIdxs: g.rowIdxs})
			continue
		}

		msg, isDomainErr := domainCreateErrorMessage(createErr)
		if !isDomainErr {
			// An operational failure (e.g. a DB outage, a context
			// cancellation, an unexpected bug) is NOT bad input -- never
			// quarantine it as N invalid rows, and never leak createErr's raw
			// Postgres text to the client. Best-effort finalize the batch as
			// 'failed' (its own error, if any, is secondary to createErr) and
			// abort with the real error so the handler 500s instead of lying
			// about a 'completed' run.
			_ = s.batch.Finalize(ctx, batchID, rowsTotal, rowsValid, rowsInvalid, errorsList, "failed")
			return BatchResult{}, createErr
		}

		if errors.Is(createErr, invoice.ErrDuplicateNumber) {
			// [dedup] A concurrent 23505 racing past the upfront
			// ExistingNumbers precheck -- report the SAME enriched shape the
			// precheck itself emits (DUP-04), never the bare generic form.
			// No id: the racing tx's row may not even be visible yet.
			errorsList = append(errorsList, storeDuplicateRowError(headerRow, g.rowIdxs, ""))
		} else {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(headerRow, g.rowIdxs),
				Field:   bestEffortBadNumericField(rows, colIndex, g.rowIdxs),
				Message: msg,
			})
		}
		quarantinedInvoices++
		rowsInvalid += len(g.rowIdxs)
		rowsValid -= len(g.rowIdxs)
	}

	// [import-validates] Run every created draft through the SAME gate the
	// manual POST /v1/invoices/{id}/validate path uses -- ONE 04 round trip
	// for the whole file ([batch-of-one]), then one atomic ApplyValidation
	// per invoice. This is what makes an import actually IMPORT AND VALIDATE:
	// clean invoices land `validated`, dirty ones stay `draft` carrying their
	// violations. Stamping violations without promoting was rejected -- it
	// leaves a 500-invoice import with 500 unvalidated drafts and forces 500
	// manual clicks.
	//
	// This runs BEFORE Finalize on purpose: a fault here must finalize the
	// batch `failed` ONCE, not walk back a `completed` it already wrote.
	//
	// Quarantined groups cannot reach here at all -- they were never created,
	// so `created` is exactly the set 04 sees (IMPV-09).
	var ruleSetVersion *int
	invoicesClean := 0
	invoicesWithViolations := 0
	var invoiceViolations []InvoiceViolations

	// [Stage-1 F2] Same caller-side guard as the dry-run: an all-quarantined
	// file creates zero invoices, and ValidateBatch would return version 0
	// having evaluated nothing (its loop body never runs, so no
	// rule_set_version_id can reach the DB either). Guard on len(created),
	// never on the returned version -- nothing evaluated => null, not 0.
	if len(created) > 0 {
		invs := make([]invoice.Invoice, len(created))
		for i, c := range created {
			invs[i] = c.inv
		}

		outcome, valErr := s.gate.ValidateBatch(ctx, invs)
		if valErr != nil {
			// [Stage-1 F6] ABORT UNCONDITIONALLY -- never route this through
			// domainCreateErrorMessage. ValidateBatch wraps ApplyValidation's
			// error with %w, and ApplyValidation CAN return ErrValidation (a
			// 22P02); domainCreateErrorMessage matches on errors.Is, so
			// reusing it here would quarantine a DB FAULT as bad data --
			// exactly the laundering [create-error-classification] forbids. A
			// validator ErrUpstream is likewise an OUTAGE, not "everything is
			// clean". Best-effort finalize `failed` (its own error is
			// secondary to valErr) and propagate valErr RAW so the handler
			// 500s instead of lying about a completed run.
			_ = s.batch.Finalize(ctx, batchID, rowsTotal, rowsValid, rowsInvalid, errorsList, "failed")
			return BatchResult{}, valErr
		}

		version := outcome.RuleSetVersion
		ruleSetVersion = &version
		// Clean/WithViolations come STRAIGHT from the outcome -- ValidateBatch
		// already counted them with the same blocking predicate that decided
		// each promotion. Recounting here would be a second predicate to keep
		// in sync forever.
		invoicesClean = outcome.Clean
		invoicesWithViolations = outcome.WithViolations
		for _, c := range created {
			vs := outcome.ByID[c.inv.ID]
			if len(vs) == 0 {
				continue
			}
			invoiceViolations = append(invoiceViolations, InvoiceViolations{
				InvoiceNumber: c.inv.InvoiceNumber,
				InvoiceID:     c.inv.ID,
				Rows:          sheetRows(headerRow, c.rowIdxs),
				Violations:    vs,
			})
		}
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

		RuleSetVersion:         ruleSetVersion,
		InvoicesClean:          invoicesClean,
		InvoicesWithViolations: invoicesWithViolations,
		InvoiceViolations:      invoiceViolations,
	}, nil
}
