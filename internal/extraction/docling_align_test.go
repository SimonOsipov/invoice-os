// docling_align_test.go: T-05-13/14, Core AC 7's reconciliation half. The committed golden is
// replayed verbatim through a real DoclingReader, and its box for the "INVOICE" token is
// checked against pdfium's own render of the same fixture -- an integration assertion, not a
// json.Unmarshal into a test struct.
package extraction_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const dcGoldenName = "native_invoice.docling.json"

func dcReadGolden(t *testing.T) []byte {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(fxDir, dcGoldenName))
	if err != nil {
		t.Fatalf("read %s: %v -- regenerate it with `scripts/ci/docling-canary.sh golden <sha> --update`", dcGoldenName, err)
	}
	return b
}

// dcFlipY rewrites every token box's y0,y1 -> 1-y1, 1-y0, leaving every other field untouched.
func dcFlipY(t *testing.T, golden []byte) []byte {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(golden, &doc); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}

	pages, _ := doc["pages"].([]any)
	if len(pages) == 0 {
		t.Fatalf("golden carries no pages; the flip below would touch nothing")
	}

	var flipped int
	for _, pv := range pages {
		page, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		tokens, _ := page["tokens"].([]any)
		for _, tv := range tokens {
			tok, ok := tv.(map[string]any)
			if !ok {
				continue
			}
			y0, ok0 := tok["y0"].(float64)
			y1, ok1 := tok["y1"].(float64)
			if !ok0 || !ok1 {
				continue
			}
			tok["y0"], tok["y1"] = 1-y1, 1-y0
			flipped++
		}
	}
	if flipped == 0 {
		t.Fatalf("flipped 0 token box(es); the golden's shape no longer matches this helper")
	}

	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal flipped golden: %v", err)
	}
	return out
}

// dcInkForGolden serves golden verbatim, reads it through a real DoclingReader, and returns the
// dark-pixel count inside the "INVOICE" token's box on pdfium's render of the same fixture.
func dcInkForGolden(t *testing.T, golden []byte) int {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(golden)
	}))
	t.Cleanup(srv.Close)

	r, err := extraction.NewDoclingReader(srv.URL)
	if err != nil {
		t.Fatalf("NewDoclingReader(%q): %v", srv.URL, err)
	}

	var dcPages []extraction.Page
	if _, err := r.Read(t.Context(), extraction.Document{Bytes: golden, ContentType: "application/pdf"}, func(p extraction.Page) error {
		dcPages = append(dcPages, p)
		return nil
	}); err != nil {
		t.Fatalf("DoclingReader.Read: %v", err)
	}

	var allTokens int
	for _, p := range dcPages {
		allTokens += len(p.Tokens)
	}
	if allTokens < ptMinTokens {
		t.Fatalf("the Docling read produced %d token(s), want at least %d -- a golden that decodes to "+
			"nothing must fail loudly here, not at prToken's Fatalf", allTokens, ptMinTokens)
	}

	var doclingToken extraction.Token
	var found bool
	for _, p := range dcPages {
		for _, tok := range p.Tokens {
			if tok.Text == ptTopLine {
				doclingToken = tok
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no %q token among %d Docling token(s)", ptTopLine, allTokens)
	}

	pages := prRead(t, fxNative, prDefaultDPI)
	g := prGray(t, fxNative, pages[0])
	prAssertWhiteGround(t, fxNative, pages[0], g)

	box := prBox(doclingToken.Region, pages[0].ImageWidth, pages[0].ImageHeight)
	inside, _ := prInk(g, box)
	return inside
}

// T-05-13: Docling's box for a known token lands on pdfium's own ink for that token, well past
// prMinInk (measured ~2324 dark pixels against a floor of 20 -- see .ralph/EXTR-03-05-lead-verified.md).
func TestDoclingReader_KnownTokenLandsOnThePdfiumInk(t *testing.T) {
	if inside := dcInkForGolden(t, dcReadGolden(t)); inside < prMinInk {
		t.Fatalf("%q's Docling box covers %d dark pixel(s) of pdfium's render, want at least %d", ptTopLine, inside, prMinInk)
	}
}

// T-05-14: the negative control. A y-flipped golden lands the same box on a blank margin.
func TestDoclingReader_YFlippedGoldenMissesTheInk(t *testing.T) {
	flipped := dcFlipY(t, dcReadGolden(t))
	if inside := dcInkForGolden(t, flipped); inside >= prMinInk {
		t.Errorf("a Y-flipped golden's box covers %d dark pixel(s), want fewer than %d: the flip should "+
			"land it on a blank margin", inside, prMinInk)
	}
}

// --- EXTR-17-02: the corpus golden and the two readers' tokenisation ---------------------

const (
	dcCorpusGoldenName = "corpus_inline_labels.docling.json"
	dcCorpusFixture    = "corpus_inline_labels.pdf"
)

// dcCorpusTokenPin is what BOTH readers produce for corpus_inline_labels.pdf: measured, one
// token per printed line. Pinned as literals rather than compared by count -- two counts that
// happen to agree say nothing about the texts, and this fixture's granularities do agree.
var dcCorpusTokenPin = []string{
	"INVOICE",
	"Invoice No: INV-1001",
	"Invoice Date: 2026-03-04",
	"Supplier TIN: 99999999-0101",
	"Supplier: Adeyemi Trading Limited",
	"Buyer TIN: 99999999-0102",
	"Buyer: Honeywell Group",
	"Currency: NGN",
	"Sub-total: 1,000.00",
	"VAT: 75.00",
	"Total: 1,075.00",
}

func dcReadNamedGolden(t *testing.T, name string) []byte {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(fxDir, name))
	if err != nil {
		t.Fatalf("read %s: %v -- regenerate it with `scripts/ci/docling-canary.sh golden <sha> internal/extraction/testdata/%s internal/extraction/testdata/%s --update`",
			name, err, dcCorpusFixture, name)
	}
	return b
}

