package approval

// evalCondition's oracle: evalCondition in frontend/app/src/lib/workflows.ts (its amount
// arm), boundary values from workflows.test.ts's evalCondition block. Pure test —
// never calls dbTestPools, so it cannot skip.

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

// dec builds a *decimal.Decimal from its text form (exponent -2 for a "x.xx" literal).
func dec(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}

func decOrNil(d *decimal.Decimal) string {
	if d == nil {
		return "<nil>"
	}
	return d.String()
}

func strOrNil(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// TestEvalCondition_AmountMatchesSPA pins evalCondition against the mock's amount arm
// (AC-2, AC-3, AC-5). Each row carries the expected result for all four operators, so
// "all four operators on every case" is structural rather than a spot-check.
func TestEvalCondition_AmountMatchesSPA(t *testing.T) {
	cases := []struct {
		name                             string
		cond                             *string
		total                            *decimal.Decimal
		wantGT, wantGTE, wantLT, wantLTE bool
	}{
		{
			"exact boundary, ₦500m seed threshold (workflows.test.ts: boundary included)",
			ptr("500000000.00"), dec("500000000.00"), false, true, false, true,
		},
		{
			"just below by one kobo",
			ptr("500000000.00"), dec("499999999.99"), false, false, true, true,
		},
		{
			"just above by one kobo",
			ptr("500000000.00"), dec("500000000.01"), true, true, false, false,
		},
		{
			"SPA's own above/below fixture (workflows.test.ts: strictly above and below)",
			ptr("250000000.00"), dec("250000001.00"), true, true, false, false,
		},
		{
			"exact boundary, ₦1bn seed threshold (f1n4/h1n4/h2n3 in the workflows.ts seed)",
			ptr("1000000000.00"), dec("1000000000.00"), false, true, false, true,
		},
		{
			"NULL total reads as 0 (AC-3)",
			ptr("500000000.00"), nil, false, false, true, true,
		},
		{
			"zero total is a real value, not a missing one (AC-5)",
			ptr("500000000.00"), dec("0.00"), false, false, true, true,
		},
		{
			"zero total against a zero threshold: 0 vs 0 is equality, not absence (AC-5)",
			ptr("0.00"), dec("0.00"), false, true, false, true,
		},
		// Every cell here flips if `total` were wrongly coerced to 0 instead of kept at -1.00.
		{
			"negative total, discriminating (AC-5)",
			ptr("-0.50"), dec("-1.00"), false, false, true, true,
		},
		{
			"negative total against a zero threshold",
			ptr("0.00"), dec("-1.00"), false, false, true, true,
		},
		{
			"NULL cond_amount reads as 0 (AC-3)",
			nil, dec("500000000.00"), true, true, false, false,
		},
		{
			"unparseable cond_amount reads as 0 (AC-3), mirroring Number('NaN') || 0",
			ptr("NaN"), dec("500000000.00"), true, true, false, false,
		},
		{
			"both sides absent: 0 vs 0",
			nil, nil, false, true, false, true,
		},
		// decimal.NewFromInt(500000000) has exponent 0; the text form below has exponent
		// -2. An == based implementation sees them as unequal at the boundary — this row
		// only passes under Cmp-style comparisons.
		{
			"scale mismatch at the boundary",
			ptr("500000000.00"), func() *decimal.Decimal { d := decimal.NewFromInt(500000000); return &d }(),
			false, true, false, true,
		},
		{
			"top of the numeric(14,2) domain, one kobo below",
			ptr("999999999999.99"), dec("999999999999.98"), false, false, true, true,
		},
		{
			"top of the domain, exact equality",
			ptr("999999999999.99"), dec("999999999999.99"), false, true, false, true,
		},
		// validateCondAmount rejects this at write time (exceeds numeric(14,2)), but a
		// row written before that gate, or by direct SQL, could still carry it. A format
		// check that rejects scientific notation before parsing would fold this to 0,
		// making every cell wrong (a=999999999999.99 would beat a folded-to-0 v).
		{
			"scientific notation cond_amount, beyond the numeric(14,2) domain",
			ptr("1e12"), dec("999999999999.99"), false, false, true, true,
		},
		// scale 3 is rejected by validateCondAmount at write time, but a pre-gate or
		// SQL-written row could carry it. An implementation that rounds/truncates to 2
		// decimals before comparing would see v==a and flip every cell.
		{
			"scale-3 cond_amount, rejected at write but not re-validated on read",
			ptr("0.001"), dec("0.00"), false, false, true, true,
		},
		// decimal.NewFromString(" 500") errors, so this folds to 0 per AC-3 — same as any
		// other unparseable string. An implementation that trims whitespace before parsing
		// would instead read this as 500, flipping every cell against a total of 200.
		{
			"leading-whitespace cond_amount is unparseable, not a trimmed 500",
			ptr(" 500"), dec("200.00"), true, true, false, false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ops := []struct {
				op   string
				want bool
			}{
				{">", tc.wantGT},
				{">=", tc.wantGTE},
				{"<", tc.wantLT},
				{"<=", tc.wantLTE},
			}
			for _, oc := range ops {
				got := evalCondition(oc.op, tc.cond, tc.total)
				if got != oc.want {
					t.Errorf("evalCondition(%q, cond=%s, total=%s) = %v, want %v",
						oc.op, strOrNil(tc.cond), decOrNil(tc.total), got, oc.want)
				}
			}
		})
	}
}

