// QA Mode B (task-217): adversarial / edge coverage added AFTER canonical_test.go's Stage 2.5
// regression guard went green. Package submission_test (external), matching every other test
// file in this package. TestMain already exists at failure_modes_test.go:57, so this file
// defines none. No testify; standard library only (reflect, testing).
package submission_test

import (
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestCanonical_MoneyFieldsAreStringPointersNeverNumeric ([D13], adversarial coverage): every
// money-shaped field on Canonical and CanonicalLine must be *string, never a numeric Go type.
// This guards against exactly the regression [D13] exists to prevent -- a future "helpful"
// refactor swapping *string for float64 (or int, or json.Number) on a money field, which would
// compile fine but reintroduce binary-float rounding into currency values the system depends
// on being exact decimal text end to end.
func TestCanonical_MoneyFieldsAreStringPointersNeverNumeric(t *testing.T) {
	assertStringPointerField := func(t *testing.T, typ reflect.Type, fieldName string) {
		t.Helper()
		f, ok := typ.FieldByName(fieldName)
		if !ok {
			t.Errorf("%s has no field %q", typ.Name(), fieldName)
			return
		}
		if f.Type.Kind() != reflect.Ptr {
			t.Errorf("%s.%s has kind %s, want Ptr (money fields are *string per [D13])",
				typ.Name(), fieldName, f.Type.Kind())
			return
		}
		if f.Type.Elem().Kind() != reflect.String {
			t.Errorf("%s.%s is a pointer to %s, want a pointer to string ([D13]: money is "+
				"::text-read decimal string, never a numeric type)",
				typ.Name(), fieldName, f.Type.Elem().Kind())
		}
	}

	canonical := reflect.TypeOf(submission.Canonical{})
	for _, field := range []string{"Subtotal", "VAT", "Total"} {
		assertStringPointerField(t, canonical, field)
	}

	line := reflect.TypeOf(submission.CanonicalLine{})
	for _, field := range []string{"Quantity", "UnitPrice", "LineTotal", "LineTax"} {
		assertStringPointerField(t, line, field)
	}
}
