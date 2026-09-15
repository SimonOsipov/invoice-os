// row_reach_test.go: the Tier-1 row reach (01-T1..T12) -- a bare amount label reads the first
// token on its own line past the right dial. External package: Resolve behaviour specs.
package extraction_test

import (
	"math"
	"reflect"
	"slices"
	"strings"
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

	t.Run("01-T1 control: without the row reach there is no candidate", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, extraction.RuleSet{Tier1: rrWithoutRowReach()})
		if sub := rvFor(got, "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %v, want none: the dial alone does not reach 0.65", rvValues(sub))
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
		dropped := rvPage(
			rvTok("Total", 0.10, 0.500, 0.18, 0.511),
			rvTok("9,999.00", 0.80, 0.510, 0.87, 0.523),
		)
		got := extraction.Resolve(dropped, extraction.RuleSet{Tier1: rrWithRowReach(t)})
		if total := rvFor(got, "total"); len(total) != 0 {
			t.Errorf("total = %v, want none: the reach's line band ignores the drop dial", rvValues(total))
		}
	})

	t.Run("01-T9 control: the same value on the line reaches", func(t *testing.T) {
		onLine := rvPage(
			rvTok("Total", 0.10, 0.500, 0.18, 0.511),
			rvTok("9,999.00", 0.80, 0.500, 0.87, 0.511),
		)
		got := extraction.Resolve(onLine, extraction.RuleSet{Tier1: rrWithRowReach(t)})
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
		shipped := extraction.Resolve(page, extraction.RuleSet{Tier1: rrWithoutRowReach()})

		if !reflect.DeepEqual(withReach, shipped) {
			t.Errorf("Resolve with the reach enabled = %+v, want the same as the set without the reach %+v", withReach, shipped)
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

// rrOn is rvTok on page n.
func rrOn(n int, text string, x0, y0, x1, y1 float64) extraction.Token {
	tok := rvTok(text, x0, y0, x1, y1)
	tok.Region.Page = n
	return tok
}

// rrSubtotalReads fails unless subtotal is exactly [9999.00] from t1.subtotal.right at 0.65.
func rrSubtotalReads(t *testing.T, got []extraction.Candidate) []extraction.Candidate {
	t.Helper()
	sub := rvFor(got, "subtotal")
	if want := []string{"9999.00"}; !slices.Equal(rvValues(sub), want) {
		t.Fatalf("subtotal = %v, want %v", rvValues(sub), want)
	}
	if !dbHasCandidateAt(sub, "9999.00", "t1.subtotal.right", 0.65) {
		t.Errorf("subtotal candidate = %+v, want Distance 0.65 from t1.subtotal.right", sub)
	}
	return sub
}

func TestResolve_TheRowReachStartsPastTheDial(t *testing.T) {
	withReach := extraction.RuleSet{Tier1: rrWithRowReach(t)}
	noReach := extraction.RuleSet{Tier1: rrWithoutRowReach()}

	t.Run("a value exactly at the dial is the dial's alone", func(t *testing.T) {
		// 0.60 - 0.25 is exactly 0.35 in float64; the shipped assertion below holds that.
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.25, 0.710),
			rvTok("9,999.00", 0.60, 0.700, 0.66, 0.710),
		)
		want := extraction.Resolve(page, noReach)
		if sub := rvFor(want, "subtotal"); len(sub) != 1 || sub[0].Distance != 0.35 {
			t.Fatalf("shipped subtotal = %+v, want one candidate at exactly 0.35", sub)
		}
		if got := extraction.Resolve(page, withReach); !reflect.DeepEqual(got, want) {
			t.Errorf("with the reach = %+v, want the result without the reach %+v", got, want)
		}
	})

	t.Run("a value one ulp past the dial is the reach's", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.25, 0.710),
			rvTok("9,999.00", math.Nextafter(0.60, 1), 0.700, 0.66, 0.710),
		)
		if sub := rvFor(extraction.Resolve(page, noReach), "subtotal"); len(sub) != 0 {
			t.Fatalf("shipped subtotal = %+v, want none: the value must sit past the dial", sub)
		}
		sub := rvFor(extraction.Resolve(page, withReach), "subtotal")
		if want := []string{"9999.00"}; !slices.Equal(rvValues(sub), want) {
			t.Fatalf("subtotal = %v, want %v", rvValues(sub), want)
		}
		if sub[0].RuleID != "t1.subtotal.right" || !(sub[0].Distance > 0.35) {
			t.Errorf("subtotal candidate = %+v, want t1.subtotal.right past 0.35", sub[0])
		}
	})

	t.Run("a first token inside the dial ends the reach", func(t *testing.T) {
		page := rvPage(
			rvTok("Total", 0.10, 0.700, 0.15, 0.710),
			rvTok("1,500.00", 0.35, 0.700, 0.41, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		got := extraction.Resolve(page, withReach)
		if total := rvFor(got, "total"); !slices.Equal(rvValues(total), []string{"1500.00"}) {
			t.Fatalf("total = %v, want [1500.00]: 9999.00 sits behind the first token", rvValues(total))
		}
		if want := extraction.Resolve(page, noReach); !reflect.DeepEqual(got, want) {
			t.Errorf("with the reach = %+v, want the result without the reach %+v", got, want)
		}
	})
}

func TestResolve_TheRowReachBreaksAnX0TieByReaderOrder(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}

	t.Run("the amount earlier in reader order is read", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
			rvTok("Memo", 0.80, 0.700, 0.86, 0.710),
		)
		rrSubtotalReads(t, extraction.Resolve(page, rules))
	})

	t.Run("the word earlier in reader order ends the reach", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("Memo", 0.80, 0.700, 0.86, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		if sub := rvFor(extraction.Resolve(page, rules), "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %v, want none: Memo wins the tie", rvValues(sub))
		}
	})
}

