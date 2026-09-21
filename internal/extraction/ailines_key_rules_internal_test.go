// Specs for the line-item answer key: aliLoadKey's refusal ladder, the key/dump coverage
// fence and the key's own census. Helpers live in ailines_key_internal_test.go.
package extraction

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- test-local fixture builders (no plan helper lives here) ---------------------------

// aliKeyFileConfirmation is a valid confirmation block for fixtures that are not exercising
// AC-1's refusal itself.
func aliKeyFileConfirmation() aitConfirmationJSON {
	return aitConfirmationJSON{Source: "test", AnswerFile: "test.answer.md", Recorded: "2026-09-21"}
}

// aliWriteKeyFile writes a line key JSON file: confirmation (nil omits the block entirely)
// plus docs, keyed by filename.
func aliWriteKeyFile(t *testing.T, confirmation any, docs map[string]any) string {
	t.Helper()
	data := map[string]any{}
	if confirmation != nil {
		data["confirmation"] = confirmation
	}
	for file, doc := range docs {
		data[file] = doc
	}
	return writeJSONFile(t, "key.line_items.json", data)
}

// aliKeyDocEntry builds one key document entry. rows == nil omits the "rows" key entirely --
// the AC-3 "no rows list" fixture needs that distinction from a present empty list.
func aliKeyDocEntry(confirmed bool, rows []map[string]any) map[string]any {
	doc := map[string]any{"confirmed": confirmed}
	if rows != nil {
		doc["rows"] = rows
	}
	return doc
}

// --- AC-1: the confirmation marker ----------------------------------------------------------

func TestAliLoadKey_AKeyWithNoConfirmationMarkerIsRefused(t *testing.T) {
	path := aliWriteKeyFile(t, nil, map[string]any{
		"doc.pdf": aliKeyDocEntry(true, []map[string]any{}),
	})
	if _, err := aliLoadKey(path); err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Errorf("aliLoadKey = %v, want a refusal naming confirmation", err)
	}
}

func TestAliLoadKey_AKeyWithABlankAnswerFileIsRefused(t *testing.T) {
	blank := aitConfirmationJSON{Source: "test", AnswerFile: "", Recorded: "2026-09-21"}
	path := aliWriteKeyFile(t, blank, map[string]any{
		"doc.pdf": aliKeyDocEntry(true, []map[string]any{}),
	})
	if _, err := aliLoadKey(path); err == nil || !strings.Contains(err.Error(), "answer_file") {
		t.Errorf("aliLoadKey = %v, want a refusal naming answer_file", err)
	}
}

func TestAliLoadKey_ACompleteConfirmationMarkerLoads(t *testing.T) {
	path := aliWriteKeyFile(t, aliKeyFileConfirmation(), map[string]any{
		"doc.pdf": aliKeyDocEntry(true, []map[string]any{}),
	})
	key, err := aliLoadKey(path)
	if err != nil {
		t.Fatalf("aliLoadKey: %v", err)
	}
	if len(key.Docs) != 1 {
		t.Errorf("len(key.Docs) = %d, want 1", len(key.Docs))
	}
}

// --- AC-2: the accepted-role set, in two halves ----------------------------------------------

func TestAliLoadKey_EveryRoleInLineRolesIsAccepted(t *testing.T) {
	if len(LineRoles) != 5 {
		t.Fatalf("len(LineRoles) = %d, want 5", len(LineRoles))
	}
	want := map[string]string{
		LineRoleDescription: "Widget",
		LineRoleQuantity:    "2",
		LineRoleUnitPrice:   "500",
		LineRoleLineTotal:   "1000",
		LineRoleLineTax:     "75", // non-null: 38 of 50 real rows are null here
	}
	row := map[string]any{}
	for role, v := range want {
		row[role] = v
	}
	path := aliWriteKeyFile(t, aliKeyFileConfirmation(), map[string]any{
		"doc.pdf": aliKeyDocEntry(true, []map[string]any{row}),
	})

	key, err := aliLoadKey(path)
	if err != nil {
		t.Fatalf("aliLoadKey: %v", err)
	}
	doc := key.Docs["doc.pdf"]
	if len(doc.Rows) != 1 {
		t.Fatalf("len(doc.Rows) = %d, want 1", len(doc.Rows))
	}
	line := doc.Rows[0]
	// ⊇ half: set equality's other half is TestAliLoadKey_ARoleOutsideLineRolesIsRefusedByName.
	for _, role := range LineRoles {
		got := line.Cell(role)
		if got == nil || *got != want[role] {
			t.Errorf("role %q = %v, want %q", role, got, want[role])
		}
	}
}

