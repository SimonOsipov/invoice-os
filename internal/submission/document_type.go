package submission

import (
	"context"
	"fmt"

	"github.com/SimonOsipov/invoice-os/internal/validation/codelist"
	"github.com/jackc/pgx/v5"
)

// DocumentType is the NRS document kind an invoice row carries.
type DocumentType string

const (
	CommercialInvoice DocumentType = "commercial_invoice"
	CreditNote        DocumentType = "credit_note"
	DebitNote         DocumentType = "debit_note"
)

// InvoiceTypeCode is the NRS cbc:InvoiceTypeCode for t. Any other value maps to 381.
func InvoiceTypeCode(t DocumentType) string {
	switch t {
	case CreditNote:
		return "380"
	case DebitNote:
		return "384"
	default:
		return "381"
	}
}

// invoiceTypeLabels is what the synced invoice-types list must say for each code InvoiceTypeCode returns.
var invoiceTypeLabels = map[DocumentType]string{
	CommercialInvoice: "Commercial Invoice",
	CreditNote:        "Credit Note",
	DebitNote:         "Debit Note",
}

// CheckInvoiceTypeCodes fails unless the synced invoice-types list labels every code
// InvoiceTypeCode returns as expected. There is no fallback: an unsynced list fails too.
func CheckInvoiceTypeCodes(ctx context.Context, tx pgx.Tx) error {
	codes := make([]string, 0, len(invoiceTypeLabels))
	for t := range invoiceTypeLabels {
		codes = append(codes, InvoiceTypeCode(t))
	}
	got, err := codelist.LabelsTx(ctx, tx, "invoice-types", codes)
	if err != nil {
		return err
	}
	for t, want := range invoiceTypeLabels {
		code := InvoiceTypeCode(t)
		if got[code] != want {
			found := "none (code absent or invoice-types list not synced)"
			if l, ok := got[code]; ok {
				found = fmt.Sprintf("%q", l)
			}
			return fmt.Errorf("submission: NRS invoice type code %s must be %q in the synced invoice-types list, found %s", code, want, found)
		}
	}
	return nil
}
