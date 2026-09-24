// jevcheck.go asks Jev, once per extraction attempt, whether each decided header value is what the page prints, and what kind of document it is.
package extraction

import (
	"context"
	"fmt"
	"slices"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// JevAsker is the slice of *jev.Client the worker uses; nil is off.
type JevAsker interface {
	Enabled() bool
	Ask(ctx context.Context, req jev.Request) (jev.Response, error)
}

// A noul at or below this flags the field (CHECK-00 Jev Measurement Results).
// ceiling: measured on 21 synthetic documents; re-measure on real documents before trusting it in production
const valueCheckThreshold = 0.5

// internal/jevmeasure/wording.go's value-check text, byte for byte
// (TestJevProduct_TheValueQuestionIsTheMeasuredQuestion).
const (
	valueCheckInstructions = "State whether the following statement about this Nigerian tax invoice is true: the value shown for this field is the value the document prints for that field. The value is written in a normalised form: amounts without thousands separators or currency symbols, dates as YYYY-MM-DD, and currency as a three-letter ISO code."
	valueCheckTrue         = "The stated value is the same value the invoice prints for this field, ignoring only formatting such as thousands separators, currency symbols, and date format."
	valueCheckFalse        = "The stated value differs from what the invoice prints for this field: a digit, character, amount, or date is different, missing, or transposed, or the value belongs to another field."
)

func ValueCheckQuestion(field, value string) jev.Question {
	return jev.Question{
		Type:         jev.TypeNoul,
		Instructions: valueCheckInstructions + fmt.Sprintf(" Field: %s. Value: %s.", field, value),
		True:         valueCheckTrue,
		False:        valueCheckFalse,
	}
}

// valueCheckRequest returns the asked result indexes too; nil means nothing to ask.
// The supplier pair is skipped: invoice.Store overwrites it from the client entity.
func valueCheckRequest(pages []TokenPage, results []FieldResult) (jev.Request, []int) {
	req := jev.Request{Purpose: jev.PurposeValueCheck, Questions: map[string]jev.Question{}}
	var asked []int
	for i, r := range results {
		if !slices.Contains(HeaderFields, r.Name) || r.Name == "supplier_tin" || r.Name == "supplier_name" ||
			r.Reason != ReasonNone || r.Value == nil {
			continue
		}
		req.Questions[r.Name] = ValueCheckQuestion(r.Name, *r.Value)
		asked = append(asked, i)
	}
	if len(asked) == 0 {
		return jev.Request{}, nil
	}
	req.State = DoclingPromptText(pages)
	return req, asked
}

// applyValueCheck flags a doubted field and never rewrites its value.
func applyValueCheck(results []FieldResult, asked []int, resp jev.Response) []FieldResult {
	out := slices.Clone(results)
	for _, i := range asked {
		if resp.Answers[out[i].Name].Noul <= valueCheckThreshold {
			out[i].Reason = ReasonUnreadable
		}
	}
	return out
}

const documentTypeQuestionID = "document_type"

const taxInvoice = "tax invoice"

// A choice at or above this confidence records a verdict (CHECK-00 Jev Measurement Results).
// ceiling: unmeasured cut, all 21 synthetic answers scored >= 0.9; re-measure on real non-invoices before a key is set
const documentTypeThreshold = 0.9

// internal/jevmeasure/wording.go's document-type text and order, byte for byte
// (TestJevProduct_TheDocumentTypeQuestionIsTheMeasuredQuestion).
const documentTypeInstructions = "Classify which of the following document types this file is, based on its layout, headings and language."

var documentTypeOptions = []jev.Option{
	{Name: "tax invoice", Description: "A demand for payment for goods or services already supplied, addressed to a specific buyer, carrying a VAT amount and a total due."},
	{Name: "receipt", Description: "A confirmation that a payment has already been received, not a demand for future payment."},
	{Name: "proforma", Description: "A preliminary bill sent before the goods or services are supplied, declaring a price in advance of a sale."},
	{Name: "quotation", Description: "An offer of a price for goods or services not yet agreed to or supplied."},
	{Name: "credit note", Description: "A document reducing or reversing a previously issued invoice, not a new demand for payment."},
	{Name: "delivery note", Description: "A record that goods were delivered, carrying quantities but no prices or payment demand."},
	{Name: "statement", Description: "A running list of an account's transactions over a period, not a single demand for payment."},
	{Name: "purchase order", Description: "A buyer's own request to a supplier to provide goods or services, not a supplier's demand for payment."},
}

func DocumentTypeQuestion() jev.Question {
	return jev.Question{
		Type:         jev.TypeChoice,
		Instructions: documentTypeInstructions,
		Options:      slices.Clone(documentTypeOptions),
		Default:      taxInvoice,
	}
}

// documentRequest always asks the type question, even with no value to check.
func documentRequest(pages []TokenPage, results []FieldResult) (jev.Request, []int) {
	req, asked := valueCheckRequest(pages, results)
	if len(asked) == 0 {
		req = jev.Request{Purpose: jev.PurposeDocumentType, State: DoclingPromptText(pages), Questions: map[string]jev.Question{}}
	}
	req.Questions[documentTypeQuestionID] = DocumentTypeQuestion()
	return req, asked
}

// documentTypeVerdict returns "" for no verdict; only a known non-invoice name may reach the column's CHECK.
func documentTypeVerdict(resp jev.Response) string {
	a, ok := resp.Answers[documentTypeQuestionID]
	// !(>=), not <, so a NaN confidence records nothing.
	if !ok || a.Type != jev.TypeChoice || a.Choice == taxInvoice || !(a.Confidence >= documentTypeThreshold) ||
		!slices.ContainsFunc(documentTypeOptions, func(o jev.Option) bool { return o.Name == a.Choice }) {
		return ""
	}
	return a.Choice
}

// checkDocument returns results unchanged and no verdict when the call fails.
// An unusable answer of one kind leaves the other kind's answer in force.
func checkDocument(ctx context.Context, j JevAsker, pages []TokenPage, results []FieldResult) ([]FieldResult, string) {
	if j == nil || !j.Enabled() {
		return results, ""
	}
	req, asked := documentRequest(pages, results)
	resp, err := j.Ask(ctx, req)
	if err != nil {
		return results, ""
	}
	usable := len(asked) > 0
	for _, i := range asked {
		if a, ok := resp.Answers[results[i].Name]; !ok || a.Type != jev.TypeNoul {
			usable = false
		}
	}
	out := results
	if usable {
		out = applyValueCheck(results, asked, resp)
	}
	return out, documentTypeVerdict(resp)
}
