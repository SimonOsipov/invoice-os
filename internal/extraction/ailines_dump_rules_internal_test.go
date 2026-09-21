// Specs for aliEngineLines and the AIR-01 Stage B wiring (aitEngineHeaders, aitDumpJSON's
// engine_lines member). Helpers live in ailines_internal_test.go / aitext_harness_internal_test.go.
package extraction

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// --- test-local fixture builders --------------------------------------------------------

// aldGappedReceiptPages is a synthetic 3-row table whose middle row's only cell sits in an
// unclassified column, so its DocLine is entirely nil -- an INTERIOR gap, unlike the CAC
// receipt's trailing one. Header words are liLexicon keys, data cells are invented.
func aldGappedReceiptPages() []Page {
	tbl := Table{
		Rows: 4, Cols: 3,
		Cells: []TableCell{
			{Row: 0, Col: 0, Text: "PAYMENT DATE"},
			{Row: 0, Col: 1, Text: "SERVICE DESCRIPTION"},
			{Row: 0, Col: 2, Text: "AMOUNT (NGN)"},
			{Row: 1, Col: 1, Text: "Registration fee"},
			{Row: 1, Col: 2, Text: "5,000.00"},
			{Row: 2, Col: 0, Text: "Footer note"}, // the row's ONLY cell; col 0 classifies to no role
			{Row: 3, Col: 1, Text: "Amount paid"},
			{Row: 3, Col: 2, Text: "5,000.00"},
		},
	}
	return []Page{{Number: 1, Tables: []Table{tbl}}}
}

// aldMismatchedPages is a synthetic 2-row table whose row 2 fails reconcileLines' arithmetic
// check (2 x 100.00 != 500.00); row 1 balances and stays clean.
func aldMismatchedPages() []Page {
	tbl := Table{
		Rows: 3, Cols: 4,
		Cells: []TableCell{
			{Row: 0, Col: 0, Text: "DESCRIPTION"},
			{Row: 0, Col: 1, Text: "QTY"},
			{Row: 0, Col: 2, Text: "RATE"},
			{Row: 0, Col: 3, Text: "AMOUNT"},
			{Row: 1, Col: 0, Text: "Steel Rods"},
			{Row: 1, Col: 1, Text: "4"},
			{Row: 1, Col: 2, Text: "1,000.00"},
			{Row: 1, Col: 3, Text: "4,000.00"},
			{Row: 2, Col: 0, Text: "Cement Bags"},
			{Row: 2, Col: 1, Text: "2"},
			{Row: 2, Col: 2, Text: "100.00"},
			{Row: 2, Col: 3, Text: "500.00"},
		},
	}
	return []Page{{Number: 1, Tables: []Table{tbl}}}
}

// aldWildRuledPages replays the committed wild_ruled_lines_totals golden -- Tables is nil from
// PDFiumReader, so this is the only way to get a real DocLine set in this package.
func aldWildRuledPages(t *testing.T) []Page {
	t.Helper()
	pages, _, _ := aitReadDoclingFile(t, filepath.Join("testdata", "wild_ruled_lines_totals.docling.json"))
	return pages
}

// aldWildRuledResults runs the full pipeline over the wild_ruled golden the way TestAIText_Dump
// does, so the RED specs replay the real engine, not a hand-built Input.
func aldWildRuledResults(t *testing.T) []FieldResult {
	t.Helper()
	pages, tokens, _ := aitReadDoclingFile(t, filepath.Join("testdata", "wild_ruled_lines_totals.docling.json"))
	return Reconcile(Input{Candidates: Resolve(tokens, RuleSet{Tier1: Tier1Rules}), Lines: LineItems(pages), Pages: tokens})
}

// --- AC-1: grouped by index, ascending -------------------------------------------------------

