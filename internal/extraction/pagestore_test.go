// pagestore_test.go: PageStore's specs. Database-free by construction — PageStore renders,
// PUTs and returns what it wrote, and the rows are the worker's own transaction
// (worker_db_test.go). scripts/ci/rls-test-gate.sh fails on any skip in this package.
package extraction_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

const (
	psTenant = "3f2a1c88-0b6d-4e19-9f31-5c7a2d840e11"

	// The same tenant with its hex letters upper-cased. uuid::text renders LOWERCASE and
	// extraction_page_images_key_tenant_scoped compares bytes, so PageKey must not fold case
	// in either direction.
	psTenantUpper = "3F2A1C88-0B6D-4E19-9F31-5C7A2D840E11"

	// A 64-hex-character stand-in for a document's content hash.
	psHashLiteral = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	psPNGMagic = "\x89PNG\r\n\x1a\n"
)

// psWantKey spells the key template out rather than calling PageKey, so every expectation
// below is independent of the function under test.
func psWantKey(tenantID, hash string, page int) string {
	return fmt.Sprintf("tenants/%s/pages/%s/v1/p%04d.png", tenantID, hash, page)
}

func psHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// psPut is one recorded sink call.
type psPut struct {
	key  string
	body []byte
}

// psSink records every PUT. failOn > 0 errors on the call of that number, so a caller can
// prove the ingest stops there rather than running on.
type psSink struct {
	puts   []psPut
	seen   int
	failOn int
	err    error
}

func (s *psSink) put(_ context.Context, key string, body []byte) error {
	s.seen++
	if s.failOn > 0 && s.seen == s.failOn {
		return s.err
	}
	// Page.ImagePNG is borrowed for the duration of the onPage call that carried it, so the
	// recorder keeps a copy rather than the caller's buffer.
	s.puts = append(s.puts, psPut{key: key, body: bytes.Clone(body)})
	return nil
}

func (s *psSink) keys() []string {
	out := make([]string, 0, len(s.puts))
	for _, p := range s.puts {
		out = append(out, p.key)
	}
	return out
}

// psReader is a PageReader over arbitrary bytes. reads is the oracle for "rendered exactly
// once per Ingest": pdfium cannot serve that claim, because a re-reading implementation would
// still produce the same keys.
type psReader struct {
	pages int
	reads int

	// tokens supplies page i+1's Tokens when set (index i). Left nil for specs that do not
	// care about token content, so Page.Tokens stays nil as before.
	tokens [][]extraction.Token
}

func (r *psReader) Name() string    { return "page-store-fake" }
func (r *psReader) Version() string { return "v1" }

func (r *psReader) Read(_ context.Context, _ extraction.Document, onPage func(extraction.Page) error) (extraction.PageResult, error) {
	r.reads++
	for i := 1; i <= r.pages; i++ {
		var toks []extraction.Token
		if i-1 < len(r.tokens) {
			toks = r.tokens[i-1]
		}
		err := onPage(extraction.Page{
			Number:      i,
			WidthPt:     612,
			HeightPt:    792,
			ImageWidth:  100 + i,
			ImageHeight: 200 + i,
			ImagePNG:    []byte(psPNGMagic + fmt.Sprintf("page-%d", i)),
			Tokens:      toks,
		})
		if err != nil {
			return extraction.PageResult{}, err
		}
	}
	return extraction.PageResult{Pages: r.pages}, nil
}

// psPDFiumStore is the store the corpus specs drive: the real renderer, a recording sink.
func psPDFiumStore(sink *psSink) *extraction.PageStore {
	return &extraction.PageStore{Reader: extraction.NewPDFiumReader(), Sink: sink.put}
}

func psDoc(b []byte) extraction.Document {
	return extraction.Document{Bytes: b, ContentType: "application/pdf"}
}

