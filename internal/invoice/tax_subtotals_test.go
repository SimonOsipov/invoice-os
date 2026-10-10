package invoice

import (
	"encoding/json"
	"reflect"
	"testing"
)

func tsPtr(s string) *string { return &s }

func tsLine(no int, cat, pct, total, tax *string) LineItem {
	return LineItem{LineNo: no, TaxCategory: cat, TaxPercent: pct, LineTotal: total, LineTax: tax}
}

func tsStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func tsEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// tsWant is a subtotal expectation; "" means the pointer must be nil.
type tsWant struct{ cat, pct, taxable, tax string }

func tsAssert(t *testing.T, got []TaxSubtotal, want []tsWant) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d subtotals %+v, want %d", len(got), got, len(want))
	}
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	for i, w := range want {
		g := got[i]
		if g.TaxCategory != w.cat || !tsEq(g.TaxPercent, opt(w.pct)) ||
			!tsEq(g.TaxableAmount, opt(w.taxable)) || !tsEq(g.TaxAmount, opt(w.tax)) {
			t.Errorf("subtotal %d = {%s %s %s %s}, want {%s %q %q %q}", i,
				g.TaxCategory, tsStr(g.TaxPercent), tsStr(g.TaxableAmount), tsStr(g.TaxAmount),
				w.cat, w.pct, w.taxable, w.tax)
		}
	}
}

func TestTaxSubtotals_GroupsByCategoryAndPercentInLineOrder(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("100.00"), tsPtr("7.50")),
		tsLine(2, tsPtr("ZERO_VAT"), tsPtr("0.00"), tsPtr("50.00"), tsPtr("0.00")),
		tsLine(3, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("200.00"), tsPtr("15.00")),
		tsLine(4, tsPtr("STANDARD_VAT"), tsPtr("5.00"), tsPtr("10.00"), tsPtr("0.50")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{
		{"STANDARD_VAT", "7.50", "300.00", "22.50"},
		{"ZERO_VAT", "0.00", "50.00", "0.00"},
		{"STANDARD_VAT", "5.00", "10.00", "0.50"},
	})
}

// Percent text is compared exactly: "7.5" and "7.50" are two groups, and the
// printed percent is the stored text, not a normalised one.
func TestTaxSubtotals_GroupingIsByExactPercentText(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.5"), tsPtr("10.00"), tsPtr("0.75")),
		tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("20.00"), tsPtr("1.50")),
		tsLine(3, tsPtr("STANDARD_VAT"), tsPtr("7.5"), tsPtr("30.00"), tsPtr("2.25")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{
		{"STANDARD_VAT", "7.5", "40.00", "3.00"},
		{"STANDARD_VAT", "7.50", "20.00", "1.50"},
	})
}

func TestTaxSubtotals_GroupingIsByExactCategoryText(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("10.00"), tsPtr("0.75")),
		tsLine(2, tsPtr("standard_vat"), tsPtr("7.50"), tsPtr("20.00"), tsPtr("1.50")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{
		{"STANDARD_VAT", "7.50", "10.00", "0.75"},
		{"standard_vat", "7.50", "20.00", "1.50"},
	})
}

// A NULL percent is its own key: it never merges with "0.00" and prints nil.
func TestTaxSubtotals_NullPercentIsItsOwnGroup(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("EXEMPTED"), nil, tsPtr("10.00"), tsPtr("0.00")),
		tsLine(2, tsPtr("EXEMPTED"), tsPtr("0.00"), tsPtr("20.00"), tsPtr("0.00")),
		tsLine(3, tsPtr("EXEMPTED"), nil, tsPtr("30.00"), tsPtr("0.00")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{
		{"EXEMPTED", "", "40.00", "0.00"},
		{"EXEMPTED", "0.00", "20.00", "0.00"},
	})
}