// dcServeGolden reads golden through a real DoclingReader over an httptest server, the shape
// every replay spec in this package uses.
func dcServeGolden(t *testing.T, golden []byte) ([]extraction.Page, []extraction.TokenPage, extraction.PageResult) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(golden)
	}))
	t.Cleanup(srv.Close)

	r, err := extraction.NewDoclingReader(srv.URL)
	if err != nil {
		t.Fatalf("NewDoclingReader(%q): %v", srv.URL, err)
	}

	var pages []extraction.Page
	var tokens []extraction.TokenPage
	collect := extraction.CollectTokens(&tokens)
	res, err := r.Read(t.Context(), extraction.Document{Bytes: golden, ContentType: "application/pdf"}, func(p extraction.Page) error {
		if err := collect(p); err != nil {
			return err
		}
		p.ImagePNG = nil
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		t.Fatalf("DoclingReader.Read: %v", err)
	}
	return pages, tokens, res
}

// dcTokenTexts flattens every page's token texts in reader order.
func dcTokenTexts(pages []extraction.TokenPage) []string {
	out := []string{}
	for _, p := range pages {
		for _, tok := range p.Tokens {
			out = append(out, tok.Text)
		}
	}
	return out
}

// dcSplitWords rewrites a golden so every multi-word token becomes one token per word, each
// keeping the line's vertical span and taking a proportional slice of its horizontal one. The
// point is a granularity this fixture does not otherwise offer: both readers return whole
// lines, so without this nothing here can observe tokenisation at all. Fatal when it splits
// nothing -- a silent no-op would make the negative control pass for the wrong reason.
func dcSplitWords(t *testing.T, golden []byte) []byte {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(golden, &doc); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	pages, _ := doc["pages"].([]any)
	if len(pages) == 0 {
		t.Fatalf("golden carries no pages; the split below would touch nothing")
	}

	split := 0
	for _, pv := range pages {
		page, ok := pv.(map[string]any)
		if !ok {
			continue
		}
		tokens, _ := page["tokens"].([]any)
		out := make([]any, 0, len(tokens))
		for _, tv := range tokens {
			tok, ok := tv.(map[string]any)
			if !ok {
				out = append(out, tv)
				continue
			}
			text, _ := tok["text"].(string)
			words := strings.Fields(text)
			if len(words) < 2 {
				out = append(out, tok)
				continue
			}
			split++
			x0, hasX0 := tok["x0"].(float64)
			x1, hasX1 := tok["x1"].(float64)
			off := 0
			for _, w := range words {
				at := strings.Index(text[off:], w) + off
				word := map[string]any{"text": w}
				for _, k := range []string{"y0", "y1"} {
					if v, ok := tok[k]; ok {
						word[k] = v
					}
				}
				if hasX0 && hasX1 {
					word["x0"] = x0 + (x1-x0)*float64(at)/float64(len(text))
					word["x1"] = x0 + (x1-x0)*float64(at+len(w))/float64(len(text))
				}
				out = append(out, word)
				off = at + len(w)
			}
		}
		page["tokens"] = out
	}
	if split == 0 {
		t.Fatalf("split 0 token(s); every token in the golden is already a single word and the negative control below proves nothing")
	}

	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal split golden: %v", err)
	}
	return b
}

