// learn_adversarial_test.go: O-08, O-10 -- edge, boundary and negative cases for the
// layout_anchors codec and AnchorLabelText's byte cap, beyond learn_test.go's happy path.
// External package: every spec reaches only exported symbols. C-agree (the box predicate
// agreement with the request path's normalisedBox) lives in learn_internal_test.go instead,
// because normalisedBox is unexported.
package extraction_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// anchorElem builds one hand-written layout_anchors array element with an explicit page and box.
func anchorElem(page int, x0, y0, x1, y1 float64) string {
	return fmt.Sprintf(`[{"label":"total","text":"Total","page":%d,"band":0,"x0":%v,"y0":%v,"x1":%v,"y1":%v}]`, page, x0, y0, x1, y1)
}

// O-08 + the codec validation table: both sides of every boundary on the box, and both sides of
// the page floor, or a check that is silently too loose would pass.
func TestAnchorObservationsCodec_RefusesOutOfRangeBoxesAndPages(t *testing.T) {
	for _, c := range []struct {
		name    string
		raw     string
		wantErr string // "" means no error expected
	}{
		{"all boundaries valid: [0,1] box, page 1", anchorElem(1, 0, 0, 1, 1), ""},
		{"zero-area box valid: x0 == x1", anchorElem(1, 0.5, 0.2, 0.5, 0.3), ""},
		{"zero-area box valid: y0 == y1", anchorElem(1, 0.2, 0.5, 0.3, 0.5), ""},
		{"x0 just inside at 0", anchorElem(1, 0, 0.2, 0.3, 0.4), ""},
		{"x0 just outside below 0", anchorElem(1, -0.0001, 0.2, 0.3, 0.4), "is not a normalised box"},
		{"y0 just inside at 0", anchorElem(1, 0.1, 0, 0.3, 0.4), ""},
		{"y0 just outside below 0", anchorElem(1, 0.1, -0.0001, 0.3, 0.4), "is not a normalised box"},
		{"x1 just inside at 1", anchorElem(1, 0.1, 0.2, 1, 0.4), ""},
		{"x1 just outside above 1", anchorElem(1, 0.1, 0.2, 1.0001, 0.4), "is not a normalised box"},
		{"y1 just inside at 1", anchorElem(1, 0.1, 0.2, 0.3, 1), ""},
		{"y1 just outside above 1", anchorElem(1, 0.1, 0.2, 0.3, 1.0001), "is not a normalised box"},
		{"x0 > x1 inverted box", anchorElem(1, 0.6, 0.2, 0.5, 0.4), "is not a normalised box"},
		{"y0 > y1 inverted box", anchorElem(1, 0.2, 0.6, 0.4, 0.5), "is not a normalised box"},
		{"page 1 boundary is valid", anchorElem(1, 0.1, 0.1, 0.2, 0.2), ""},
		{"page 0 is below 1", anchorElem(0, 0.1, 0.1, 0.2, 0.2), "page 0 is below 1"},
		{"page -1 is below 1", anchorElem(-1, 0.1, 0.1, 0.2, 0.2), "below 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := extraction.UnmarshalAnchorObservations([]byte(c.raw))
			if c.wantErr == "" {
				if err != nil {
					t.Errorf("UnmarshalAnchorObservations(%s) error = %v, want nil", c.raw, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("UnmarshalAnchorObservations(%s) error = nil, want error containing %q", c.raw, c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("UnmarshalAnchorObservations(%s) error = %q, want it to contain %q", c.raw, err.Error(), c.wantErr)
			}
		})
	}
}

// O-08: a non-array top level is refused with a distinct, named error, not a bare JSON syntax
// error surfacing as a 500.
func TestAnchorObservationsCodec_RefusesANonArrayTopLevel(t *testing.T) {
	const wantErr = "layout_anchors: want a JSON array"

	for _, raw := range []string{`null`, `{}`, `"x"`, `3`} {
		t.Run(raw, func(t *testing.T) {
			got, err := extraction.UnmarshalAnchorObservations([]byte(raw))
			if err == nil {
				t.Fatalf("UnmarshalAnchorObservations(%s) = %+v, err = nil, want an error", raw, got)
			}
			if !strings.Contains(err.Error(), wantErr) {
				t.Errorf("UnmarshalAnchorObservations(%s) error = %q, want it to contain %q", raw, err.Error(), wantErr)
			}
		})
	}
}

// The positive control for the non-array table above: "[]" is a valid array and must decode
// with no error, to a non-nil, zero-length slice.
func TestAnchorObservationsCodec_EmptyArrayIsValid(t *testing.T) {
	got, err := extraction.UnmarshalAnchorObservations([]byte(`[]`))
	if err != nil {
		t.Fatalf("UnmarshalAnchorObservations([]) error = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("UnmarshalAnchorObservations([]) = nil, want a non-nil, zero-length slice")
	}
	if len(got) != 0 {
		t.Errorf("len(UnmarshalAnchorObservations([])) = %d, want 0", len(got))
	}
}

// O-10: AnchorLabelText truncates a matched label over the byte cap on a rune boundary, never
// splitting a multi-byte rune. issue_date's pattern ("date\s*of\s*issue") has an unbounded \s*,
// so a real match can run past any cap, and Go's case folding matches "issue" against "iſſue"
// (U+017F LATIN SMALL LETTER LONG S, 2 UTF-8 bytes) -- measured against the real pattern.
//
// pad=119 makes the naive 128-byte cut land inside the first U+017F (bytes 127-128 of the
// match); a correct, byte-safe truncation backs off to 127 bytes, ending after "i". pad=120
// shifts everything by one byte, so the 128-byte cut lands exactly after "i" already -- the
// truncation must NOT shorten by an extra rune when the cut is already clean. A rune-counting
// (rather than byte-counting) implementation would produce a 129-byte result for pad=119,
// failing the <=128 assertion -- that is what discriminates the two.
func TestAnchorLabelText_TruncatesOnAByteBoundary(t *testing.T) {
	for _, c := range []struct {
		pad       int
		wantBytes int
	}{
		{119, 127}, // cut lands mid-rune: back off to the rune boundary at "i"
		{120, 128}, // cut already lands on a rune boundary: must not shorten further
	} {
		t.Run(fmt.Sprintf("pad=%d", c.pad), func(t *testing.T) {
			text := "date" + strings.Repeat(" ", c.pad) + "of iſſue"
			token := extraction.Token{Text: text, Region: extraction.Region{Page: 1, X0: 0.1, Y0: 0.1, X1: 0.9, Y1: 0.12}}
			obs := extraction.AnchorObservation{Label: "issue_date", Page: 1, Band: 0, X0: 0.1, Y0: 0.1, X1: 0.9, Y1: 0.12}

			got := extraction.AnchorLabelText(obs, token)

			if len(got) > 128 {
				t.Errorf("len(AnchorLabelText) = %d, want <= 128 (the byte cap)", len(got))
			}
			if len(got) <= 100 {
				t.Errorf("len(AnchorLabelText) = %d, want > 100: an empty or near-empty return would also satisfy the weaker checks", len(got))
			}
			if !utf8.ValidString(got) {
				t.Errorf("AnchorLabelText returned invalid UTF-8: %q", got)
			}
			if !strings.HasPrefix(text, got) {
				t.Errorf("AnchorLabelText = %q, want a prefix of the full match %q", got, text)
			}
			if len(got) != c.wantBytes {
				t.Errorf("len(AnchorLabelText) = %d, want exactly %d bytes", len(got), c.wantBytes)
			}
		})
	}
}
