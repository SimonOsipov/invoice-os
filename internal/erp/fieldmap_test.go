package erp

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
)

func docOf(t *testing.T, body string) Document {
	t.Helper()
	return Document{Ref: DocRef{SourceDocID: "d", SourceRevision: "1"}, ContentType: jsonType, Body: []byte(body)}
}

func TestTargets_AreTheImportKeysThenTheThreeERPKeys(t *testing.T) {
	ts := Targets()
	keys := invoicefields.ImportKeys()
	if len(ts) != len(keys)+3 || len(keys) != 36 {
		t.Fatalf("targets = %d, import keys = %d", len(ts), len(keys))
	}
	byKey := map[string]invoicefields.Field{}
	for _, f := range invoicefields.All {
		byKey[f.ImportKey()] = f
	}
	for i, k := range keys {
		f := byKey[k]
		if ts[i].Key != k || ts[i].Label != f.Label || ts[i].Line != f.Line || ts[i].Type != f.Type {
			t.Errorf("target %d = %+v, want key %s of %+v", i, ts[i], k, f)
		}
	}
	for i, w := range []struct {
		key  string
		line bool
	}{{TargetCustomerID, false}, {TargetTaxCode, true}, {TargetItemID, true}} {
		if got := ts[len(keys)+i]; got.Key != w.key || got.Line != w.line {
			t.Errorf("erp target %d = %+v, want %+v", i, got, w)
		}
	}
}

// setFields names the CreateInput and line fields that are set.
func setFields(in invoice.CreateInput) (header, line []string) {
	v := reflect.ValueOf(in)
	for i := range v.NumField() {
		if v.Type().Field(i).Name != "LineItems" && !v.Field(i).IsZero() {
			header = append(header, v.Type().Field(i).Name)
		}
	}
	if len(in.LineItems) == 1 {
		lv := reflect.ValueOf(in.LineItems[0])
		for i := range lv.NumField() {
			if !lv.Field(i).IsZero() {
				line = append(line, lv.Type().Field(i).Name)
			}
		}
	}
	return
}

func TestApply_EachImportKeySetsItsOwnField(t *testing.T) {
	seen := map[string]bool{}
	for _, tg := range Targets()[:36] {
		val := "x-" + tg.Key
		switch tg.Type {
		case invoicefields.Date:
			val = "2026-03-04"
		case invoicefields.Money, invoicefields.Quantity, invoicefields.Percent:
			val = "12.50"
		}
		path, body := "v", fmt.Sprintf(`{"v":%q}`, val)
		if tg.Line {
			path, body = "lines[].v", fmt.Sprintf(`{"lines":[{"v":%q}]}`, val)
		}
		got, err := Apply(Mapping{tg.Key: path}, docOf(t, body), nil)
		if err != nil {
			t.Fatalf("%s: %v", tg.Key, err)
		}
		h, l := setFields(got.In)
		fields := append(h, l...)
		if len(fields) != 1 || (tg.Line && len(l) != 1) {
			t.Errorf("%s set %v, want exactly one", tg.Key, fields)
			continue
		}
		if seen[fields[0]] {
			t.Errorf("%s shares field %s", tg.Key, fields[0])
		}
		seen[fields[0]] = true
	}
	if len(seen) != 36 {
		t.Errorf("distinct fields = %d, want 36", len(seen))
	}
}

func validMapping() Mapping {
	return Mapping{
		"invoice_number": "number", "line_description": "lines[].d", "line_quantity": "lines[].q",
		TargetTaxCode: "lines[].tax", "buyer_tin": "@party.tin",
	}
}

func TestValidateMapping_AcceptsTheGrammar(t *testing.T) {
	m := validMapping()
	m[TargetTaxCode] = "Line[].SalesItemLineDetail.TaxCodeRef.value"
	for k := range m {
		if tg, _ := targetOf(k); tg.Line {
			m[k] = "Line[]." + strings.ReplaceAll(k, "_", "-")
		}
	}
	m["issue_date"] = strings.TrimSuffix(strings.Repeat("a.", 16), ".")
	m["due_date"] = strings.Repeat("a", 64) + "." + strings.Repeat("b", 64) + "." + strings.Repeat("c", 64) + "." + strings.Repeat("d", 61)
	if err := ValidateMapping(m); err != nil {
		t.Fatalf("ValidateMapping: %v", err)
	}
}

