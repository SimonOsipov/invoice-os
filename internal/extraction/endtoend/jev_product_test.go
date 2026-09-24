// jev_product_test.go: the product's value and document-type questions are the ones CHECK-01's harness measured.
package endtoend

import (
	"slices"
	"strings"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/jevmeasure"
	"github.com/SimonOsipov/invoice-os/internal/platform/jev"
)

func TestJevProduct_TheValueQuestionIsTheMeasuredQuestion(t *testing.T) {
	fields := []string{"invoice_number", "issue_date", "buyer_tin", "buyer_name", "currency", "subtotal", "vat", "total"}
	const value = "INV 2026.001"

	for _, f := range fields {
		got := extraction.ValueCheckQuestion(f, value)
		want := jvValueQuestion(f, value)

		if want.Instructions == "" || want.Criteria["true"] == "" || want.Criteria["false"] == "" {
			t.Fatalf("%s: the harness question has an empty part: %+v", f, want)
		}
		if got.Instructions != want.Instructions {
			t.Errorf("%s: Instructions\ngot:  %q\nwant: %q", f, got.Instructions, want.Instructions)
		}
		if got.True != want.Criteria["true"] {
			t.Errorf("%s: True\ngot:  %q\nwant: %q", f, got.True, want.Criteria["true"])
		}
		if got.False != want.Criteria["false"] {
			t.Errorf("%s: False\ngot:  %q\nwant: %q", f, got.False, want.Criteria["false"])
		}
		if string(got.Type) != want.Type || got.Type != jev.TypeNoul {
			t.Errorf("%s: Type = %q, want %q", f, got.Type, want.Type)
		}
		// The measured wording states the normalisation.
		if !strings.Contains(got.Instructions, "normalised form") {
			t.Errorf("%s: Instructions %q do not carry the measured wording", f, got.Instructions)
		}
		// Control: the comparison sees the value.
		if extraction.ValueCheckQuestion(f, value+"9").Instructions == want.Instructions {
			t.Errorf("%s: a changed value composes the same instructions", f)
		}
	}
}

func TestJevProduct_TheDocumentTypeQuestionIsTheMeasuredQuestion(t *testing.T) {
	wantNames := jevmeasure.DocumentTypeOptions()
	if len(jevmeasure.DocumentTypeCriteria) != 8 || len(wantNames) != 8 || jevmeasure.DocumentTypeInstructions == "" {
		t.Fatalf("the harness question is not eight described options: %d criteria, %d names", len(jevmeasure.DocumentTypeCriteria), len(wantNames))
	}

	got := extraction.DocumentTypeQuestion()

	if got.Instructions != jevmeasure.DocumentTypeInstructions {
		t.Errorf("Instructions\ngot:  %q\nwant: %q", got.Instructions, jevmeasure.DocumentTypeInstructions)
	}
	if string(got.Type) != jevmeasure.QuestionTypeChoice || got.Type != jev.TypeChoice {
		t.Errorf("Type = %q, want %q", got.Type, jevmeasure.QuestionTypeChoice)
	}
	gotNames := make([]string, len(got.Options))
	for i, o := range got.Options {
		gotNames[i] = o.Name
		if want := jevmeasure.DocumentTypeCriteria[o.Name]; want == "" || o.Description != want {
			t.Errorf("%s: Description\ngot:  %q\nwant: %q", o.Name, o.Description, want)
		}
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Errorf("option names = %v, want %v", gotNames, wantNames)
	}
}
