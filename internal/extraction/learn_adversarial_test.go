// learn_adversarial_test.go: O-08, O-10 -- edge, boundary and negative cases for the
// layout_anchors codec and AnchorLabelText's byte cap, beyond learn_test.go's happy path.
// External package: every spec reaches only exported symbols. C-agree (the box predicate
// agreement with the request path's normalisedBox) lives in learn_internal_test.go instead,
// because normalisedBox is unexported.
package extraction_test

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
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

// A JSON string where the schema declares a number (page is int) must be refused, not silently
// coerced -- encoding/json never coerces string<->number, so this also guards against a future
// switch to a decoder that does.
func TestAnchorObservationsCodec_RefusesATypeMismatchedField(t *testing.T) {
	raw := []byte(`[{"label":"total","text":"Total","page":"1","band":0,"x0":0,"y0":0,"x1":0.1,"y1":0.1}]`)

	got, err := extraction.UnmarshalAnchorObservations(raw)
	if err == nil {
		t.Fatalf("UnmarshalAnchorObservations(page as a string) = %+v, err = nil, want an error", got)
	}
	if !strings.Contains(err.Error(), "layout_anchors:") {
		t.Errorf("UnmarshalAnchorObservations(page as a string) error = %q, want it to contain %q", err.Error(), "layout_anchors:")
	}
}

// A duplicate key in one element is not a decode error -- encoding/json applies both and keeps
// the last -- so the codec must reflect that behaviour rather than erroring or keeping the first.
func TestAnchorObservationsCodec_DuplicateKeyKeepsTheLastValue(t *testing.T) {
	raw := []byte(`[{"label":"total","label":"vat","text":"Total","page":1,"band":0,"x0":0,"y0":0,"x1":0.1,"y1":0.1}]`)

	got, err := extraction.UnmarshalAnchorObservations(raw)
	if err != nil {
		t.Fatalf("UnmarshalAnchorObservations(duplicate key) error = %v, want nil", err)
	}
	want := []extraction.AnchorObservation{{Label: "vat", Text: "Total", Page: 1, Band: 0, X0: 0, Y0: 0, X1: 0.1, Y1: 0.1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnmarshalAnchorObservations(duplicate key) = %+v, want %+v -- the second occurrence wins", got, want)
	}
}

// AnchorLabelText returns "" -- not a panic, not the token's full text -- when there is nothing
// to report: an empty token, a token whose text does not match the observation's own label, and
// a label the lexicon does not define at all.
func TestAnchorLabelText_ReturnsEmptyWhenNothingMatches(t *testing.T) {
	box := extraction.Region{Page: 1, X0: 0.1, Y0: 0.1, X1: 0.2, Y1: 0.12}
	knownObs := extraction.AnchorObservation{Label: "total", Page: 1, Band: 0, X0: box.X0, Y0: box.Y0, X1: box.X1, Y1: box.Y1}
	unknownObs := extraction.AnchorObservation{Label: "not_a_real_label", Page: 1, Band: 0, X0: box.X0, Y0: box.Y0, X1: box.X1, Y1: box.Y1}

	for _, c := range []struct {
		name string
		obs  extraction.AnchorObservation
		tok  extraction.Token
	}{
		{"empty token text", knownObs, extraction.Token{Text: "", Region: box}},
		{"known label, non-matching token", knownObs, extraction.Token{Text: "Invoice No: 5", Region: box}},
		{"label names no lexicon entry", unknownObs, extraction.Token{Text: "Total: 5", Region: box}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := extraction.AnchorLabelText(c.obs, c.tok); got != "" {
				t.Errorf("AnchorLabelText(%+v, %+v) = %q, want \"\"", c.obs, c.tok, got)
			}
		})
	}
}

// --- EXTR-14-04: LearnRule -- edge, boundary and negative cases --------------

// rvPermutations calls fn once per permutation of xs (Heap's algorithm), xs itself included.
func rvPermutations(xs []extraction.AnchorObservation, fn func([]extraction.AnchorObservation)) {
	n := len(xs)
	cur := append([]extraction.AnchorObservation(nil), xs...)
	fn(append([]extraction.AnchorObservation(nil), cur...))

	c := make([]int, n)
	for i := 0; i < n; {
		if c[i] < i {
			if i%2 == 0 {
				cur[0], cur[i] = cur[i], cur[0]
			} else {
				cur[c[i]], cur[i] = cur[i], cur[c[i]]
			}
			fn(append([]extraction.AnchorObservation(nil), cur...))
			c[i]++
			i = 0
		} else {
			c[i] = 0
			i++
		}
	}
}

// R-04: two anchors qualify via right; the nearer wins, and swapping input order does not
// change the winner.
func TestLearnRule_R04_NearestAnchorWinsRegardlessOfOrder(t *testing.T) {
	near := rvAnchor("near", "Near", 0.30, 0.10, 0.40, 0.13) // gap 0.10
	far := rvAnchor("far", "Far", 0.05, 0.10, 0.15, 0.13)    // gap 0.35, still within the cap
	region := extraction.Region{Page: 1, X0: 0.50, Y0: 0.10, X1: 0.60, Y1: 0.13}

	for _, order := range [][]extraction.AnchorObservation{{near, far}, {far, near}} {
		lr, ok := extraction.LearnRule("total", region, order)
		if !ok {
			t.Fatalf("LearnRule(total, order=%v) ok = false, want true", order)
		}
		if lr.Anchor.Label != "near" {
			t.Errorf("LearnRule(total, order=%v) anchor = %q, want %q (the smaller gap)", order, lr.Anchor.Label, "near")
		}
	}
}

// R-05: three anchors at an exactly equal gap resolve to the same rule across every permutation
// of the input slice. None names a real lexicon entry, so all three also tie on key 3 (lexicon
// index), and the string compare on Label (key 4) alone decides.
func TestLearnRule_R05_ExactTieIsStableAcrossEveryPermutation(t *testing.T) {
	region := extraction.Region{Page: 1, X0: 0.50, Y0: 0.10, X1: 0.60, Y1: 0.13}
	a := rvAnchor("aaa_not_in_lexicon", "A", 0.30, 0.10, 0.40, 0.13)
	b := rvAnchor("bbb_not_in_lexicon", "B", 0.30, 0.10, 0.40, 0.13)
	c := rvAnchor("ccc_not_in_lexicon", "C", 0.30, 0.10, 0.40, 0.13)

	var wantBody []byte
	n := 0
	rvPermutations([]extraction.AnchorObservation{a, b, c}, func(order []extraction.AnchorObservation) {
		n++
		lr, ok := extraction.LearnRule("total", region, order)
		if !ok {
			t.Fatalf("LearnRule(total, order=%v) ok = false, want true", order)
		}
		if lr.Anchor.Label != "aaa_not_in_lexicon" {
			t.Fatalf("LearnRule(total, order=%v) anchor = %q, want %q", order, lr.Anchor.Label, "aaa_not_in_lexicon")
		}
		if wantBody == nil {
			wantBody = lr.Body
		} else if string(lr.Body) != string(wantBody) {
			t.Errorf("LearnRule(total, order=%v) body = %s, want %s (the first permutation's body)", order, lr.Body, wantBody)
		}
	})
	if n != 6 {
		t.Fatalf("rvPermutations visited %d order(s), want 6 (3!) -- the loop above did not exercise every ordering", n)
	}
}

// R-06: a below gap of 0.061 (over the 0.06 dial) derives nothing; 0.059 (under it) derives.
func TestLearnRule_R06_BelowGapBoundary(t *testing.T) {
	anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.30, 0.13)

	over := extraction.Region{Page: 1, X0: 0.10, Y0: 0.191, X1: 0.30, Y1: 0.22} // gap 0.061
	if _, ok := extraction.LearnRule("total", over, []extraction.AnchorObservation{anchor}); ok {
		t.Error("LearnRule(total, gap 0.061) ok = true, want false -- over the below dial")
	}

	under := extraction.Region{Page: 1, X0: 0.10, Y0: 0.189, X1: 0.30, Y1: 0.22} // gap 0.059
	if _, ok := extraction.LearnRule("total", under, []extraction.AnchorObservation{anchor}); !ok {
		t.Error("LearnRule(total, gap 0.059) ok = false, want true -- the paired control")
	}
}

