// classify_internal_test.go: RendersPageImages and the table behind it. Package extraction, so
// these specs can name pageImageFormats: a bare func(string) bool has no table to be exhaustive
// over, and every input to one is "classified".
package extraction

import (
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
