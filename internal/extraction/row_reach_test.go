// row_reach_test.go: the Tier-1 row reach (01-T1..T12) -- a bare amount label reads the first
// token on its own line past the right dial. External package: Resolve behaviour specs.
package extraction_test

import (
	"math"
	"reflect"
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rrWithRowReach clones the shipped set and sets RowReach on the three amount right rules;
// struct copy, no re-parse, as dbWithDrop does for Drop.
func rrWithRowReach(t *testing.T) []extraction.Tier1Rule {
	t.Helper()

	targets := map[string]bool{"t1.subtotal.right": true, "t1.vat.right": true, "t1.total.right": true}
	out := slices.Clone(extraction.Tier1Rules)
	n := 0
	for i := range out {
		if targets[out[i].Key] {
			out[i].RowReach = true
			n++
		}
	}
	if n != 3 {
		t.Fatalf("set RowReach on %d rule(s), want exactly 3", n)
	}
	return out
}

// rrRule returns a copy of the shipped rule keyed key, so a single-rule variant can be built
// without exercising the rest of the shipped set.
func rrRule(t *testing.T, key string) extraction.Tier1Rule {
	t.Helper()

	for _, r := range extraction.Tier1Rules {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no shipped rule keyed %q", key)
	return extraction.Tier1Rule{}
}

// 01-T1..T3
func TestResolve_TheRowReachReadsTheFirstTokenOnTheLine(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}

	t.Run("01-T1 reads the first token past the dial", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		sub := rvFor(got, "subtotal")
		if want := []string{"9999.00"}; !slices.Equal(rvValues(sub), want) {
			t.Fatalf("subtotal = %v, want %v", rvValues(sub), want)
		}
		if !dbHasCandidateAt(sub, "9999.00", "t1.subtotal.right", 0.65) {
			t.Errorf("subtotal candidate = %+v, want Distance 0.65 from t1.subtotal.right", sub)
		}
		if !sub[0].Adjacent {
			t.Errorf("Adjacent = false, want true")
		}
	})

	t.Run("01-T1 control: the shipped set has no candidate", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, extraction.RuleSet{Tier1: extraction.Tier1Rules})
		if sub := rvFor(got, "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %v, want none: the shipped dial does not reach 0.65", rvValues(sub))
		}
	})

	t.Run("01-T2 a closer amount becomes the first token", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("1,000.00", 0.55, 0.700, 0.61, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		sub := rvFor(got, "subtotal")
		if want := []string{"1000.00"}; !slices.Equal(rvValues(sub), want) {
			t.Fatalf("subtotal = %v, want %v", rvValues(sub), want)
		}
		if !dbHasCandidateAt(sub, "1000.00", "t1.subtotal.right", 0.40) {
			t.Errorf("subtotal candidate = %+v, want Distance 0.40 from t1.subtotal.right", sub)
		}
	})

	t.Run("01-T3 a bare word first on the line ends the reach", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("Memo", 0.55, 0.700, 0.60, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		if sub := rvFor(got, "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %v, want none: Memo is the first token on the line", rvValues(sub))
		}
	})
}