func TestAliEngineLines_GroupsReconcilesRowsByIndexInAscendingOrder(t *testing.T) {
	results := []FieldResult{
		{Field: Field{Name: "line_items[2].description", Value: aliStr("Roofing Sheets"), Reason: ReasonNone}},
		{Field: Field{Name: "line_items[1].line_total", Value: aliStr("4000.00"), Reason: ReasonNone}},
		{Field: Field{Name: "line_items[2].quantity", Value: aliStr("2"), Reason: ReasonNone}},
		{Field: Field{Name: "line_items[1].description", Value: aliStr("Steel Rods"), Reason: ReasonNone}},
	}
	got := aliEngineLines(results)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Index != 1 || got[1].Index != 2 {
		t.Fatalf("indexes = [%d, %d], want [1, 2]", got[0].Index, got[1].Index)
	}
	if len(got[0].Roles) != 2 || len(got[1].Roles) != 2 {
		t.Fatalf("role counts = [%d, %d], want [2, 2]", len(got[0].Roles), len(got[1].Roles))
	}
	if r := got[0].Roles[LineRoleDescription]; r.Value == nil || *r.Value != "Steel Rods" || r.Reason != string(ReasonNone) {
		t.Errorf("got[0].Roles[description] = %+v, want Steel Rods/none", r)
	}
	if r := got[0].Roles[LineRoleLineTotal]; r.Value == nil || *r.Value != "4000.00" || r.Reason != string(ReasonNone) {
		t.Errorf("got[0].Roles[line_total] = %+v, want 4000.00/none", r)
	}
	if r := got[1].Roles[LineRoleDescription]; r.Value == nil || *r.Value != "Roofing Sheets" || r.Reason != string(ReasonNone) {
		t.Errorf("got[1].Roles[description] = %+v, want Roofing Sheets/none", r)
	}
	if r := got[1].Roles[LineRoleQuantity]; r.Value == nil || *r.Value != "2" || r.Reason != string(ReasonNone) {
		t.Errorf("got[1].Roles[quantity] = %+v, want 2/none", r)
	}
}

func TestAliEngineLines_TheRuledWildGoldenGroupsIntoThreeAscendingEntries(t *testing.T) {
	got := aliEngineLines(aldWildRuledResults(t))
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	if got[0].Index != 1 || got[1].Index != 2 || got[2].Index != 3 {
		t.Fatalf("indexes = [%d, %d, %d], want [1, 2, 3]", got[0].Index, got[1].Index, got[2].Index)
	}
}

// --- AC-2: a present role key means a populated cell -------------------------------------------

func TestAliEngineLines_APresentRoleKeyMeansAPopulatedCell(t *testing.T) {
	if len(LineRoles) != 5 {
		t.Fatalf("len(LineRoles) = %d, want 5", len(LineRoles))
	}
	pages := aldWildRuledPages(t)
	lines := LineItems(pages)
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}
	got := aliEngineLines(aldWildRuledResults(t))
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}

	for i, line := range lines {
		entry := got[i]
		if entry.Index != line.Index {
			t.Fatalf("got[%d].Index = %d, want %d", i, entry.Index, line.Index)
		}
		for _, role := range LineRoles {
			cell := line.Cell(role)
			r, present := entry.Roles[role]
			if cell == nil && present {
				t.Errorf("line %d role %s: present in engine_lines, want absent (cell is nil)", line.Index, role)
			}
			if cell != nil {
				if !present {
					t.Errorf("line %d role %s: absent from engine_lines, want present (cell = %q)", line.Index, role, *cell)
				} else if r.Value == nil || *r.Value == "" {
					t.Errorf("line %d role %s: Value = %v, want a non-empty reading", line.Index, role, r.Value)
				}
			}
		}
	}
}

// --- AC-3: an all-absent row leaves a gap, not a renumbering --------------------------------

func TestAliEngineLines_ARowWithNoReadableCellLeavesAGapInTheIndexes(t *testing.T) {
	pages := aldGappedReceiptPages()
	lines := LineItems(pages)
	if len(lines) != 3 {
		t.Fatalf("len(lines) = %d, want 3", len(lines))
	}
	for _, role := range LineRoles {
		if lines[1].Cell(role) != nil {
			t.Fatalf("lines[1] (the gap row) has role %s populated, want every role nil", role)
		}
	}

	got := aliEngineLines(Reconcile(Input{Lines: lines}))
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Index != 1 || got[1].Index != 3 {
		t.Fatalf("indexes = [%d, %d], want [1, 3]", got[0].Index, got[1].Index)
	}
}

// --- AC-4: a flagged line_total keeps its inconsistent reason ---------------------------------

func TestAliEngineLines_AFlaggedLineTotalKeepsItsInconsistentReason(t *testing.T) {
	pages := aldMismatchedPages()
	lines := LineItems(pages)
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	got := aliEngineLines(Reconcile(Input{Lines: lines}))
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}

	if r := got[0].Roles[LineRoleLineTotal]; r.Reason != string(ReasonNone) {
		t.Errorf("row 1 line_total reason = %q, want none (4 x 1000.00 balances)", r.Reason)
	}
	row2Total := got[1].Roles[LineRoleLineTotal]
	if row2Total.Reason != string(ReasonInconsistent) {
		t.Errorf("row 2 line_total reason = %q, want inconsistent (2 x 100.00 != 500.00)", row2Total.Reason)
	}
	if row2Total.Value == nil || *row2Total.Value != "500.00" {
		t.Errorf("row 2 line_total value = %v, want 500.00 (the printed reading, not the computed one)", row2Total.Value)
	}
	if r := got[1].Roles[LineRoleQuantity]; r.Reason != string(ReasonNone) {
		t.Errorf("row 2 quantity reason = %q, want none: the flag is per role, not per entry", r.Reason)
	}
}