// TestEvalCondition_UnknownOperatorIsFalse: workflows.ts's evalCondition falls through to `<=` for
// an unrecognised operator, but that's the amount arm's internal ladder — there is no
// field column server-side, and cond_op's CHECK admits only the four amount operators
// (migrations/20260809210326_approval_policies.sql:96), so a foreign operator can never
// reach this function through a write. The one reachable shape is cond_op IS NULL,
// dereferenced by the caller as "". Deliberately false, not a port of the mock's
// fall-through: an unspecified condition must take the else lane, not silently mean "<=".
func TestEvalCondition_UnknownOperatorIsFalse(t *testing.T) {
	cond, total := ptr("500000000.00"), dec("500000000.01")

	for _, op := range []string{"", "==", "=", ">>", "≥"} {
		if got := evalCondition(op, cond, total); got != false {
			t.Errorf("evalCondition(%q, ...) = %v, want false", op, got)
		}
	}

	// Positive control: the same inputs under ">" must be true, so a stub that always
	// returns false cannot pass this test.
	if got := evalCondition(">", cond, total); got != true {
		t.Errorf("evalCondition(%q, ...) = %v, want true (positive control)", ">", got)
	}

	// Second pair, total == cond, so the mock's fallthrough (a <= v) is true here.
	// The pair above has total > cond, so a <= v is false there too and cannot catch
	// an implementation that silently falls through to LessThanOrEqual instead of false.
	cond2, total2 := ptr("500000000.00"), dec("500000000.00")
	for _, op := range []string{"", "==", "=", ">>", "≥"} {
		if got := evalCondition(op, cond2, total2); got != false {
			t.Errorf("evalCondition(%q, cond=500000000.00, total=500000000.00) = %v, want false", op, got)
		}
	}
}

// TestEvalCondition_HugeExponentTotalDoesNotHang: total is a *decimal.Decimal, not a
// parsed string, so a caller could hand evalCondition a huge exponent. Cmp rescales to
// the smaller exponent before comparing, which could build a huge coefficient; this
// pins that the comparison stays correct and fast (no hang, no panic).
func TestEvalCondition_HugeExponentTotalDoesNotHang(t *testing.T) {
	huge := decimal.NewFromBigInt(big.NewInt(1), 1000000)
	if got := evalCondition(">", ptr("500000000.00"), &huge); got != true {
		t.Errorf("evalCondition(\">\", ...) = %v, want true", got)
	}
	if got := evalCondition("<=", ptr("500000000.00"), &huge); got != false {
		t.Errorf("evalCondition(\"<=\", ...) = %v, want false", got)
	}
}

