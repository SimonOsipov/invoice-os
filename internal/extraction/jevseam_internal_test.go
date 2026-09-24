// jevseam_internal_test.go: the decision seam -- what ExtractWorker.Work actually merges --
// replayed over the real fourteen-layout corpus instead of hand-built rows.
package extraction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// jsWantLayouts pins the corpus size: any list below it must have shrunk.
const jsWantLayouts = 14

// jsLayouts mirrors endtoend's expectByLayout order (TestJevSeam_LayoutListMirrorsExpectByLayout
// pins the copy against drift).
var jsLayouts = []string{
	"corpus_inline_labels", "corpus_split_labels", "corpus_stacked_labels", "corpus_two_column",
	"corpus_ambiguous_date", "corpus_totals_block", "wild_two_party_bare_tin", "wild_ruled_lines_totals",
	"wild_rc_due_naira", "wild_stacked_borderless", "wild_scanned_no_number",
	"wild_two_party_bare_tin_asprinted", "wild_ruled_lines_totals_asprinted",
	"wild_stacked_borderless_asprinted",
}

// jsGoldenRead replays one committed Docling golden through the real DoclingReader and readText
// -- production's own two-slice read (readText), not aitGoldenPages' single TokenPage slice.
func jsGoldenRead(t *testing.T, name string) ([]Page, []TokenPage) {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", name+".docling.json"))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	r, err := NewDoclingReader(srv.URL)
	if err != nil {
		t.Fatalf("NewDoclingReader(%q): %v", srv.URL, err)
	}

	pages, tokens, _, err := readText(t.Context(), r, Document{ContentType: "application/pdf"})
	if err != nil {
		t.Fatalf("readText golden %s: %v", name, err)
	}
	return pages, tokens
}

func jsTokenCount(tokens []TokenPage) int {
	n := 0
	for _, p := range tokens {
		n += len(p.Tokens)
	}
	return n
}

// TestJevSeam_ABlankAIDecisionEqualsReconcile is AC-3's identity, over the real corpus rather
// than hand-built rows: it is what turns "measured on what production decides" into a fact.
// Passes today, unrenamed -- both merges are already the identity under a blank answer
// (TestMergeAI_Row6, TestMergeAILines_NilAIReturnsRowsUnchanged); this is a characterization
// pin, not a RED driver.
func TestJevSeam_ABlankAIDecisionEqualsReconcile(t *testing.T) {
	if len(jsLayouts) == 0 {
		t.Fatal("jsLayouts is empty -- the walk below would range over nothing and pass vacuously")
	}
	for _, layout := range jsLayouts {
		t.Run(layout, func(t *testing.T) {
			pages, tokens := jsGoldenRead(t, layout)
			if jsTokenCount(tokens) == 0 {
				t.Fatalf("%s: golden replay yielded zero tokens -- the seam below has nothing to reconcile", layout)
			}
			lines := LineItems(pages)
			in := Input{
				Candidates: Resolve(tokens, RuleSet{Tier1: Tier1Rules}),
				Lines:      lines,
				Entity:     Entity{},
				Pages:      tokens,
			}
			want := Reconcile(in)
			if len(want) == 0 {
				t.Fatalf("%s: Reconcile decided zero rows -- the identity below would hold over two empty slices", layout)
			}
			got := mergeAILines(mergeAI(Reconcile(in), nil, tokens, lines), nil, tokens)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: a blank AI answer changed the decision\ngot:  %+v\nwant: %+v (Reconcile's own output)", layout, got, want)
			}
		})
	}
}

// TestJevSeam_ZeroLayoutsWalkedIsAFatal is the walk's own floor (docs/extraction-corpus.md):
// wild_scanned_no_number is image-only, so its golden is the only route to a non-zero token count.
func TestJevSeam_ZeroLayoutsWalkedIsAFatal(t *testing.T) {
	if len(jsLayouts) < jsWantLayouts {
		t.Fatalf("jsLayouts has %d entries, want at least %d -- the walk covers fewer than the corpus", len(jsLayouts), jsWantLayouts)
	}
	for _, layout := range jsLayouts {
		_, tokens := jsGoldenRead(t, layout)
		if n := jsTokenCount(tokens); n == 0 {
			t.Fatalf("%s: golden replay yielded zero tokens -- a walk claiming %d layouts must prove each one was read", layout, jsWantLayouts)
		}
	}
}

// jsExpectByLayoutRE isolates endtoend's var expectByLayout = []struct{...}{...} block: the
// struct-type close plus slice-open ("}{") through the slice literal's own unindented close.
var jsExpectByLayoutRE = regexp.MustCompile(`(?s)var expectByLayout = .*?\n\}\n`)

var jsFileRE = regexp.MustCompile(`file:\s*"([^"]+)\.pdf"`)

