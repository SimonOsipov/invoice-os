package submission

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
