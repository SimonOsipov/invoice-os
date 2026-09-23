// jev_mapping_test.go: CHECK-01-07's mapping check. For each layout under $JEV_OUT, builds the
// AUTO placement set (auto_placements.json, CHECK-01-06) and the Flash Lite set by replaying
// mapping_answers.jsonl (CHECK-01-05/06) through the SHIPPED guardHeaderRow/guardPlacements,
// re-decoding at the answered header row exactly as SuggestMappingHandler does (D-2). Labels
// each placement against the layout's key, asks Jev one noul question per placement, and merges
// into the $JEV_OUT/jev-outcomes.json ledger jevmeasure.MergeOutcomes produces (D-1). No
// OpenRouter call anywhere in this file (J-3).
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/jevmeasure"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// jpLayout is one $JEV_OUT/layouts.json entry. Key[f] is the list of headers the generator
// considers an equally-correct answer for field f (A56): never null AND a header in the same
// list -- either a non-empty list of header strings, or the single-element list [null].
type jpLayout struct {
	ID      string               `json:"id"`
	Columns []string             `json:"columns"`
	Key     map[string][]*string `json:"key"`
}

// jpKeyIsComplete/jpAutoEntryIsComplete are pulled out pure so the "missing a canonical field is
// fatal" control leg is directly testable without needing to observe a t.Fatalf.
func jpKeyIsComplete(key map[string][]*string) bool {
	for _, f := range mappingFields {
		if _, ok := key[f]; !ok {
			return false
		}
	}
	return true
}

func jpAutoEntryIsComplete(entry map[string]string) bool {
	for _, f := range mappingFields {
		if _, ok := entry[f]; !ok {
			return false
		}
	}
	return true
}

// jpLoadLayouts reads $JEV_OUT/layouts.json. Fatal on absence, decode failure, zero layouts, or
// any layout whose key does not hold all eleven canonicalFields.
func jpLoadLayouts(t *testing.T, dir string) []jpLayout {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "layouts.json"))
	if err != nil {
		t.Fatalf("read layouts.json: %v", err)
	}
	var layouts []jpLayout
	if err := json.Unmarshal(b, &layouts); err != nil {
		t.Fatalf("unmarshal layouts.json: %v", err)
	}
	if len(layouts) == 0 {
		t.Fatalf("layouts.json holds no layouts")
	}
	for _, l := range layouts {
		if !jpKeyIsComplete(l.Key) {
			t.Fatalf("layout %q's key does not hold all eleven canonical fields", l.ID)
		}
	}
	return layouts
}

// jpAutoSet reads $JEV_OUT/auto_placements.json: layout id -> field -> header, a JSON null
// decoding to "" (an unplaced field, not an error; CHECK-01-06 §2.1). Fatal only on: file
// absent, decode failure, an id not in layouts.json, or an entry missing one of the eleven keys
// -- an all-null entry (a layout that placed nothing) is normal output, never fatal.
func jpAutoSet(t *testing.T, dir string, layouts []jpLayout) map[string]map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "auto_placements.json"))
	if err != nil {
		t.Fatalf("read auto_placements.json: %v", err)
	}
	var raw map[string]map[string]string
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal auto_placements.json: %v", err)
	}
	known := map[string]bool{}
	for _, l := range layouts {
		known[l.ID] = true
	}
	for id, entry := range raw {
		if !known[id] {
			t.Fatalf("auto_placements.json names layout %q, absent from layouts.json", id)
		}
		if !jpAutoEntryIsComplete(entry) {
			t.Fatalf("auto_placements.json entry %q does not hold all eleven canonical fields", id)
		}
	}
	return raw
}

// jpOpenCSV opens $JEV_OUT/csv/<id>.csv -- the file production's own re-decode (D-2) reads,
// never layouts.json's rows (CHECK-01-06 D-1: encoding/csv drops the titled layouts' blank
// record, so a rows-based window is a prompt production never sends).
func jpOpenCSV(t *testing.T, dir, id string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "csv", id+".csv"))
	if err != nil {
		t.Fatalf("open csv for %s: %v", id, err)
	}
	return f
}

// jpWindow is production's own path: decode the CSV, then suggestWindow -- the same window sent
// to Jev as state (§3.5), and the same window guardHeaderRow's window length is measured
// against.
func jpWindow(t *testing.T, dir, id string) [][]string {
	t.Helper()
	f := jpOpenCSV(t, dir, id)
	defer func() { _ = f.Close() }()
	header, rows, _, err := Decode(f, "csv")
	if err != nil {
		t.Fatalf("Decode csv for %s: %v", id, err)
	}
	return suggestWindow(header, rows)
}

// jpAISet builds the Flash Lite placement set by replaying every recorded answer through the
// SHIPPED guardHeaderRow -> DecodeFrom(headerRow) -> guardPlacements, exactly as
// SuggestMappingHandler does (D-2). mapping_answers.jsonl absent (the recorder has not run yet)
// answers an empty set, not an error -- the Flash Lite N is owed by the live run, never invented
// here.
func jpAISet(t *testing.T, dir string, layouts []jpLayout) map[string]map[string]string {
	t.Helper()
	known := map[string]bool{}
	for _, l := range layouts {
		known[l.ID] = true
	}
	path := filepath.Join(dir, "mapping_answers.jsonl")
	answers, err := jrReadAnswers(path, known)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]map[string]string{}
		}
		t.Fatalf("jrReadAnswers: %v", err)
	}

	out := map[string]map[string]string{}
	for _, a := range answers {
		f1 := jpOpenCSV(t, dir, a.Layout)
		hdr1, rows1, _, err := Decode(f1, "csv")
		_ = f1.Close()
		if err != nil {
			t.Fatalf("Decode csv for %s: %v", a.Layout, err)
		}

		window := suggestWindow(hdr1, rows1)
		headerRow := guardHeaderRow(a.Answer, len(window))
		header := hdr1
		if headerRow != defaultHeaderRow {
			f2 := jpOpenCSV(t, dir, a.Layout)
			h, _, _, derr := DecodeFrom(f2, "csv", headerRow)
			_ = f2.Close()
			// Production's own fallback, literally: a re-parse failure or a blank header at
			// that row falls back to row 1, never fails the layout.
			if derr == nil && len(h) > 0 {
				header = h
			}
		}

		placed := guardPlacements(a.Answer, header)
		full := make(map[string]string, len(mappingFields))
		for _, f := range mappingFields {
			full[f] = placed[f]
		}
		out[a.Layout] = full
	}
	return out
}

// jpAccepted is layout.Key[field] with its nulls dropped (§3.4): the list of headers the
// generator considers equally correct.
func jpAccepted(layout jpLayout, field string) []string {
	var out []string
	for _, h := range layout.Key[field] {
		if h != nil {
			out = append(out, *h)
		}
	}
	return out
}

const (
	jpReasonNoKeyNoPlacement = "no key column and nothing placed"
	jpReasonKeyedButUnplaced = "the key names a column; nothing was placed"
)

// jpLabel is pure and total (Row 22): every (accepted, header) pair returns exactly one of
// right/wrong/not-asked, with a reason iff not-asked. A56: right on ANY accepted member, never
// accepted[0] -- AUTO never exercises the multi-member case (§1.3), so this is the only defence
// against a Key[f][0] scorer.
func jpLabel(accepted []string, header string) (label, reason string) {
	switch {
	case header == "" && len(accepted) == 0:
		return "not-asked", jpReasonNoKeyNoPlacement
	case header == "":
		return "not-asked", jpReasonKeyedButUnplaced
	case slices.Contains(accepted, header):
		return "right", ""
	default:
		return "wrong", ""
	}
}

// jpQuestion composes one noul question: MappingCheckInstructions names neither the field nor
// the header (same resolution as CHECK-01-04 R-11's value question), so the constant is sent as
// a byte-exact prefix.
func jpQuestion(field, header string) jevmeasure.Question {
	return jevmeasure.Question{
		Type:         jevmeasure.QuestionTypeNoul,
		Instructions: jevmeasure.MappingCheckInstructions + fmt.Sprintf(" Field: %s. Column header: %s.", field, header),
		Criteria: map[string]string{
			"true":  jevmeasure.MappingCheckCriteriaTrue,
			"false": jevmeasure.MappingCheckCriteriaFalse,
		},
	}
}

