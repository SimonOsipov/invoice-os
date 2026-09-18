package extraction_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// The rotation must reach the reader's token order: if pdfium re-sorted by position, every
// rotated page would read identically and TestFixtures_AISteeredOutcomeIgnoresTokenOrder would
// prove nothing.
func TestFixtures_AISteeredRotationMovesTheMarkerInTheReadOrder(t *testing.T) {
	lines := fxAISteeredLines()
	for i := range lines {
		pages := fxPagesFromBytes(t, fxTextPage(fxLinesWithMarkerAt(lines, i)...))
		if len(pages) != 1 || len(pages[0].Tokens) != len(lines) {
			t.Fatalf("marker at %d: read %d page(s), want 1 page of %d tokens: %+v", i, len(pages), len(lines), pages)
		}
		var others []string
		for j, tok := range pages[0].Tokens {
			isMarker := strings.HasPrefix(tok.Text, fxAISteeredMarkerPrefix)
			if isMarker != (j == i) {
				t.Errorf("marker at %d: token %d = %q, marker=%v", i, j, tok.Text, isMarker)
			}
			if !isMarker {
				others = append(others, tok.Text)
			}
		}
		var want []string
		for _, l := range lines[:len(lines)-1] {
			want = append(want, l.text)
		}
		if !reflect.DeepEqual(others, want) {
			t.Errorf("marker at %d: the other nine read %q, want %q", i, others, want)
		}
	}
}

// The order test's literal engine rows must be what the engine really reads off the committed
// fixture, and the real merge must land the three cases while leaving every other row alone.
func TestFixtures_AISteeredCommittedFixtureReadsAsTheOrderTestAssumes(t *testing.T) {
	pages := rvCorpusPages(t, fxAISteered)

	var markers []string
	for _, tok := range pages[0].Tokens {
		if strings.HasPrefix(tok.Text, fxAISteeredMarkerPrefix) {
			markers = append(markers, tok.Text)
		}
	}
	if len(markers) != 1 || markers[0] != fxAISteeredMarker() {
		t.Fatalf("committed fixture carries marker token(s) %q, want exactly one equal to the generator's", markers)
	}

	engine := extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(pages, rvGeneric())})
	if len(engine) == 0 {
		t.Fatal("Reconcile returned no rows")
	}
	eng := make(map[string]extraction.FieldResult, len(engine))
	for _, r := range engine {
		eng[r.Name] = r
	}
	if r := eng["invoice_number"]; r.Reason != extraction.ReasonMissing {
		t.Errorf("engine invoice_number = %+v, want missing", r)
	}
	if r := eng["buyer_tin"]; r.Reason != extraction.ReasonNone || r.Value == nil || *r.Value != "12345678-0001" {
		t.Errorf("engine buyer_tin = %+v, want decided 12345678-0001", r)
	}
	if r := eng["buyer_name"]; r.Reason != extraction.ReasonMissing {
		t.Errorf("engine buyer_name = %+v, want missing", r)
	}

	answer := fxDecodeAISteeredMarker(t, markers[0])
	out := extraction.MergeAIForTest(engine, answer, pages, nil)
	if len(out) != len(engine) {
		t.Fatalf("merge returned %d rows, want %d", len(out), len(engine))
	}
	for i, r := range out {
		switch r.Name {
		case "invoice_number":
			if r.Reason != extraction.ReasonNone || r.Value == nil || *r.Value != "20417" || r.Region == nil {
				t.Errorf("merged invoice_number = %+v, want decided 20417 with a region", r)
			}
		case "buyer_tin":
			if r.Reason != extraction.ReasonAmbiguous || len(r.Alternatives) != 1 || *r.Alternatives[0].Value != "87654321-0002" {
				t.Errorf("merged buyer_tin = %+v, want ambiguous with alt 87654321-0002", r)
			}
		case "buyer_name":
			if r.Reason != extraction.ReasonUnreadable || r.Value != nil || len(r.Alternatives) != 1 || *r.Alternatives[0].Value != "ZENITH HOLDINGS LIMITED" {
				t.Errorf("merged buyer_name = %+v, want unreadable offering ZENITH HOLDINGS LIMITED", r)
			}
		default:
			if !reflect.DeepEqual(r, engine[i]) {
				t.Errorf("merge moved %s: %+v, engine read %+v", r.Name, r, engine[i])
			}
		}
	}

	// docs/ai-client.md "Document reading": a blank answer changes nothing.
	if blank := extraction.MergeAIForTest(engine, nil, pages, nil); !reflect.DeepEqual(blank, engine) {
		t.Errorf("a blank answer moved the engine's reading:\n got %+v\nwant %+v", blank, engine)
	}
}