// TestJevSeam_LayoutListMirrorsExpectByLayout pins jsLayouts against endtoend's own table so the
// hand copy across the package boundary (package extraction cannot import package endtoend)
// cannot drift silently.
func TestJevSeam_LayoutListMirrorsExpectByLayout(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("endtoend", "score_test.go"))
	if err != nil {
		t.Fatalf("read endtoend/score_test.go: %v", err)
	}

	block := jsExpectByLayoutRE.FindString(string(src))
	if block == "" {
		t.Fatal("expectByLayout block not found in endtoend/score_test.go -- the mirror below would compare against nothing")
	}

	var got []string
	for _, m := range jsFileRE.FindAllStringSubmatch(block, -1) {
		got = append(got, m[1])
	}
	if len(got) == 0 {
		t.Fatal("extracted zero layout names from expectByLayout -- the set-equality check below would pass vacuously")
	}

	gotSorted := slices.Clone(got)
	wantSorted := slices.Clone(jsLayouts)
	slices.Sort(gotSorted)
	slices.Sort(wantSorted)
	if !slices.Equal(gotSorted, wantSorted) {
		t.Errorf("jsLayouts drifted from endtoend's expectByLayout\nendtoend has: %v\njsLayouts has: %v", gotSorted, wantSorted)
	}
}

// TestJevSeam_BothProductionCallsSendTheSameBody is AC-2's cross-call half: each call site is
// pinned to DoclingPromptText separately, but nothing else asserts the two bodies are the same
// bytes for the same pages -- which is the one-shape constraint CHECK-03's thresholds rest on.
func TestJevSeam_BothProductionCallsSendTheSameBody(t *testing.T) {
	pages := []TokenPage{
		{Number: 1, Tokens: []Token{
			tok("Invoice", 1, 0.10, 0.10, 0.30, 0.12),
			tok("INV-1", 1, 0.10, 0.20, 0.30, 0.22),
		}},
		{Number: 2, Tokens: []Token{tok("Total 100.00", 2, 0.50, 0.80, 0.90, 0.82)}},
	}

	doc := &recordingAI{enabled: true, answer: map[string]any{}}
	lines := &recordingAI{enabled: true, answer: map[string]any{"line_items": []any{}}}
	askAI(context.Background(), doc, pages)
	askAILines(context.Background(), lines, pages)

	if len(doc.calls) != 1 || len(lines.calls) != 1 {
		t.Fatalf("calls = %d document, %d line-item, want exactly 1 each", len(doc.calls), len(lines.calls))
	}
	want := DoclingPromptText(pages)
	if want == "" {
		t.Fatal("DoclingPromptText returned empty for a two-page token set -- the equality below would be vacuous")
	}
	if doc.calls[0].Text != want {
		t.Errorf("askAI Text = %q, want DoclingPromptText = %q", doc.calls[0].Text, want)
	}
	if lines.calls[0].Text != want {
		t.Errorf("askAILines Text = %q, want DoclingPromptText = %q", lines.calls[0].Text, want)
	}
	if doc.calls[0].Text != lines.calls[0].Text {
		t.Errorf("the two production calls sent different bodies:\ndocument:  %q\nline-item: %q", doc.calls[0].Text, lines.calls[0].Text)
	}
}

// TestJevSeam_ANonBlankAnswerMovesARow is AC-4's control leg: without it, the identity above
// could pass because nothing ever moves a row, not because a blank answer is a no-op. Follows
// the mergeWith precedent at aimerge_internal_test.go's TestMergeAI_Row5.
func TestJevSeam_ANonBlankAnswerMovesARow(t *testing.T) {
	engine := []FieldResult{
		{Field: Field{Name: "invoice_number", Reason: ReasonMissing}, Alternatives: []Field{}},
	}
	pages := onePage(1, tok("Invoice Number: 20417", 1, 0.10, 0.10, 0.40, 0.12))

	got := mergeWith(engine, map[string]any{"invoice_number": "20417"}, pages, nil)

	if got[0].Reason != ReasonNone {
		t.Fatalf("row Reason = %q, want %q -- a non-blank answer must move the row, or the identity above is not a real control", got[0].Reason, ReasonNone)
	}
	if got[0].Value == nil || *got[0].Value != "20417" {
		t.Errorf("row Value = %v, want \"20417\"", got[0].Value)
	}
	if got[0].Region == nil {
		t.Error("row Region is nil, want the token's own region")
	}
}

// jsFakeJev is the real client in fake mode, as a PR environment runs it.
func jsFakeJev(t *testing.T) *jev.Client {
	t.Helper()
	t.Setenv(jev.EnvFake, "true")
	t.Setenv(jev.EnvKey, "")
	c, err := jev.FromEnv(nil)
	if err != nil {
		t.Fatalf("jev.FromEnv: %v", err)
	}
	if !c.Enabled() {
		t.Fatal("the fake client is not enabled; checkValues would never ask it")
	}
	return c
}

