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

// canonicalFields is the closed set of column keys a mapping is allowed to
// use -- the 11 fields Import actually understands. A mapping key outside
// this set (e.g. a typo like "totla") is rejected in resolveMapping, by
// exact symmetry with the mapped-header-absent-from-row-1 check just below
// it: [mapping]'s guarantee is that the server structurally cannot mis-map,
// which requires rejecting an unrecognized KEY just as firmly as it rejects
// a mapped HEADER string that doesn't exist -- silently ignoring an unknown
// key would import that canonical field as NULL with no error at all.
var canonicalFields = map[string]bool{
	"invoice_number":   true,
	"issue_date":       true,
	"buyer_tin":        true,
	"buyer_name":       true,
	"currency":         true,
	"subtotal":         true,
	"vat":              true,
	"total":            true,
	"line_description": true,
	"line_quantity":    true,
	"line_unit_price":  true,
}

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
// numeric, or trimming surrounding whitespace if field is issue_date — a
// shared helper for headerConflictField and bestEffortBadNumericField, which
// both need the raw (not nil-on-blank) normalized string rather than
// fieldValue's *string/nil-on-blank shape. issue_date's trim mirrors
// parseIssueDate's own strings.TrimSpace: without it, "2026-01-10" and
// " 2026-01-10 " in the same group would spuriously conflict in
// headerConflictField despite parseIssueDate resolving them to the identical
// stored date.
func cellAt(row []string, idx int, field string) string {
	var v string
	if idx < len(row) {
		v = row[idx]
	}
	switch {
	case numericFields[field]:
		v = normalizeNumeric(v)
	case field == "issue_date":
		v = strings.TrimSpace(v)
	}
	return v
}

// bestEffortBadNumericField scans the group's 5 numeric fields (header
// fields off rowIdxs[0], line fields off every row), returning the FIRST
// whose normalized value doesn't parse as a plain decimal number. It serves
// two callers: (1) Import's classify step, where it is now authoritative —
// promoted from a post-Create diagnostic per Core AC#2 ("the same file +
// mapping dry-run gets the EXACT verdict the real import will produce"):
// numeric validity used to be deferred entirely to Postgres's ::numeric cast
// at Create time, which a dry-run never reaches, so a non-numeric cell (e.g.
// "N/A") wrongly reported READY in dry-run but quarantined for real. Checking
// it here, in BOTH dry-run and real, closes that gap — mirroring how
// issueDateParseError already quarantines a non-empty unparseable issue_date
// at classify time rather than at Create; (2) Create's error path (Import,
// below), where it is still a best-effort diagnostic: if Create returns
// invoice.ErrValidation for a reason THIS scan didn't catch, its SQLSTATE
// (22P02) doesn't itself disambiguate which column broke, so this gives
// RowError.Field a best guess. Returns "" if no numeric field is clearly bad.
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

// canonicalFIRSTIN matches the 12-digit canonical form portfolio's ValidateTIN
// (internal/portfolio/tin.go) produces for a FIRS TIN -- the hyphen-stripped
// spelling of NNNNNNNN-NNNN. A 10-digit JTB TIN is deliberately NOT matched
// (see mbsSupplierTIN).
var canonicalFIRSTIN = regexp.MustCompile(`^\d{12}$`)