// jpReader names what produced a mapping document's text: the mapping documents are
// spreadsheets, so naming a PDF reader would be a lie (§3.6).
const jpReader = "importer.Decode (csv)"

// jpCallTimeout bounds every Ask(), mirroring endtoend's jvCallTimeout: Client sets no
// http.Client.Timeout, so ctx is the walk's only clock.
const jpCallTimeout = 30 * time.Second

const jpModel = "systemone-default"

func jpResolveModel() string {
	if m := os.Getenv("JEV_MODEL"); m != "" {
		return m
	}
	return jpModel
}

// The harness and the product client send one model id; JEV_MODEL still overrides it.
func TestJevMapping_TheDefaultModelIsTheClientsModel(t *testing.T) {
	t.Setenv("JEV_MODEL", "")
	if err := os.Unsetenv("JEV_MODEL"); err != nil {
		t.Fatalf("unset JEV_MODEL: %v", err)
	}
	if got := jpResolveModel(); got != jev.Model {
		t.Errorf("jpResolveModel() with JEV_MODEL unset = %q, want jev.Model %q", got, jev.Model)
	}

	t.Setenv("JEV_MODEL", "x")
	if got := jpResolveModel(); got != "x" {
		t.Errorf("jpResolveModel() with JEV_MODEL=x = %q, want %q", got, "x")
	}
}

// jpResolvePricing reads the operator's two $ rates per million tokens (A50); either absent or
// malformed leaves the zero value, which Render prints as "price not supplied".
func jpResolvePricing() jevmeasure.Pricing {
	return jevmeasure.PricingFromRates(os.Getenv(jevmeasure.PriceInputEnv), os.Getenv(jevmeasure.PriceOutputEnv))
}

// jpWalk runs the measurement pass for every layout under dir: the AUTO call and the Flash Lite
// call are each production-shaped (D3) -- one Ask() per layout per set with at least one
// placement, zero calls for a set with none.
func jpWalk(t *testing.T, baseURL, dir string) []jevmeasure.Outcome {
	t.Helper()
	layouts := jpLoadLayouts(t, dir)
	auto := jpAutoSet(t, dir, layouts)
	aiSet := jpAISet(t, dir, layouts)

	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		key = "jp-test-key"
	}
	client := jevmeasure.NewClient(baseURL, jpResolveModel(), key)

	var outcomes []jevmeasure.Outcome
	for _, layout := range layouts {
		window := jpWindow(t, dir, layout.ID)
		state := mappingPromptText(window)

		for _, set := range []string{"auto", "ai"} {
			var placements map[string]string
			if set == "auto" {
				placements = auto[layout.ID]
			} else {
				placements = aiSet[layout.ID]
			}
			checkName := "mapping_check_" + set

			questions := map[string]jevmeasure.Question{}
			for _, f := range mappingFields {
				if h := placements[f]; h != "" {
					questions[f] = jpQuestion(f, h)
				}
			}

			var resp jevmeasure.Response
			var elapsed time.Duration
			var callErr error
			var callID string
			if len(questions) > 0 {
				callID = layout.ID + "#" + set
				ctx, cancel := context.WithTimeout(t.Context(), jpCallTimeout)
				resp, elapsed, callErr = client.Ask(ctx, state, questions)
				cancel()
			}

			for _, f := range mappingFields {
				accepted := jpAccepted(layout, f)
				header := placements[f]
				label, reason := jpLabel(accepted, header)
				switch {
				case label == "not-asked":
					outcomes = append(outcomes, jevmeasure.Outcome{
						Check: checkName, DocumentID: layout.ID, Field: f, Label: "not-asked",
						Reason: reason, ProbabilityKind: jevmeasure.KindNoul, Reader: jpReader,
					})
				case callErr != nil:
					outcomes = append(outcomes, jevmeasure.Outcome{
						Check: checkName, DocumentID: layout.ID, Field: f, Label: "not-asked",
						Failed: true, Reason: callErr.Error(), ProbabilityKind: jevmeasure.KindNoul,
						Elapsed: elapsed, CallID: callID, Reader: jpReader,
					})
				default:
					ans := resp.Answers[f]
					outcomes = append(outcomes, jevmeasure.Outcome{
						Check: checkName, DocumentID: layout.ID, Field: f, Label: label,
						ProbabilityKind: jevmeasure.KindNoul, Probability: ans.Noul,
						Elapsed: elapsed, Usage: resp.Usage, CallID: callID, Reader: jpReader,
					})
				}
			}
		}
	}
	return outcomes
}

// --- the gate, the ledger, and the live entry point -----------------------------------------

var jpGateLogs []string

func jpResetGateLogs() { jpGateLogs = nil }

func jpGateLog(t *testing.T, msg string) {
	t.Helper()
	jpGateLogs = append(jpGateLogs, msg)
	t.Log(msg)
}

func jpGateReason(key, out string) string {
	switch {
	case key == "" && out == "":
		return "TYPESAFE_API_KEY and JEV_OUT unset: no client built, no call"
	case key == "":
		return "TYPESAFE_API_KEY unset: no client built, no call"
	default:
		return "JEV_OUT unset: no client built, no call"
	}
}

// jpWrite is this file's only write seam: temp-file-plus-rename, so a crash leaves the previous
// artifact rather than a torn one (§4.1).
func jpWrite(t *testing.T, dir, name string, b []byte) {
	t.Helper()
	tmp := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		t.Fatalf("rename %s to %s: %v", tmp, name, err)
	}
}

// jpReadLedger reads $JEV_OUT/jev-outcomes.json -- an absent file answers nil, not an error
// (§4.1): the first binary to run has no prior ledger to merge.
func jpReadLedger(t *testing.T, dir string) []jevmeasure.Outcome {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "jev-outcomes.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read jev-outcomes.json: %v", err)
	}
	var outcomes []jevmeasure.Outcome
	if err := json.Unmarshal(b, &outcomes); err != nil {
		t.Fatalf("unmarshal jev-outcomes.json: %v", err)
	}
	return outcomes
}

// jpGatedRun reads TYPESAFE_API_KEY and JEV_OUT; either empty logs one line naming which and
// returns false without building a client -- never t.Skip (rls-test-gate.sh runs this package
// unfiltered and exits 1 on a skip). Both set: runs the mapping walk, merges it into the
// $JEV_OUT/jev-outcomes.json ledger (D-1) alongside whatever the value/document-type binary
// already wrote, and renders jev-report.md / jev-report.json over the merged set.
func jpGatedRun(t *testing.T, baseURL string) bool {
	t.Helper()
	key := os.Getenv("TYPESAFE_API_KEY")
	out := os.Getenv("JEV_OUT")
	if key == "" || out == "" {
		jpGateLog(t, jpGateReason(key, out))
		return false
	}
	t.Logf("live run: model %s", jpResolveModel())

	fresh := jpWalk(t, baseURL, out)
	prior := jpReadLedger(t, out)
	all := jevmeasure.MergeOutcomes(prior, fresh)

	ledgerJSON, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		t.Fatalf("marshal jev-outcomes.json: %v", err)
	}
	jpWrite(t, out, "jev-outcomes.json", ledgerJSON)

	md, reportJSON, err := jevmeasure.Render(all, jpResolvePricing())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	jpWrite(t, out, "jev-report.md", md)
	jpWrite(t, out, "jev-report.json", reportJSON)
	return true
}

// TestJevMapping_Measure is the mapping check's live-run entry point. CI sets neither env var,
// so this always declines there.
func TestJevMapping_Measure(t *testing.T) {
	jpGatedRun(t, jevmeasure.Endpoint)
}

// --- test fixtures ----------------------------------------------------------------------------

func jpKeyPtr(s string) *string { return &s }

