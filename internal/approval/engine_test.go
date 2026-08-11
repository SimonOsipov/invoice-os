package approval

// evalCondition's oracle: frontend/app/src/lib/workflows.ts:461-476 (amount arm at
// :462-471), boundary values transcribed from workflows.test.ts:674-687. Pure test —
// never calls dbTestPools, so it cannot skip.

import (
	"math/big"
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
			"exact boundary, ₦500m seed threshold (workflows.test.ts:675-679)",
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
			"SPA's own above/below fixture (workflows.test.ts:683-686)",
			ptr("250000000.00"), dec("250000001.00"), true, true, false, false,
		},
		{
			"exact boundary, ₦1bn seed threshold (workflows.ts:147,180,193)",
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

// TestEvalCondition_UnknownOperatorIsFalse: workflows.ts:470 falls through to `<=` for
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