func TestValidateMapping_RefusesEachBreach(t *testing.T) {
	for _, c := range []struct {
		name, target, path, reason string
	}{
		{"unknown target", "nope", "x", reasonUnknown},
		{"empty segment", "issue_date", "a..b", reasonPath},
		{"trailing dot", "issue_date", "a.", reasonPath},
		{"space", "issue_date", "a b", reasonPath},
		{"index", "issue_date", "a[0]", reasonPath},
		{"17 segments", "issue_date", strings.TrimSuffix(strings.Repeat("a.", 17), "."), reasonPath},
		{"257 characters", "issue_date", strings.Repeat("a", 257), reasonPath},
		{"header reads []", "issue_date", "lines[].date", reasonHeaderArr},
		{"line reads none", "line_quantity", "qty", reasonLineArr},
		{"line reads two", "line_quantity", "a[].b[].c", reasonLineArr},
		{"two arrays", "line_quantity", "rows[].x", reasonOneArray},
		{"party on a line", "line_description", "@party.name", reasonParty},
		{"party customer", TargetCustomerID, "@party.id", reasonParty},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := validMapping()
			m[c.target] = c.path
			err := ValidateMapping(m)
			if !errors.Is(err, ErrInvalidMapping) {
				t.Fatalf("err = %v, want ErrInvalidMapping", err)
			}
			if want := c.target + ": " + c.reason; err.Error() != want {
				t.Errorf("message = %q, want %q", err, want)
			}
		})
	}
	m := validMapping()
	delete(m, "invoice_number")
	if err := ValidateMapping(m); !errors.Is(err, ErrInvalidMapping) || err.Error() != "invoice_number: "+reasonRequired {
		t.Errorf("unmapped invoice_number: %v", err)
	}
}

func TestApply_ResolvesHeaderPartyAndLines(t *testing.T) {
	m := Mapping{
		"invoice_number": "meta.ref", "issue_date": "date", "buyer_tin": "@party.tin", TargetCustomerID: "cust",
		"line_description": "items[].d", "line_unit_price": "items[].price", TargetTaxCode: "items[].tax", TargetItemID: "items[].sku",
	}
	body := `{"meta":{"ref":"N-1"},"date":"2026-01-05","cust":"c1","items":[
		{"d":"A","price":1.5,"tax":"T1","sku":"S1"},{"d":"B","price":"2.25","tax":"T2","sku":"S2"},{"d":"C","price":3,"tax":"T3","sku":"S3"}]}`
	got, err := Apply(m, docOf(t, body), &Party{Body: []byte(`{"tin":"1234567897"}`)})
	if err != nil {
		t.Fatal(err)
	}
	in := got.In
	if in.InvoiceNumber != "N-1" || in.IssueDate == nil || in.IssueDate.Format("2006-01-02") != "2026-01-05" || got.CustomerID != "c1" {
		t.Errorf("header = %+v %q", in, got.CustomerID)
	}
	exact(t, "BuyerTIN", in.BuyerTIN, "1234567897")
	if len(in.LineItems) != 3 {
		t.Fatalf("lines = %d", len(in.LineItems))
	}
	for i, w := range [][4]string{{"A", "1.5", "T1", "S1"}, {"B", "2.25", "T2", "S2"}, {"C", "3", "T3", "S3"}} {
		exact(t, "Description", in.LineItems[i].Description, w[0])
		exact(t, "UnitPrice", in.LineItems[i].UnitPrice, w[1])
		if got.TaxCodes[i] != w[2] || got.Items[i] != w[3] {
			t.Errorf("line %d keys = %q %q", i, got.TaxCodes[i], got.Items[i])
		}
	}
}