// TestPageKey_IsTenantPrefixedAndProfileVersioned: both segments are server-derived, and the
// /v1/ segment is what makes a future render profile a new object rather than an overwrite.
func TestPageKey_IsTenantPrefixedAndProfileVersioned(t *testing.T) {
	got := extraction.PageKey(psTenant, psHashLiteral, 1)
	want := "tenants/3f2a1c88-0b6d-4e19-9f31-5c7a2d840e11/pages/" +
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/v1/p0001.png"
	if got != want {
		t.Errorf("PageKey(tenant, hash, 1) = %q, want %q", got, want)
	}

	// The page cap, so the four-digit pad is proved at both ends of its range.
	if got, want := extraction.PageKey(psTenant, psHashLiteral, 800), psWantKey(psTenant, psHashLiteral, 800); got != want {
		t.Errorf("PageKey(tenant, hash, 800) = %q, want %q", got, want)
	}
	if !strings.HasSuffix(extraction.PageKey(psTenant, psHashLiteral, 800), "/p0800.png") {
		t.Errorf("page 800 does not end in /p0800.png: %q", extraction.PageKey(psTenant, psHashLiteral, 800))
	}

	// Zero-padding is what makes the lexical order of the keys the page order. Without it
	// p10 sorts between p1 and p2.
	if a, b := extraction.PageKey(psTenant, psHashLiteral, 2), extraction.PageKey(psTenant, psHashLiteral, 10); a >= b {
		t.Errorf("page 2 sorts at %q and page 10 at %q; unpadded page numbers do not sort in page order", a, b)
	}
}

// TestPageKey_DoesNotCaseTransformTheTenantSegment: D-23. uuid::text renders lowercase and
// extraction_page_images_key_tenant_scoped compares bytes, so a key folded to either case is
// rejected by the CHECK for every tenant whose id carries a hex letter.
func TestPageKey_DoesNotCaseTransformTheTenantSegment(t *testing.T) {
	if strings.EqualFold(psTenant, psTenantUpper) && psTenant == psTenantUpper {
		t.Fatal("the two tenant fixtures are the same string; this test cannot see a case fold")
	}

	got := extraction.PageKey(psTenantUpper, psHashLiteral, 3)
	if !strings.HasPrefix(got, "tenants/"+psTenantUpper+"/") {
		t.Errorf("PageKey(%q, ...) = %q; the tenant segment was case-folded, and the storage_key CHECK compares it byte-for-byte against uuid::text", psTenantUpper, got)
	}
	if got == extraction.PageKey(psTenant, psHashLiteral, 3) {
		t.Error("PageKey returns one key for two tenant strings that differ only in case; the prefix assertion above is satisfied by both")
	}
}

// TestPageStore_WritesOneObjectPerPage: one PUT per page, in page order, each a PNG.
func TestPageStore_WritesOneObjectPerPage(t *testing.T) {
	raw := fxRead(t, fxNative3)
	sink := &psSink{}

	images, _, res, err := psPDFiumStore(sink).Ingest(t.Context(), psTenant, psDoc(raw))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Pages != 3 {
		t.Fatalf("the reader reported %d page(s), want 3; every key assertion below would be reading the wrong corpus", res.Pages)
	}
	if len(images) != 3 {
		t.Fatalf("Ingest returned %d image(s), want 3", len(images))
	}

	hash := psHash(raw)
	want := []string{
		psWantKey(psTenant, hash, 1),
		psWantKey(psTenant, hash, 2),
		psWantKey(psTenant, hash, 3),
	}
	if got := sink.keys(); !slices.Equal(got, want) {
		t.Errorf("the sink recorded keys\n  %v\nwant\n  %v", got, want)
	}
	for i, p := range sink.puts {
		if !bytes.HasPrefix(p.body, []byte(psPNGMagic)) {
			t.Errorf("page %d was PUT as %d byte(s) that do not begin with the PNG signature", i+1, len(p.body))
		}
	}
}

// TestPageStore_IngestRendersExactlyOncePerCall: an implementation that re-reads the document
// per page renders an 800-page document 800 times and blows the render budget on page one.
func TestPageStore_IngestRendersExactlyOncePerCall(t *testing.T) {
	reader := &psReader{pages: 3}
	sink := &psSink{}
	store := &extraction.PageStore{Reader: reader, Sink: sink.put}

	if _, _, _, err := store.Ingest(t.Context(), psTenant, psDoc([]byte("three pages"))); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if reader.reads != 1 {
		t.Errorf("Ingest called PageReader.Read %d time(s) for one document, want 1", reader.reads)
	}
	if len(sink.puts) != 3 {
		t.Fatalf("the sink saw %d PUT(s), want 3; a read count of 1 over a reader that emitted nothing proves nothing", len(sink.puts))
	}
}

