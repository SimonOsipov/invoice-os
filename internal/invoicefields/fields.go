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
	{Key: "invoice_kind", Label: "Invoice kind", Type: Text},
	{Key: "tax_currency_code", Label: "Tax currency code", Type: Text},
	{Key: "due_date", Label: "Due date", Type: Date},
	{Key: "issue_time", Label: "Issue time", Type: Text},
	{Key: "tax_point_date", Label: "Tax point date", Type: Date},
	{Key: "payment_status", Label: "Payment status", Type: Text},
	{Key: "supplier_email", Label: "Supplier email", Type: Text},
	{Key: "supplier_telephone", Label: "Supplier telephone", Type: Text},
	{Key: "supplier_street", Label: "Supplier street", Type: Text},
	{Key: "supplier_city", Label: "Supplier city", Type: Text},
	{Key: "supplier_postal_zone", Label: "Supplier postal zone", Type: Text},
	{Key: "supplier_country", Label: "Supplier country", Type: Text},
	{Key: "supplier_state", Label: "Supplier state", Type: Text},
	{Key: "supplier_lga", Label: "Supplier LGA", Type: Text},
	{Key: "buyer_email", Label: "Buyer email", Type: Text},
	{Key: "buyer_telephone", Label: "Buyer telephone", Type: Text},
	{Key: "buyer_street", Label: "Buyer street", Type: Text},
	{Key: "buyer_city", Label: "Buyer city", Type: Text},
	{Key: "buyer_postal_zone", Label: "Buyer postal zone", Type: Text},
	{Key: "buyer_country", Label: "Buyer country", Type: Text},
	{Key: "buyer_state", Label: "Buyer state", Type: Text},
	{Key: "buyer_lga", Label: "Buyer LGA", Type: Text},
	{Key: "description", Label: "Description", Type: Text, Line: true, Import: true, Edit: true, Extract: true},
	{Key: "quantity", Label: "Quantity", Type: Quantity, Line: true, Import: true, Edit: true, Extract: true},
	{Key: "unit_price", Label: "Unit price", Type: Money, Line: true, Import: true, Edit: true, Extract: true},
	{Key: "line_total", Label: "Line total", Type: Money, Line: true, Edit: true, Extract: true},
	{Key: "line_tax", Label: "Line tax", Type: Money, Line: true, Edit: true, Extract: true},
	{Key: "tax_category", Label: "Tax category", Type: Text, Line: true},
	{Key: "hsn_code", Label: "HSN code", Type: Text, Line: true},
	{Key: "isic_code", Label: "ISIC code", Type: Text, Line: true},
	{Key: "product_category", Label: "Product category", Type: Text, Line: true},
	{Key: "service_category", Label: "Service category", Type: Text, Line: true},
	{Key: "sellers_item_identification", Label: "Seller's item identification", Type: Text, Line: true},
	{Key: "price_unit", Label: "Price unit", Type: Text, Line: true},
	{Key: "tax_percent", Label: "Tax percent", Type: Money, Line: true},
	{Key: "base_quantity", Label: "Base quantity", Type: Quantity, Line: true},
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
