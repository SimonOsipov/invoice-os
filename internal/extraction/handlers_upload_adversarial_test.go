// handlers_upload_adversarial_test.go: the inputs POST /v1/documents will actually be sent --
// a filename and a Content-Type that disagree, a double extension, a part that is empty or
// absent, a header with unusual casing. Pure, like handlers_upload_test.go: no pool, never
// stRequire. Shares that file's up* harness.
package extraction_test

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- the classifier's two signals disagree ----------------------------------------------------

// TestUploadHandler_ExtensionWinsOverAContradictingContentType: the extension is consulted
// first and is decisive when the table knows it, so a browser mislabelling a PDF as text/csv
// still uploads a PDF. This is detectFormat's shape (internal/importer/handlers.go:142-162)
// and the order EXTR-09-04's picker classifies in, so the two agree by construction.
//
// The last case is the CONSEQUENCE of that order and is pinned as a known hole, not as
// desired behaviour: an extension the table does not know falls THROUGH to the declared type,
// so a caller can hand this route a .csv by declaring it application/pdf. Nothing downstream
// re-checks. The picker never produces that request; a hostile or broken client can.
func TestUploadHandler_ExtensionWinsOverAContradictingContentType(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		partType string
		wantCode int
		wantType string
	}{
		{"a pdf declared text/csv is a pdf", "scan.pdf", "text/csv", http.StatusCreated, upPDF},
		// Both signals name an accepted type and disagree, in both directions. The first row
		// was a .png declared application/pdf until EXTR-15-03 narrowed .png out of the table.
		{"a pdf declared as a docx is a pdf", "scan.pdf", upDOCX, http.StatusCreated, upPDF},
		{"a docx declared image/jpeg is a docx", "scan.docx", upJPEG, http.StatusCreated, upDOCX},

		// KNOWN HOLES, pinned so a change to either is deliberate. The .png row is EXTR-15-03's
		// own consequence: narrowing removed the EXTENSION, so a .png now falls through to the
		// declared type like any other unknown one, and image bytes still enter under a lie.
		{"a csv declared application/pdf is accepted as a pdf", "ledger.csv", upPDF, http.StatusCreated, upPDF},
		{"a png declared application/pdf is accepted as a pdf", "scan.png", upPDF, http.StatusCreated, upPDF},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := newUpSpy()
			rec, raw, decoded := upPost(t, spy, c.filename, c.partType, []byte("bytes"))

			if rec.Code != c.wantCode {
				t.Fatalf("status = %d, want %d for %s declared %q (body=%s)", rec.Code, c.wantCode, c.filename, c.partType, raw)
			}
			if got := upString(t, decoded, "content_type"); got != c.wantType {
				t.Errorf("content_type = %q, want %q", got, c.wantType)
			}
			if spy.gotContentType != c.wantType {
				t.Errorf("the store seam was handed %q, want the canonical %q -- the row records the classifier's verdict, never the client's header", spy.gotContentType, c.wantType)
			}
		})
	}
}

// TestUploadHandler_DoubleExtensionReadsTheLastOne: filepath.Ext takes the final segment, so
// invoice.pdf.csv is a .csv and invoice.csv.pdf is a .pdf. The pair is here because a
// classifier written with strings.Contains rather than an extension lookup would accept both.
func TestUploadHandler_DoubleExtensionReadsTheLastOne(t *testing.T) {
	t.Run("pdf.csv with no declared type is refused", func(t *testing.T) {
		spy := newUpSpy()
		rec, raw, decoded := upPost(t, spy, "invoice.pdf.csv", "", []byte("a,b\n1,2\n"))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for invoice.pdf.csv (body=%s)", rec.Code, raw)
		}
		if got := upString(t, decoded, "error"); got != upMsgRefusal {
			t.Errorf("error = %q, want %q", got, upMsgRefusal)
		}
		if spy.calls("store") != 0 {
			t.Errorf("the store seam ran %d time(s) on a refused .pdf.csv, want 0", spy.calls("store"))
		}
	})

	t.Run("csv.pdf is a pdf", func(t *testing.T) {
		spy := newUpSpy()
		rec, raw, decoded := upPost(t, spy, "invoice.csv.pdf", "", []byte("%PDF-1.7 fake"))

		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 for invoice.csv.pdf (body=%s)", rec.Code, raw)
		}
		if got := upString(t, decoded, "content_type"); got != upPDF {
			t.Errorf("content_type = %q, want %q", got, upPDF)
		}
		if got := upString(t, decoded, "filename"); got != "invoice.csv.pdf" {
			t.Errorf("filename = %q, want the part's own %q", got, "invoice.csv.pdf")
		}
	})
}