// jpFullKey builds an eleven-field Key map, [null] for every field not named in overrides.
func jpFullKey(overrides map[string][]*string) map[string][]*string {
	key := make(map[string][]*string, len(mappingFields))
	for _, f := range mappingFields {
		key[f] = []*string{nil}
	}
	for f, v := range overrides {
		key[f] = v
	}
	return key
}

func jpWriteLayouts(t *testing.T, dir string, layouts []jpLayout) {
	t.Helper()
	b, err := json.Marshal(layouts)
	if err != nil {
		t.Fatalf("marshal layouts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "layouts.json"), b, 0o644); err != nil {
		t.Fatalf("write layouts.json: %v", err)
	}
}

func jpWriteCSV(t *testing.T, dir, id, content string) {
	t.Helper()
	csvDir := filepath.Join(dir, "csv")
	if err := os.MkdirAll(csvDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", csvDir, err)
	}
	if err := os.WriteFile(filepath.Join(csvDir, id+".csv"), []byte(content), 0o644); err != nil {
		t.Fatalf("write csv for %s: %v", id, err)
	}
}

// jpWriteAutoPlacements writes $JEV_OUT/auto_placements.json, filling every layout's entry out
// to all eleven canonical fields ("" for anything the caller did not set) -- CHECK-01-06's own
// shape (AC-2: every layout gets a full eleven-key mapping).
func jpWriteAutoPlacements(t *testing.T, dir string, placements map[string]map[string]string) {
	t.Helper()
	full := make(map[string]map[string]string, len(placements))
	for id, m := range placements {
		row := make(map[string]string, len(mappingFields))
		for _, f := range mappingFields {
			row[f] = m[f]
		}
		full[id] = row
	}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("marshal auto_placements: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auto_placements.json"), b, 0o644); err != nil {
		t.Fatalf("write auto_placements.json: %v", err)
	}
}

func jpPlainCSVText() string {
	return "Invoice No,Total\nINV-1,100\n"
}

// jpSeedOneLayoutFixture writes a one-layout corpus under dir with a single AUTO placement
// (invoice_number), for tests that only need a gated run to make exactly one call.
func jpSeedOneLayoutFixture(t *testing.T, dir, id string) {
	t.Helper()
	layouts := []jpLayout{{
		ID: id, Columns: []string{"Invoice No", "Total"},
		Key: jpFullKey(map[string][]*string{"invoice_number": {jpKeyPtr("Invoice No")}}),
	}}
	jpWriteLayouts(t, dir, layouts)
	jpWriteCSV(t, dir, id, jpPlainCSVText())
	jpWriteAutoPlacements(t, dir, map[string]map[string]string{id: {"invoice_number": "Invoice No"}})
}

// jpRequest is one decoded call jpFake received.
type jpRequest struct {
	State     string
	Questions map[string]any
}

// jpFake starts an httptest server decoding every request into map[string]any -- never a typed
// wireRequest mirror (endtoend's R-16 precedent: a typed mirror breaks silently on a field
// rename in jevmeasure). t.Errorf, not t.Fatalf: FailNow is unsafe off the handler's goroutine.
func jpFake(t *testing.T, answer func(state string, questions map[string]any) (body string, status int)) (*httptest.Server, *[]jpRequest) {
	t.Helper()
	recorded := &[]jpRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Errorf("jpFake: decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		state, _ := raw["state"].(string)
		questions, _ := raw["questions"].(map[string]any)
		*recorded = append(*recorded, jpRequest{State: state, Questions: questions})
		body, status := answer(state, questions)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, recorded
}

// jpNoulBody answers every question in questions with a noul of val.
func jpNoulBody(questions map[string]any, val string) string {
	answers := make(map[string]any, len(questions))
	for id := range questions {
		answers[id] = map[string]any{"type": jevmeasure.QuestionTypeNoul, "noul": json.Number(val)}
	}
	b, _ := json.Marshal(map[string]any{"answers": answers, "usage": map[string]any{}})
	return string(b)
}

// jpNoulBodyWithUsage answers like jpNoulBody and reports token counts, so the report has
// tokens for an operator-supplied rate to price.
func jpNoulBodyWithUsage(questions map[string]any, val string, inTokens, outTokens int) string {
	answers := make(map[string]any, len(questions))
	for id := range questions {
		answers[id] = map[string]any{"type": jevmeasure.QuestionTypeNoul, "noul": json.Number(val)}
	}
	b, _ := json.Marshal(map[string]any{
		"answers": answers,
		"usage":   map[string]any{"input_tokens": inTokens, "output_tokens": outTokens},
	})
	return string(b)
}

// jpSection returns just the "## <check>" block of a rendered report.
func jpSection(t *testing.T, md, check string) string {
	t.Helper()
	head := "## " + check + "\n"
	i := strings.Index(md, head)
	if i == -1 {
		t.Fatalf("report has no %q section", check)
	}
	rest := md[i+len(head):]
	if j := strings.Index(rest, "\n## "); j != -1 {
		return rest[:j]
	}
	return rest
}

var jpAskedRe = regexp.MustCompile(`questions asked: (\d+)`)

func jpAskedCount(t *testing.T, md, check string) int {
	t.Helper()
	m := jpAskedRe.FindStringSubmatch(jpSection(t, md, check))
	if m == nil {
		t.Fatalf("no %q line in %q section", "questions asked:", check)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parse asked count %q: %v", m[1], err)
	}
	return n
}

// --- AC-1 ---------------------------------------------------------------------------------

// Row 1. jpGatedRun opens no socket and returns false while either env var is unset, logging
// exactly one line naming which; the positive control is essential -- without it a gate that
// always declines passes every negative leg.
func TestJevMapping_UnsetKeyLogsAndReturns(t *testing.T) {
	negative := func(t *testing.T, key, out string, wantKeyNamed, wantOutNamed bool) {
		t.Helper()
		t.Setenv("TYPESAFE_API_KEY", key)
		t.Setenv("JEV_OUT", out)
		jpResetGateLogs()
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			t.Errorf("the gate opened a socket")
		}))
		defer srv.Close()
		if got := jpGatedRun(t, srv.URL); got {
			t.Errorf("jpGatedRun(key=%q, out=%q) = true, want false", key, out)
		}
		if calls != 0 {
			t.Errorf("jpGatedRun opened %d call(s), want 0", calls)
		}
		if len(jpGateLogs) != 1 {
			t.Fatalf("jpGatedRun logged %d line(s), want exactly 1", len(jpGateLogs))
		}
		msg := jpGateLogs[0]
		if strings.Contains(msg, "TYPESAFE_API_KEY") != wantKeyNamed {
			t.Errorf("logged %q; TYPESAFE_API_KEY named = %v, want %v", msg, strings.Contains(msg, "TYPESAFE_API_KEY"), wantKeyNamed)
		}
		if strings.Contains(msg, "JEV_OUT") != wantOutNamed {
			t.Errorf("logged %q; JEV_OUT named = %v, want %v", msg, strings.Contains(msg, "JEV_OUT"), wantOutNamed)
		}
	}
	t.Run("neither set", func(t *testing.T) { negative(t, "", "", true, true) })
	t.Run("key only", func(t *testing.T) { negative(t, "sk-test", "", false, true) })
	t.Run("out only", func(t *testing.T) { negative(t, "", t.TempDir(), true, false) })

	t.Run("both set: positive control", func(t *testing.T) {
		dir := t.TempDir()
		jpSeedOneLayoutFixture(t, dir, "plain_01")
		t.Setenv("TYPESAFE_API_KEY", "sk-test")
		t.Setenv("JEV_OUT", dir)
		jpResetGateLogs()
		srv, _ := jpFake(t, func(state string, questions map[string]any) (string, int) {
			return jpNoulBody(questions, "0.90"), http.StatusOK
		})
		if got := jpGatedRun(t, srv.URL); !got {
			t.Errorf("jpGatedRun with both env vars set = false, want true")
		}
		if len(jpGateLogs) != 0 {
			t.Errorf("jpGatedRun logged %d line(s), want 0 on the positive control", len(jpGateLogs))
		}
		if _, err := os.Stat(filepath.Join(dir, "jev-report.md")); err != nil {
			t.Errorf("jev-report.md not written: %v", err)
		}
	})
}

