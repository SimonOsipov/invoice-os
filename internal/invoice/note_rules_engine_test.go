package invoice_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/submission"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

var amountRuleKeys = []string{"subtotal-non-negative", "vat-non-negative", "total-non-negative", "line-cost-non-negative"}

// noteV6Invoice is the v5-compliant invoice of TestPayloadEngine_V5CompliantInvoiceThroughTheMapper.
func noteV6Invoice() invoice.Invoice {
	date := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	line := func(no int, category, qty, price, total, percent, tax string) invoice.LineItem {
		return invoice.LineItem{
			ID: uuid.NewString(), LineNo: no, Description: ptr("Laptop"), Quantity: ptr(qty), UnitPrice: ptr(price),
			LineTotal: ptr(total), LineTax: ptr(tax), TaxCategory: ptr(category), TaxPercent: ptr(percent),
			HSNCode: ptr("8471.30"), ProductCategory: ptr("Computers"), SellersItemIdentification: ptr("SKU-1"),
			PriceUnit: ptr("EA"), BaseQuantity: ptr("1"),
		}
	}
	return invoice.Invoice{
		InvoiceNumber: "CN-2027-000123", IssueDate: &date, Currency: ptr("NGN"),
		InvoiceKind: ptr("B2B"), TaxCurrencyCode: ptr("NGN"),
		SupplierTIN: ptr("12345678-0001"), SupplierName: ptr("Acme Nigeria Ltd"), SupplierEmail: ptr("accounts@acme.ng"),
		SupplierStreet: ptr("1 Marina Road"), SupplierCity: ptr("Lagos"), SupplierPostalZone: ptr("100001"),
		SupplierLGA: ptr("NG-LA-AGE"), SupplierState: ptr("NG-LA"), SupplierCountry: ptr("NG"),
		BuyerTIN: ptr("87654321-0002"), BuyerName: ptr("Beta Ltd"), BuyerEmail: ptr("ap@beta.ng"),
		BuyerStreet: ptr("2 Side Street"), BuyerCity: ptr("Abuja"), BuyerPostalZone: ptr("900001"),
		BuyerLGA: ptr("NG-FC-AML"), BuyerState: ptr("NG-FC"), BuyerCountry: ptr("NG"),
		Subtotal: ptr("150.00"), VAT: ptr("7.50"), Total: ptr("157.50"),
		LineItems: []invoice.LineItem{
			line(1, "STANDARD_VAT", "1", "100.00", "100.00", "7.50", "7.50"),
			line(2, "ZERO_VAT", "1", "50.00", "50.00", "0", "0.00"),
		},
	}
}

// noteCase is a credit note and the rule set that judges it.
type noteCase struct {
	name string
	rs   validation.RuleSet
	inv  invoice.Invoice
}

// noteCases returns a credit note under v4 (2026-07-01) and under v6 (2027-01-15).
func noteCases(t *testing.T) []noteCase {
	t.Helper()
	pool := rulesAppPool(t)
	seedV5MapperLists(t)
	store := validation.NewStore(pool)

	v4 := ruleSetOn(t, store, "2026-07-01")
	v6 := ruleSetOn(t, store, "2027-01-15")
	if v4.Version != 4 || v6.Version != 6 {
		t.Fatalf("rule sets on 2026-07-01 and 2027-01-15 = v%d, v%d, want v4, v6", v4.Version, v6.Version)
	}
	note := func(inv invoice.Invoice) invoice.Invoice {
		d := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
		inv.DocumentType = submission.CreditNote
		inv.Original = &submission.BillingReference{IRN: ptr("IRN-ORIG-1"), IssueDate: &d}
		inv.OriginalTotal, inv.CreditedBefore = ptr("1000.00"), ptr("0.00")
		return inv
	}
	return []noteCase{
		{"v4", v4, note(pay18NRSInvoice())},
		{"v6", v6, note(noteV6Invoice())},
	}
}

func TestNoteRules_ValidCreditNotePassesTheAmountRules(t *testing.T) {
	for _, c := range noteCases(t) {
		t.Run(c.name, func(t *testing.T) {
			res, err := validation.NewDefaultEngine().Evaluate(rooted(t, c.inv), c.rs)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			for _, v := range res.Violations {
				for _, k := range amountRuleKeys {
					if v.RuleKey == k {
						t.Errorf("%s fired on a valid credit note: %v", k, v)
					}
				}
			}
			if len(res.Violations) != 0 {
				t.Errorf("violations = %v, want zero", res.Violations)
			}
		})
	}
}

func TestNoteRules_NegativeAmountsFireUnchanged(t *testing.T) {
	cases := []struct {
		key    string
		mutate func(*invoice.Invoice)
	}{
		{"subtotal-non-negative", func(i *invoice.Invoice) { i.Subtotal = ptr("-150.00") }},
		{"vat-non-negative", func(i *invoice.Invoice) { i.VAT = ptr("-7.50") }},
		{"total-non-negative", func(i *invoice.Invoice) { i.Total = ptr("-157.50") }},
		{"line-cost-non-negative", func(i *invoice.Invoice) { i.LineItems[0].UnitPrice = ptr("-100.00") }},
	}
	for _, nc := range noteCases(t) {
		for _, c := range cases {
			t.Run(nc.name+"/"+c.key, func(t *testing.T) {
				inv := nc.inv
				inv.LineItems = append([]invoice.LineItem(nil), inv.LineItems...)
				c.mutate(&inv)
				res, err := validation.NewDefaultEngine().Evaluate(rooted(t, inv), nc.rs)
				if err != nil {
					t.Fatalf("Evaluate: %v", err)
				}
				for _, v := range res.Violations {
					if v.RuleKey == c.key {
						if v.Severity != "error" {
							t.Errorf("%s severity = %q, want error", c.key, v.Severity)
						}
						return
					}
				}
				t.Errorf("%s did not fire: %v", c.key, res.Violations)
			})
		}
	}
}