// --- the part itself ---------------------------------------------------------------------------

// TestUploadHandler_MissingFilePartIs400: a form with no "file" part at all. FormFile's error
// must land on the 400 arm rather than reaching the store with a nil reader.
//
// The well-formed control runs first: "the store never ran" also holds for a handler that
// never reaches the store on any input.
func TestUploadHandler_MissingFilePartIs400(t *testing.T) {
	control := newUpSpy()
	rec, raw, _ := upPost(t, control, "scan.pdf", upPDF, []byte("%PDF-1.7 fake"))
	if rec.Code != http.StatusCreated || control.calls("store") != 1 {
		t.Fatalf("control: an accepted pdf returned %d with %d store call(s), want 201 and 1 (body=%s)", rec.Code, control.calls("store"), raw)
	}

	// A blank filename tells upBody to write no file part, only the extra fields.
	spy := newUpSpy()
	body, ct := upBody(t, "", "", nil, map[string]string{"entity_id": "not-a-file"})
	rec, raw, decoded := upServe(t, spy, &upIdentity, ct, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a form with no file part (body=%s)", rec.Code, raw)
	}
	if got := upString(t, decoded, "error"); got != "file is required" {
		t.Errorf("error = %q, want %q", got, "file is required")
	}
	if len(spy.order) != 0 {
		t.Errorf("a form with no file part ran %v, want neither seam", spy.order)
	}
}

// TestUploadHandler_EmptyFilePartIs201: an accepted extension carrying zero bytes is stored
// and enqueued today -- documents.size_bytes CHECKs >= 0, so the row is legal, and nothing
// between here and the worker looks at the length.
//
// Pinned as the SHIPPED behaviour, not as a preference: no acceptance criterion rules on it,
// and the cost is one dead extraction job per empty upload. A future minimum-size refusal
// should change this spec deliberately.
func TestUploadHandler_EmptyFilePartIs201(t *testing.T) {
	spy := newUpSpy()
	rec, raw, decoded := upPost(t, spy, "scan.pdf", upPDF, []byte{})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for a zero-byte pdf part (body=%s)", rec.Code, raw)
	}
	if spy.gotSize != 0 {
		t.Errorf("the store seam was handed size %d, want 0", spy.gotSize)
	}
	if spy.calls("enqueue") != 1 {
		t.Errorf("enqueue ran %d time(s) for a zero-byte upload, want 1 (the shipped behaviour)", spy.calls("enqueue"))
	}
	if got := upBool(t, decoded, "reused"); got {
		t.Errorf("reused = true from a store seam that reported false")
	}
}

// TestUploadHandler_FirstFilePartWins: two parts both named "file". FormFile returns the
// first, so exactly one document is stored -- a handler that looped the parts would store two
// and enqueue two, and the 201 would name only one of them.
func TestUploadHandler_FirstFilePartWins(t *testing.T) {
	spy := newUpSpy()
	body, ct := upTwoFileParts(t, "first.pdf", upPDF, []byte("%PDF first"), "second.png", upPNG, []byte("PNG second"))
	rec, raw, decoded := upServe(t, spy, &upIdentity, ct, body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, raw)
	}
	if spy.calls("store") != 1 {
		t.Errorf("the store seam ran %d time(s) for two file parts, want 1", spy.calls("store"))
	}
	if spy.calls("enqueue") != 1 {
		t.Errorf("enqueue ran %d time(s) for two file parts, want 1", spy.calls("enqueue"))
	}
	if got := upString(t, decoded, "filename"); got != "first.pdf" {
		t.Errorf("filename = %q, want the FIRST part's %q", got, "first.pdf")
	}
}