// --- AC-2 ---------------------------------------------------------------------------------

// Row 2. Absolute leg: a recorded answer claiming one header for two fields must drop both
// (guardPlacements' own rule 5), matched against guardPlacements called directly. Positive
// control: moving one field to a real, distinct header keeps both -- without it a walk that
// drops everything would also pass the absolute leg.
//
// Every inline answer fixture below uses only canonical field names plus header_row: the two
// wire keys the suggest-mapping scope guard forbids outside suggest.go are not needed by
// anything here, so they are never spelled in this file.
func TestJevMapping_UsesTheShippedGuard(t *testing.T) {
	dir := t.TempDir()
	layouts := []jpLayout{{ID: "conflict_01", Columns: []string{"Amount", "Description"}, Key: jpFullKey(nil)}}
	jpWriteLayouts(t, dir, layouts)
	jpWriteCSV(t, dir, "conflict_01", "Amount,Description\n100,Widget\n")
	answersPath := filepath.Join(dir, "mapping_answers.jsonl")

	conflicting := `{"layout":"conflict_01","answer":{"total":"Amount","subtotal":"Amount","header_row":1},"model":"m"}` + "\n"
	if err := os.WriteFile(answersPath, []byte(conflicting), 0o644); err != nil {
		t.Fatalf("write mapping_answers.jsonl: %v", err)
	}
	got := jpAISet(t, dir, layouts)
	if h := got["conflict_01"]["total"]; h != "" {
		t.Errorf("total = %q, want empty -- a header two fields claimed must be dropped", h)
	}
	if h := got["conflict_01"]["subtotal"]; h != "" {
		t.Errorf("subtotal = %q, want empty -- same rule", h)
	}

	distinct := `{"layout":"conflict_01","answer":{"total":"Amount","subtotal":"Description","header_row":1},"model":"m"}` + "\n"
	if err := os.WriteFile(answersPath, []byte(distinct), 0o644); err != nil {
		t.Fatalf("write mapping_answers.jsonl: %v", err)
	}
	got2 := jpAISet(t, dir, layouts)
	header := []string{"Amount", "Description"}
	want := guardPlacements(map[string]any{"total": "Amount", "subtotal": "Description", "header_row": json.Number("1")}, header)
	if got2["conflict_01"]["total"] != want["total"] || got2["conflict_01"]["subtotal"] != want["subtotal"] {
		t.Errorf("jpAISet = %v, want it to match guardPlacements called directly: %v", got2["conflict_01"], want)
	}
	if got2["conflict_01"]["total"] != "Amount" || got2["conflict_01"]["subtotal"] != "Description" {
		t.Errorf("got2 = %v, want both fields placed once no header is claimed twice", got2["conflict_01"])
	}
}

// Row 3, respecified (J-3/D-6): the mapping walk reaches no client. AST-scan this file itself:
// no import of internal/platform/ai, no call to askMapping or ai.FromEnv, no reference to
// MappingSuggester. Control: guardPlacements and guardHeaderRow ARE referenced (the walk must
// still use the shipped guards). Floor: a truncated parse must not report clean. The only
// internal/platform/jev symbol allowed is jev.Model, and it must be seen.
func TestJevMapping_TheWalkNamesNoClient(t *testing.T) {
	src, err := os.ReadFile("jev_mapping_test.go")
	if err != nil {
		t.Fatalf("read jev_mapping_test.go: %v", err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "jev_mapping_test.go", src, 0)
	if err != nil {
		t.Fatalf("parse jev_mapping_test.go: %v", err)
	}
	if len(f.Decls) < 10 {
		t.Fatalf("parsed %d top-level decl(s), want at least 10 -- a truncated parse would report clean vacuously", len(f.Decls))
	}
	jevName := ""
	for _, imp := range f.Imports {
		switch strings.Trim(imp.Path.Value, `"`) {
		case "github.com/SimonOsipov/invoice-os/internal/platform/ai":
			t.Errorf("jev_mapping_test.go imports internal/platform/ai -- the walk must reach no live client")
		case "github.com/SimonOsipov/invoice-os/internal/platform/jev":
			jevName = "jev"
			if imp.Name != nil {
				jevName = imp.Name.Name
			}
		}
	}
	if jevName == "" || jevName == "." || jevName == "_" {
		t.Fatalf("internal/platform/jev import name = %q, want a named import -- jev.Model cannot be seen as a selector", jevName)
	}

	var sawAskMapping, sawFromEnv, sawSuggester, sawGuardPlacements, sawGuardHeaderRow, sawJevModel bool
	var otherJev []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == jevName {
				if x.Sel.Name == "Model" {
					sawJevModel = true
				} else {
					otherJev = append(otherJev, jevName+"."+x.Sel.Name)
				}
			}
		case *ast.CallExpr:
			switch fn := x.Fun.(type) {
			case *ast.Ident:
				switch fn.Name {
				case "askMapping":
					sawAskMapping = true
				case "guardPlacements":
					sawGuardPlacements = true
				case "guardHeaderRow":
					sawGuardHeaderRow = true
				}
			case *ast.SelectorExpr:
				if id, ok := fn.X.(*ast.Ident); ok && id.Name == "ai" && fn.Sel.Name == "FromEnv" {
					sawFromEnv = true
				}
			}
		case *ast.Ident:
			if x.Name == "MappingSuggester" {
				sawSuggester = true
			}
		}
		return true
	})
	if sawAskMapping {
		t.Errorf("jev_mapping_test.go calls askMapping -- the walk must replay recorded answers, never call the suggester")
	}
	if sawFromEnv {
		t.Errorf("jev_mapping_test.go calls ai.FromEnv -- the walk must reach no live client")
	}
	if sawSuggester {
		t.Errorf("jev_mapping_test.go references MappingSuggester -- the walk takes no suggester parameter")
	}
	if !sawGuardPlacements {
		t.Fatalf("control needle guardPlacements not found -- the scan itself is broken")
	}
	if !sawGuardHeaderRow {
		t.Fatalf("control needle guardHeaderRow not found -- the scan itself is broken")
	}
	if !sawJevModel {
		t.Fatalf("control needle jev.Model not found -- the scan itself is broken")
	}
	if len(otherJev) > 0 {
		t.Errorf("jev_mapping_test.go names %v -- only jev.Model may appear; the walk's calls go through jevmeasure", otherJev)
	}
}

// --- AC-3 ---------------------------------------------------------------------------------

// Row 4/A56. The single most likely silent defect: AUTO never places line_description and no
// recorded answer for a multi-header layout exists in this repo, so no corpus walk can ever
// reach the accepted[0] bug (§1.3) -- only this synthetic two-member-key unit test can.
// tools/aimodeltest/csvgen_test.go's TestCSVGen_NoHeaderIsClaimedTwice already pins the real
// generator's two multi-header key entries (sw_zoho, sw_quickbooks_import) as an exact set; that
// corpus-level pin is not duplicated here.
func TestJevMapping_APlacementMatchingTheKeyIsRight(t *testing.T) {
	cases := []struct {
		name     string
		accepted []string
		header   string
		want     string
	}{
		{"plain case", []string{"Grand Total"}, "Grand Total", "right"},
		{"first member", []string{"Item Name", "Item Desc"}, "Item Name", "right"},
		{"A56: second member", []string{"Item Name", "Item Desc"}, "Item Desc", "right"},
		{"a third alternative still fails", []string{"Item Name", "Item Desc"}, "Item Total", "wrong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, _ := jpLabel(tc.accepted, tc.header)
			if label != tc.want {
				t.Errorf("jpLabel(%v, %q) = %q, want %q", tc.accepted, tc.header, label, tc.want)
			}
		})
	}
}

