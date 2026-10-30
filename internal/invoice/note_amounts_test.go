package invoice

import (
	"errors"
	"reflect"
	"testing"
)

func nmLine(qty, price string, category, percent *string) noteMoneyLine {
	return noteMoneyLine{Quantity: qty, UnitPrice: price, TaxCategory: category, TaxPercent: percent}
}

var (
	nmStd  = strPtr("STANDARD_VAT")
	nmZero = strPtr("ZERO_VAT")
	nmP75  = strPtr("7.5")
	nmP0   = strPtr("0")
)

// nmWant is one vector of the form's own maths (draftTotals in invoiceDraft.ts).
type nmWant struct {
	name                 string
	lines                []noteMoneyLine
	subtotal, vat, total string
	taxes                []*string
}

var nmVectors = []nmWant{
	{"flat1", []noteMoneyLine{nmLine("1.5", "33.33", nil, nil)}, "50.00", "3.75", "53.75", []*string{nil}},
	{"flat2", []noteMoneyLine{nmLine("1.5", "33.33", nil, nil), nmLine("1.5", "33.33", nil, nil)}, "99.99", "7.50", "107.49", []*string{nil, nil}},
	{"tie", []noteMoneyLine{nmLine("1", "10.06", nmStd, nmP75), nmLine("1", "10.06", nmStd, nmP75), nmLine("1", "10.06", nmStd, nmP75)},
		"30.18", "2.26", "32.44", []*string{strPtr("0.76"), strPtr("0.75"), strPtr("0.75")}},
	{"rem", []noteMoneyLine{nmLine("1", "10.07", nmStd, nmP75), nmLine("1", "10.09", nmStd, nmP75), nmLine("1", "10.08", nmStd, nmP75)},
		"30.24", "2.27", "32.51", []*string{strPtr("0.75"), strPtr("0.76"), strPtr("0.76")}},
	{"mixed", []noteMoneyLine{nmLine("1", "10.06", nmStd, nmP75), nmLine("1", "5.00", nmZero, nmP0), nmLine("2", "10.06", nmStd, nmP75), nmLine("1", "4.00", nil, nil)},
		"39.18", "2.26", "41.44", []*string{strPtr("0.75"), strPtr("0.00"), strPtr("1.51"), nil}},
	{"nopct", []noteMoneyLine{nmLine("1", "10.00", nmStd, nil)}, "10.00", "0.00", "10.00", []*string{nil}},
	{"nopctmix", []noteMoneyLine{nmLine("1", "10.06", nmStd, nmP75), nmLine("1", "5.00", nmStd, nil)},
		"15.06", "0.75", "15.81", []*string{strPtr("0.75"), nil}},
}

func nmVector(name string) nmWant {
	for _, v := range nmVectors {
		if v.name == name {
			return v
		}
	}
	panic("no vector " + name)
}

func nmTexts(taxes []*string) []string {
	out := make([]string, len(taxes))
	for i, p := range taxes {
		if p == nil {
			out[i] = "<nil>"
		} else {
			out[i] = *p
		}
	}
	return out
}

func nmCheck(t *testing.T, v nmWant) noteMoney {
	t.Helper()
	got, err := noteAmounts(v.lines)
	if err != nil {
		t.Fatalf("%s: noteAmounts: %v", v.name, err)
	}
	if got.Subtotal != v.subtotal || got.VAT != v.vat || got.Total != v.total {
		t.Errorf("%s: (subtotal, vat, total) = (%s, %s, %s), want (%s, %s, %s)",
			v.name, got.Subtotal, got.VAT, got.Total, v.subtotal, v.vat, v.total)
	}
	if !reflect.DeepEqual(nmTexts(got.LineTaxes), nmTexts(v.taxes)) {
		t.Errorf("%s: line taxes = %v, want %v", v.name, nmTexts(got.LineTaxes), nmTexts(v.taxes))
	}
	return got
}

func TestNoteAmounts_Vectors(t *testing.T) {
	if len(nmVectors) != 7 {
		t.Fatalf("vectors = %d, want the 7 of Decision 15", len(nmVectors))
	}
	for _, v := range nmVectors {
		t.Run(v.name, func(t *testing.T) { nmCheck(t, v) })
	}
}

// Each 1.5 x 33.33 line is 49.995, so the rounded line totals sum to 100.00 and the exact sum rounds to 99.99.
func TestNoteAmounts_SubtotalRoundsTheExactSum(t *testing.T) {
	got := nmCheck(t, nmVector("flat2"))
	if got.Subtotal == "100.00" {
		t.Errorf("subtotal = 100.00, the sum of the rounded line totals")
	}
	if want := []string{"50.00", "50.00"}; !reflect.DeepEqual(got.LineTotals, want) {
		t.Errorf("line totals = %v, want %v (round half up)", got.LineTotals, want)
	}
}

