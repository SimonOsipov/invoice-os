package accountmail

import (
	"strings"
	"testing"
)

func renderReadiness(t *testing.T, m ReadinessCheckMail) (subject, out string) {
	t.Helper()
	subject, out, err := RenderReadinessCheck(m)
	if err != nil {
		t.Fatalf("RenderReadinessCheck: %v", err)
	}
	return subject, out
}

// blocks splits the rendered details into one string per invoice.
func blocks(out string) []string {
	parts := strings.Split(out, "<tbody data-invoice>")[1:]
	for i, p := range parts {
		parts[i], _, _ = strings.Cut(p, "</tbody>")
	}
	return parts
}

func TestRenderReadinessCheck_ListsEachFailureWithFieldAndFix(t *testing.T) {
	_, out := renderReadiness(t, ReadinessCheckMail{Checked: 1, Total: 1, Invoices: []ReadinessCheckInvoice{{
		Number: "A1", Failures: []MailField{{"Supplier TIN", "Supplier TIN is required"}, {"Quantity", "Quantity must be above zero"}},
	}}})
	for _, w := range []string{"Supplier TIN is required", "Quantity must be above zero"} {
		if strings.Count(out, w) != 1 {
			t.Errorf("%q count = %d, want 1", w, strings.Count(out, w))
		}
	}
	if strings.Index(out, "Supplier TIN is required") > strings.Index(out, "Quantity must be above zero") {
		t.Error("failures are not in violation order")
	}
	if strings.Contains(out, "Passes every rule") {
		t.Error("a failing invoice says it passes")
	}
}

func TestRenderReadinessCheck_PassesEveryRule(t *testing.T) {
	_, out := renderReadiness(t, ReadinessCheckMail{Checked: 1, Total: 1, Invoices: []ReadinessCheckInvoice{{Number: "A1", Passes: true}}})
	if n := strings.Count(out, "Passes every rule"); n != 1 {
		t.Errorf("Passes every rule count = %d, want 1", n)
	}
}

func TestRenderReadinessCheck_ShowsWarningsApart(t *testing.T) {
	_, out := renderReadiness(t, ReadinessCheckMail{Checked: 1, Total: 1, Invoices: []ReadinessCheckInvoice{{
		Number: "A1", Failures: []MailField{{"Currency", "FAILMSG"}}, Warnings: []MailField{{"Buyer email", "WARNMSG"}},
	}}})
	if n := strings.Count(out, "Warnings"); n != 1 {
		t.Fatalf("Warnings count = %d, want 1", n)
	}
	f, w, h := strings.Index(out, "FAILMSG"), strings.Index(out, "WARNMSG"), strings.Index(out, "Warnings")
	if !(f < h && h < w) {
		t.Errorf("order failure %d, heading %d, warning %d; want failure, heading, warning", f, h, w)
	}
}

func TestRenderReadinessCheck_ShowsNotCheckedPerInvoice(t *testing.T) {
	_, out := renderReadiness(t, ReadinessCheckMail{Checked: 2, Total: 2, Invoices: []ReadinessCheckInvoice{
		{Number: "A1", Passes: true},
		{Number: "B2", Passes: true, NotChecked: []string{"supplier-city-required", "supplier-email-required"}},
	}})
	if n := strings.Count(out, "Not checked"); n != 1 {
		t.Fatalf("Not checked count = %d, want 1", n)
	}
	b := blocks(out)
	if len(b) != 2 || strings.Contains(b[0], "Not checked") || !strings.Contains(b[1], "supplier-city-required, supplier-email-required") {
		t.Errorf("Not checked is not inside the second invoice's block: %q", b)
	}
}

func TestRenderReadinessCheck_ShowsAmountsAndBuyer(t *testing.T) {
	_, out := renderReadiness(t, ReadinessCheckMail{Checked: 1, Total: 1, Invoices: []ReadinessCheckInvoice{{
		Number: "A1", Currency: "NGN", Total: "1075.50", Buyer: []MailField{{"Buyer name", "Beta Ltd"}}, Passes: true,
	}}})
	for _, w := range []string{"NGN", "1075.50", "Buyer name", "Beta Ltd"} {
		if !strings.Contains(out, w) {
			t.Errorf("mail lacks %q", w)
		}
	}
	if strings.Contains(out, ">Subtotal<") {
		t.Error("an absent subtotal still renders a row")
	}
}