// --- AC-5: a header field and the line_items block row are never emitted ----------------------

func TestAliEngineLines_AHeaderFieldAndTheBlockRowAreNeverEmitted(t *testing.T) {
	if _, _, ok := ParseLineFieldName("total"); ok {
		t.Fatalf("ParseLineFieldName(%q) ok = true, want false", "total")
	}
	if _, _, ok := ParseLineFieldName("line_items"); ok {
		t.Fatalf("ParseLineFieldName(%q) ok = true, want false", "line_items")
	}

	results := []FieldResult{
		{Field: Field{Name: "total", Value: aliStr("100.00"), Reason: ReasonNone}},
		{Field: Field{Name: "line_items", Reason: ReasonMissing}},
		{Field: Field{Name: "line_items[1].description", Value: aliStr("Steel Rods"), Reason: ReasonNone}},
	}
	got := aliEngineLines(results)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Index != 1 {
		t.Fatalf("got[0].Index = %d, want 1", got[0].Index)
	}
	if len(got[0].Roles) != 1 {
		t.Fatalf("len(got[0].Roles) = %d, want 1", len(got[0].Roles))
	}
	if _, ok := got[0].Roles[LineRoleDescription]; !ok {
		t.Errorf("got[0].Roles has no %s key", LineRoleDescription)
	}
}

// --- AC-7: the ruled wild golden's row 3 carries no unit_price ---------------------------------

// The cell reads "500.00 Total" and reAmount is anchored, so this is the engine's own reading,
// not the printed truth; TestLineItems_TheRuledWildFixtureNowMapsRateAndAmount pins the same
// absence at the LineItems level. Do not widen reAmount to close it.
func TestAliEngineLines_TheRuledWildRowThreeCarriesNoUnitPrice(t *testing.T) {
	got := aliEngineLines(aldWildRuledResults(t))
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	if _, ok := got[0].Roles[LineRoleUnitPrice]; !ok {
		t.Errorf("got[0].Roles has no unit_price, want present")
	}
	if _, ok := got[1].Roles[LineRoleUnitPrice]; !ok {
		t.Errorf("got[1].Roles has no unit_price, want present")
	}
	if _, ok := got[2].Roles[LineRoleUnitPrice]; ok {
		t.Errorf("got[2].Roles has unit_price, want absent")
	}
	if got[2].Index != 3 {
		t.Fatalf("got[2].Index = %d, want 3", got[2].Index)
	}
	if len(got[2].Roles) != 3 {
		t.Fatalf("len(got[2].Roles) = %d, want 3 (description, quantity, line_total)", len(got[2].Roles))
	}
}

// --- premise: every emitted line row carries a value -------------------------------------------

func TestAliEngineLines_EveryEmittedLineRowCarriesAValue(t *testing.T) {
	results := aldWildRuledResults(t)
	parsed := 0
	for _, r := range results {
		if _, _, ok := ParseLineFieldName(r.Name); !ok {
			continue
		}
		parsed++
		if r.Value == nil {
			t.Errorf("%s: Value = nil, want a value", r.Name)
		}
	}
	if parsed < 3 {
		t.Fatalf("parsed %d line-field result(s), want at least 3", parsed)
	}
}

// --- AC-6a: the driver's engine map stays headers-only beside engine_lines ---------------------

