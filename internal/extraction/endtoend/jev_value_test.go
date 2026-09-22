// jev_value_test.go: the value check and the document-type check, measured against a fake Jev
// (CHECK-01-04), plus CHECK-01-03's non-invoice ratchet guard. No database.
package endtoend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/jevmeasure"
)

// --- CHECK-01-03: the ratchet guard ---

// The seven non-invoice fixtures, declared here because internal/extraction's own table is in
// package extraction_test and unreachable. Held against that table by the mirror clause below.
var jvNonInvoicePDFs = []string{
	"noninvoice_receipt.pdf", "noninvoice_proforma.pdf", "noninvoice_quotation.pdf",
	"noninvoice_credit_note.pdf", "noninvoice_delivery_note.pdf", "noninvoice_statement.pdf",
	"noninvoice_purchase_order.pdf",
}

const jvNonInvoiceFile = "../noninvoice_test.go"
const jvNonInvoicePrefix = "noninvoice_"

// TestNonInvoice_NoFixtureEntersACorpusRatchet: AC-8, two-legged because the four ratchets in
// package extraction_test are unreachable as Go values from here. Half A checks this package's
// own ratchets directly; Half B reuses wildRatchets + wildVarBody, already proved by
// TestWildLayouts_DoNotEnterTheCorpusRatchets, for the other four.
func TestNonInvoice_NoFixtureEntersACorpusRatchet(t *testing.T) {
	if len(jvNonInvoicePDFs) != 7 {
		t.Fatalf("jvNonInvoicePDFs names %d PDF(s), want 7", len(jvNonInvoicePDFs))
	}
	if len(wildRatchets) < 4 {
		t.Fatalf("wildRatchets names %d collection(s), want at least 4", len(wildRatchets))
	}
	if len(expectByLayout) < wildRequireListPin {
		t.Fatalf("expectByLayout names %d row(s), want at least %d", len(expectByLayout), wildRequireListPin)
	}
	if len(requiredPDFs) < wildRequireListPin {
		t.Fatalf("requiredPDFs names %d entr(ies), want at least %d", len(requiredPDFs), wildRequireListPin)
	}
	// Control: the Go-value half below is reading a table that must still hold its known member.
	if !slices.Contains(requiredPDFs, "corpus_inline_labels.pdf") {
		t.Fatalf("requiredPDFs no longer names corpus_inline_labels.pdf; the Go-value half is reading a table that has been emptied")
	}

	for _, name := range jvNonInvoicePDFs {
		t.Run(name, func(t *testing.T) {
			if slices.Contains(wildLayouts, name) {
				t.Errorf("%s is in wildLayouts", name)
			}
			if slices.Contains(wildTextLayouts, name) {
				t.Errorf("%s is in wildTextLayouts", name)
			}
			if slices.Contains(requiredPDFs, name) {
				t.Errorf("%s is in requiredPDFs", name)
			}
			if _, ok := wildTokenFloor[name]; ok {
				t.Errorf("%s has a wildTokenFloor row", name)
			}
			if _, ok := eeLinesExpected[name]; ok {
				t.Errorf("%s has an eeLinesExpected row", name)
			}
			for _, row := range expectByLayout {
				if row.file == name {
					t.Errorf("%s has an expectByLayout row", name)
				}
			}

			golden := strings.TrimSuffix(name, ".pdf") + ".docling.json"
			if slices.Contains(requiredGoldens, golden) {
				t.Errorf("%s is in requiredGoldens", golden)
			}
		})
	}

	for _, r := range wildRatchets {
		body := wildVarBody(t, wildReadFile(t, r.file), r.decl, r.file)
		if n := strings.Count(body, r.needle); n < r.min {
			t.Fatalf("%s's %q names %d %q entr(ies), want at least %d; the scan is not reading the collection", r.file, r.decl, n, r.needle, r.min)
		}
		if strings.Contains(body, jvNonInvoicePrefix) {
			t.Errorf("%s's %q names a %s fixture; that moves the Tier-1 denominators the same way a wild_ layout would", r.file, r.decl, jvNonInvoicePrefix)
		}
	}

	// Mirror clause: a name added to internal/extraction's table and forgotten here.
	src := wildReadFile(t, jvNonInvoiceFile)
	for _, name := range jvNonInvoicePDFs {
		if !strings.Contains(src, name) {
			t.Errorf("%s does not mention %s; the two lists have drifted apart", jvNonInvoiceFile, name)
		}
	}
	if n := strings.Count(src, jvNonInvoicePrefix); n < 7 {
		t.Errorf("%s carries %d occurrence(s) of %q, want at least 7", jvNonInvoiceFile, n, jvNonInvoicePrefix)
	}
}

// --- the walk ---

// jvDoc is one document the walk measures: its file, true type, and (for the fourteen scored
// layouts) its answer key. expect is nil for a non-invoice (R-1). decided is every
// HeaderFields member's decided value, filled by the walk after Reconcile runs -- jvPlant's
// swapped_tins variant needs the document's own supplier_tin, which is not a written field.
type jvDoc struct {
	pdf      string
	trueType string
	expect   map[string][]string
	decided  map[string]string
}

// jvTrueType is hard-coded, never derived: a derived table cannot see a missing type.
var jvTrueType = map[string]string{
	"noninvoice_receipt.pdf":        "receipt",
	"noninvoice_proforma.pdf":       "proforma",
	"noninvoice_quotation.pdf":      "quotation",
	"noninvoice_credit_note.pdf":    "credit note",
	"noninvoice_delivery_note.pdf":  "delivery note",
	"noninvoice_statement.pdf":      "statement",
	"noninvoice_purchase_order.pdf": "purchase order",
}

// jvDocuments derives the 21-document walk list: the fourteen expectByLayout rows in their own
// order, then the seven jvNonInvoicePDFs in their declared order.
func jvDocuments() []jvDoc {
	docs := make([]jvDoc, 0, len(expectByLayout)+len(jvNonInvoicePDFs))
	for _, row := range expectByLayout {
		docs = append(docs, jvDoc{pdf: row.file, trueType: "tax invoice", expect: row.fields})
	}
	for _, pdf := range jvNonInvoicePDFs {
		docs = append(docs, jvDoc{pdf: pdf, trueType: jvTrueType[pdf], expect: nil})
	}
	return docs
}

// jvRowsCache memoizes jvRows by pdf: jvPlants' swapped_tins lookup needs the whole Reconcile
// output (supplier_tin, not a written field) without a second read of the same golden.
var jvRowsCache = map[string][]extraction.FieldResult{}

// jvRows is the harness's own decided rows for one document: Resolve -> LineItems -> Reconcile
// over its committed Docling golden, with Learned nil and Entity zero.
func jvRows(t *testing.T, pdf string) []extraction.FieldResult {
	t.Helper()
	if rows, ok := jvRowsCache[pdf]; ok {
		return rows
	}
	pages := jvTokenPages(t, pdf)
	lines := extraction.LineItems(jvPages(t, pdf))
	cands := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
	rows := extraction.Reconcile(extraction.Input{Candidates: cands, Lines: lines, Entity: extraction.Entity{}, Pages: pages})
	jvRowsCache[pdf] = rows
	return rows
}

// jvDecided reads every extraction.HeaderFields member's decided value off rows -- jvPlants'
// swapped_tins lookup needs supplier_tin, which is not a written field.
func jvDecided(rows []extraction.FieldResult) map[string]string {
	decided := map[string]string{}
	for _, f := range extraction.HeaderFields {
		if fr, ok := jvFieldResult(rows, f); ok && fr.Reason == extraction.ReasonNone && fr.Value != nil {
			decided[f] = *fr.Value
		}
	}
	return decided
}

// jvFieldResult returns rows' entry for field, if any.
func jvFieldResult(rows []extraction.FieldResult, field string) (extraction.FieldResult, bool) {
	for _, r := range rows {
		if r.Name == field {
			return r, true
		}
	}
	return extraction.FieldResult{}, false
}

// jvCell is one written field's outcome for one document: asked (labelled against the answer
// key) or not-asked (with its reason -- an extraction.Reason string, "no value", or "no answer
// key").
type jvCell struct {
	Field  string
	Value  string
	Label  string // "right" | "wrong" | "not-asked"
	Reason string // set only when Label == "not-asked"
}

// jvCellFor returns cells' entry for field, if any.
func jvCellFor(cells []jvCell, field string) (jvCell, bool) {
	for _, c := range cells {
		if c.Field == field {
			return c, true
		}
	}
	return jvCell{}, false
}

// jvCells derives the eight jvCell rows for one document from Reconcile's output, against
// doc.expect (nil for a non-invoice). A cell is not-asked either because the engine doubts it
// (Reason/no value) or, on a non-invoice, because it has no answer key at all (R-1).
func jvCells(t *testing.T, rows []extraction.FieldResult, doc jvDoc) []jvCell {
	t.Helper()
	cells := make([]jvCell, 0, len(writtenFields))
	for _, field := range writtenFields {
		fr, _ := jvFieldResult(rows, field)
		if fr.Reason != extraction.ReasonNone || fr.Value == nil {
			reason := string(fr.Reason)
			if reason == "" {
				reason = "no value"
			}
			cells = append(cells, jvCell{Field: field, Label: "not-asked", Reason: reason})
			continue
		}

		value := *fr.Value
		if doc.expect == nil {
			cells = append(cells, jvCell{Field: field, Value: value, Label: "not-asked", Reason: "no answer key"})
			continue
		}
		cells = append(cells, jvCell{Field: field, Value: value, Label: jvLabel(value, doc.expect[field])})
	}
	return cells
}

