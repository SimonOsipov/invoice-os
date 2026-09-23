// jevcheck.go asks Jev once per document whether each decided header value is what the page prints.
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

// checkValues returns results unchanged whenever the check cannot run in full.
func checkValues(ctx context.Context, j JevAsker, pages []TokenPage, results []FieldResult) []FieldResult {
	if j == nil || !j.Enabled() {
		return results
	}
	req, asked := valueCheckRequest(pages, results)
	if len(asked) == 0 {
		return results
	}
	resp, err := j.Ask(ctx, req)
	if err != nil {
		return results
	}
	for _, i := range asked {
		if a, ok := resp.Answers[results[i].Name]; !ok || a.Type != jev.TypeNoul {
			return results
		}
	}
	return applyValueCheck(results, asked, resp)
}
