// learn_internal_test.go: C-agree. Package extraction, not extraction_test: this spec drives
// the request path's own unexported normalisedBox directly against the layout_anchors codec's
// predicate, so the two cannot silently drift onto different rules.
package extraction

import (
	"fmt"
	"math"
	"reflect"
	"testing"
)

// C-agree: normalisedBox (handlers_correction.go) and UnmarshalAnchorObservations' box
// predicate must accept and refuse the same boxes. A box the wire refuses that a worker could
// still store -- or the reverse -- is exactly the drift this spec exists to catch.
func TestNormalisedBoxAgreesWithTheAnchorCodecPredicate(t *testing.T) {
	table := []struct {
		name           string
		page           int
		x0, y0, x1, y1 float64
		wantAccept     bool
	}{
		{"all boundaries valid", 1, 0, 0, 1, 1, true},
		{"zero-area box (x0 == x1)", 1, 0.5, 0.2, 0.5, 0.3, true},
		{"zero-area box (y0 == y1)", 1, 0.2, 0.5, 0.3, 0.5, true},
		{"x0 below 0", 1, -0.0001, 0.2, 0.3, 0.4, false},
		{"y0 below 0", 1, 0.1, -0.0001, 0.3, 0.4, false},
		{"x1 above 1", 1, 0.1, 0.2, 1.0001, 0.4, false},
		{"y1 above 1", 1, 0.1, 0.2, 0.3, 1.0001, false},
		{"x0 > x1", 1, 0.6, 0.2, 0.5, 0.4, false},
		{"y0 > y1", 1, 0.2, 0.6, 0.4, 0.5, false},
		{"page 0", 0, 0.1, 0.1, 0.2, 0.2, false},
		{"page 1 boundary", 1, 0.1, 0.1, 0.2, 0.2, true},
	}

	for _, c := range table {
		t.Run(c.name, func(t *testing.T) {
			wireAccept := normalisedBox(ExtractionRegion{Page: c.page, X0: c.x0, Y0: c.y0, X1: c.x1, Y1: c.y1})
			if wireAccept != c.wantAccept {
				t.Fatalf("normalisedBox(%+v) = %v, want %v -- fixture disagrees with its own label; the codec comparison below would be meaningless", c, wireAccept, c.wantAccept)
			}

			raw := []byte(fmt.Sprintf(`[{"label":"total","text":"Total","page":%d,"band":0,"x0":%v,"y0":%v,"x1":%v,"y1":%v}]`,
				c.page, c.x0, c.y0, c.x1, c.y1))
			_, err := UnmarshalAnchorObservations(raw)
			codecAccept := err == nil

			if codecAccept != wireAccept {
				t.Errorf("%s: normalisedBox accepts = %v, codec accepts = %v (err=%v), want agreement", c.name, wireAccept, codecAccept, err)
			}
		})
	}
}

// --- EXTR-14-04: hundredthsAtLeast and betterAnchor --------------------------

// L-round: hundredthsAtLeast returns the smallest integer h >= 0 with h/100 >= g, over every
// exact hundredth, a hair either side of each, the domain's edges, and a dense sweep.
func TestHundredthsAtLeast_IsTheSmallestSufficientHundredth(t *testing.T) {
	check := func(t *testing.T, g float64) {
		t.Helper()
		h := hundredthsAtLeast(g)
		if h < 0 {
			t.Fatalf("hundredthsAtLeast(%v) = %d, want >= 0", g, h)
		}
		if float64(h)/100 < g {
			t.Errorf("hundredthsAtLeast(%v) = %d, but %d/100 = %v is under g", g, h, h, float64(h)/100)
		}
		if h > 0 && float64(h-1)/100 >= g {
			t.Errorf("hundredthsAtLeast(%v) = %d, but %d/100 = %v already satisfies >= g -- h is not the smallest", g, h, h-1, float64(h-1)/100)
		}
	}

	for i := 0; i <= 100; i++ {
		g := float64(i) / 100
		t.Run(fmt.Sprintf("exact %d/100", i), func(t *testing.T) { check(t, g) })
		t.Run(fmt.Sprintf("a hair under %d/100", i), func(t *testing.T) { check(t, math.Nextafter(g, 0)) })
		t.Run(fmt.Sprintf("a hair over %d/100", i), func(t *testing.T) { check(t, math.Nextafter(g, 1)) })
	}

	for _, g := range []float64{0, -1, -0.0001} {
		if h := hundredthsAtLeast(g); h != 0 {
			t.Errorf("hundredthsAtLeast(%v) = %d, want 0 for a non-positive gap", g, h)
		}
	}

	// A dense deterministic sweep stands in for the architecture pass's 3,000,000-sample
	// random check: same property, reproducible without a seed.
	for i := 0; i < 20000; i++ {
		check(t, float64(i)/20000)
	}
}

// L-round-naive is the needle that proves L-round is not vacuous: the naive
// math.Ceil(g*100)/100 over-rounds at these exact hundredths, and undershoots a hair above
// others -- both measured in .ralph/subtasks/extr-14-04-arch.md S:1.3.
func TestHundredthsAtLeast_TheNaiveCeilFormulaIsWrongAtTheseValues(t *testing.T) {
	for _, i := range []int{7, 14, 28, 55, 56} {
		g := float64(i) / 100
		if naive := int(math.Ceil(g * 100)); naive == i {
			t.Errorf("naive Ceil(%v*100) = %d, want it to over-round past %d -- this needle no longer demonstrates the bug hundredthsAtLeast fixes", g, naive, i)
		}
		if h := hundredthsAtLeast(g); h != i {
			t.Errorf("hundredthsAtLeast(%v) = %d, want %d -- the real function must not inherit the naive one's over-rounding", g, h, i)
		}
	}

	for _, i := range []int{35, 41, 47, 69, 70, 82, 83, 94, 95} {
		g := math.Nextafter(float64(i)/100, 1)
		if naive := math.Ceil(g*100) / 100; naive >= g {
			t.Errorf("naive Ceil(%v*100)/100 = %v, want it to undershoot g = %v -- this needle no longer demonstrates the bug", g, naive, g)
		}
		if h := hundredthsAtLeast(g); float64(h)/100 < g {
			t.Errorf("hundredthsAtLeast(%v) = %d, but %d/100 = %v is under g -- the real function must not undershoot either", g, h, h, float64(h)/100)
		}
	}
}

