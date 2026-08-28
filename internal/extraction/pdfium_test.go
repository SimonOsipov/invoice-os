// pdfium_test.go: AC-2's tokens and AC-3's text half. Every spec runs the real reader over the
// committed corpus, so a token box is measured against pdfium rather than described.
//
// The -update flag is fixtures_test.go's fxUpdate, reused: a second flag.Bool("update", ...)
// in one test binary panics at registration. fixtures_test.go sorts first, so one -update run
// regenerates the PDFs and then the goldens read from them.
package extraction_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	// Floor: every native fixture page carries three text lines, so a shorter token list means
	// the read found nothing and the assertions over it report nothing.
	ptMinTokens = 3

	// The top line of every native fixture, drawn at y=720 of a 792pt page -- 0.09 down from
	// the top. It anchors the Y-flip needle to a known glyph rather than to the arithmetic
	// under test.
	ptTopLine = "INVOICE"

	// Floor under the ceiling case: the documents upload limit is megabytes, not bytes.
	ptMinSizeLimit = 1 << 20
)

// ptTextFixtures are the fixtures that carry a text layer at all.
var ptTextFixtures = []string{fxNative, fxNative3, fxHybrid}

// ptGoldenFixtures are the two the committed token goldens pin.
var ptGoldenFixtures = []string{fxNative, fxNative3}

// --- harness ----------------------------------------------------------------

func ptDoc(t *testing.T, name string) extraction.Document {
	t.Helper()
	return extraction.Document{Bytes: fxRead(t, name), ContentType: "application/pdf"}
}

// ptRead runs the reader over one committed fixture and collects every page.
func ptRead(t *testing.T, name string) ([]extraction.Page, extraction.PageResult) {
	t.Helper()

	var pages []extraction.Page
	res, err := extraction.NewPDFiumReader().Read(t.Context(), ptDoc(t, name), func(p extraction.Page) error {
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		t.Fatalf("Read(%s): %v", name, err)
	}
	if len(pages) != res.Pages {
		t.Fatalf("Read(%s) called onPage %d time(s) but reported %d page(s)", name, len(pages), res.Pages)
	}
	return pages, res
}

// ptTokens flattens a read into one document-order token list.
func ptTokens(pages []extraction.Page) []extraction.Token {
	var out []extraction.Token
	for _, p := range pages {
		out = append(out, p.Tokens...)
	}
	return out
}

func ptMarshal(t *testing.T, tokens []extraction.Token) []byte {
	t.Helper()

	b, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		t.Fatalf("marshal %d token(s): %v", len(tokens), err)
	}
	return append(b, '\n')
}

func ptGoldenPath(fixture string) string {
	return filepath.Join(fxDir, strings.TrimSuffix(fixture, ".pdf")+".golden.json")
}

// --- the tests --------------------------------------------------------------

// TestPDFiumReader_TokensMatchTheGolden: AC-3. A committed golden pins every token's text and
// every digit of its box, so a coordinate change of any size is a readable diff rather than a
// silent drift.
func TestPDFiumReader_TokensMatchTheGolden(t *testing.T) {
	for _, name := range ptGoldenFixtures {
		t.Run(name, func(t *testing.T) {
			pages, _ := ptRead(t, name)
			got := ptMarshal(t, ptTokens(pages))
			path := ptGoldenPath(name)

			if *fxUpdate {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", path, err)
				}
				t.Logf("updated golden %s", path)
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v -- the goldens are committed; regenerate a deliberate change with `go test ./internal/extraction/ -run TestPDFiumReader_TokensMatchTheGolden -update` and read the diff", path, err)
			}

			var pinned []extraction.Token
			if err := json.Unmarshal(want, &pinned); err != nil {
				t.Fatalf("unmarshal golden %s: %v", path, err)
			}
			if len(pinned) < ptMinTokens {
				t.Fatalf("golden %s holds %d token(s), want at least %d -- a byte-compare against an empty golden reports nothing", path, len(pinned), ptMinTokens)
			}

			if !bytes.Equal(got, want) {
				t.Errorf("tokens for %s do not match %s -- %d byte(s) on disk, %d fresh\n--- want (golden) ---\n%s\n--- got (this read) ---\n%s",
					name, path, len(want), len(got), want, got)
			}
		})
	}
}