// tie: three equal remainders, the leftover kobo goes to the lower line. rem: it goes to the two largest remainders.
func TestNoteAmounts_LeftoverKoboGoesToLargestRemainderThenLowerLine(t *testing.T) {
	rem := nmCheck(t, nmVector("rem"))
	tie := nmCheck(t, nmVector("tie"))
	if rem.LineTaxes[0] == nil || *rem.LineTaxes[0] != "0.75" || tie.LineTaxes[0] == nil || *tie.LineTaxes[0] != "0.76" {
		t.Errorf("first-line taxes = (%v, %v), want rem 0.75 (smallest remainder) and tie 0.76 (lowest line)",
			nmTexts(rem.LineTaxes[:1]), nmTexts(tie.LineTaxes[:1]))
	}
}

// A categorised line with no percent joins no group, and the note still counts as categorised: no flat 7.5% fallback.
func TestNoteAmounts_CategorisedLineWithNoPercentHasNoTax(t *testing.T) {
	for _, name := range []string{"nopct", "nopctmix"} {
		v := nmVector(name)
		got := nmCheck(t, v)
		if got.LineTaxes[len(got.LineTaxes)-1] != nil {
			t.Errorf("%s: last line tax = %s, want nil", name, *got.LineTaxes[len(got.LineTaxes)-1])
		}
		flat := map[string]string{"nopct": "0.75", "nopctmix": "1.13"}[name]
		if got.VAT == flat {
			t.Errorf("%s: vat = %s, the flat-rate fallback", name, got.VAT)
		}
	}
}

// Groups key on (category, percent to 2dp): the same percent spelled two ways is one group, another category is another.
func TestNoteAmounts_GroupsByCategoryAndPercentToTwoDecimals(t *testing.T) {
	other := strPtr("OTHER_VAT")
	cases := []nmWant{
		{"7.5 and 7.50 are one group", []noteMoneyLine{nmLine("1", "10.06", nmStd, nmP75), nmLine("1", "10.06", nmStd, strPtr("7.50"))},
			"20.12", "1.51", "21.63", []*string{strPtr("0.76"), strPtr("0.75")}},
		{"7.5 and 7.501 are one group", []noteMoneyLine{nmLine("1", "10.06", nmStd, nmP75), nmLine("1", "10.06", nmStd, strPtr("7.501"))},
			"20.12", "1.51", "21.63", []*string{strPtr("0.76"), strPtr("0.75")}},
		{"another category is another group", []noteMoneyLine{nmLine("1", "10.06", nmStd, nmP75), nmLine("1", "10.06", other, nmP75)},
			"20.12", "1.50", "21.62", []*string{strPtr("0.75"), strPtr("0.75")}},
		{"another percent is another group", []noteMoneyLine{nmLine("1", "10.06", nmStd, nmP75), nmLine("1", "10.06", nmStd, strPtr("8"))},
			"20.12", "1.55", "21.67", []*string{strPtr("0.75"), strPtr("0.80")}},
		{"an empty category is no category: flat rate", []noteMoneyLine{nmLine("1", "10.00", strPtr(""), nmP75)},
			"10.00", "0.75", "10.75", []*string{nil}},
		{"a line total rounds half up", []noteMoneyLine{nmLine("0.001", "5.00", nil, nil)},
			"0.01", "0.00", "0.01", []*string{nil}},
	}
	for _, v := range cases {
		t.Run(v.name, func(t *testing.T) {
			got := nmCheck(t, v)
			if v.name == "a line total rounds half up" && got.LineTotals[0] != "0.01" {
				t.Errorf("line total = %s, want 0.01", got.LineTotals[0])
			}
		})
	}
}

// A value the quantity, unit price, line total or total column cannot hold is refused here, before the database.
func TestNoteAmounts_RefusesWhatTheColumnsCannotHold(t *testing.T) {
	if _, err := noteAmounts([]noteMoneyLine{nmLine("1.000", "100.00", nil, nil)}); err != nil {
		t.Fatalf("control: a valid line: %v", err)
	}
	for _, c := range []struct{ name, qty, price string }{
		{"quantity 10^11", "100000000000", "0.00"},
		{"quantity 4 decimals", "1.0005", "1.00"},
		{"quantity zero", "0", "1.00"},
		{"unit price 10^12 on a small quantity", "0.001", "1000000000000"},
		{"unit price 3 decimals", "1", "1.005"},
		{"unit price negative", "1", "-0.01"},
		{"line total 10^12", "1000", "1000000000.00"},
		{"total 10^12 through two lines", "1", "999999999999.99"},
	} {
		lines := []noteMoneyLine{nmLine(c.qty, c.price, nil, nil)}
		if c.name == "total 10^12 through two lines" {
			lines = append(lines, nmLine("1", "0.01", nil, nil))
		}
		if _, err := noteAmounts(lines); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", c.name, err)
		}
	}
}