// materialise's oracle: simulate in frontend/app/src/lib/workflows.ts, boundary values
// from workflows.test.ts's simulate block. Pure test — never calls
// dbTestPools, so it cannot skip.

// --- Step tree fixtures -------------------------------------------------------------
//
// stepApproval/stepNotify/stepAutoapprove/stepCond build the nested Step shape
// (policy.go:19-30), distinct from policy_draft_test.go's approvalStep and
// policy_test.go's condIn, which build stepInput for the wire/store seam, not this one.

func stepApproval(role string, sla int) Step {
	return Step{Kind: "approval", WorkflowRoleKey: ptr(role), SLAHours: ptr(sla)}
}

func stepNotify(target, channel string) Step {
	return Step{Kind: "notify", NotifyTarget: ptr(target), NotifyChannel: ptr(channel)}
}

func stepAutoapprove() Step {
	return Step{Kind: "autoapprove"}
}

func stepCond(op, amount string, then, els []Step) Step {
	return Step{Kind: "condition", CondOp: ptr(op), CondAmount: ptr(amount), Then: then, Else: els}
}

// polF1..polH2 transcribe the SPA seed's five policies (SEED_FIRM_POLICIES and
// SEED_INHOUSE_POLICIES in workflows.ts) into the nested Step shape. Each returns a fresh
// tree per call, mirroring workflows.test.ts's clone-per-call polF1 helper. The
// never-taken lanes are left nil rather than
// []Step{}, since a hand-built literal may hold either (policy.go:19-30's Then/Else are
// only guaranteed non-nil once nestSteps has run) — that is the "at least one fixture
// with nil lanes" case the plan calls for.

func polF1() []Step {
	return []Step{
		stepApproval("fin_mgr", 48),
		stepCond(">", "250000000.00", []Step{stepApproval("fin_dir", 48)}, nil),
		stepCond(">", "1000000000.00", []Step{stepApproval("cfo", 72), stepNotify("Audit Committee", "Email")}, nil),
		stepApproval("compliance", 24),
	}
}

func polF2() []Step {
	return []Step{
		stepApproval("fin_mgr", 48),
		stepCond(">", "500000000.00", []Step{stepApproval("fin_dir", 48)}, nil),
		stepApproval("compliance", 24),
	}
}

func polF3() []Step {
	return []Step{
		stepApproval("fin_dir", 48),
		stepCond(">", "1000000000.00", []Step{stepApproval("cfo", 72)}, nil),
		stepApproval("compliance", 24),
	}
}

// polH1's h1n4 is the only seeded condition with a non-empty else — the only lane in
// the whole seed that can reach an autoapprove (h1n4 in the workflows.ts seed).
func polH1() []Step {
	return []Step{
		stepApproval("line_mgr", 48),
		stepCond(">", "500000000.00", []Step{stepApproval("fin_dir", 48)}, nil),
		stepCond(">", "1000000000.00", []Step{stepApproval("cfo", 72)}, []Step{stepAutoapprove()}),
		stepNotify("Tax Team", "In-app"),
	}
}

func polH2() []Step {
	return []Step{
		stepApproval("line_mgr", 48),
		stepApproval("fin_dir", 48),
		stepCond(">", "1000000000.00", []Step{stepApproval("cfo", 72), stepApproval("ceo", 72)}, nil),
	}
}

// --- expected-output builders --------------------------------------------------------

// wantApproval/wantNotify/wantAutoapprove build the runStep shape a case expects,
// leaving Ord at its zero value: ordered stamps the real 0..n-1 sequence on afterward,
// so a case only spells what materialise cannot get for free from the tree.

func wantApproval(role string, sla int) runStep {
	return runStep{Kind: "approval", WorkflowRoleKey: ptr(role), SLAHours: ptr(sla)}
}

