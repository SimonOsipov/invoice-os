// Package readinesscheck turns a public check result into the results mail and the summary row.
package readinesscheck

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/accountmail"
	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
	"github.com/SimonOsipov/invoice-os/internal/publiccheck"
)

const maxFileText = 120 // runes of one file value in the mail

var (
	linePath = regexp.MustCompile(`^[a-z_]+\[\d+\]$`)

	// buyerParts are the buyer payload keys the mail shows, in order.
	buyerParts = []string{"name", "tin", "email", "telephone", "street", "city", "postal_zone", "lga", "state", "country"}

	pathLabels = map[string]string{
		"billing_reference.irn":        "Original invoice IRN",
		"billing_reference.issue_date": "Original invoice date",
		"line_items":                   "Line items",
		"tax_subtotals":                "VAT breakdown",
	}
)

// cut shortens file text to maxFileText runes, the last one "…".
func cut(s string) string {
	r := []rune(s)
	if len(r) <= maxFileText {
		return s
	}
	return string(r[:maxFileText-1]) + "…"
}

func labelOf(key string) (string, bool) {
	for _, f := range invoicefields.All {
		if f.Key == key {
			return f.Label, true
		}
	}
	return "", false
}

// fieldLabel names the field a violation judged; an unknown path is its own label.
func fieldLabel(path, field string) string {
	if field != "" && linePath.MatchString(path) {
		if l, ok := labelOf(field); ok {
			return l
		}
	}
	key := path
	if rest, ok := strings.CutPrefix(path, "supplier."); ok {
		key = "supplier_" + rest
	} else if rest, ok := strings.CutPrefix(path, "buyer."); ok {
		key = "buyer_" + rest
	}
	if l, ok := labelOf(key); ok {
		return l
	}
	if l, ok := pathLabels[path]; ok {
		return l
	}
	return path
}

// FailedRuleIDs returns the distinct, sorted, non-empty rule keys of error violations.
func FailedRuleIDs(res publiccheck.Result) []string {
	var ids []string
	for _, inv := range res.Invoices {
		for _, v := range inv.Failures {
			if v.Severity == "error" && v.RuleKey != "" {
				ids = append(ids, v.RuleKey)
			}
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// MailFor builds the results mail from a check result; the caller must not log it.
func MailFor(res publiccheck.Result, checksLeft int) accountmail.ReadinessCheckMail {
	m := accountmail.ReadinessCheckMail{
		Checked: len(res.Invoices), Total: res.InvoicesTotal, ChecksLeft: checksLeft,
		DocumentType: res.DocumentType, BlankNumberRows: len(res.BlankNumberRows),
	}
	for _, inv := range res.Invoices {
		m.Invoices = append(m.Invoices, mailInvoice(inv))
	}
	return m
}

func mailInvoice(inv publiccheck.Invoice) accountmail.ReadinessCheckInvoice {
	out := accountmail.ReadinessCheckInvoice{Number: cut(inv.Number), NotChecked: inv.NotChecked}
	for _, v := range inv.Failures {
		msg := v.Message
		if v.RuleKey == "" { // a read problem may quote the file
			msg = cut(msg)
		}
		label := fieldLabel(v.Path, v.Field)
		if label == "" { // a rule with no target judges the whole invoice
			label = "Invoice"
		}
		f := accountmail.MailField{Label: label, Value: msg}
		if v.Severity == "error" {
			out.Failures = append(out.Failures, f)
		} else {
			out.Warnings = append(out.Warnings, f)
		}
	}
	p := inv.Payload
	if p == nil {
		return out
	}
	out.Passes = len(out.Failures) == 0
	out.IssueDate, out.Currency = text(p["issue_date"]), text(p["currency"])
	out.Subtotal, out.VAT, out.Total = money(p["subtotal"]), money(p["vat"]), money(p["total"])
	buyer, _ := p["buyer"].(map[string]any)
	for _, part := range buyerParts {
		if v := text(buyer[part]); v != "" {
			label, _ := labelOf("buyer_" + part)
			out.Buyer = append(out.Buyer, accountmail.MailField{Label: label, Value: v})
		}
	}
	return out
}

func text(v any) string {
	s, _ := v.(string)
	return cut(s)
}

func money(v any) string {
	if f, ok := v.(float64); ok {
		return fmt.Sprintf("%.2f", f)
	}
	return ""
}