// 01-T4, T5, T5b, T5c
func TestResolve_TheRowReachStopsAtAnotherColumnsValue(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}

	t.Run("01-T4 a label in another column blocks the reach", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("VAT", 0.60, 0.700, 0.64, 0.710),
			rvTok("75.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		if sub := rvFor(got, "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %v, want none: VAT is the first token on the line and the shape refuses it", rvValues(sub))
		}
		vat := rvFor(got, "vat")
		if want := []string{"75.00"}; !slices.Equal(rvValues(vat), want) {
			t.Fatalf("vat = %v, want %v", rvValues(vat), want)
		}
		if !dbHasCandidateAt(vat, "75.00", "t1.vat.right", 0.16) {
			t.Errorf("vat candidate = %+v, want Distance 0.16 from t1.vat.right", vat)
		}
	})

	t.Run("01-T5 a value stacked under another label is refused", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("Balance due", 0.75, 0.685, 0.85, 0.695),
			rvTok("5,000.00", 0.75, 0.700, 0.81, 0.710),
		)
		got := extraction.Resolve(page, rules)
		if sub := rvFor(got, "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %v, want none: Balance due owns the value stacked below it", rvValues(sub))
		}
		total := rvFor(got, "total")
		if want := []string{"5000.00"}; !slices.Equal(rvValues(total), want) {
			t.Fatalf("total = %v, want %v", rvValues(total), want)
		}
		if !dbHasCandidateAt(total, "5000.00", "t1.total.below", 0.005) {
			t.Errorf("total candidate = %+v, want Distance 0.005 from t1.total.below", total)
		}
	})

	t.Run("01-T5 control: a non-label word above the value loses nothing", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("Memo", 0.75, 0.685, 0.85, 0.695),
			rvTok("5,000.00", 0.75, 0.700, 0.81, 0.710),
		)
		got := extraction.Resolve(page, rules)
		sub := rvFor(got, "subtotal")
		if want := []string{"5000.00"}; !slices.Equal(rvValues(sub), want) {
			t.Fatalf("subtotal = %v, want %v; the control fatals if it reaches nothing", rvValues(sub), want)
		}
		if !dbHasCandidateAt(sub, "5000.00", "t1.subtotal.right", 0.60) {
			t.Errorf("subtotal candidate = %+v, want Distance 0.60 from t1.subtotal.right", sub)
		}
	})

	t.Run("01-T5b a same-field owner loses nothing", func(t *testing.T) {
		page := rvPage(
			rvTok("Total", 0.10, 0.700, 0.15, 0.710),
			rvTok("Total", 0.80, 0.685, 0.86, 0.695),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		total := rvFor(got, "total")
		if want := []string{"9999.00"}; !slices.Equal(rvValues(total), want) {
			t.Fatalf("total = %v, want %v", rvValues(total), want)
		}
		if !dbHasCandidateAt(total, "9999.00", "t1.total.below", 0.005) {
			t.Errorf("total candidate = %+v, want Distance 0.005 from t1.total.below", total)
		}
		for _, c := range total {
			if c.RuleID == "t1.total.right" {
				t.Errorf("total candidate %+v came from t1.total.right, want none: the stacked owner refuses the reach", c)
			}
		}
	})

	t.Run("01-T5c a non-amount label stacked over the value pins the ownedBelow ceiling", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("Date", 0.80, 0.685, 0.86, 0.695),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		if sub := rvFor(got, "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %v, want none: Date owns the value stacked below it (the accepted ceiling)", rvValues(sub))
		}
	})

	t.Run("01-T5c control: a non-label token in the same box loses nothing", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("Amount ₦", 0.80, 0.685, 0.86, 0.695),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		sub := rvFor(got, "subtotal")
		if want := []string{"9999.00"}; !slices.Equal(rvValues(sub), want) {
			t.Fatalf("subtotal = %v, want %v", rvValues(sub), want)
		}
		if !dbHasCandidateAt(sub, "9999.00", "t1.subtotal.right", 0.65) {
			t.Errorf("subtotal candidate = %+v, want Distance 0.65 from t1.subtotal.right", sub)
		}
	})

	t.Run("01-T5c control: an offset label with no column overlap loses nothing", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("VAT", 0.60, 0.685, 0.64, 0.695),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		sub := rvFor(got, "subtotal")
		if want := []string{"9999.00"}; !slices.Equal(rvValues(sub), want) {
			t.Fatalf("subtotal = %v, want %v", rvValues(sub), want)
		}
		if !dbHasCandidateAt(sub, "9999.00", "t1.subtotal.right", 0.65) {
			t.Errorf("subtotal candidate = %+v, want Distance 0.65 from t1.subtotal.right", sub)
		}
	})
}