// --- the declared type's spelling ---------------------------------------------------------------

// TestUploadHandler_ContentTypeCasingAndParametersAreNormalized: mime.ParseMediaType lowercases
// the media type and drops its parameters, so only the base type is compared. The refusal case
// is the control -- a classifier that matched loosely would accept it too.
func TestUploadHandler_ContentTypeCasingAndParametersAreNormalized(t *testing.T) {
	cases := []struct {
		name     string
		partType string
		wantCode int
	}{
		{"upper case", "APPLICATION/PDF", http.StatusCreated},
		{"mixed case with a charset", "Application/PDF; charset=UTF-8", http.StatusCreated},
		{"a boundary-style parameter", `application/pdf; name="scan.pdf"`, http.StatusCreated},
		{"leading and trailing space around the parameter", "application/pdf ;  charset=utf-8", http.StatusCreated},

		{"a supported type as a PARAMETER value is not the base type", `text/csv; fallback="application/pdf"`, http.StatusBadRequest},
		{"a supported type as a PREFIX is not the base type", "application/pdf-fake", http.StatusBadRequest},
		{"a supported type as a SUFFIX is not the base type", "x-application/pdf", http.StatusBadRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// scan.bin so the extension cannot decide; the declared type is the only signal.
			spy := newUpSpy()
			rec, raw, decoded := upPost(t, spy, "scan.bin", c.partType, []byte("bytes"))

			if rec.Code != c.wantCode {
				t.Fatalf("status = %d, want %d for Content-Type %q (body=%s)", rec.Code, c.wantCode, c.partType, raw)
			}
			if c.wantCode != http.StatusCreated {
				if got := upString(t, decoded, "error"); got != upMsgRefusal {
					t.Errorf("error = %q, want %q", got, upMsgRefusal)
				}
				if spy.calls("store") != 0 {
					t.Errorf("the store seam ran %d time(s) on a refused type, want 0", spy.calls("store"))
				}
				return
			}
			if got := upString(t, decoded, "content_type"); got != upPDF {
				t.Errorf("content_type = %q, want the canonical %q -- the parameters and the casing must not reach the row", got, upPDF)
			}
		})
	}
}

