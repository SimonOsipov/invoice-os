package erp

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

func mappedWith(codes, items []string) Mapped {
	m := Mapped{CustomerID: "c1", TaxCodes: codes, Items: items}
	m.In.BuyerTIN, m.In.BuyerState, m.In.BuyerLGA = sp("1234567897"), sp("mapped-state"), sp("mapped-lga")
	for i := range codes {
		m.In.LineItems = append(m.In.LineItems, invoice.LineItemInput{
			TaxCategory: sp(fmt.Sprintf("cat%d", i+1)), HSNCode: sp(fmt.Sprintf("hsn%d", i+1)), ISICCode: sp(fmt.Sprintf("isic%d", i+1)),
		})
	}
	return m
}

func TestApplyLookups_WritesEachFoundValue(t *testing.T) {
	m := mappedWith([]string{"A", "B"}, []string{"i1", "i2"})
	ApplyLookups(&m, []Lookup{
		{Kind: "tax_code", ERPKey: "A", TaxCategory: sp("ZERO_VAT")},
		{Kind: "item", ERPKey: "i1", HSNCode: sp("H")},
		{Kind: "item", ERPKey: "i2", ServiceCode: sp("S")},
		{Kind: "customer", ERPKey: "c1", TIN: sp("12345678-0001"), State: sp("Lagos"), LGA: sp("Ikeja")},
	})
	ls := m.In.LineItems
	if *ls[0].TaxCategory != "ZERO_VAT" || *ls[1].TaxCategory != "cat2" {
		t.Errorf("tax categories = %q, %q", *ls[0].TaxCategory, *ls[1].TaxCategory)
	}
	if *ls[0].HSNCode != "H" || *ls[1].HSNCode != "hsn2" {
		t.Errorf("hsn = %q, %q", *ls[0].HSNCode, *ls[1].HSNCode)
	}
	if *ls[0].ISICCode != "isic1" || *ls[1].ISICCode != "S" {
		t.Errorf("isic = %q, %q", *ls[0].ISICCode, *ls[1].ISICCode)
	}
	if *m.In.BuyerTIN != "12345678-0001" || *m.In.BuyerState != "Lagos" || *m.In.BuyerLGA != "Ikeja" {
		t.Errorf("buyer = %q %q %q", *m.In.BuyerTIN, *m.In.BuyerState, *m.In.BuyerLGA)
	}
}

func TestApplyLookups_NilFieldsLeaveMappedValues(t *testing.T) {
	m := mappedWith([]string{"A"}, []string{"i1"})
	ApplyLookups(&m, []Lookup{
		{Kind: "customer", ERPKey: "c1", State: sp("Lagos")},
		{Kind: "item", ERPKey: "i1", HSNCode: sp("H")},
	})
	if *m.In.BuyerTIN != "1234567897" || *m.In.BuyerLGA != "mapped-lga" || *m.In.BuyerState != "Lagos" {
		t.Errorf("buyer = %q %q %q", *m.In.BuyerTIN, *m.In.BuyerState, *m.In.BuyerLGA)
	}
	if l := m.In.LineItems[0]; *l.ISICCode != "isic1" || *l.HSNCode != "H" || *l.TaxCategory != "cat1" {
		t.Errorf("line = %q %q %q", *l.ISICCode, *l.HSNCode, *l.TaxCategory)
	}
}

func TestApplyLookups_ExemptionCodeIsNeverApplied(t *testing.T) {
	m := mappedWith([]string{"A"}, []string{""})
	ApplyLookups(&m, []Lookup{{Kind: "tax_code", ERPKey: "A", TaxCategory: sp("EXEMPTED"), ExemptionCode: sp("2915.3100")}})
	if *m.In.LineItems[0].TaxCategory != "EXEMPTED" {
		t.Errorf("tax category = %q, want EXEMPTED", *m.In.LineItems[0].TaxCategory)
	}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Struct:
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		case reflect.Slice:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.String:
			if strings.Contains(v.String(), "2915.3100") {
				t.Errorf("the exemption code reached the input: %q", v.String())
			}
		}
	}
	walk(reflect.ValueOf(m.In))
}

func TestApplyLookups_EmptyKeysMatchNothing(t *testing.T) {
	m := mappedWith([]string{""}, []string{""})
	m.CustomerID = ""
	want := mappedWith([]string{""}, []string{""})
	want.CustomerID = ""
	ApplyLookups(&m, []Lookup{
		{Kind: "customer", ERPKey: "x", TIN: sp("12345678-0001")},
		{Kind: "tax_code", ERPKey: "x", TaxCategory: sp("ZERO_VAT")},
		{Kind: "item", ERPKey: "x", HSNCode: sp("H")},
	})
	if !reflect.DeepEqual(m, want) {
		t.Errorf("input changed: %+v", m.In)
	}
}
