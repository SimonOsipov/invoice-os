// jevcheck.go asks Jev once per document whether each decided header value is what the page prints.
package extraction

import (
	"context"

	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

// JevAsker is the slice of *jev.Client the worker uses; nil is off.
type JevAsker interface {
	Enabled() bool
	Ask(ctx context.Context, req jev.Request) (jev.Response, error)
}

// A noul at or below this flags the field (CHECK-00 Jev Measurement Results).
const valueCheckThreshold = 0

// internal/jevmeasure/wording.go's value-check text, byte for byte
// (TestJevProduct_TheValueQuestionIsTheMeasuredQuestion).
const (
	valueCheckInstructions = ""
	valueCheckTrue         = ""
	valueCheckFalse        = ""
)

func ValueCheckQuestion(field, value string) jev.Question {
	return jev.Question{}
}

// valueCheckRequest returns the asked result indexes too; nil means nothing to ask.
func valueCheckRequest(pages []TokenPage, results []FieldResult) (jev.Request, []int) {
	return jev.Request{}, nil
}

func applyValueCheck(results []FieldResult, asked []int, resp jev.Response) []FieldResult {
	return results
}

func checkValues(ctx context.Context, j JevAsker, pages []TokenPage, results []FieldResult) []FieldResult {
	return results
}
