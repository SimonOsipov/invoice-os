// vocabulary.go: the field vocabulary. Resolve (EXTR-04-07) and Tier1Rules (EXTR-04-08) both
// read it, so neither owns it.
package extraction

// HeaderFields is the field vocabulary, in a fixed order -- invoices column names (D-4).
// EXTR-05 reads it to know which fields got no candidate at all.
var HeaderFields = []string{
	"invoice_number", "issue_date", "supplier_tin", "supplier_name",
	"buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total",
}
