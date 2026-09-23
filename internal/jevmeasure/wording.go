package jevmeasure

import "slices"

// Provisional wording (A37): CHECK-01-02 authors and pins this text so the
// report can quote it byte-for-byte. Reconciling it with the real
// TypeSafe-console wording that produced the chosen thresholds is owed at
// the live run, not here.

const (
	ValueCheckInstructions    = "State whether the following statement about this Nigerian tax invoice is true: the value shown for this field is exactly the value printed on the document."
	ValueCheckCriteriaTrue    = "The stated value matches what is printed on the invoice, character for character."
	ValueCheckCriteriaFalse   = "The stated value does not match what is printed on the invoice, or the field is missing, corrupted, or transposed."
	MappingCheckInstructions  = "State whether the following statement about this spreadsheet column mapping is true: the column header was correctly mapped to the invoice field it names."
	MappingCheckCriteriaTrue  = "The column holds the data the mapped field expects, and no other field better matches the column's contents."
	MappingCheckCriteriaFalse = "The column's data belongs to a different invoice field, or the column does not correspond to any invoice field."
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
