// drop_band_test.go: the Tier-1 right drop band's dial, pinned inside its measured window. External
// package: reading the pdfium twin builds the pdfium pool.
package extraction_test

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// The twin fixtures the lower and at-the-dial arms read.
const (
	dbTwinPDF    = "wild_stacked_borderless.pdf"
	dbTwinGolden = "wild_stacked_borderless.docling.json"
)

// dbScannedGolden is the upper arm's fixture: VAT's 135.00 starts exactly at SUBTOTAL's bottom edge.
const dbScannedGolden = "wild_scanned_no_number.docling.json"

// The drop band's measured window.
const (
	dbDropLower     = 0.9494 // the pdfium twin's VAT needs t > 0.949395917528841
	dbDropTooNarrow = 0.9493 // the pdfium twin's vat loses 112.50
	dbDropUpper     = 1.0    // the widest clean dial; exclusive -- 135.00 sits at exactly t = 1
	dbDropMerges    = 1.0001 // the scanned golden's subtotal reaches VAT's 135.00
)

// dbWithDrop clones the shipped set and sets Drop on every right rule; struct copy, no re-parse, as
// in acWithDistance. It fatals only on no right rule: a shipped rule may already carry drop.
func dbWithDrop(t *testing.T, drop float64) []extraction.Tier1Rule {
	t.Helper()

	out := slices.Clone(extraction.Tier1Rules)
	n := 0
	for i := range out {
		if out[i].Rule.Relation.Kind != extraction.RelRight {
			continue
		}
		out[i].Drop = drop
		n++
	}
	if n == 0 {
		t.Fatalf("no shipped rule uses relation %q; the variant is the shipped set and asserts nothing about the drop band", extraction.RelRight)
	}
	return out
}

// dbHasCandidate reports whether cs carries value from ruleID.
func dbHasCandidate(cs []extraction.Candidate, value, ruleID string) bool {
	for _, c := range cs {
		if c.Value == value && c.RuleID == ruleID {
			return true
		}
	}
	return false
}

// dbHasCandidateAt is dbHasCandidate plus an exact Distance check.
func dbHasCandidateAt(cs []extraction.Candidate, value, ruleID string, distance float64) bool {
	for _, c := range cs {
		if c.Value == value && c.RuleID == ruleID && math.Abs(c.Distance-distance) <= 1e-9 {
			return true
		}
	}
	return false
}

// dbTwinFields is the twin's six labelled fields and the value both readers print for each.
var dbTwinFields = []struct{ field, value, ruleID string }{
	{"issue_date", "2026-07-30", "t1.issue_date.right"},
	{"buyer_name", "Honeywell Group", "t1.buyer_name.right"},
	{"currency", "NGN", "t1.currency.right"},
	{"subtotal", "1500.00", "t1.subtotal.right"},
	{"vat", "112.50", "t1.vat.right"},
	{"total", "1612.50", "t1.total.right"},
}

// dbAdjacentPage puts label2 on the Sub total anchor's band, with a competing dropped amount beyond
// it. "VAT" is an anchor label and blocks the rightward read (crossesALabel); "Memo" does not.
func dbAdjacentPage(label2 string) []extraction.TokenPage {
	return rvPage(
		rvTok("Sub total", 0.10, 0.500, 0.18, 0.511),
		rvTok("1,500.00", 0.20, 0.510, 0.27, 0.523),
		rvTok(label2, 0.30, 0.500, 0.34, 0.511),
		rvTok("112.50", 0.36, 0.510, 0.42, 0.521),
	)
}