// Row 5. Table leg plus a walk-level leg: over a real AUTO run, the asked count is exactly the
// number of non-null placements, and the two not-asked reasons are tallied into separate
// buckets rather than one bucket (which would pass AC-3's literal words and destroy the only
// recall figure the mapping check produces).
func TestJevMapping_ANullKeyAndNoPlacementIsNotAsked(t *testing.T) {
	cases := []struct {
		accepted   []string
		header     string
		wantLabel  string
		wantReason string
	}{
		{nil, "", "not-asked", jpReasonNoKeyNoPlacement},
		{[]string{"Tax Amount"}, "", "not-asked", jpReasonKeyedButUnplaced},
		{[]string{"Tax Amount"}, "Tax Amount", "right", ""},
	}
	for _, tc := range cases {
		label, reason := jpLabel(tc.accepted, tc.header)
		if label != tc.wantLabel || reason != tc.wantReason {
			t.Errorf("jpLabel(%v, %q) = (%q,%q), want (%q,%q)", tc.accepted, tc.header, label, reason, tc.wantLabel, tc.wantReason)
		}
	}

	dir := t.TempDir()
	layouts := []jpLayout{{
		ID: "mix_01", Columns: []string{"Invoice No"},
		Key: jpFullKey(map[string][]*string{
			"invoice_number": {jpKeyPtr("Invoice No")}, // placed -> right
			"vat":            {jpKeyPtr("VAT Amount")}, // keyed, unplaced
		}),
	}}
	jpWriteLayouts(t, dir, layouts)
	jpWriteCSV(t, dir, "mix_01", "Invoice No\nINV-1\n")
	jpWriteAutoPlacements(t, dir, map[string]map[string]string{"mix_01": {"invoice_number": "Invoice No"}})

	srv, _ := jpFake(t, func(state string, questions map[string]any) (string, int) {
		return jpNoulBody(questions, "0.90"), http.StatusOK
	})
	outcomes := jpWalk(t, srv.URL, dir)
	md, _, err := jevmeasure.Render(outcomes, jevmeasure.Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	sec := jpSection(t, string(md), "mapping_check_auto")
	if !strings.Contains(sec, "questions asked: 1") {
		t.Errorf("mapping_check_auto section must show asked: 1 (only invoice_number placed):\n%s", sec)
	}
	if !strings.Contains(sec, jpReasonNoKeyNoPlacement) || !strings.Contains(sec, jpReasonKeyedButUnplaced) {
		t.Errorf("mapping_check_auto section must tally BOTH not-asked reasons separately:\n%s", sec)
	}
}

// --- AC-4 ---------------------------------------------------------------------------------

// Row 6. Two populations: a keyed field placed on a different header, and an unkeyed field
// placed anyway -- both wrong. The second is the easy silent hole: checking len(accepted)==0
// before the header=="" test would read it as not-asked and drop it from the denominator.
func TestJevMapping_APlacementTheKeyDoesNotNameIsWrong(t *testing.T) {
	cases := []struct {
		name     string
		accepted []string
		header   string
	}{
		{"keyed field, different header", []string{"Customer Tax ID"}, "Our TIN"},
		{"unkeyed field, placed anyway", nil, "Outstanding"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, reason := jpLabel(tc.accepted, tc.header)
			if label != "wrong" {
				t.Errorf("jpLabel(%v, %q) = %q, want wrong", tc.accepted, tc.header, label)
			}
			if reason != "" {
				t.Errorf("a wrong label must carry an empty reason, got %q", reason)
			}
		})
	}
}

// --- AC-5 ---------------------------------------------------------------------------------