func TestTaxSubtotals_OrderFollowsLineNoNotSliceOrder(t *testing.T) {
	lines := []LineItem{
		tsLine(3, tsPtr("C_CAT"), tsPtr("1.00"), tsPtr("3.00"), tsPtr("0.03")),
		tsLine(1, tsPtr("A_CAT"), tsPtr("1.00"), tsPtr("1.00"), tsPtr("0.01")),
		tsLine(2, tsPtr("B_CAT"), tsPtr("1.00"), tsPtr("2.00"), tsPtr("0.02")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{
		{"A_CAT", "1.00", "1.00", "0.01"},
		{"B_CAT", "1.00", "2.00", "0.02"},
		{"C_CAT", "1.00", "3.00", "0.03"},
	})
}

// A group is ordered by its lowest line_no even when a later line of the
// group comes first in the slice.
func TestTaxSubtotals_GroupOrderUsesTheGroupsFirstLineNo(t *testing.T) {
	lines := []LineItem{
		tsLine(2, tsPtr("B_CAT"), tsPtr("1.00"), tsPtr("2.00"), tsPtr("0.02")),
		tsLine(5, tsPtr("A_CAT"), tsPtr("1.00"), tsPtr("5.00"), tsPtr("0.05")),
		tsLine(1, tsPtr("A_CAT"), tsPtr("1.00"), tsPtr("1.00"), tsPtr("0.01")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{
		{"A_CAT", "1.00", "6.00", "0.06"},
		{"B_CAT", "1.00", "2.00", "0.02"},
	})
}

func TestTaxSubtotals_SumsAreExact(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("0.10"), tsPtr("0.01")),
		tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("0.20"), tsPtr("0.02")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{{"STANDARD_VAT", "7.50", "0.30", "0.03"}})
}

func TestTaxSubtotals_ATwentyDigitSumIsExact(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("12345678901234567.89"), tsPtr("12345678901234567.89")),
		tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("0.01"), tsPtr("0.01")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{
		{"STANDARD_VAT", "7.50", "12345678901234567.90", "12345678901234567.90"},
	})
}

func TestTaxSubtotals_NegativeAndZeroLinesSum(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("100.00"), tsPtr("7.50")),
		tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("-100.00"), tsPtr("-7.50")),
		tsLine(3, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("0"), tsPtr("0")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{{"STANDARD_VAT", "7.50", "0.00", "0.00"}})
}

// Two decimals always print, and the sum rounds once (half away from zero),
// not per member: 0.004 + 0.004 is 0.01, not 0.00.
func TestTaxSubtotals_PrintsTwoDecimalsAndRoundsTheSumOnce(t *testing.T) {
	cases := []struct {
		name         string
		totals, taxs []string
		wantTotal    string
		wantTax      string
	}{
		{"whole number pads", []string{"5"}, []string{"1.5"}, "5.00", "1.50"},
		{"sum rounds once", []string{"0.004", "0.004"}, []string{"0.004", "0.004"}, "0.01", "0.01"},
		{"half rounds away from zero", []string{"1.005"}, []string{"2.675"}, "1.01", "2.68"},
		{"negative half rounds away from zero", []string{"-1.005"}, []string{"-0.005"}, "-1.01", "-0.01"},
		{"below half rounds down", []string{"0.004"}, []string{"0.0049"}, "0.00", "0.00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var lines []LineItem
			for i := range c.totals {
				lines = append(lines, tsLine(i+1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr(c.totals[i]), tsPtr(c.taxs[i])))
			}
			tsAssert(t, taxSubtotals(lines), []tsWant{{"STANDARD_VAT", "7.50", c.wantTotal, c.wantTax}})
		})
	}
}