// tier1DropRight sits inside its measured window; each edge is pinned by the value that sets it.
func TestTier1_TheDropBandStaysInsideItsMeasuredWindow(t *testing.T) {
	// Explicit float64: the seam is an untyped constant, and := would make an integral dial an int.
	var drop float64 = extraction.Tier1DropRightForTest
	if drop < dbDropLower || drop >= dbDropUpper {
		t.Errorf("tier1DropRight is %v, outside the measured window [%v, %v)", drop, dbDropLower, dbDropUpper)
	}

	// Explicit dials, independent of tier1DropRight.
	t.Run("lower_bound", func(t *testing.T) {
		pages := rvCorpusPages(t, dbTwinPDF)

		narrow := extraction.Resolve(pages, extraction.RuleSet{Tier1: dbWithDrop(t, dbDropTooNarrow)})
		rvFloor(t, narrow, "the pdfium twin with Drop at "+fmt.Sprint(dbDropTooNarrow))
		if v := rvValues(rvFor(narrow, "vat")); slices.Contains(v, "112.50") {
			t.Errorf("vat = %v at Drop %v, want none: this is the value that pins the lower edge", v, dbDropTooNarrow)
		}

		at := extraction.Resolve(pages, extraction.RuleSet{Tier1: dbWithDrop(t, dbDropLower)})
		rvControl(t, rvFor(at, "vat"), fmt.Sprintf("vat at Drop %v", dbDropLower))
		if v := rvValues(rvFor(at, "vat")); !slices.Contains(v, "112.50") {
			t.Errorf("vat = %v at Drop %v, want it to contain 112.50", v, dbDropLower)
		}
	})

	// "At the dial" reads extraction.Tier1DropRightForTest.
	t.Run("at_the_dial", func(t *testing.T) {
		rules := extraction.RuleSet{Tier1: dbWithDrop(t, drop)}

		pdfium := extraction.Resolve(rvCorpusPages(t, dbTwinPDF), rules)
		rvFloor(t, pdfium, "the pdfium twin at the dial")

		_, doclingPages, _ := dcServeGolden(t, dcReadNamedGolden(t, dbTwinGolden))
		docling := extraction.Resolve(doclingPages, rules)
		rvFloor(t, docling, "the docling twin golden at the dial")

		for _, reader := range []struct {
			name string
			got  []extraction.Candidate
		}{
			{"pdfium", pdfium},
			{"docling", docling},
		} {
			for _, f := range dbTwinFields {
				if !dbHasCandidate(rvFor(reader.got, f.field), f.value, f.ruleID) {
					t.Errorf("%s = %v at the dial %v on the %s reading, want %q from %s", f.field, rvValues(rvFor(reader.got, f.field)), drop, reader.name, f.value, f.ruleID)
				}
			}
		}
	})

	// 1.0 stays clean only because the band is strict; dbDropMerges proves 135.00 is in reach.
	t.Run("upper_bound", func(t *testing.T) {
		_, pages, _ := dcServeGolden(t, dcReadNamedGolden(t, dbScannedGolden))

		for _, d := range []float64{drop, dbDropUpper} {
			got := extraction.Resolve(pages, extraction.RuleSet{Tier1: dbWithDrop(t, d)})
			rvFloor(t, got, fmt.Sprintf("%s at Drop %v", dbScannedGolden, d))
			v := rvValues(rvFor(got, "subtotal"))
			if slices.Contains(v, "135.00") {
				t.Errorf("subtotal = %v at Drop %v, want no 135.00: SUBTOTAL's own value must not reach VAT's", v, d)
			}
			if !slices.Contains(v, "1800.00") {
				t.Errorf("subtotal = %v at Drop %v, want it to contain 1800.00", v, d)
			}
		}

		merged := extraction.Resolve(pages, extraction.RuleSet{Tier1: dbWithDrop(t, dbDropMerges)})
		rvControl(t, rvFor(merged, "subtotal"), fmt.Sprintf("subtotal at Drop %v", dbDropMerges))
		v := rvValues(rvFor(merged, "subtotal"))
		for _, want := range []string{"135.00", "1800.00"} {
			if !slices.Contains(v, want) {
				t.Errorf("subtotal = %v at Drop %v, want it to contain %s", v, dbDropMerges, want)
			}
		}
	})

	// No rvFloor before the equality check: at Drop 0 the page resolves nothing, and a floor would
	// fatal before the mismatch prints.
	t.Run("adjacent_column", func(t *testing.T) {
		own := dbAdjacentPage("VAT")
		got := extraction.Resolve(own, extraction.RuleSet{Tier1: dbWithDrop(t, drop)})
		if v := rvValues(rvFor(got, "subtotal")); !slices.Equal(v, []string{"1500.00"}) {
			t.Errorf("subtotal = %v, want [1500.00]", v)
		}

		// Control: Memo is not a recognised anchor label, so it does not block the rightward
		// read and 112.50 joins as a subtotal candidate at the wider distance.
		memo := dbAdjacentPage("Memo")
		memoGot := extraction.Resolve(memo, extraction.RuleSet{Tier1: dbWithDrop(t, drop)})
		rvControl(t, rvFor(memoGot, "subtotal"), "subtotal via the Memo control at the dial")
		if !dbHasCandidateAt(rvFor(memoGot, "subtotal"), "112.50", "t1.subtotal.right", 0.18) {
			t.Errorf("subtotal = %v via the Memo control, want 112.50 from t1.subtotal.right at Distance 0.18", rvValues(rvFor(memoGot, "subtotal")))
		}

		// Control: at Drop 0 the Memo page's subtotal lacks 112.50 -- the positive control
		// above comes from the drop band, not the line band.
		memoZero := extraction.Resolve(memo, extraction.RuleSet{Tier1: dbWithDrop(t, 0)})
		if v := rvValues(rvFor(memoZero, "subtotal")); slices.Contains(v, "112.50") {
			t.Errorf("subtotal = %v on the Memo page at Drop 0, want no 112.50", v)
		}
	})
}

// The band compares the offset value.Y0-anchor.Y0 against drop*height, not a sum: both sum forms,
// fused or not, refuse this value where the offset form admits it.
func TestResolve_TheDropBandComparesTheOffsetNotTheSum(t *testing.T) {
	const drop = 0.97
	label := extraction.Region{Page: 1, X0: 0.10, Y0: 0.500, X1: 0.18, Y1: 0.511}
	value := extraction.Region{Page: 1, X0: 0.40, Y0: 0.51067, X1: 0.47, Y1: 0.523}
	h := label.Y1 - label.Y0

	// The explicit float64(...) conversion stops this line's own multiply-add from fusing, so
	// the self-check reads the same unfused arithmetic a naive rewrite would compile to.
	if value.Y0 < label.Y0+float64(drop*h) {
		t.Fatalf("value.Y0 %.20g already sits inside the plain sum-form band; this fixture cannot separate it from the offset form", value.Y0)
	}
	if value.Y0 < math.FMA(drop, h, label.Y0) {
		t.Fatalf("value.Y0 %.20g already sits inside the FMA sum-form band; this fixture cannot separate it from the offset form", value.Y0)
	}

	order, distance, overlap := extraction.RelationClausesForTest(label, value, extraction.RelRight, 0.35, drop)
	if order || distance || overlap {
		t.Errorf("relationClauses = (order=%v, distance=%v, overlap=%v), want all false: the offset form admits this value where both sum forms refuse it", order, distance, overlap)
	}

	// Control: at Drop 0 the same pair fails on overlap, the positive control for the zero above.
	if _, _, ctrlOverlap := extraction.RelationClausesForTest(label, value, extraction.RelRight, 0.35, 0); !ctrlOverlap {
		t.Errorf("at Drop 0, overlap = %v, want true", ctrlOverlap)
	}
}
