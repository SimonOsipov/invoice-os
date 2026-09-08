// reconcile_total_test.go: the header total inside the doubt's scope. The corpus produces no
// total with two competing generic adjacent readings, so these are that field's only oracle.
package extraction_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/shopspring/decimal"

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

// AC-5. Reconcile now runs the subtotal + vat identity (corroborateTotal), and a lone reading
// that fails it stays decided: the referee returns the cell untouched on every arm but "exactly
// one of several competing readings balances".
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

// --- EXTR-23-02: arithmetic breaks the tie, and never condemns -----------------------
//
// Where two or more readings compete for the header total, exactly one of them may satisfy the
// document's own subtotal + vat. That one is the answer the page corroborates. Every other arm
// leaves the cell exactly as decideField wrote it: this pass adds evidence, never doubt.

// The tie the arithmetic breaks. 8000.00 + 600.00 = 8600.00, which is the FARTHER reading, so
// the comparator's own head (rtNear) is never the corroborated answer.
const (
	rtNear = "1000.00"
	rtFar  = "8600.00"
	rtSub  = "8000.00"
	rtVAT  = "600.00"
)

// rtTie builds that pair. The two readings sit on different pages so a spec can tell which
// reading the decided cell points the reviewer at.
func rtTie() (near, far extraction.Candidate) {
	near, far = rcDoubtPair("total", rtNear, rtFar, extraction.TierGeneric)
	near.Region = &extraction.Region{Page: 1, X0: 0.70, Y0: 0.80, X1: 0.90, Y1: 0.83}
	far.Region = &extraction.Region{Page: 3, X0: 0.70, Y0: 0.10, X1: 0.90, Y1: 0.13}
	return near, far
}

// rtDec parses one fixture amount the way reconcile.go does, so a typo fails here rather than
// reading as a zero inside a tolerance floor.
func rtDec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	v, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("the fixture amount %q does not parse: %v", s, err)
	}
	return v
}

// rtSameField compares one cell by every part a reviewer sees: name, value, region, reason.
func rtSameField(a, b extraction.Field) bool {
	if a.Name != b.Name || a.Reason != b.Reason {
		return false
	}
	if (a.Value == nil) != (b.Value == nil) || (a.Value != nil && *a.Value != *b.Value) {
		return false
	}
	if (a.Region == nil) != (b.Region == nil) || (a.Region != nil && *a.Region != *b.Region) {
		return false
	}
	return true
}

// rtSameResult is the "unchanged" the never-condemns half asserts: the decided cell and every
// alternative, in order, part by part.
func rtSameResult(a, b extraction.FieldResult) bool {
	if !rtSameField(a.Field, b.Field) || len(a.Alternatives) != len(b.Alternatives) {
		return false
	}
	for i := range a.Alternatives {
		if !rtSameField(a.Alternatives[i], b.Alternatives[i]) {
			return false
		}
	}
	return true
}

// rtVal renders a cell's value for a failure message; a bare %v over a *string prints an address.
func rtVal(v *string) string {
	if v == nil {
		return "<nil>"
	}
	return *v
}

func rtShow(r extraction.FieldResult) string {
	return fmt.Sprintf("{value=%q region=%+v reason=%q alternatives=%q}",
		valuesOf([]extraction.Field{r.Field}), r.Region, r.Reason, valuesOf(r.Alternatives))
}

