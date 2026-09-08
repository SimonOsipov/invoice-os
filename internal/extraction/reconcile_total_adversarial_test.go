// reconcile_total_adversarial_test.go: the header total's doubt past its acceptance criteria --
// the dedup, more than two competitors, the region an alternative carries, and the two other
// Reconcile passes that write a reason.
package extraction_test

import (
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// rtaFind returns the result named name, failing rather than handing back a zero FieldResult a
// later assertion would read as "decided with no value".
func rtaFind(t *testing.T, results []extraction.FieldResult, name string) extraction.FieldResult {
	t.Helper()
	got, ok := rcFind(results, name)
	if !ok {
		t.Fatalf("Reconcile emitted no %s", name)
	}
	return got
}

// D-15 on the widened field: two readings of one printed total are one answer. The widening is
// what puts the farther reading in the group at all, so only the dedup keeps this decided.
func TestReconcile_TwoAdjacentTotalReadingsOfOneValueStayDecided(t *testing.T) {
	const v = "1000.00"
	near := rcAdjacentAt("total", v, extraction.TierGeneric, 0.03)
	far := rcAdjacentAt("total", v, extraction.TierGeneric, 0.05)

	// Non-vacuity: both arms satisfy every conjunct of the doubt and stand at different
	// distances, so nothing but the dedup can decide this.
	if !near.Adjacent || near.Tier != extraction.TierGeneric || near.Distance == far.Distance || near.Value != far.Value {
		t.Fatalf("fixture %+v / %+v: two adjacent generic readings of ONE value at different distances, or the zero below is earned by the wrong clause", near, far)
	}

	got := rcDecide(t, "total", far, near)
	if *got.Value != v {
		t.Errorf("total = %q, want %q", *got.Value, v)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("total reason = %q, want %q -- two readings of one value are one answer", got.Reason, extraction.ReasonNone)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("total alternatives = %q, want none -- a reviewer asked to choose between 1000.00 and 1000.00 has no choice", valuesOf(got.Alternatives))
	}

	// The discriminator: the same pair, one value different. Without it the zero above is also
	// what a doubt that never reaches total produces.
	competing := far
	competing.Value = "8600.00"
	doubted := rcDecide(t, "total", competing, near)
	if doubted.Reason != extraction.ReasonAmbiguous || !slices.Equal(valuesOf(doubted.Alternatives), []string{competing.Value}) {
		t.Fatalf("the same pair carrying two values reads %q with alternatives %q, want %q with [%q]", doubted.Reason, valuesOf(doubted.Alternatives), extraction.ReasonAmbiguous, competing.Value)
	}
}

// Two competitors and a repeat of one of them: a fixture of two cannot tell a walk that stops
// at the first alternative from one that stops at the first duplicate. The order is the
// comparator's, nearest competitor first, and the review screen renders the chips in it.
func TestReconcile_EveryCompetingTotalReadingIsOfferedOnceNearestFirst(t *testing.T) {
	head := rcAdjacentAt("total", "1000.00", extraction.TierGeneric, 0.03)
	second := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.05)
	repeat := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.07)
	third := rcAdjacentAt("total", "250.00", extraction.TierGeneric, 0.09)

	// Input order is deliberately not output order: the head is the comparator's choice.
	got := rcDecide(t, "total", third, repeat, second, head)
	if *got.Value != head.Value {
		t.Errorf("total = %q, want %q -- the nearest reading still decides the value", *got.Value, head.Value)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q", got.Reason, extraction.ReasonAmbiguous)
	}
	alts := valuesOf(got.Alternatives)
	if len(alts) == 0 {
		t.Fatalf("total reads ambiguous with no alternative; every assertion below reads an empty list")
	}
	if want := []string{second.Value, third.Value}; !slices.Equal(alts, want) {
		t.Errorf("total alternatives = %q, want %q -- both competitors, the repeat collapsed, nearest first", alts, want)
	}
}

