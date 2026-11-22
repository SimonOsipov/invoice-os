package erp

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

func sp(s string) *string { return &s }

// decEq compares two decimal strings by value, so "7.5" equals "7.50".
func decEq(t *testing.T, what string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %s", what, want)
		return
	}
	g, ok1 := new(big.Rat).SetString(*got)
	w, ok2 := new(big.Rat).SetString(want)
	if !ok1 || !ok2 || g.Cmp(w) != 0 {
		t.Errorf("%s = %q, want %s", what, *got, want)
	}
}

// exact compares the stored text, for the amounts the mapping rounds to two places.
func exact(t *testing.T, what string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %q", what, shown(got), want)
	}
}

func shown(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func bodyOf(t *testing.T, number, date, customer string, lines ...fakeLine) Document {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"number": number, "issue_date": date, "currency": "NGN", "customer_id": customer, "lines": lines,
	})
	if err != nil {
		t.Fatal(err)
	}
	return Document{Ref: DocRef{SourceDocID: "d", SourceRevision: "1", Scope: "fake-co-1"}, ContentType: jsonType, Body: b}
}

func TestMapFake_MapsTheSeededInvoice(t *testing.T) {
	f := testFake(t)
	doc, err := f.Fetch(ctx, cr, DocRef{SourceDocID: "fake-co-1-inv-1", Scope: "fake-co-1"})
	if err != nil {
		t.Fatal(err)
	}
	party, err := f.FetchParty(ctx, cr, "fake-co-1", "fake-co-1-cust-1")
	if err != nil {
		t.Fatal(err)
	}
	in, err := mapFake(doc, &party)
	if err != nil {
		t.Fatalf("mapFake: %v", err)
	}
	if in.InvoiceNumber != "FK1-0001" {
		t.Errorf("InvoiceNumber = %q", in.InvoiceNumber)
	}
	if in.IssueDate == nil || in.IssueDate.Format("2006-01-02") != "2026-01-05" {
		t.Errorf("IssueDate = %v", in.IssueDate)
	}
	exact(t, "Currency", in.Currency, "NGN")
	exact(t, "BuyerName", in.BuyerName, "Adewale Traders Ltd")
	exact(t, "BuyerTIN", in.BuyerTIN, "1234567897")
	exact(t, "BuyerEmail", in.BuyerEmail, "accounts@adewale.example")
	exact(t, "Subtotal", in.Subtotal, "100000.00")
	exact(t, "VAT", in.VAT, "7500.00")
	exact(t, "Total", in.Total, "107500.00")
	if in.EntityID != "" || in.SupplierName != nil || in.SupplierTIN != nil {
		t.Errorf("entity and supplier belong to the ingest: %q %v %v", in.EntityID, in.SupplierName, in.SupplierTIN)
	}
	if len(in.LineItems) != 1 {
		t.Fatalf("lines = %d, want 1", len(in.LineItems))
	}
	l := in.LineItems[0]
	exact(t, "Description", l.Description, "Consulting services")
	decEq(t, "Quantity", l.Quantity, "1")
	decEq(t, "UnitPrice", l.UnitPrice, "100000.00")
	decEq(t, "TaxPercent", l.TaxPercent, "7.5")
	exact(t, "LineTotal", l.LineTotal, "100000.00")
	exact(t, "LineTax", l.LineTax, "7500.00")
}

func TestMapFake_RoundsEachLineHalfAwayFromZero(t *testing.T) {
	doc := bodyOf(t, "R-1", "2026-05-01", "fake-co-1-cust-1",
		fakeLine{"a", "3", "33.335", "7.5"},
		fakeLine{"b", "1", "0.125", "0"},
		fakeLine{"c", "2", "10.00", "7.5"},
	)
	in, err := mapFake(doc, nil)
	if err != nil {
		t.Fatalf("mapFake: %v", err)
	}
	if len(in.LineItems) != 3 {
		t.Fatalf("lines = %d, want 3", len(in.LineItems))
	}
	// 3 x 33.335 is 100.005; its tax is taken on the rounded 100.01 (7.50075).
	want := [][2]string{{"100.01", "7.50"}, {"0.13", "0.00"}, {"20.00", "1.50"}}
	for i, w := range want {
		exact(t, "LineTotal", in.LineItems[i].LineTotal, w[0])
		exact(t, "LineTax", in.LineItems[i].LineTax, w[1])
	}
	exact(t, "Subtotal", in.Subtotal, "120.14")
	exact(t, "VAT", in.VAT, "9.00")
	exact(t, "Total", in.Total, "129.14")
}