// TestPageStore_SecondIngestWritesTheSameKeysAndBodies: the key carries no nonce, so a River
// retry overwrites its own objects rather than accumulating a new set per attempt.
func TestPageStore_SecondIngestWritesTheSameKeysAndBodies(t *testing.T) {
	raw := fxRead(t, fxNative3)

	first, second := &psSink{}, &psSink{}
	if _, _, _, err := psPDFiumStore(first).Ingest(t.Context(), psTenant, psDoc(raw)); err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if _, _, _, err := psPDFiumStore(second).Ingest(t.Context(), psTenant, psDoc(raw)); err != nil {
		t.Fatalf("second Ingest: %v", err)
	}

	if len(first.puts) != 3 || len(second.puts) != 3 {
		t.Fatalf("the two ingests wrote %d and %d object(s), want 3 each", len(first.puts), len(second.puts))
	}
	hash := psHash(raw)
	for i := range first.puts {
		want := psWantKey(psTenant, hash, i+1)
		if first.puts[i].key != want {
			t.Errorf("the first ingest wrote page %d to %q, want %q", i+1, first.puts[i].key, want)
		}
		if second.puts[i].key != first.puts[i].key {
			t.Errorf("page %d landed on %q then %q; a key that changes per attempt orphans every earlier object", i+1, first.puts[i].key, second.puts[i].key)
		}
		if !bytes.Equal(first.puts[i].body, second.puts[i].body) {
			t.Errorf("page %d rendered %d byte(s) then %d byte(s); the same document must overwrite its own object with the same pixels", i+1, len(first.puts[i].body), len(second.puts[i].body))
		}
	}
}

// TestPageStore_KeyDependsOnTheDocumentBytes: the positive needle under the stability spec
// above, which a PageKey returning a constant would satisfy vacuously.
//
// A fake reader, not pdfium: the claim is that the key derives from the document's bytes, and
// a PDF with one byte flipped is not required to stay parseable.
func TestPageStore_KeyDependsOnTheDocumentBytes(t *testing.T) {
	raw := fxRead(t, fxNative3)
	flipped := bytes.Clone(raw)
	flipped[len(flipped)/2] ^= 0x01

	if psHash(raw) == psHash(flipped) {
		t.Fatal("the two fixtures hash identically; the key assertions below cannot distinguish them")
	}

	original, altered := &psSink{}, &psSink{}
	for _, c := range []struct {
		body []byte
		sink *psSink
	}{{raw, original}, {flipped, altered}} {
		store := &extraction.PageStore{Reader: &psReader{pages: 3}, Sink: c.sink.put}
		if _, _, _, err := store.Ingest(t.Context(), psTenant, psDoc(c.body)); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}
	if len(original.puts) != 3 || len(altered.puts) != 3 {
		t.Fatalf("the two ingests wrote %d and %d object(s), want 3 each", len(original.puts), len(altered.puts))
	}

	for i := range original.puts {
		if original.puts[i].key == altered.puts[i].key {
			t.Errorf("page %d of two different documents landed on the same key %q; one document's pixels would overwrite the other's", i+1, original.puts[i].key)
		}
		if !strings.Contains(original.puts[i].key, psHash(raw)) {
			t.Errorf("page %d's key %q does not carry the document's content hash", i+1, original.puts[i].key)
		}
		if !strings.Contains(altered.puts[i].key, psHash(flipped)) {
			t.Errorf("the altered document's page %d key %q does not carry its own content hash", i+1, altered.puts[i].key)
		}
	}
}

// TestPageStore_ReturnsWhatItWrote: D-22. The returned StorageKey is the key that was PUT, and
// the dimensions are the render's own grid — a canvas scales a normalised box by these, so a
// value recomputed from the page's points misplaces every highlight.
func TestPageStore_ReturnsWhatItWrote(t *testing.T) {
	raw := fxRead(t, fxNative3)
	sink := &psSink{}

	images, _, _, err := psPDFiumStore(sink).Ingest(t.Context(), psTenant, psDoc(raw))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(images) != 3 || len(sink.puts) != 3 {
		t.Fatalf("Ingest returned %d image(s) over %d PUT(s), want 3 and 3", len(images), len(sink.puts))
	}

	hash := psHash(raw)
	for i, img := range images {
		if img.Page != i+1 {
			t.Errorf("image %d reports page %d, want %d", i, img.Page, i+1)
		}
		if want := psWantKey(psTenant, hash, i+1); img.StorageKey != want {
			t.Errorf("image %d carries storage key %q, want %q", i, img.StorageKey, want)
		}
		if want := extraction.PageKey(psTenant, hash, i+1); img.StorageKey != want {
			t.Errorf("image %d carries storage key %q, which is not PageKey's %q: the stored column must be pinned to the derivation", i, img.StorageKey, want)
		}
		if img.StorageKey != sink.puts[i].key {
			t.Errorf("image %d names %q but the object went to %q: a row would name an object that was never written", i, img.StorageKey, sink.puts[i].key)
		}

		cfg, err := png.DecodeConfig(bytes.NewReader(sink.puts[i].body))
		if err != nil {
			t.Fatalf("decode the PNG written for page %d: %v", i+1, err)
		}
		if cfg.Width == 0 || cfg.Height == 0 {
			t.Fatalf("page %d decoded to a %dx%d image; the dimension assertions below would be vacuous", i+1, cfg.Width, cfg.Height)
		}
		if img.WidthPx != cfg.Width || img.HeightPx != cfg.Height {
			t.Errorf("image %d reports %dx%d px but the PNG it wrote is %dx%d", i, img.WidthPx, img.HeightPx, cfg.Width, cfg.Height)
		}
	}
}

