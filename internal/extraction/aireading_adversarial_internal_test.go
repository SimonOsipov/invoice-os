// aireading_adversarial_internal_test.go: edge and adversarial rows for askAI and checkAI,
// each refusal paired with a control that passes.
package extraction

import (
	"context"
	"math"
	"testing"
)

func TestAICheck_NoPagesOrNoTokensChecksNothing(t *testing.T) {
	for name, pages := range map[string][]TokenPage{
		"nil pages":        nil,
		"one empty page":   {{Number: 1}},
		"two empty pages":  {{Number: 1}, {Number: 2}},
		"blank token text": onePage(1, tok("   ", 1, 0.1, 0.1, 0.2, 0.12)),
	} {
		if _, ok := checkAI("total", "1935.00", pages); ok {
			t.Errorf("%s: checkAI accepted a value with nothing printed", name)
		}
	}
	if _, ok := checkAI("total", "1935.00", onePage(1, tok("1,935.00", 1, 0.1, 0.1, 0.2, 0.12))); !ok {
		t.Error("control: checkAI refused a printed total")
	}
}

func TestAICheck_WhitespaceOnlyAnswerIsRefused(t *testing.T) {
	pages := onePage(1, tok("Invoice No: 20417", 1, 0.1, 0.1, 0.4, 0.12))
	for _, field := range HeaderFields {
		if _, ok := checkAI(field, " \t\n ", pages); ok {
			t.Errorf("checkAI(%s) accepted a whitespace-only answer", field)
		}
	}
	if _, ok := checkAI("invoice_number", " 20417\t", pages); !ok {
		t.Error("control: checkAI refused 20417 padded with whitespace")
	}
}

func TestAICheck_AnUnknownFieldIsRefused(t *testing.T) {
	pages := onePage(1, tok("line_items 20417", 1, 0.1, 0.1, 0.4, 0.12))
	if _, ok := checkAI("line_items", "20417", pages); ok {
		t.Error("checkAI accepted a field outside HeaderFields")
	}
}

func TestAICheck_TheValueIsTheShapesOutput(t *testing.T) {
	pages := onePage(1, tok("Total Due 1,935.00", 1, 0.1, 0.1, 0.4, 0.12))
	if r, ok := checkAI("total", "1,935.00", pages); !ok || r.Value != "1935.00" {
		t.Errorf("checkAI(total 1,935.00) = %+v, %v; want checked as 1935.00", r, ok)
	}
	inv := onePage(1, tok("Invoice No: 20417", 1, 0.1, 0.1, 0.4, 0.12))
	if r, ok := checkAI("invoice_number", " 20417 ", inv); !ok || r.Value != "20417" {
		t.Errorf("checkAI(invoice_number ' 20417 ') = %+v, %v; want checked as 20417", r, ok)
	}
}

func TestAICheck_RegionIsTheFirstOccurrence(t *testing.T) {
	first := tok("Total 1,935.00", 1, 0.1, 0.1, 0.4, 0.12)
	second := tok("Amount due 1,935.00", 1, 0.1, 0.5, 0.4, 0.52)
	r, ok := checkAI("total", "1935.00", onePage(1, first, second))
	if !ok || r.Region == nil || *r.Region != first.Region {
		t.Errorf("checkAI = %+v, %v; want the first token's region %v", r, ok, first.Region)
	}
}

func TestAICheck_InvoiceNumberRegionIsTheLabelledOccurrence(t *testing.T) {
	bare := tok("20417", 1, 0.1, 0.1, 0.2, 0.12)
	labelled := tok("Invoice No: 20417", 1, 0.1, 0.5, 0.4, 0.52)
	r, ok := checkAI("invoice_number", "20417", onePage(1, bare, labelled))
	if !ok || r.Region == nil || *r.Region != labelled.Region {
		t.Errorf("checkAI = %+v, %v; want the labelled token's region %v", r, ok, labelled.Region)
	}
}