func TestTaxSubtotals_UncategorisedLinesJoinNoGroup(t *testing.T) {
	lines := []LineItem{
		tsLine(1, nil, tsPtr("7.50"), tsPtr("999.00"), tsPtr("99.00")),
		tsLine(2, tsPtr("EXEMPTED"), tsPtr("0.00"), tsPtr("10.00"), tsPtr("0.00")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{{"EXEMPTED", "0.00", "10.00", "0.00"}})
}

// An uncategorised line with a NULL or junk amount must not poison a group.
func TestTaxSubtotals_UncategorisedLineAmountsNeverAffectAGroup(t *testing.T) {
	lines := []LineItem{
		tsLine(1, nil, nil, nil, tsPtr("NaN")),
		tsLine(2, tsPtr("EXEMPTED"), tsPtr("0.00"), tsPtr("10.00"), tsPtr("0.00")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{{"EXEMPTED", "0.00", "10.00", "0.00"}})
}

func TestTaxSubtotals_NoCategoryIsNil(t *testing.T) {
	allNull := []LineItem{
		tsLine(1, nil, tsPtr("7.50"), tsPtr("10.00"), tsPtr("0.75")),
		tsLine(2, nil, nil, tsPtr("20.00"), tsPtr("1.50")),
	}
	if got := taxSubtotals(allNull); got != nil {
		t.Errorf("all-NULL categories: got %+v, want nil", got)
	}
	if got := taxSubtotals(nil); got != nil {
		t.Errorf("nil lines: got %+v, want nil", got)
	}
	if got := taxSubtotals([]LineItem{}); got != nil {
		t.Errorf("empty lines: got %+v, want nil", got)
	}
}

func TestTaxSubtotals_NullOrNonDecimalMemberMakesThatAmountAbsent(t *testing.T) {
	cases := []struct {
		name  string
		total *string // line 2's line_total
		tax   *string // line 2's line_tax
		want  tsWant
	}{
		{"NULL line_total", nil, tsPtr("0.02"), tsWant{"STANDARD_VAT", "7.50", "", "0.03"}},
		{"NaN line_total", tsPtr("NaN"), tsPtr("0.02"), tsWant{"STANDARD_VAT", "7.50", "", "0.03"}},
		{"text line_total", tsPtr("abc"), tsPtr("0.02"), tsWant{"STANDARD_VAT", "7.50", "", "0.03"}},
		{"empty line_total", tsPtr(""), tsPtr("0.02"), tsWant{"STANDARD_VAT", "7.50", "", "0.03"}},
		{"NULL line_tax", tsPtr("0.20"), nil, tsWant{"STANDARD_VAT", "7.50", "0.30", ""}},
		{"NaN line_tax", tsPtr("0.20"), tsPtr("NaN"), tsWant{"STANDARD_VAT", "7.50", "0.30", ""}},
		{"both bad", nil, tsPtr("abc"), tsWant{"STANDARD_VAT", "7.50", "", ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := []LineItem{
				tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("0.10"), tsPtr("0.01")),
				tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), c.total, c.tax),
			}
			tsAssert(t, taxSubtotals(lines), []tsWant{c.want})
		})
	}
}

// An absent amount in one group leaves every other group's sums intact.
func TestTaxSubtotals_AnAbsentAmountStaysInItsOwnGroup(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), nil, tsPtr("0.75")),
		tsLine(2, tsPtr("ZERO_VAT"), tsPtr("0.00"), tsPtr("5.00"), tsPtr("0.00")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{
		{"STANDARD_VAT", "7.50", "", "0.75"},
		{"ZERO_VAT", "0.00", "5.00", "0.00"},
	})
}

func TestTaxSubtotals_DoesNotTouchTheCallersSlice(t *testing.T) {
	build := func() []LineItem {
		return []LineItem{
			tsLine(2, tsPtr("ZERO_VAT"), tsPtr("0.00"), tsPtr("5.00"), tsPtr("0.00")),
			tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("100.00"), tsPtr("7.50")),
		}
	}
	lines, before := build(), build()
	got := taxSubtotals(lines)
	if len(got) != 2 {
		t.Fatalf("got %d subtotals, want 2 (the check below needs a real call)", len(got))
	}
	if !reflect.DeepEqual(lines, before) {
		t.Errorf("caller's slice changed: %+v, want %+v", lines, before)
	}
	if lines[0].LineNo != 2 || lines[1].LineNo != 1 {
		t.Errorf("caller's slice reordered: line_no %d,%d", lines[0].LineNo, lines[1].LineNo)
	}
}

// Mutating a result must not reach back into the lines' strings.
func TestTaxSubtotals_ResultSharesNoPointerWithTheLines(t *testing.T) {
	pct, total, tax := tsPtr("7.50"), tsPtr("100.00"), tsPtr("7.50")
	lines := []LineItem{tsLine(1, tsPtr("STANDARD_VAT"), pct, total, tax)}
	got := taxSubtotals(lines)
	if len(got) != 1 {
		t.Fatalf("got %d subtotals, want 1", len(got))
	}
	if got[0].TaxPercent != nil {
		*got[0].TaxPercent = "X"
	}
	if got[0].TaxableAmount != nil {
		*got[0].TaxableAmount = "X"
	}
	if got[0].TaxAmount != nil {
		*got[0].TaxAmount = "X"
	}
	if *pct != "7.50" || *total != "100.00" || *tax != "7.50" {
		t.Errorf("lines changed through the result: %s %s %s", *pct, *total, *tax)
	}
}