// TestUploadHandler_NoExtensionAndNoDeclaredTypeIsRefused: a part with neither signal. The
// accepted-by-content-type control proves the extensionless filename alone is not what
// refuses it.
func TestUploadHandler_NoExtensionAndNoDeclaredTypeIsRefused(t *testing.T) {
	control := newUpSpy()
	rec, raw, _ := upPost(t, control, "scan", upPDF, []byte("%PDF-1.7 fake"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("control: an extensionless part declared application/pdf returned %d, want 201 (body=%s); the refusal below would be about the missing extension, not the missing type", rec.Code, raw)
	}

	// A blank part type leaves multipart on its application/octet-stream default.
	spy := newUpSpy()
	rec, raw, decoded := upPost(t, spy, "scan", "", []byte("bytes"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a part with no extension and no declared type (body=%s)", rec.Code, raw)
	}
	if got := upString(t, decoded, "error"); got != upMsgRefusal {
		t.Errorf("error = %q, want %q", got, upMsgRefusal)
	}
	if len(spy.order) != 0 {
		t.Errorf("a refused upload ran %v, want neither seam", spy.order)
	}
}

// --- the published table -------------------------------------------------------------------------

// TestAcceptedDocumentTypes_IsPDFAndDOCXAndNoSpreadsheet reads the table EXTR-09-04's
// CLASSIFY-5 will compare its TypeScript copy against. It is a package-level literal precisely
// so that comparison is possible, and this spec is what keeps it readable and honest.
func TestAcceptedDocumentTypes_IsPDFAndDOCXAndNoSpreadsheet(t *testing.T) {
	got := extractionAcceptedTypes(t)
	if len(got) == 0 {
		t.Fatal("the accepted-type table read as empty; every assertion below would pass over nothing")
	}

	wantExtensions := map[string]string{
		".pdf":  upPDF,
		".docx": upDOCX,
	}
	if len(got) != len(wantExtensions) {
		t.Errorf("the table holds %d extension(s) %v, want %d %v", len(got), got, len(wantExtensions), wantExtensions)
	}
	for ext, want := range wantExtensions {
		if got[ext] != want {
			t.Errorf("the table maps %q to %q, want %q", ext, got[ext], want)
		}
	}

	distinct := map[string]bool{}
	for ext, ct := range got {
		if ext != strings.ToLower(ext) {
			t.Errorf("the table is keyed by %q; the lookup lowercases the extension, so an upper-case key is unreachable", ext)
		}
		if !strings.HasPrefix(ext, ".") {
			t.Errorf("the table key %q has no leading dot; filepath.Ext returns one, so this key is unreachable", ext)
		}
		distinct[ct] = true
	}
	// Derived from wantExtensions, never typed: two extensions may share a content type (.jpg
	// and .jpeg did), so this is the aliasing claim, not a second count of the keys.
	wantDistinct := map[string]bool{}
	for _, ct := range wantExtensions {
		wantDistinct[ct] = true
	}
	if len(distinct) != len(wantDistinct) {
		t.Errorf("the table names %d distinct content type(s) %v, want the %d in %v", len(distinct), distinct, len(wantDistinct), wantDistinct)
	}
	for _, banned := range []string{upXLSX, "text/csv", "text/plain"} {
		if distinct[banned] {
			t.Errorf("the table names %q; a spreadsheet has its own route, and accepting it on both is two ways to store the same thing", banned)
		}
	}
}

// --- harness ---------------------------------------------------------------------------------

// upTwoFileParts builds a body with TWO parts both named "file".
func upTwoFileParts(t *testing.T, name1, type1 string, body1 []byte, name2, type2 string, body2 []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range []struct {
		name, ctype string
		body        []byte
	}{{name1, type1, body1}, {name2, type2, body2}} {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, p.name))
		h.Set("Content-Type", p.ctype)
		fw, err := w.CreatePart(h)
		if err != nil {
			t.Fatalf("create part %s: %v", p.name, err)
		}
		if _, err := fw.Write(p.body); err != nil {
			t.Fatalf("write part %s: %v", p.name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

var (
	upTableRE = regexp.MustCompile(`(?s)acceptedDocumentTypes\s*=\s*map\[string\]string\{(.*?)\n\}`)
	upEntryRE = regexp.MustCompile(`"([^"]*)":\s*"([^"]*)"`)
)

// extractionAcceptedTypes reads the table out of classify.go's SOURCE rather than through an
// exported copy: EXTR-09-04's CLASSIFY-5 compares the picker's TypeScript table against this
// literal, so "it is still a readable package-level literal" is itself the thing under test.
// An inline switch would satisfy any assertion made through an exported map and break -04.
func extractionAcceptedTypes(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("classify.go")
	if err != nil {
		t.Fatalf("read classify.go: %v", err)
	}
	m := upTableRE.FindSubmatch(raw)
	if m == nil {
		t.Fatal("no `acceptedDocumentTypes = map[string]string{...}` literal in classify.go; EXTR-09-04's CLASSIFY-5 reads this table and has just lost its anchor")
	}
	out := map[string]string{}
	for _, e := range upEntryRE.FindAllSubmatch(m[1], -1) {
		out[string(e[1])] = string(e[2])
	}
	return out
}

// --- AC-3, sharpened -------------------------------------------------------------------------

// upCountingReader records how many bytes were pulled off the request body.
type upCountingReader struct {
	r    io.Reader
	read int
}

func (c *upCountingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

// TestUploadHandler_NoIdentityNeverReadsTheBody: AC-3's "before the cap is applied", asserted
// on the wire rather than on a status code.
//
// The status assertion alone cannot see the ordering: http.MaxBytesReader only WRAPS the body,
// so hoisting it above the identity check changes nothing observable -- the 401 still comes
// back, because nothing reads until ParseMultipartForm. What "identity first" actually buys is
// that a stranger's 16 MiB never crosses the process boundary, and only a byte count sees it.
func TestUploadHandler_NoIdentityNeverReadsTheBody(t *testing.T) {
	build := func() (io.Reader, string, *upCountingReader) {
		body, ct := upBody(t, "scan.pdf", upPDF, bytes.Repeat([]byte("x"), 16<<20), nil)
		counter := &upCountingReader{r: body}
		return counter, ct, counter
	}

	// Control: the SAME body, with an identity. Those bytes must be read -- otherwise "zero
	// bytes read" below is a fact about the harness, not about the handler.
	reader, ct, counted := build()
	control := newUpSpy()
	rec, raw, _ := upServe(t, control, &upIdentity, ct, reader)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("control: an authenticated 16 MiB body returned %d, want 413 (body=%s)", rec.Code, raw)
	}
	if counted.read == 0 {
		t.Fatalf("control: the handler read 0 bytes off an authenticated oversized body; this harness cannot observe a read at all, so the assertion below would pass over nothing")
	}

	reader, ct, counted = build()
	spy := newUpSpy()
	rec, raw, _ = upServe(t, spy, nil, ct, reader)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, raw)
	}
	if counted.read != 0 {
		t.Errorf("the handler read %d byte(s) off an UNAUTHENTICATED body, want 0 -- a stranger's upload must be refused before any of it is pulled across the process boundary", counted.read)
	}
}

// --- EXTR-15-03 (task-854, Test-first): the accepted set narrows to PDF + DOCX ----------------

// PN-7: the four narrowed-out extensions and their three content types are gone from
// acceptedDocumentTypes, and the two that stay are still there. Both halves are derived from
// upNarrowedOutExts / upNarrowedInTypes (handlers_upload_test.go), so the population follows the
// narrowing rather than a count typed here.
func TestAcceptedDocumentTypes_DropsTheImageTypes(t *testing.T) {
	got := extractionAcceptedTypes(t)
	if len(got) == 0 {
		t.Fatal("the accepted-type table read as empty; every assertion below would pass over nothing")
	}

	// The control runs FIRST. An absence check returns zero hits when the scan is broken, and
	// only a hit on what must STAY separates a narrowed table from an unreadable one.
	for ext, want := range upNarrowedInTypes {
		if got[ext] != want {
			t.Fatalf("acceptedDocumentTypes[%q] = %q, want %q; the absences below would pass over a table this scan misread", ext, got[ext], want)
		}
	}

	values := map[string]bool{}
	for _, ct := range got {
		values[ct] = true
	}
	for _, ext := range upNarrowedOutExts {
		if ct, ok := got[ext]; ok {
			t.Errorf("acceptedDocumentTypes still maps %q to %q; EXTR-15-03 drops it from the picker and this route together", ext, ct)
		}
	}
	for _, ct := range upNarrowedOutTypes {
		if values[ct] {
			t.Errorf("acceptedDocumentTypes still names %q; a declared image type must be refused, not classified", ct)
		}
	}
	// The absence restated as an equality: this cannot pass over an emptied table.
	if len(got) != len(upNarrowedInTypes) {
		t.Errorf("acceptedDocumentTypes holds %d entr(ies) %v, want exactly the %d that stay %v", len(got), got, len(upNarrowedInTypes), upNarrowedInTypes)
	}
}

var (
	upWantExtRE   = regexp.MustCompile(`(?s)wantExtensions\s*:=\s*map\[string\]string\{(.*?)\n\t\}`)
	upWantEntryRE = regexp.MustCompile(`"([^"]*)":\s*(\w+)`)
)

// PN-11: wantExtensions is a THIRD copy of the accepted-type table, and it is the copy
// CLASSIFY-5's honesty rests on. Read out of this file's own source and compared to classify.go,
// so the two cannot narrow apart.
func TestWantExtensions_MirrorsTheAcceptedTable(t *testing.T) {
	const self = "handlers_upload_adversarial_test.go"
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read %s: %v", self, err)
	}
	m := upWantExtRE.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("no `wantExtensions := map[string]string{...}` literal in %s; the third copy of the accepted-type table has moved and this mirror has lost its anchor", self)
	}

	// The literal names Go constants, not strings, so the identifiers are resolved here.
	idents := map[string]string{"upPDF": upPDF, "upPNG": upPNG, "upJPEG": upJPEG, "upWebP": upWebP, "upDOCX": upDOCX, "upXLSX": upXLSX}
	mirror := map[string]string{}
	for _, e := range upWantEntryRE.FindAllSubmatch(m[1], -1) {
		ext, ident := string(e[1]), string(e[2])
		ct, ok := idents[ident]
		if !ok {
			t.Fatalf("wantExtensions maps %q to the identifier %s, which this spec cannot resolve; add it to idents above rather than letting the mirror read short", ext, ident)
		}
		mirror[ext] = ct
	}
	if len(mirror) == 0 {
		t.Fatalf("wantExtensions parsed to 0 entries out of %s; the comparison below would be vacuous", self)
	}

	got := extractionAcceptedTypes(t)
	if len(got) == 0 {
		t.Fatal("acceptedDocumentTypes parsed empty out of classify.go; the comparison below would be vacuous")
	}
	if !maps.Equal(mirror, got) {
		t.Errorf("the accepted-type table's copies differ.\n  wantExtensions (%s): %v\n  acceptedDocumentTypes (classify.go): %v", self, mirror, got)
	}
}