// jvTokenPages is the committed Docling golden as positioned tokens -- the shape Resolve and
// DoclingPromptText both read. eeTokenPages is PDFium and is the wrong reader here (R-2). Reads
// via jvOpen (not eeGoldenReader's own eeFixtureBytes) so the read is recorded for AC-2's scope
// guard.
func jvTokenPages(t *testing.T, doc string) []extraction.TokenPage {
	t.Helper()
	golden := wildGolden(doc)
	eeRequireFixtures(t, []string{golden})
	r := eeReplayReader(t, jvOpen(t, golden))

	var pages []extraction.TokenPage
	if _, err := r.Read(t.Context(), extraction.Document{ContentType: eeContentType}, extraction.CollectTokens(&pages)); err != nil {
		t.Fatalf("replay %s: %v", golden, err)
	}
	if len(pages) == 0 {
		t.Fatalf("%s replayed 0 page(s); every question asked off it would be asked about nothing", doc)
	}
	return pages
}

// jvPages is jvTokenPages' Page-shaped twin, for LineItems.
func jvPages(t *testing.T, doc string) []extraction.Page { return eeGoldenPages(t, wildGolden(doc)) }

// jvPaths records every path jvOpen/jvWrite touched, cleaned -- the runtime leg of AC-2's scope
// guard. Reset per test with jvResetPaths.
var jvPaths []string

func jvResetPaths() { jvPaths = nil }

// jvOpenPath is this file's one filesystem read call site; jvOpen and the merge ledger's reader
// both route through it. notFoundOK tolerates a first run with no ledger written yet.
func jvOpenPath(t *testing.T, path string, notFoundOK bool) ([]byte, bool) {
	t.Helper()
	jvPaths = append(jvPaths, filepath.Clean(path))
	b, err := os.ReadFile(path)
	if err != nil {
		if notFoundOK && os.IsNotExist(err) {
			return nil, false
		}
		t.Fatalf("jvOpenPath %s: %v", path, err)
	}
	return b, true
}

// jvOpen is this suite's read seam for golden/fixture files, under eeFxDir.
func jvOpen(t *testing.T, name string) []byte {
	t.Helper()
	b, _ := jvOpenPath(t, filepath.Join(eeFxDir, name), false)
	return b
}

// jvWritePath is this file's one filesystem write call site: temp-file-plus-rename (design §4.1),
// so a crash leaves the previous artifact rather than a torn one. jvWrite and the merge ledger's
// writer both route through it.
func jvWritePath(t *testing.T, path string, b []byte) {
	t.Helper()
	jvPaths = append(jvPaths, filepath.Clean(path))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Fatalf("jvWritePath %s: %v", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("jvWritePath rename %s: %v", path, err)
	}
}

// jvWrite is this suite's write seam: every artefact write goes through it.
func jvWrite(t *testing.T, out, name string, b []byte) {
	t.Helper()
	if out == "" {
		t.Fatalf("jvWrite %s with an empty out root would write into the package directory", name)
	}
	jvWritePath(t, filepath.Join(out, name), b)
}

// jvLedgerName is the merge ledger D-1 introduces: two independently-run test binaries jointly
// produce one report by reading, merging through jevmeasure.MergeOutcomes, and rewriting this.
const jvLedgerName = "jev-outcomes.json"

// jvReadLedger reads the prior ledger, tolerating a first run where nothing has been written yet.
func jvReadLedger(t *testing.T, out string) []jevmeasure.Outcome {
	t.Helper()
	b, ok := jvOpenPath(t, filepath.Join(out, jvLedgerName), true)
	if !ok {
		return nil
	}
	var prior []jevmeasure.Outcome
	if err := json.Unmarshal(b, &prior); err != nil {
		t.Fatalf("jvReadLedger: unmarshal %s: %v", jvLedgerName, err)
	}
	return prior
}

// jvWriteLedger marshals outcomes and writes them to the ledger via jvWritePath.
func jvWriteLedger(t *testing.T, out string, outcomes []jevmeasure.Outcome) {
	t.Helper()
	b, err := json.Marshal(outcomes)
	if err != nil {
		t.Fatalf("jvWriteLedger: marshal: %v", err)
	}
	jvWritePath(t, filepath.Join(out, jvLedgerName), b)
}

// jvPathAllowed reports whether path, once cleaned, sits under eeFxDir (a read root) or under
// out (a write root).
func jvPathAllowed(path, out string) bool {
	clean := filepath.Clean(path)
	if rel, err := filepath.Rel(eeFxDir, clean); err == nil && !strings.HasPrefix(rel, "..") {
		return true
	}
	if out != "" {
		if rel, err := filepath.Rel(out, clean); err == nil && !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
}

// jvCallTimeout bounds every Ask(): Client sets no http.Client.Timeout, so ctx is the walk's
// only clock (design 1.1).
const jvCallTimeout = 30 * time.Second

// jvModel is the vendor model id CHECK-02 still owes (R-14); JEV_MODEL overrides it at the live
// run so an operator can see and swap what gets sent.
const jvModel = "systemone-default"

// jvReader: this suite always runs Docling (design §1, "Import wizard deployed vs sysmap").
const jvReader = "docling"

func jvResolveModel() string {
	if m := os.Getenv("JEV_MODEL"); m != "" {
		return m
	}
	return jvModel
}

// jvValueQuestion composes one noul question: ValueCheckInstructions has no placeholder for the
// field or the value (R-11), so the constant is sent as a byte-exact prefix, followed by both.
func jvValueQuestion(field, value string) jevmeasure.Question {
	return jevmeasure.Question{
		Type:         jevmeasure.QuestionTypeNoul,
		Instructions: jevmeasure.ValueCheckInstructions + fmt.Sprintf(" Field: %s. Value: %s.", field, value),
		Criteria: map[string]string{
			"true":  jevmeasure.ValueCheckCriteriaTrue,
			"false": jevmeasure.ValueCheckCriteriaFalse,
		},
	}
}

// jvWalk runs the measurement pass against baseURL for the given documents (all 21 when only
// is empty): every document's production and variant Ask() calls, and the outcomes they
// produce.
func jvWalk(t *testing.T, baseURL string, only ...string) []jevmeasure.Outcome {
	t.Helper()

	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		key = "jv-test-key"
	}
	client := jevmeasure.NewClient(baseURL, jvResolveModel(), key)

	docs := jvDocuments()
	if len(only) > 0 {
		filtered := docs[:0:0]
		for _, doc := range docs {
			if slices.Contains(only, doc.pdf) {
				filtered = append(filtered, doc)
			}
		}
		docs = filtered
	}

	var outcomes []jevmeasure.Outcome
	for _, doc := range docs {
		rows := jvRows(t, doc.pdf)
		cells := jvCells(t, rows, doc)
		pages := jvTokenPages(t, doc.pdf)
		state := extraction.DoclingPromptText(pages)

		questions := map[string]jevmeasure.Question{}
		for _, cell := range cells {
			if cell.Label != "not-asked" {
				questions[cell.Field] = jvValueQuestion(cell.Field, cell.Value)
			}
		}
		questions["document_type"] = jevmeasure.Question{
			Type:         jevmeasure.QuestionTypeChoice,
			Instructions: jevmeasure.DocumentTypeInstructions,
			Criteria:     jevmeasure.DocumentTypeCriteria,
		}

		ctx, cancel := context.WithTimeout(t.Context(), jvCallTimeout)
		resp, elapsed, err := client.Ask(ctx, state, questions)
		cancel()
		callID := doc.pdf + "#production"

		for _, cell := range cells {
			switch {
			case cell.Label == "not-asked":
				outcomes = append(outcomes, jevmeasure.Outcome{
					Check: "value_check", DocumentID: doc.pdf, Field: cell.Field,
					Label: "not-asked", Reason: cell.Reason, ProbabilityKind: jevmeasure.KindNoul,
					Reader: jvReader,
				})
			case err != nil:
				outcomes = append(outcomes, jevmeasure.Outcome{
					Check: "value_check", DocumentID: doc.pdf, Field: cell.Field,
					Label: "not-asked", Failed: true, Reason: err.Error(),
					ProbabilityKind: jevmeasure.KindNoul, Elapsed: elapsed, CallID: callID,
					Reader: jvReader,
				})
			default:
				ans := resp.Answers[cell.Field]
				outcomes = append(outcomes, jevmeasure.Outcome{
					Check: "value_check", DocumentID: doc.pdf, Field: cell.Field,
					Label: cell.Label, ProbabilityKind: jevmeasure.KindNoul, Probability: ans.Noul,
					Elapsed: elapsed, Usage: resp.Usage, CallID: callID, Reader: jvReader,
				})
			}
		}

		if err != nil {
			outcomes = append(outcomes, jevmeasure.Outcome{
				Check: "document_type_check", DocumentID: doc.pdf, Field: doc.trueType,
				Label: "not-asked", Failed: true, Reason: err.Error(),
				ProbabilityKind: jevmeasure.KindChoiceConfidence, Elapsed: elapsed, CallID: callID,
				Reader: jvReader,
			})
		} else {
			ans := resp.Answers["document_type"]
			label := "wrong"
			if ans.Choice == doc.trueType {
				label = "right"
			}
			outcomes = append(outcomes, jevmeasure.Outcome{
				Check: "document_type_check", DocumentID: doc.pdf, Field: doc.trueType,
				Answer: ans.Choice, Label: label, ProbabilityKind: jevmeasure.KindChoiceConfidence,
				Probability: ans.Confidence, Elapsed: elapsed, Usage: resp.Usage, CallID: callID,
				Reader: jvReader,
			})
		}

		if doc.expect == nil {
			continue // R-1: a non-invoice has no answer key, so no field on it is ever "decided correctly" to corrupt.
		}
		doc.decided = jvDecided(rows)
		plants := jvPlants(cells, doc)

		variantQuestions := map[string]jevmeasure.Question{}
		for _, p := range plants {
			if p.Skip == "" {
				variantQuestions["variant_"+p.Variant+"_"+p.Field] = jvValueQuestion(p.Field, p.Value)
			}
		}

		var vresp jevmeasure.Response
		var velapsed time.Duration
		var verr error
		if len(variantQuestions) > 0 {
			vctx, vcancel := context.WithTimeout(t.Context(), jvCallTimeout)
			vresp, velapsed, verr = client.Ask(vctx, state, variantQuestions)
			vcancel()
		}
		vCallID := doc.pdf + "#variants"

		for _, p := range plants {
			qid := "variant_" + p.Variant + "_" + p.Field
			switch {
			case p.Skip != "":
				outcomes = append(outcomes, jevmeasure.Outcome{
					Check: "value_check", DocumentID: doc.pdf, Field: qid,
					Label: "not-asked", Reason: "variant not plantable", ProbabilityKind: jevmeasure.KindNoul,
					Reader: jvReader,
				})
			case verr != nil:
				outcomes = append(outcomes, jevmeasure.Outcome{
					Check: "value_check", DocumentID: doc.pdf, Field: qid,
					Label: "not-asked", Failed: true, Reason: verr.Error(), Variant: true,
					ProbabilityKind: jevmeasure.KindNoul, Elapsed: velapsed, CallID: vCallID,
					Reader: jvReader,
				})
			default:
				ans := vresp.Answers[qid]
				outcomes = append(outcomes, jevmeasure.Outcome{
					Check: "value_check", DocumentID: doc.pdf, Field: qid,
					Label: "wrong", Variant: true, ProbabilityKind: jevmeasure.KindNoul,
					Probability: ans.Noul, Elapsed: velapsed, Usage: vresp.Usage, CallID: vCallID,
					Reader: jvReader,
				})
			}
		}
	}
	return outcomes
}