func wantNotify(target, channel string) runStep {
	return runStep{Kind: "notify", NotifyTarget: ptr(target), NotifyChannel: ptr(channel)}
}

func wantAutoapprove() runStep {
	return runStep{Kind: "autoapprove"}
}

func ordered(steps ...runStep) []runStep {
	for i := range steps {
		steps[i].Ord = i
	}
	return steps
}

// TestMaterialise_LinearPassMatchesSimulate pins AC-2 and AC-5: the five seed shapes at
// every condition boundary must match the SPA oracle's emitted (Kind, WorkflowRoleKey,
// SLAHours, NotifyTarget, NotifyChannel) tuple and its auto flag. polF2/polF3/polH2 have
// no SPA spec case (workflows.test.ts has none) and are derived straight from their
// "then-only, empty else" shape.
func TestMaterialise_LinearPassMatchesSimulate(t *testing.T) {
	cases := []struct {
		name     string
		tree     []Step
		total    *decimal.Decimal
		want     []runStep
		wantAuto bool
	}{
		{"polF1 @1,000 (workflows.test.ts: empty else branch)", polF1(), dec("1000.00"),
			ordered(wantApproval("fin_mgr", 48), wantApproval("compliance", 24)), false},
		{"polF1 @250,000,000.00 exact boundary, > is false", polF1(), dec("250000000.00"),
			ordered(wantApproval("fin_mgr", 48), wantApproval("compliance", 24)), false},
		{"polF1 @750,000,000 (workflows.test.ts: default scenario)", polF1(), dec("750000000.00"),
			ordered(wantApproval("fin_mgr", 48), wantApproval("fin_dir", 48), wantApproval("compliance", 24)), false},
		{"polF1 @2,000,000,000 (workflows.test.ts: then branch taken)", polF1(), dec("2000000000.00"),
			ordered(wantApproval("fin_mgr", 48), wantApproval("fin_dir", 48), wantApproval("cfo", 72),
				wantNotify("Audit Committee", "Email"), wantApproval("compliance", 24)), false},
		{"polF2 @500,000,000.00 exact boundary", polF2(), dec("500000000.00"),
			ordered(wantApproval("fin_mgr", 48), wantApproval("compliance", 24)), false},
		{"polF2 @500,000,000.01", polF2(), dec("500000000.01"),
			ordered(wantApproval("fin_mgr", 48), wantApproval("fin_dir", 48), wantApproval("compliance", 24)), false},
		{"polF3 @1,000,000,000.00 exact boundary", polF3(), dec("1000000000.00"),
			ordered(wantApproval("fin_dir", 48), wantApproval("compliance", 24)), false},
		{"polF3 @1,000,000,000.01", polF3(), dec("1000000000.01"),
			ordered(wantApproval("fin_dir", 48), wantApproval("cfo", 72), wantApproval("compliance", 24)), false},
		{"polH1 @100,000,000, else lane's autoapprove", polH1(), dec("100000000.00"),
			ordered(wantApproval("line_mgr", 48), wantAutoapprove(), wantNotify("Tax Team", "In-app")), true},
		{"polH1 @750,000,000 (workflows.test.ts: autoapprove reached)", polH1(), dec("750000000.00"),
			ordered(wantApproval("line_mgr", 48), wantApproval("fin_dir", 48), wantAutoapprove(),
				wantNotify("Tax Team", "In-app")), true},
		{"polH1 @1,500,000,000 (workflows.test.ts: CFO branch instead)", polH1(), dec("1500000000.00"),
			ordered(wantApproval("line_mgr", 48), wantApproval("fin_dir", 48), wantApproval("cfo", 72),
				wantNotify("Tax Team", "In-app")), false},
		{"polH2 @999,999,999.99", polH2(), dec("999999999.99"),
			ordered(wantApproval("line_mgr", 48), wantApproval("fin_dir", 48)), false},
		{"polH2 @2,000,000,000", polH2(), dec("2000000000.00"),
			ordered(wantApproval("line_mgr", 48), wantApproval("fin_dir", 48), wantApproval("cfo", 72),
				wantApproval("ceo", 72)), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps, auto := materialise(tc.tree, tc.total)
			if !reflect.DeepEqual(steps, tc.want) {
				t.Errorf("materialise(...) steps = %+v, want %+v", steps, tc.want)
			}
			if auto != tc.wantAuto {
				t.Errorf("materialise(...) auto = %v, want %v", auto, tc.wantAuto)
			}
		})
	}
}

