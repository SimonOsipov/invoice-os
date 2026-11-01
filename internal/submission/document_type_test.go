package submission

import "testing"

func TestInvoiceTypeCode_MapsEachDocumentType(t *testing.T) {
	for _, tc := range []struct {
		in   DocumentType
		want string
	}{
		{CommercialInvoice, "381"},
		{CreditNote, "380"},
		{DebitNote, "384"},
		{"", "381"},
		{"bogus_type", "381"},
		{"Credit_Note", "381"},
		{"380", "381"},
	} {
		if got := InvoiceTypeCode(tc.in); got != tc.want {
			t.Errorf("InvoiceTypeCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