// R-07: a right gap of 0.36 (over the 0.35 dial) derives nothing; 0.34 (under it) derives.
func TestLearnRule_R07_RightGapBoundary(t *testing.T) {
	anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.20, 0.13)

	over := extraction.Region{Page: 1, X0: 0.56, Y0: 0.10, X1: 0.66, Y1: 0.13} // gap 0.36
	if _, ok := extraction.LearnRule("total", over, []extraction.AnchorObservation{anchor}); ok {
		t.Error("LearnRule(total, gap 0.36) ok = true, want false -- over the right dial")
	}

	under := extraction.Region{Page: 1, X0: 0.54, Y0: 0.10, X1: 0.64, Y1: 0.13} // gap 0.34
	if _, ok := extraction.LearnRule("total", under, []extraction.AnchorObservation{anchor}); !ok {
		t.Error("LearnRule(total, gap 0.34) ok = false, want true -- the paired control")
	}
}

// R-08: a box diagonally offset from the only anchor satisfies neither right's nor below's
// off-axis overlap test, and derives nothing.
func TestLearnRule_R08_DiagonalOffsetDerivesNothing(t *testing.T) {
	anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.20, 0.13)
	region := extraction.Region{Page: 1, X0: 0.30, Y0: 0.30, X1: 0.40, Y1: 0.33}

	if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); ok {
		t.Error("LearnRule(total, diagonal offset) ok = true, want false")
	}
}

// R-09: a nil anchors slice and an empty one both derive nothing and do not panic.
func TestLearnRule_R09_NilAndEmptyAnchorsDeriveNothing(t *testing.T) {
	region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.13}

	if _, ok := extraction.LearnRule("total", region, nil); ok {
		t.Error("LearnRule(total, nil anchors) ok = true, want false")
	}
	if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{}); ok {
		t.Error("LearnRule(total, empty anchors) ok = true, want false")
	}
}

// R-10: a region on page 2 does not anchor to a page-1 observation, even at an identical box.
func TestLearnRule_R10_PageMismatchDerivesNothing(t *testing.T) {
	anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.20, 0.13) // page 1
	region := extraction.Region{Page: 2, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.13}

	if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); ok {
		t.Error("LearnRule(total, region page 2, anchor page 1) ok = true, want false")
	}
}

// R-13: an anchor whose matched text ends in "." derives a label with no trailing \b (a literal
// "." is not a word byte), and that label still matches the token it came from.
func TestLearnRule_R13_TrailingDotLabelHasNoTrailingBoundary(t *testing.T) {
	anchor := extraction.AnchorObservation{Label: "invoice_no", Text: "Inv No.", Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}
	region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}

	lr, ok := extraction.LearnRule("invoice_number", region, []extraction.AnchorObservation{anchor})
	if !ok {
		t.Fatalf("LearnRule(invoice_number) ok = false, want true")
	}

	const wantLabel = `(?i)\bInv No\.`
	if lr.Rule.Label != wantLabel {
		t.Fatalf("LearnRule label = %q, want %q", lr.Rule.Label, wantLabel)
	}
	if strings.HasSuffix(lr.Rule.Label, `\b`) {
		t.Errorf(`label %q carries a trailing \b; a literal "." is not a word byte`, lr.Rule.Label)
	}
	if !regexp.MustCompile(lr.Rule.Label).MatchString("Inv No.") {
		t.Errorf("label %q does not match its own source text %q", lr.Rule.Label, "Inv No.")
	}
}

// R-15: a field outside HeaderFields derives nothing, even with a plainly qualifying anchor --
// the field gate rejects before any geometry is read.
func TestLearnRule_R15_UnknownFieldDerivesNothing(t *testing.T) {
	anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.30, 0.13)
	region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13} // overlaps -- would derive for a real field

	if _, ok := extraction.LearnRule("not_a_field", region, []extraction.AnchorObservation{anchor}); ok {
		t.Error("LearnRule(not_a_field) ok = true, want false")
	}
}

// L-ov-boundary-right: right's vertical-overlap gate at exactly half the shorter span, a hair
// under it, and the subnormal-span case resolve.go's own ov > 0 conjunct exists for
// (TestResolve_RejectsAZeroOverlapUnderASubnormalSpan).
func TestLearnRule_RightOverlapBoundary(t *testing.T) {
	t.Run("exactly half span derives", func(t *testing.T) {
		anchor := rvAnchor("a", "A", 0.10, 0.25, 0.20, 0.50) // Y span 0.25
		region := extraction.Region{Page: 1, X0: 0.30, Y0: 0.375, X1: 0.40, Y1: 0.625}
		if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); !ok {
			t.Error("LearnRule(total) ok = false, want true -- ov equals 0.5*span exactly")
		}
	})

	t.Run("a hair under half span derives nothing", func(t *testing.T) {
		anchor := rvAnchor("a", "A", 0.10, 0.25, 0.20, 0.50)
		region := extraction.Region{Page: 1, X0: 0.30, Y0: math.Nextafter(0.375, 1), X1: 0.40, Y1: 0.625}
		if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); ok {
			t.Error("LearnRule(total) ok = true, want false -- ov is one ULP under 0.5*span")
		}
	})

	t.Run("subnormal span with zero overlap derives nothing, a normal band derives", func(t *testing.T) {
		tiny := math.SmallestNonzeroFloat64
		anchor := rvAnchor("a", "InvoiceNo", 0.10, 0, 0.20, tiny)
		region := extraction.Region{Page: 1, X0: 0.30, Y0: tiny, X1: 0.40, Y1: 0.5}
		if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); ok {
			t.Error("LearnRule(total) ok = true, want false -- the two bands touch at a point and overlap by nothing")
		}

		normalAnchor := rvAnchor("a", "InvoiceNo", 0.10, 0.10, 0.20, 0.13)
		normalRegion := extraction.Region{Page: 1, X0: 0.30, Y0: 0.10, X1: 0.40, Y1: 0.13}
		if _, ok := extraction.LearnRule("total", normalRegion, []extraction.AnchorObservation{normalAnchor}); !ok {
			t.Error("LearnRule(total) ok = false, want true -- the same pair on a normal line band")
		}
	})
}

// L-ov-boundary-below: below's horizontal-overlap gate, mirrored on the other axis.
func TestLearnRule_BelowOverlapBoundary(t *testing.T) {
	t.Run("exactly half span derives", func(t *testing.T) {
		anchor := rvAnchor("a", "A", 0.25, 0.10, 0.50, 0.20) // X span 0.25
		region := extraction.Region{Page: 1, X0: 0.375, Y0: 0.22, X1: 0.625, Y1: 0.32}
		if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); !ok {
			t.Error("LearnRule(total) ok = false, want true -- ov equals 0.5*span exactly")
		}
	})

	t.Run("a hair under half span derives nothing", func(t *testing.T) {
		anchor := rvAnchor("a", "A", 0.25, 0.10, 0.50, 0.20)
		region := extraction.Region{Page: 1, X0: math.Nextafter(0.375, 1), Y0: 0.22, X1: 0.625, Y1: 0.32}
		if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); ok {
			t.Error("LearnRule(total) ok = true, want false -- ov is one ULP under 0.5*span")
		}
	})

	t.Run("subnormal span with zero overlap derives nothing, a normal column derives", func(t *testing.T) {
		tiny := math.SmallestNonzeroFloat64
		anchor := rvAnchor("a", "Buyer", 0, 0.10, tiny, 0.13)
		region := extraction.Region{Page: 1, X0: tiny, Y0: 0.15, X1: 0.5, Y1: 0.20}
		if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); ok {
			t.Error("LearnRule(total) ok = true, want false -- the two columns touch at a point and overlap by nothing")
		}

		normalAnchor := rvAnchor("a", "Buyer", 0.10, 0.10, 0.13, 0.13)
		normalRegion := extraction.Region{Page: 1, X0: 0.10, Y0: 0.15, X1: 0.13, Y1: 0.20}
		if _, ok := extraction.LearnRule("total", normalRegion, []extraction.AnchorObservation{normalAnchor}); !ok {
			t.Error("LearnRule(total) ok = false, want true -- the same pair on a normal column")
		}
	})
}

