package readinesscheck

import (
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
	"github.com/SimonOsipov/invoice-os/internal/validation"
)

func viol(key, sev, path string) validation.Violation {
	return validation.Violation{RuleKey: key, Severity: validation.Severity(sev), Path: path, Message: key + " message"}
}

func TestMailFor_AWarningIsNotAFailure(t *testing.T) {
	res := publiccheck.Result{InvoicesTotal: 1, Invoices: []publiccheck.Invoice{{
		Number: "A", Payload: map[string]any{}, Failures: []validation.Violation{viol("w", "warning", "buyer.email")},
	}}}
	inv := MailFor(res, 2).Invoices[0]
	if len(inv.Failures) != 0 || len(inv.Warnings) != 1 || !inv.Passes {
		t.Errorf("invoice = %+v, want no failures, one warning, Passes", inv)
	}
	if inv.Warnings[0].Label != "Buyer email" {
		t.Errorf("warning label = %q, want Buyer email", inv.Warnings[0].Label)
	}
}

func TestMailFor_CarriesAmountsAndBuyerFromThePayload(t *testing.T) {
	res := publiccheck.Result{InvoicesTotal: 1, Invoices: []publiccheck.Invoice{{Number: "A", Payload: map[string]any{
		"total": 1075.5, "currency": "NGN", "issue_date": "2026-06-01",
		"buyer": map[string]any{"tin": "12345678-0001", "name": "Beta Ltd"},
	}}}}
	inv := MailFor(res, 1).Invoices[0]
	if inv.Total != "1075.50" || inv.Currency != "NGN" || inv.IssueDate != "2026-06-01" || inv.Subtotal != "" || inv.VAT != "" {
		t.Errorf("amounts = %+v", inv)
	}
	want := []accountmail.MailField{{Label: "Buyer name", Value: "Beta Ltd"}, {Label: "Buyer TIN", Value: "12345678-0001"}}
	if !slices.Equal(inv.Buyer, want) {
		t.Errorf("Buyer = %v, want %v", inv.Buyer, want)
	}
}

func TestMailFor_CutsLongFileText(t *testing.T) {
	long, exact := strings.Repeat("é", 5000), strings.Repeat("é", 120)
	for _, c := range []struct{ in, want string }{{long, strings.Repeat("é", 119) + "…"}, {exact, exact}} {
		res := publiccheck.Result{Invoices: []publiccheck.Invoice{{Number: "A", Payload: map[string]any{"buyer": map[string]any{"name": c.in}}}}}
		if got := MailFor(res, 0).Invoices[0].Buyer[0].Value; got != c.want {
			t.Errorf("%d runes in: got %d runes, want %d", len([]rune(c.in)), len([]rune(got)), len([]rune(c.want)))
		}
	}
}

func TestMailFor_NotCheckedFollowsEachInvoice(t *testing.T) {
	res := publiccheck.Result{InvoicesTotal: 2, NotChecked: []string{"a", "b"}, Invoices: []publiccheck.Invoice{
		{Number: "1", Payload: map[string]any{}}, {Number: "2", Payload: map[string]any{}, NotChecked: []string{"a", "b"}},
	}}
	m := MailFor(res, 0)
	if m.Invoices[0].NotChecked != nil || !slices.Equal(m.Invoices[1].NotChecked, []string{"a", "b"}) {
		t.Errorf("NotChecked = %v and %v, want nil and [a b]", m.Invoices[0].NotChecked, m.Invoices[1].NotChecked)
	}
}

func TestMailFor_TotalIsInvoicesNotRows(t *testing.T) {
	m := MailFor(publiccheck.Result{InvoicesTotal: 3, RowsTotal: 7}, 0)
	if m.Total != 3 {
		t.Errorf("Total = %d, want 3", m.Total)
	}
}

func TestMailFor_AProblemInvoiceShowsItsProblemOnly(t *testing.T) {
	res := publiccheck.Result{InvoicesTotal: 1, Invoices: []publiccheck.Invoice{{Number: "A", Failures: []validation.Violation{
		{Severity: "error", Path: "issue_date", Message: "not a date"},
	}}}}
	inv := MailFor(res, 0).Invoices[0]
	if len(inv.Failures) != 1 || inv.Failures[0].Label != "Issue date" || inv.Failures[0].Value != "not a date" {
		t.Errorf("Failures = %v, want one on Issue date", inv.Failures)
	}
	if inv.Passes || inv.NotChecked != nil || inv.Buyer != nil || inv.Total != "" || inv.IssueDate != "" {
		t.Errorf("invoice = %+v, want only the problem", inv)
	}
}