func TestMapFake_NegativeLineRoundsAwayFromZero(t *testing.T) {
	doc := bodyOf(t, "N-1", "2026-05-01", "fake-co-1-cust-1",
		fakeLine{"credit", "1", "-0.125", "0"},
		fakeLine{"credit tax", "1", "-0.05", "10"},
	)
	in, err := mapFake(doc, nil)
	if err != nil {
		t.Fatalf("mapFake: %v", err)
	}
	if len(in.LineItems) != 2 {
		t.Fatalf("lines = %d, want 2", len(in.LineItems))
	}
	exact(t, "LineTotal", in.LineItems[0].LineTotal, "-0.13")
	exact(t, "LineTotal", in.LineItems[1].LineTotal, "-0.05")
	// -0.05 x 10% is -0.005, which rounds away from zero to -0.01.
	exact(t, "LineTax", in.LineItems[1].LineTax, "-0.01")
	exact(t, "Subtotal", in.Subtotal, "-0.18")
	exact(t, "VAT", in.VAT, "-0.01")
	exact(t, "Total", in.Total, "-0.19")
}

func TestMapFake_RefusesEachUnparseableField(t *testing.T) {
	good := func() map[string]any {
		return map[string]any{
			"number": "X-1", "issue_date": "2026-05-01", "currency": "NGN", "customer_id": "fake-co-1-cust-1",
			"lines": []map[string]string{{"description": "d", "quantity": "1", "unit_price": "10.00", "vat_rate": "7.5"}},
		}
	}
	line := func(m map[string]any, k, v string) {
		m["lines"].([]map[string]string)[0][k] = v
	}
	if _, err := mapFake(docFrom(t, good()), nil); err != nil {
		t.Fatalf("the valid base must map: %v", err)
	}
	for _, c := range []struct {
		name, field, bad string
		doc              func() Document
	}{
		{"body", "body", "{", func() Document { d := docFrom(t, good()); d.Body = []byte("{"); return d }},
		{"issue_date", "issue_date", "05/01/2026", func() Document { m := good(); m["issue_date"] = "05/01/2026"; return docFrom(t, m) }},
		{"quantity", "quantity", "1,5", func() Document { m := good(); line(m, "quantity", "1,5"); return docFrom(t, m) }},
		{"unit_price", "unit_price", "", func() Document { m := good(); line(m, "unit_price", ""); return docFrom(t, m) }},
		{"vat_rate", "vat_rate", "abc", func() Document { m := good(); line(m, "vat_rate", "abc"); return docFrom(t, m) }},
		{"number", "number", "  ", func() Document { m := good(); m["number"] = "  "; return docFrom(t, m) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := mapFake(c.doc(), nil)
			if !errors.Is(err, ErrUnmappable) {
				t.Fatalf("err = %v, want ErrUnmappable", err)
			}
			if !strings.Contains(err.Error(), c.field) {
				t.Errorf("error %q does not name %q", err, c.field)
			}
			if c.bad != "" && strings.Contains(err.Error(), c.bad) {
				t.Errorf("error %q repeats the bad value %q", err, c.bad)
			}
		})
	}
}

func docFrom(t *testing.T, m map[string]any) Document {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return Document{Ref: DocRef{SourceDocID: "d", SourceRevision: "1", Scope: "fake-co-1"}, ContentType: jsonType, Body: b}
}