func TestAitEngineHeaders_KeepsOnlyHeaderFieldsBesideEngineLines(t *testing.T) {
	results := aldWildRuledResults(t)
	if len(results) == 0 {
		t.Fatalf("len(results) = 0, want > 0")
	}
	parsed := 0
	for _, r := range results {
		if _, _, ok := ParseLineFieldName(r.Name); ok {
			parsed++
		}
	}
	if parsed < 3 {
		// Without this, corpus_totals_block (zero line rows) would pass the filter assertion
		// below vacuously -- this fixture must actually carry line rows to exercise the filter.
		t.Fatalf("parsed %d line-field result(s), want at least 3", parsed)
	}

	engine := aitEngineHeaders(results)
	if len(engine) != len(HeaderFields) {
		t.Fatalf("len(engine) = %d, want %d (len(HeaderFields))", len(engine), len(HeaderFields))
	}
	for k := range engine {
		if !containsString(HeaderFields, k) {
			t.Errorf("engine key %q is not a HeaderFields member", k)
		}
		if _, _, ok := ParseLineFieldName(k); ok {
			t.Errorf("engine key %q parses as a line field, want headers only", k)
		}
	}
	if _, ok := engine["line_items"]; ok {
		t.Errorf("engine has a line_items key, want headers only")
	}

	lines := aliEngineLines(results)
	if len(lines) != 3 {
		t.Fatalf("len(aliEngineLines(results)) = %d, want 3", len(lines))
	}
}

// --- AC-6b: engine_lines is appended after the AIR-01 members, byte for byte ------------------

// aitDumpAIR01JSON freezes AIR-01's dump members. engine_lines must be purely additive, so
// everything before it must still marshal byte for byte the same.
type aitDumpAIR01JSON struct {
	File      string                       `json:"file"`
	Set       string                       `json:"set"`
	TextChars int                          `json:"text_chars"`
	Pages     []aitDumpPageJSON            `json:"pages"`
	Engine    map[string]aitDumpEngineJSON `json:"engine"`
}

func TestAitDumpJSON_EngineLinesIsAppendedAfterTheAIR01Members(t *testing.T) {
	pages, tokens, res := aitReadDoclingFile(t, filepath.Join("testdata", "corpus_totals_block.docling.json"))
	results := Reconcile(Input{Candidates: Resolve(tokens, RuleSet{Tier1: Tier1Rules}), Lines: LineItems(pages), Pages: tokens})

	lines := aitPromptLines(tokens)
	byPage := map[int][]promptLine{}
	for _, l := range lines {
		byPage[l.Page] = append(byPage[l.Page], l)
	}
	pagesJSON := make([]aitDumpPageJSON, 0, len(tokens))
	for _, p := range tokens {
		tokJSON := make([]aitDumpTokenJSON, 0, len(p.Tokens))
		for _, tk := range p.Tokens {
			tokJSON = append(tokJSON, aitDumpTokenJSON{Text: tk.Text, Box: aitBoxJSON{
				Page: tk.Region.Page, X0: tk.Region.X0, Y0: tk.Region.Y0, X1: tk.Region.X1, Y1: tk.Region.Y1,
			}})
		}
		lineJSON := make([]aitDumpLineJSON, 0, len(byPage[p.Number]))
		for _, l := range byPage[p.Number] {
			segs := make([]aitDumpSegmentJSON, 0, len(l.Segments))
			for _, s := range l.Segments {
				segs = append(segs, aitDumpSegmentJSON{X: s.X, Text: s.Text})
			}
			lineJSON = append(lineJSON, aitDumpLineJSON{Y: l.Y, Segments: segs})
		}
		pagesJSON = append(pagesJSON, aitDumpPageJSON{Number: p.Number, Tokens: tokJSON, Lines: lineJSON})
	}

	engine := aitEngineHeaders(results)
	if len(engine) != len(HeaderFields) {
		t.Fatalf("len(engine) = %d, want %d", len(engine), len(HeaderFields))
	}
	if len(pagesJSON) == 0 {
		t.Fatalf("len(pagesJSON) = 0, want > 0")
	}
	lines2 := aliEngineLines(results)
	if len(lines2) != 0 {
		t.Fatalf("len(aliEngineLines(results)) = %d, want 0 (corpus_totals_block has no line items)", len(lines2))
	}

	air01 := aitDumpAIR01JSON{File: "corpus_totals_block.pdf", Set: "corpus", TextChars: res.TextChars, Pages: pagesJSON, Engine: engine}
	air01Bytes, err := json.MarshalIndent(air01, "", "  ")
	if err != nil {
		t.Fatalf("marshal air01 shape: %v", err)
	}

	full := aitDumpJSON{File: "corpus_totals_block.pdf", Set: "corpus", TextChars: res.TextChars, Pages: pagesJSON, Engine: engine, EngineLines: lines2}
	fullBytes, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		t.Fatalf("marshal full shape: %v", err)
	}

	want := strings.TrimSuffix(string(air01Bytes), "\n}") + ",\n  \"engine_lines\": []\n}"
	if string(fullBytes) != want {
		t.Errorf("aitDumpJSON bytes changed beyond the additive engine_lines tail\ngot:  %s\nwant: %s", fullBytes, want)
	}
}