// Row 7. mapping_check_auto and mapping_check_ai render as two sections, each with its own N;
// the JSON twin's crisp leg is what a merged "mapping_check" with Asked=8 would fail.
func TestJevMapping_AutoAndAIAreReportedSeparately(t *testing.T) {
	num := func(s string) *json.Number { n := json.Number(s); return &n }
	var outcomes []jevmeasure.Outcome
	for i := 0; i < 3; i++ {
		outcomes = append(outcomes, jevmeasure.Outcome{
			Check: "mapping_check_auto", DocumentID: fmt.Sprintf("l%d", i), Field: "total",
			Label: "right", ProbabilityKind: jevmeasure.KindNoul, Probability: num("0.90"),
		})
	}
	for i := 0; i < 5; i++ {
		outcomes = append(outcomes, jevmeasure.Outcome{
			Check: "mapping_check_ai", DocumentID: fmt.Sprintf("m%d", i), Field: "total",
			Label: "right", ProbabilityKind: jevmeasure.KindNoul, Probability: num("0.90"),
		})
	}
	md, twin, err := jevmeasure.Render(outcomes, jevmeasure.Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := string(md)
	if strings.Count(body, "## mapping_check_auto\n") != 1 || strings.Count(body, "## mapping_check_ai\n") != 1 {
		t.Fatalf("report must have exactly one heading per mapping check:\n%s", body)
	}
	if !strings.Contains(jpSection(t, body, "mapping_check_auto"), "questions asked: 3") {
		t.Errorf("mapping_check_auto must show asked: 3")
	}
	if !strings.Contains(jpSection(t, body, "mapping_check_ai"), "questions asked: 5") {
		t.Errorf("mapping_check_ai must show asked: 5")
	}
	if strings.Contains(body, "questions asked: 8") {
		t.Errorf("report shows a combined asked count of 8 -- the two sets must never merge into one rate")
	}

	var sections []struct {
		Check string
		Asked int
	}
	if err := json.Unmarshal(twin, &sections); err != nil {
		t.Fatalf("JSON twin does not parse: %v", err)
	}
	if len(sections) != 2 {
		t.Fatalf("JSON twin has %d section(s), want 2", len(sections))
	}
	got := map[string]int{}
	for _, s := range sections {
		got[s.Check] = s.Asked
		if s.Asked == 8 {
			t.Errorf("JSON twin has a merged section with Asked=8: %+v", s)
		}
	}
	if got["mapping_check_auto"] != 3 || got["mapping_check_ai"] != 5 {
		t.Errorf("JSON twin asked counts = %v, want auto=3 ai=5", got)
	}
	if !strings.Contains(body, "the two halves of ONE check") {
		t.Errorf("mapping.two_halves caveat must render when both mapping sections are present")
	}
}

// --- D3 -----------------------------------------------------------------------------------

// Row 15. busy_01 has 3 AUTO placements and 5 AI placements -> exactly 2 requests, one carrying
// 3 questions, the other 5. quiet_01 has an all-null AUTO entry and 2 AI placements -> exactly 1
// further request, the AI one -- without this leg an implementation that always calls once per
// layout per set burns 15 pointless calls on the real 48-layout corpus.
func TestJevMapping_OneLayoutSetProducesOneCall(t *testing.T) {
	dir := t.TempDir()
	layouts := []jpLayout{
		{
			ID: "busy_01", Columns: []string{"A", "B", "C", "D", "E", "F", "G"},
			Key: jpFullKey(map[string][]*string{
				"invoice_number": {jpKeyPtr("A")}, "total": {jpKeyPtr("B")}, "vat": {jpKeyPtr("C")},
				"buyer_name": {jpKeyPtr("D")}, "currency": {jpKeyPtr("E")}, "subtotal": {jpKeyPtr("F")},
				"issue_date": {jpKeyPtr("G")}, "line_description": {jpKeyPtr("A")},
			}),
		},
		{
			ID: "quiet_01", Columns: []string{"H", "I"},
			Key: jpFullKey(map[string][]*string{"invoice_number": {jpKeyPtr("H")}, "total": {jpKeyPtr("I")}}),
		},
	}
	jpWriteLayouts(t, dir, layouts)
	jpWriteCSV(t, dir, "busy_01", "A,B,C,D,E,F,G\n1,2,3,4,5,6,7\n")
	jpWriteCSV(t, dir, "quiet_01", "H,I\n1,2\n")
	jpWriteAutoPlacements(t, dir, map[string]map[string]string{
		"busy_01": {"invoice_number": "A", "total": "B", "vat": "C"},
		// quiet_01 deliberately absent: an all-null AUTO entry.
	})
	answers := strings.Join([]string{
		`{"layout":"busy_01","answer":{"buyer_name":"D","currency":"E","subtotal":"F","issue_date":"G","line_description":"A","header_row":1},"model":"m"}`,
		`{"layout":"quiet_01","answer":{"invoice_number":"H","total":"I","header_row":1},"model":"m"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "mapping_answers.jsonl"), []byte(answers), 0o644); err != nil {
		t.Fatalf("write mapping_answers.jsonl: %v", err)
	}

	srv, recorded := jpFake(t, func(state string, questions map[string]any) (string, int) {
		return jpNoulBody(questions, "0.90"), http.StatusOK
	})
	outcomes := jpWalk(t, srv.URL, dir)

	if len(*recorded) != 3 {
		t.Fatalf("recorded %d request(s), want 3 (busy_01 AUTO+AI, quiet_01 AI)", len(*recorded))
	}
	var busyAuto, busyAI, quietAI *jpRequest
	for i := range *recorded {
		req := &(*recorded)[i]
		switch len(req.Questions) {
		case 3:
			busyAuto = req
		case 5:
			busyAI = req
		case 2:
			quietAI = req
		}
	}
	if busyAuto == nil || busyAI == nil || quietAI == nil {
		t.Fatalf("did not find one request each with 3, 5, and 2 question(s); got %d/%d/%d question counts across %d requests",
			len(*recorded), len(*recorded), len(*recorded), len(*recorded))
	}
	for _, f := range []string{"invoice_number", "total", "vat"} {
		if _, ok := busyAuto.Questions[f]; !ok {
			t.Errorf("AUTO request missing field %q", f)
		}
	}
	// §3.5: state is production's own CSV window -- the bytes the model judged the mapping
	// against -- never the whole file, never layouts.json's rows, never empty.
	if want := mappingPromptText(jpWindow(t, dir, "busy_01")); busyAuto.State != want {
		t.Errorf("AUTO request state = %q, want the CSV window production sends: %q", busyAuto.State, want)
	}
	if !strings.Contains(busyAuto.State, "Row 1: A,B,C,D,E,F,G") || !strings.Contains(busyAuto.State, "Row 2: 1,2,3,4,5,6,7") {
		t.Errorf("state does not carry the CSV's own header and sample rows: %q", busyAuto.State)
	}
	for _, f := range []string{"buyer_name", "currency", "subtotal", "issue_date", "line_description"} {
		if _, ok := busyAI.Questions[f]; !ok {
			t.Errorf("busy_01 AI request missing field %q", f)
		}
	}
	for _, f := range []string{"invoice_number", "total"} {
		if _, ok := quietAI.Questions[f]; !ok {
			t.Errorf("quiet_01 AI request missing field %q", f)
		}
	}

	callIDs := map[string]string{} // "check#doc" -> the one CallID all its asked rows share
	for _, o := range outcomes {
		if o.Label == "not-asked" {
			continue
		}
		key := o.Check + "#" + o.DocumentID
		if prev, ok := callIDs[key]; ok && prev != o.CallID {
			t.Errorf("%s: outcomes for one call carry different CallIDs %q and %q", key, prev, o.CallID)
		}
		callIDs[key] = o.CallID
		if o.CallID == "" {
			t.Errorf("%s: asked outcome carries an empty CallID", key)
		}
	}
	if callIDs["mapping_check_auto#busy_01"] == callIDs["mapping_check_ai#busy_01"] {
		t.Errorf("busy_01's AUTO and AI calls share one CallID: %q", callIDs["mapping_check_auto#busy_01"])
	}
}

// --- AC-7: the reader, on the mapping half ------------------------------------------------

// AC-7. Row 10 proves jevmeasure RENDERS a reader per document; nothing proved the mapping walk
// SETS one. Emptying jpReader left the whole package green, so this is that clause's only
// oracle: every mapping outcome names the csv reader, and the rendered Provenance carries one
// row per (check, document).
func TestJevMapping_TheReportNamesTheCSVReaderForEveryMappingDocument(t *testing.T) {
	dir := t.TempDir()
	jpSeedOneLayoutFixture(t, dir, "plain_01")
	srv, _ := jpFake(t, func(state string, questions map[string]any) (string, int) {
		return jpNoulBody(questions, "0.90"), http.StatusOK
	})
	outcomes := jpWalk(t, srv.URL, dir)
	if len(outcomes) == 0 {
		t.Fatalf("the walk produced no outcomes")
	}
	for _, o := range outcomes {
		if o.Reader != jpReader {
			t.Errorf("%s/%s/%s carries Reader %q, want %q -- the mapping documents are spreadsheets", o.Check, o.DocumentID, o.Field, o.Reader, jpReader)
		}
		if o.ProbabilityKind != jevmeasure.KindNoul {
			t.Errorf("%s/%s/%s carries ProbabilityKind %q, want %q", o.Check, o.DocumentID, o.Field, o.ProbabilityKind, jevmeasure.KindNoul)
		}
	}

	md, _, err := jevmeasure.Render(outcomes, jevmeasure.Pricing{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	prov := jpSection(t, string(md), "Provenance")
	for _, want := range []string{
		"| mapping_check_auto | plain_01 | " + jpReader + " |",
		"| mapping_check_ai | plain_01 | " + jpReader + " |",
	} {
		if !strings.Contains(prov, want) {
			t.Errorf("Provenance is missing the row %q:\n%s", want, prov)
		}
	}
}

// --- new: the re-decode (D-2, highest-value addition) -------------------------------------

// Row 17. The defect this catches ships green and costs the whole titled half of the Flash
// Lite measurement: implemented literally (no re-decode), guardPlacements runs against the
// title line and rule 4 drops every claim.
func TestJevMapping_ATitledLayoutIsRedecodedAtTheAnsweredHeaderRow(t *testing.T) {
	dir := t.TempDir()
	layouts := []jpLayout{
		{
			ID: "title_01", Columns: []string{"Inv No", "Total"},
			Key: jpFullKey(map[string][]*string{"invoice_number": {jpKeyPtr("Inv No")}, "total": {jpKeyPtr("Total")}}),
		},
		{
			ID: "plain_01", Columns: []string{"Inv No", "Total"},
			Key: jpFullKey(map[string][]*string{"invoice_number": {jpKeyPtr("Inv No")}, "total": {jpKeyPtr("Total")}}),
		},
		{
			ID: "overshoot_01", Columns: []string{"Inv No", "Total"},
			Key: jpFullKey(map[string][]*string{"invoice_number": {jpKeyPtr("Inv No")}, "total": {jpKeyPtr("Total")}}),
		},
		{
			ID: "blankrow_01", Columns: []string{"Inv No", "Total"},
			Key: jpFullKey(map[string][]*string{"invoice_number": {jpKeyPtr("Inv No")}, "total": {jpKeyPtr("Total")}}),
		},
	}
	jpWriteLayouts(t, dir, layouts)
	// title_01: three title lines, a blank line, then the header at physical row 5 -- the
	// titled layouts' real shape.
	jpWriteCSV(t, dir, "title_01", "Title Line 1\nTitle Line 2\nTitle Line 3\n\nInv No,Total\nINV-1,100\nINV-2,200\n")
	jpWriteCSV(t, dir, "plain_01", "Inv No,Total\nINV-1,100\n")
	jpWriteCSV(t, dir, "overshoot_01", "Inv No,Total\nINV-1,100\n")
	// blankrow_01's physical row 2 is blank, and encoding/csv drops it, so the window is two
	// rows long and a header_row of 2 passes guardHeaderRow untouched. DecodeFrom then answers
	// a BLANK header, and only the len(h) > 0 fallback puts the row-1 header back.
	jpWriteCSV(t, dir, "blankrow_01", "Inv No,Total\n\nINV-1,100\n")

	answers := strings.Join([]string{
		`{"layout":"title_01","answer":{"invoice_number":"Inv No","total":"Total","header_row":5},"model":"m"}`,
		// Control: header_row 1 must still work through the hdr1 path.
		`{"layout":"plain_01","answer":{"invoice_number":"Inv No","total":"Total","header_row":1},"model":"m"}`,
		// Clamp leg: header_row points past the window -> guardHeaderRow answers row 1, so the
		// re-decode is never entered at all.
		`{"layout":"overshoot_01","answer":{"invoice_number":"Inv No","total":"Total","header_row":99},"model":"m"}`,
		// Fallback leg: header_row 2 IS inside the window, so the re-decode runs and comes back
		// blank; production falls back to row 1 rather than failing the layout, and so must this.
		`{"layout":"blankrow_01","answer":{"invoice_number":"Inv No","total":"Total","header_row":2},"model":"m"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "mapping_answers.jsonl"), []byte(answers), 0o644); err != nil {
		t.Fatalf("write mapping_answers.jsonl: %v", err)
	}

	got := jpAISet(t, dir, layouts)
	for _, id := range []string{"title_01", "plain_01", "overshoot_01", "blankrow_01"} {
		if got[id]["invoice_number"] != "Inv No" || got[id]["total"] != "Total" {
			t.Errorf("%s: got %v, want both fields placed at Inv No/Total", id, got[id])
		}
	}
}

// --- new: the all-null AUTO entry -----------------------------------------------------------

// Row 18. Fifteen of forty-eight real layouts produce an all-null AUTO entry; a reader that
// treats it as a corrupt artifact loses a third of the corpus silently. Both legs are needed:
// without the fatal leg, "tolerate anything" would also pass.
func TestJevMapping_AnAllNullAutoEntryPlacesNothingAndIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	layouts := []jpLayout{
		{ID: "empty_01", Columns: []string{"X"}, Key: jpFullKey(nil)},
		{ID: "full_01", Columns: []string{"Invoice No"}, Key: jpFullKey(map[string][]*string{"invoice_number": {jpKeyPtr("Invoice No")}})},
	}
	jpWriteLayouts(t, dir, layouts)
	jpWriteAutoPlacements(t, dir, map[string]map[string]string{
		"empty_01": {},
		"full_01":  {"invoice_number": "Invoice No"},
	})

	got := jpAutoSet(t, dir, layouts)
	if len(got) != 2 {
		t.Fatalf("jpAutoSet returned %d entr(ies), want 2", len(got))
	}
	for _, f := range mappingFields {
		if got["empty_01"][f] != "" {
			t.Errorf("empty_01[%q] = %q, want empty -- an all-null entry must place nothing", f, got["empty_01"][f])
		}
	}
	if got["full_01"]["invoice_number"] != "Invoice No" {
		t.Errorf("full_01[invoice_number] = %q, want %q", got["full_01"]["invoice_number"], "Invoice No")
	}

	// Control/floor: an entry missing one of the eleven keys is a real malformed artifact, not
	// an all-null one -- tested on the pure predicate jpAutoSet's fatal check is built from.
	if jpAutoEntryIsComplete(map[string]string{"invoice_number": "X"}) {
		t.Errorf("jpAutoEntryIsComplete must be false for an entry missing 10 of the 11 keys")
	}
	full := map[string]string{}
	for _, f := range mappingFields {
		full[f] = ""
	}
	if !jpAutoEntryIsComplete(full) {
		t.Errorf("jpAutoEntryIsComplete must be true for a complete, even if all-null, entry")
	}
}

// --- new: the 528-slot invariant (deliberately NOT pinning the 56) -------------------------

// Row 19. Over the REAL generated corpus: asked + not-asked == 528 (48 layouts x 11 fields) and
// documents == 48. Deliberately does not pin the 56 placements (a property of the shipped alias
// table, not the generator; CHECK-01-04 D-7 rules against a second corpus ratchet). Gated on
// JEV_OUT holding a real corpus -- CI's go job has neither python3-generated layouts.json nor a
// pnpm-built auto_placements.json on hand, so this declines (logs, returns) rather than skips;
// an operator populates JEV_OUT before the live run (§9).
func TestJevMapping_TheAutoSetCoversEveryLayoutAndEverySlot(t *testing.T) {
	dir := os.Getenv("JEV_OUT")
	if dir == "" {
		t.Log("JEV_OUT unset: no real corpus to measure, declining")
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "layouts.json")); err != nil {
		t.Logf("no layouts.json under JEV_OUT, declining: %v", err)
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "auto_placements.json")); err != nil {
		t.Logf("no auto_placements.json under JEV_OUT, declining: %v", err)
		return
	}

	layouts := jpLoadLayouts(t, dir)
	if len(layouts) != 48 {
		t.Fatalf("layouts.json holds %d layout(s), want 48", len(layouts))
	}
	auto := jpAutoSet(t, dir, layouts)

	// Count the slots the ARTIFACT carries, never len(layouts) x len(mappingFields): the loop
	// walks those two and would sum to 528 whatever jpAutoSet returned.
	asked, notAsked, slots := 0, 0, 0
	for _, l := range layouts {
		entry, ok := auto[l.ID]
		if !ok {
			t.Errorf("auto_placements.json carries no entry for layout %q", l.ID)
			continue
		}
		slots += len(entry)
		for _, h := range entry {
			if h != "" {
				asked++
			} else {
				notAsked++
			}
		}
	}
	if slots != 528 {
		t.Errorf("the AUTO artifact carries %d slot(s), want 528 (48 layouts x 11 fields)", slots)
	}
	if asked+notAsked != slots {
		t.Errorf("asked(%d) + not-asked(%d) = %d, want every one of the %d slots", asked, notAsked, asked+notAsked, slots)
	}
	if asked == 0 {
		t.Errorf("the AUTO artifact places nothing at all over 48 layouts -- a reader that answered empty strings reads exactly this way")
	}
}

