// publiccheck.go: the tenantless Readiness check's mapping suggestion.
package importer

import (
	"bytes"
	"context"
	"strings"

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