// TestPDFiumReader_IsByteIdenticalAcrossTwoReads: AC-3. The same bytes twice in one process
// yield the same tokens and the same totals.
func TestPDFiumReader_IsByteIdenticalAcrossTwoReads(t *testing.T) {
	for _, name := range ptTextFixtures {
		t.Run(name, func(t *testing.T) {
			firstPages, firstRes := ptRead(t, name)
			first := ptMarshal(t, ptTokens(firstPages))
			if n := len(ptTokens(firstPages)); n < ptMinTokens {
				t.Fatalf("the first read of %s produced %d token(s), want at least %d -- two empty reads are equal", name, n, ptMinTokens)
			}

			secondPages, secondRes := ptRead(t, name)
			second := ptMarshal(t, ptTokens(secondPages))

			if !bytes.Equal(first, second) {
				t.Errorf("two reads of %s in one process differ\n--- first ---\n%s\n--- second ---\n%s", name, first, second)
			}
			if firstRes != secondRes {
				t.Errorf("two reads of %s totalled %+v then %+v", name, firstRes, secondRes)
			}
		})
	}
}

// TestPDFiumReader_BoxesAreNormalisedTopLeft: AC-2. The predicate is
// extraction_field_results_bbox_normalised's, and the ptTopLine clause is the Y-flip needle:
// pdfium measures from the page bottom and a Region from the top, so dropping the flip -- or
// reaching for PixelPosition, which is the point position scaled and not flipped -- puts the
// document's top line in the bottom half.
func TestPDFiumReader_BoxesAreNormalisedTopLeft(t *testing.T) {
	for _, name := range []string{fxNative, fxNative3, fxScanned, fxHybrid} {
		t.Run(name, func(t *testing.T) {
			pages, res := ptRead(t, name)

			var topLines int
			for _, p := range pages {
				for i, tok := range p.Tokens {
					r := tok.Region
					if r.Page < 1 || r.Page > res.Pages {
						t.Errorf("%s token %d on page %d carries Region.Page %d, want 1..%d", name, i, p.Number, r.Page, res.Pages)
					}
					if r.Page != p.Number {
						t.Errorf("%s token %d arrived with page %d but carries Region.Page %d", name, i, p.Number, r.Page)
					}
					if !(0 <= r.X0 && r.X0 <= r.X1 && r.X1 <= 1) {
						t.Errorf("%s token %d (%q) has X0=%v X1=%v, want 0 <= X0 <= X1 <= 1", name, i, tok.Text, r.X0, r.X1)
					}
					if !(0 <= r.Y0 && r.Y0 <= r.Y1 && r.Y1 <= 1) {
						t.Errorf("%s token %d (%q) has Y0=%v Y1=%v, want 0 <= Y0 <= Y1 <= 1", name, i, tok.Text, r.Y0, r.Y1)
					}
					if tok.Text != ptTopLine {
						continue
					}
					topLines++
					if r.Y0 >= 0.5 {
						t.Errorf("%s page %d's %q line has Y0=%v, want below 0.5: it is drawn near the top of the page, and a Region is measured from the top -- this is the clause a missing Y flip inverts", name, p.Number, ptTopLine, r.Y0)
					}
				}
			}

			if name == fxScanned {
				return // no text layer: the clauses above are vacuous here by design
			}
			if topLines == 0 {
				t.Fatalf("%s yielded no %q token; the Y-flip needle above examined nothing", name, ptTopLine)
			}
		})
	}
}

// TestPDFiumReader_CountsPagesWithText: AC-2 and D-9. TextChars counts non-whitespace
// characters, so the \r\n pdfium puts between text runs does not inflate it.
func TestPDFiumReader_CountsPagesWithText(t *testing.T) {
	cases := []struct {
		fixture                     string
		pages, withText, nonWSChars int
	}{
		{fxNative, 1, 1, 41},
		{fxNative3, 3, 3, 96},
		{fxScanned, 1, 0, 0},
		{fxHybrid, 2, 1, 41},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			_, res := ptRead(t, tc.fixture)

			if res.Pages != tc.pages {
				t.Errorf("%s reported %d page(s), want %d", tc.fixture, res.Pages, tc.pages)
			}
			if res.PagesWithText != tc.withText {
				t.Errorf("%s reported PagesWithText %d, want %d", tc.fixture, res.PagesWithText, tc.withText)
			}
			if res.TextChars != tc.nonWSChars {
				t.Errorf("%s reported TextChars %d, want %d non-whitespace character(s)", tc.fixture, res.TextChars, tc.nonWSChars)
			}
			if res.PagesWithText > res.Pages {
				t.Errorf("%s reported PagesWithText %d of Pages %d", tc.fixture, res.PagesWithText, res.Pages)
			}
		})
	}
}