func TestAliLoadKey_ARoleOutsideLineRolesIsRefusedByName(t *testing.T) {
	cases := []string{"amount", "line_item", "Description", "unit price", "line_items", ""}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	for _, role := range cases {
		t.Run(role, func(t *testing.T) {
			path := aliWriteKeyFile(t, aliKeyFileConfirmation(), map[string]any{
				"doc.pdf": aliKeyDocEntry(true, []map[string]any{{role: "x"}}),
			})
			_, err := aliLoadKey(path)
			if err == nil {
				t.Fatalf("aliLoadKey(role %q) = nil error, want a refusal", role)
			}
			if !strings.Contains(err.Error(), role) {
				t.Errorf("error %q does not name role %q", err.Error(), role)
			}
		})
	}
}

func TestAliKey_TheRealKeysRoleSetEqualsLineRoles(t *testing.T) {
	path := aliRealKeyPath(t)
	if path == "" {
		return
	}
	key, err := aliLoadKey(path)
	if err != nil {
		t.Fatalf("aliLoadKey: %v", err)
	}
	rowsRead := 0
	for _, doc := range key.Docs {
		rowsRead += len(doc.Rows)
	}
	if rowsRead == 0 {
		t.Fatal("read 0 rows off the real key; the role-set check above would pass vacuously")
	}
	t.Logf("real key: %d row(s) loaded with no role refusal", rowsRead)
}

// --- AC-3: the three states, plus the refused fourth -------------------------------------------

func TestAliLoadKey_AConfirmedZeroRowControlIsNotAnUnconfirmedDocument(t *testing.T) {
	path := aliWriteKeyFile(t, aliKeyFileConfirmation(), map[string]any{
		"zero.pdf":  aliKeyDocEntry(true, []map[string]any{}),
		"other.pdf": aliKeyDocEntry(true, []map[string]any{{LineRoleDescription: "X"}}),
	})
	key, err := aliLoadKey(path)
	if err != nil {
		t.Fatalf("aliLoadKey: %v", err)
	}
	if len(key.Docs) != 2 {
		t.Fatalf("len(key.Docs) = %d, want 2", len(key.Docs))
	}
	doc := key.Docs["zero.pdf"]
	if doc.Rows == nil {
		t.Errorf("Rows = nil, want a non-nil empty slice")
	}
	if len(doc.Rows) != 0 {
		t.Errorf("len(Rows) = %d, want 0", len(doc.Rows))
	}
	if !doc.Confirmed {
		t.Errorf("Confirmed = false, want true")
	}
	if !doc.aliScoredZeroRow() {
		t.Errorf("aliScoredZeroRow() = false, want true")
	}
}

func TestAliLoadKey_AnUnconfirmedDocumentScoresNoneOfItsRows(t *testing.T) {
	path := aliWriteKeyFile(t, aliKeyFileConfirmation(), map[string]any{
		"scored.pdf":      aliKeyDocEntry(true, []map[string]any{{LineRoleDescription: "A"}}),
		"unconfirmed.pdf": aliKeyDocEntry(false, []map[string]any{{LineRoleDescription: "B"}}),
	})
	key, err := aliLoadKey(path)
	if err != nil {
		t.Fatalf("aliLoadKey: %v", err)
	}
	scored, unconfirmed := aliScoredDocs(key)
	if len(scored)+len(unconfirmed) != 2 {
		t.Fatalf("len(scored)+len(unconfirmed) = %d, want 2", len(scored)+len(unconfirmed))
	}
	if !containsString(scored, "scored.pdf") || containsString(unconfirmed, "scored.pdf") {
		t.Errorf("scored=%v unconfirmed=%v, want scored.pdf in scored only", scored, unconfirmed)
	}
	if !containsString(unconfirmed, "unconfirmed.pdf") || containsString(scored, "unconfirmed.pdf") {
		t.Errorf("scored=%v unconfirmed=%v, want unconfirmed.pdf in unconfirmed only", scored, unconfirmed)
	}
}