// L-at-the-cap: a below gap of exactly 0.06 and a right gap of exactly 0.35 each derive a rule
// whose max_distance equals the cap; one ULP over each derives nothing.
func TestLearnRule_GapExactlyAtTheCap(t *testing.T) {
	t.Run("below at 0.06 derives, one ULP over does not", func(t *testing.T) {
		anchor := rvAnchor("a", "VAT", 0.05, 0.04, 0.15, 0.10)
		atCap := extraction.Region{Page: 1, X0: 0.05, Y0: 0.16, X1: 0.15, Y1: 0.18}
		lr, ok := extraction.LearnRule("vat", atCap, []extraction.AnchorObservation{anchor})
		if !ok {
			t.Fatalf("LearnRule(vat, gap 0.06) ok = false, want true")
		}
		if lr.Rule.Relation.MaxDistance != 0.06 {
			t.Errorf("LearnRule(vat, gap 0.06) max_distance = %v, want 0.06", lr.Rule.Relation.MaxDistance)
		}

		overCap := extraction.Region{Page: 1, X0: 0.05, Y0: math.Nextafter(0.16, 1), X1: 0.15, Y1: 0.18}
		if _, ok := extraction.LearnRule("vat", overCap, []extraction.AnchorObservation{anchor}); ok {
			t.Error("LearnRule(vat, one ULP over 0.06) ok = true, want false")
		}
	})

	t.Run("right at 0.35 derives, one ULP over does not", func(t *testing.T) {
		anchor := rvAnchor("a", "Sub", 0.05, 0.05, 0.12, 0.08)
		atCap := extraction.Region{Page: 1, X0: 0.47, Y0: 0.05, X1: 0.57, Y1: 0.08}
		lr, ok := extraction.LearnRule("subtotal", atCap, []extraction.AnchorObservation{anchor})
		if !ok {
			t.Fatalf("LearnRule(subtotal, gap 0.35) ok = false, want true")
		}
		if lr.Rule.Relation.MaxDistance != 0.35 {
			t.Errorf("LearnRule(subtotal, gap 0.35) max_distance = %v, want 0.35", lr.Rule.Relation.MaxDistance)
		}

		overCap := extraction.Region{Page: 1, X0: math.Nextafter(0.47, 1), Y0: 0.05, X1: 0.57, Y1: 0.08}
		if _, ok := extraction.LearnRule("subtotal", overCap, []extraction.AnchorObservation{anchor}); ok {
			t.Error("LearnRule(subtotal, one ULP over 0.35) ok = true, want false")
		}
	})
}

// L-degenerate-region: a zero-area region and a page-0 region both derive nothing and do not
// panic -- both are admitted by the wire's normalisedBox, so LearnRule must refuse them itself.
func TestLearnRule_DegenerateRegionDerivesNothing(t *testing.T) {
	anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.30, 0.13) // would derive same_token against a normal region at the same box

	for _, c := range []struct {
		name   string
		region extraction.Region
	}{
		{"zero width (x0 == x1)", extraction.Region{Page: 1, X0: 0.20, Y0: 0.10, X1: 0.20, Y1: 0.13}},
		{"zero height (y0 == y1)", extraction.Region{Page: 1, X0: 0.10, Y0: 0.115, X1: 0.30, Y1: 0.115}},
		{"page below 1", extraction.Region{Page: 0, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, ok := extraction.LearnRule("total", c.region, []extraction.AnchorObservation{anchor}); ok {
				t.Errorf("LearnRule(total, %s) ok = true, want false", c.name)
			}
		})
	}

	control := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}
	if _, ok := extraction.LearnRule("total", control, []extraction.AnchorObservation{anchor}); !ok {
		t.Error("LearnRule(total, the same box, non-degenerate) ok = false, want true -- the positive control")
	}
}

// L-cap-bytes: an AnchorObservation.Text of 512 bytes -- a DB row the codec would accept, since
// it checks no length on Text -- derives a label of at most 264 bytes that ParseRule accepts.
func TestLearnRule_LabelStaysUnderTheByteCapEvenFromA512ByteText(t *testing.T) {
	anchor := extraction.AnchorObservation{
		Label: "a", Text: strings.Repeat("$", 512), // '$' is escaped by QuoteMeta and is not a word byte -- the worst case
		Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.90, Y1: 0.13,
	}
	region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.90, Y1: 0.13}

	lr, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor})
	if !ok {
		t.Fatalf("LearnRule(total) ok = false, want true")
	}
	if len(lr.Rule.Label) != 260 {
		t.Errorf(`len(label) = %d, want 260: "(?i)" (4) + 128 escaped "$" (256), no \b on either side`, len(lr.Rule.Label))
	}
	if len(lr.Rule.Label) > 264 {
		t.Errorf("len(label) = %d, over the measured 264-byte worst case", len(lr.Rule.Label))
	}
	if _, err := extraction.ParseRule(lr.Body); err != nil {
		t.Errorf("ParseRule(%s) error = %v, want nil", lr.Body, err)
	}
}

// L-overlap-boundary: boxes touching on exactly one edge are not an overlap -- that derives
// right, at distance 0. A one-ULP interpenetration IS an overlap, and wins as same_token
// instead (checked first).
func TestLearnRule_TouchingEdgeIsRightNotSameToken(t *testing.T) {
	anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.30, 0.20)

	t.Run("touching exactly is right at distance 0", func(t *testing.T) {
		region := extraction.Region{Page: 1, X0: 0.30, Y0: 0.10, X1: 0.40, Y1: 0.20}
		lr, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor})
		if !ok {
			t.Fatalf("LearnRule(total) ok = false, want true")
		}
		if lr.Rule.Relation.Kind != extraction.RelRight {
			t.Errorf("LearnRule(total) relation = %q, want %q", lr.Rule.Relation.Kind, extraction.RelRight)
		}
		if lr.Rule.Relation.MaxDistance != 0 {
			t.Errorf("LearnRule(total) max_distance = %v, want 0", lr.Rule.Relation.MaxDistance)
		}
	})

	t.Run("one ULP of interpenetration is same_token", func(t *testing.T) {
		region := extraction.Region{Page: 1, X0: math.Nextafter(0.30, 0), Y0: 0.10, X1: 0.40, Y1: 0.20}
		lr, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor})
		if !ok {
			t.Fatalf("LearnRule(total) ok = false, want true")
		}
		if lr.Rule.Relation.Kind != extraction.RelSameToken {
			t.Errorf("LearnRule(total) relation = %q, want %q", lr.Rule.Relation.Kind, extraction.RelSameToken)
		}
	})
}

// L-empty-text: an observation whose Text trims to "" is not a candidate -- an empty pattern
// would match every token.
func TestLearnRule_EmptyTextIsNotACandidate(t *testing.T) {
	region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}
	blank := extraction.AnchorObservation{Label: "a", Text: "   ", Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}

	if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{blank}); ok {
		t.Error("LearnRule(total, whitespace-only Text) ok = true, want false")
	}

	real := extraction.AnchorObservation{Label: "b", Text: "Real Label", Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}
	lr, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{blank, real})
	if !ok {
		t.Fatalf("LearnRule(total, [blank, real]) ok = false, want true -- the blank one must be skipped, not fatal")
	}
	if lr.Anchor.Label != "b" {
		t.Errorf("LearnRule(total, [blank, real]) anchor = %q, want %q", lr.Anchor.Label, "b")
	}
}