func TestResolve_TheRowReachIsScopedToItsOwnPage(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}
	pages := func(page1 ...extraction.Token) []extraction.TokenPage {
		return []extraction.TokenPage{
			{Number: 1, WidthPt: 612, HeightPt: 792, Tokens: page1},
			{Number: 2, WidthPt: 612, HeightPt: 792, Tokens: []extraction.Token{
				rrOn(2, "Subtotal", 0.10, 0.700, 0.15, 0.710),
				rrOn(2, "9,999.00", 0.80, 0.700, 0.86, 0.710),
			}},
		}
	}

	t.Run("page 2 reads its own line, not page 1's", func(t *testing.T) {
		sub := rrSubtotalReads(t, extraction.Resolve(pages(rrOn(1, "1,000.00", 0.55, 0.700, 0.61, 0.710)), rules))
		if want := rrOn(2, "", 0.80, 0.700, 0.86, 0.710).Region; sub[0].Region == nil || *sub[0].Region != want {
			t.Errorf("Region = %v, want the value's own box %v", sub[0].Region, want)
		}
	})

	t.Run("a label stacked at the same spot on page 1 owns nothing on page 2", func(t *testing.T) {
		rrSubtotalReads(t, extraction.Resolve(pages(rrOn(1, "Date", 0.80, 0.685, 0.86, 0.695)), rules))
	})
}

func TestResolve_TheRowReachOwnerSitsDirectlyAboveTheValue(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}

	t.Run("a label beyond the below dial owns nothing", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("Date", 0.80, 0.590, 0.86, 0.600),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		rrSubtotalReads(t, extraction.Resolve(page, rules))
	})

	t.Run("a label under the value owns nothing", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
			rvTok("Date", 0.80, 0.715, 0.86, 0.725),
		)
		rrSubtotalReads(t, extraction.Resolve(page, rules))
	})

	t.Run("a label with an unusable box owns nothing", func(t *testing.T) {
		nan := math.NaN()
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
			rvTok("Date", nan, nan, nan, nan),
		)
		rrSubtotalReads(t, extraction.Resolve(page, rules))
	})
}

func TestResolve_TheRowReachNeedsUsableBoxes(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}
	nan := math.NaN()

	t.Run("an unusable anchor box reaches nothing", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", nan, nan, nan, nan),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		if sub := rvFor(extraction.Resolve(page, rules), "subtotal"); len(sub) != 0 {
			t.Errorf("subtotal = %+v, want none: a NaN anchor fails every clause's comparison", sub)
		}
	})

	t.Run("control: the same anchor with a usable box reaches", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		rrSubtotalReads(t, extraction.Resolve(page, rules))
	})

	t.Run("an unusable token earlier in reader order is passed over", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("Memo", nan, nan, nan, nan),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		rrSubtotalReads(t, extraction.Resolve(page, rules))
	})
}