// jsCountingAsker counts Ask calls through to the real client.
type jsCountingAsker struct {
	JevAsker
	calls int
}

func (a *jsCountingAsker) Ask(ctx context.Context, req jev.Request) (jev.Response, error) {
	a.calls++
	return a.JevAsker.Ask(ctx, req)
}

// jsDecided is what the worker hands the check: the decided rows under a blank AI answer.
func jsDecided(t *testing.T, layout string) ([]FieldResult, []TokenPage) {
	t.Helper()
	pages, tokens := jsGoldenRead(t, layout)
	if jsTokenCount(tokens) == 0 {
		t.Fatalf("%s: golden replay yielded zero tokens", layout)
	}
	lines := LineItems(pages)
	in := Input{Candidates: Resolve(tokens, RuleSet{Tier1: Tier1Rules}), Lines: lines, Entity: Entity{}, Pages: tokens}
	results := mergeAILines(mergeAI(Reconcile(in), nil, tokens, lines), nil, tokens)
	if len(results) == 0 {
		t.Fatalf("%s: zero decided rows", layout)
	}
	return results, tokens
}

func TestJevSeam_TheFakeDefaultChangesNoLayout(t *testing.T) {
	if len(jsLayouts) < jsWantLayouts {
		t.Fatalf("jsLayouts has %d entries, want at least %d", len(jsLayouts), jsWantLayouts)
	}
	client := jsFakeJev(t)
	walked, asking := 0, 0
	for _, layout := range jsLayouts {
		results, tokens := jsDecided(t, layout)
		if strings.Contains(DoclingPromptText(tokens), "JEVFAKE-") {
			t.Fatalf("%s: the golden text already holds a fake marker", layout)
		}
		before := cloneResults(results)
		a := &jsCountingAsker{JevAsker: client}

		out := checkValues(t.Context(), a, tokens, results)

		walked++
		if !reflect.DeepEqual(out, before) {
			t.Errorf("%s: the fake default changed the decided rows\ngot:  %+v\nwant: %+v", layout, out, before)
		}
		// Control: the identity only means something if the fake was asked.
		wantCalls := 0
		if slices.ContainsFunc(before, vcCheckable) {
			wantCalls = 1
			asking++
		}
		if a.calls != wantCalls {
			t.Errorf("%s: %d Ask calls, want %d", layout, a.calls, wantCalls)
		}
	}
	if walked < jsWantLayouts || asking == 0 {
		t.Fatalf("walked %d layouts (want %d), %d with a checkable field (want > 0)", walked, jsWantLayouts, asking)
	}
}

func TestJevSeam_TheDoubtMarkerFlipsEveryAskedFieldOnly(t *testing.T) {
	if len(jsLayouts) < jsWantLayouts {
		t.Fatalf("jsLayouts has %d entries, want at least %d", len(jsLayouts), jsWantLayouts)
	}
	client := jsFakeJev(t)
	maxAsked, suppliersSeen := 0, 0
	for _, layout := range jsLayouts {
		results, tokens := jsDecided(t, layout)
		n := len(tokens) + 1
		marked := append(slices.Clone(tokens), TokenPage{Number: n, Tokens: []Token{tok("JEVFAKE-DOUBT", n, 0.10, 0.10, 0.30, 0.12)}})
		before := cloneResults(results)

		out := checkValues(t.Context(), client, marked, results)

		if len(out) != len(before) {
			t.Fatalf("%s: %d rows out, want %d", layout, len(out), len(before))
		}
		asked := 0
		for i, b := range before {
			switch {
			case vcCheckable(b):
				asked++
				want := b
				want.Reason = ReasonUnreadable
				if !reflect.DeepEqual(out[i], want) {
					t.Errorf("%s: %s Reason = %q, want %q with value, region and alternatives kept", layout, b.Name, out[i].Reason, ReasonUnreadable)
				}
			case b.Name == "supplier_tin" || b.Name == "supplier_name":
				if b.Reason == ReasonNone && b.Value != nil {
					suppliersSeen++
				}
				if out[i].Reason != b.Reason {
					t.Errorf("%s: %s Reason = %q, want %q -- the supplier pair is never asked", layout, b.Name, out[i].Reason, b.Reason)
				}
			default:
				if !reflect.DeepEqual(out[i], b) {
					t.Errorf("%s: unasked row %s changed: %+v", layout, b.Name, out[i])
				}
			}
		}
		maxAsked = max(maxAsked, asked)
	}
	if maxAsked < 3 {
		t.Errorf("the most fields any layout asks is %d, want at least 3", maxAsked)
	}
	if suppliersSeen == 0 {
		t.Error("no layout decided a supplier field, so the supplier-pair leg is vacuous")
	}
}