// --- new: the merge ledger's filesystem integration (carried deliverable 5) ----------------

// Row 20. Write/merge/replace, exercised through jpGatedRun against a synthetic $JEV_OUT --
// MergeOutcomes' own pure legs (order-independence, replace-by-check, JSON round-trip) live in
// jevmeasure/report_test.go; this proves the read/merge/write seam around it.
func TestJevMapping_TheArtifactIsWrittenUnderJEVOUT(t *testing.T) {
	fake := func() (*httptest.Server, *[]jpRequest) {
		return jpFake(t, func(state string, questions map[string]any) (string, int) {
			return jpNoulBody(questions, "0.90"), http.StatusOK
		})
	}

	t.Run("write", func(t *testing.T) {
		dir := t.TempDir()
		jpSeedOneLayoutFixture(t, dir, "plain_01")
		t.Setenv("TYPESAFE_API_KEY", "sk-test")
		t.Setenv("JEV_OUT", dir)
		srv, _ := fake()
		if got := jpGatedRun(t, srv.URL); !got {
			t.Fatalf("jpGatedRun = false, want true")
		}
		for _, name := range []string{"jev-report.md", "jev-report.json", "jev-outcomes.json"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Errorf("%s not written: %v", name, err)
			}
		}
		md, err := os.ReadFile(filepath.Join(dir, "jev-report.md"))
		if err != nil {
			t.Fatalf("read jev-report.md: %v", err)
		}
		if !strings.HasPrefix(string(md), "# Jev Measurement Report") {
			t.Errorf("jev-report.md's first line is wrong: %q", string(md))
		}
		twin, err := os.ReadFile(filepath.Join(dir, "jev-report.json"))
		if err != nil {
			t.Fatalf("read jev-report.json: %v", err)
		}
		var arr []any
		if err := json.Unmarshal(twin, &arr); err != nil || len(arr) == 0 {
			t.Errorf("jev-report.json is not a non-empty array: err=%v arr=%v", err, arr)
		}
		if _, err := os.Stat(filepath.Join(dir, "report.md")); err == nil {
			t.Errorf("report.md exists -- the old name must be gone, or a live run leaves two artifacts")
		}
	})

	t.Run("merge", func(t *testing.T) {
		dir := t.TempDir()
		jpSeedOneLayoutFixture(t, dir, "plain_01")
		prior := []byte(`[{"check":"value_check","document_id":"v1","field":"amount","label":"right"}]` + "\n")
		if err := os.WriteFile(filepath.Join(dir, "jev-outcomes.json"), prior, 0o644); err != nil {
			t.Fatalf("pre-write jev-outcomes.json: %v", err)
		}
		t.Setenv("TYPESAFE_API_KEY", "sk-test")
		t.Setenv("JEV_OUT", dir)
		srv, _ := fake()
		if got := jpGatedRun(t, srv.URL); !got {
			t.Fatalf("jpGatedRun = false, want true")
		}
		md, err := os.ReadFile(filepath.Join(dir, "jev-report.md"))
		if err != nil {
			t.Fatalf("read jev-report.md: %v", err)
		}
		body := string(md)
		valueIdx := strings.Index(body, "## value_check")
		mappingIdx := strings.Index(body, "## mapping_check_auto")
		if valueIdx == -1 {
			t.Errorf("merged report lost the prior value_check section")
		}
		if mappingIdx == -1 {
			t.Errorf("merged report has no mapping_check_auto section")
		}
		if valueIdx != -1 && mappingIdx != -1 && valueIdx > mappingIdx {
			t.Errorf("value_check must render before mapping_check_auto (checkOrder), regardless of arrival order")
		}
	})

	t.Run("replace not append", func(t *testing.T) {
		dir := t.TempDir()
		jpSeedOneLayoutFixture(t, dir, "plain_01")
		t.Setenv("TYPESAFE_API_KEY", "sk-test")
		t.Setenv("JEV_OUT", dir)

		srv1, _ := fake()
		if got := jpGatedRun(t, srv1.URL); !got {
			t.Fatalf("first jpGatedRun = false, want true")
		}
		md1, err := os.ReadFile(filepath.Join(dir, "jev-report.md"))
		if err != nil {
			t.Fatalf("read jev-report.md (1): %v", err)
		}
		asked1 := jpAskedCount(t, string(md1), "mapping_check_auto")

		srv2, _ := fake()
		if got := jpGatedRun(t, srv2.URL); !got {
			t.Fatalf("second jpGatedRun = false, want true")
		}
		md2, err := os.ReadFile(filepath.Join(dir, "jev-report.md"))
		if err != nil {
			t.Fatalf("read jev-report.md (2): %v", err)
		}
		asked2 := jpAskedCount(t, string(md2), "mapping_check_auto")

		if asked1 != asked2 {
			t.Errorf("mapping_check_auto's asked count changed across a re-run: %d then %d -- rows must be replaced, not appended", asked1, asked2)
		}
	})
}