func TestResolve_TheRowReachPassesOverOnlyABareNaira(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}
	line := func(between ...extraction.Token) []extraction.TokenPage {
		toks := append([]extraction.Token{rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710)}, between...)
		return rvPage(append(toks, rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710))...)
	}

	t.Run("a bare naira inside the dial does not end the reach", func(t *testing.T) {
		rrSubtotalReads(t, extraction.Resolve(line(rvTok("₦", 0.30, 0.700, 0.31, 0.710)), rules))
	})

	t.Run("two bare nairas are both passed over", func(t *testing.T) {
		rrSubtotalReads(t, extraction.Resolve(line(
			rvTok("₦", 0.70, 0.700, 0.71, 0.710),
			rvTok("₦", 0.75, 0.700, 0.76, 0.710),
		), rules))
	})

	t.Run("a padded bare naira is passed over", func(t *testing.T) {
		rrSubtotalReads(t, extraction.Resolve(line(rvTok(" ₦ ", 0.70, 0.700, 0.71, 0.710)), rules))
	})

	t.Run("a naira glued to the amount is the amount", func(t *testing.T) {
		page := rvPage(
			rvTok("Subtotal", 0.10, 0.700, 0.15, 0.710),
			rvTok("₦9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		rrSubtotalReads(t, extraction.Resolve(page, rules))
	})
}

// Only a letter outside the match refuses the reach: punctuation and a symbol do not.
func TestResolve_TheRowReachBareLabelCountsOnlyLetters(t *testing.T) {
	rules := extraction.RuleSet{Tier1: rrWithRowReach(t)}
	total := func(label string) []extraction.Candidate {
		page := rvPage(
			rvTok(label, 0.10, 0.700, 0.15, 0.710),
			rvTok("9,999.00", 0.80, 0.700, 0.86, 0.710),
		)
		return rvFor(extraction.Resolve(page, rules), "total")
	}

	for _, label := range []string{"Total:", "Total ₦"} {
		t.Run(label+" reaches", func(t *testing.T) {
			got := total(label)
			if !slices.Equal(rvValues(got), []string{"9999.00"}) {
				t.Fatalf("total = %v, want [9999.00]", rvValues(got))
			}
			if !dbHasCandidateAt(got, "9999.00", "t1.total.right", 0.65) {
				t.Errorf("total candidate = %+v, want Distance 0.65 from t1.total.right", got)
			}
		})
	}

	t.Run("Total (NGN) gets no reach, the pinned ceiling", func(t *testing.T) {
		if got := total("Total (NGN)"); len(got) != 0 {
			t.Errorf("total = %v, want none: NGN is letters outside the match", rvValues(got))
		}
	})
}

// 02-T1..T3, T8: the register's far-right amounts, once RowReach is attached to the three
// amount .right rules.

// rrWithoutRowReach clones the shipped set with RowReach cleared on every rule.
func rrWithoutRowReach() []extraction.Tier1Rule {
	out := slices.Clone(extraction.Tier1Rules)
	for i := range out {
		out[i].RowReach = false
	}
	return out
}

// rrRegisterAmounts is the register's three far-right amounts, as pdfium prints and measures them.
var rrRegisterAmounts = []struct {
	field, value, ruleID, printed string
	gap                           float64
}{
	{"subtotal", "14800000.00", "t1.subtotal.right", "14,800,000.00", 0.6147},
	{"vat", "1110000.00", "t1.vat.right", "1,110,000.00", 0.6133},
	{"total", "14430000.00", "t1.total.right", "₦14,430,000.00", 0.4604},
}

func rrHasNear(cs []extraction.Candidate, field, value, ruleID string, gap float64) bool {
	return slices.ContainsFunc(cs, func(c extraction.Candidate) bool {
		return c.Field == field && c.Value == value && c.RuleID == ruleID && math.Abs(c.Distance-gap) <= 5e-5
	})
}

// rrDecides fails unless every rrRegisterAmounts field decided ReasonNone at its pinned value,
// with no alternative.
func rrDecides(t *testing.T, what string, out []extraction.FieldResult) {
	t.Helper()
	for _, a := range rrRegisterAmounts {
		r, ok := rcFind(out, a.field)
		if !ok || r.Reason != extraction.ReasonNone || !advSameValue(r.Value, rcStr(a.value)) || len(r.Alternatives) != 0 {
			t.Errorf("%s: %s = %s / %q with %d alternative(s) (ok=%v), want %s / ReasonNone with none", what, a.field, advStr(r.Value), r.Reason, len(r.Alternatives), ok, a.value)
		}
	}
}

func TestAdvisory_TheRegisterReadsItsFarRightAmounts(t *testing.T) {
	for _, fx := range []string{fxAdvisoryRegister, fxAdvisoryRegisterUnspaced} {
		rrDecides(t, fx, advReconcile(t, fx))
		cands := advResolve(t, fx)
		for _, a := range rrRegisterAmounts {
			if !rrHasNear(cands, a.field, a.value, a.ruleID, a.gap) {
				t.Errorf("%s: %s carries no %s from %s at %v: %+v", fx, a.field, a.value, a.ruleID, a.gap, rvFor(cands, a.field))
			}
		}
	}

	// 02-T2: the control that the reach, not something else, moved the three fields.
	t.Run("control: without the row reach all three are missing", func(t *testing.T) {
		cands := extraction.Resolve(rvCorpusPages(t, fxAdvisoryRegister), extraction.RuleSet{Tier1: rrWithoutRowReach()})
		out := extraction.Reconcile(extraction.Input{Candidates: cands})
		for _, a := range rrRegisterAmounts {
			if r, ok := rcFind(out, a.field); !ok || r.Reason != extraction.ReasonMissing {
				t.Errorf("R0 %s without the row reach = %s / %q (ok=%v), want Reason %q", a.field, advStr(r.Value), r.Reason, ok, extraction.ReasonMissing)
			}
		}
	})
}

func TestAdvisory_TheLineItemLabelledVATIsNotTheVAT(t *testing.T) {
	for _, fx := range []string{fxAdvisoryRegister, fxAdvisoryRegisterUnspaced} {
		if slices.Contains(rvValues(rvFor(advResolve(t, fx), "vat")), "2850000.00") {
			t.Errorf("%s: a vat candidate carries the line item's 2850000.00", fx)
		}
	}

	// Control: the line item IS in reach once bare, so the register's silence above is the
	// lexicon match refusing it, not the reach failing to fire at all.
	pages := advRewriteToken(t, rvCorpusPages(t, fxAdvisoryRegister), "VAT compliance health check", "VAT")
	if vat := rvFor(extraction.Resolve(pages, rvGeneric()), "vat"); !rrHasNear(vat, "vat", "2850000.00", "t1.vat.right", 0.4984) {
		t.Errorf("vat lacks 2850000.00 from t1.vat.right at 0.4984 after rewriting the label to VAT: %+v", vat)
	}
}

// rrSplitNaira rebuilds the register's three amount tokens, splitting the glued naira symbol off
// the target amount (joined=false) or joining a bare naira onto it (joined=true). Every inserted
// token copies the target's own Page, Y0 and Y1; X-coordinates come from the target's measured box.
func rrSplitNaira(t *testing.T, pages []extraction.TokenPage, joined bool) []extraction.TokenPage {
	t.Helper()
	out := make([]extraction.TokenPage, len(pages))
	hits := 0
	for i, p := range pages {
		toks := make([]extraction.Token, 0, len(p.Tokens)+len(rrRegisterAmounts))
		for _, tok := range p.Tokens {
			target := false
			for _, a := range rrRegisterAmounts {
				if tok.Text == a.printed {
					target = true
				}
			}
			if !target {
				toks = append(toks, tok)
				continue
			}
			hits++
			digits := strings.TrimPrefix(tok.Text, "₦")
			switch {
			case joined:
				tok.Text = "₦ " + digits
				toks = append(toks, tok)
			case digits != tok.Text:
				naira, amount := tok, tok
				naira.Text, naira.Region.X1 = "₦", tok.Region.X0+0.007
				amount.Text, amount.Region.X0 = digits, tok.Region.X0+0.010
				toks = append(toks, naira, amount)
			default:
				naira := tok
				naira.Text, naira.Region.X0, naira.Region.X1 = "₦", tok.Region.X0-0.010, tok.Region.X0-0.003
				toks = append(toks, naira, tok)
			}
		}
		out[i] = extraction.TokenPage{Number: p.Number, WidthPt: p.WidthPt, HeightPt: p.HeightPt, Tokens: toks}
	}
	if hits != len(rrRegisterAmounts) {
		t.Fatalf("rebuilt %d amount token(s), want %d", hits, len(rrRegisterAmounts))
	}
	return out
}

func TestAdvisory_ASplitNairaStillReadsTheRegistersAmounts(t *testing.T) {
	t.Run("R0 with a split naira", func(t *testing.T) {
		pages := rrSplitNaira(t, rvCorpusPages(t, fxAdvisoryRegister), false)
		rrDecides(t, "R0 with a split naira", extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(pages, rvGeneric())}))
	})
	t.Run("control: R0 with a joined naira", func(t *testing.T) {
		pages := rrSplitNaira(t, rvCorpusPages(t, fxAdvisoryRegister), true)
		rrDecides(t, "R0 with a joined naira", extraction.Reconcile(extraction.Input{Candidates: extraction.Resolve(pages, rvGeneric())}))
	})
}