// An interior control byte in the anchor's Text (an OCR/extraction artifact, not something
// TrimSpace removes) survives QuoteMeta unescaped, and jsonString escapes only backslash and
// quote -- so the derived body carries a raw control byte inside a JSON string, which
// encoding/json refuses. LearnRule must refuse rather than persist that row: an honest
// ok=false with a zero-value LearnedRule, not a body a later ParseRule call would choke on.
func TestLearnRule_InteriorControlByteRefusesRatherThanEmitsAnUnparseableBody(t *testing.T) {
	region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}
	anchor := extraction.AnchorObservation{Label: "a", Text: "TIN\n123", Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}

	lr, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor})
	if ok {
		t.Fatalf("LearnRule(total, interior control byte) ok = true, want false; body = %s", lr.Body)
	}
	if lr.Body != nil {
		t.Errorf("LearnRule(total, interior control byte) Body = %q, want nil (zero value)", lr.Body)
	}
	if lr.Field != "" {
		t.Errorf("LearnRule(total, interior control byte) Field = %q, want \"\" (zero value)", lr.Field)
	}

	// Positive control: the same box, the control byte removed, derives normally -- the
	// refusal above is about the control byte, not the fixture's geometry.
	clean := extraction.AnchorObservation{Label: "a", Text: "TIN123", Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}
	if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{clean}); !ok {
		t.Error("LearnRule(total, control byte removed) ok = false, want true -- the positive control")
	}
}

// D-3's "word-bounded where the outer runes allow": a label whose entire matched text is
// non-word bytes carries no \b on either side, and still parses and matches its own source.
func TestLearnRule_AllNonWordLabelHasNoBoundariesEitherSide(t *testing.T) {
	region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.13}
	anchor := extraction.AnchorObservation{Label: "a", Text: "#", Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.13}

	lr, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor})
	if !ok {
		t.Fatalf("LearnRule(total, all-non-word text) ok = false, want true")
	}
	const wantLabel = `(?i)#`
	if lr.Rule.Label != wantLabel {
		t.Fatalf("LearnRule label = %q, want %q", lr.Rule.Label, wantLabel)
	}
	if !regexp.MustCompile(lr.Rule.Label).MatchString("Invoice # 4") {
		t.Errorf("label %q does not match a real token carrying it, %q", lr.Rule.Label, "Invoice # 4")
	}
}

// Anchor.Text at exactly the 128-byte cap is not truncated; one byte past it loses exactly the
// last byte. Both are ASCII, so the byte cut and the rune cut coincide -- this isolates the
// off-by-one at the boundary itself, distinct from L-cap-bytes' 512-byte worst case.
func TestLearnRule_TextAtAndOverThe128ByteCap(t *testing.T) {
	region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.90, Y1: 0.13}

	atCap := extraction.AnchorObservation{Label: "a", Text: strings.Repeat("x", 128), Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.90, Y1: 0.13}
	lr, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{atCap})
	if !ok {
		t.Fatalf("LearnRule(total, 128-byte text) ok = false, want true")
	}
	// "(?i)" (4) + `\b` (2 bytes: backslash, b) + 128 "x" + `\b` (2): "x" is a word byte, so
	// both boundaries apply.
	if want := 4 + 2 + 128 + 2; len(lr.Rule.Label) != want {
		t.Errorf("len(label) = %d, want %d -- the 128-byte text must not be truncated", len(lr.Rule.Label), want)
	}

	overCap := extraction.AnchorObservation{Label: "a", Text: strings.Repeat("x", 129), Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.90, Y1: 0.13}
	lr2, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{overCap})
	if !ok {
		t.Fatalf("LearnRule(total, 129-byte text) ok = false, want true")
	}
	if want := 4 + 2 + 128 + 2; len(lr2.Rule.Label) != want {
		t.Errorf("len(label) = %d, want %d -- the 129th byte must be dropped, not carried through", len(lr2.Rule.Label), want)
	}
	if lr.Rule.Label != lr2.Rule.Label {
		t.Errorf("128-byte and 129-byte text produced different labels (%q vs %q); truncation should make them identical", lr.Rule.Label, lr2.Rule.Label)
	}
}

// NaN/Inf coordinates are refused before they ever reach betterAnchor: usableBox rejects a
// non-finite region outright, and a non-finite anchor box is filtered per-candidate, leaving a
// well-formed anchor to win instead of panicking or comparing NaN.
func TestLearnRule_NonFiniteCoordinatesAreRefusedNotCompared(t *testing.T) {
	t.Run("NaN region is refused", func(t *testing.T) {
		region := extraction.Region{Page: 1, X0: math.NaN(), Y0: 0.10, X1: 0.30, Y1: 0.13}
		anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.30, 0.13)
		if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); ok {
			t.Error("LearnRule(total, NaN region) ok = true, want false")
		}
	})

	t.Run("+Inf region is refused", func(t *testing.T) {
		region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: math.Inf(1), Y1: 0.13}
		anchor := rvAnchor("a", "Label", 0.10, 0.10, 0.30, 0.13)
		if _, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{anchor}); ok {
			t.Error("LearnRule(total, +Inf region) ok = true, want false")
		}
	})

	t.Run("NaN anchor is skipped, a well-formed anchor still wins", func(t *testing.T) {
		region := extraction.Region{Page: 1, X0: 0.10, Y0: 0.10, X1: 0.30, Y1: 0.13}
		nanAnchor := extraction.AnchorObservation{Label: "nan", Text: "NaN", Page: 1, Band: 0, X0: math.NaN(), Y0: 0.10, X1: 0.30, Y1: 0.13}
		goodAnchor := rvAnchor("good", "Label", 0.10, 0.10, 0.30, 0.13)

		lr, ok := extraction.LearnRule("total", region, []extraction.AnchorObservation{nanAnchor, goodAnchor})
		if !ok {
			t.Fatalf("LearnRule(total, [NaN, good]) ok = false, want true")
		}
		if lr.Anchor.Label != "good" {
			t.Errorf("LearnRule(total, [NaN, good]) anchor = %q, want %q -- the NaN anchor must be skipped, not win by comparing false", lr.Anchor.Label, "good")
		}
	})
}

// Two anchors at an equal gap of 0 but different relations: one overlaps the region
// (same_token), the other only touches its right edge (right, gap 0 -- L-overlap-boundary's
// "touching exactly" case). betterAnchor's key 2 must decide, and same_token -- the one
// relation requiring literal box containment, not merely a nearby box -- must win.
func TestLearnRule_EqualGapAcrossRelationsPicksSameTokenOverRight(t *testing.T) {
	region := extraction.Region{Page: 1, X0: 0.30, Y0: 0.10, X1: 0.40, Y1: 0.13}
	overlapping := extraction.AnchorObservation{Label: "overlap", Text: "Overlap", Page: 1, Band: 0, X0: 0.25, Y0: 0.10, X1: 0.35, Y1: 0.13}
	touchingRight := extraction.AnchorObservation{Label: "touch", Text: "Touch", Page: 1, Band: 0, X0: 0.20, Y0: 0.10, X1: 0.30, Y1: 0.13}

	for _, order := range [][]extraction.AnchorObservation{{overlapping, touchingRight}, {touchingRight, overlapping}} {
		lr, ok := extraction.LearnRule("total", region, order)
		if !ok {
			t.Fatalf("LearnRule(total, order=%v) ok = false, want true", order)
		}
		if lr.Anchor.Label != "overlap" {
			t.Errorf("LearnRule(total, order=%v) anchor = %q, want %q (same_token at equal gap 0)", order, lr.Anchor.Label, "overlap")
		}
		if lr.Rule.Relation.Kind != extraction.RelSameToken {
			t.Errorf("LearnRule(total, order=%v) relation = %q, want %q", order, lr.Rule.Relation.Kind, extraction.RelSameToken)
		}
	}
}

// --- EXTR-19-07: LearnBoxlessRule, the refusals ---------------------------------------
//
// Every refusal below carries a control that must DERIVE. A lone `ok == false` assertion is
// green against any function that refuses everything, which proves nothing about the refusal
// it names.