func TestMapFake_UnknownCustomerLeavesTheBuyerEmpty(t *testing.T) {
	sc := scriptConnector{Connector: testFake(t), fetch: func(context.Context, Credentials, DocRef) (Document, error) {
		return bodyOf(t, "U-1", "2026-05-01", "fake-co-1-gone", fakeLine{"d", "1", "10.00", "7.5"}), nil
	}}
	doc, err := sc.Fetch(ctx, cr, DocRef{SourceDocID: "x", Scope: "fake-co-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.FetchParty(ctx, cr, "fake-co-1", "fake-co-1-gone"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FetchParty = %v, want ErrNotFound", err)
	}
	in, err := mapFake(doc, nil)
	if err != nil {
		t.Fatalf("mapFake: %v", err)
	}
	if in.InvoiceNumber != "U-1" || len(in.LineItems) != 1 {
		t.Errorf("the invoice must still map: %+v", in)
	}
	if in.BuyerName != nil || in.BuyerTIN != nil || in.BuyerEmail != nil {
		t.Errorf("buyer = %v %v %v, want all nil", shown(in.BuyerName), shown(in.BuyerTIN), shown(in.BuyerEmail))
	}
}

func TestMapperFor_OnlyTheFakeHasAFixedMapping(t *testing.T) {
	m, err := mapperFor("fake")
	if err != nil || m == nil {
		t.Fatalf("fake: %v, %v", m == nil, err)
	}
	doc, err := testFake(t).Fetch(ctx, cr, DocRef{SourceDocID: "fake-co-2-inv-1", Scope: "fake-co-2"})
	if err != nil {
		t.Fatal(err)
	}
	if in, err := m(doc, nil); err != nil || in.InvoiceNumber != "FK2-0001" {
		t.Errorf("the fake's mapper gave %q, %v", in.InvoiceNumber, err)
	}
	for _, sys := range []string{"quickbooks", ""} {
		if m, err := mapperFor(sys); !errors.Is(err, ErrNoMapping) || m != nil {
			t.Errorf("%q: mapper nil=%v, err = %v, want ErrNoMapping", sys, m == nil, err)
		}
	}
}

// hashBase is a fully mapped input; its names and amounts are all inside the projection.
func hashBase() invoice.CreateInput {
	d := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	return invoice.CreateInput{
		InvoiceNumber: "H-1", IssueDate: &d, Currency: sp("NGN"),
		BuyerName: sp("Buyer"), BuyerTIN: sp("1234567897"), BuyerEmail: sp("b@example.test"),
		Subtotal: sp("100.00"), VAT: sp("7.50"), Total: sp("107.50"),
		LineItems: []invoice.LineItemInput{{
			Description: sp("Work"), Quantity: sp("1"), UnitPrice: sp("100.00"), TaxPercent: sp("7.5"),
			LineTotal: sp("100.00"), LineTax: sp("7.50"),
		}},
	}
}

// fill sets every field of v whose name is not in keep to a non-zero value.
func fill(t *testing.T, v reflect.Value, keep ...string) []string {
	t.Helper()
	var set []string
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if slices.Contains(keep, f.Name) || !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Pointer:
			p := reflect.New(fv.Type().Elem())
			if p.Elem().Kind() == reflect.String {
				p.Elem().SetString("outside")
			}
			if p.Elem().Type() == reflect.TypeOf(time.Time{}) {
				p.Elem().Set(reflect.ValueOf(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
			}
			fv.Set(p)
		case reflect.String:
			fv.SetString("outside")
		case reflect.Bool:
			fv.SetBool(true)
		case reflect.Slice:
			fv.Set(reflect.MakeSlice(fv.Type(), 1, 1))
		default:
			continue
		}
		set = append(set, f.Name)
	}
	return set
}

var hex64 = regexp.MustCompile("^[0-9a-f]{64}$")