// AC-1. Of two competing readings exactly one satisfies subtotal + vat, and it becomes the
// decided value carrying its OWN region -- the page the reviewer would be sent to.
func TestReconcile_TheBalancingTotalWinsTheTie(t *testing.T) {
	near, far := rtTie()

	// Non-vacuity: the comparator's head is the nearer reading and it is NOT the one the
	// identity balances, so a pass below cannot be the chooser's own answer restated.
	if near.Distance >= far.Distance || near.Value == far.Value || *near.Region == *far.Region {
		t.Fatalf("fixture %+v / %+v: the balancing reading must be the farther one, carrying a different value and its own region", near, far)
	}

	got := rcDecide(t, "total", far, near, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *got.Value != rtFar {
		t.Errorf("total = %q, want %q -- the reading the document's own arithmetic corroborates is the answer", *got.Value, rtFar)
	}
	if got.Region == nil || *got.Region != *far.Region {
		t.Errorf("total region = %+v, want %+v -- a reviewer sent to the losing reading's page is sent to the wrong number", got.Region, far.Region)
	}
}

// AC-2. The corroborated pick is DECIDED: no reason, and no chooser left behind for a reviewer
// who has nothing left to choose.
func TestReconcile_ACorroboratedTotalCarriesNoDoubt(t *testing.T) {
	near, far := rtTie()

	// Non-vacuity: the same pair without the addends is ambiguous with one alternative, so the
	// zeros below belong to the corroboration and not to a fixture that never doubted anything.
	doubted := rcDecide(t, "total", far, near)
	if doubted.Reason != extraction.ReasonAmbiguous || !slices.Equal(valuesOf(doubted.Alternatives), []string{rtFar}) {
		t.Fatalf("the same pair with no addends reads %q with alternatives %q, want %q with [%q]", doubted.Reason, valuesOf(doubted.Alternatives), extraction.ReasonAmbiguous, rtFar)
	}

	got := rcDecide(t, "total", far, near, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if got.Reason != extraction.ReasonNone {
		t.Errorf("a corroborated total reads %q, want %q -- the arithmetic settled the choice, so no reviewer is asked to", got.Reason, extraction.ReasonNone)
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("a corroborated total still offers %q, want none -- a decided cell that kept its chooser sends the reviewer to re-answer what the page already answered", valuesOf(got.Alternatives))
	}
	if got.Alternatives == nil {
		t.Error("a corroborated total carries a nil Alternatives; it marshals to null, and the review screen reads null as a missing key rather than an empty list")
	}
}

// AC-3. No reading balances: the cell is exactly what decideField returned. Asserted by naming
// the clause -- value, reason, alternative list -- and then by whole-cell equality against the
// same candidates with no addends at all.
func TestReconcile_NoBalancingReadingLeavesTheDoubtExactlyAsItWas(t *testing.T) {
	const miss = "3000.00" // neither 1000.00 nor 3000.00 is 8000.00 + 600.00
	near, far := rtTie()
	off := far
	off.Value = miss

	withAddends := rcDecide(t, "total", off, near, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *withAddends.Value != rtNear {
		t.Errorf("total = %q, want %q -- a failed identity moves no value", *withAddends.Value, rtNear)
	}
	if withAddends.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q -- arithmetic that corroborates no reading corroborates nothing", withAddends.Reason, extraction.ReasonAmbiguous)
	}
	if want := []string{miss}; !slices.Equal(valuesOf(withAddends.Alternatives), want) {
		t.Errorf("total alternatives = %q, want %q -- the competing reading is still the reviewer's to settle", valuesOf(withAddends.Alternatives), want)
	}
	if without := rcDecide(t, "total", off, near); !rtSameResult(withAddends, without) {
		t.Errorf("with the addends the total reads %s and without them %s; a failed identity must leave the cell exactly as decideField wrote it", rtShow(withAddends), rtShow(without))
	}

	// The instrument. The same comparison over a reading that DOES balance must report a
	// difference, or the equality above is what an instrument blind to any change also reports.
	balanced := rcDecide(t, "total", far, near, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if rtSameResult(balanced, rcDecide(t, "total", far, near)) {
		t.Errorf("the balancing reading leaves the cell unchanged at %s; rtSameResult cannot see a change, so the equality above is vacuous", rtShow(balanced))
	}
}

// AC-4. Both readings balance: arithmetic that cannot discriminate does not pick. The tie is
// left for the reviewer rather than resolved by reading order.
func TestReconcile_TwoBalancingReadingsDoNotBreakTheTie(t *testing.T) {
	const first, second = "8600.00", "8600.01"
	head := rcAdjacentAt("total", first, extraction.TierGeneric, 0.03)
	comp := rcAdjacentAt("total", second, extraction.TierGeneric, 0.05)

	// Floor: both readings really do satisfy the identity. The tolerance comparison is STRICTLY
	// greater (TestReconcile_ToleranceIsOneMinorUnit pins the constant), so a gap of exactly one
	// kobo balances and this fixture is a genuine two-way tie, not a one-way win.
	want, tol := rtDec(t, rtSub).Add(rtDec(t, rtVAT)), rtDec(t, "0.01")
	for _, v := range []string{first, second} {
		if want.Sub(rtDec(t, v)).Abs().GreaterThan(tol) {
			t.Fatalf("the reading %s is more than %s from %s; only one reading balances and this fixture measures AC-1 instead", v, tol, want)
		}
	}
	if head.Value == comp.Value {
		t.Fatalf("both readings carry %q; decideField dedups them and there is no tie to leave alone", head.Value)
	}

	got := rcDecide(t, "total", comp, head, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *got.Value != first {
		t.Errorf("total = %q, want %q -- a tie the arithmetic cannot break moves no value", *got.Value, first)
	}
	if got.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q -- two readings that both balance are two answers, and the reviewer still picks", got.Reason, extraction.ReasonAmbiguous)
	}
	if wantAlts := []string{second}; !slices.Equal(valuesOf(got.Alternatives), wantAlts) {
		t.Errorf("total alternatives = %q, want %q", valuesOf(got.Alternatives), wantAlts)
	}

	// The instrument. One reading moved off the identity and the referee must fire, or the
	// unchanged cell above is what a referee that never ran also produces.
	off := comp
	off.Value = "3000.00"
	single := rcDecide(t, "total", off, head, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *single.Value != first || single.Reason != extraction.ReasonNone || len(single.Alternatives) != 0 {
		t.Errorf("with only one balancing reading the total reads %s, want %q decided with no alternative", rtShow(single), first)
	}
}

// AC-5. An addend is evidence only when it is DECIDED. Absent, unparseable, ambiguous, or
// condemned by the line-sum pass: each holds the referee off, and each arm proves the addend
// really reached that state before it reads the total.
func TestReconcile_AnUndecidedAddendIsNotEvidence(t *testing.T) {
	near, far := rtTie()
	vatNear, vatFar := rcDoubtPair("vat", rtVAT, "500.00", extraction.TierGeneric)

	// Every arm is built so that reading the addend past its own state WOULD corroborate the far
	// reading. Arms (b), (c) and (e) pair rtSub with rtVAT, whose sum is rtFar, and pin the value
	// the doubted addend heads on. Arms (a) and (d) instead carry a vat that ALONE equals rtFar,
	// because the alternative to "not checked" there is a subtotal folded to zero.
	cases := []struct {
		name       string
		extra      []extraction.Candidate
		lines      []extraction.DocLine
		addend     string
		wantReason extraction.Reason
		wantValue  string // "" when the addend cell carries none
	}{
		{
			name:       "no subtotal candidate at all",
			extra:      []extraction.Candidate{rcCandidate("vat", rtFar)},
			addend:     "subtotal",
			wantReason: extraction.ReasonMissing,
		},
		{
			name: "an equal-standing subtotal is ambiguous",
			extra: []extraction.Candidate{
				rcCandAt("subtotal", rtSub, extraction.TierGeneric, 0.03),
				rcCandAt("subtotal", "9000.00", extraction.TierGeneric, 0.03),
				rcCandidate("vat", rtVAT),
			},
			addend:     "subtotal",
			wantReason: extraction.ReasonAmbiguous,
			wantValue:  rtSub,
		},
		{
			name:       "an ambiguous vat",
			extra:      []extraction.Candidate{vatFar, vatNear, rcCandidate("subtotal", rtSub)},
			addend:     "vat",
			wantReason: extraction.ReasonAmbiguous,
			wantValue:  rtVAT,
		},
		{
			name:       "an unparseable subtotal",
			extra:      []extraction.Candidate{rcCandidate("subtotal", "eight thousand"), rcCandidate("vat", rtFar)},
			addend:     "subtotal",
			wantReason: extraction.ReasonNone, // decided, and still not a number
			wantValue:  "eight thousand",
		},
		{
			name:       "a subtotal the line sum condemned",
			extra:      []extraction.Candidate{rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT)},
			lines:      []extraction.DocLine{{Index: 1, Quantity: rcStr("1"), UnitPrice: rcStr("900.00"), LineTotal: rcStr("900.00")}},
			addend:     "subtotal",
			wantReason: extraction.ReasonInconsistent,
			wantValue:  rtSub,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := extraction.Reconcile(extraction.Input{
				Candidates: append([]extraction.Candidate{far, near}, tc.extra...),
				Lines:      tc.lines,
			})

			// Floor: the addend reached the state this arm is named for, or the total below is
			// held off by some other clause entirely.
			addend := rtaFind(t, results, tc.addend)
			if addend.Reason != tc.wantReason {
				t.Fatalf("%s reads %q, want %q -- this arm never reached the state it names", tc.addend, addend.Reason, tc.wantReason)
			}
			if tc.wantValue != "" && (addend.Value == nil || *addend.Value != tc.wantValue) {
				t.Fatalf("%s holds %q, want %q -- the arm is earned by the value, not by the reason", tc.addend, rtVal(addend.Value), tc.wantValue)
			}

			total := rtaFind(t, results, "total")
			if total.Value == nil || *total.Value != rtNear {
				t.Errorf("total = %q, want %q -- an addend nobody trusts moves no value", rtVal(total.Value), rtNear)
			}
			if total.Reason != extraction.ReasonAmbiguous {
				t.Errorf("total reason = %q, want %q -- corroboration needs an addend this pass itself decided", total.Reason, extraction.ReasonAmbiguous)
			}
			if want := []string{rtFar}; !slices.Equal(valuesOf(total.Alternatives), want) {
				t.Errorf("total alternatives = %q, want %q", valuesOf(total.Alternatives), want)
			}
		})
	}

	// The instrument: the same competing pair with both addends decided, and the referee fires.
	// Without it every arm above is what a referee that never ran also produces.
	decided := rcDecide(t, "total", far, near, rcCandidate("subtotal", rtSub), rcCandidate("vat", rtVAT))
	if *decided.Value != rtFar || decided.Reason != extraction.ReasonNone {
		t.Errorf("with both addends decided the total reads %q / %q, want %q / %q", *decided.Value, decided.Reason, rtFar, extraction.ReasonNone)
	}
}

// AC-6. A document silent on VAT is not a document that printed 0.00. The discriminator is the
// printed zero: same subtotal, same competing pair, and only the printed zero corroborates.
func TestReconcile_AMissingVATIsNotZero(t *testing.T) {
	near, far := rtTie()
	results := extraction.Reconcile(extraction.Input{
		Candidates: []extraction.Candidate{far, near, rcCandidate("subtotal", rtFar)},
	})

	// Floor: the arm is earned by an ABSENT vat, not by an unparseable or doubtful one.
	if got := rtaFind(t, results, "vat"); got.Reason != extraction.ReasonMissing {
		t.Fatalf("vat reads %q, want %q -- this fixture is meant to be silent on VAT", got.Reason, extraction.ReasonMissing)
	}

	total := rtaFind(t, results, "total")
	if total.Value == nil || *total.Value != rtNear {
		t.Errorf("total = %q, want %q -- a subtotal that equals a competing reading is half the identity, not the identity", rtVal(total.Value), rtNear)
	}
	if total.Reason != extraction.ReasonAmbiguous {
		t.Errorf("total reason = %q, want %q -- an absent VAT read as zero would corroborate every subtotal-equals-total document ever scanned", total.Reason, extraction.ReasonAmbiguous)
	}
	if want := []string{rtFar}; !slices.Equal(valuesOf(total.Alternatives), want) {
		t.Errorf("total alternatives = %q, want %q", valuesOf(total.Alternatives), want)
	}

	// The instrument: the same subtotal with a PRINTED zero VAT does corroborate.
	withZero := rcDecide(t, "total", far, near, rcCandidate("subtotal", rtFar), rcCandidate("vat", "0.00"))
	if *withZero.Value != rtFar || withZero.Reason != extraction.ReasonNone {
		t.Errorf("with a printed vat of 0.00 the total reads %q / %q, want %q / %q; the absence above is otherwise what a referee that never ran also produces", *withZero.Value, withZero.Reason, rtFar, extraction.ReasonNone)
	}
}