// L-total: betterAnchor is a strict total order over candidate -- for any two DISTINCT
// candidates, exactly one of betterAnchor(a,b)/betterAnchor(b,a) holds, and it is irreflexive.
// A permutation spec cannot catch a comparator that silently ignores one of the eight keys
// (compareCandidates's own oracle note, resolve.go); this is the direct spec that can. The
// adversarial set varies exactly one key at a time from base -- modelled on
// TestResolve_ComparatorIsTotal.
func TestBetterAnchor_IsATotalOrder(t *testing.T) {
	base := candidate{
		AnchorObservation: AnchorObservation{Label: "invoice_no", Text: "Invoice No", Page: 1, Band: 0, X0: 0.10, Y0: 0.10, X1: 0.20, Y1: 0.13},
		kind:              RelSameToken,
		gap:               0.10,
	}
	with := func(mut func(*candidate)) candidate {
		c := base
		mut(&c)
		return c
	}

	set := []struct {
		name string
		c    candidate
	}{
		{"base", base},
		{"gap", with(func(c *candidate) { c.gap = 0.05 })},
		{"kind", with(func(c *candidate) { c.kind = RelRight })},
		{"label (also varies key 3)", with(func(c *candidate) { c.Label = "issue_date"; c.Text = "Date" })},
		{"page", with(func(c *candidate) { c.Page = 2 })},
		{"y0", with(func(c *candidate) { c.Y0 = 0.15 })},
		{"x0", with(func(c *candidate) { c.X0 = 0.15 })},
		{"y1", with(func(c *candidate) { c.Y1 = 0.30 })},
		{"x1", with(func(c *candidate) { c.X1 = 0.30 })},
		{"band", with(func(c *candidate) { c.Band = 1 })},
		{"text", with(func(c *candidate) { c.Text = "Different Text" })},
	}

	// Floor: two identical entries would make the totality assertion below unsatisfiable and
	// the failure unreadable.
	for i := range set {
		for j := i + 1; j < len(set); j++ {
			if reflect.DeepEqual(set[i].c, set[j].c) {
				t.Fatalf("the adversarial set repeats itself: %s and %s are the same candidate", set[i].name, set[j].name)
			}
		}
	}

	for i := range set {
		if betterAnchor(set[i].c, set[i].c) {
			t.Errorf("betterAnchor is not irreflexive on %s: a candidate must not be better than itself", set[i].name)
		}
	}

	for i := range set {
		for j := i + 1; j < len(set); j++ {
			ij := betterAnchor(set[i].c, set[j].c)
			ji := betterAnchor(set[j].c, set[i].c)
			if ij == ji {
				t.Errorf("betterAnchor(%s, %s) = %v and betterAnchor(%s, %s) = %v: exactly one must hold for two distinct candidates, or the order is not total", set[i].name, set[j].name, ij, set[j].name, set[i].name, ji)
			}
		}
	}
}

// betterAnchor must consult gap (key 1) BEFORE relation kind (key 2): a closer right beats a
// farther same_token, even though same_token outranks right on key 2 alone.
func TestBetterAnchor_GapOutranksRelationKind(t *testing.T) {
	closerRight := candidate{AnchorObservation: AnchorObservation{Label: "x", Page: 1}, kind: RelRight, gap: 0.01}
	fartherSameToken := candidate{AnchorObservation: AnchorObservation{Label: "x", Page: 1}, kind: RelSameToken, gap: 0.20}

	if !betterAnchor(closerRight, fartherSameToken) {
		t.Error("betterAnchor(closer right, farther same_token) = false, want true -- gap must be compared first")
	}
	if betterAnchor(fartherSameToken, closerRight) {
		t.Error("betterAnchor(farther same_token, closer right) = true, want false")
	}
}

// betterAnchor must consult lexicon index (key 3) BEFORE the Label string itself (key 4):
// supplier_tin (lexicon index 2) beats buyer_tin (index 3) even though "buyer_tin" sorts first
// alphabetically -- a comparator that skipped straight to the string compare would pick wrong.
func TestBetterAnchor_LexiconIndexOutranksLabelString(t *testing.T) {
	if "buyer_tin" > "supplier_tin" {
		t.Fatal("this test assumes buyer_tin sorts before supplier_tin; it no longer does")
	}
	supplierTIN := candidate{AnchorObservation: AnchorObservation{Label: "supplier_tin", Text: "TIN", Page: 1}, kind: RelSameToken, gap: 0}
	buyerTIN := candidate{AnchorObservation: AnchorObservation{Label: "buyer_tin", Text: "TIN", Page: 1}, kind: RelSameToken, gap: 0}

	if !betterAnchor(supplierTIN, buyerTIN) {
		t.Error("betterAnchor(supplier_tin, buyer_tin) = false, want true -- lexicon index (2 < 3) must decide before the alphabetical compare would (which favours buyer_tin)")
	}
	if betterAnchor(buyerTIN, supplierTIN) {
		t.Error("betterAnchor(buyer_tin, supplier_tin) = true, want false")
	}
}
