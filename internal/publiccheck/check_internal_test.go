package publiccheck

import (
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

func TestCheckFields_MapsNameValueAndReason(t *testing.T) {
	v := "INV-1"
	in := []extraction.FieldResult{
		{Field: extraction.Field{Name: "invoice_number", Value: &v}},
		{Field: extraction.Field{Name: "total", Reason: extraction.ReasonAmbiguous}},
		{Field: extraction.Field{Name: "document_text_layer", Reason: extraction.ReasonUnreadable}},
	}
	got := checkFields(in)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Name != "invoice_number" || got[0].Value != &v || got[0].Reason != nil {
		t.Errorf("field 0 = %+v, want value kept and Reason nil", got[0])
	}
	for i, want := range map[int]string{1: "ambiguous", 2: "unreadable"} {
		if got[i].Value != nil || got[i].Reason == nil || *got[i].Reason != want {
			t.Errorf("field %d = %+v, want reason %q", i, got[i], want)
		}
	}
	if got[2].Name != "document_text_layer" {
		t.Errorf("marker name = %q, want document_text_layer", got[2].Name)
	}
}