func TestTaxSubtotals_RepeatedCallsAgree(t *testing.T) {
	lines := []LineItem{tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("1.00"), tsPtr("0.08"))}
	first := taxSubtotals(lines)
	second := taxSubtotals(lines)
	if len(first) != 1 || !reflect.DeepEqual(first, second) {
		t.Errorf("two calls differ or are empty: %+v vs %+v", first, second)
	}
}

// Parser edge cases: anything shopspring/decimal rejects leaves the amount absent,
// while a valid sibling amount on the same lines still sums.
func TestTaxSubtotals_UnparseableTextMakesTheAmountAbsent(t *testing.T) {
	for _, bad := range []string{"NaN", "nan", "Inf", "-Inf", "Infinity", "", " ", " 1.00", "1.00 ", "1_000", "0x10", "1,50", "1.2.3", "--1", "1e", "e1"} {
		t.Run(bad, func(t *testing.T) {
			lines := []LineItem{
				tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("1.00"), tsPtr("0.10")),
				tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr(bad), tsPtr("0.20")),
			}
			tsAssert(t, taxSubtotals(lines), []tsWant{{"STANDARD_VAT", "7.50", "", "0.30"}})
		})
	}
}

func TestTaxSubtotals_ShortFormDecimalsParse(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr(".5"), tsPtr("+1")),
		tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("5."), tsPtr("-0")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{{"STANDARD_VAT", "7.50", "5.50", "1.00"}})
}

// A bad first member keeps the amount absent through later valid members, and
// the other amount keeps summing across every line.
func TestTaxSubtotals_ABadFirstMemberStaysAbsentAndSpareAmountSums(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), nil, tsPtr("0.10")),
		tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("5.00"), tsPtr("0.20")),
		tsLine(3, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("6.00"), tsPtr("0.30")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{{"STANDARD_VAT", "7.50", "", "0.60"}})
}

func TestTaxSubtotals_AGroupOfOnlyInvalidValuesHasBothAmountsAbsent(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("NaN"), nil),
		tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("abc"), nil),
	}
	got := taxSubtotals(lines)
	if len(got) != 1 {
		t.Fatalf("got %d subtotals, want the group to stay listed", len(got))
	}
	tsAssert(t, got, []tsWant{{"STANDARD_VAT", "7.50", "", ""}})
}

// "" and NULL are different percents, and "" is a stored value that prints back as "".
func TestTaxSubtotals_EmptyPercentIsNotNullAndNotZero(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("EXEMPTED"), nil, tsPtr("1.00"), tsPtr("0.00")),
		tsLine(2, tsPtr("EXEMPTED"), tsPtr(""), tsPtr("2.00"), tsPtr("0.00")),
		tsLine(3, tsPtr("EXEMPTED"), tsPtr("0"), tsPtr("4.00"), tsPtr("0.00")),
		tsLine(4, tsPtr("EXEMPTED"), tsPtr(""), tsPtr("8.00"), tsPtr("0.00")),
	}
	got := taxSubtotals(lines)
	if len(got) != 3 {
		t.Fatalf("got %d subtotals %+v, want 3 (NULL, \"\", \"0\")", len(got), got)
	}
	if got[0].TaxPercent != nil || tsStr(got[0].TaxableAmount) != "1.00" {
		t.Errorf("group 0 = %s %s, want NULL percent, 1.00", tsStr(got[0].TaxPercent), tsStr(got[0].TaxableAmount))
	}
	if got[1].TaxPercent == nil || *got[1].TaxPercent != "" || tsStr(got[1].TaxableAmount) != "10.00" {
		t.Errorf("group 1 = %s %s, want empty-string percent, 10.00", tsStr(got[1].TaxPercent), tsStr(got[1].TaxableAmount))
	}
	if tsStr(got[2].TaxPercent) != "0" || tsStr(got[2].TaxableAmount) != "4.00" {
		t.Errorf("group 2 = %s %s, want 0, 4.00", tsStr(got[2].TaxPercent), tsStr(got[2].TaxableAmount))
	}
}