// TestMaterialise_ConditionEmitsExactlyOneLaneNeverBoth pins AC-2. polH1's h1n4 is the
// only seeded condition with two non-empty lanes, but its else holds an autoapprove —
// indistinguishable from "nothing chosen" by role. Built here with one approval per
// lane, each with a distinct role, so a leak from the untaken lane is provable.
func TestMaterialise_ConditionEmitsExactlyOneLaneNeverBoth(t *testing.T) {
	tree := []Step{
		stepCond(">", "500000000.00",
			[]Step{stepApproval("then-role", 48)},
			[]Step{stepApproval("else-role", 24)},
		),
	}

	below, _ := materialise(tree, dec("100000000.00"))
	wantBelow := ordered(wantApproval("else-role", 24))
	if !reflect.DeepEqual(below, wantBelow) {
		t.Errorf("below threshold: materialise(...) = %+v, want %+v (else lane only)", below, wantBelow)
	}

	above, _ := materialise(tree, dec("600000000.00"))
	wantAbove := ordered(wantApproval("then-role", 48))
	if !reflect.DeepEqual(above, wantAbove) {
		t.Errorf("above threshold: materialise(...) = %+v, want %+v (then lane only)", above, wantAbove)
	}
}

// TestMaterialise_EmptyChosenLaneEmitsNothing pins AC-3. polH1's h1n2 (> ₦500m, empty
// else) at ₦100,000,000 takes its empty else lane: zero steps, no placeholder, no
// skipped row — while h1n1 before it and h1n4/h1n7 after it still emit in order. The
// explicit length check is what rules out a placeholder row the tuple match alone
// would not catch if it happened to carry zero-value fields.
func TestMaterialise_EmptyChosenLaneEmitsNothing(t *testing.T) {
	steps, auto := materialise(polH1(), dec("100000000.00"))
	want := ordered(wantApproval("line_mgr", 48), wantAutoapprove(), wantNotify("Tax Team", "In-app"))
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("materialise(...) = %+v, want %+v (h1n2's empty else contributes nothing)", steps, want)
	}
	if !auto {
		t.Errorf("auto = false, want true")
	}
	if len(steps) != 3 {
		t.Errorf("len(steps) = %d, want 3 — an empty chosen lane must not emit a placeholder", len(steps))
	}
}

// TestMaterialise_EmptyTreeEmitsNothing pins AC-3's other edge and the plan's slice
// contract. Oracle: workflows.test.ts, an empty policy simulates to nothing at all.
func TestMaterialise_EmptyTreeEmitsNothing(t *testing.T) {
	cases := []struct {
		name string
		tree []Step
	}{
		{"nil tree", nil},
		{"empty tree", []Step{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps, auto := materialise(tc.tree, dec("750000000.00"))
			if steps == nil {
				t.Errorf("materialise(...) returned a nil slice, want non-nil so a caller may range and append")
			}
			if len(steps) != 0 {
				t.Errorf("len(steps) = %d, want 0", len(steps))
			}
			if auto {
				t.Errorf("auto = true, want false")
			}
		})
	}
}