func TestContentHash_StableAndBlindToProvenance(t *testing.T) {
	a, b := ContentHash(hashBase()), ContentHash(hashBase())
	if !hex64.MatchString(a) {
		t.Fatalf("hash %q is not 64 lower-case hex", a)
	}
	if a != b {
		t.Errorf("equal inputs hash to %s and %s", a, b)
	}

	// Every CreateInput field outside the projection, ERPConnectionID and InvoiceKind among them.
	inside := []string{"InvoiceNumber", "IssueDate", "Currency", "BuyerName", "BuyerTIN", "BuyerEmail", "Subtotal", "VAT", "Total", "LineItems"}
	probe := hashBase()
	outside := fill(t, reflect.ValueOf(&probe).Elem(), inside...)
	if !slices.Contains(outside, "InvoiceKind") {
		t.Fatalf("expected InvoiceKind among the outside fields, got %v", outside)
	}
	for _, name := range outside {
		in := hashBase()
		v := reflect.ValueOf(&in).Elem()
		fill(t, v, append(slices.Clone(inside), others(v.Type(), name)...)...)
		if got := ContentHash(in); got != a {
			t.Errorf("setting %s moved the hash", name)
		}
	}

	// Line fields outside the projection.
	lineInside := []string{"Description", "Quantity", "UnitPrice", "TaxPercent", "LineTotal", "LineTax"}
	lineProbe := invoice.LineItemInput{}
	lineOutside := fill(t, reflect.ValueOf(&lineProbe).Elem(), lineInside...)
	if len(lineOutside) == 0 {
		t.Fatal("no line field outside the projection")
	}
	for _, name := range lineOutside {
		in := hashBase()
		lv := reflect.ValueOf(&in.LineItems[0]).Elem()
		fill(t, lv, append(slices.Clone(lineInside), others(lv.Type(), name)...)...)
		if got := ContentHash(in); got != a {
			t.Errorf("setting line %s moved the hash", name)
		}
	}
}

// others names every field of t except name.
func others(t reflect.Type, name string) []string {
	var out []string
	for i := range t.NumField() {
		if n := t.Field(i).Name; n != name {
			out = append(out, n)
		}
	}
	return out
}

func TestContentHash_ChangesWithAnyProjectedValue(t *testing.T) {
	base := ContentHash(hashBase())
	if !hex64.MatchString(base) {
		t.Fatalf("hash %q is not 64 lower-case hex", base)
	}
	d := time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	for name, mut := range map[string]func(*invoice.CreateInput){
		"InvoiceNumber":    func(in *invoice.CreateInput) { in.InvoiceNumber = "H-2" },
		"IssueDate":        func(in *invoice.CreateInput) { in.IssueDate = &d },
		"Currency":         func(in *invoice.CreateInput) { in.Currency = sp("USD") },
		"BuyerName":        func(in *invoice.CreateInput) { in.BuyerName = sp("Other") },
		"BuyerTIN":         func(in *invoice.CreateInput) { in.BuyerTIN = sp("2345678904") },
		"BuyerEmail":       func(in *invoice.CreateInput) { in.BuyerEmail = sp("o@example.test") },
		"Subtotal":         func(in *invoice.CreateInput) { in.Subtotal = sp("100.01") },
		"VAT":              func(in *invoice.CreateInput) { in.VAT = sp("7.51") },
		"Total":            func(in *invoice.CreateInput) { in.Total = sp("107.51") },
		"line Description": func(in *invoice.CreateInput) { in.LineItems[0].Description = sp("Other") },
		"line Quantity":    func(in *invoice.CreateInput) { in.LineItems[0].Quantity = sp("2") },
		"line UnitPrice":   func(in *invoice.CreateInput) { in.LineItems[0].UnitPrice = sp("100.01") },
		"line TaxPercent":  func(in *invoice.CreateInput) { in.LineItems[0].TaxPercent = sp("5") },
		"line LineTotal":   func(in *invoice.CreateInput) { in.LineItems[0].LineTotal = sp("100.01") },
		"line LineTax":     func(in *invoice.CreateInput) { in.LineItems[0].LineTax = sp("7.51") },
		"an extra line": func(in *invoice.CreateInput) {
			in.LineItems = append(in.LineItems, in.LineItems[0])
		},
		"no lines": func(in *invoice.CreateInput) { in.LineItems = nil },
	} {
		in := hashBase()
		mut(&in)
		if got := ContentHash(in); got == base {
			t.Errorf("changing %s left the hash unchanged", name)
		}
	}
}

func TestContentHash_DecimalsHashByValue(t *testing.T) {
	if !hex64.MatchString(ContentHash(hashBase())) {
		t.Fatal("the hash must be 64 lower-case hex")
	}
	a, b := hashBase(), hashBase()
	a.LineItems[0].UnitPrice, a.Subtotal = sp("100"), sp("100")
	b.LineItems[0].UnitPrice, b.Subtotal = sp("100.00"), sp("100.00")
	if ContentHash(a) != ContentHash(b) {
		t.Error("100 and 100.00 must hash alike")
	}
}