// An empty or padded category is a non-NULL value: it forms its own group.
func TestTaxSubtotals_EmptyAndPaddedCategoriesAreDistinctGroups(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr(""), tsPtr("7.50"), tsPtr("1.00"), tsPtr("0.10")),
		tsLine(2, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("2.00"), tsPtr("0.20")),
		tsLine(3, tsPtr("STANDARD_VAT "), tsPtr("7.50"), tsPtr("4.00"), tsPtr("0.40")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{
		{"", "7.50", "1.00", "0.10"},
		{"STANDARD_VAT", "7.50", "2.00", "0.20"},
		{"STANDARD_VAT ", "7.50", "4.00", "0.40"},
	})
}

// Equal line_no values keep slice order (stable sort). Lines alternate between
// two line_no values so an unstable sort has to move equal keys past each other.
func TestTaxSubtotals_DuplicateLineNoKeepsSliceOrder(t *testing.T) {
	var lines []LineItem
	var ones, twos []tsWant
	for i := 0; i < 60; i++ {
		cat := string(rune('A'+i/10)) + string(rune('a'+i%10))
		no := 2 - i%2
		lines = append(lines, tsLine(no, tsPtr(cat), tsPtr("1.00"), tsPtr("1.00"), tsPtr("0.01")))
		w := tsWant{cat, "1.00", "1.00", "0.01"}
		if no == 1 {
			ones = append(ones, w)
		} else {
			twos = append(twos, w)
		}
	}
	tsAssert(t, taxSubtotals(lines), append(ones, twos...))
}

// A categorised line between uncategorised ones neither splits nor drops a group.
func TestTaxSubtotals_UncategorisedLinesBetweenMembersDoNotSplitAGroup(t *testing.T) {
	lines := []LineItem{
		tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("1.00"), tsPtr("0.10")),
		tsLine(2, nil, tsPtr("7.50"), tsPtr("100.00"), tsPtr("10.00")),
		tsLine(3, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("2.00"), tsPtr("0.20")),
	}
	tsAssert(t, taxSubtotals(lines), []tsWant{{"STANDARD_VAT", "7.50", "3.00", "0.30"}})
}

// Changing the lines after the call must not change an earlier result.
func TestTaxSubtotals_LaterEditsToTheLinesDoNotChangeTheResult(t *testing.T) {
	pct, total, tax := tsPtr("7.50"), tsPtr("100.00"), tsPtr("7.50")
	got := taxSubtotals([]LineItem{tsLine(1, tsPtr("STANDARD_VAT"), pct, total, tax)})
	if len(got) != 1 || got[0].TaxPercent == nil {
		t.Fatalf("got %+v, want one subtotal with a percent", got)
	}
	*pct, *total, *tax = "X", "X", "X"
	tsAssert(t, got, []tsWant{{"STANDARD_VAT", "7.50", "100.00", "7.50"}})
}

// A sum that rounds to zero from below prints 0.00, never -0.00.
func TestTaxSubtotals_NegativeSumRoundingToZeroPrintsPlainZero(t *testing.T) {
	lines := []LineItem{tsLine(1, tsPtr("STANDARD_VAT"), tsPtr("7.50"), tsPtr("-0.001"), tsPtr("-0.004"))}
	tsAssert(t, taxSubtotals(lines), []tsWant{{"STANDARD_VAT", "7.50", "0.00", "0.00"}})
}

// The JSON keys are tax_category, tax_percent, taxable_amount, tax_amount;
// an absent value is null.
func TestTaxSubtotals_JSONKeysAndNullForAbsent(t *testing.T) {
	got := taxSubtotals([]LineItem{tsLine(1, tsPtr("EXEMPTED"), nil, nil, tsPtr("0.00"))})
	if len(got) != 1 {
		t.Fatalf("got %d subtotals, want 1", len(got))
	}
	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"tax_category":"EXEMPTED","tax_percent":null,"taxable_amount":null,"tax_amount":"0.00"}`
	if string(b) != want {
		t.Errorf("json = %s, want %s", b, want)
	}
}
