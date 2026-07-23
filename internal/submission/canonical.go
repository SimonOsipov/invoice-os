package submission

import "time"

// Canonical is 05's own, stable projection of an invoice — the type M6 is written against
// ([canonical-is-05-owned]). It carries invoice CONTENT only: no tenant id, no status, no
// violations ([canonical-is-invoice-content]).
type Canonical struct {
	InvoiceID     string // invoices.id — the adapter's correlation handle
	InvoiceNumber string
	IssueDate     *time.Time
	Supplier      Party
	Buyer         Party
	Currency      *string
	Subtotal      *string // ::text-read decimal string, never float64 [D13]
	VAT           *string
	Total         *string
	Lines         []CanonicalLine
}

type Party struct{ TIN, Name *string }

type CanonicalLine struct {
	LineID      string // line_items.id; "" for a not-yet-stored line
	LineNo      int    // 1..N, the system-assigned order [D10]
	Description *string
	Quantity    *string
	UnitPrice   *string
	LineTotal   *string
	LineTax     *string
}

// Wire is whatever bytes this adapter puts on the wire. Opaque to everything above the seam
// ([wire-is-opaque-bytes]).
type Wire []byte