// TestMaterialise_OrdIsDenseAndZeroBased pins AC-1. polH1 @750,000,000 fires both
// conditions (h1n2's then, h1n4's else); every lane's own steps carry policy ord 0
// (policy.go:114), so only materialise's own counter can be dense over the emitted
// sequence.
func TestMaterialise_OrdIsDenseAndZeroBased(t *testing.T) {
	steps, _ := materialise(polH1(), dec("750000000.00"))
	if len(steps) != 4 {
		t.Fatalf("len(steps) = %d, want 4", len(steps))
	}
	for i, s := range steps {
		if s.Ord != i {
			t.Errorf("steps[%d].Ord = %d, want %d", i, s.Ord, i)
		}
	}
}

// TestMaterialise_AutoIsStickyAndDoesNotTruncate pins AC-4's non-truncating half.
// Oracle: workflows.test.ts, auto does NOT truncate the walk.
func TestMaterialise_AutoIsStickyAndDoesNotTruncate(t *testing.T) {
	steps, auto := materialise(polH1(), dec("750000000.00"))
	if !auto {
		t.Errorf("auto = false, want true")
	}
	if len(steps) == 0 {
		t.Fatalf("materialise returned zero steps")
	}
	last := steps[len(steps)-1]
	want := runStep{Ord: len(steps) - 1, Kind: "notify", NotifyTarget: ptr("Tax Team"), NotifyChannel: ptr("In-app")}
	if !reflect.DeepEqual(last, want) {
		t.Errorf("last step = %+v, want %+v (walk continues past the autoapprove)", last, want)
	}
}

// TestMaterialise_AutoIsFalseWhenTheAutoapproveIsInTheUntakenLane pins AC-4's other
// half and is the discriminating test named in the plan: polH1 @1,500,000,000 — h1n4
// (> ₦1bn) is true, so the then lane (cfo) is taken and the autoapprove sitting in the
// untaken else lane must not set auto. An implementation that scans the whole TREE for
// an autoapprove instead of the emitted list would flip this — a run that should still
// need a cfo sign-off would close itself. Oracle: workflows.test.ts, above ₦1,000,000,000
// polH1 takes the CFO branch.
func TestMaterialise_AutoIsFalseWhenTheAutoapproveIsInTheUntakenLane(t *testing.T) {
	steps, auto := materialise(polH1(), dec("1500000000.00"))
	if auto {
		t.Errorf("auto = true, want false — the autoapprove is in the untaken else lane")
	}
	for _, s := range steps {
		if s.Kind == "autoapprove" {
			t.Errorf("emitted an autoapprove step: %+v, want none", s)
		}
	}

	// Positive control, same tree: at ₦750,000,000 the else lane IS taken, so a stub
	// that always returns auto=false cannot pass this test.
	_, autoBelow := materialise(polH1(), dec("750000000.00"))
	if !autoBelow {
		t.Errorf("positive control: auto = false at ₦750,000,000, want true")
	}
}

// TestMaterialise_NeverEmitsAConditionStep pins AC-6 across every seed shape at every
// boundary above, each with a positive control on the emitted count so a stub
// returning nothing cannot pass.
func TestMaterialise_NeverEmitsAConditionStep(t *testing.T) {
	cases := []struct {
		name  string
		tree  []Step
		total *decimal.Decimal
	}{
		{"polF1 @750,000,000", polF1(), dec("750000000.00")},
		{"polF1 @2,000,000,000", polF1(), dec("2000000000.00")},
		{"polF2 @500,000,000.01", polF2(), dec("500000000.01")},
		{"polF3 @1,000,000,000.01", polF3(), dec("1000000000.01")},
		{"polH1 @100,000,000", polH1(), dec("100000000.00")},
		{"polH1 @1,500,000,000", polH1(), dec("1500000000.00")},
		{"polH2 @2,000,000,000", polH2(), dec("2000000000.00")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps, _ := materialise(tc.tree, tc.total)
			if len(steps) == 0 {
				t.Fatalf("materialise returned zero steps — positive control requires a non-zero count")
			}
			for _, s := range steps {
				if s.Kind == "condition" {
					t.Errorf("emitted a condition step: %+v", s)
				}
			}
		})
	}
}