func TestAICheck_ValueOnTwoPages(t *testing.T) {
	p1 := tok("Bill To: ZENITH HOLDINGS LIMITED", 1, 0.1, 0.1, 0.6, 0.12)
	p2 := tok("Account Name: ZENITH HOLDINGS LIMITED", 2, 0.1, 0.1, 0.6, 0.12)
	clean := tok("Bill To: ZENITH HOLDINGS LIMITED", 2, 0.1, 0.1, 0.6, 0.12)

	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", []TokenPage{
		{Number: 1, Tokens: []Token{p1}}, {Number: 2, Tokens: []Token{p2}},
	}); ok {
		t.Error("checkAI accepted a name behind a payment label on page 2")
	}
	r, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", []TokenPage{
		{Number: 1, Tokens: []Token{p1}}, {Number: 2, Tokens: []Token{clean}},
	})
	if !ok || r.Region == nil || *r.Region != p1.Region {
		t.Errorf("control: checkAI = %+v, %v; want checked with page 1's region", r, ok)
	}
}

func TestAICheck_LineBeforeIsSamePageOnly(t *testing.T) {
	label := tok("ACCOUNT NAME", 1, 0.1, 0.9, 0.3, 0.92)
	value := tok("ZENITH HOLDINGS LIMITED", 2, 0.1, 0.1, 0.5, 0.12)
	across := []TokenPage{{Number: 1, Tokens: []Token{label}}, {Number: 2, Tokens: []Token{value}}}
	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", across); !ok {
		t.Error("checkAI refused a value because the PREVIOUS page ends with a payment label")
	}
	same := onePage(1, label, value)
	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", same); ok {
		t.Error("control: checkAI accepted the value with the label on the line before, same page")
	}
}

func TestAICheck_PaymentLabelIsAWholeWord(t *testing.T) {
	value := tok("ZENITH HOLDINGS LIMITED", 1, 0.1, 0.2, 0.5, 0.22)
	for _, before := range []string{"BANKOLE PLAZA, 5 BROAD STREET", "DATABANK SERVICES", "Bankers Row"} {
		pages := onePage(1, tok(before, 1, 0.1, 0.1, 0.5, 0.12), value)
		if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", pages); !ok {
			t.Errorf("checkAI refused a value behind %q, which carries no payment label word", before)
		}
	}
	named := onePage(1, tok("Bill To: BANKOLE TRADING LIMITED", 1, 0.1, 0.1, 0.5, 0.12))
	if _, ok := checkAI("supplier_name", "BANKOLE TRADING LIMITED", named); !ok {
		t.Error("checkAI refused BANKOLE TRADING LIMITED as a party name")
	}
	control := onePage(1, tok("Our Bank", 1, 0.1, 0.1, 0.5, 0.12), value)
	if _, ok := checkAI("buyer_name", "ZENITH HOLDINGS LIMITED", control); ok {
		t.Error("control: checkAI accepted a value behind the word Bank")
	}
}

func TestAICheck_PaymentLabelIgnoresCaseAndSpacing(t *testing.T) {
	value := tok("ZENITH HOLDINGS LIMITED", 1, 0.1, 0.2, 0.5, 0.22)
	for _, before := range []string{
		"account name", "AccountName:", "ACCOUNT   NUMBER", "bAnK", "A C C O U N T   N A M E",
	} {
		pages := onePage(1, tok(before, 1, 0.1, 0.1, 0.5, 0.12), value)
		if _, ok := checkAI("supplier_name", "ZENITH HOLDINGS LIMITED", pages); ok {
			t.Errorf("checkAI accepted a value behind the payment label %q", before)
		}
	}
	if _, ok := checkAI("supplier_name", "ZENITH HOLDINGS LIMITED", onePage(1, tok("Supplier", 1, 0.1, 0.1, 0.5, 0.12), value)); !ok {
		t.Error("control: checkAI refused the value behind a non-payment label")
	}
}