// TestPageStore_ASinkFailureStopsTheIngest: a partial return is what would let a caller commit
// a row for a page that was never PUT. W-07 (EXTR-14-03) extends this to tokens: a partial
// token list is the same defect as a partial image list.
func TestPageStore_ASinkFailureStopsTheIngest(t *testing.T) {
	raw := fxRead(t, fxNative3)
	boom := errors.New("page store: the object store refused the PUT")
	sink := &psSink{failOn: 2, err: boom}

	images, tokens, res, err := psPDFiumStore(sink).Ingest(t.Context(), psTenant, psDoc(raw))
	if !errors.Is(err, boom) {
		t.Fatalf("Ingest returned %v, want the sink's error", err)
	}
	if images != nil {
		t.Errorf("Ingest returned %d image(s) alongside its error, want none", len(images))
	}
	if tokens != nil {
		t.Errorf("Ingest returned %d token page(s) alongside its error, want none", len(tokens))
	}
	if res.Pages != 0 {
		t.Errorf("Ingest returned a PageResult reporting %d page(s) alongside its error, want the zero value", res.Pages)
	}
	if sink.seen != 2 {
		t.Errorf("the sink was called %d time(s), want 2: the ingest ran on past the page that failed", sink.seen)
	}
	if len(sink.puts) != 1 {
		t.Errorf("the sink recorded %d successful PUT(s), want 1 (page 1 only)", len(sink.puts))
	}
}

// TestPageStore_ReturnsOneTokenPagePerPage: W-06 (AC-5, EXTR-14-03). Ingest returns one
// TokenPage per page, in page order, carrying the reader's own tokens untouched -- not a
// re-derivation of them.
func TestPageStore_ReturnsOneTokenPagePerPage(t *testing.T) {
	reader := &psReader{pages: 2, tokens: [][]extraction.Token{
		{{Text: "page-1-token"}},
		{{Text: "page-2-token"}},
	}}
	sink := &psSink{}
	store := &extraction.PageStore{Reader: reader, Sink: sink.put}

	_, tokens, res, err := store.Ingest(t.Context(), psTenant, psDoc([]byte("two pages")))
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Pages != 2 {
		t.Fatalf("the reader reported %d page(s), want 2; the assertions below would read the wrong corpus", res.Pages)
	}
	if len(tokens) != res.Pages {
		t.Fatalf("Ingest returned %d TokenPage(s), want %d -- one per page", len(tokens), res.Pages)
	}
	for i, tp := range tokens {
		if tp.Number != i+1 {
			t.Errorf("token page %d reports page number %d, want %d", i, tp.Number, i+1)
		}
		want := fmt.Sprintf("page-%d-token", i+1)
		if len(tp.Tokens) != 1 || tp.Tokens[0].Text != want {
			t.Errorf("token page %d carries %+v, want exactly one token with text %q", i+1, tp.Tokens, want)
		}
	}

	// Control: the same claim over the real PDFiumReader, where the token count per page is
	// measured (fxNative3: 3 pages, 3 tokens each) rather than dictated by a fake.
	rawNative3 := fxRead(t, fxNative3)
	_, nativeTokens, nativeRes, err := psPDFiumStore(&psSink{}).Ingest(t.Context(), psTenant, psDoc(rawNative3))
	if err != nil {
		t.Fatalf("Ingest over %s: %v", fxNative3, err)
	}
	if nativeRes.Pages != 3 {
		t.Fatalf("%s reports %d page(s), want 3", fxNative3, nativeRes.Pages)
	}
	if len(nativeTokens) != 3 {
		t.Fatalf("Ingest over %s returned %d TokenPage(s), want 3", fxNative3, len(nativeTokens))
	}
	for i, tp := range nativeTokens {
		if tp.Number != i+1 {
			t.Errorf("native token page %d reports page number %d, want %d", i, tp.Number, i+1)
		}
		if len(tp.Tokens) != 3 {
			t.Errorf("native token page %d carries %d token(s), want 3 (measured on this fixture)", i+1, len(tp.Tokens))
		}
	}
}
