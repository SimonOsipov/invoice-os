// publiccheck.go: the tenantless Readiness check's mapping suggestion.
package importer

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
	"github.com/SimonOsipov/invoice-os/internal/platform/airecord"
)

// CheckMappingKeys is the import keys plus the two supplier keys a check can place.
var CheckMappingKeys = append(invoicefields.ImportKeys(), "supplier_name", "supplier_tin")

// checkMappingSystem is mappingSystem plus the two supplier definitions.
var checkMappingSystem = strings.Replace(mappingSystem, "\n\nAlso return:",
	"\n- supplier_name: the seller's (supplier's) name, the business that issued the invoice. Not the buyer's."+
		"\n- supplier_tin: the seller's (supplier's) Tax Identification Number. Not the buyer's TIN or an RC number."+
		"\n\nAlso return:", 1)

var checkMappingSchema = mappingSchemaFor(CheckMappingKeys)

var checkFieldsAllowed = func() map[string]bool {
	m := make(map[string]bool, len(CheckMappingKeys))
	for _, k := range CheckMappingKeys {
		m[k] = true
	}
	return m
}()

// CheckSuggestion is SuggestCheckMapping's answer; Mapping is never nil.
type CheckSuggestion struct {
	Source     string
	HeaderRow  int
	Columns    []string
	SampleRows [][]string
	RowsTotal  int
	Mapping    map[string]string
}

// SuggestCheckMapping is SuggestMappingHandler's decode-to-guard ladder for a visitor's
// file, with the supplier-aware prompt and no entity, document or saved mapping.
func SuggestCheckMapping(ctx context.Context, s MappingSuggester, file []byte, format string) (CheckSuggestion, error) {
	hdr1, rows1, _, err := Decode(ctx, bytes.NewReader(file), format)
	if err != nil {
		return CheckSuggestion{}, err
	}
	window := suggestWindow(hdr1, rows1)
	if len(window) == 0 {
		return checkSuggestion("none", defaultHeaderRow, hdr1, rows1, nil), nil
	}

	ans := askMappingWith(airecord.WithPublicCheck(ctx), s, window, checkMappingSystem, checkMappingSchema)
	headerRow := guardHeaderRow(ans, len(window))
	header, rows := hdr1, rows1
	if headerRow != defaultHeaderRow {
		h, rs, _, derr := DecodeFrom(ctx, bytes.NewReader(file), format, headerRow)
		if derr != nil || len(h) == 0 {
			headerRow = defaultHeaderRow
		} else {
			header, rows = h, rs
		}
	}

	mapping := guardPlacementsIn(ans, header, checkFieldsAllowed)
	source := "ai"
	if len(mapping) == 0 && headerRow == defaultHeaderRow {
		source = "none"
	}
	return checkSuggestion(source, headerRow, header, rows, mapping), nil
}

func checkSuggestion(source string, headerRow int, header []string, rows [][]string, mapping map[string]string) CheckSuggestion {
	if mapping == nil {
		mapping = map[string]string{}
	}
	return CheckSuggestion{
		Source:     source,
		HeaderRow:  headerRow,
		Columns:    coerceColumns(header),
		SampleRows: coerceSampleRows(rows),
		RowsTotal:  len(rows),
		Mapping:    mapping,
	}
}

// CheckSheetInvoice is one checked invoice; Invoice is nil when Problem is set.
type CheckSheetInvoice struct {
	Number  string
	Invoice *invoice.Invoice
	Problem *RowError
}

// CheckSheet holds the first invoices of a file; the totals count the whole file.
type CheckSheet struct {
	Invoices      []CheckSheetInvoice
	RowsTotal     int
	InvoicesTotal int
	// BlankNumberRows is one RowError per row Service.Import cannot group; the whole file's.
	BlankNumberRows []RowError
}

