package invoice

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// Each UpdateInput field set alone passes both guards; the zero value fails both.
func TestUpdateInput_EveryFieldPassesBothGuards(t *testing.T) {
	typ := reflect.TypeOf(UpdateInput{})
	if typ.NumField() < 31 {
		t.Fatalf("UpdateInput has %d fields, want at least 31", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			var in UpdateInput
			f := reflect.ValueOf(&in).Elem().Field(i)
			switch f.Type() {
			case reflect.TypeOf((*string)(nil)):
				s := "x"
				f.Set(reflect.ValueOf(&s))
			case reflect.TypeOf((*time.Time)(nil)):
				d := time.Now()
				f.Set(reflect.ValueOf(&d))
			default:
				t.Fatalf("field %s has unhandled type %s", name, f.Type())
			}
			if !headerFieldsPresent(in) {
				t.Errorf("headerFieldsPresent is false with only %s set", name)
			}
			if _, err := NewStore(nil).Update(t.Context(), "id", in); !errors.Is(err, db.ErrNoTenant) {
				t.Errorf("Update with only %s set = %v, want db.ErrNoTenant (past the inline guard)", name, err)
			}
		})
	}
	if headerFieldsPresent(UpdateInput{}) {
		t.Error("headerFieldsPresent is true for the zero UpdateInput")
	}
	if _, err := NewStore(nil).Update(t.Context(), "id", UpdateInput{}); !errors.Is(err, ErrValidation) {
		t.Errorf("Update(zero) = %v, want ErrValidation", err)
	}
}
