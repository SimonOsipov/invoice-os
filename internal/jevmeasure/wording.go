package jevmeasure

import "slices"

// The value- and mapping-check wording is the one measured in CHECK-00 Jev Measurement Results.

const (
	ValueCheckInstructions    = "State whether the following statement about this Nigerian tax invoice is true: the value shown for this field is the value the document prints for that field. The value is written in a normalised form: amounts without thousands separators or currency symbols, dates as YYYY-MM-DD, and currency as a three-letter ISO code."
	ValueCheckCriteriaTrue    = "The stated value is the same value the invoice prints for this field, ignoring only formatting such as thousands separators, currency symbols, and date format."
	ValueCheckCriteriaFalse   = "The stated value differs from what the invoice prints for this field: a digit, character, amount, or date is different, missing, or transposed, or the value belongs to another field."
	MappingCheckInstructions  = "State whether the following statement about this spreadsheet is true: the named column holds the named invoice field. The spreadsheet's first rows are shown; one row is one invoice line, and invoice-level values repeat on every line of the same invoice. Judge from the column's header and its values; a header may use any name, abbreviation or language for its field. The fields are: invoice_number: the invoice's own number, not an order, PO, customer, account or payment reference. issue_date: the date the invoice was issued, not a due, delivery or payment date. buyer_tin: the buyer's (customer's) Tax Identification Number, not the seller's own TIN or an RC number. buyer_name: the buyer's name, not a customer code, address, email or phone. currency: the currency code. subtotal: the invoice's amount before VAT, not a line amount. vat: the invoice's VAT amount, not a VAT rate or a line's tax. total: the invoice's total including VAT, not a line amount, amount paid, balance due or amount after withholding tax. line_description: the line's item or service description. line_quantity: the line's quantity. line_unit_price: the line's price per unit, not a line total."
	MappingCheckCriteriaTrue  = "The column holds the named field as defined above, whatever its header is called."
	MappingCheckCriteriaFalse = "The column holds something other than the named field as defined above: another field, a rate or percentage where an amount is defined, a line amount where an invoice amount is defined, the seller's details where the buyer's are defined, or data that is not an invoice field."
	DocumentTypeInstructions  = "Classify which of the following document types this file is, based on its layout, headings and language."
)

// documentTypeOrder is A30's fixed eight-option list, in its fixed order.
var documentTypeOrder = []string{
	"tax invoice", "receipt", "proforma", "quotation",
	"credit note", "delivery note", "statement", "purchase order",
}

// DocumentTypeOptions is A30's closed, ordered eight. Cloned: the order is a fixed fact, not a
// slice a caller may reorder.
func DocumentTypeOptions() []string { return slices.Clone(documentTypeOrder) }

// The two question-type spellings.
const (
	QuestionTypeNoul   = "noul"
	QuestionTypeChoice = "choice"
)

// DocumentTypeCriteria describes each of A30's eight options, keyed by option name.
var DocumentTypeCriteria = map[string]string{
	"tax invoice":    "A demand for payment for goods or services already supplied, addressed to a specific buyer, carrying a VAT amount and a total due.",
	"receipt":        "A confirmation that a payment has already been received, not a demand for future payment.",
	"proforma":       "A preliminary bill sent before the goods or services are supplied, declaring a price in advance of a sale.",
	"quotation":      "An offer of a price for goods or services not yet agreed to or supplied.",
	"credit note":    "A document reducing or reversing a previously issued invoice, not a new demand for payment.",
	"delivery note":  "A record that goods were delivered, carrying quantities but no prices or payment demand.",
	"statement":      "A running list of an account's transactions over a period, not a single demand for payment.",
	"purchase order": "A buyer's own request to a supplier to provide goods or services, not a supplier's demand for payment.",
}

// WordingRegistry returns every wording string the report must quote
// verbatim: the seven instructions/criteria constants plus the eight
// DocumentTypeCriteria entries. Rendering from this map, not a hand-written
// list, means a constant added here without being wired into Render fails
// TestReport_QuotesEveryWordingRegistryEntryVerbatim rather than escaping unnoticed.
func WordingRegistry() map[string]string {
	reg := map[string]string{
		"value_check.instructions":         ValueCheckInstructions,
		"value_check.criteria.true":        ValueCheckCriteriaTrue,
		"value_check.criteria.false":       ValueCheckCriteriaFalse,
		"mapping_check.instructions":       MappingCheckInstructions,
		"mapping_check.criteria.true":      MappingCheckCriteriaTrue,
		"mapping_check.criteria.false":     MappingCheckCriteriaFalse,
		"document_type_check.instructions": DocumentTypeInstructions,
	}
	for opt, desc := range DocumentTypeCriteria {
		reg["document_type_check.criteria."+opt] = desc
	}
	return reg
}