// AC-6a/AC-10. The committed corpus golden is what the sidecar really returned, not a
// hand-written stand-in. Two independent clauses:
//
//   - the round trip. json.Decoder with UseNumber preserves Python's "792.0", which a decode
//     into any and a re-marshal would flatten to "792" -- the oracle without UseNumber fails on
//     every machine-generated golden and passes only on a hand-authored one.
//   - docling_version. A stub image answers /v1/read with "stub" and one "STUB" token, which
//     round-trips perfectly.
func TestDoclingGolden_CorpusInlineLabelsIsMachineGenerated(t *testing.T) {
	golden := dcReadNamedGolden(t, dcCorpusGoldenName)

	dec := json.NewDecoder(bytes.NewReader(golden))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode %s: %v", dcCorpusGoldenName, err)
	}
	round, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("re-serialise %s: %v", dcCorpusGoldenName, err)
	}
	round = append(round, '\n') // the script's print() writes one; Go's marshal does not

	if !bytes.Equal(round, golden) {
		t.Errorf("%s is not what `docling-canary.sh golden` writes (%d bytes re-serialised, %d committed); regenerate it from a freshly built image rather than editing it",
			dcCorpusGoldenName, len(round), len(golden))
	}

	obj, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("%s decodes to %T, want a JSON object", dcCorpusGoldenName, doc)
	}
	version, _ := obj["docling_version"].(string)
	if version == "" || version == "stub" {
		t.Errorf("%s reports docling_version %q; it was generated by a stub image, not the pinned sidecar", dcCorpusGoldenName, version)
	}
}

// AC-6b/AC-12. Both readers over the same fixture, each pinned against the SAME literal list.
// The pin is the assertion: a count comparison goes false-green the day the two counts stop
// differing, and on this fixture they never differed to begin with.
func TestDoclingReader_CorpusTokenisationMatchesItsPin(t *testing.T) {
	var pdfium []extraction.TokenPage
	doc := extraction.Document{Bytes: fxRead(t, dcCorpusFixture), ContentType: "application/pdf"}
	if _, err := extraction.NewPDFiumReader().Read(t.Context(), doc, extraction.CollectTokens(&pdfium)); err != nil {
		t.Fatalf("PDFiumReader.Read(%s): %v", dcCorpusFixture, err)
	}
	_, docling, _ := dcServeGolden(t, dcReadNamedGolden(t, dcCorpusGoldenName))

	pdfiumTexts, doclingTexts := dcTokenTexts(pdfium), dcTokenTexts(docling)
	t.Logf("pdfium %d token(s): %q", len(pdfiumTexts), pdfiumTexts)
	t.Logf("docling %d token(s): %q", len(doclingTexts), doclingTexts)

	if len(dcCorpusTokenPin) == 0 {
		t.Fatalf("the pin is empty; both comparisons below would hold over nothing")
	}
	if !slices.Equal(pdfiumTexts, dcCorpusTokenPin) {
		t.Errorf("PDFium read %q, want the pin %q", pdfiumTexts, dcCorpusTokenPin)
	}
	if !slices.Equal(doclingTexts, dcCorpusTokenPin) {
		t.Errorf("Docling read %q, want the pin %q", doclingTexts, dcCorpusTokenPin)
	}

	// The control for dcSplitWords, so the negative control in worker_pipeline_db_test.go
	// cannot pass on a helper that silently changed nothing.
	_, splitPages, _ := dcServeGolden(t, dcSplitWords(t, dcReadNamedGolden(t, dcCorpusGoldenName)))
	splitTexts := dcTokenTexts(splitPages)
	if len(splitTexts) <= len(doclingTexts) {
		t.Errorf("dcSplitWords left %d token(s) against the golden's %d; it must raise the count", len(splitTexts), len(doclingTexts))
	}
	if slices.Contains(splitTexts, "Buyer TIN: 99999999-0102") {
		t.Errorf("dcSplitWords left the whole line %q intact; the learned rule under test would still match it", "Buyer TIN: 99999999-0102")
	}
}
