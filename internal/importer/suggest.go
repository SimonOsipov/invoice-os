// suggest.go: csvrun.py's SYSTEM/SCHEMA and the AIR-07 mapping guard. Pure Go; no HTTP, no DB.
//
// AIR-07-01 Mode A: declarations only. mappingSystem, mappingIntro, mappingRowFmt and
// sampleRows are stub values pinned wrong on purpose (their measured-harness tests must fail
// on assertion, not on a missing symbol); every function returns its zero value until AIR-07's
// executor stage implements it for real.
package importer

import "encoding/json"

const (
	// sampleRows is csvrun.py's SAMPLE_ROWS (:17), real value 5. Stub 0 here;
	// TestSampleRows_MatchesTheMeasuredHarness fails on assertion until the real value lands.
	// NOT handlers.go's maxSampleRows, which caps the preview response.
	sampleRows = 0
	// maxDetectableHeaderRow: a choice, not a derived literal (D-03) -- no measured-harness
	// test reads this one, so it carries its real value already.
	maxDetectableHeaderRow = 5
	windowRows             = maxDetectableHeaderRow + sampleRows
	defaultHeaderRow       = 1
	mappingSchemaName      = "column_mapping" // csvrun.py:87
	// mappingIntro is csvrun.py's intro (:61). Stub value; TestMappingPrompt_MatchesTheMeasuredHarness
	// fails on assertion until the real text lands.
	mappingIntro = ""
	// mappingRowFmt is csvrun.py's `Row {i}: {csv_line(r)}` (:66), Go-formatted. Same stub note.
	mappingRowFmt = ""
)

// mappingFields is csvrun.py's FIELDS (:13-14), in order. Same eleven, same order, as
// canonicalFields (service.go:154-166).
var mappingFields = []string{"invoice_number", "issue_date", "buyer_tin", "buyer_name",
	"currency", "subtotal", "vat", "total", "line_description", "line_quantity", "line_unit_price"}

// mappingSystem is csvrun.py's SYSTEM (:19-40), byte for byte once implemented. Stub value;
// TestMappingPrompt_MatchesTheMeasuredHarness fails on assertion until the real text lands.
const mappingSystem = ""

// mappingSchema is mappingSchemaJSON()'s output, built once at package init.
var mappingSchema = mappingSchemaJSON()

// mappingSchemaJSON mirrors aiSchemaFor (aireading.go:55-68). Stub: no schema yet, so a real
// ai.Client call is refused (TestMappingSchema_IsAcceptedByTheAIClient fails on assertion).
func mappingSchemaJSON() json.RawMessage {
	return nil
}

// suggestWindow reshapes Decode's header+rows into the AI's window (§6). Stub: always nil.
func suggestWindow(header []string, rows [][]string) [][]string {
	return nil
}

// mappingPromptText renders csvrun.py's `rows` user text. Stub: always empty.
func mappingPromptText(rows [][]string) string {
	return ""
}

// csvLine renders one row the way csvrun.py's csv_line does. Stub: always empty.
func csvLine(row []string) string {
	return ""
}

// guardHeaderRow validates the AI's header_row claim against the window (§6). Stub: always 0,
// never a real answer (real fallback is defaultHeaderRow=1), so every assertion reds.
func guardHeaderRow(ans map[string]any, windowLen int) int {
	return 0
}

// guardPlacements validates the AI's field->header claims against header (§6). Stub: always
// nil, so no field is ever placed and the "non-nil map" assertion reds too.
func guardPlacements(ans map[string]any, header []string) map[string]string {
	return nil
}
