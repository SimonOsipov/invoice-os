package submission

// DocumentType is the NRS document kind an invoice row carries.
type DocumentType string

const (
	CommercialInvoice DocumentType = "commercial_invoice"
	CreditNote        DocumentType = "credit_note"
	DebitNote         DocumentType = "debit_note"
)