// CheckSpreadsheet reads the first limit invoice groups of a file in file order.
// A group that fails Service.Import's classification carries its Problem.
func CheckSpreadsheet(ctx context.Context, file []byte, format string, headerRow int, mapping map[string]string, limit int) (CheckSheet, error) {
	if headerRow == 0 {
		headerRow = defaultHeaderRow
	}
	header, rows, _, err := DecodeFrom(ctx, bytes.NewReader(file), format, headerRow)
	if err != nil {
		return CheckSheet{}, err
	}
	colIndex, err := resolveMappingIn(mapping, header, checkFieldsAllowed)
	if err != nil {
		return CheckSheet{}, err
	}

	groups := map[string]*invoiceGroup{}
	var order []string
	var blank []RowError
	invNumIdx := colIndex["invoice_number"]
	for i, row := range rows {
		var raw string
		if invNumIdx < len(row) {
			raw = row[invNumIdx]
		}
		if strings.TrimSpace(raw) == "" {
			blank = append(blank, RowError{Row: sheetRow(headerRow, i), Message: blankNumberMessage})
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

	out := CheckSheet{RowsTotal: len(rows), InvoicesTotal: len(order), BlankNumberRows: blank}
	for _, num := range order[:min(limit, len(order))] {
		g := groups[num]
		item := CheckSheetInvoice{Number: num}
		if p := classifyGroup(rows, colIndex, g, headerRow); p != nil {
			item.Problem = p
		} else {
			in := buildCreateInput("", rows, colIndex, g, "", "", headerRow, "", nil, invoice.SupplierContact{}, "")
			firstRow := rows[g.rowIdxs[0]]
			in.SupplierName = invoice.TrimmedOrNil(fieldValue(firstRow, colIndex, "supplier_name"))
			in.SupplierTIN = invoice.MBSSupplierTIN(invoice.TrimmedOrNil(fieldValue(firstRow, colIndex, "supplier_tin")))
			inv := invoiceFromCreateInput(in)
			item.Invoice = &inv
		}
		out.Invoices = append(out.Invoices, item)
	}
	return out, nil
}

// classifyGroup runs Service.Import's per-group checks in its order; nil means ready.
func classifyGroup(rows [][]string, colIndex map[string]int, g *invoiceGroup, headerRow int) *RowError {
	re := func(field, msg string) *RowError {
		return &RowError{Rows: sheetRows(headerRow, g.rowIdxs), Field: field, Message: msg}
	}
	if field := commaDecimalField(rows, colIndex, g.rowIdxs); field != "" {
		return re(field, fmt.Sprintf("%s uses a comma as the decimal mark; write it with a dot, e.g. 1234.56", field))
	}
	if field := headerConflictField(rows, colIndex, g.rowIdxs); field != "" {
		return re(field, fmt.Sprintf("rows disagree on %s", field))
	}
	if field, dateErr := dateParseError(rows, colIndex, g.rowIdxs); dateErr != nil {
		return re(field, dateErr.Error())
	}
	if timeErr := issueTimeError(rows, colIndex, g.rowIdxs); timeErr != nil {
		return re("issue_time", timeErr.Error())
	}
	if field := bestEffortBadNumericField(rows, colIndex, g.rowIdxs); field != "" {
		return re(field, fmt.Sprintf("%s is not a valid number", field))
	}
	if field := numericRangeField(rows, colIndex, g.rowIdxs); field != "" {
		lim := numericRange[field]
		return re(field, fmt.Sprintf("%s must have at most %d digits before the decimal point and %d after", field, lim.intDigits, lim.scale))
	}
	return nil
}

// CheckField is one field of a document reading; it mirrors extraction.FieldResult's decided value.
type CheckField struct {
	Name   string
	Value  *string
	Reason *string
}

// CheckDocumentInvoice builds the invoice a document reading describes, supplier included.
// A reading with no invoice number, or the poor-scan marker, is a RowError.
func CheckDocumentInvoice(fields []CheckField) (*invoice.Invoice, *RowError) {
	ef := make([]extractedField, len(fields))
	values := make(map[string]*string, len(fields))
	for i, f := range fields {
		ef[i] = extractedField{Name: f.Name, Value: f.Value, Reason: f.Reason}
		values[f.Name] = f.Value
	}
	in, rowErr := documentCreateInput("", "", SettledExtraction{Fields: ef})
	if rowErr != nil {
		return nil, rowErr
	}
	in.SourceDocumentID = nil // no stored document
	in.SupplierName = invoice.TrimmedOrNil(values["supplier_name"])
	in.SupplierTIN = invoice.MBSSupplierTIN(invoice.TrimmedOrNil(values["supplier_tin"]))
	inv := invoiceFromCreateInput(in)
	return &inv, nil
}