func TestAICheck_UnicodeNames(t *testing.T) {
	name := "ÀDÉYẸMÍ ỌLỌ́JÀ LIMITED"
	if _, ok := checkAI("supplier_name", name, onePage(1, tok("From: "+name, 1, 0.1, 0.1, 0.6, 0.12))); !ok {
		t.Error("checkAI refused a printed unicode name")
	}
	if _, ok := checkAI("supplier_name", name, onePage(1, tok("Account Name: "+name, 1, 0.1, 0.1, 0.6, 0.12))); ok {
		t.Error("checkAI accepted a unicode name behind a payment label")
	}
	if _, ok := checkAI("supplier_name", "ADEYEMI OLOJA LIMITED", onePage(1, tok(name, 1, 0.1, 0.1, 0.6, 0.12))); ok {
		t.Error("checkAI accepted an accent-stripped name that is not printed")
	}
}

func TestAICheck_LabelRemainderFindsAGluedValue(t *testing.T) {
	glued := onePage(1, tok("TIN:99999999-1202", 1, 0.1, 0.1, 0.4, 0.12))
	if _, ok := checkAI("buyer_tin", "99999999-1202", glued); !ok {
		t.Error("checkAI refused a TIN glued to its label")
	}
	if _, ok := checkAI("buyer_tin", "99999999-1203", glued); ok {
		t.Error("control: checkAI accepted a different TIN")
	}
}

func TestAICheck_InvoiceNumberExceptionIsForInvoiceNumberOnly(t *testing.T) {
	pages := onePage(1, tok("Invoice No: 20417", 1, 0.1, 0.1, 0.4, 0.12))
	for _, field := range []string{"supplier_tin", "buyer_tin", "currency", "issue_date"} {
		if _, ok := checkAI(field, "20417", pages); ok {
			t.Errorf("checkAI(%s) accepted all-digit 20417 under the invoice-number exception", field)
		}
	}
	if _, ok := checkAI("invoice_number", "20417", pages); !ok {
		t.Error("control: checkAI(invoice_number) refused 20417 behind its label")
	}
}

func TestAICheck_UnusableBoxesGiveANilRegion(t *testing.T) {
	for name, r := range map[string]Region{
		"NaN":       {Page: 1, X0: math.NaN(), Y0: 0.1, X1: 0.4, Y1: 0.12},
		"zero area": {Page: 1, X0: 0.1, Y0: 0.1, X1: 0.1, Y1: 0.12},
	} {
		got, ok := checkAI("total", "1935.00", onePage(1, Token{Text: "1,935.00", Region: r}))
		if !ok || got.Region != nil {
			t.Errorf("%s: checkAI = %+v, %v; want checked with a nil region", name, got, ok)
		}
	}
}

func TestAskAI_DropsWhitespaceOnlyAndNonHeaderAnswers(t *testing.T) {
	pages := onePage(1, tok("Invoice", 1, 0.1, 0.1, 0.2, 0.12))
	stub := &recordingAI{enabled: true, answer: map[string]any{
		"buyer_name": "\t\n", "currency": "", "line_items": "x", "total": 1935.0, "vat": " 135.00 ",
	}}
	got, _ := askAI(context.Background(), stub, pages)
	if len(got) != 1 || got["vat"] != " 135.00 " {
		t.Errorf("askAI = %q, want only vat kept untrimmed", got)
	}
	stub.answer = map[string]any{"buyer_name": "\t\n"}
	if got, _ := askAI(context.Background(), stub, pages); got != nil {
		t.Errorf("askAI(all blank) = %v, want nil", got)
	}
}

func TestAIPromptText_EmptyPageStillPrintsItsHeader(t *testing.T) {
	got := aiPromptText([]TokenPage{{Number: 1}, {Number: 2, Tokens: []Token{tok("X", 2, 0.5, 0.25, 0.6, 0.3)}}})
	want := aiTextIntro + "\n\n--- page 1 ---\n--- page 2 ---\ny=0.250 | x=0.50 X"
	if got != want {
		t.Errorf("aiPromptText = %q, want %q", got, want)
	}
}