// mbsSupplierTIN restores the MBS wire spelling of an ENTITY's TIN as it
// crosses the entity -> invoice boundary ([supplier-from-entity]).
//
// WHY THIS EXISTS: portfolio.ValidateTIN accepts a FIRS TIN in either
// spelling (bare 12-digit or hyphenated NNNNNNNN-NNNN) and CANONICALIZES it
// to 12 bare digits before persisting (tin.go:39, strings.Replace(trimmed,
// "-", "", 1)) -- its own doc: "both spellings of a FIRS TIN persist
// identically". The MBS rule supplier-tin-format
// (migrations/20260711121327_seed_mbs_v1.sql) demands ^[0-9]{8}-[0-9]{4}$.
// So an entity created through the REAL API path carries a TIN the wire rule
// rejects, and every invoice imported for it reports a FALSE
// supplier-tin-format violation -- a firm's valid invoices rejected.
//
// WHY HERE, and not in invoice.MBSPayload: an ENTITY tin is KNOWN-VALID (it
// passed ValidateTIN + Luhn on the way in, and WE stripped the hyphen), so
// restoring the spelling we removed is a faithful MAPPING, not a repair. A
// user-supplied invoice TIN (POST /v1/invoices) has UNKNOWN validity, and
// re-hyphenating it in the pure wire mapper would be FIXING USER DATA --
// breaking store-invalid-faithfully (migrations/20260714103137_invoices.sql's
// header), under which a malformed TIN MUST still violate. Restore the format
// only where we stripped it.
//
// SHAPES -- the exact inverse of tin.go's canonicalization, which is the only
// thing that writes business_entities.tin:
//   - 12 bare digits -> NNNNNNNN-NNNN. Both FIRS spellings canonicalize to the
//     same 12 digits and ARE the same TIN (tin.go's own doc), so both map onto
//     the single MBS spelling.
//   - 10-digit JTB TIN -> UNCHANGED. There is no hyphen to restore, and an 8+4
//     split would fabricate a FIRS TIN out of a JTB one. Such a TIN genuinely
//     cannot satisfy supplier-tin-format: that is a REAL violation to report
//     (flagged by M4-04-08), not a formatting bug to paper over here.
//   - anything else -- an already-hyphenated row we never canonicalized
//     (db/demo-reset.sql's literals, raw-seeded fixtures) -> UNCHANGED.
//
// nil (an entity with no TIN) stays nil: supplier-tin-required fires, which is
// the correct pre-existing signal (IMPV-12).
func mbsSupplierTIN(tin *string) *string {
	if tin == nil || !canonicalFIRSTIN.MatchString(*tin) {
		return tin
	}
	wire := (*tin)[:8] + "-" + (*tin)[8:]
	return &wire
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
// value: MBSPayload reads none of them (it projects invoice_number, issue_date,
// currency, the three money fields, supplier/buyer and line_items only), so a
// dry-run needs no id and no persisted state to be evaluated faithfully.
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
	}
	for i, li := range in.LineItems {
		inv.LineItems = append(inv.LineItems, invoice.LineItem{
			LineNo:      i + 1,
			Description: li.Description,
			Quantity:    li.Quantity,
			UnitPrice:   li.UnitPrice,
			LineTotal:   li.LineTotal,
			LineTax:     li.LineTax,
		})
	}
	return inv
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
//     genuinely blank cell reads as NULL); else a non-empty numeric-mapped
//     cell (subtotal/vat/total/line_quantity/line_unit_price) that doesn't
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
		if field := bestEffortBadNumericField(rows, colIndex, g.rowIdxs); field != "" {
			errorsList = append(errorsList, RowError{
				Rows:    sheetRows(g.rowIdxs),
				Field:   field,
				Message: fmt.Sprintf("%s is not a valid number", field),
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
	// Restore the entity TIN's MBS wire spelling ONCE, here at the entity ->
	// invoice boundary, so the real (buildCreateInput below) and dry-run paths
	// -- which both read this single variable -- structurally cannot disagree.
	// EntitySupplier itself keeps returning the row EXACTLY as stored.
	supplierTIN = mbsSupplierTIN(supplierTIN)

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
			in := buildCreateInput(entityID, rows, colIndex, g, "", supplierName, supplierTIN)
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
					Rows:       sheetRows(g.rowIdxs),
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
		in := buildCreateInput(entityID, rows, colIndex, g, batchID, supplierName, supplierTIN)
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

		errorsList = append(errorsList, RowError{
			Rows:    sheetRows(g.rowIdxs),
			Field:   bestEffortBadNumericField(rows, colIndex, g.rowIdxs),
			Message: msg,
		})
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
				Rows:          sheetRows(c.rowIdxs),
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
