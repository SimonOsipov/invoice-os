// Per-rule tests for rule-set v5. The rule list is read from the migration file re-applied in a
// rolled-back tx (v5ReappliedTx), so a rule added to the file without a case here fails.
package validation

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// v5RuleCase is one failing invoice for one v5 rule. also names the companion violations the
// mutation causes; path is the Path of the rule's own violation.
type v5RuleCase struct {
	build func() Payload
	path  string
	also  []string
}

func v5Mutated(mut func(inv map[string]any)) func() Payload {
	return func() Payload {
		p := v5B2BPayload()
		mut(invoiceOf(p))
		return p
	}
}

func v5LineOf(inv map[string]any, i int) map[string]any {
	return inv["line_items"].([]any)[i].(map[string]any)
}

func v5PartyOf(inv map[string]any, who string) map[string]any { return inv[who].(map[string]any) }

// v5RuleCases returns one failing invoice per v5 rule that is not a carried v4 rule.
func v5RuleCases() map[string]v5RuleCase {
	cases := map[string]v5RuleCase{}
	set := func(key, path string, mut func(inv map[string]any), also ...string) {
		cases[key] = v5RuleCase{build: v5Mutated(mut), path: path, also: also}
	}

	set("buyer-tin-required", "buyer.tin", func(inv map[string]any) { delete(v5PartyOf(inv, "buyer"), "tin") })
	set("supplier-tin-format", "supplier.tin", func(inv map[string]any) { v5PartyOf(inv, "supplier")["tin"] = "BADTIN" })
	set("buyer-tin-format", "buyer.tin", func(inv map[string]any) { v5PartyOf(inv, "buyer")["tin"] = "BADTIN" })
	set("currency-allowed", "currency", func(inv map[string]any) { inv["currency"] = "XXX" })
	cases["vat-standard-rate"] = v5RuleCase{path: "tax_subtotals[1]", build: func() Payload {
		lines := v5StandardLines()
		lines[0]["line_tax"] = 70.0 // vat follows the lines, so only the category rate is wrong
		return v5Invoice("B2B", lines)
	}}

	set("invoice-kind-required", "invoice_kind", func(inv map[string]any) { delete(inv, "invoice_kind") })
	set("invoice-kind-allowed", "invoice_kind", func(inv map[string]any) { inv["invoice_kind"] = "G2G" })
	set("tax-currency-required", "tax_currency_code", func(inv map[string]any) { delete(inv, "tax_currency_code") })
	set("tax-currency-allowed", "tax_currency_code", func(inv map[string]any) { inv["tax_currency_code"] = "XXX" })
	set("vat-equals-tax-subtotals", "vat", func(inv map[string]any) { inv["vat"] = 80.0 })
	cases["vat-standard-rate-uncategorised"] = v5RuleCase{path: "vat", also: []string{"line-tax-category-required", "line-tax-percent-required"},
		build: func() Payload {
			line := v5Line("1", "STANDARD_VAT", 1000, 7.5, 75)
			delete(line, "tax_category")
			delete(line, "tax_percent")
			p := v5Invoice("B2B", []map[string]any{line})
			invoiceOf(p)["vat"] = 50.0
			return p
		}}

	for _, who := range []string{"supplier", "buyer"} {
		for _, f := range []string{"email", "street", "city", "postal_zone", "lga", "state", "country"} {
			set(who+"-"+strings.ReplaceAll(f, "_", "-")+"-required", who+"."+f,
				func(inv map[string]any) { delete(v5PartyOf(inv, who), f) })
		}
		for _, f := range []string{"lga", "state", "country"} {
			set(who+"-"+f+"-allowed", who+"."+f, func(inv map[string]any) { v5PartyOf(inv, who)[f] = "ZZ" })
		}
		set(who+"-name-length", who+".name", func(inv map[string]any) { v5PartyOf(inv, who)["name"] = strings.Repeat("a", 256) })
		set(who+"-email-length", who+".email", func(inv map[string]any) { v5PartyOf(inv, who)["email"] = strings.Repeat("a", 101) })
	}
	set("buyer-name-required", "buyer.name", func(inv map[string]any) { delete(v5PartyOf(inv, "buyer"), "name") })

	line := func(key, path string, mut func(l map[string]any), also ...string) {
		set(key, path, func(inv map[string]any) { mut(v5LineOf(inv, 0)) }, also...)
	}
	drop := func(key, field string, also ...string) {
		line(key, "line_items[1]", func(l map[string]any) { delete(l, field) }, also...)
	}
	drop("line-quantity-required", "quantity")
	drop("line-amount-required", "line_total")
	drop("line-description-required", "description")
	drop("line-item-id-required", "sellers_item_identification")
	drop("line-price-required", "unit_price", "line-items-sum-subtotal") // the sum rule reads unit_price
	drop("line-base-quantity-required", "base_quantity")
	drop("line-price-unit-required", "price_unit")
	drop("line-classification-required", "hsn_code")
	drop("line-tax-category-required", "tax_category")
	drop("line-tax-percent-required", "tax_percent")
	drop("line-tax-required", "line_tax")
	line("line-price-unit-allowed", "line_items[1].price_unit", func(l map[string]any) { l["price_unit"] = "ZZ" })
	line("line-hs-code-allowed", "line_items[1].hsn_code", func(l map[string]any) { l["hsn_code"] = "0000.00" })
	line("line-service-code-allowed", "line_items[1].isic_code", func(l map[string]any) { l["isic_code"] = "9999" })
	line("line-tax-category-allowed", "line_items[1].tax_category", func(l map[string]any) { l["tax_category"] = "BOGUS" })
	line("tax-percent-matches-category", "line_items[1]", func(l map[string]any) { l["tax_percent"] = 5.0 })
	return cases
}