// jvProductionRequest returns the first recorded request -- the walk sends a document's
// production call before its variant call.
func jvProductionRequest(t *testing.T, recorded []jvRequest) jvRequest {
	t.Helper()
	if len(recorded) == 0 {
		t.Fatalf("the fake recorded 0 request(s); wanted at least the production call")
	}
	return recorded[0]
}

// jvCountByCheck tallies outcomes by Check name.
func jvCountByCheck(outcomes []jevmeasure.Outcome) (value, docType int) {
	for _, o := range outcomes {
		switch o.Check {
		case "value_check":
			value++
		case "document_type_check":
			docType++
		}
	}
	return value, docType
}

// jvGateLogs records every message jvGateLog sent to t.Log -- the seam
// TestJevValue_UnsetKeyLogsAndReturns inspects to prove the gate logs exactly one line naming
// the missing var(s). Reset per subtest with jvResetGateLogs.
var jvGateLogs []string

func jvResetGateLogs() { jvGateLogs = nil }

// jvGateLog is jvGatedRun's only logging seam when it declines to run: it both records msg (for
// the test to inspect) and logs it, so a mutation silencing the log also empties the record.
func jvGateLog(t *testing.T, msg string) {
	t.Helper()
	jvGateLogs = append(jvGateLogs, msg)
	t.Log(msg)
}

// jvGateReason names which env var(s) jvGatedRun found missing, so the logged line lets an
// operator tell key-missing, out-missing and both-missing apart.
func jvGateReason(key, out string) string {
	switch {
	case key == "" && out == "":
		return "TYPESAFE_API_KEY and JEV_OUT unset: no client built, no call"
	case key == "":
		return "TYPESAFE_API_KEY unset: no client built, no call"
	default:
		return "JEV_OUT unset: no client built, no call"
	}
}

// jvGatedRun reads TYPESAFE_API_KEY and JEV_OUT; if either is empty it logs one line naming
// which one and returns false without building a client. Copies TestAIText_WriteCorpusKey's
// shape. When both are set it runs the full walk against baseURL and writes the rendered report
// under JEV_OUT -- the live run this gate exists to control.
func jvGatedRun(t *testing.T, baseURL string) bool {
	t.Helper()
	key := os.Getenv("TYPESAFE_API_KEY")
	out := os.Getenv("JEV_OUT")
	if key == "" || out == "" {
		jvGateLog(t, jvGateReason(key, out))
		return false
	}
	t.Logf("live run: model %s", jvResolveModel()) // R-14: named so an operator sees what will be sent

	fresh := jvWalk(t, baseURL)
	prior := jvReadLedger(t, out)
	all := jevmeasure.MergeOutcomes(prior, fresh)
	jvWriteLedger(t, out, all)

	md, reportJSON, err := jevmeasure.Render(all, jevmeasure.Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	jvWrite(t, out, "jev-report.md", md)
	jvWrite(t, out, "jev-report.json", reportJSON)
	return true
}

// Row 11 / AC-8. A literal pin, not a re-measurement: the real oracle is the DB-gated
// TestRLS_EndToEndScoresTheCorpus (score_db_test.go), which does not run in CI's go job. This
// only catches eeCorpusHits or eeCorpusCells moving in this branch's own test list -- weak by
// construction (a self-consistent edit to both still passes TestEndToEnd_TheFloorIsAQuotientOfThePinnedIntegers),
// worth having anyway.
func TestJevGuard_TheCorpusFigureIsUnmoved(t *testing.T) {
	if eeCorpusHits != 89 {
		t.Errorf("eeCorpusHits = %d, want 89", eeCorpusHits)
	}
	if eeCorpusCells != 112 {
		t.Errorf("eeCorpusCells = %d, want 112", eeCorpusCells)
	}
	if want := 89.0 / 112.0; eeCorpusFloor != want {
		t.Errorf("eeCorpusFloor = %v, want %v", eeCorpusFloor, want)
	}
}

// jvLineWithTokens returns the first report line containing every tok.
func jvLineWithTokens(t *testing.T, md string, toks ...string) string {
	t.Helper()
	for _, line := range strings.Split(md, "\n") {
		all := true
		for _, tok := range toks {
			if !strings.Contains(line, tok) {
				all = false
				break
			}
		}
		if all {
			return line
		}
	}
	t.Fatalf("no report line contains all of %v", toks)
	return ""
}

func jvHasNumber(s, n string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(n) + `\b`).MatchString(s)
}

// jvSection returns just the "## <heading>" block.
func jvSection(t *testing.T, md, heading string) string {
	t.Helper()
	head := "## " + heading + "\n"
	i := strings.Index(md, head)
	if i == -1 {
		t.Fatalf("report has no %q section", heading)
	}
	rest := md[i+len(head):]
	if j := strings.Index(rest, "\n## "); j != -1 {
		return rest[:j]
	}
	return rest
}

// --- the planter ---

// jvVariants are the four planted-variant names, in a fixed order (design §3.2).
var jvVariants = []string{"dropped_digit", "swapped_tins", "subtotal_as_total", "month_first_date"}

// jvPlantRow is one variant's plant-or-skip record for one document.
type jvPlantRow struct {
	Variant string
	Field   string
	Value   string // the corrupted value; "" when Skip is non-empty
	Skip    string // non-empty: why this site is unplantable
}

// jvVariantField is the written field each of jvVariants targets.
var jvVariantField = map[string]string{
	"dropped_digit":     "buyer_tin",
	"swapped_tins":      "buyer_tin",
	"subtotal_as_total": "total",
	"month_first_date":  "issue_date",
}

// jvPlants builds one jvPlantRow per member of jvVariants for one document, calling the pure
// jvPlant for each. A site is planted only when its base field is decided and already labelled
// right (A29); otherwise it is skipped as "<field> not decided". swapped_tins needs
// supplier_tin, which cells (the 8 written fields) does not carry: doc.decided supplies it when
// the caller has filled it in, jvRowsCache otherwise (every caller runs jvRows(t, doc.pdf)
// first, so the cache always holds this pdf's rows by the time jvPlants runs).
func jvPlants(cells []jvCell, doc jvDoc) []jvPlantRow {
	if doc.decided == nil {
		doc.decided = jvDecided(jvRowsCache[doc.pdf])
	}

	rows := make([]jvPlantRow, 0, len(jvVariants))
	for _, variant := range jvVariants {
		field := jvVariantField[variant]
		cell, ok := jvCellFor(cells, field)
		if !ok || cell.Label != "right" {
			rows = append(rows, jvPlantRow{Variant: variant, Field: field, Skip: field + " not decided"})
			continue
		}
		value, skip := jvPlant(variant, cell, doc)
		rows = append(rows, jvPlantRow{Variant: variant, Field: field, Value: value, Skip: skip})
	}
	return rows
}

// jvLabel is AC-5's pure labeller: an empty expectation can never be a hit.
func jvLabel(value string, expect []string) string {
	if len(expect) == 0 {
		return "wrong"
	}
	if slices.Contains(expect, value) {
		return "right"
	}
	return "wrong"
}

// jvTINShape is the NNNNNNNN-NNNN shape dropped_digit corrupts.
var jvTINShape = regexp.MustCompile(`^\d{8}-\d{4}$`)

// jvPlant is one variant's pure planter: a non-empty skip means unplantable. Pure so the two
// corpus-unreachable Test Specs (R-7, R-8) can exercise it directly.
func jvPlant(variant string, cell jvCell, doc jvDoc) (value string, skip string) {
	switch variant {
	case "dropped_digit":
		if !jvTINShape.MatchString(cell.Value) {
			return "", "value is not shaped NNNNNNNN-NNNN"
		}
		return cell.Value[:7] + cell.Value[8:], ""

	case "swapped_tins":
		supplier, ok := doc.decided["supplier_tin"]
		if !ok || supplier == "" {
			return "", "supplier_tin not decided"
		}
		if supplier == cell.Value {
			return "", "the two TINs are already equal"
		}
		return supplier, ""

	case "subtotal_as_total":
		subtotal, ok := doc.decided["subtotal"]
		if !ok || subtotal == "" {
			return "", "subtotal not decided"
		}
		if subtotal == cell.Value {
			return "", "subtotal equals total"
		}
		return subtotal, ""

	case "month_first_date":
		return jvPlantDate(cell.Value)

	default:
		return "", "unknown variant " + variant
	}
}

// jvPlantDate is month_first_date's date arithmetic: swap month and day in a YYYY-MM-DD value.
func jvPlantDate(date string) (value string, skip string) {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", "the source date does not parse"
	}
	year, month, day := t.Date()
	if int(month) == day {
		return "", "swap is a no-op"
	}
	if day < 1 || day > 12 {
		return "", "the swapped month is not a valid month"
	}
	swapped := time.Date(year, time.Month(day), int(month), 0, 0, 0, 0, time.UTC)
	return swapped.Format("2006-01-02"), ""
}