func TestHashedContent_FieldsArePinned(t *testing.T) {
	// The order is part of the hash: a reorder moves every stored hash of this version.
	names := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			out = append(out, rt.Field(i).Name)
		}
		return out
	}
	want := []string{"InvoiceNumber", "IssueDate", "Currency", "BuyerName", "BuyerTIN", "BuyerEmail", "Subtotal", "VAT", "Total", "Lines"}
	if got := names(hashedContent{}); !slices.Equal(got, want) {
		t.Errorf("hashedContent fields = %v, want %v", got, want)
	}
	wantLine := []string{"Description", "Quantity", "UnitPrice", "TaxPercent", "LineTotal", "LineTax"}
	if got := names(hashedLine{}); !slices.Equal(got, wantLine) {
		t.Errorf("hashedLine fields = %v, want %v", got, wantLine)
	}
	if f, ok := reflect.TypeOf(hashedContent{}).FieldByName("Lines"); !ok || f.Type != reflect.TypeOf([]hashedLine{}) {
		t.Errorf("Lines = %v, want []hashedLine", f.Type)
	}
	if HashVersion != 1 {
		t.Errorf("HashVersion = %d, want 1", HashVersion)
	}
}

func TestDiffers_AmountsOrLines(t *testing.T) {
	in := hashBase()
	invOf := func(sub, vat, tot *string) invoiceAmounts { return invoiceAmounts{sub, vat, tot} }
	same := func() []lineAmounts {
		return []lineAmounts{{Description: sp("Work"), Quantity: sp("1"), UnitPrice: sp("100.00"), LineTotal: sp("100.00"), LineTax: sp("7.50")}}
	}
	with := func(f func(l *lineAmounts)) []lineAmounts { ls := same(); f(&ls[0]); return ls }
	for _, c := range []struct {
		name  string
		inv   invoiceAmounts
		lines []lineAmounts
		want  bool
	}{
		{"equal", invOf(sp("100.00"), sp("7.50"), sp("107.50")), same(), false},
		{"equal as decimals", invOf(sp("100"), sp("7.5"), sp("107.5")), with(func(l *lineAmounts) {
			l.UnitPrice, l.LineTotal, l.LineTax, l.Quantity = sp("100"), sp("100"), sp("7.5"), sp("1.00")
		}), false},
		{"description differs only by spaces", invOf(sp("100.00"), sp("7.50"), sp("107.50")), with(func(l *lineAmounts) { l.Description = sp("  Work ") }), false},
		{"Subtotal differs", invOf(sp("100.01"), sp("7.50"), sp("107.50")), same(), true},
		{"Total differs", invOf(sp("100.00"), sp("7.50"), sp("107.51")), same(), true},
		{"VAT differs", invOf(sp("100.00"), sp("7.51"), sp("107.50")), same(), true},
		{"an extra line", invOf(sp("100.00"), sp("7.50"), sp("107.50")), append(same(), same()[0]), true},
		{"a missing line", invOf(sp("100.00"), sp("7.50"), sp("107.50")), nil, true},
		{"same totals, another description", invOf(sp("100.00"), sp("7.50"), sp("107.50")), with(func(l *lineAmounts) { l.Description = sp("Other") }), true},
		{"a line unit price differs", invOf(sp("100.00"), sp("7.50"), sp("107.50")), with(func(l *lineAmounts) { l.UnitPrice = sp("100.01") }), true},
		{"a line tax differs", invOf(sp("100.00"), sp("7.50"), sp("107.50")), with(func(l *lineAmounts) { l.LineTax = sp("7.51") }), true},
		{"NULL total against a value", invOf(sp("100.00"), sp("7.50"), nil), same(), true},
		{"NULL line total against a value", invOf(sp("100.00"), sp("7.50"), sp("107.50")), with(func(l *lineAmounts) { l.LineTotal = nil }), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Differs(in, c.inv, c.lines); got != c.want {
				t.Errorf("Differs = %v, want %v", got, c.want)
			}
		})
	}
}