// v5ChangedKeys are the keys of v5 whose row is not a verbatim v4 row.
func v5ChangedKeys(t *testing.T, v4, v5 map[string]ruleRow) []string {
	t.Helper()
	var keys []string
	for key, row := range v5 {
		if old, ok := v4[key]; ok {
			old.Enabled = true
			if reflect.DeepEqual(row, old) {
				continue
			}
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestV5Rules_EachRuleFailsOnItsInvoice(t *testing.T) {
	super, _ := dbTestPools(t)
	seedV5Lists(t, super)
	tx := v5ReappliedTx(t, super)
	rs := v5LoadOn(t, tx, v5Start, 5)
	keys := v5ChangedKeys(t, ruleRowsByKey(t, tx, 4), ruleRowsByKey(t, tx, 5))
	if len(keys) != 52 {
		t.Fatalf("v5 holds %d fixed and new rules, want 52", len(keys))
	}
	wantKeys(t, "compliant B2B", evalV5(t, rs, v5B2BPayload()))

	cases := v5RuleCases()
	for _, key := range keys {
		c, ok := cases[key]
		if !ok {
			t.Errorf("v5 rule %s has no failing-invoice case", key)
			continue
		}
		res := evalV5(t, rs, c.build())
		want := append([]string{key}, c.also...)
		slices.Sort(want)
		if got := violationKeys(res); !slices.Equal(got, want) {
			t.Errorf("%s: violations = %s, want exactly %v", key, showViolations(res), want)
			continue
		}
		if v := violationOf(t, res, key); v.Path != c.path {
			t.Errorf("%s: Path = %q, want %q", key, v.Path, c.path)
		}
	}
	for key := range cases {
		if !slices.Contains(keys, key) {
			t.Errorf("case %s names no fixed or new v5 rule", key)
		}
	}
}

// D10: matching is exact, on the payload side and on the listed side.
func TestV5Rules_ListMatchIsExact(t *testing.T) {
	super, _ := dbTestPools(t)
	seedV5Lists(t, super)
	tx := v5ReappliedTx(t, super)
	ctx := context.Background()
	rs := v5LoadOn(t, tx, v5Start, 5)

	p := v5B2BPayload()
	invoiceOf(p)["currency"] = "NGN "
	res := evalV5(t, rs, p)
	wantKeys(t, "currency with a trailing space", res, "currency-allowed")
	if v := violationOf(t, res, "currency-allowed"); v.Actual == nil || *v.Actual != "NGN " {
		t.Errorf("currency-allowed Actual = %s, want %q", ptrStr(v.Actual), "NGN ")
	}

	if _, err := tx.Exec(ctx, `DELETE FROM nrs_codes WHERE list = 'invoice-quantity-codes'`); err != nil {
		t.Fatalf("delete invoice-quantity-codes: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO nrs_codes (list, code, entries) VALUES ('invoice-quantity-codes', 'EA ', '[{}]')`); err != nil {
		t.Fatalf("insert the only listed code: %v", err)
	}
	rs = v5LoadOn(t, tx, v5Start, 5)
	res = evalV5(t, rs, v5Invoice("B2B", []map[string]any{v5Line("1", "STANDARD_VAT", 1000, 7.5, 75)}))
	wantKeys(t, "price_unit EA against the only code \"EA \"", res, "line-price-unit-allowed")
}

func TestV5Rules_NoRuleReadsVatExemptions(t *testing.T) {
	super, _ := dbTestPools(t)
	tx := v5ReappliedTx(t, super)
	var n int
	if err := tx.QueryRow(context.Background(),
		`SELECT count(*) FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		  WHERE v.version = 5 AND r.params->>'list' = 'vat-exemptions'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d v5 rules name the vat-exemptions list, want 0", n)
	}
}

// A blank line text counts as missing under the same definition requiredEval applies to a header field.
func TestV5Rules_LineBlankMatchesRequiredBlank(t *testing.T) {
	rs := v5RuleSet(t)
	for _, c := range []struct {
		name, text string
		blank      bool
	}{
		{"empty", "", true}, {"space", " ", true}, {"tab", "\t", true}, {"vertical tab", "\v", true},
		{"NEL", "\u0085", true}, {"no-break space", " ", true}, {"em space", " ", true}, {"text", "a", false},
	} {
		p := v5B2BPayload()
		inv := invoiceOf(p)
		v5LineOf(inv, 0)["description"] = c.text
		v5PartyOf(inv, "supplier")["email"] = c.text
		res := evalV5(t, rs, p)
		line, header := hasViolation(res, "line-description-required"), hasViolation(res, "supplier-email-required")
		if line != c.blank || header != c.blank {
			t.Errorf("%s: line-description-required=%t supplier-email-required=%t, want both %t", c.name, line, header, c.blank)
		}
	}
}