// TestMaterialise_NestedConditionIsSkippedNotEmitted pins the defensive ruling: the
// depth-cap CHECK (migrations/20260809210326_approval_policies.sql:110-111) means
// nestSteps can never produce a condition below the root, but a Go literal can. Poison
// steps sit in the nested condition's own lanes — if materialise ever walked into it,
// the exact-slice comparison below would fail by including them.
func TestMaterialise_NestedConditionIsSkippedNotEmitted(t *testing.T) {
	nested := stepCond(">", "0.00",
		[]Step{stepApproval("poison-then", 1)},
		[]Step{stepApproval("poison-else", 1)},
	)
	tree := []Step{
		stepCond(">", "0.00", []Step{
			stepApproval("a", 1),
			nested,
			stepApproval("b", 1),
		}, nil),
	}

	steps, auto := materialise(tree, dec("1.00"))
	want := ordered(wantApproval("a", 1), wantApproval("b", 1))
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("materialise(...) = %+v, want %+v (nested condition skipped, its lanes not walked)", steps, want)
	}
	if auto {
		t.Errorf("auto = true, want false")
	}
}

// TestMaterialise_NullCondOpTakesElseLane pins AC-2's nil-safety edge: cond_op is
// nullable (migrations/20260809210326_approval_policies.sql:96) and Step.CondOp is a
// *string, so a bare *n.CondOp deref would panic inside the promotion transaction. The
// caller must deref through a local defaulting to "", which evalCondition already
// answers false for — the else lane.
func TestMaterialise_NullCondOpTakesElseLane(t *testing.T) {
	build := func(op *string) []Step {
		return []Step{{
			Kind:       "condition",
			CondOp:     op,
			CondAmount: ptr("1.00"),
			Then:       []Step{stepApproval("then-role", 1)},
			Else:       []Step{stepApproval("else-role", 1)},
		}}
	}

	steps, _ := materialise(build(nil), dec("100.00"))
	want := ordered(wantApproval("else-role", 1))
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("nil cond_op: materialise(...) = %+v, want %+v (else lane, no panic)", steps, want)
	}

	// Positive control, identical tree: an explicit operator takes then.
	steps, _ = materialise(build(ptr(">")), dec("100.00"))
	want = ordered(wantApproval("then-role", 1))
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("cond_op '>': materialise(...) = %+v, want %+v (then lane)", steps, want)
	}
}

// TestMaterialise_ConditionOnlyTreeWithBothLanesEmptyEmitsNothing: unlike
// TestMaterialise_EmptyTreeEmitsNothing (no nodes at all), this tree has one condition
// node whose chosen lane is empty either way — the zero-step shape APPR-06-05 (AC-4's
// state rewrite) must also handle for a run whose only step was a spent condition.
func TestMaterialise_ConditionOnlyTreeWithBothLanesEmptyEmitsNothing(t *testing.T) {
	tree := []Step{stepCond(">", "500000000.00", nil, nil)}

	for _, tc := range []struct {
		name  string
		total *decimal.Decimal
	}{
		{"condition true, then lane empty", dec("600000000.00")},
		{"condition false, else lane empty", dec("100000000.00")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps, auto := materialise(tree, tc.total)
			if steps == nil {
				t.Errorf("materialise(...) returned a nil slice, want non-nil")
			}
			if len(steps) != 0 {
				t.Errorf("len(steps) = %d, want 0", len(steps))
			}
			if auto {
				t.Errorf("auto = true, want false")
			}
		})
	}
}

// TestMaterialise_AutoapproveAtRootIsRecognised: the seed shapes only ever place
// autoapprove inside a condition's else lane, but nothing in materialise requires
// that — a root-lane autoapprove must set auto too.
func TestMaterialise_AutoapproveAtRootIsRecognised(t *testing.T) {
	tree := []Step{stepApproval("fin_mgr", 48), stepAutoapprove()}
	steps, auto := materialise(tree, dec("1.00"))
	want := ordered(wantApproval("fin_mgr", 48), wantAutoapprove())
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("materialise(...) = %+v, want %+v", steps, want)
	}
	if !auto {
		t.Errorf("auto = false, want true")
	}
}

