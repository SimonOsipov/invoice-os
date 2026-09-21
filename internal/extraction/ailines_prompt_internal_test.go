// ailines_prompt_internal_test.go: mirrors run.py's purpose dimension -- the line-item and
// combined prompts/schemas, the purpose-aware resume key, and the unchanged security guards.
package extraction

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const (
	aliSystemSHA256    = "ae9f106b6eb44f50b9704997a30dc6fed9fa361cf64ec3914a85efa92bc684e0"
	aliTextIntroSHA256 = "17042bb71a50e60e70b4cedbc59e33481e32b29a64602c2c1c8c4b3af1e81240"
)

func aliRunPy(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "tools", "aimodeltest", "run.py"))
	if err != nil {
		t.Fatalf("read run.py: %v", err)
	}
	return string(b)
}

// aliPyLiteral extracts a triple-quoted Python literal by name, Fatal on any miss so nothing
// downstream scores an empty string as a match.
func aliPyLiteral(t *testing.T, src, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)\n` + regexp.QuoteMeta(name) + ` = """(.*?)"""`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("%s literal not found in run.py", name)
	}
	if m[1] == "" {
		t.Fatalf("%s literal is empty in run.py", name)
	}
	return m[1]
}

// T1: LINE_ROLES must equal extraction.LineRoles, in order, checked both as a set (a
// brace-blind extractor returning zero keys must Fatal, not pass vacuously) and by DeepEqual.
func TestAliRunPy_LineRolesIsExactlyExtractionLineRoles(t *testing.T) {
	if len(LineRoles) != 5 {
		t.Fatalf("extraction.LineRoles has %d entries, want 5 -- this test's ordered pin assumes 5", len(LineRoles))
	}
	src := aliRunPy(t)

	quoted := make([]string, len(LineRoles))
	for i, r := range LineRoles {
		quoted[i] = `"` + r + `"`
	}
	want := "LINE_ROLES = [" + strings.Join(quoted, ", ") + "]"
	if !strings.Contains(src, want) {
		t.Fatalf("run.py does not contain %q, built from extraction.LineRoles", want)
	}

	m := regexp.MustCompile(`(?m)^LINE_ROLES = \[([^\]]*)\]$`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("LINE_ROLES = [...] line not found in run.py")
	}
	names := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1)
	if len(names) == 0 {
		t.Fatal("LINE_ROLES parsed zero names -- a brace-blind extractor must not pass vacuously")
	}
	if len(names) != 5 {
		t.Fatalf("LINE_ROLES parsed %d names, want 5", len(names))
	}
	var parsed []string
	for _, n := range names {
		parsed = append(parsed, n[1])
	}

	pySet := map[string]bool{}
	for _, n := range parsed {
		pySet[n] = true
	}
	goSet := map[string]bool{}
	for _, r := range LineRoles {
		goSet[r] = true
	}
	for n := range pySet {
		if !goSet[n] {
			t.Errorf("run.py LINE_ROLES has %q, not in extraction.LineRoles", n)
		}
	}
	for r := range goSet {
		if !pySet[r] {
			t.Errorf("extraction.LineRoles has %q, missing from run.py LINE_ROLES", r)
		}
	}

	if !reflect.DeepEqual(parsed, LineRoles) {
		t.Errorf("LINE_ROLES order = %v, want extraction.LineRoles order %v", parsed, LineRoles)
	}
}

// T2: the three schemas, byte-pinned; the empty row list stays legal by
// construction (no minItems anywhere, no post-construction schema mutation).
func TestAliRunPy_TheLineItemSchemaAcceptsAnEmptyRowList(t *testing.T) {
	src := aliRunPy(t)

	lines := []string{
		`LINE_ITEM_SCHEMA = {"type": "object", "additionalProperties": False, "required": LINE_ROLES, "properties": {r: {"type": ["string", "null"]} for r in LINE_ROLES}}`,
		`LINES_SCHEMA = {"type": "object", "additionalProperties": False, "required": ["line_items"], "properties": {"line_items": {"type": "array", "items": LINE_ITEM_SCHEMA}}}`,
		`COMBINED_SCHEMA = {"type": "object", "additionalProperties": False, "required": FIELDS + ["line_items"], "properties": dict(SCHEMA["properties"], line_items={"type": "array", "items": LINE_ITEM_SCHEMA})}`,
	}
	for _, l := range lines {
		if !strings.Contains(src, l) {
			t.Errorf("run.py is missing the schema line %q", l)
		}
	}

	if strings.Contains(src, "minItems") {
		t.Error(`run.py contains "minItems" -- the empty row list must stay legal by construction`)
	}
	if m := regexp.MustCompile(`(?m)^\s*\w*SCHEMA\[[^\]]*\] =`).FindAllString(src, -1); len(m) != 0 {
		t.Errorf("run.py mutates a schema dict after construction: %v", m)
	}
}

// T3: the three json_schema names each occur exactly once, and the call shapes / the
// response_format line that reads them are byte-pinned.
func TestAliRunPy_TheSchemaNamesAreTheOnesTheReportQuotes(t *testing.T) {
	src := aliRunPy(t)

	for _, name := range []string{"invoice_fields", "invoice_line_items", "invoice_fields_and_lines"} {
		if c := strings.Count(src, `"`+name+`"`); c != 1 {
			t.Errorf("%q occurs %d times in run.py, want exactly 1", name, c)
		}
	}

	callShapeLines := []string{
		`CALL_SHAPES = {"header": (SYSTEM, "invoice_fields", SCHEMA),`,
		`"lines": (LINE_SYSTEM, "invoice_line_items", LINES_SCHEMA),`,
		`"combined": (COMBINED_SYSTEM, "invoice_fields_and_lines", COMBINED_SCHEMA)}`,
	}
	for _, l := range callShapeLines {
		if !strings.Contains(src, l) {
			t.Errorf("run.py is missing CALL_SHAPES line %q", l)
		}
	}

	respFmt := `"response_format": {"type": "json_schema", "json_schema": {"name": schema_name, "strict": True, "schema": schema}},`
	if !strings.Contains(src, respFmt) {
		t.Errorf("run.py is missing the response_format line reading schema_name and schema: %q", respFmt)
	}
}

// T4: PURPOSES is exactly [header, combined, lines], each with a CALL_SHAPES entry and a
// PURPOSE_OUT entry, and an unknown purpose (including "") exits non-zero naming it.
func TestAliRunPy_EveryPurposeHasACallShapeAndAnOutputFile(t *testing.T) {
	src := aliRunPy(t)

	m := regexp.MustCompile(`(?m)^PURPOSES = \[([^\]]*)\]$`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("PURPOSES = [...] line not found in run.py")
	}
	names := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1)
	if len(names) == 0 {
		t.Fatal("PURPOSES parsed zero names")
	}
	if len(names) != 3 {
		t.Fatalf("PURPOSES parsed %d names, want 3", len(names))
	}
	var parsed []string
	for _, n := range names {
		parsed = append(parsed, n[1])
	}
	want := []string{"header", "combined", "lines"}
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("PURPOSES = %v, want %v in order", parsed, want)
	}

	callShapeEntry := map[string]string{
		"header":   `"header": (SYSTEM, "invoice_fields", SCHEMA)`,
		"combined": `"combined": (COMBINED_SYSTEM, "invoice_fields_and_lines", COMBINED_SCHEMA)`,
		"lines":    `"lines": (LINE_SYSTEM, "invoice_line_items", LINES_SCHEMA)`,
	}
	purposeOutEntry := map[string]string{
		"header":   `"header": OUT`,
		"lines":    `"lines": LINE_OUT`,
		"combined": `"combined": LINE_OUT`,
	}
	for _, p := range parsed {
		if !strings.Contains(src, callShapeEntry[p]) {
			t.Errorf("CALL_SHAPES has no entry for purpose %q", p)
		}
		if !strings.Contains(src, purposeOutEntry[p]) {
			t.Errorf("PURPOSE_OUT has no entry for purpose %q", p)
		}
	}

	if !strings.Contains(src, `purposes = os.environ.get("ONLY_PURPOSES", ",".join(PURPOSES)).split(",")`) {
		t.Error("run.py is missing the ONLY_PURPOSES env line")
	}
	if !strings.Contains(src, `sys.exit(f"unknown purpose(s): {','.join(bad)}")`) {
		t.Error("run.py is missing the unknown-purpose sys.exit line")
	}
}

// T5: the resume key carries (file, model, purpose, run) in both the write and the skip
// test, and neither pre-purpose 3-part form survives anywhere in the file.
func TestAliRunPy_TheResumeKeyCarriesThePurpose(t *testing.T) {
	src := aliRunPy(t)

	require := []string{
		`keys.add((r["file"], r["model"], r.get("purpose", "header"), r["run"]))`,
		`if (name, model, purpose, run) not in keys:`,
	}
	for _, l := range require {
		if !strings.Contains(src, l) {
			t.Errorf("run.py is missing %q", l)
		}
	}

	forbid := []string{
		`(r["file"], r["model"], r["run"])`,
		`(name, model, run)`,
	}
	for _, l := range forbid {
		if strings.Contains(src, l) {
			t.Errorf("run.py still contains the pre-purpose resume key form %q", l)
		}
	}
}

// T6: line_items lands under a top-level "lines" key, never inside "fields" -- a nested
// array there would fail aitLoadAnswers' map[string]string unmarshal (see T7).
func TestAliRunPy_LineItemsAreATopLevelKeyNeverInsideFields(t *testing.T) {
	src := aliRunPy(t)

	require := []string{
		`rec["lines"] = [{r: row.get(r) for r in LINE_ROLES} for row in rows]`,
		`rec["fields"] = {f: parsed.get(f) for f in FIELDS}`,
		`"purpose": purpose`,
	}
	for _, l := range require {
		if !strings.Contains(src, l) {
			t.Errorf("run.py is missing %q", l)
		}
	}

	if strings.Contains(src, `rec["fields"][`) {
		t.Error(`run.py writes into rec["fields"][...] -- line_items must never nest inside fields`)
	}
}

// T7: the one leg that runs real code. aitAnswerRecordJSON.Fields is
// map[string]string, so a record whose fields carries a nested line_items array hard-fails the
// header loader naming the line; a flat-fields control on the same loader passes clean.
func TestAliAnswers_ANestedLineItemsArrayHardFailsTheHeaderLoader(t *testing.T) {
	dir := t.TempDir()

	subject := filepath.Join(dir, "subject.jsonl")
	subjectLine := `{"file":"doc1.pdf","run":1,"model":"m","fields":{"invoice_number":"1","line_items":[{"description":"x"}]}}`
	if err := os.WriteFile(subject, []byte(subjectLine+"\n"), 0o644); err != nil {
		t.Fatalf("write subject: %v", err)
	}
	if _, _, err := aitLoadAnswers(subject); err == nil {
		t.Fatal("aitLoadAnswers accepted a record whose fields carries a nested line_items array")
	} else if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error = %q, want it to name line 1", err.Error())
	}

	control := filepath.Join(dir, "control.jsonl")
	controlLine := `{"file":"doc1.pdf","run":1,"model":"m","fields":{"invoice_number":"1"}}`
	if err := os.WriteFile(control, []byte(controlLine+"\n"), 0o644); err != nil {
		t.Fatalf("write control: %v", err)
	}
	good, errCount, err := aitLoadAnswers(control)
	if err != nil {
		t.Fatalf("control: aitLoadAnswers returned an error: %v", err)
	}
	if len(good) != 1 || errCount != 0 {
		t.Errorf("control: got %d good records and %d errors, want 1 and 0", len(good), errCount)
	}
}

// T8: SYSTEM and TEXT_INTRO pinned by hash, not only by the existing aiSystem/
// aiTextIntro comparison -- that comparison has no second side to catch a two-sided edit.
func TestAliRunPy_TheHeaderPromptIsByteUnchanged(t *testing.T) {
	src := aliRunPy(t)

	system := aliPyLiteral(t, src, "SYSTEM")
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(system))); got != aliSystemSHA256 {
		t.Errorf("sha256(SYSTEM) = %s, want %s -- SYSTEM changed", got, aliSystemSHA256)
	}

	textIntro := aliPyLiteral(t, src, "TEXT_INTRO")
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(textIntro))); got != aliTextIntroSHA256 {
		t.Errorf("sha256(TEXT_INTRO) = %s, want %s -- TEXT_INTRO changed", got, aliTextIntroSHA256)
	}
}

// T9: the line-item prompt carries its eight load-bearing rules, and both arms compose
// LINE_SYSTEM/COMBINED_SYSTEM from the same LINE_RULES literal byte for byte.
func TestAliRunPy_TheLinePromptCarriesItsLoadBearingRules(t *testing.T) {
	src := aliRunPy(t)
	lineRules := aliPyLiteral(t, src, "LINE_RULES")

	needles := []string{
		"in printed order, top to bottom",
		"One object per printed row.",
		"never invent a row that is not printed",
		"A totals band is never a line item.",
		"join the lines with one space",
		"A tax rate (7.5%) is not a tax amount",
		"or null when the row does not print that cell",
		"Return an empty list when the document prints no line-item rows at all.",
	}
	if len(needles) != 8 {
		t.Fatalf("test declares %d needles, want 8 -- AC-4 names eight load-bearing rules", len(needles))
	}
	for _, n := range needles {
		if !strings.Contains(lineRules, n) {
			t.Errorf("LINE_RULES is missing rule %q", n)
		}
	}

	compositions := []string{
		`LINE_SYSTEM = "You extract the line items of one invoice for a Nigerian e-invoicing system.\n\n" + LINE_RULES`,
		`COMBINED_SYSTEM = SYSTEM + "\n\nYou also extract the same invoice's line items.\n\n" + LINE_RULES`,
	}
	for _, l := range compositions {
		if !strings.Contains(src, l) {
			t.Errorf("run.py is missing %q -- the two arms must share LINE_RULES byte for byte", l)
		}
	}
}

// T10: the DATA and key-file work-tree guards, check-ignore and COST_CAP are unchanged,
// and no print( line -- the leak sweep's floor -- ever names the key or its path.
func TestAliRunPy_TheKeyAndCostGuardsAreIntact(t *testing.T) {
	src := aliRunPy(t)

	require := []string{
		`if subprocess.run(["git", "-C", _nearest_existing_dir(BASE), "rev-parse", "--is-inside-work-tree"],`,
		`if subprocess.run(["git", "-C", _nearest_existing_dir(BASE), "check-ignore", "-q", os.path.abspath(BASE)]).returncode != 0:`,
		`sys.exit(f"DATA {BASE} is inside a git work tree and is not ignored")`,
		`if subprocess.run(["git", "-C", key_dir, "rev-parse", "--is-inside-work-tree"],`,
		`sys.exit(f"OPENROUTER_KEY_FILE {key_path} resolves inside a git work tree")`,
		`COST_CAP = float(os.environ.get("COST_CAP", "4.0"))`,
		`if spent[0] >= COST_CAP:`,
	}
	for _, l := range require {
		if !strings.Contains(src, l) {
			t.Errorf("run.py is missing %q", l)
		}
	}

	var printLines []string
	for _, line := range strings.Split(src, "\n") {
		if strings.Contains(line, "print(") {
			printLines = append(printLines, line)
		}
	}
	if len(printLines) == 0 {
		t.Fatal("run.py has no print( lines -- the leak sweep would be vacuous")
	}
	for _, line := range printLines {
		if strings.Contains(line, "KEY") || strings.Contains(line, "key_path") {
			t.Errorf("a print line names the key: %q", line)
		}
	}

	if c := strings.Count(src, `"Bearer " + KEY`); c != 1 {
		t.Errorf(`"Bearer " + KEY occurs %d times, want exactly 1`, c)
	}
}