func TestRenderReadinessCheck_TwoOfNOnlyWhenMoreInvoices(t *testing.T) {
	inv := []ReadinessCheckInvoice{{Number: "A", Passes: true}}
	_, out := renderReadiness(t, ReadinessCheckMail{Invoices: inv, Checked: 2, Total: 5})
	if !strings.Contains(out, "2 of 5 invoices checked") {
		t.Error("no '2 of 5 invoices checked' line")
	}
	for _, c := range [][2]int{{2, 2}, {1, 1}} {
		_, out := renderReadiness(t, ReadinessCheckMail{Invoices: inv, Checked: c[0], Total: c[1]})
		if strings.Contains(out, " of ") && strings.Contains(out, "invoices checked. Sign up") {
			t.Errorf("(%d, %d) shows the 'of' line", c[0], c[1])
		}
	}
}

func TestRenderReadinessCheck_StatesChecksLeftAndLinksSignUp(t *testing.T) {
	inv := []ReadinessCheckInvoice{{Number: "A", Passes: true}}
	for left, want := range map[int]string{2: "You have 2 free checks left.", 1: "You have 1 free check left.", 0: "You have no free checks left."} {
		_, out := renderReadiness(t, ReadinessCheckMail{Invoices: inv, Checked: 1, Total: 1, ChecksLeft: left})
		if !strings.Contains(out, want) {
			t.Errorf("ChecksLeft %d: mail lacks %q", left, want)
		}
		if !strings.Contains(out, `href="`+ReadinessCheckSignUpURL+`"`) || !strings.Contains(out, "ascomply.com/?register") {
			t.Errorf("ChecksLeft %d: no href to %s", left, ReadinessCheckSignUpURL)
		}
	}
}

func TestRenderReadinessCheck_EscapesFileText(t *testing.T) {
	_, out := renderReadiness(t, ReadinessCheckMail{Checked: 1, Total: 1, Invoices: []ReadinessCheckInvoice{{
		Number: "A1", Buyer: []MailField{{"Buyer name", "<script>x</script>"}}, Passes: true,
	}}})
	if !strings.Contains(out, "&lt;script&gt;") || strings.Contains(out, "<script>x") {
		t.Error("file text is not escaped")
	}
}

func TestRenderReadinessCheck_SubjectIsFixed(t *testing.T) {
	s, _ := renderReadiness(t, ReadinessCheckMail{Invoices: []ReadinessCheckInvoice{{Number: "<b>"}}, Checked: 1, Total: 1})
	if s != ReadinessCheckSubject {
		t.Errorf("subject = %q, want %q", s, ReadinessCheckSubject)
	}
}

func TestReadinessCheckTemplate_ComposesTheLayout(t *testing.T) {
	checkComposes(t, "readiness_check")
	src, _ := Template("readiness_check")
	if !strings.Contains(string(src), `{{define "layout"}}`) || !strings.Contains(string(src), "Passes every rule") {
		t.Error("Template(readiness_check) lacks the layout define or the body")
	}
}

func TestRenderReadinessCheck_SaysWhenADocumentIsNotAnInvoice(t *testing.T) {
	inv := []ReadinessCheckInvoice{{Number: "A", Passes: true}}
	_, out := renderReadiness(t, ReadinessCheckMail{Invoices: inv, Checked: 1, Total: 1, DocumentType: "credit_note"})
	if !strings.Contains(out, "it was checked as an invoice") {
		t.Error("no 'checked as an invoice' line")
	}
	_, out = renderReadiness(t, ReadinessCheckMail{Invoices: inv, Checked: 1, Total: 1})
	if strings.Contains(out, "checked as an invoice") {
		t.Error("the line shows for an invoice")
	}
}

func TestRenderReadinessCheck_NoInvoiceFound(t *testing.T) {
	_, out := renderReadiness(t, ReadinessCheckMail{BlankNumberRows: 4})
	if !strings.Contains(out, "We found no invoice in this file") || !strings.Contains(out, "4 rows") {
		t.Error("mail lacks the no-invoice line or the 4 rows count")
	}
	_, out = renderReadiness(t, ReadinessCheckMail{})
	if strings.Contains(out, " rows ") {
		t.Error("a zero blank-row count renders a rows line")
	}
}
