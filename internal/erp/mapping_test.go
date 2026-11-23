package erp

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
)

func sp(s string) *string { return &s }

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

func TestDefaultMapping_HashesTheSeededDocumentsAsTheFixedMappingDid(t *testing.T) {
	f := testFake(t)
	m := f.DefaultMapping()
	for _, c := range []struct{ scope, doc, cust, want string }{
		{"fake-co-1", "fake-co-1-inv-1", "fake-co-1-cust-1", "010683e9ae9a0cdd2c108be2156e4b8ca7cf0c41e83b96ebd5f3a1665b830b08"},
		{"fake-co-2", "fake-co-2-inv-4", "fake-co-2-cust-2", "8aa244547bbfb026cea2eb611b8de483984b20b8af92932d029ff01eca52bcce"},
		{"fake-co-3", "fake-co-3-inv-2", "fake-co-3-cust-1", "bad2f4323457a1474329e645adb51ce89b042f6c2a32df03e033eb8468866598"},
	} {
		doc, err := f.Fetch(ctx, cr, DocRef{SourceDocID: c.doc, Scope: c.scope})
		if err != nil {
			t.Fatal(err)
		}
		party, err := f.FetchParty(ctx, cr, c.scope, c.cust)
		if err != nil {
			t.Fatal(err)
		}
		mapped, err := Apply(m, doc, &party)
		if err != nil {
			t.Fatalf("%s: Apply: %v", c.doc, err)
		}
		if got := ContentHash(mapped.In); got != c.want {
			t.Errorf("%s: hash = %s, want %s", c.doc, got, c.want)
		}
	}
	if HashVersion != 1 {
		t.Errorf("HashVersion = %d, want 1", HashVersion)
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
	// A change below the second place is a change: the hash keeps each value's exact decimals.
	c, d := hashBase(), hashBase()
	c.LineItems[0].Quantity, d.LineItems[0].Quantity = sp("33.335"), sp("33.336")
	if ContentHash(c) == ContentHash(d) {
		t.Error("33.335 and 33.336 must hash apart")
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
		{"a line quantity differs", invOf(sp("100.00"), sp("7.50"), sp("107.50")), with(func(l *lineAmounts) { l.Quantity = sp("2") }), true},
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

// Fingerprint drops targets ContentHash never reads, so the two must name the same set.
func TestHashedTargets_MatchWhatContentHashReads(t *testing.T) {
	targets := Targets()
	if len(targets) == 0 {
		t.Fatal("no targets")
	}
	m := Mapping{}
	for _, tg := range targets {
		if tg.Line {
			m[tg.Key] = "lines[].f_" + tg.Key
		} else {
			m[tg.Key] = "f_" + tg.Key
		}
	}
	value := func(tg Target, alt bool) string {
		switch {
		case tg.Type == invoicefields.Date && alt:
			return "2026-01-06"
		case tg.Type == invoicefields.Date:
			return "2026-01-05"
		case alt:
			return "11"
		}
		return "10"
	}
	hashWith := func(changed string) string {
		head, line := map[string]any{}, map[string]any{}
		for _, tg := range targets {
			v := value(tg, tg.Key == changed)
			if tg.Line {
				line["f_"+tg.Key] = v
			} else {
				head["f_"+tg.Key] = v
			}
		}
		head["lines"] = []any{line}
		body, err := json.Marshal(head)
		if err != nil {
			t.Fatal(err)
		}
		mapped, err := Apply(m, Document{Body: body}, nil)
		if err != nil {
			t.Fatalf("Apply changing %q: %v", changed, err)
		}
		if len(mapped.In.LineItems) != 1 {
			t.Fatalf("want one line, got %d", len(mapped.In.LineItems))
		}
		return ContentHash(mapped.In)
	}
	base := hashWith("")
	for _, tg := range targets {
		moved := hashWith(tg.Key) != base
		if want := hashedTargets[tg.Key]; moved != want {
			t.Errorf("%s: ContentHash moves = %v, hashedTargets = %v", tg.Key, moved, want)
		}
	}
	for k := range hashedTargets {
		if _, ok := targetOf(k); !ok {
			t.Errorf("hashedTargets names %q, which is not a target", k)
		}
	}
}
