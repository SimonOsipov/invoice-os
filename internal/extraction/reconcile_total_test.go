// reconcile_total_test.go: the header total inside the doubt's scope. The corpus produces no
// total with two competing generic adjacent readings, so these are that field's only oracle.
package extraction_test

import (
	"slices"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// AC-2. The far reading is not equal standing (D-14) and falls out of the group until total is
// in scope, so this is the spec the widening exists to turn green.
func TestReconcile_ADistantCompetingTotalNowReadsDoubtful(t *testing.T) {
	const near, far = "1000.00", "8600.00"
	n, f := rcDoubtPair("total", near, far, extraction.TierGeneric)

	// Non-vacuity: every conjunct of the doubt is explicit here, so the reason below is earned
	// by the scope list rather than by a fixture that quietly misses one.
	if !n.Adjacent || n.Tier != extraction.TierGeneric || n.Distance == f.Distance || n.Value == f.Value {
		t.Fatalf("fixture %+v / %+v: the head must be adjacent, generic, nearer, and carry a different value", n, f)
	}

	// far first: the head is the comparator's choice, never the caller's. No subtotal and no vat
	// candidate, so nothing but the scope decides this.
	got := rcDecide(t, "total", f, n)
	if *got.Value != near {
		t.Errorf("total = %q, want %q -- the doubt moves a reason, never a value", *got.Value, near)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q -- an adjacent generic total head with a second distinct reading is not corroborated", got.Reason, extraction.ReasonAmbiguous)
	}
	if want := []string{far}; !slices.Equal(valuesOf(got.Alternatives), want) {
		t.Errorf("total alternatives = %q, want %q -- the competing reading is what the reviewer has to settle", valuesOf(got.Alternatives), want)
	}
}

// AC-3. A total read from inside its own label token is corroborated by that label. The two
// arms differ in Adjacent and nothing else.
func TestReconcile_ASameTokenTotalHeadStaysDecided(t *testing.T) {
	const near, far = "1000.00", "8600.00"
	n, f := rcDoubtPair("total", near, far, extraction.TierGeneric)

	sameToken := n
	sameToken.Adjacent, sameToken.Distance = false, 0 // what RelSameToken emits
	got := rcDecide(t, "total", f, sameToken)
	if *got.Value != near {
		t.Errorf("total = %q, want %q", *got.Value, near)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("a same_token total head reads %q, want %q -- the label the value came out of is its corroboration", got.Reason, extraction.ReasonNone)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("a same_token total head carries alternatives %q, want none", valuesOf(got.Alternatives))
	}

	// The discriminator: the same head one field different. Without it the zero above is also
	// what a doubt that never reaches total produces.
	adjacent := sameToken
	adjacent.Adjacent = true
	doubted := rcDecide(t, "total", f, adjacent)
	if doubted.Reason != extraction.ReasonAmbiguous || !slices.Equal(valuesOf(doubted.Alternatives), []string{far}) {
		t.Fatalf("the same head marked adjacent reads %q with alternatives %q, want %q with [%q]; the decided answer above is otherwise a zero the doubt never reaches", doubted.Reason, valuesOf(doubted.Alternatives), extraction.ReasonAmbiguous, far)
	}
}

// AC-4. A learned total head is the tenant's own answer for this layout and is never
// second-guessed. The generic arm is the discriminator: the pairs differ in Tier and nothing else.
func TestReconcile_ALearnedTotalHeadStaysDecided(t *testing.T) {
	const near, far = "1000.00", "8600.00"

	ln, lf := rcDoubtPair("total", near, far, extraction.TierLearned)
	learned := rcDecide(t, "total", lf, ln)
	if *learned.Value != near {
		t.Errorf("total = %q, want %q", *learned.Value, near)
	}
	if learned.Reason != extraction.ReasonNone {
		t.Errorf("a learned total head reads %q, want %q -- the tenant's own rule is the corroboration", learned.Reason, extraction.ReasonNone)
	}
	if len(learned.Alternatives) != 0 {
		t.Errorf("a learned total head carries alternatives %q, want none", valuesOf(learned.Alternatives))
	}

	gn, gf := rcDoubtPair("total", near, far, extraction.TierGeneric)
	generic := rcDecide(t, "total", gf, gn)
	if generic.Reason != extraction.ReasonAmbiguous || !slices.Equal(valuesOf(generic.Alternatives), []string{far}) {
		t.Fatalf("the same pair at TierGeneric reads %q with alternatives %q, want %q with [%q]; the learned zero above is otherwise a zero the widening never reaches", generic.Reason, valuesOf(generic.Alternatives), extraction.ReasonAmbiguous, far)
	}
}

// AC-5. RATCHET, not a driver: Reconcile runs no subtotal+vat identity today, so this asserts an
// absence that is already true and it has no red phase in this subtask. It is EXTR-23-02's
// oracle -- the referee it adds may break a tie, never condemn a lone reading.
func TestReconcile_ASingleTotalCandidateThatFailsTheIdentityStaysDecided(t *testing.T) {
	const total, subtotal, vat = "1000.00", "8000.00", "600.00" // 8000.00 + 600.00 = 8600.00, not 1000.00
	lone := rcAdjacentAt("total", total, extraction.TierGeneric, 0.03)

	// Non-vacuity: the head satisfies every conjunct of the doubt, so only the count of distinct
	// readings and the (absent) identity can decide it.
	if !lone.Adjacent || lone.Tier != extraction.TierGeneric {
		t.Fatalf("the fixture is %+v; it must be adjacent and generic or the zero below is earned by the wrong clause", lone)
	}

	got := rcDecide(t, "total", lone, rcCandidate("subtotal", subtotal), rcCandidate("vat", vat))
	if *got.Value != total {
		t.Errorf("total = %q, want %q", *got.Value, total)
	}
	if got.Reason != extraction.ReasonNone {
		t.Errorf("total reason = %q, want %q specifically -- one distinct reading is one answer whatever its relation to subtotal + vat", got.Reason, extraction.ReasonNone)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("total alternatives = %q, want none", valuesOf(got.Alternatives))
	}
}