func TestAliLoadKey_ADocumentWithNoRowsListIsRefused(t *testing.T) {
	path := aliWriteKeyFile(t, aliKeyFileConfirmation(), map[string]any{
		"doc.pdf": map[string]any{"confirmed": true}, // no "rows" key at all
	})
	_, err := aliLoadKey(path)
	if err == nil {
		t.Fatal("aliLoadKey = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "rows") {
		t.Errorf("error %q does not mention rows", err.Error())
	}
}

// --- AC-4: the fence is a pure function --------------------------------------------------------

func TestAliKeyCoverageProblems_ReturnsItsProblemsToTheCaller(t *testing.T) {
	dumps := []docDump{{File: "scored_no_key.pdf", Set: "user", TextChars: 100}}
	key := aliKey{Docs: map[string]aliKeyDoc{
		"ghost.pdf": {Confirmed: true, Rows: []DocLine{}},
	}}

	problems := aliKeyCoverageProblems(key, dumps, nil)
	if len(problems) != 2 {
		t.Fatalf("len(problems) = %d, want 2: %v", len(problems), problems)
	}
}

// --- AC-5: the four problems, in order -----------------------------------------------------

func TestAliKeyCoverageProblems_ZeroDumpsIsItselfAProblem(t *testing.T) {
	key := aliKey{Docs: map[string]aliKeyDoc{}}
	problems := aliKeyCoverageProblems(key, nil, nil)
	if len(problems) != 1 {
		t.Fatalf("len(problems) = %d, want 1: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "0 dump") {
		t.Errorf("problems[0] = %q, want it to mention 0 dumps", problems[0])
	}
}

func TestAliKeyCoverageProblems_AScoredDumpWithNoKeyRowIsReported(t *testing.T) {
	dumps := []docDump{
		{File: "known.pdf", Set: "user", TextChars: 100},
		{File: "unknown.pdf", Set: "user", TextChars: 100},
	}
	key := aliKey{Docs: map[string]aliKeyDoc{
		"known.pdf": {Confirmed: true, Rows: []DocLine{{Index: 1, Description: aliStr("A")}}},
	}}

	problems := aliKeyCoverageProblems(key, dumps, nil)
	if len(problems) != 1 {
		t.Fatalf("len(problems) = %d, want 1: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "1 scored document(s)") {
		t.Errorf("problems[0] = %q, want it to mention 1 scored document(s)", problems[0])
	}
}

func TestAliKeyCoverageProblems_AKeyRowMatchingNoDocumentIsReported(t *testing.T) {
	dumps := []docDump{{File: "known.pdf", Set: "user", TextChars: 100}}
	key := aliKey{Docs: map[string]aliKeyDoc{
		"known.pdf": {Confirmed: true, Rows: []DocLine{{Index: 1, Description: aliStr("A")}}},
		"ghost.pdf": {Confirmed: true, Rows: []DocLine{{Index: 1, Description: aliStr("B")}}},
	}}

	problems := aliKeyCoverageProblems(key, dumps, nil)
	if len(problems) != 1 {
		t.Fatalf("len(problems) = %d, want 1: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "1 key row(s)") {
		t.Errorf("problems[0] = %q, want the literal count %q", problems[0], "1 key row(s)")
	}
}

func TestAliKeyCoverageProblems_ATextlessOrNotScoredDocumentAccountsForItsKeyRow(t *testing.T) {
	dumps := []docDump{{File: "textless.pdf", Set: "user", TextChars: 0}}
	notScored := []notScoredEntry{{File: "not_scored.pdf", Reason: "corrupt"}}
	key := aliKey{Docs: map[string]aliKeyDoc{
		"textless.pdf":   {Confirmed: true, Rows: []DocLine{}},
		"not_scored.pdf": {Confirmed: true, Rows: []DocLine{{Index: 1, Description: aliStr("A")}}},
	}}

	problems := aliKeyCoverageProblems(key, dumps, notScored)
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
}

// --- AC-6: the only names a failure may print ---------------------------------------------

func TestAliKeyCoverageProblems_ADumpWhoseSetContradictsItsNameIsReported(t *testing.T) {
	dumps := []docDump{{File: "corpus_mislabeled.pdf", Set: "user", TextChars: 100}}
	key := aliKey{Docs: map[string]aliKeyDoc{
		"corpus_mislabeled.pdf": {Confirmed: true, Rows: []DocLine{{Index: 1, Description: aliStr("A")}}},
	}}

	problems := aliKeyCoverageProblems(key, dumps, nil)
	if len(problems) != 1 {
		t.Fatalf("len(problems) = %d, want 1: %v", len(problems), problems)
	}
	if !strings.Contains(problems[0], "contradicts") {
		t.Errorf("problems[0] = %q, want it to mention the contradiction", problems[0])
	}
}

func TestAliKeyCoverageProblems_TheFailureNamesCorpusStemsAndNoUserFile(t *testing.T) {
	dumps := []docDump{{File: "accounted.pdf", Set: "user", TextChars: 100}}
	key := aliKey{Docs: map[string]aliKeyDoc{
		"accounted.pdf":         {Confirmed: true, Rows: []DocLine{{Index: 1, Description: aliStr("A")}}},
		"corpus_ghost.pdf":      {Confirmed: true, Rows: []DocLine{{Index: 1, Description: aliStr("B")}}},
		"not-a-corpus-name.pdf": {Confirmed: true, Rows: []DocLine{{Index: 1, Description: aliStr("C")}}},
	}}

	problems := aliKeyCoverageProblems(key, dumps, nil)
	if len(problems) != 1 {
		t.Fatalf("len(problems) = %d, want 1: %v", len(problems), problems)
	}
	text := problems[0]
	if !strings.Contains(text, "corpus_ghost") {
		t.Errorf("problems[0] = %q, want it to name corpus_ghost", text)
	}
	if strings.Contains(text, "not-a-corpus-name") {
		t.Errorf("problems[0] = %q, must not name a user file", text)
	}
}

// --- AC-7: the census, env-free and mutation-graded --------------------------------------

func TestAliKeyTotals_SplitsValueCellsFromCorrectNulls(t *testing.T) {
	docA := aliKeyDoc{
		Confirmed: true,
		Rows: []DocLine{
			{Index: 1, Description: aliStr("Widget"), Quantity: aliStr("2"), UnitPrice: aliStr("500"), LineTotal: aliStr("1000"), LineTax: aliStr("75")},
			{Index: 2, Description: aliStr("Gadget"), Quantity: nil, UnitPrice: aliStr("  "), LineTotal: aliStr("250"), LineTax: nil},
		},
	}
	docB := aliKeyDoc{Confirmed: true, Rows: []DocLine{}} // the zero-row control
	docC := aliKeyDoc{Confirmed: false, Rows: []DocLine{
		{Index: 1, Description: aliStr("X"), Quantity: aliStr("1"), UnitPrice: aliStr("1"), LineTotal: aliStr("1"), LineTax: aliStr("1")},
	}}
	key := aliKey{Docs: map[string]aliKeyDoc{"a.pdf": docA, "b.pdf": docB, "c.pdf": docC}}

	totals := aliKeyTotals(key)
	if totals.Rows != 2 {
		t.Fatalf("Rows = %d, want 2", totals.Rows)
	}
	if totals.Docs != 3 {
		t.Errorf("Docs = %d, want 3", totals.Docs)
	}
	if totals.ZeroRowControls != 1 {
		t.Errorf("ZeroRowControls = %d, want 1", totals.ZeroRowControls)
	}
	if totals.Unconfirmed != 1 {
		t.Errorf("Unconfirmed = %d, want 1", totals.Unconfirmed)
	}
	if totals.ValueCells != 7 {
		t.Errorf("ValueCells = %d, want 7", totals.ValueCells)
	}
	if totals.NullCells != 3 {
		t.Errorf("NullCells = %d, want 3", totals.NullCells)
	}
	wantNulls := map[string]int{
		LineRoleDescription: 0, LineRoleQuantity: 1, LineRoleUnitPrice: 1, LineRoleLineTotal: 0, LineRoleLineTax: 1,
	}
	for role, want := range wantNulls {
		if got := totals.NullsByRole[role]; got != want {
			t.Errorf("NullsByRole[%q] = %d, want %d", role, got, want)
		}
	}
}

func TestAliKey_TheKeyOnDiskHoldsTwentyOneDocumentsFiftyRowsAndTwelveControls(t *testing.T) {
	path := aliRealKeyPath(t)
	if path == "" {
		return
	}
	key, err := aliLoadKey(path)
	if err != nil {
		t.Fatalf("aliLoadKey: %v", err)
	}
	if len(key.Docs) == 0 {
		t.Fatal("read 0 documents off the real key")
	}
	totals := aliKeyTotals(key)
	if totals.Docs != 21 {
		t.Errorf("Docs = %d, want 21", totals.Docs)
	}
	if totals.Rows != 50 {
		t.Errorf("Rows = %d, want 50", totals.Rows)
	}
	if totals.ZeroRowControls != 12 {
		t.Errorf("ZeroRowControls = %d, want 12", totals.ZeroRowControls)
	}
	if totals.Unconfirmed != 0 {
		t.Errorf("Unconfirmed = %d, want 0", totals.Unconfirmed)
	}
}

func TestAliKey_TheKeyOnDiskSplitsTwoHundredFourValueCellsFromFortySixNulls(t *testing.T) {
	path := aliRealKeyPath(t)
	if path == "" {
		return
	}
	key, err := aliLoadKey(path)
	if err != nil {
		t.Fatalf("aliLoadKey: %v", err)
	}
	if len(key.Docs) == 0 {
		t.Fatal("read 0 documents off the real key")
	}
	totals := aliKeyTotals(key)
	if totals.ValueCells != 204 {
		t.Errorf("ValueCells = %d, want 204", totals.ValueCells)
	}
	if totals.NullCells != 46 {
		t.Errorf("NullCells = %d, want 46", totals.NullCells)
	}
	wantNulls := map[string]int{
		LineRoleDescription: 0, LineRoleQuantity: 4, LineRoleUnitPrice: 4, LineRoleLineTotal: 0, LineRoleLineTax: 38,
	}
	for role, want := range wantNulls {
		if got := totals.NullsByRole[role]; got != want {
			t.Errorf("NullsByRole[%q] = %d, want %d", role, got, want)
		}
	}
}

// --- AC-8: the provenance record, env-free -------------------------------------------------

func TestAliProvenanceProblem_APresentRecordIsNoProblem(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key.provenance.md"), []byte("recorded"), 0o644); err != nil {
		t.Fatalf("write provenance: %v", err)
	}
	keyPath := filepath.Join(dir, "key.line_items.json")
	if got := aliProvenanceProblem(keyPath); got != "" {
		t.Errorf("aliProvenanceProblem = %q, want none", got)
	}
}

func TestAliProvenanceProblem_AMissingRecordIsReported(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "key.line_items.json")
	got := aliProvenanceProblem(keyPath)
	if got == "" {
		t.Fatal("aliProvenanceProblem = \"\", want a problem")
	}
	if !strings.Contains(got, "no provenance record") {
		t.Errorf("aliProvenanceProblem = %q, want it to say no record", got)
	}
}

func TestAliProvenanceProblem_AnEmptyRecordIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key.provenance.md"), nil, 0o644); err != nil {
		t.Fatalf("write empty provenance: %v", err)
	}
	keyPath := filepath.Join(dir, "key.line_items.json")
	got := aliProvenanceProblem(keyPath)
	if got == "" {
		t.Fatal("aliProvenanceProblem = \"\", want a problem")
	}
	if !strings.Contains(got, "empty") {
		t.Errorf("aliProvenanceProblem = %q, want it to say empty", got)
	}
}

func TestAliKey_TheProvenanceRecordSitsBesideTheRealKey(t *testing.T) {
	path := aliRealKeyPath(t)
	if path == "" {
		return
	}
	if got := aliProvenanceProblem(path); got != "" {
		t.Errorf("aliProvenanceProblem = %q, want none", got)
	}
}
