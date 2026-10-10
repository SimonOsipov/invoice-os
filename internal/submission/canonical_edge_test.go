// QA Mode B (task-217): adversarial / edge coverage added AFTER canonical_test.go's Stage 2.5
// regression guard went green. Package submission_test (external), matching every other test
// file in this package. TestMain already exists at failure_modes_test.go:57, so this file
// defines none. No testify; standard library only (reflect, testing).
package submission_test

import (
	"reflect"
	"testing"
	"time"

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
	for _, field := range []string{"Quantity", "UnitPrice", "LineTotal", "LineTax", "TaxPercent", "BaseQuantity"} {
		assertStringPointerField(t, line, field)
	}

	sub := reflect.TypeOf(submission.TaxSubtotal{})
	for _, field := range []string{"Percent", "TaxableAmount", "TaxAmount"} {
		assertStringPointerField(t, sub, field)
	}
}

func TestCanonical_NoFieldAnywhereIsNumeric(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(typ reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || typ == reflect.TypeOf(time.Time{}) || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			ft := f.Type
			for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
				ft = ft.Elem()
			}
			switch ft.Kind() {
			case reflect.Float32, reflect.Float64, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				t.Errorf("%s.%s is %s: decimals are text, never numeric", typ.Name(), f.Name, ft.Kind())
			}
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(submission.Canonical{}))
	if len(seen) != 4 {
		t.Errorf("walked %d struct types, want 4", len(seen))
	}
}
