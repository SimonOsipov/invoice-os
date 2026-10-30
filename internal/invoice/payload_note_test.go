package invoice

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

func marshalPayload(t *testing.T, inv Invoice) string {
	t.Helper()
	b, err := json.Marshal(MBSPayload(inv))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMBSPayload_DocumentTypePresentWhenSet(t *testing.T) {
	for _, dt := range []submission.DocumentType{"credit_note", "commercial_invoice"} {
		if got := MBSPayload(Invoice{DocumentType: dt})["document_type"]; got != string(dt) {
			t.Errorf("document_type = %#v, want %q", got, dt)
		}
	}
}

func TestMBSPayload_DocumentTypeAbsentWhenEmpty(t *testing.T) {
	if got := marshalPayload(t, Invoice{InvoiceNumber: "E-1"}); got != `{"invoice_number":"E-1"}` {
		t.Errorf("payload = %s", got)
	}
}

func TestMBSPayload_BillingReferenceFromTheOriginal(t *testing.T) {
	d := time.Date(2026, 3, 4, 15, 0, 0, 0, time.UTC)
	inv := Invoice{Original: &submission.BillingReference{IRN: strPtr("IRN-1"), IssueDate: &d}}
	b, _ := json.Marshal(MBSPayload(inv)["billing_reference"])
	if string(b) != `{"irn":"IRN-1","issue_date":"2026-03-04"}` {
		t.Errorf("billing_reference = %s", b)
	}
}

func TestMBSPayload_BillingReferenceOmitsNilMembers(t *testing.T) {
	d := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		orig *submission.BillingReference
		want string
	}{
		"nil irn":      {&submission.BillingReference{IssueDate: &d}, `{"issue_date":"2026-03-04"}`},
		"nil date":     {&submission.BillingReference{IRN: strPtr("IRN-1")}, `{"irn":"IRN-1"}`},
		"both nil":     {&submission.BillingReference{}, ""},
		"nil original": {nil, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := MBSPayload(Invoice{Original: c.orig})
			got, ok := p["billing_reference"]
			if c.want == "" {
				if ok {
					t.Fatalf("billing_reference = %#v, want absent", got)
				}
				return
			}
			b, _ := json.Marshal(got)
			if string(b) != c.want {
				t.Errorf("billing_reference = %s, want %s", b, c.want)
			}
		})
	}
}

func TestMBSPayload_OriginalTotalAndCreditedBeforeAreNumbers(t *testing.T) {
	got := marshalPayload(t, Invoice{InvoiceNumber: "N-1", OriginalTotal: strPtr("100.00"), CreditedBefore: strPtr("0.00")})
	want := `{"credited_before":0.00,"invoice_number":"N-1","original_total":100.00}`
	if got != want {
		t.Errorf("payload = %s, want %s", got, want)
	}
}

func TestMBSPayload_NaNFactStaysARawString(t *testing.T) {
	got := marshalPayload(t, Invoice{InvoiceNumber: "N-1", OriginalTotal: strPtr("NaN")})
	if !strings.Contains(got, `"original_total":"NaN"`) {
		t.Errorf("payload = %s, want original_total as the string NaN", got)
	}
}

func TestMBSPayload_CommercialInvoiceGainsOnlyDocumentType(t *testing.T) {
	inv := fullFingerprintFixture()
	head := MBSPayload(inv)
	inv.DocumentType = "commercial_invoice"
	var added []string
	for k := range MBSPayload(inv) {
		if _, ok := head[k]; !ok {
			added = append(added, k)
		}
	}
	if !reflect.DeepEqual(added, []string{"document_type"}) {
		t.Errorf("keys gained = %v, want [document_type]", added)
	}
}

func TestContentFingerprint_IgnoresTheNoteFacts(t *testing.T) {
	a, b := fullFingerprintFixture(), fullFingerprintFixture()
	b.Original = &submission.BillingReference{IRN: strPtr("IRN-1")}
	b.OriginalTotal, b.CreditedBefore = strPtr("100.00"), strPtr("5.00")
	if contentFingerprint(a, a.LineItems) != contentFingerprint(b, b.LineItems) {
		t.Error("the note facts moved the fingerprint")
	}
}

func TestInvoiceJSON_DoesNotCarryTheNoteFacts(t *testing.T) {
	b, err := json.Marshal(Invoice{OriginalTotal: strPtr("100.00"), CreditedBefore: strPtr("5.00")})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"original_total", "credited_before"} {
		if strings.Contains(string(b), k) {
			t.Errorf("invoice JSON carries %s: %s", k, b)
		}
	}
}
