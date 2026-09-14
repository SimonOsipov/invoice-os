// drop_band_test.go: EXTR-31-03, the Tier-1 right drop band's production dial, measured and
// pinned inside its window from both sides. External package: reading the pdfium twin builds
// the pdfium pool.
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

// dbScannedGolden is the upper arm's fixture: SUBTOTAL's own value sits just below VAT's.
const dbScannedGolden = "wild_scanned_no_number.docling.json"

// The drop band's measured window. Lower and upper edges: [bound-set-by-pdfium], [dial-0.97].
const (
	dbDropLower     = 0.9494 // the pdfium twin's VAT needs t > 0.949395917528841
	dbDropTooNarrow = 0.9493 // the pdfium twin's vat loses 112.50
	dbDropUpper     = 1.0    // the widest clean dial; exclusive -- 135.00 sits at exactly t = 1
	dbDropMerges    = 1.0001 // the scanned golden's subtotal reaches VAT's 135.00
)

// dbWithDrop clones the shipped set and sets Drop on every right rule. Struct copy only, no
// re-parse, so a variant never forks the rule the fingerprint is built from (mirrors
// acWithDistance). Shipped rules carry Drop == 0 until subtask 04 wires the dial, so every arm
// here builds its own variant regardless of what tier1DropRight currently holds.
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

// dbTwinFields is the twin's six labelled fields, label -> printed value, both readers agree on
// (measured at HEAD 5d21771a).
var dbTwinFields = []struct{ field, value, ruleID string }{
	{"issue_date", "2026-07-30", "t1.issue_date.right"},
	{"buyer_name", "Honeywell Group", "t1.buyer_name.right"},
	{"currency", "NGN", "t1.currency.right"},
	{"subtotal", "1500.00", "t1.subtotal.right"},
	{"vat", "112.50", "t1.vat.right"},
	{"total", "1612.50", "t1.total.right"},
}

// dbAdjacentPage is the twin-proportioned page 03-T5 needs: a Sub total anchor whose own value
// sits in the drop band, and a second label on the anchor's own band with a competing amount
// beyond it. label2 "VAT" is a recognised anchor label and blocks the rightward read
// (crossesALabel); "Memo" is not and does not.
func dbAdjacentPage(label2 string) []extraction.TokenPage {
	return rvPage(
		rvTok("Sub total", 0.10, 0.500, 0.18, 0.511),
		rvTok("1,500.00", 0.20, 0.510, 0.27, 0.523),
		rvTok(label2, 0.30, 0.500, 0.34, 0.511),
		rvTok("112.50", 0.36, 0.510, 0.42, 0.521),
	)
}

// AC-1, AC-2, AC-3, AC-5. tier1DropRight sits inside the window from both sides, and the four
// arms that measured its edges.
func TestTier1_TheDropBandStaysInsideItsMeasuredWindow(t *testing.T) {
	// Explicit float64: Tier1DropRightForTest is an untyped constant and := would default an
	// integral value (e.g. the Stage 2.5 stub, 0) to int.
	var drop float64 = extraction.Tier1DropRightForTest
	if drop < dbDropLower || drop >= dbDropUpper {
		t.Errorf("tier1DropRight is %v, outside the measured window [%v, %v)", drop, dbDropLower, dbDropUpper)
	}

	// 03-T2. A guard: explicit dials on subtask 02's band, independent of tier1DropRight.
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

	// 03-T3. "At the dial" reads extraction.Tier1DropRightForTest, so this arm is red at the
	// Stage 2.5 stub (0) and turns green once the dial is set.
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

	// 03-T4. A guard: dbDropUpper and dbDropMerges are explicit literals, independent of
	// tier1DropRight. It bites once the dial is set to 0.97.
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

	// 03-T5. No rvFloor before the first assertion: the page resolves nothing at Drop 0, so a
	// floor here would fatal with the wrong message rather than the exact-equality mismatch.
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

// 03-T7, AC-2. resolve.go compares the offset (value.Y0 - anchor.Y0) against the product
// (drop * height), not a sum -- a rewrite into either sum form refuses this fixture where the
// offset form admits it, on both fused and unfused arithmetic.
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