// TestMaterialise_TwoAutoapprovesInOnePolicyBothEmitAndAutoStaysTrue: no seed policy
// has two autoapproves; nothing in materialise assumes at most one, so both must
// emit as ordinary steps and auto must not toggle back off after the first.
func TestMaterialise_TwoAutoapprovesInOnePolicyBothEmitAndAutoStaysTrue(t *testing.T) {
	tree := []Step{
		stepCond(">", "0.00", []Step{stepAutoapprove()}, nil),
		stepAutoapprove(),
	}
	steps, auto := materialise(tree, dec("1.00"))
	want := ordered(wantAutoapprove(), wantAutoapprove())
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("materialise(...) = %+v, want %+v (both autoapproves emitted)", steps, want)
	}
	if !auto {
		t.Errorf("auto = false, want true")
	}
}

// TestMaterialise_NonConditionNodeWithLanesIgnoresThem pins the schema's known gap
// (schema_constraints_test.go:376-399, PC-17): approval_policy_steps_depth_cap only
// rejects a CHILD whose own kind is 'condition', so an approval step can carry a
// non-empty Then/Else to arbitrary depth and nestSteps will populate them. The SPA
// oracle's BranchNode has no then/else on a non-condition node at all — take() only
// ever reads n's own fields via take([]Step{n}), so this is structural, not
// incidental: the exact-slice comparison below fails if either poison lane leaks in.
func TestMaterialise_NonConditionNodeWithLanesIgnoresThem(t *testing.T) {
	approvalWithLanes := stepApproval("fin_mgr", 48)
	approvalWithLanes.Then = []Step{stepApproval("poison-then", 1)}
	approvalWithLanes.Else = []Step{stepApproval("poison-else", 1)}

	tree := []Step{approvalWithLanes, stepApproval("compliance", 24)}
	steps, auto := materialise(tree, dec("1.00"))
	want := ordered(wantApproval("fin_mgr", 48), wantApproval("compliance", 24))
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("materialise(...) = %+v, want %+v (approval's own then/else lanes not walked)", steps, want)
	}
	if len(steps) != 2 {
		t.Errorf("len(steps) = %d, want 2 — a poison lane leaked in", len(steps))
	}
	if auto {
		t.Errorf("auto = true, want false")
	}
}

// TestMaterialise_ReturnedPointersAliasTheSourceTree documents rather than guards
// against aliasing: runStep's *string/*int fields are the same pointers as the Step
// tree's, by design (the plan rules out a deep copy). Mutating a returned runStep's
// pointee DOES corrupt the source tree — proven here — but it does not matter in
// practice: readPolicyTrees (policy_store.go:87) allocates a fresh tree per call, so
// nothing outlives the single ArmTx invocation that both builds and consumes it, and
// no code downstream of materialise ever writes through these pointers.
func TestMaterialise_ReturnedPointersAliasTheSourceTree(t *testing.T) {
	tree := []Step{stepApproval("fin_mgr", 48)}
	steps, _ := materialise(tree, dec("1.00"))
	if len(steps) != 1 {
		t.Fatalf("len(steps) = %d, want 1", len(steps))
	}

	if steps[0].WorkflowRoleKey != tree[0].WorkflowRoleKey {
		t.Fatalf("runStep.WorkflowRoleKey is not the same pointer as the source Step's — aliasing assumption is wrong")
	}

	*steps[0].WorkflowRoleKey = "mutated"
	if *tree[0].WorkflowRoleKey != "mutated" {
		t.Errorf("source tree unaffected by a mutation through the returned runStep — aliasing assumption is wrong")
	}
}
