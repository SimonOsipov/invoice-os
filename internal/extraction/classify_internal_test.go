// classify_internal_test.go: RendersPageImages and the table behind it. Package extraction, so
// these specs can name pageImageFormats: a bare func(string) bool has no table to be exhaustive
// over, and every input to one is "classified".
package extraction

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// cxDocxType is what acceptedDocumentTypes records for .docx, spelled out so the specs below
// name a format rather than an extension.
const cxDocxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// DX-1 (AC-1): across every accepted format exactly one renders page images, and it is
// application/pdf.
func TestRendersPageImages_OnlyPDFRendersPageImages(t *testing.T) {
	if len(acceptedDocumentTypes) == 0 {
		t.Fatalf("acceptedDocumentTypes is empty; the sweep below would hold vacuously")
	}

	var renders []string
	seen := map[string]bool{}
	for _, ct := range acceptedDocumentTypes {
		if seen[ct] {
			continue
		}
		seen[ct] = true
		if RendersPageImages(ct) {
			renders = append(renders, ct)
		}
	}
	slices.Sort(renders)
	if want := []string{"application/pdf"}; !slices.Equal(renders, want) {
		t.Errorf("RendersPageImages is true for %v across acceptedDocumentTypes, want %v", renders, want)
	}

	// Named cases as well: the set compare above is also satisfied by a predicate true for a
	// pdf spelled some other way, and it never reaches a type the table does not accept.
	if !RendersPageImages("application/pdf") {
		t.Errorf(`RendersPageImages("application/pdf") = false, want true`)
	}
	if RendersPageImages(cxDocxType) {
		t.Errorf("RendersPageImages(%q) = true, want false", cxDocxType)
	}
	// A strict allowlist. documents.declared_content_type is nullable and Upsert is ON CONFLICT
	// DO NOTHING, so a row stored by another route can hand extraction "" or a type this table
	// never accepted; both must take the no-render branch.
	if RendersPageImages("") {
		t.Errorf(`RendersPageImages("") = true, want false`)
	}
	if RendersPageImages("text/csv") {
		t.Errorf(`RendersPageImages("text/csv") = true, want false`)
	}
}

// DX-2 (AC-7): every value of acceptedDocumentTypes is classified by name in pageImageFormats.
// A format added to that table with no entry here fails rather than defaulting to the no-render
// branch. The population is read off the map, never written as a literal, so EXTR-15-03's
// narrowing cannot orphan this spec.
func TestRendersPageImages_TableIsExhaustiveOverAcceptedTypes(t *testing.T) {
	if len(acceptedDocumentTypes) == 0 {
		t.Fatalf("acceptedDocumentTypes is empty; the loop below would assert nothing")
	}
	if len(pageImageFormats) == 0 {
		t.Fatalf("pageImageFormats is empty; every accepted format would take the no-render branch by default")
	}
	for ext, ct := range acceptedDocumentTypes {
		if _, ok := pageImageFormats[ct]; !ok {
			t.Errorf("acceptedDocumentTypes[%q] = %q has no entry in pageImageFormats; decide whether that format renders page images rather than letting a missing key decide it", ext, ct)
		}
	}
}

// --- looksLikePDF and RendersPageImagesForDocument --------------------------------------

// cxRead reads a fixture directly: fxRead and its fixture-name constants live in package
// extraction_test, which package extraction (this file, for the unexported looksLikePDF) cannot import.
func cxRead(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

func TestLooksLikePDF_AcceptsOnlyTheHeaderAtOffsetZero(t *testing.T) {
	docxBytes := cxRead(t, "invoice.docx")

	tests := []struct {
		name string
		in   []byte
		want bool
	}{
		{"PDF header at offset zero", []byte("%PDF-1.4\n%rest"), true},
		{"exactly the five-byte header, nothing trailing", []byte("%PDF-"), true},
		{"truncated to four bytes", []byte("%PDF"), false},
		{"header present but not at offset zero", []byte("\n%PDF-1.4"), false},
		{"header at offset one, minimal", []byte("_%PDF-"), false},
		{"lowercase header", []byte("%pdf-1.4"), false},
		{"DOCX magic (PK\\x03\\x04), read off the real invoice.docx fixture", docxBytes, false},
		{"nil", nil, false},
		{"empty", []byte(""), false},
		{"one byte", []byte("%"), false},
	}
	if len(tests) == 0 {
		t.Fatalf("tests is empty; the loop below would assert nothing")
	}

	for _, tc := range tests {
		if got := looksLikePDF(tc.in); got != tc.want {
			t.Errorf("looksLikePDF(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A five-byte prefix check has no reason to scan past the header; this pins that it doesn't.
func TestLooksLikePDF_DoesNotScanPastTheHeaderOnALargeBuffer(t *testing.T) {
	large := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 1<<20)...)
	if !looksLikePDF(large) {
		t.Errorf("looksLikePDF(1MB buffer with a valid header) = false, want true")
	}
}

func TestRendersPageImagesForDocument_SniffsWhenTheDeclaredTypeRefuses(t *testing.T) {
	pdfBytes := cxRead(t, "native_3page.pdf")

	tests := []struct {
		name string
		doc  Document
	}{
		{"declared type refuses, bytes are a real PDF", Document{Bytes: pdfBytes, ContentType: "application/octet-stream"}},
		{"declared type empty, bytes are a real PDF", Document{Bytes: pdfBytes, ContentType: ""}},
	}
	if len(tests) == 0 {
		t.Fatalf("tests is empty; the loop below would assert nothing")
	}

	for _, tc := range tests {
		if !RendersPageImagesForDocument(tc.doc) {
			t.Errorf("RendersPageImagesForDocument(%s) = false, want true", tc.name)
		}
	}
}

func TestRendersPageImagesForDocument_DoesNotWidenTheGateForANonPDF(t *testing.T) {
	docxBytes := cxRead(t, "invoice.docx")

	tests := []struct {
		name string
		doc  Document
	}{
		{"DOCX bytes, empty declared type", Document{Bytes: docxBytes, ContentType: ""}},
		{"CSV bytes, declared text/csv", Document{Bytes: []byte("id,amount\n1,2"), ContentType: "text/csv"}},
	}
	if len(tests) == 0 {
		t.Fatalf("tests is empty; the loop below would assert nothing")
	}

	for _, tc := range tests {
		if RendersPageImagesForDocument(tc.doc) {
			t.Errorf("RendersPageImagesForDocument(%s) = true, want false", tc.name)
		}
	}
}

// The control: without this case, a predicate that ignored ContentType entirely and sniffed
// only bytes would still pass the three tests above.
func TestRendersPageImagesForDocument_KeepsTheDeclaredTypeAuthoritativeOnItsOwn(t *testing.T) {
	doc := Document{Bytes: nil, ContentType: "application/pdf"}
	if !RendersPageImagesForDocument(doc) {
		t.Errorf("RendersPageImagesForDocument(%+v) = false, want true", doc)
	}
}