// lbxLabelTexts is every matched text AnchorObservations recorded for one lexicon id on a
// boxless page -- production's own view of which labels a token carries.
func lbxLabelTexts(obs []extraction.AnchorObservation, label string) []string {
	var out []string
	for _, o := range obs {
		if o.Label == label {
			out = append(out, o.Text)
		}
	}
	return out
}

// AC-3: zero qualifying tokens refuses. Must-fail mutation: return the first lexicon hit
// regardless of value.
func TestLearnBoxlessRule_RefusesWhenNoTokenCarriesTheValue(t *testing.T) {
	lr, ok := extraction.LearnBoxlessRule("total", "999.99", dxParagraphs)
	if ok {
		t.Errorf("LearnBoxlessRule(total, %q, dxParagraphs) ok = true, body = %s, want false: no token reads 999.99", "999.99", lr.Body)
	}
	if !reflect.DeepEqual(lr, extraction.LearnedRule{}) {
		t.Errorf("LearnBoxlessRule(total, %q) refused with %+v, want the zero LearnedRule", "999.99", lr)
	}

	// Control: the same field and the same tokens derive for the value the page does carry, so
	// the refusal above keys on the value, not on the fixture.
	got, ok := extraction.LearnBoxlessRule("total", "4300.00", dxParagraphs)
	if !ok {
		t.Fatalf("control: LearnBoxlessRule(total, %q, dxParagraphs) ok = false, want true", "4300.00")
	}
	if string(got.Body) != lbxTotalBody {
		t.Errorf("control: body = %s, want %s", got.Body, lbxTotalBody)
	}
}

// AC-4: two qualifying hits producing different bodies refuse -- an ambiguous derivation is a
// guess. Must-fail mutation: take the first match and stop.
func TestLearnBoxlessRule_RefusesTwoDistinctDerivations(t *testing.T) {
	tokens := []string{"Total: 300.00", "Amount Due: 300.00"}

	// Both tokens must hit the SAME lexicon id with DIFFERENT matched text, or the refusal is
	// an incidental miss rather than the body test firing.
	texts := lbxLabelTexts(extraction.AnchorObservations(lbxPage(tokens)), "total")
	if len(texts) != 2 {
		t.Fatalf("lexicon id %q matched %d of %v (%q), want exactly 2", "total", len(texts), tokens, texts)
	}
	if !(texts[0] == "Total" && texts[1] == "Amount Due") && !(texts[0] == "Amount Due" && texts[1] == "Total") {
		t.Fatalf("lexicon id %q matched %q, want the distinct pair {\"Total\", \"Amount Due\"}: the refusal below needs two DIFFERENT labels", "total", texts)
	}

	if lr, ok := extraction.LearnBoxlessRule("total", "300.00", tokens); ok {
		t.Errorf("LearnBoxlessRule(total, %q, %v) ok = true, body = %s, want false: two distinct labels read 300.00", "300.00", tokens, lr.Body)
	}

	// Must-stay-green: with the second amount at 301.00 only one hit qualifies, the same two
	// tokens derive, and the refusal above is about the choice, not about the fixture size.
	green := []string{"Total: 300.00", "Amount Due: 301.00"}
	lr, ok := extraction.LearnBoxlessRule("total", "300.00", green)
	if !ok {
		t.Fatalf("control: LearnBoxlessRule(total, %q, %v) ok = false, want true", "300.00", green)
	}
	if string(lr.Body) != lbxTotalBody {
		t.Errorf("control: body = %s, want %s", lr.Body, lbxTotalBody)
	}
}

// AC-5: hits that all produce the SAME body derive it -- deduping is on the body, not on the
// hit count. Must-fail mutation: dedupe on the (token index, label) pair.
func TestLearnBoxlessRule_DerivesWhenADuplicatedValueYieldsOneRule(t *testing.T) {
	twice := []string{"Total: 300.00", "Some line", "Total: 300.00"}
	lr, ok := extraction.LearnBoxlessRule("total", "300.00", twice)
	if !ok {
		t.Fatalf("LearnBoxlessRule(total, %q, %v) ok = false, want true: both hits derive one body", "300.00", twice)
	}
	if len(lr.Body) == 0 {
		t.Fatal("derived body is empty; the identity below would be vacuous")
	}
	if string(lr.Body) != lbxTotalBody {
		t.Errorf("LearnBoxlessRule(total, the duplicated total) body = %s, want %s", lr.Body, lbxTotalBody)
	}

	once := []string{"Total: 300.00"}
	single, ok := extraction.LearnBoxlessRule("total", "300.00", once)
	if !ok {
		t.Fatalf("LearnBoxlessRule(total, %q, %v) ok = false, want true", "300.00", once)
	}
	if string(lr.Body) != string(single.Body) {
		t.Errorf("body over %v = %s, want byte-identical to the body over %v = %s", twice, lr.Body, once, single.Body)
	}
}

// AC-6: the refusal keys on the tier1Specs shape lookup, not on the field name -- the field
// lock is refuseField, upstream. Must-fail mutation: default the shape on a tier1Shape miss.
func TestLearnBoxlessRule_RefusesAFieldNoRuleCanFill(t *testing.T) {
	lr, ok := extraction.LearnBoxlessRule("not_a_field", "ASC-2026-0919", dxParagraphs)
	if ok {
		t.Errorf("LearnBoxlessRule(not_a_field, %q, dxParagraphs) ok = true, body = %s, want false", "ASC-2026-0919", lr.Body)
	}
	if !reflect.DeepEqual(lr, extraction.LearnedRule{}) {
		t.Errorf("LearnBoxlessRule(not_a_field) refused with %+v, want the zero LearnedRule", lr)
	}

	// Must-stay-green: invoice_number IS in tier1Specs, so the same token derives. A test
	// asserting only the refusal cannot tell a shape-lookup miss from a field-name lock.
	got, ok := extraction.LearnBoxlessRule("invoice_number", "ASC-2026-0919", dxParagraphs)
	if !ok {
		t.Fatalf("control: LearnBoxlessRule(invoice_number, %q, dxParagraphs) ok = false, want true: invoice_number is in tier1Specs", "ASC-2026-0919")
	}
	const want = `{"label":"(?i)\\bInvoice No\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"invoice_number"}`
	if string(got.Body) != want {
		t.Errorf("control: body = %s, want %s", got.Body, want)
	}
}