// --- new: jpLabel is pure and total ---------------------------------------------------------

// Row 22. jpLabel is a pure function four AC rows rest on, so purity is a design requirement
// (CHECK-01-04 D-5's ruling, same shape): every call in {[], [A], [A,B]} x {"", A, B, C} returns
// a valid label, a not-asked always carries a reason, a right/wrong never does, the function is
// deterministic, and it never mutates its input slice.
func TestJevMapping_LabelIsPureAndTotal(t *testing.T) {
	acceptedSets := [][]string{nil, {"A"}, {"A", "B"}}
	headers := []string{"", "A", "B", "C"}
	for _, accepted := range acceptedSets {
		for _, header := range headers {
			original := append([]string(nil), accepted...)
			label, reason := jpLabel(accepted, header)
			if label != "right" && label != "wrong" && label != "not-asked" {
				t.Errorf("jpLabel(%v, %q) label = %q, want one of right/wrong/not-asked", accepted, header, label)
			}
			if label == "not-asked" && reason == "" {
				t.Errorf("jpLabel(%v, %q) = not-asked with an empty reason", accepted, header)
			}
			if label != "not-asked" && reason != "" {
				t.Errorf("jpLabel(%v, %q) = %q with a non-empty reason %q", accepted, header, label, reason)
			}
			label2, reason2 := jpLabel(accepted, header)
			if label2 != label || reason2 != reason {
				t.Errorf("jpLabel(%v, %q) is not deterministic: (%q,%q) then (%q,%q)", accepted, header, label, reason, label2, reason2)
			}
			if !slices.Equal(accepted, original) {
				t.Errorf("jpLabel mutated its accepted slice: got %v, want %v", accepted, original)
			}
		}
	}
}

// jpCostLabel is the report's production-shaped cost line; jpCostFigure reads its dollar figure.
const jpCostLabel = "cost per 1,000 documents, production-shaped calls only"

var jpCostRe = regexp.MustCompile(`\$([0-9]+\.[0-9]{2}) \(price used`)

// jpCostLine returns the auto set's cost line -- the ai set asks nothing under a one-layout
// fixture, so its own cost line is never priced.
func jpCostLine(t *testing.T, md string) string {
	t.Helper()
	for _, line := range strings.Split(jpSection(t, md, "mapping_check_auto"), "\n") {
		if strings.HasPrefix(line, jpCostLabel) {
			return line
		}
	}
	t.Fatalf("mapping_check_auto has no %q line", jpCostLabel)
	return ""
}

func jpCostFigure(t *testing.T, line string) float64 {
	t.Helper()
	m := jpCostRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("no cost figure in %q", line)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("cost figure %q does not parse: %v", m[1], err)
	}
	return v
}

// Core AC-6, this binary's copy. The gated seams are duplicated by design, so the mapping
// binary needs its own oracle that the operator's rates reach Render.
func TestJevMapping_TheOperatorSuppliedPriceReachesTheCostLine(t *testing.T) {
	run := func(t *testing.T, in, out string) string {
		t.Helper()
		dir := t.TempDir()
		jpSeedOneLayoutFixture(t, dir, "plain_01")
		t.Setenv("TYPESAFE_API_KEY", "sk-test")
		t.Setenv("JEV_OUT", dir)
		t.Setenv("JEV_PRICE_INPUT_PER_M", in)
		t.Setenv("JEV_PRICE_OUTPUT_PER_M", out)
		srv, _ := jpFake(t, func(state string, questions map[string]any) (string, int) {
			return jpNoulBodyWithUsage(questions, "0.90", 1000, 200), http.StatusOK
		})
		if got := jpGatedRun(t, srv.URL); !got {
			t.Fatalf("jpGatedRun = false, want true")
		}
		md, err := os.ReadFile(filepath.Join(dir, "jev-report.md"))
		if err != nil {
			t.Fatalf("read jev-report.md: %v", err)
		}
		return jpCostLine(t, string(md))
	}

	t.Run("a supplied price prices the tokens", func(t *testing.T) {
		line := run(t, "2.00", "10.00")
		if strings.Contains(line, "price not supplied") {
			t.Fatalf("both rates were supplied and the report still reads: %s", line)
		}
		if !strings.Contains(line, "price used: $2.00 / 1M input tokens, $10.00 / 1M output tokens") {
			t.Errorf("the cost line does not echo the operator's rates: %s", line)
		}
		single := jpCostFigure(t, line)
		if single <= 0 {
			t.Fatalf("cost per 1,000 documents is %v, want a positive figure: %s", single, line)
		}
		// Discriminating leg: a cost that ignores the operator's numbers would not move. The
		// band absorbs the cost line's two-decimal rounding, nothing wider.
		double := jpCostFigure(t, run(t, "4.00", "20.00"))
		if double < 1.9*single || double > 2.1*single {
			t.Errorf("doubling both rates rendered $%.2f against $%.2f -- the operator's rates do not drive the arithmetic", double, single)
		}
	})

	for _, tc := range []struct{ name, in, out string }{
		{"unset", "", ""},
		{"input not a number", "abc", "10.00"},
		{"output negative", "2.00", "-1"},
	} {
		t.Run(tc.name+" leaves the price unsupplied", func(t *testing.T) {
			line := run(t, tc.in, tc.out)
			if !strings.Contains(line, "price not supplied — cost not computed") {
				t.Errorf("rates (%q, %q) rendered %q, want the price-not-supplied sentence", tc.in, tc.out, line)
			}
			if strings.Contains(line, "$0.00") {
				t.Errorf("rates (%q, %q) priced the run at zero: %s", tc.in, tc.out, line)
			}
		})
	}
}
