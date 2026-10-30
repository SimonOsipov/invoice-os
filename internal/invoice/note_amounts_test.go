package invoice

import (
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