// The two renames AC-8 requires. Old names are assembled from fragments on purpose: written
// whole, this spec's own source would satisfy its own absence check forever.
var upRenamedTests = map[string]string{
	"TestUploadHandler_AcceptsAll" + "FiveDocumentTypes":                     "TestUploadHandler_AcceptsEveryTypeInTheAcceptedTable",
	"TestAcceptedDocumentTypes_IsThe" + "FiveCanonicalTypesAndNoSpreadsheet": "TestAcceptedDocumentTypes_IsPDFAndDOCXAndNoSpreadsheet",
}

// PN-12: neither false test name survives, and both replacements are declared exactly once. A
// test name that states a type count the table no longer holds is a lie a reader will believe.
// The declaration count is the control needle: the absence half returns zero hits when the sweep
// is broken, and only a hit on the REPLACEMENT proves it is not.
func TestTestNames_StateNoFalseTypeCount(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob *_test.go: %v", err)
	}
	if len(files) < 4 {
		t.Fatalf("the glob found %d _test.go file(s) in this package; the sweep below would read almost nothing", len(files))
	}
	corpus := map[string]string{}
	decls := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		corpus[f] = string(b)
		decls += strings.Count(string(b), "\nfunc Test")
	}
	if decls < 20 {
		t.Fatalf("the sweep found %d top-level Test function(s) across %d file(s); it did not read the package", decls, len(files))
	}

	for old, replacement := range upRenamedTests {
		n := 0
		for _, src := range corpus {
			n += strings.Count(src, "func "+replacement+"(")
		}
		if n != 1 {
			t.Errorf("`func %s(` is declared %d time(s) in this package, want exactly 1 -- it is the name %s must take", replacement, n, old)
		}
		for f, src := range corpus {
			if strings.Contains(src, old) {
				t.Errorf("%s still names %s; rename it to %s, never delete it", f, old, replacement)
			}
		}
	}
}