// 01-T6
func TestResolve_TheRowReachNeedsABareLabel(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}

	t.Run("a label carrying other words gets no reach (vat)", func(t *testing.T) {
		page := rvPage(
			rvTok("VAT compliance health check", 0.10, 0.700, 0.28, 0.710),
			rvTok("2,850,000.00", 0.78, 0.700, 0.85, 0.710),
		)
		got := extraction.Resolve(page, rules)
		if vat := rvFor(got, "vat"); len(vat) != 0 {
			t.Errorf("vat = %v, want none: the label carries words outside its lexicon match", rvValues(vat))
		}
	})

	t.Run("control: a bare vat label reaches", func(t *testing.T) {
		page := rvPage(
			rvTok("VAT 7.5%", 0.10, 0.700, 0.16, 0.710),
			rvTok("2,850,000.00", 0.78, 0.700, 0.85, 0.710),
		)
		got := extraction.Resolve(page, rules)
		vat := rvFor(got, "vat")
		if want := []string{"2850000.00"}; !slices.Equal(rvValues(vat), want) {
			t.Fatalf("vat = %v, want %v", rvValues(vat), want)
		}
		if !dbHasCandidateAt(vat, "2850000.00", "t1.vat.right", 0.62) {
			t.Errorf("vat candidate = %+v, want Distance 0.62 from t1.vat.right", vat)
		}
	})

	t.Run("a label carrying other words gets no reach (total)", func(t *testing.T) {
		page := rvPage(
			rvTok("Page Total", 0.10, 0.700, 0.20, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		if total := rvFor(got, "total"); len(total) != 0 {
			t.Errorf("total = %v, want none: the label carries a word outside its lexicon match", rvValues(total))
		}
	})

	t.Run("control: a bare total label reaches", func(t *testing.T) {
		page := rvPage(
			rvTok("Total", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		total := rvFor(got, "total")
		if want := []string{"9999.00"}; !slices.Equal(rvValues(total), want) {
			t.Fatalf("total = %v, want %v", rvValues(total), want)
		}
		if !dbHasCandidateAt(total, "9999.00", "t1.total.right", 0.65) {
			t.Errorf("total candidate = %+v, want Distance 0.65 from t1.total.right", total)
		}
	})
}

// 01-T7..T11
func TestResolve_TheRowReachIsTier1RightOnly(t *testing.T) {
	t.Run("01-T7 a learned right rule gets no reach", func(t *testing.T) {
		page := rvPage(
			rvTok("Total", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		learned := extraction.RuleSet{Learned: []extraction.AnchorRule{
			rvLearned(t, "learned-total", "total", rvLabelTotal, extraction.RelRight, 0.35, extraction.ShapeAmount),
		}}
		if got := extraction.Resolve(page, learned); len(got) != 0 {
			t.Errorf("got = %+v, want none: a learned rule always passes rowReach false", got)
		}
	})

	t.Run("01-T8 a below rule ignores RowReach", func(t *testing.T) {
		page := rvPage(
			rvTok("Total", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		below := rrRule(t, "t1.total.below")
		below.RowReach = true
		got := extraction.Resolve(page, extraction.RuleSet{Tier1: []extraction.Tier1Rule{below}})
		if len(got) != 0 {
			t.Errorf("got = %+v, want none: RowReach is inert on a below rule", got)
		}
	})

	t.Run("01-T9 the reach ignores the drop band", func(t *testing.T) {
		rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}

		dropped := rvPage(
			rvTok("Total", 0.10, 0.500, 0.18, 0.511),
			rvTok("9,999.00", 0.80, 0.510, 0.87, 0.523),
		)
		got := extraction.Resolve(dropped, rules)
		if total := rvFor(got, "total"); len(total) != 0 {
			t.Errorf("total = %v, want none: the reach's line band ignores the drop dial", rvValues(total))
		}

		onLine := rvPage(
			rvTok("Total", 0.10, 0.500, 0.18, 0.511),
			rvTok("9,999.00", 0.80, 0.500, 0.87, 0.511),
		)
		got = extraction.Resolve(onLine, rules)
		total := rvFor(got, "total")
		if want := []string{"9999.00"}; !slices.Equal(rvValues(total), want) {
			t.Fatalf("total = %v, want %v", rvValues(total), want)
		}
		if !dbHasCandidateAt(total, "9999.00", "t1.total.right", 0.62) {
			t.Errorf("total candidate = %+v, want Distance 0.62 from t1.total.right", total)
		}
	})

	t.Run("01-T10 a value inside the dial is unaffected", func(t *testing.T) {
		page := rvPage(
			rvTok("Total", 0.10, 0.700, 0.15, 0.710),
			rvTok("1,500.00", 0.35, 0.700, 0.41, 0.710),
		)
		withReach := extraction.Resolve(page, extraction.RuleSet{Tier1: rrWithRowReach(t)})
		shipped := extraction.Resolve(page, extraction.RuleSet{Tier1: extraction.Tier1Rules})

		if !reflect.DeepEqual(withReach, shipped) {
			t.Errorf("Resolve with the reach enabled = %+v, want the same as the shipped set %+v", withReach, shipped)
		}
		total := rvFor(shipped, "total")
		if len(total) != 1 {
			t.Fatalf("total = %+v, want exactly one candidate: the reach must add no duplicate", total)
		}
		if !dbHasCandidateAt(total, "1500.00", "t1.total.right", 0.20) {
			t.Errorf("total candidate = %+v, want Distance 0.20 from t1.total.right", total)
		}
	})

	t.Run("01-T11 a subnormal label height yields zero line overlap", func(t *testing.T) {
		// Control: 01-T1 exercises the same mechanism at a normal box height.
		page := rvPage(
			rvTok("Total", 0.10, 0, 0.15, math.SmallestNonzeroFloat64),
			rvTok("9,999.00", 0.80, math.SmallestNonzeroFloat64, 0.86, 0.5),
		)
		got := extraction.Resolve(page, extraction.RuleSet{Tier1: rrWithRowReach(t)})
		if len(got) != 0 {
			t.Errorf("got = %+v, want none: a subnormal label height fails the strict overlap conjunct", got)
		}
	})
}

// 01-T12
func TestResolve_TheRowReachPassesOverABareNaira(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}

	t.Run("a bare naira before the amount does not end the reach", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("₦", 0.78, 0.700, 0.79, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		sub := rvFor(got, "subtotal")
		if want := []string{"9999.00"}; !slices.Equal(rvValues(sub), want) {
			t.Fatalf("subtotal = %v, want %v", rvValues(sub), want)
		}
		if !dbHasCandidateAt(sub, "9999.00", "t1.subtotal.right", 0.65) {
			t.Errorf("subtotal candidate = %+v, want Distance 0.65 from t1.subtotal.right", sub)
		}
		// Every naira token also sweeps as a currency reading, unrelated to the reach.
		if cur := rvFor(got, "currency"); !slices.Contains(rvValues(cur), "NGN") {
			t.Errorf("currency = %v, want NGN from the bare naira token", rvValues(cur))
		}
	})

	t.Run("a bare naira with no amount past it reaches nothing", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("₦", 0.78, 0.700, 0.79, 0.710),
		)
		got := extraction.Resolve(page, rules)
		if sub := rvFor(got, "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %v, want none: no token remains once the naira is passed over", rvValues(sub))
		}
	})

	t.Run("a bare naira then a word ends the reach", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("₦", 0.55, 0.700, 0.56, 0.710),
			rvTok("Memo", 0.60, 0.700, 0.64, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		if sub := rvFor(got, "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %v, want none: Memo is the first token once the naira is passed over, and the shape refuses it", rvValues(sub))
		}
	})

	t.Run("another letterless token still ends the reach", func(t *testing.T) {
		page := rvPage(
			rvTok("VAT", 0.10, 0.700, 0.14, 0.710),
			rvTok("—", 0.55, 0.700, 0.56, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, rules)
		if vat := rvFor(got, "vat"); len(vat) != 0 {
			t.Errorf("vat = %v, want none: an em dash is not passed over like a bare naira", rvValues(vat))
		}
	})
}