func TestFailedRuleIDs_DistinctSortedAndSkipsReadProblems(t *testing.T) {
	res := publiccheck.Result{Invoices: []publiccheck.Invoice{
		{Failures: []validation.Violation{viol("b", "error", "")}},
		{Failures: []validation.Violation{viol("a", "error", "")}},
		{Failures: []validation.Violation{viol("b", "error", ""), viol("", "error", "issue_date")}},
	}}
	if got := FailedRuleIDs(res); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("FailedRuleIDs = %v, want [a b]", got)
	}
}

func TestFailedRuleIDs_CountsErrorsOnly(t *testing.T) {
	res := publiccheck.Result{Invoices: []publiccheck.Invoice{{Failures: []validation.Violation{
		viol("a", "error", ""), viol("b", "warning", ""), viol("c", "info", ""),
	}}}}
	if got := FailedRuleIDs(res); !slices.Equal(got, []string{"a"}) {
		t.Errorf("FailedRuleIDs = %v, want [a]", got)
	}
}

func TestFieldLabel_ResolvesEachPathShape(t *testing.T) {
	for _, c := range []struct{ path, field, want string }{
		{"supplier.tin", "", "Supplier TIN"},
		{"buyer.postal_zone", "", "Buyer postal zone"},
		{"issue_date", "", "Issue date"},
		{"line_items[1]", "quantity", "Quantity"},
		{"tax_subtotals", "", "VAT breakdown"},
		{"billing_reference.irn", "", "Original invoice IRN"},
		{"line_items", "", "Line items"},
		{"x.y", "", "x.y"},
	} {
		if got := fieldLabel(c.path, c.field); got != c.want {
			t.Errorf("fieldLabel(%q, %q) = %q, want %q", c.path, c.field, got, c.want)
		}
	}
}

func TestMailFor_ARuleWithNoTargetIsLabelledInvoice(t *testing.T) {
	res := publiccheck.Result{Invoices: []publiccheck.Invoice{{Number: "A", Payload: map[string]any{}, Failures: []validation.Violation{viol("k", "error", "")}}}}
	if got := MailFor(res, 0).Invoices[0].Failures[0].Label; got != "Invoice" {
		t.Errorf("label = %q, want Invoice", got)
	}
}

func TestMailFor_AnInfoViolationIsAWarningAndAnErrorIsTheOnlyFailure(t *testing.T) {
	res := publiccheck.Result{InvoicesTotal: 1, Invoices: []publiccheck.Invoice{{Number: "A", Payload: map[string]any{}, Failures: []validation.Violation{
		viol("i", "info", "buyer.email"), viol("e", "error", "supplier.tin"),
	}}}}
	inv := MailFor(res, 2).Invoices[0]
	if len(inv.Failures) != 1 || inv.Failures[0].Label != "Supplier TIN" || len(inv.Warnings) != 1 || inv.Warnings[0].Label != "Buyer email" || inv.Passes {
		t.Errorf("invoice = %+v, want one failure on Supplier TIN, one warning on Buyer email, not Passes", inv)
	}
}

func TestMailFor_CutsEveryFileValueButNotARuleMessage(t *testing.T) {
	long := strings.Repeat("é", 5000)
	res := publiccheck.Result{InvoicesTotal: 1, Invoices: []publiccheck.Invoice{
		{Number: long, Payload: map[string]any{"issue_date": long, "currency": long}, Failures: []validation.Violation{
			{RuleKey: "r", Severity: "error", Path: "total", Message: long},
		}},
		{Number: "B", Failures: []validation.Violation{{Severity: "error", Path: "issue_date", Message: long}}},
	}}
	m := MailFor(res, 0)
	runes := func(s string) int { return len([]rune(s)) }
	a := m.Invoices[0]
	if runes(a.Number) != 120 || runes(a.IssueDate) != 120 || runes(a.Currency) != 120 {
		t.Errorf("number, date, currency = %d, %d, %d runes, want 120 each", runes(a.Number), runes(a.IssueDate), runes(a.Currency))
	}
	if got := runes(a.Failures[0].Value); got != 5000 {
		t.Errorf("a rule message was cut to %d runes, want it whole", got)
	}
	if got := runes(m.Invoices[1].Failures[0].Value); got != 120 {
		t.Errorf("a read problem quoting the file is %d runes, want 120", got)
	}
}
