// Package invoicefields is the one list of invoice fields that the import,
// extraction and SPA field lists derive from.
package invoicefields

type Type string

const (
	Text     Type = "text"
	Date     Type = "date"
	Money    Type = "money"
	Quantity Type = "quantity"
)

type Field struct {
	Key      string // wire key: the invoices or line_items column
	Label    string
	Type     Type
	Line     bool   // a line_items field; else a header field
	Import   bool   // on the import Map step
	Required bool   // an import mapping must place it
	FormKey  string // the create form's Draft key; "" when not on the form
	Edit     bool   // on the editor
	Extract  bool   // read from a document
}

var All = []Field{
	{Key: "invoice_number", Label: "Invoice number", Type: Text, Import: true, Required: true, FormKey: "number", Extract: true},
	{Key: "issue_date", Label: "Issue date", Type: Date, Import: true, FormKey: "date", Edit: true, Extract: true},
	{Key: "supplier_tin", Label: "Supplier TIN", Type: Text, Edit: true, Extract: true},
	{Key: "supplier_name", Label: "Supplier name", Type: Text, Edit: true, Extract: true},
	{Key: "buyer_tin", Label: "Buyer TIN", Type: Text, Import: true, FormKey: "buyerTin", Edit: true, Extract: true},
	{Key: "buyer_name", Label: "Buyer name", Type: Text, Import: true, FormKey: "buyer", Edit: true, Extract: true},
	{Key: "currency", Label: "Currency", Type: Text, Import: true, FormKey: "currency", Edit: true, Extract: true},
	{Key: "subtotal", Label: "Subtotal", Type: Money, Import: true, Edit: true, Extract: true},
	{Key: "vat", Label: "VAT", Type: Money, Import: true, Edit: true, Extract: true},
	{Key: "total", Label: "Total", Type: Money, Import: true, Edit: true, Extract: true},
	{Key: "description", Label: "Description", Type: Text, Line: true, Import: true, Edit: true, Extract: true},
	{Key: "quantity", Label: "Quantity", Type: Quantity, Line: true, Import: true, Edit: true, Extract: true},
	{Key: "unit_price", Label: "Unit price", Type: Money, Line: true, Import: true, Edit: true, Extract: true},
	{Key: "line_total", Label: "Line total", Type: Money, Line: true, Edit: true, Extract: true},
	{Key: "line_tax", Label: "Line tax", Type: Money, Line: true, Edit: true, Extract: true},
}

// ImportKey is the key on the import Map step: line fields get a "line_" prefix.
func (f Field) ImportKey() string {
	if f.Line {
		return "line_" + f.Key
	}
	return f.Key
}

func ImportKeys() []string {
	var keys []string
	for _, f := range All {
		if f.Import {
			keys = append(keys, f.ImportKey())
		}
	}
	return keys
}

func ExtractHeaderKeys() []string { return extractKeys(false) }

func ExtractLineKeys() []string { return extractKeys(true) }

func extractKeys(line bool) []string {
	var keys []string
	for _, f := range All {
		if f.Extract && f.Line == line {
			keys = append(keys, f.Key)
		}
	}
	return keys
}