// TestPDFiumReader_DoesNotMutateInput: law E05. The bytes are handed to pdfium by reference.
func TestPDFiumReader_DoesNotMutateInput(t *testing.T) {
	limit, err := documentSizeLimit()
	if err != nil {
		t.Fatalf("documentSizeLimit: %v", err)
	}
	if limit < ptMinSizeLimit {
		t.Fatalf("the migrations state a documents ceiling of %d byte(s), want at least %d -- the ceiling case below would not be a large blob", limit, ptMinSizeLimit)
	}

	cases := []struct {
		name     string
		bytes    []byte
		readable bool
	}{
		{fxNative, fxRead(t, fxNative), true},
		{fxNative3, fxRead(t, fxNative3), true},
		{fxScanned, fxRead(t, fxScanned), true},
		{fxHybrid, fxRead(t, fxHybrid), true},
		{"nil", nil, false},
		{"empty", []byte{}, false},
		{"ceiling-zeros", make([]byte, limit), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := extraction.Document{Bytes: tc.bytes, ContentType: "application/pdf"}
			before, beforeLen := sha256.Sum256(doc.Bytes), len(doc.Bytes)

			_, err := extraction.NewPDFiumReader().Read(t.Context(), doc, func(extraction.Page) error { return nil })
			if tc.readable && err != nil {
				t.Fatalf("Read(%s): %v -- an unmutated verdict over a read that never opened the document proves nothing", tc.name, err)
			}

			if got := len(doc.Bytes); got != beforeLen {
				t.Errorf("Read(%s) left doc.Bytes %d byte(s) long, was %d", tc.name, got, beforeLen)
			}
			if after := sha256.Sum256(doc.Bytes); after != before {
				t.Errorf("Read(%s) changed doc.Bytes: %x before, %x after", tc.name, before, after)
			}
		})
	}
}

// TestPDFiumReader_DropsEmptyTextRects: a Field carrying an empty Value is what law E08 and
// Field.Value semantics both refuse, and a token converts to a Field with no conversion.
func TestPDFiumReader_DropsEmptyTextRects(t *testing.T) {
	for _, name := range ptTextFixtures {
		t.Run(name, func(t *testing.T) {
			pages, _ := ptRead(t, name)
			tokens := ptTokens(pages)
			if len(tokens) < ptMinTokens {
				t.Fatalf("%s yielded %d token(s), want at least %d -- an empty list holds no empty text either", name, len(tokens), ptMinTokens)
			}
			for i, tok := range tokens {
				if tok.Text == "" {
					t.Errorf("%s token %d on page %d carries empty text at %+v", name, i, tok.Region.Page, tok.Region)
				}
			}
		})
	}
}

// TestPDFiumReader_RejectsAMalformedPDF: an unreadable document is an error, not an empty
// success. The native fixture runs first as the control needle -- a reader that failed on
// everything would otherwise pass every clause below.
func TestPDFiumReader_RejectsAMalformedPDF(t *testing.T) {
	limit, err := documentSizeLimit()
	if err != nil {
		t.Fatalf("documentSizeLimit: %v", err)
	}

	if _, err := extraction.NewPDFiumReader().Read(t.Context(), ptDoc(t, fxNative), func(extraction.Page) error { return nil }); err != nil {
		t.Fatalf("Read(%s): %v -- the reader rejects a document it must accept, so the refusals below prove nothing", fxNative, err)
	}

	cases := []struct {
		name  string
		bytes []byte
	}{
		{"pdf-header-without-a-page-tree", exactBytes("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n")},
		{"empty", []byte{}},
		{"nil", nil},
		{"ceiling-zeros", make([]byte, limit)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := 0
			res, err := extraction.NewPDFiumReader().Read(t.Context(), extraction.Document{Bytes: tc.bytes, ContentType: "application/pdf"}, func(extraction.Page) error {
				pages++
				return nil
			})

			if err == nil {
				t.Errorf("Read(%s) returned no error; an unreadable document is refused, not reported as a document with no text", tc.name)
			}
			if res != (extraction.PageResult{}) {
				t.Errorf("Read(%s) failed but returned %+v, want a zero PageResult", tc.name, res)
			}
			if pages != 0 {
				t.Errorf("Read(%s) failed but called onPage %d time(s)", tc.name, pages)
			}
		})
	}
}

// TestPDFiumReader_PinsNameAndVersion: both are persisted as extraction_jobs.extractor /
// .extractor_version, so a drifting value orphans every stored row. Mirrors
// TestMockExtractor_PinsNameAndVersion.
func TestPDFiumReader_PinsNameAndVersion(t *testing.T) {
	first, second := extraction.NewPDFiumReader(), extraction.NewPDFiumReader()

	if got := first.Name(); got != "pdfium" {
		t.Errorf("Name() is %q, want %q", got, "pdfium")
	}
	if got := first.Version(); got != "v1" {
		t.Errorf("Version() is %q, want %q", got, "v1")
	}
	if first.Name() != second.Name() || first.Version() != second.Version() {
		t.Errorf("a second reader reports %q/%q, want %q/%q", second.Name(), second.Version(), first.Name(), first.Version())
	}
}
