// docling_align_test.go: T-05-13/14, Core AC 7's reconciliation half. The committed golden is
// replayed verbatim through a real DoclingReader, and its box for the "INVOICE" token is
// checked against pdfium's own render of the same fixture -- an integration assertion, not a
// json.Unmarshal into a test struct.
package extraction_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
