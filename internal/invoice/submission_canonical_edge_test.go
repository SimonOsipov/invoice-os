package invoice

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// distinctInvoice sets every content field to a value no other field shares.
func distinctInvoice(t *testing.T, lines int) Invoice {
	t.Helper()
	var inv Invoice
	idx, tags := contentFields(reflect.TypeOf(inv))
	for n, i := range idx {
		f := reflect.ValueOf(&inv).Elem().Field(i)
		setSample(t, f, tags[n])
		if f.Type() == reflect.TypeOf((*time.Time)(nil)) {
			d := time.Date(2031, 1, 1+n, 0, 0, 0, 0, time.UTC)
			f.Set(reflect.ValueOf(&d))
		}
	}
	lIdx, lTags := contentFields(reflect.TypeOf(LineItem{}))
	for k := 0; k < lines; k++ {
		var li LineItem
		for n, i := range lIdx {
			setSample(t, reflect.ValueOf(&li).Elem().Field(i), lTags[n])
			if p, ok := reflect.ValueOf(&li).Elem().Field(i).Interface().(*string); ok {
				s := *p + "-l" + string(rune('a'+k))
				reflect.ValueOf(&li).Elem().Field(i).Set(reflect.ValueOf(&s))
			}
		}
		li.ID, li.LineNo = "line-"+string(rune('a'+k)), k+1
		inv.LineItems = append(inv.LineItems, li)
	}
	return inv
}

// leaves collects every non-TaxSubtotals pointer leaf of v as a comparable string.
func leaves(v reflect.Value, out map[string]int) {
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeOf(time.Time{}) {
			out[v.Interface().(time.Time).String()]++
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).Name == "TaxSubtotals" {
				continue
			}
			leaves(v.Field(i), out)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			leaves(v.Index(i), out)
		}
	case reflect.Ptr:
		if !v.IsNil() {
			leaves(v.Elem(), out)
		}
	case reflect.String:
		out[v.String()]++
	}
}

func TestSubmissionCanonical_EveryFieldSetAtOnceLandsOnceOnItsOwnParty(t *testing.T) {
	inv := distinctInvoice(t, 3)
	c := SubmissionCanonical(inv)

	idx, _ := contentFields(reflect.TypeOf(inv))
	if len(idx) < 32 {
		t.Fatalf("walked %d header fields, want 32", len(idx))
	}
	for _, i := range idx {
		name := reflect.TypeOf(inv).Field(i).Name
		got := canonicalTarget(reflect.ValueOf(c), name)
		if !got.IsValid() || !reflect.DeepEqual(got.Interface(), reflect.ValueOf(inv).Field(i).Interface()) {
			t.Errorf("Canonical for Invoice.%s = %v, want %v", name, got, reflect.ValueOf(inv).Field(i).Interface())
		}
	}

	seen := map[string]int{}
	leaves(reflect.ValueOf(c), seen)
	for _, i := range idx {
		want := map[string]int{}
		leaves(reflect.ValueOf(inv).Field(i), want)
		for v := range want {
			if seen[v] != 1 {
				t.Errorf("Invoice.%s value %q appears %d times in Canonical, want 1", reflect.TypeOf(inv).Field(i).Name, v, seen[v])
			}
		}
	}
}

func TestSubmissionCanonical_EachLineCarriesItsOwnNRSFields(t *testing.T) {
	inv := distinctInvoice(t, 3)
	c := SubmissionCanonical(inv)
	if len(c.Lines) != 3 {
		t.Fatalf("len(Lines) = %d, want 3", len(c.Lines))
	}
	lIdx, _ := contentFields(reflect.TypeOf(LineItem{}))
	if len(lIdx) != 15 {
		t.Fatalf("walked %d line fields, want 15", len(lIdx))
	}
	for k, li := range inv.LineItems {
		for _, i := range lIdx {
			name := reflect.TypeOf(li).Field(i).Name
			got := reflect.ValueOf(c.Lines[k]).FieldByName(name)
			if !got.IsValid() || !reflect.DeepEqual(got.Interface(), reflect.ValueOf(li).Field(i).Interface()) {
				t.Errorf("Lines[%d].%s = %v, want %v", k, name, got, reflect.ValueOf(li).Field(i).Interface())
			}
		}
	}
}

func TestSubmissionCanonical_TaxSubtotalsValuesAndShape(t *testing.T) {
	lines := []LineItem{
		{LineNo: 1, TaxCategory: strPtr("STANDARD_VAT"), TaxPercent: strPtr("7.50"), LineTotal: strPtr("100.00"), LineTax: strPtr("7.50")},
		{LineNo: 2, TaxCategory: strPtr("ZERO_RATED"), TaxPercent: strPtr("0"), LineTotal: strPtr("50.00"), LineTax: strPtr("0.00")},
		{LineNo: 3, TaxCategory: strPtr("STANDARD_VAT"), TaxPercent: strPtr("7.50"), LineTotal: strPtr("20.00"), LineTax: strPtr("1.50")},
		{LineNo: 4, LineTotal: strPtr("999.00"), LineTax: strPtr("99.00")},
	}
	got := SubmissionCanonical(Invoice{LineItems: lines}).TaxSubtotals
	type row struct{ cat, pct, base, tax string }
	want := []row{{"STANDARD_VAT", "7.50", "120.00", "9.00"}, {"ZERO_RATED", "0", "50.00", "0.00"}}
	if len(got) != len(want) {
		t.Fatalf("got %d TaxSubtotals, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Percent == nil || g.TaxableAmount == nil || g.TaxAmount == nil {
			t.Fatalf("TaxSubtotals[%d] has a nil member: %+v", i, g)
		}
		if g.Category != w.cat || *g.Percent != w.pct || *g.TaxableAmount != w.base || *g.TaxAmount != w.tax {
			t.Errorf("TaxSubtotals[%d] = %s %s %s %s, want %+v", i, g.Category, *g.Percent, *g.TaxableAmount, *g.TaxAmount, w)
		}
	}

	*lines[0].TaxPercent = "99"
	if *got[0].Percent != "7.50" {
		t.Errorf("TaxSubtotals[0].Percent changed to %s with the line edit: it aliases the line", *got[0].Percent)
	}
}

func TestSubmissionCanonical_NoCategorisedLineGivesNilTaxSubtotals(t *testing.T) {
	c := SubmissionCanonical(Invoice{LineItems: []LineItem{{LineNo: 1, LineTotal: strPtr("10.00"), LineTax: strPtr("0.75")}}})
	if c.TaxSubtotals != nil {
		t.Errorf("TaxSubtotals = %+v, want nil", c.TaxSubtotals)
	}
	if len(c.Lines) != 1 {
		t.Errorf("len(Lines) = %d, want 1", len(c.Lines))
	}
}

func TestSubmissionCanonical_NRSMappingLeavesTheInvoiceUntouched(t *testing.T) {
	inv := distinctInvoice(t, 3)
	inv.LineItems[0].LineNo, inv.LineItems[2].LineNo = 3, 1
	before, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{inv.LineItems[0].ID, inv.LineItems[1].ID, inv.LineItems[2].ID}

	c := SubmissionCanonical(inv)

	after, _ := json.Marshal(inv)
	if string(before) != string(after) {
		t.Errorf("SubmissionCanonical changed the invoice:\nbefore %s\nafter  %s", before, after)
	}
	for i, l := range c.Lines {
		if l.LineID != order[i] {
			t.Errorf("Lines[%d].LineID = %q, want %q (input order)", i, l.LineID, order[i])
		}
	}
}