func TestApply_AbsentNullAndBlankAreNil(t *testing.T) {
	m := Mapping{
		"invoice_number": "number", "currency": "missing", "subtotal": "nothing", "vat": "blank",
		"buyer_tin": "@party.tin", "buyer_name": "@party.name",
	}
	got, err := Apply(m, docOf(t, `{"number":"N-1","nothing":null,"blank":"  "}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	in := got.In
	if in.Currency != nil || in.Subtotal != nil || in.VAT != nil || in.BuyerTIN != nil || in.BuyerName != nil || len(in.LineItems) != 0 {
		t.Errorf("want nil fields and no lines: %+v", in)
	}
	lm := Mapping{"invoice_number": "number", "line_quantity": "lines[].q"}
	got, err = Apply(lm, docOf(t, `{"number":"N-1"}`), nil)
	if err != nil || len(got.In.LineItems) != 0 {
		t.Errorf("no lines array: %v, %d lines", err, len(got.In.LineItems))
	}
}

func TestApply_RefusesEachUnmappableValue(t *testing.T) {
	for _, c := range []struct{ name, target, path, body, bad string }{
		{"date", "issue_date", "d", `{"number":"N","d":"05/01/2026"}`, "05/01/2026"},
		{"quantity", "line_quantity", "lines[].q", `{"number":"N","lines":[{"q":"1,5"}]}`, "1,5"},
		{"money", "subtotal", "d", `{"number":"N","d":"abc"}`, "abc"},
		{"bool", "currency", "d", `{"number":"N","d":true}`, "true"},
		{"object", "currency", "d", `{"number":"N","d":{"zz":"secret"}}`, "secret"},
		{"array", "currency", "d", `{"number":"N","d":["secret"]}`, "secret"},
		{"blank number", "invoice_number", "number", `{"number":"  "}`, ""},
		{"not json", "currency", "d", `{`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := Mapping{"invoice_number": "number", c.target: c.path}
			_, err := Apply(m, docOf(t, c.body), nil)
			if !errors.Is(err, ErrUnmappable) {
				t.Fatalf("err = %v, want ErrUnmappable", err)
			}
			if c.name != "not json" && !strings.Contains(err.Error(), c.target) {
				t.Errorf("error %q does not name %q", err, c.target)
			}
			if c.bad != "" && strings.Contains(err.Error(), c.bad) {
				t.Errorf("error %q repeats %q", err, c.bad)
			}
		})
	}
}

func TestApply_NumbersKeepTheirLiteralText(t *testing.T) {
	m := Mapping{"invoice_number": "number", "line_unit_price": "lines[].p"}
	got, err := Apply(m, docOf(t, `{"number":"N","lines":[{"p":100000.10}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	exact(t, "UnitPrice", got.In.LineItems[0].UnitPrice, "100000.10")
}

func TestCustomerIDOf_ReadsTheMappedCustomer(t *testing.T) {
	m := Mapping{TargetCustomerID: "customer_id"}
	if got, err := CustomerIDOf(m, docOf(t, `{"customer_id":" c-1 "}`)); err != nil || got != "c-1" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := CustomerIDOf(Mapping{}, docOf(t, `{"customer_id":"c-1"}`)); err != nil || got != "" {
		t.Errorf("unmapped: %q, %v", got, err)
	}
	if _, err := CustomerIDOf(m, docOf(t, `{"customer_id":{"a":1}}`)); !errors.Is(err, ErrUnmappable) {
		t.Errorf("object leaf: %v", err)
	}
}

func TestMapping_MergeOverridesPerTarget(t *testing.T) {
	base, over := Mapping{"a": "x", "b": "y"}, Mapping{"b": "z", "c": "w"}
	got := base.Merge(over)
	if !reflect.DeepEqual(got, Mapping{"a": "x", "b": "z", "c": "w"}) {
		t.Errorf("merged = %v", got)
	}
	if !reflect.DeepEqual(base, Mapping{"a": "x", "b": "y"}) || !reflect.DeepEqual(over, Mapping{"b": "z", "c": "w"}) {
		t.Errorf("inputs changed: %v %v", base, over)
	}
}