// AC-7: a label that is empty after capAnchorLabelBytes and TrimSpace refuses, rather than
// emitting a pattern that matches every token. Must-fail mutation: ignore learnedLabel's ok.
func TestLearnBoxlessRule_RefusesAnEmptyLabel(t *testing.T) {
	tok := "x" + strings.Repeat(" ", 200) + "tin: 12345678-0001"

	// The lexicon really does match this token starting at the boundary after "x", so the 128
	// capped bytes are pure whitespace. Without this the refusal could be "nothing matched".
	// bare_tin is the id that claims a party-less TIN label since EXTR-22-02.
	texts := lbxLabelTexts(extraction.AnchorObservations(lbxPage([]string{tok})), "bare_tin")
	if len(texts) != 1 {
		t.Fatalf("lexicon id %q matched %d time(s) on the fixture (%q), want exactly 1", "bare_tin", len(texts), texts)
	}
	if texts[0] == "" || strings.TrimSpace(texts[0]) != "" {
		t.Fatalf("matched text = %q (%d bytes), want non-empty whitespace that trims to empty", texts[0], len(texts[0]))
	}

	if lr, ok := extraction.LearnBoxlessRule("supplier_tin", "12345678-0001", []string{tok}); ok {
		t.Errorf("LearnBoxlessRule(supplier_tin, the whitespace-swallowed match) ok = true, body = %s, want false", lr.Body)
	}

	// Control: drop the leading "x" and the match starts at the label itself, so the same field
	// and the same value derive.
	plain := []string{"tin: 12345678-0001"}
	got, ok := extraction.LearnBoxlessRule("supplier_tin", "12345678-0001", plain)
	if !ok {
		t.Fatalf("control: LearnBoxlessRule(supplier_tin, %q, %v) ok = false, want true", "12345678-0001", plain)
	}
	const want = `{"label":"(?i)\\btin\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
	if string(got.Body) != want {
		t.Errorf("control: body = %s, want %s", got.Body, want)
	}
}

// AC-9: sameTokenValue's loc comes from the emitted label's own compiled pattern, not from the
// lexicon matcher's match. Must-fail mutation: take loc from the matcher -- the function then
// derives a rule that resolves nothing, which the second half below measures.
func TestLearnBoxlessRule_TakesLocFromTheEmittedLabelNotTheMatcher(t *testing.T) {
	tok := "supplier" + strings.Repeat(" ", 600) + "tin: 12345678-0001"

	if lr, ok := extraction.LearnBoxlessRule("supplier_tin", "12345678-0001", []string{tok}); ok {
		t.Errorf("LearnBoxlessRule(supplier_tin, the 600-space token) ok = true, body = %s, want false: the emitted label ends at byte 8 and its remainder is not a TIN", lr.Body)
	}

	// What the matcher-loc reading would emit. It parses, so the mutation reaches Resolve
	// rather than dying at ParseRule -- and Resolve then finds nothing on the very token the
	// rule was derived from.
	const mutant = `{"label":"(?i)\\bsupplier\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
	pages := lbxPage([]string{tok})
	mutantRule, err := extraction.ParseRule([]byte(mutant))
	if err != nil {
		t.Fatalf("ParseRule(%s) error = %v, want nil", mutant, err)
	}
	if got := lbxResolveTIN(pages, mutantRule); len(got) != 0 {
		t.Errorf("Resolve(the 600-space token, the matcher-loc rule) = %+v, want no candidate at all", got)
	}

	// Control needle: Resolve CAN see a candidate on this token, so the zero above is a
	// property of the rule and not of the fixture.
	const working = `{"label":"(?i)\\btin\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
	workingRule, err := extraction.ParseRule([]byte(working))
	if err != nil {
		t.Fatalf("ParseRule(%s) error = %v, want nil", working, err)
	}
	got := lbxResolveTIN(pages, workingRule)
	if len(got) != 1 || got[0].Value != "12345678-0001" || got[0].Tier != extraction.TierLearned {
		t.Fatalf("Resolve(the 600-space token, the label-loc rule) = %+v, want exactly one TierLearned candidate %q", got, "12345678-0001")
	}

	// Must-stay-green: when the cap does not move the match end, the same field and value
	// derive -- so the refusal above is about the moved end, not about the fixture family.
	//
	// ceiling: the party-bearing spelling "supplier tin: ..." can no longer serve as this
	// control. Since EXTR-22-02 bare_tin derives a SECOND, distinct body from the same token
	// and LearnBoxlessRule refuses the pair, so a boxless pointed correction on an inline party
	// TIN teaches nothing. Revisit when the learn path applies anchorOutranked as Resolve does.
	plain := []string{"tin: 12345678-0001"}
	lr, ok := extraction.LearnBoxlessRule("supplier_tin", "12345678-0001", plain)
	if !ok {
		t.Fatalf("control: LearnBoxlessRule(supplier_tin, %q, %v) ok = false, want true", "12345678-0001", plain)
	}
	const want = `{"label":"(?i)\\btin\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
	if string(lr.Body) != want {
		t.Errorf("control: body = %s, want %s", lr.Body, want)
	}
}

// lbxResolveTIN runs one learned supplier_tin rule over pages and returns its candidates.
func lbxResolveTIN(pages []extraction.TokenPage, rule extraction.Rule) []extraction.Candidate {
	return extraction.Resolve(pages, extraction.RuleSet{Learned: []extraction.AnchorRule{
		{ID: "learned-boxless", Field: "supplier_tin", Rule: rule},
	}})
}

// AC-10: a token two lexicon ids match refuses. Accepted rather than porting anchorOutranked,
// which would silently pick the wider label on a genuinely nested pair.
func TestLearnBoxlessRule_RefusesATokenTwoLexiconIdsMatch(t *testing.T) {
	tokens := []string{"Sub-total: 300.00"}

	// Name both matched texts: if the lexicon stops overlapping here, this reds instead of
	// passing on a refusal that has become an ordinary no-hit.
	obs := extraction.AnchorObservations(lbxPage(tokens))
	sub, total := lbxLabelTexts(obs, "subtotal"), lbxLabelTexts(obs, "total")
	if len(sub) != 1 || sub[0] != "Sub-total" {
		t.Fatalf("lexicon id %q matched %q on %q, want exactly [\"Sub-total\"]", "subtotal", sub, tokens[0])
	}
	if len(total) != 1 || total[0] != "total" {
		t.Fatalf("lexicon id %q matched %q on %q, want exactly [\"total\"]", "total", total, tokens[0])
	}

	if lr, ok := extraction.LearnBoxlessRule("total", "300.00", tokens); ok {
		t.Errorf("LearnBoxlessRule(total, %q, %v) ok = true, body = %s, want false: subtotal and total both read 300.00", "300.00", tokens, lr.Body)
	}

	// Must-stay-green: one lexicon id on the token, one body, and it derives.
	single := []string{"Total: 300.00"}
	if got := extraction.AnchorObservations(lbxPage(single)); len(got) != 1 {
		t.Fatalf("the control token %q carries %d lexicon match(es) (%+v), want exactly 1", single[0], len(got), got)
	}
	lr, ok := extraction.LearnBoxlessRule("total", "300.00", single)
	if !ok {
		t.Fatalf("control: LearnBoxlessRule(total, %q, %v) ok = false, want true", "300.00", single)
	}
	if string(lr.Body) != lbxTotalBody {
		t.Errorf("control: body = %s, want %s", lr.Body, lbxTotalBody)
	}
}

// --- EXTR-19-07 QA: the branches the ten ACs leave uncovered ---------------------------
//
// Recon A-2 called three guards unreachable. Two of them fire on ordinary DOCX text, measured
// below, so each gets a spec: a reachable branch with no test is a branch nothing pins.

// AC-7, second arm. The shipped empty-label spec uses a supplier_tin fixture, and ShapeTIN
// rejects the whole token either way -- so it refuses whether or not learnedLabel's ok is
// honoured, and cannot tell the guard from the shape. Under ShapeName the whole token IS a
// reading, so ignoring that ok derives {"label":"", ...} -- a rule that matches every token of
// every later document. Must-fail mutation: `if !ok` -> `if !ok && false` at learn.go:120.
func TestLearnBoxlessRule_NeverEmitsAnEmptyLabel(t *testing.T) {
	tok := "x" + strings.Repeat(" ", 130) + "tin: Acme Trading Ltd"

	// The lexicon really matches, and the 128 capped bytes are pure whitespace, so learnedLabel
	// is what refuses. Without this the refusal below could be "nothing matched".
	texts := lbxLabelTexts(extraction.AnchorObservations(lbxPage([]string{tok})), "bare_tin")
	if len(texts) != 1 {
		t.Fatalf("lexicon id %q matched %d time(s) on the fixture, want exactly 1", "bare_tin", len(texts))
	}
	if texts[0] == "" || strings.TrimSpace(texts[0]) != "" {
		t.Fatalf("matched text = %q (%d bytes), want non-empty whitespace that trims to empty", texts[0], len(texts[0]))
	}

	// ShapeName reads the whole token, so a hit carrying the empty label WOULD qualify. Without
	// this the refusal is the shape's and the guard could be deleted unnoticed.
	if got := extraction.ShapeName.Normalize(tok); len(got) != 1 || got[0] != tok {
		t.Fatalf("ShapeName.Normalize(the fixture) = %q, want exactly one reading equal to the token", got)
	}

	if lr, ok := extraction.LearnBoxlessRule("supplier_name", tok, []string{tok}); ok {
		t.Errorf("LearnBoxlessRule(supplier_name, the whitespace-swallowed match) ok = true, body = %s, want false", lr.Body)
	}

	// The harm the guard prevents, measured: ParseRule accepts an empty label, and the rule it
	// compiles fires on a token that has nothing to do with the fixture.
	const empty = `{"label":"","relation":{"kind":"same_token","max_distance":0.00},"shape":"name"}`
	emptyRule, err := extraction.ParseRule([]byte(empty))
	if err != nil {
		t.Fatalf("ParseRule(%s) error = %v, want nil: the guard, not ParseRule, is what refuses", empty, err)
	}
	stranger := lbxPage([]string{"Nothing here resembles the fixture"})
	if got := extraction.Resolve(stranger, extraction.RuleSet{Learned: []extraction.AnchorRule{
		{ID: "empty-label", Field: "supplier_name", Rule: emptyRule},
	}}); len(got) == 0 {
		t.Error("Resolve(an unrelated token, the empty-label rule) returned no candidate; the empty label is meant to match everything, so this spec's premise is wrong")
	}

	// Must-stay-green: a match the cap does not swallow derives for the same field.
	plain := []string{"Supplier: Acme Trading Ltd"}
	lr, ok := extraction.LearnBoxlessRule("supplier_name", "Acme Trading Ltd", plain)
	if !ok {
		t.Fatalf("control: LearnBoxlessRule(supplier_name, %q, %v) ok = false, want true", "Acme Trading Ltd", plain)
	}
	const want = `{"label":"(?i)\\bSupplier\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"name"}`
	if string(lr.Body) != want {
		t.Errorf("control: body = %s, want %s", lr.Body, want)
	}
}

// Recon A-2 says ParseRule can never reject a derived body. It can: jsonString escapes only \
// and ", so a raw tab or newline inside the matched label spells invalid JSON. A DOCX paragraph
// carries tabs, so this is the story's own input class. The hit is skipped, which is safe --
// but it is the ParseRule guard, not the shape, that does it.
func TestLearnBoxlessRule_RefusesALabelJSONCannotSpell(t *testing.T) {
	for _, tc := range []struct{ name, tok string }{
		{"interior tab", "Amount\tDue: 300.00"},
		{"interior newline", "Amount\nDue: 300.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The lexicon matches across the control byte, so the refusal is not a miss.
			texts := lbxLabelTexts(extraction.AnchorObservations(lbxPage([]string{tc.tok})), "total")
			if len(texts) != 1 || !strings.ContainsAny(texts[0], "\t\n") {
				t.Fatalf("lexicon id %q matched %q, want exactly one match carrying the control byte", "total", texts)
			}

			// ParseRule is what refuses, named directly so a jsonString that learns to escape
			// control bytes reds here rather than silently changing the derivation.
			body := `{"label":"(?i)\\b` + texts[0] + `\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"amount"}`
			if _, err := extraction.ParseRule([]byte(body)); err == nil {
				t.Fatalf("ParseRule(%q) error = nil, want non-nil: this spec's premise is that the body does not parse", body)
			}

			if lr, ok := extraction.LearnBoxlessRule("total", "300.00", []string{tc.tok}); ok {
				t.Errorf("LearnBoxlessRule(total, %q) ok = true, body = %s, want false", tc.tok, lr.Body)
			}
		})
	}

	// Must-stay-green: the same label spelled with a plain space derives, so the refusals above
	// are about the control byte and not about the "Amount Due" fixture family.
	plain := []string{"Amount Due: 300.00"}
	lr, ok := extraction.LearnBoxlessRule("total", "300.00", plain)
	if !ok {
		t.Fatalf("control: LearnBoxlessRule(total, %q, %v) ok = false, want true", "300.00", plain)
	}
	const want = `{"label":"(?i)\\bAmount Due\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"amount"}`
	if string(lr.Body) != want {
		t.Errorf("control: body = %s, want %s", lr.Body, want)
	}
}

// Recon A-2 also says the emitted label always re-finds on its own token. It does not: the
// 128-byte cap can land inside the label's trailing word, leaving a \b that cannot hold. The
// consequence is not cosmetic -- dropping that hit turns a two-body refusal into a derivation.
// Must-fail mutation: `if loc == nil { continue }` -> `if loc == nil { loc = mloc }` at
// learn.go:138, and the 122-dash arm refuses instead of deriving.
func TestLearnBoxlessRule_ACappedLabelThatCannotRefindIsDropped(t *testing.T) {
	// 120 dashes: the subtotal match is exactly 128 bytes, the cap splits nothing, its label
	// re-finds, and its body competes with total's -- two distinct bodies, so a refusal.
	fits := []string{"sub" + strings.Repeat("-", 120) + "total: 300.00"}
	// 122 dashes: the cap now falls inside the trailing "total", so the emitted label ends
	// \btot\b, which cannot match "tota" -- the hit is dropped and only total's body survives.
	splits := []string{"sub" + strings.Repeat("-", 122) + "total: 300.00"}

	for _, tokens := range [][]string{fits, splits} {
		texts := lbxLabelTexts(extraction.AnchorObservations(lbxPage(tokens)), "subtotal")
		if len(texts) != 1 || len(texts[0]) != 128 {
			t.Fatalf("lexicon id %q matched %d text(s) of %d bytes on %d dashes, want exactly one capped to 128", "subtotal", len(texts), len(texts[0]), len(tokens[0]))
		}
		if got := lbxLabelTexts(extraction.AnchorObservations(lbxPage(tokens)), "total"); len(got) != 1 || got[0] != "total" {
			t.Fatalf("lexicon id %q matched %q, want exactly [\"total\"]: the second body this spec turns on", "total", got)
		}
	}

	// The two fixtures differ only in whether the cap splits a word.
	if lr, ok := extraction.LearnBoxlessRule("subtotal", "300.00", fits); ok {
		t.Errorf("LearnBoxlessRule(subtotal, 120 dashes) ok = true, body = %s, want false: two labels read 300.00", lr.Body)
	}
	lr, ok := extraction.LearnBoxlessRule("subtotal", "300.00", splits)
	if !ok {
		t.Fatalf("LearnBoxlessRule(subtotal, 122 dashes) ok = false, want true: the split label cannot re-find, so only one body survives")
	}
	if string(lr.Body) != lbxTotalBodyLower {
		t.Errorf("LearnBoxlessRule(subtotal, 122 dashes) body = %s, want %s", lr.Body, lbxTotalBodyLower)
	}
}

// lbxTotalBodyLower is lbxTotalBody's lower-case twin: learnedLabel QuoteMetas the matched text
// verbatim, so "Total" and "total" are different labels and different bodies.
const lbxTotalBodyLower = `{"label":"(?i)\\btotal\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"amount"}`

// Every shipped fixture puts its label at byte 0, so none of them can tell a derivation that
// searches the token from one that only reads its head. Must-fail mutation: add
// `|| loc[0] != 0` to the loc guard at learn.go:138.
func TestLearnBoxlessRule_DerivesFromALabelInsideTheToken(t *testing.T) {
	tokens := []string{"Invoice total: 300.00"}

	texts := lbxLabelTexts(extraction.AnchorObservations(lbxPage(tokens)), "total")
	if len(texts) != 1 || texts[0] != "total" {
		t.Fatalf("lexicon id %q matched %q, want exactly [\"total\"]", "total", texts)
	}
	if got := strings.Index(tokens[0], texts[0]); got == 0 {
		t.Fatalf("the label sits at byte 0 of %q; this spec needs it inside the token", tokens[0])
	}

	lr, ok := extraction.LearnBoxlessRule("total", "300.00", tokens)
	if !ok {
		t.Fatalf("LearnBoxlessRule(total, %q, %v) ok = false, want true", "300.00", tokens)
	}
	if string(lr.Body) != lbxTotalBodyLower {
		t.Errorf("LearnBoxlessRule(total, a label inside the token) body = %s, want %s", lr.Body, lbxTotalBodyLower)
	}

	// The round trip is what makes the offset real rather than arithmetic.
	got := extraction.Resolve(lbxPage(tokens), extraction.RuleSet{Learned: []extraction.AnchorRule{
		{ID: "learned-boxless", Field: "total", Rule: lr.Rule},
	}})
	if len(got) != 1 || got[0].Value != "300.00" || got[0].Tier != extraction.TierLearned {
		t.Errorf("Resolve(%v, the derived rule) = %+v, want exactly one TierLearned candidate %q", tokens, got, "300.00")
	}
}

// The lexicon id never reaches the body: Resolve applies a learned rule to the field its ROW
// names, so the derivation must not restrict itself to the field's own label. Accepted, and
// sharp -- correcting supplier_tin here writes "the TIN is whatever follows Total". Must-fail
// mutation: skip a matcher whose m.ID is not tier1Specs' label id for field.
func TestLearnBoxlessRule_TheAnchorsLexiconIdDoesNotConstrainTheField(t *testing.T) {
	tokens := []string{"Total: 12345678-0001"}

	texts := lbxLabelTexts(extraction.AnchorObservations(lbxPage(tokens)), "supplier_tin")
	if len(texts) != 0 {
		t.Fatalf("lexicon id %q matched %q on %q, want none: the point is that the field's OWN label is absent", "supplier_tin", texts, tokens[0])
	}

	lr, ok := extraction.LearnBoxlessRule("supplier_tin", "12345678-0001", tokens)
	if !ok {
		t.Fatalf("LearnBoxlessRule(supplier_tin, %q, %v) ok = false, want true", "12345678-0001", tokens)
	}
	const want = `{"label":"(?i)\\bTotal\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`
	if string(lr.Body) != want {
		t.Errorf("LearnBoxlessRule(supplier_tin, a total-labelled TIN) body = %s, want %s", lr.Body, want)
	}
	if lr.Anchor.Label != "total" {
		t.Errorf("anchor label = %q, want %q: the anchor records which lexicon entry hit, the body records the field's shape", lr.Anchor.Label, "total")
	}
}

// Two hits can share a body and differ in Anchor.Text -- capAnchorLabelBytes runs before
// TrimSpace, so " tin" and "tin" emit one label and two anchors. The body is what is stored, so
// D-27 holds; the anchor is the FIRST hit in token order and reaches the correction row.
// Must-fail mutation: update anchor on every hit instead of only the first (learn.go:152-153).
func TestLearnBoxlessRule_KeepsTheFirstAnchorWhenTwoHitsShareABody(t *testing.T) {
	const spaced, bare = "Acme tin: 12345678-0001", "tin: 12345678-0001"
	const wantBody = `{"label":"(?i)\\btin\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"tin"}`

	// The two tokens really do produce different matched texts, or there is nothing to order.
	spacedText := lbxLabelTexts(extraction.AnchorObservations(lbxPage([]string{spaced})), "bare_tin")
	bareText := lbxLabelTexts(extraction.AnchorObservations(lbxPage([]string{bare})), "bare_tin")
	if len(spacedText) != 1 || len(bareText) != 1 {
		t.Fatalf("lexicon id %q matched %q and %q, want exactly one each", "bare_tin", spacedText, bareText)
	}
	if spacedText[0] == bareText[0] {
		t.Fatalf("both tokens matched %q; this spec needs two DIFFERENT matched texts that share one label", spacedText[0])
	}

	for _, tc := range []struct {
		name   string
		tokens []string
		anchor string
	}{
		{"spaced first", []string{spaced, bare}, spacedText[0]},
		{"bare first", []string{bare, spaced}, bareText[0]},
	} {
		lr, ok := extraction.LearnBoxlessRule("supplier_tin", "12345678-0001", tc.tokens)
		if !ok {
			t.Fatalf("%s: LearnBoxlessRule(supplier_tin, %q, %v) ok = false, want true: one body, so no ambiguity", tc.name, "12345678-0001", tc.tokens)
		}
		if string(lr.Body) != wantBody {
			t.Errorf("%s: body = %s, want %s: the stored bytes must not depend on token order", tc.name, lr.Body, wantBody)
		}
		if lr.Anchor.Text != tc.anchor {
			t.Errorf("%s: anchor text = %q, want %q: the first hit in token order wins", tc.name, lr.Anchor.Text, tc.anchor)
		}
	}

	// The difference is erased where the anchor lands: handlers_correction.go writes
	// strings.TrimSpace(lr.Anchor.Text) into the correction row.
	if strings.TrimSpace(spacedText[0]) != strings.TrimSpace(bareText[0]) {
		t.Errorf("TrimSpace(%q) = %q and TrimSpace(%q) = %q; the correction row would record two different anchor_labels for one rule",
			spacedText[0], strings.TrimSpace(spacedText[0]), bareText[0], strings.TrimSpace(bareText[0]))
	}
}

// readsAs compares a reading to the value with ==, and the value is never a pattern. Must-fail
// mutation: strings.HasPrefix(v, value) instead of v == value at learn.go:165 -- an empty
// correction then matches every reading and the first arm reds.
func TestLearnBoxlessRule_RefusesAnEmptyValueAndSkipsEmptyTokens(t *testing.T) {
	qualifying := []string{"Total: 300.00"}

	// Floor: the fixture is one a non-empty value derives from, so the refusal below is the
	// empty value's doing.
	if _, ok := extraction.LearnBoxlessRule("total", "300.00", qualifying); !ok {
		t.Fatal("the fixture does not derive for 300.00; every arm below would be vacuous")
	}
	if lr, ok := extraction.LearnBoxlessRule("total", "", qualifying); ok {
		t.Errorf("LearnBoxlessRule(total, the empty value, %v) ok = true, body = %s, want false", qualifying, lr.Body)
	}

	// A nil and an empty token list are the same refusal, not a panic and a refusal.
	for _, tokens := range [][]string{nil, {}} {
		if lr, ok := extraction.LearnBoxlessRule("total", "300.00", tokens); ok {
			t.Errorf("LearnBoxlessRule(total, %q, %#v) ok = true, body = %s, want false", "300.00", tokens, lr.Body)
		}
	}

	// An empty token carries no lexicon match and must not disturb one that does.
	withBlanks := []string{"", "Total: 300.00", ""}
	lr, ok := extraction.LearnBoxlessRule("total", "300.00", withBlanks)
	if !ok {
		t.Fatalf("LearnBoxlessRule(total, %q, %v) ok = false, want true", "300.00", withBlanks)
	}
	if string(lr.Body) != lbxTotalBody {
		t.Errorf("LearnBoxlessRule(total, blanks around the hit) body = %s, want %s", lr.Body, lbxTotalBody)
	}
}

// A shape can read one token two ways. readsAs must try every reading, not the first: an
// ambiguous numeric date reads day-first and month-first, and the reviewer's correction is what
// says which. Must-fail mutation: read only shape.Normalize(raw)[0] at learn.go:164.
func TestLearnBoxlessRule_MatchesAnyReadingOfAnAmbiguousShape(t *testing.T) {
	tokens := []string{"Date: 12/03/2026"}

	// Two readings, and the one this spec corrects to is NOT the first -- otherwise reading
	// only the head would pass.
	readings := extraction.ShapeDate.Normalize("12/03/2026")
	if len(readings) != 2 {
		t.Fatalf("ShapeDate.Normalize(%q) = %q, want exactly two readings", "12/03/2026", readings)
	}
	const want = "2026-12-03"
	if readings[0] == want {
		t.Fatalf("ShapeDate.Normalize(%q)[0] = %q, want the corrected value to be the SECOND reading", "12/03/2026", readings[0])
	}
	if readings[1] != want {
		t.Fatalf("ShapeDate.Normalize(%q) = %q, want %q among them", "12/03/2026", readings, want)
	}

	lr, ok := extraction.LearnBoxlessRule("issue_date", want, tokens)
	if !ok {
		t.Fatalf("LearnBoxlessRule(issue_date, %q, %v) ok = false, want true", want, tokens)
	}
	const wantBody = `{"label":"(?i)\\bDate\\b","relation":{"kind":"same_token","max_distance":0.00},"shape":"date"}`
	if string(lr.Body) != wantBody {
		t.Errorf("LearnBoxlessRule(issue_date, the month-first reading) body = %s, want %s", lr.Body, wantBody)
	}
}