// An alternative on another page is what the review screen points the reviewer at, and no spec
// read Alternatives[i].Region before. The doubt also crosses pages, which no corpus layout does.
func TestReconcile_ATotalCompetingOnAnotherPageCarriesThatPagesRegion(t *testing.T) {
	head := rcAdjacentAt("total", "1000.00", extraction.TierGeneric, 0.03)
	head.Region = &extraction.Region{Page: 1, X0: 0.70, Y0: 0.80, X1: 0.90, Y1: 0.83}
	far := rcAdjacentAt("total", "8600.00", extraction.TierGeneric, 0.05)
	far.Region = &extraction.Region{Page: 3, X0: 0.70, Y0: 0.10, X1: 0.90, Y1: 0.13}

	got := rcDecide(t, "total", far, head)
	if got.Reason != extraction.ReasonAmbiguous {
		t.Fatalf("total reason = %q, want %q -- the doubt is keyed on the head's flag, not on the two readings sharing a page", got.Reason, extraction.ReasonAmbiguous)
	}
	if got.Region == nil || *got.Region != *head.Region {
		t.Errorf("total region = %+v, want %+v -- the decided cell points at the reading that decided it", got.Region, head.Region)
	}
	if len(got.Alternatives) != 1 {
		t.Fatalf("total carries %d alternative(s) %q, want 1", len(got.Alternatives), valuesOf(got.Alternatives))
	}
	alt := got.Alternatives[0]
	if alt.Value == nil || *alt.Value != far.Value {
		t.Errorf("the alternative carries %v, want %q", alt.Value, far.Value)
	}
	if alt.Reason != extraction.ReasonNone {
		t.Errorf("the alternative reads %q, want %q -- the reason lives on the decided cell alone", alt.Reason, extraction.ReasonNone)
	}
	if alt.Region == nil || *alt.Region != *far.Region {
		t.Errorf("the alternative points at %+v, want %+v -- a reviewer sent to page 1 for a page 3 amount is sent nowhere", alt.Region, far.Region)
	}
}

// Reconcile writes a reason in three places. This runs all three at once: the line sum condemns
// subtotal, the entity check condemns supplier_name, and neither may reach the doubtful total.
func TestReconcile_ADoubtfulTotalSurvivesTheSubtotalAndSupplierPasses(t *testing.T) {
	const near, far = "1000.00", "8600.00"
	n, f := rcDoubtPair("total", near, far, extraction.TierGeneric)
	results := extraction.Reconcile(extraction.Input{
		Candidates: []extraction.Candidate{
			f, n,
			rcCandidate("subtotal", "1000.00"), // the lines below sum to 900.00
			rcCandidate("supplier_name", "Acme Ltd"),
		},
		Lines:  []extraction.DocLine{{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("900.00"), LineTotal: rcStr("900.00")}},
		Entity: extraction.Entity{Name: "Zenith Ltd"},
	})

	// Floors: both other passes must actually have fired, or the total below is untouched by
	// two passes that never ran.
	if got := rtaFind(t, results, "subtotal"); got.Reason != extraction.ReasonInconsistent {
		t.Fatalf("subtotal reads %q, want %q -- the line-sum pass did not fire and this fixture proves nothing about it", got.Reason, extraction.ReasonInconsistent)
	}
	if got := rtaFind(t, results, "supplier_name"); got.Reason != extraction.ReasonInconsistent {
		t.Fatalf("supplier_name reads %q, want %q -- the entity pass did not fire and this fixture proves nothing about it", got.Reason, extraction.ReasonInconsistent)
	}

	total := rtaFind(t, results, "total")
	if total.Value == nil || *total.Value != near {
		t.Errorf("total = %v, want %q", total.Value, near)
	}
	if total.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q -- a doubtful total is not rewritten by a pass keyed on another field", total.Reason, extraction.ReasonAmbiguous)
	}
	if want := []string{far}; !slices.Equal(valuesOf(total.Alternatives), want) {
		t.Errorf("total alternatives = %q, want %q", valuesOf(total.Alternatives), want)
	}

	// The header total and a row's line_total are different names; the doubt reaches neither
	// the rows nor their arithmetic.
	if got := rcLineValues(results); len(got) == 0 {
		t.Errorf("Reconcile emitted no line-item value row from one clean line")
	}
	if got := rcLineFlags(results); len(got) != 0 {
		t.Errorf("Reconcile flagged %d line row(s) %+v on arithmetic that balances", len(got), got)
	}
}