// --- the fake ---

// jvRequest is one decoded call jvFake received.
type jvRequest struct {
	State     string
	Questions map[string]any
}

// jvFake starts an httptest server decoding every request into map[string]any -- never a typed
// wireRequest mirror (R-16: a typed mirror breaks silently on a field rename in jevmeasure, the
// steerMarker.test.ts trap already recorded on this repo). Every decoded request is recorded,
// in arrival order. t.Errorf, not t.Fatalf: FailNow is unsafe off the test's own goroutine, and
// the handler runs on the server's.
func jvFake(t *testing.T, answer func(state string, questions map[string]any) (body string, status int)) (*httptest.Server, *[]jvRequest) {
	t.Helper()
	recorded := &[]jvRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("jvFake: decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		state, _ := raw["state"].(string)
		questions, _ := raw["questions"].(map[string]any)
		if state == "" {
			t.Errorf("jvFake: request carries an empty state")
		}
		if len(questions) == 0 {
			t.Errorf("jvFake: request decoded to %d question(s), want at least 1", len(questions))
		}
		*recorded = append(*recorded, jvRequest{State: state, Questions: questions})
		body, status := answer(state, questions)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, recorded
}

// jvNoulBody answers every question in questions with a noul of val.
func jvNoulBody(questions map[string]any, val string) string {
	answers := make(map[string]any, len(questions))
	for id := range questions {
		answers[id] = map[string]any{"type": jevmeasure.QuestionTypeNoul, "noul": json.Number(val)}
	}
	b, _ := json.Marshal(map[string]any{"answers": answers, "usage": map[string]any{}})
	return string(b)
}

// jvChoiceBody answers every question with choice, carrying confidence when non-empty.
func jvChoiceBody(questions map[string]any, choice, confidence string) string {
	answers := make(map[string]any, len(questions))
	for id := range questions {
		a := map[string]any{"type": jevmeasure.QuestionTypeChoice, "choice": choice}
		if confidence != "" {
			a["confidence"] = json.Number(confidence)
		}
		answers[id] = a
	}
	b, _ := json.Marshal(map[string]any{"answers": answers, "usage": map[string]any{}})
	return string(b)
}

// jvTypeNeedles maps a literal string unique to one document's state text to the type that
// document really is. Deliberately NOT jvTrueType: a fake that derived its answer the way the
// walk derives the true type would agree with the walk by construction and prove nothing. Every
// other document reads as a tax invoice.
var jvTypeNeedles = []struct{ needle, answer string }{
	{"RECEIPT", "receipt"},
	{"PROFORMA INVOICE", "proforma"},
	{"QUOTATION", "quotation"},
	{"CREDIT NOTE", "credit note"},
	{"DELIVERY NOTE", "delivery note"},
	{"STATEMENT OF ACCOUNT", "statement"},
	{"PURCHASE ORDER", "purchase order"},
}

// jvReadsAs is the type one document's state text reads as, by literal needle alone.
func jvReadsAs(state string) string {
	for _, n := range jvTypeNeedles {
		if strings.Contains(state, n.needle) {
			return n.answer
		}
	}
	return "tax invoice"
}

// jvRequireUniqueNeedle fatals unless needle appears in exactly one of the 21 documents' state
// -- a needle matching none (or several) would silently misroute the fake's answer.
func jvRequireUniqueNeedle(t *testing.T, needle string) {
	t.Helper()
	var all []string
	all = append(all, requiredPDFs...)
	all = append(all, jvNonInvoicePDFs...)
	hits := 0
	for _, pdf := range all {
		if strings.Contains(extraction.DoclingPromptText(jvTokenPages(t, pdf)), needle) {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("needle %q appears in %d document('s) state, want exactly 1", needle, hits)
	}
}

// jvTypeFake answers each document the type its own text reads as, except the one document
// whose state carries misreadNeedle, answered misreadAs. An empty misreadNeedle means every
// document is answered correctly.
func jvTypeFake(t *testing.T, misreadNeedle, misreadAs, confidence string) *httptest.Server {
	t.Helper()
	if misreadNeedle != "" {
		jvRequireUniqueNeedle(t, misreadNeedle)
	}
	srv, _ := jvFake(t, func(state string, questions map[string]any) (string, int) {
		answer := jvReadsAs(state)
		if misreadNeedle != "" && strings.Contains(state, misreadNeedle) {
			answer = misreadAs
		}
		return jvChoiceBody(questions, answer, confidence), http.StatusOK
	})
	return srv
}

// jvConfusionCells parses the rendered confusion table into trueType -> answeredType -> count.
func jvConfusionCells(t *testing.T, md string) map[string]map[string]int {
	t.Helper()
	const head = "confusion table (rows: true type, columns: answered type)"
	i := strings.Index(md, head)
	if i == -1 {
		t.Fatalf("report has no confusion table")
	}
	options := jevmeasure.DocumentTypeOptions()
	cells := map[string]map[string]int{}
	for _, line := range strings.Split(md[i:], "\n") {
		if !strings.HasPrefix(line, "| ") || strings.Contains(line, "---") {
			continue
		}
		parts := strings.Split(strings.Trim(line, "|"), "|")
		if len(parts) != len(options)+1 {
			continue
		}
		trueType := strings.TrimSpace(parts[0])
		if trueType == "true type" {
			continue
		}
		row := map[string]int{}
		for j, opt := range options {
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(parts[j+1]), "%d", &n); err != nil {
				t.Fatalf("confusion row %q cell %q does not parse as a count", trueType, parts[j+1])
			}
			row[opt] = n
		}
		cells[trueType] = row
	}
	if len(cells) == 0 {
		t.Fatalf("confusion table parsed to 0 row(s); the parser is reading nothing")
	}
	return cells
}

// jvConfusionTotal sums every parsed cell.
func jvConfusionTotal(cells map[string]map[string]int) int {
	total := 0
	for _, row := range cells {
		for _, n := range row {
			total += n
		}
	}
	return total
}

// --- the tests ---

// AC-1. No t.Skip: t.Log + return, copying TestAIText_WriteCorpusKey's shape. The gate must log
// exactly one line naming which var is missing, and log nothing when it proceeds.
func TestJevValue_UnsetKeyLogsAndReturns(t *testing.T) {
	negative := func(t *testing.T, key, out string, wantKeyNamed, wantOutNamed bool) {
		t.Helper()
		t.Setenv("TYPESAFE_API_KEY", key)
		t.Setenv("JEV_OUT", out)
		jvResetGateLogs()
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			t.Errorf("the gate opened a socket")
		}))
		defer srv.Close()
		if got := jvGatedRun(t, srv.URL); got {
			t.Errorf("jvGatedRun(key=%q, out=%q) = true, want false", key, out)
		}
		if calls != 0 {
			t.Errorf("jvGatedRun(key=%q, out=%q) opened %d call(s), want 0", key, out, calls)
		}
		if len(jvGateLogs) != 1 {
			t.Fatalf("jvGatedRun(key=%q, out=%q) logged %d line(s), want exactly 1", key, out, len(jvGateLogs))
		}
		msg := jvGateLogs[0]
		if strings.Contains(msg, "TYPESAFE_API_KEY") != wantKeyNamed {
			t.Errorf("jvGatedRun(key=%q, out=%q) logged %q; TYPESAFE_API_KEY named = %v, want %v", key, out, msg, strings.Contains(msg, "TYPESAFE_API_KEY"), wantKeyNamed)
		}
		if strings.Contains(msg, "JEV_OUT") != wantOutNamed {
			t.Errorf("jvGatedRun(key=%q, out=%q) logged %q; JEV_OUT named = %v, want %v", key, out, msg, strings.Contains(msg, "JEV_OUT"), wantOutNamed)
		}
	}
	// wantKeyNamed/wantOutNamed let each subtest tell key-missing, out-missing and
	// both-missing apart by what the logged line names.
	t.Run("neither set", func(t *testing.T) { negative(t, "", "", true, true) })
	t.Run("key only", func(t *testing.T) { negative(t, "sk-test", "", false, true) })
	t.Run("out only", func(t *testing.T) { negative(t, "", t.TempDir(), true, false) })

	// Positive control leg (essential): without it a gate that always returns false passes.
	t.Run("both set: positive control", func(t *testing.T) {
		t.Setenv("TYPESAFE_API_KEY", "sk-test")
		t.Setenv("JEV_OUT", t.TempDir())
		jvResetGateLogs()
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"answers":{},"usage":{}}`))
		}))
		defer srv.Close()
		if got := jvGatedRun(t, srv.URL); !got {
			t.Errorf("jvGatedRun with both env vars set = false, want true")
		}
		if calls == 0 {
			t.Errorf("jvGatedRun with both env vars set opened 0 call(s), want > 0")
		}
		if len(jvGateLogs) != 0 {
			t.Errorf("jvGatedRun with both env vars set logged %d line(s), want 0 -- the decline line must be absent on the positive control", len(jvGateLogs))
		}
	})
}

// AC-2. jvDocuments is compared against an independent table -- requiredPDFs + jvNonInvoicePDFs.
func TestJevValue_TheWalkCoversEveryScoredLayoutAndNonInvoice(t *testing.T) {
	if len(expectByLayout) < wildRequireListPin {
		t.Fatalf("expectByLayout names %d row(s), want at least %d", len(expectByLayout), wildRequireListPin)
	}
	if len(requiredPDFs) < wildRequireListPin {
		t.Fatalf("requiredPDFs names %d entr(ies), want at least %d", len(requiredPDFs), wildRequireListPin)
	}

	var want []string
	want = append(want, requiredPDFs...)
	want = append(want, jvNonInvoicePDFs...)

	docs := jvDocuments()
	if len(docs) != len(want) {
		t.Fatalf("jvDocuments() has %d document(s), want %d (%d scored layouts + %d non-invoices)",
			len(docs), len(want), len(requiredPDFs), len(jvNonInvoicePDFs))
	}
	for i, doc := range docs {
		if doc.pdf != want[i] {
			t.Errorf("jvDocuments()[%d] = %q, want %q -- the fourteen scored layouts must precede the seven non-invoices, both in their own declared order", i, doc.pdf, want[i])
		}
	}
}

// AC-3. jvRows must deep-equal an independently recomputed Resolve -> LineItems -> Reconcile
// over the committed Docling golden, for all 21 documents; plus the three discriminators that
// stop the comparison being a tautology (R-17).
func TestJevValue_DecidedFieldsMatchTheEngine(t *testing.T) {
	var pdfs []string
	pdfs = append(pdfs, requiredPDFs...)
	pdfs = append(pdfs, jvNonInvoicePDFs...)

	for _, pdf := range pdfs {
		t.Run(pdf, func(t *testing.T) {
			pages := jvTokenPages(t, pdf)
			lines := extraction.LineItems(jvPages(t, pdf))
			cands := extraction.Resolve(pages, extraction.RuleSet{Tier1: extraction.Tier1Rules})
			want := extraction.Reconcile(extraction.Input{Candidates: cands, Lines: lines, Entity: extraction.Entity{}, Pages: pages})

			got := jvRows(t, pdf)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("jvRows(%s) does not deep-equal an independently recomputed Reconcile()", pdf)
			}
		})
	}

	t.Run("Pages is passed: wild_ruled_lines_totals.pdf's total stays ambiguous", func(t *testing.T) {
		const layout = "wild_ruled_lines_totals.pdf"
		rows := jvRows(t, layout)
		total, ok := jvFieldResult(rows, "total")
		if !ok {
			t.Fatalf("%s: jvRows has no total row", layout)
		}
		if total.Reason != extraction.ReasonAmbiguous {
			t.Errorf("%s: total's reason is %q, want ambiguous -- Pages must be passed to Reconcile", layout, total.Reason)
		}
	})

	t.Run("Lines is passed: the row count includes the line-item rows", func(t *testing.T) {
		const layout = "wild_ruled_lines_totals.pdf"
		lines := extraction.LineItems(jvPages(t, layout))
		if len(lines) != 3 {
			t.Fatalf("%s: LineItems returned %d line(s), want 3 -- the fixture no longer discriminates", layout, len(lines))
		}
		rows := jvRows(t, layout)
		if len(rows) != 22 {
			t.Errorf("%s: jvRows returned %d row(s), want 22 -- Lines must be passed to Reconcile", layout, len(rows))
		}
	})

	t.Run("Entity is empty: supplier_tin never reads inconsistent", func(t *testing.T) {
		for _, pdf := range requiredPDFs {
			st, ok := jvFieldResult(jvRows(t, pdf), "supplier_tin")
			if !ok {
				t.Errorf("%s: jvRows has no supplier_tin row", pdf)
				continue
			}
			if st.Reason != extraction.ReasonNone {
				t.Errorf("%s: supplier_tin reason is %q, want ReasonNone -- a non-empty Entity flips it to inconsistent", pdf, st.Reason)
			}
		}
	})
}

// AC-4. A doubted field is recorded not-asked with its reason, and no question is sent for it.
func TestJevValue_ADoubtedFieldIsRecordedNotAsked(t *testing.T) {
	const layout = "corpus_ambiguous_date.pdf"

	// Pre-clause: the engine really does report ambiguous here.
	real, ok := jvFieldResult(jvRows(t, layout), "issue_date")
	if !ok {
		t.Fatalf("%s: jvRows has no issue_date row", layout)
	}
	if real.Reason != extraction.ReasonAmbiguous {
		t.Fatalf("%s: issue_date's reason is %q, want ambiguous -- the fixture no longer discriminates", layout, real.Reason)
	}

	cells := jvCells(t, jvRows(t, layout), jvDoc{pdf: layout, trueType: "tax invoice", expect: wildExpectRow(t, layout)})
	cell, ok := jvCellFor(cells, "issue_date")
	if !ok {
		t.Fatalf("%s: jvCells has no issue_date cell", layout)
	}
	if cell.Label != "not-asked" || cell.Reason != "ambiguous" {
		t.Errorf("%s: issue_date cell = %+v, want Label not-asked, Reason ambiguous", layout, cell)
	}

	srv, recorded := jvFake(t, func(state string, questions map[string]any) (string, int) {
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	jvWalk(t, srv.URL, layout)
	prod := jvProductionRequest(t, *recorded)
	if _, ok := prod.Questions["issue_date"]; ok {
		t.Errorf("%s's production call carries an issue_date question; a doubted field must not be asked", layout)
	}
}

// AC-5 (R-7: unbuildable from the corpus -- no cell is both empty-expectation and decided).
func TestJevValue_AnEmptyExpectationIsNeverAHit(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		expect []string
		want   string
	}{
		{"nil expectation", "1075.00", nil, "wrong"},
		{"empty expectation", "1075.00", []string{}, "wrong"},
		{"empty value, nil expectation", "", nil, "wrong"},
		{"control: a hit labels right", "1075.00", []string{"1075.00"}, "right"},
		{"a miss labels wrong", "1075.00", []string{"1000.00"}, "wrong"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := jvLabel(c.value, c.expect); got != c.want {
				t.Errorf("jvLabel(%q, %v) = %q, want %q", c.value, c.expect, got, c.want)
			}
		})
	}
}

// AC-5, AC-4. Two branches the corpus cannot reach: every asked corpus cell is also correct, so
// a labeller wired to "right" would pass the walk; and no corpus cell is decided with a nil
// value, so the "no value" not-asked reason is never produced. Both are exercised here.
func TestJevValue_ACellIsLabelledAgainstItsOwnAnswerKey(t *testing.T) {
	val := func(s string) *string { return &s }
	rows := []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: val("INV-1"), Reason: extraction.ReasonNone}},
		{Field: extraction.Field{Name: "total", Value: val("999.00"), Reason: extraction.ReasonNone}},
		{Field: extraction.Field{Name: "currency", Value: nil, Reason: extraction.ReasonNone}},
	}
	doc := jvDoc{pdf: "synthetic.pdf", trueType: "tax invoice", expect: map[string][]string{
		"invoice_number": {"INV-1"},
		"total":          {"1000.00"},
	}}

	cells := jvCells(t, rows, doc)
	if len(cells) != len(writtenFields) {
		t.Fatalf("jvCells returned %d cell(s), want one per written field (%d)", len(cells), len(writtenFields))
	}

	want := []struct{ field, label, reason string }{
		{"invoice_number", "right", ""},
		{"total", "wrong", ""},                // decided, but not what the answer key says
		{"currency", "not-asked", "no value"}, // ReasonNone with no value is still not asked
	}
	for _, w := range want {
		cell, ok := jvCellFor(cells, w.field)
		if !ok {
			t.Errorf("jvCells has no %s cell", w.field)
			continue
		}
		if cell.Label != w.label || cell.Reason != w.reason {
			t.Errorf("%s cell = {Label:%q Reason:%q}, want {Label:%q Reason:%q}", w.field, cell.Label, cell.Reason, w.label, w.reason)
		}
	}
}

// R-1's ruling, new: a non-invoice's decided fields are not-asked for want of an answer key,
// and its production call carries exactly the one document_type question.
func TestJevValue_ANonInvoiceValueCellIsNotAskedForWantOfAnAnswerKey(t *testing.T) {
	const layout = "noninvoice_proforma.pdf"
	cells := jvCells(t, jvRows(t, layout), jvDoc{pdf: layout, trueType: jvTrueType[layout], expect: nil})

	decided := 0
	for _, c := range cells {
		if c.Reason != "no answer key" {
			continue
		}
		decided++
		if c.Label != "not-asked" {
			t.Errorf("%s field %s: label = %q, want not-asked", layout, c.Field, c.Label)
		}
	}
	if decided != 4 {
		t.Fatalf("%s: %d cell(s) reason \"no answer key\", want 4 -- the fixture no longer discriminates R-1", layout, decided)
	}

	srv, recorded := jvFake(t, func(state string, questions map[string]any) (string, int) {
		return jvChoiceBody(questions, jvTrueType[layout], "0.9"), http.StatusOK
	})
	jvWalk(t, srv.URL, layout)
	prod := jvProductionRequest(t, *recorded)
	if len(prod.Questions) != 1 {
		t.Errorf("%s's production call carries %d question(s), want exactly 1 (document_type)", layout, len(prod.Questions))
	}
	if _, ok := prod.Questions["document_type"]; !ok {
		t.Errorf("%s's production call has no document_type question", layout)
	}

	tally := 0
	for _, pdf := range jvNonInvoicePDFs {
		for _, c := range jvCells(t, jvRows(t, pdf), jvDoc{pdf: pdf, trueType: jvTrueType[pdf], expect: nil}) {
			if c.Reason == "no answer key" {
				tally++
			}
		}
	}
	// Measured 2026-09-22 against today's Tier1Rules and goldens. Re-measure and update in the
	// same commit if the corpus moves -- never relax this assertion (R-15).
	if tally != 15 {
		t.Errorf("no answer key tally = %d, want 15", tally)
	}
}

// AC-6. planted + skipped == 14 per variant, every skip reason is in that variant's closed set,
// and the planted total is 38 (R-15: the one aggregate literal this subtask pins).
func TestJevValue_EveryVariantIsPlantedOrCounted(t *testing.T) {
	if len(jvVariants) != 4 {
		t.Fatalf("jvVariants names %d variant(s), want exactly 4", len(jvVariants))
	}
	reasons := map[string]map[string]bool{
		"dropped_digit":     {"buyer_tin not decided": true, "value is not shaped NNNNNNNN-NNNN": true},
		"swapped_tins":      {"buyer_tin not decided": true, "supplier_tin not decided": true, "the two TINs are already equal": true},
		"subtotal_as_total": {"total not decided": true, "subtotal not decided": true, "subtotal equals total": true},
		"month_first_date":  {"issue_date not decided": true, "swap is a no-op": true, "the swapped month is not a valid month": true},
	}

	planted := map[string]int{}
	skipped := map[string]int{}
	for _, pdf := range requiredPDFs {
		doc := jvDoc{pdf: pdf, trueType: "tax invoice", expect: wildExpectRow(t, pdf)}
		cells := jvCells(t, jvRows(t, pdf), doc)
		for _, row := range jvPlants(cells, doc) {
			if row.Skip == "" {
				planted[row.Variant]++
				continue
			}
			skipped[row.Variant]++
			if !reasons[row.Variant][row.Skip] {
				t.Errorf("%s/%s: skip reason %q is not in the closed set for this variant", pdf, row.Variant, row.Skip)
			}
		}
	}

	total := 0
	for _, variant := range jvVariants {
		if n := planted[variant] + skipped[variant]; n != 14 {
			t.Errorf("%s: planted(%d) + skipped(%d) = %d, want 14", variant, planted[variant], skipped[variant], n)
		}
		total += planted[variant]
	}
	if total != 38 {
		t.Errorf("planted sites total %d, want 38", total)
	}
}

// AC-6. The swapped_tins variant plants the document's own decided supplier_tin, on two
// documents so a hard-coded literal cannot satisfy both.
func TestJevValue_TheSwappedTINVariantUsesTheDocumentsOwnSupplierTIN(t *testing.T) {
	cases := []struct{ pdf, supplierTIN, buyerTIN string }{
		{"corpus_two_column.pdf", "99999999-0401", "99999999-0402"},
		{"wild_rc_due_naira.pdf", "99999999-1001", "99999999-1002"},
	}
	for _, c := range cases {
		t.Run(c.pdf, func(t *testing.T) {
			doc := jvDoc{pdf: c.pdf, decided: map[string]string{"supplier_tin": c.supplierTIN}}
			cell := jvCell{Field: "buyer_tin", Value: c.buyerTIN, Label: "right"}
			value, skip := jvPlant("swapped_tins", cell, doc)
			if skip != "" {
				t.Fatalf("jvPlant(swapped_tins, %s) skipped: %q, want a plant", c.pdf, skip)
			}
			if value != c.supplierTIN {
				t.Errorf("jvPlant(swapped_tins, %s) = %q, want the document's own supplier_tin %q", c.pdf, value, c.supplierTIN)
			}
			if value == c.buyerTIN {
				t.Errorf("jvPlant(swapped_tins, %s) = %q, equals the correct buyer_tin -- no corruption was planted", c.pdf, value)
			}
		})
	}
}

// AC-6 (R-8: unbuildable from the corpus -- no corpus date has day == month).
func TestJevValue_TheMonthFirstVariantIsSkippedWhenDayEqualsMonth(t *testing.T) {
	cases := []struct {
		name, date, wantValue, wantSkip string
		wantAnySkip                     bool
	}{
		{name: "day equals month: no-op", date: "2026-03-03", wantSkip: "swap is a no-op"},
		{name: "control: a valid swap plants", date: "2026-03-04", wantValue: "2026-04-03"},
		{name: "swapped month invalid: skip", date: "2026-04-15", wantSkip: "the swapped month is not a valid month"},
		{name: "invalid source date: skip", date: "2026-02-30", wantAnySkip: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cell := jvCell{Field: "issue_date", Value: c.date, Label: "right"}
			value, skip := jvPlant("month_first_date", cell, jvDoc{})
			switch {
			case c.wantSkip != "":
				if skip != c.wantSkip {
					t.Errorf("jvPlant(month_first_date, %s) skip = %q, want %q", c.date, skip, c.wantSkip)
				}
			case c.wantAnySkip:
				if skip == "" {
					t.Errorf("jvPlant(month_first_date, %s) did not skip an invalid source date", c.date)
				}
			default:
				if skip != "" {
					t.Fatalf("jvPlant(month_first_date, %s) skipped: %q, want a plant", c.date, skip)
				}
				if value != c.wantValue {
					t.Errorf("jvPlant(month_first_date, %s) = %q, want %q", c.date, value, c.wantValue)
				}
			}
		})
	}
}

// AC-7. A planted variant is always labelled wrong, whatever noul Jev returns; the same run's
// 88 base rows are the control that the labeller is not hard-wired to wrong.
func TestJevValue_APlantedVariantIsAlwaysLabelledWrong(t *testing.T) {
	srv, _ := jvFake(t, func(state string, questions map[string]any) (string, int) {
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	outcomes := jvWalk(t, srv.URL)

	var variantWrong, baseRight int
	for _, o := range outcomes {
		if o.Check != "value_check" {
			continue
		}
		if o.Variant {
			if o.Label != "wrong" {
				t.Errorf("variant outcome %s/%s: label = %q, want wrong", o.DocumentID, o.Field, o.Label)
			} else {
				variantWrong++
			}
			if o.Probability == nil || o.Probability.String() != "0.99" {
				t.Errorf("variant outcome %s/%s: probability = %v, want 0.99", o.DocumentID, o.Field, o.Probability)
			}
			continue
		}
		if o.Label == "right" {
			baseRight++
		}
	}
	if variantWrong != 38 {
		t.Errorf("value_check has %d variant row(s) correctly labelled wrong, want 38", variantWrong)
	}
	// 15 (no answer key, above), 38 (planted total, TestJevValue_EveryVariantIsPlantedOrCounted)
	// and 88 here are corpus-shape figures that move together: re-measure all three in the same
	// commit when the corpus changes -- never relax any of them (R-15).
	if baseRight != 88 {
		t.Errorf("value_check has %d non-variant row(s) labelled right, want 88 -- control: the labeller is not hard-wired to wrong", baseRight)
	}
}

// AC-8. jvTrueType's seven non-invoice types are A30's options 2-8, and DocumentTypeOptions()
// is the closed eight, cloned.
func TestJevType_TheOptionListIsTheClosedEight(t *testing.T) {
	want := []string{
		"tax invoice", "receipt", "proforma", "quotation",
		"credit note", "delivery note", "statement", "purchase order",
	}
	got := jevmeasure.DocumentTypeOptions()
	if !slices.Equal(got, want) {
		t.Fatalf("DocumentTypeOptions() = %v, want %v", got, want)
	}
	if len(jevmeasure.DocumentTypeCriteria) != 8 {
		t.Fatalf("DocumentTypeCriteria has %d entr(ies), want 8", len(jevmeasure.DocumentTypeCriteria))
	}
	for _, opt := range want {
		if desc := jevmeasure.DocumentTypeCriteria[opt]; desc == "" {
			t.Errorf("DocumentTypeCriteria has no (or an empty) entry for %q", opt)
		}
	}

	// The clone: mutating one call's result must not move the next call's.
	got[0] = "mutated"
	again := jevmeasure.DocumentTypeOptions()
	if again[0] != want[0] {
		t.Errorf("DocumentTypeOptions() returned a shared slice: mutating one call's result moved the next call's")
	}

	if len(jvNonInvoicePDFs) != 7 {
		t.Fatalf("jvNonInvoicePDFs names %d PDF(s), want 7", len(jvNonInvoicePDFs))
	}
	for _, pdf := range jvNonInvoicePDFs {
		transformed := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(pdf, jvNonInvoicePrefix), ".pdf"), "_", " ")
		mapped, ok := jvTrueType[pdf]
		if !ok {
			t.Errorf("jvTrueType has no entry for %s", pdf)
			continue
		}
		if mapped != transformed {
			t.Errorf("jvTrueType[%q] = %q, want the filename transform %q", pdf, mapped, transformed)
		}
		if mapped == "tax invoice" || !slices.Contains(want[1:], mapped) {
			t.Errorf("jvTrueType[%q] = %q, want one of A30's options 2-8", pdf, mapped)
		}
	}
}

// AC-8. The confusion table has a row for every true type, and its cells sum to the documents
// asked the document_type question.
func TestJevType_TheConfusionTableHasARowPerTrueType(t *testing.T) {
	// Every document answered the type its own text reads as.
	srv := jvTypeFake(t, "", "", "0.95")
	outcomes := jvWalk(t, srv.URL)
	if len(outcomes) == 0 {
		t.Fatalf("jvWalk produced 0 outcome(s); no confusion table can be built from nothing")
	}

	md, _, err := jevmeasure.Render(outcomes, jevmeasure.Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	cells := jvConfusionCells(t, string(md))
	options := jevmeasure.DocumentTypeOptions()
	if len(cells) != len(options) {
		t.Fatalf("confusion table has %d row(s), want one per true type (%d): %v", len(cells), len(options), cells)
	}
	if total := jvConfusionTotal(cells); total != 21 {
		t.Errorf("confusion table cells sum to %d, want 21 (one per document asked)", total)
	}
	// Every answer was correct, so the whole table sits on the diagonal.
	wantDiagonal := map[string]int{"tax invoice": 14}
	for _, opt := range options[1:] {
		wantDiagonal[opt] = 1
	}
	for _, trueType := range options {
		row, ok := cells[trueType]
		if !ok {
			t.Errorf("confusion table has no row for true type %q", trueType)
			continue
		}
		for _, answered := range options {
			want := 0
			if answered == trueType {
				want = wantDiagonal[trueType]
			}
			if row[answered] != want {
				t.Errorf("confusion cell (%q, %q) = %d, want %d", trueType, answered, row[answered], want)
			}
		}
	}

	sum := 0
	for _, o := range outcomes {
		if o.Check == "document_type_check" && o.Answer != "" {
			sum++
		}
	}
	if sum != 21 {
		t.Errorf("21 documents must ask the document-type question and carry an Answer, got %d", sum)
	}
}

// AC-9. A tax invoice answered as any other type is a false alarm, separate from the
// confusion table's non-invoice totals.
func TestJevType_ATaxInvoiceReadAsAReceiptIsAFalseAlarm(t *testing.T) {
	// corpus_inline_labels.pdf (INV-1001) reads as receipt; every other document reads correctly.
	srv := jvTypeFake(t, "INV-1001", "receipt", "0.95")
	outcomes := jvWalk(t, srv.URL)
	if len(outcomes) == 0 {
		t.Fatalf("jvWalk produced 0 outcome(s)")
	}
	md, _, err := jevmeasure.Render(outcomes, jevmeasure.Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	falseAlarms := jvLineWithTokens(t, body, "false alarms")
	if !jvHasNumber(falseAlarms, "1") {
		t.Errorf("false-alarm line %q must read 1", falseAlarms)
	}
	misses := jvLineWithTokens(t, body, "non-invoice misses")
	if !jvHasNumber(misses, "0") {
		t.Errorf("non-invoice-miss line %q must read 0", misses)
	}

	cells := jvConfusionCells(t, body)
	if got := cells["tax invoice"]["receipt"]; got != 1 {
		t.Errorf("confusion cell (tax invoice, receipt) = %d, want 1", got)
	}
	if got := cells["tax invoice"]["tax invoice"]; got != 13 {
		t.Errorf("confusion cell (tax invoice, tax invoice) = %d, want 13", got)
	}
	if total := jvConfusionTotal(cells); total != 21 {
		t.Errorf("confusion table cells sum to %d, want 21 -- a false alarm stays in the table", total)
	}
}

// AC-9. A non-invoice read as a different non-invoice type is a confusion-table miss, not a
// false alarm -- the pair with the test above that discriminates A31 in both directions.
func TestJevType_ANonInvoiceReadAsAnotherNonInvoiceIsNotAFalseAlarm(t *testing.T) {
	// noninvoice_receipt.pdf (titled RECEIPT) reads as proforma; every other document reads
	// correctly.
	srv := jvTypeFake(t, "RECEIPT", "proforma", "0.9")
	outcomes := jvWalk(t, srv.URL)
	if len(outcomes) == 0 {
		t.Fatalf("jvWalk produced 0 outcome(s)")
	}
	md, _, err := jevmeasure.Render(outcomes, jevmeasure.Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)

	falseAlarms := jvLineWithTokens(t, body, "false alarms")
	if !jvHasNumber(falseAlarms, "0") {
		t.Errorf("false-alarm line %q must read 0 -- a non-invoice misread is not a false alarm", falseAlarms)
	}
	misses := jvLineWithTokens(t, body, "non-invoice misses")
	if !jvHasNumber(misses, "1") {
		t.Errorf("non-invoice-miss line %q must read 1", misses)
	}

	cells := jvConfusionCells(t, body)
	if got := cells["receipt"]["proforma"]; got != 1 {
		t.Errorf("confusion cell (receipt, proforma) = %d, want 1", got)
	}
	if got := cells["tax invoice"]["tax invoice"]; got != 14 {
		t.Errorf("confusion cell (tax invoice, tax invoice) = %d, want 14 -- no tax invoice was misread here", got)
	}
	if total := jvConfusionTotal(cells); total != 21 {
		t.Errorf("confusion table cells sum to %d, want 21", total)
	}
}

// AC-12. Whether the choice answer carried a confidence is recorded in the report's
// provenance section, gated so an ungated line cannot red the shipped no-confidence-column test.
func TestJevType_TheProbabilityPresenceIsRecordedInProvenance(t *testing.T) {
	run := func(t *testing.T, confidence string) string {
		t.Helper()
		srv, _ := jvFake(t, func(state string, questions map[string]any) (string, int) {
			return jvChoiceBody(questions, "tax invoice", confidence), http.StatusOK
		})
		outcomes := jvWalk(t, srv.URL)
		if len(outcomes) == 0 {
			t.Fatalf("jvWalk produced 0 outcome(s)")
		}
		md, _, err := jevmeasure.Render(outcomes, jevmeasure.Pricing{})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		return string(md)
	}

	sectionA := jvSection(t, run(t, "0.95"), "Provenance")
	if !strings.Contains(sectionA, "confidence: present") {
		t.Errorf("provenance section %q must read confidence: present", sectionA)
	}
	if strings.Contains(sectionA, "confidence: absent") {
		t.Errorf("provenance section %q must not read absent -- this run's choice carried a confidence", sectionA)
	}

	sectionB := jvSection(t, run(t, ""), "Provenance")
	if !strings.Contains(sectionB, "confidence: absent") {
		t.Errorf("provenance section %q must read confidence: absent", sectionB)
	}
	if strings.Contains(sectionB, "confidence: present") {
		t.Errorf("provenance section %q must not read present -- this run's choice carried no confidence", sectionB)
	}
}

// D3. One document, one production-shaped call, plus its variant call -- exactly 2 requests,
// and exactly one carries the document_type key.
func TestJevValue_OneDocumentProducesOneProductionShapedCall(t *testing.T) {
	const layout = "corpus_inline_labels.pdf"
	srv, recorded := jvFake(t, func(state string, questions map[string]any) (string, int) {
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	jvWalk(t, srv.URL, layout)

	if len(*recorded) != 2 {
		t.Fatalf("%s produced %d request(s), want exactly 2 (one production, one variant)", layout, len(*recorded))
	}
	typeQuestions := 0
	for _, req := range *recorded {
		if _, ok := req.Questions["document_type"]; ok {
			typeQuestions++
		}
	}
	if typeQuestions != 1 {
		t.Errorf("%s: %d request(s) carry a document_type question, want exactly 1", layout, typeQuestions)
	}
}

// D3. wild_scanned_no_number.pdf decides exactly six fields; the production call's questions
// map must hold exactly those six noul keys plus the one choice key.
func TestJevValue_TheProductionCallCarriesEveryDecidedFieldAndTheTypeQuestion(t *testing.T) {
	const layout = "wild_scanned_no_number.pdf"

	rows := jvRows(t, layout)
	decided := 0
	for _, f := range writtenFields {
		fr, ok := jvFieldResult(rows, f)
		if ok && fr.Reason == extraction.ReasonNone && fr.Value != nil {
			decided++
		}
	}
	if decided != 6 {
		t.Fatalf("%s decides %d written field(s), want 6 -- the fixture no longer discriminates", layout, decided)
	}

	srv, recorded := jvFake(t, func(state string, questions map[string]any) (string, int) {
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	jvWalk(t, srv.URL, layout)
	prod := jvProductionRequest(t, *recorded)

	want := map[string]string{
		"issue_date": jevmeasure.QuestionTypeNoul, "buyer_tin": jevmeasure.QuestionTypeNoul,
		"currency": jevmeasure.QuestionTypeNoul, "subtotal": jevmeasure.QuestionTypeNoul,
		"vat": jevmeasure.QuestionTypeNoul, "total": jevmeasure.QuestionTypeNoul,
		"document_type": jevmeasure.QuestionTypeChoice,
	}
	if len(prod.Questions) != len(want) {
		t.Fatalf("%s's production call carries %d question(s), want %d", layout, len(prod.Questions), len(want))
	}
	for id, wantType := range want {
		q, ok := prod.Questions[id].(map[string]any)
		if !ok {
			t.Errorf("%s's production call has no %q question", layout, id)
			continue
		}
		if got, _ := q["type"].(string); got != wantType {
			t.Errorf("%s's %q question type = %q, want %q", layout, id, got, wantType)
		}
	}
	for _, absent := range []string{"invoice_number", "buyer_name"} {
		if _, ok := prod.Questions[absent]; ok {
			t.Errorf("%s's production call carries %q, which is not decided on this layout", layout, absent)
		}
	}
}

// D3. corpus_split_labels.pdf has exactly three plantable variants; they ride a second call,
// carrying exactly those three noul questions, tagged variant_ and distinct from production.
func TestJevValue_TheVariantsRideTheirOwnCall(t *testing.T) {
	const layout = "corpus_split_labels.pdf"

	doc := jvDoc{pdf: layout, trueType: "tax invoice", expect: wildExpectRow(t, layout)}
	cells := jvCells(t, jvRows(t, layout), doc)
	planted := 0
	for _, row := range jvPlants(cells, doc) {
		if row.Skip == "" {
			planted++
		}
	}
	if planted != 3 {
		t.Fatalf("%s has %d plantable variant(s), want 3 -- the fixture no longer discriminates", layout, planted)
	}

	srv, recorded := jvFake(t, func(state string, questions map[string]any) (string, int) {
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	outcomes := jvWalk(t, srv.URL, layout)
	if len(*recorded) != 2 {
		t.Fatalf("%s produced %d request(s), want exactly 2", layout, len(*recorded))
	}
	prod, variant := (*recorded)[0], (*recorded)[1]
	if prod.State != variant.State {
		t.Errorf("%s: the production and variant calls carry different state strings", layout)
	}
	if len(variant.Questions) != 3 {
		t.Errorf("%s's variant call carries %d question(s), want 3", layout, len(variant.Questions))
	}
	if _, ok := variant.Questions["document_type"]; ok {
		t.Errorf("%s's variant call carries a document_type question", layout)
	}
	for id, raw := range variant.Questions {
		if !strings.HasPrefix(id, "variant_") {
			t.Errorf("%s's variant question id %q lacks the variant_ prefix", layout, id)
		}
		q, _ := raw.(map[string]any)
		if got, _ := q["type"].(string); got != jevmeasure.QuestionTypeNoul {
			t.Errorf("%s's variant question %q has type %q, want noul", layout, id, got)
		}
	}

	variantOutcomes := 0
	callIDs := map[string]bool{}
	for _, o := range outcomes {
		if o.DocumentID != layout || o.CallID == "" {
			continue
		}
		callIDs[o.CallID] = true
		if o.Variant {
			variantOutcomes++
		}
	}
	if variantOutcomes != 3 {
		t.Errorf("%s produced %d Variant outcome(s), want 3", layout, variantOutcomes)
	}
	// Two distinct CallIDs, or usageStats folds the variant call's tokens into the production
	// call and the all-calls cost silently loses them.
	if len(callIDs) != 2 {
		t.Errorf("%s's outcomes carry %d distinct CallID(s) (%v), want 2 -- one per call", layout, len(callIDs), callIDs)
	}
}

// AC-13. A failed Ask() is counted and the walk continues to every document; the failed
// document's would-be-asked rows carry Failed, not-asked and a reason.
func TestJevValue_AFailedCallIsCountedNotFatal(t *testing.T) {
	const failing = "corpus_stacked_labels.pdf"

	// Floor: INV-1003 must appear in exactly one document's state.
	hits := 0
	for _, pdf := range requiredPDFs {
		if strings.Contains(extraction.DoclingPromptText(jvTokenPages(t, pdf)), "INV-1003") {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("INV-1003 appears in %d document('s) state, want exactly 1", hits)
	}

	srv, _ := jvFake(t, func(state string, questions map[string]any) (string, int) {
		if strings.Contains(state, "INV-1003") {
			return "", http.StatusInternalServerError
		}
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	outcomes := jvWalk(t, srv.URL)

	docs := map[string]bool{}
	for _, o := range outcomes {
		docs[o.DocumentID] = true
	}
	var all []string
	all = append(all, requiredPDFs...)
	all = append(all, jvNonInvoicePDFs...)
	for _, pdf := range all {
		if !docs[pdf] {
			t.Errorf("no outcome row carries DocumentID %s; the walk must not stop at a failed call", pdf)
		}
	}

	sawFailed := false
	for _, o := range outcomes {
		if o.DocumentID != failing || !o.Failed {
			continue
		}
		sawFailed = true
		if o.Label != "not-asked" {
			t.Errorf("%s: a failed row has Label %q, want not-asked", failing, o.Label)
		}
		if o.Reason == "" {
			t.Errorf("%s: a failed row carries no Reason", failing)
		}
	}
	if !sawFailed {
		t.Errorf("no outcome row for %s carries Failed: true", failing)
	}
}

// AC-13, paired with the test above: the row count never changes, whatever fails.
func TestJevValue_TheRowCountIsTheSameWhetherOrNotACallFails(t *testing.T) {
	srvOK, _ := jvFake(t, func(state string, questions map[string]any) (string, int) {
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	okValue, okType := jvCountByCheck(jvWalk(t, srvOK.URL))

	srvFail, _ := jvFake(t, func(state string, questions map[string]any) (string, int) {
		if strings.Contains(state, "INV-1003") {
			return "", http.StatusInternalServerError
		}
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	failValue, failType := jvCountByCheck(jvWalk(t, srvFail.URL))

	if okValue != 224 || failValue != 224 {
		t.Errorf("value_check row counts = %d (all-succeed), %d (one failure), want 224 both times", okValue, failValue)
	}
	if okType != 21 || failType != 21 {
		t.Errorf("document_type_check row counts = %d (all-succeed), %d (one failure), want 21 both times", okType, failType)
	}
}

// AC-2. Every path the Go harness opens goes through jvOpen/jvWrite, and every recorded path
// sits under eeFxDir or the run's own JEV_OUT.
func TestJevValue_TheGoHarnessOpensOnlyFixtureRootPaths(t *testing.T) {
	out := t.TempDir()

	// Control needle: the predicate must discriminate before the runtime leg trusts it.
	if jvPathAllowed("/etc/passwd", out) {
		t.Fatalf("jvPathAllowed(%q, out) = true, want false", "/etc/passwd")
	}
	const testdataGolden = "../testdata/corpus_inline_labels.docling.json"
	if !jvPathAllowed(testdataGolden, out) {
		t.Fatalf("jvPathAllowed(%q, out) = false, want true", testdataGolden)
	}

	// Runtime leg.
	jvResetPaths()
	srv, _ := jvFake(t, func(state string, questions map[string]any) (string, int) {
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	jvWalk(t, srv.URL)
	if len(jvPaths) == 0 {
		t.Fatalf("the walk recorded 0 path(s) through jvOpen/jvWrite; a walk that touches no golden proves nothing about scope")
	}
	if len(jvPaths) < 21 {
		t.Errorf("the walk recorded %d path(s), want at least 21", len(jvPaths))
	}
	for _, p := range jvPaths {
		if !jvPathAllowed(p, out) {
			t.Errorf("recorded path %q is outside %s and outside the fixture root", p, eeFxDir)
		}
	}

	// Source leg: this file's own text carries exactly one filesystem read and one filesystem
	// write, both inside jvOpen/jvWrite, and no bare file open. Self-match: every needle is two
	// fragments joined AND is interpolated into its diagnostic rather than spelled there, so
	// neither the scan nor its own error message is counted as a call site.
	readNeedle, writeNeedle, openNeedle := "os.Read"+"File(", "os.Write"+"File(", "os.Op"+"en("
	src := wildReadFile(t, "jev_value_test.go")
	if n := strings.Count(src, readNeedle); n != 1 {
		t.Errorf("jev_value_test.go carries %d %s call(s), want exactly 1 (jvOpen)", n, readNeedle)
	}
	if n := strings.Count(src, writeNeedle); n != 1 {
		t.Errorf("jev_value_test.go carries %d %s call(s), want exactly 1 (jvWrite)", n, writeNeedle)
	}
	if n := strings.Count(src, openNeedle); n != 0 {
		t.Errorf("jev_value_test.go carries %d %s call(s), want 0", n, openNeedle)
	}
}

// AC-14. state must equal extraction.DoclingPromptText(jvTokenPages(...)) byte for byte --
// never the PDFium reading, never an empty page list.
func TestJevValue_StateIsTheSharedDoclingShape(t *testing.T) {
	layouts := []string{"corpus_inline_labels.pdf", "wild_ruled_lines_totals.pdf", "wild_scanned_no_number.pdf"}

	for _, layout := range layouts {
		t.Run(layout, func(t *testing.T) {
			docling := extraction.DoclingPromptText(jvTokenPages(t, layout))
			pdfium := extraction.DoclingPromptText(eeTokenPages(t, layout))
			empty := extraction.DoclingPromptText(nil)
			// Pre-clause: the negative control below is vacuous if the two readers agree.
			if docling == pdfium {
				t.Fatalf("%s: the Docling and PDFium readings are byte-identical; the negative control below proves nothing", layout)
			}

			srv, recorded := jvFake(t, func(state string, questions map[string]any) (string, int) {
				return jvNoulBody(questions, "0.99"), http.StatusOK
			})
			jvWalk(t, srv.URL, layout)
			prod := jvProductionRequest(t, *recorded)

			if prod.State != docling {
				t.Errorf("%s: request state does not equal DoclingPromptText(jvTokenPages(...)) byte for byte", layout)
			}
			if prod.State == pdfium {
				t.Errorf("%s: request state equals the PDFium reading; the wrong reader was sent", layout)
			}
			if prod.State == empty {
				t.Errorf("%s: request state equals DoclingPromptText(nil) (the 220-byte intro alone); jvTokenPages read nothing", layout)
			}
		})
	}
}

// R-11, new. Every noul question quotes ValueCheckInstructions as a prefix and names its field
// and value; every noul's criteria is the shipped true/false pair; the choice question quotes
// DocumentTypeInstructions and DocumentTypeCriteria exactly.
func TestJevValue_EveryValueQuestionQuotesTheShippedWording(t *testing.T) {
	srv, recorded := jvFake(t, func(state string, questions map[string]any) (string, int) {
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	jvWalk(t, srv.URL)

	noulSeen := 0
	for _, req := range *recorded {
		for id, raw := range req.Questions {
			q, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("question %q did not decode to an object", id)
				continue
			}
			qType, _ := q["type"].(string)
			instructions, _ := q["instructions"].(string)
			criteria, _ := q["criteria"].(map[string]any)

			switch qType {
			case jevmeasure.QuestionTypeNoul:
				noulSeen++
				if !strings.HasPrefix(instructions, jevmeasure.ValueCheckInstructions) {
					t.Errorf("noul question %q instructions does not carry ValueCheckInstructions as a prefix", id)
				}
				if instructions == jevmeasure.ValueCheckInstructions {
					t.Errorf("noul question %q instructions names no field or value", id)
				}
				if len(criteria) != 2 {
					t.Errorf("noul question %q carries %d criteria key(s), want 2 (true, false)", id, len(criteria))
				}
				if got, _ := criteria["true"].(string); got != jevmeasure.ValueCheckCriteriaTrue {
					t.Errorf("noul question %q criteria.true = %q, want the shipped constant", id, got)
				}
				if got, _ := criteria["false"].(string); got != jevmeasure.ValueCheckCriteriaFalse {
					t.Errorf("noul question %q criteria.false = %q, want the shipped constant", id, got)
				}
			case jevmeasure.QuestionTypeChoice:
				if instructions != jevmeasure.DocumentTypeInstructions {
					t.Errorf("choice question %q instructions = %q, want DocumentTypeInstructions exactly", id, instructions)
				}
				if len(criteria) != len(jevmeasure.DocumentTypeCriteria) {
					t.Errorf("choice question %q carries %d criteria key(s), want %d", id, len(criteria), len(jevmeasure.DocumentTypeCriteria))
				}
				for opt, want := range jevmeasure.DocumentTypeCriteria {
					if got, _ := criteria[opt].(string); got != want {
						t.Errorf("choice question %q criteria[%q] = %q, want %q", id, opt, got, want)
					}
				}
			}
		}
	}
	if noulSeen < 88 {
		t.Errorf("saw %d noul question(s) over the walk, want at least 88", noulSeen)
	}
}

// C-2. No outcome may carry Failed: true together with a Label other than not-asked -- the
// invariant that makes D-4's ruling enforceable rather than conventional.
func TestJevValue_AFailedRowCarriesNotAskedAndNeverALabel(t *testing.T) {
	srv, _ := jvFake(t, func(state string, questions map[string]any) (string, int) {
		if strings.Contains(state, "INV-1003") {
			return "", http.StatusInternalServerError
		}
		return jvNoulBody(questions, "0.99"), http.StatusOK
	})
	outcomes := jvWalk(t, srv.URL)
	if len(outcomes) == 0 {
		t.Fatalf("jvWalk produced 0 outcome(s); nothing to check")
	}
	for _, o := range outcomes {
		if o.Failed && o.Label != "not-asked" {
			t.Errorf("%s/%s: Failed=true but Label=%q, want not-asked", o.DocumentID, o.Field, o.Label)
		}
	}
}
