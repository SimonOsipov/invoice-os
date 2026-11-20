package submission

import (
	"context"
	"errors"
	"fmt"

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

// invoiceTypeLabels is what the synced invoice-types list must say for each code InvoiceTypeCode
// returns. A slice, so the first failure reported is the same every run.
var invoiceTypeLabels = []struct {
	docType DocumentType
	label   string
}{
	{CommercialInvoice, "Commercial Invoice"},
	{CreditNote, "Credit Note"},
	{DebitNote, "Debit Note"},
}

// LabelsFunc reads the label of each wanted code in a synced code list (codelist.LabelsTx).
// Injected so internal/archive, which imports this package, does not reach codelist.
type LabelsFunc func(ctx context.Context, tx pgx.Tx, list string, codes []string) (map[string]string, error)

// CheckInvoiceTypeCodes fails unless the synced invoice-types list labels every code
// InvoiceTypeCode returns as expected. There is no fallback: an unsynced list or a nil
// labels lookup fails too.
func CheckInvoiceTypeCodes(ctx context.Context, tx pgx.Tx, labels LabelsFunc) error {
	if labels == nil {
		return errors.New("submission: no invoice-types lookup wired; cannot check the NRS invoice type codes")
	}
	codes := make([]string, 0, len(invoiceTypeLabels))
	for _, e := range invoiceTypeLabels {
		codes = append(codes, InvoiceTypeCode(e.docType))
	}
	got, err := labels(ctx, tx, "invoice-types", codes)
	if err != nil {
		return err
	}
	for _, e := range invoiceTypeLabels {
		code := InvoiceTypeCode(e.docType)
		if got[code] != e.label {
			found := "none (code absent or invoice-types list not synced)"
			if l, ok := got[code]; ok {
				found = fmt.Sprintf("%q", l)
			}
			return fmt.Errorf("submission: NRS invoice type code %s must be %q in the synced invoice-types list, found %s", code, e.label, found)
		}
	}
	return nil
}
