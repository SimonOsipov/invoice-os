// shapes_test.go: S-01..S-13 (EXTR-04-02 AC). External package: every spec reaches only
// exported symbols -- the six Shape constants and the Normalize method.
package extraction_test

import (
	"reflect"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// S-01: whitespace around the separator is stripped; the canonical form is emitted verbatim.
func TestShapeTIN_AcceptsTheShippedFormat(t *testing.T) {
	for _, raw := range []string{"12345678-9012", " 12345678 - 9012 "} {
		if got, want := extraction.ShapeTIN.Normalize(raw), []string{"12345678-9012"}; !reflect.DeepEqual(got, want) {
			t.Errorf("ShapeTIN.Normalize(%q) = %v, want %v", raw, got, want)
		}
	}
}

// S-02: a digit count other than 8-4 is rejected. Paired with a valid TIN so a nil-returning
// stub cannot satisfy this test on the reject cases alone.
func TestShapeTIN_RejectsAWrongDigitCount(t *testing.T) {
	for _, raw := range []string{"1234567-9012", "123456789-9012", "12345678-901"} {
		if got := extraction.ShapeTIN.Normalize(raw); len(got) != 0 {
			t.Errorf("ShapeTIN.Normalize(%q) = %v, want an empty slice", raw, got)
		}
	}
	if got, want := extraction.ShapeTIN.Normalize("12345678-9012"), []string{"12345678-9012"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeTIN.Normalize(%q) = %v, want %v", "12345678-9012", got, want)
	}
}

// S-03: currency prefix and thousands grouping are stripped; the fraction is preserved
// as-is, never zero-padded.
func TestShapeAmount_StripsCurrencyAndGrouping(t *testing.T) {
	cases := map[string]string{
		"NGN 1,500.00": "1500.00",
		"₦1,500.00":    "1500.00",
		"1500":         "1500",
	}
	for raw, want := range cases {
		if got := extraction.ShapeAmount.Normalize(raw); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("ShapeAmount.Normalize(%q) = %v, want [%q]", raw, got, want)
		}
	}
}

// S-04: a third fraction digit is a reject, never a round -- the numeric(14,2) column scale.
// Paired with a valid amount so a nil-returning stub cannot satisfy this test alone.
func TestShapeAmount_RejectsThreeFractionDigits(t *testing.T) {
	if got := extraction.ShapeAmount.Normalize("1,500.000"); len(got) != 0 {
		t.Errorf("ShapeAmount.Normalize(%q) = %v, want an empty slice", "1,500.000", got)
	}
	if got, want := extraction.ShapeAmount.Normalize("1500.00"), []string{"1500.00"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeAmount.Normalize(%q) = %v, want %v", "1500.00", got, want)
	}
}

// S-05: every unambiguous accepted layout emits ISO 2006-01-02.
func TestShapeDate_NormalisesEveryAcceptedLayout(t *testing.T) {
	for _, raw := range []string{"2026-03-12", "12 Mar 2026", "Mar 12, 2026"} {
		if got, want := extraction.ShapeDate.Normalize(raw), []string{"2026-03-12"}; !reflect.DeepEqual(got, want) {
			t.Errorf("ShapeDate.Normalize(%q) = %v, want %v", raw, got, want)
		}
	}
}

// S-06: an ambiguous NN/NN/YYYY returns both readings, day-first at index 0, month-first at
// index 1 -- a fixed position, not a value sort (D-10).
func TestShapeDate_ReturnsBothReadingsOfAnAmbiguousNumericDate(t *testing.T) {
	got := extraction.ShapeDate.Normalize("12/03/2026")
	want := []string{"2026-03-12", "2026-12-03"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeDate.Normalize(%q) = %v, want %v in this exact order", "12/03/2026", got, want)
	}
}

// S-07: a day over 12 rules out the month-first reading, so exactly one candidate comes back.
func TestShapeDate_ReturnsOneReadingWhenTheDayExceedsTwelve(t *testing.T) {
	if got, want := extraction.ShapeDate.Normalize("25/03/2026"), []string{"2026-03-25"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeDate.Normalize(%q) = %v, want %v", "25/03/2026", got, want)
	}
}

// S-08: a date impossible under both readings rejects with no candidate at all. Paired with a
// valid date so a nil-returning stub cannot satisfy this test alone.
func TestShapeDate_RejectsAnImpossibleDate(t *testing.T) {
	if got := extraction.ShapeDate.Normalize("31/02/2026"); len(got) != 0 {
		t.Errorf("ShapeDate.Normalize(%q) = %v, want an empty slice", "31/02/2026", got)
	}
	if got, want := extraction.ShapeDate.Normalize("2026-03-12"), []string{"2026-03-12"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeDate.Normalize(%q) = %v, want %v", "2026-03-12", got, want)
	}
}

// S-09: a three-letter code is upper-cased; the naira sign maps to NGN.
func TestShapeCurrency_UpperCasesAndMapsTheNaira(t *testing.T) {
	for raw, want := range map[string]string{"ngn": "NGN", "₦": "NGN"} {
		if got := extraction.ShapeCurrency.Normalize(raw); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("ShapeCurrency.Normalize(%q) = %v, want [%q]", raw, got, want)
		}
	}
}

// S-10: a value that is only a date or only an amount is not an invoice number (AC #6).
// Paired with a typical identifier so a nil-returning stub cannot satisfy this test alone.
func TestShapeInvoiceNumber_RejectsABareDateOrAmount(t *testing.T) {
	for _, raw := range []string{"2026-03-12", "1,500.00"} {
		if got := extraction.ShapeInvoiceNumber.Normalize(raw); len(got) != 0 {
			t.Errorf("ShapeInvoiceNumber.Normalize(%q) = %v, want an empty slice", raw, got)
		}
	}
	if got, want := extraction.ShapeInvoiceNumber.Normalize("INV-001"), []string{"INV-001"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeInvoiceNumber.Normalize(%q) = %v, want %v", "INV-001", got, want)
	}
}

// S-11: typical identifiers pass through unchanged.
func TestShapeInvoiceNumber_AcceptsTypicalIdentifiers(t *testing.T) {
	for _, raw := range []string{"INV-001", "2026/INV/0042", "A1"} {
		if got, want := extraction.ShapeInvoiceNumber.Normalize(raw), []string{raw}; !reflect.DeepEqual(got, want) {
			t.Errorf("ShapeInvoiceNumber.Normalize(%q) = %v, want %v", raw, got, want)
		}
	}
}

// S-12: whitespace is trimmed; a name over 256 runes (counted on the trimmed string) rejects.
func TestShapeName_TrimsAndBoundsLength(t *testing.T) {
	if got, want := extraction.ShapeName.Normalize("  Honeywell Group  "), []string{"Honeywell Group"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeName.Normalize(%q) = %v, want %v", "  Honeywell Group  ", got, want)
	}

	oversize := ""
	for i := 0; i < 257; i++ {
		oversize += "a"
	}
	if got := extraction.ShapeName.Normalize(oversize); len(got) != 0 {
		t.Errorf("ShapeName.Normalize(<257 runes>) = %v, want an empty slice", got)
	}
}

// S-13: calling Normalize twice on the same input yields byte-identical slices. Normalize
// dispatches with a switch (shapes.go), never a map, so nothing here is order-dependent --
// this test exists to catch a future regression, not this stub. The len(got1) > 0 check on
// accepting fixtures matters: two nil slices are trivially reflect.DeepEqual, so a rejection
// pair alone could not tell a working normaliser from an unimplemented one.
func TestShapes_AreDeterministicAcrossCalls(t *testing.T) {
	type fixture struct {
		shape   extraction.Shape
		raw     string
		accepts bool
	}
	fixtures := []fixture{
		{extraction.ShapeTIN, "12345678-9012", true},
		{extraction.ShapeTIN, "1234567-9012", false},
		{extraction.ShapeAmount, "NGN 1,500.00", true},
		{extraction.ShapeAmount, "1,500.000", false},
		{extraction.ShapeDate, "2026-03-12", true},
		{extraction.ShapeDate, "31/02/2026", false},
		{extraction.ShapeDate, "12/03/2026", true},
		{extraction.ShapeCurrency, "ngn", true},
		{extraction.ShapeCurrency, "ngnx", false},
		{extraction.ShapeInvoiceNumber, "INV-001", true},
		{extraction.ShapeInvoiceNumber, "2026-03-12", false},
		{extraction.ShapeName, "  Honeywell Group  ", true},
	}

	for _, f := range fixtures {
		got1 := f.shape.Normalize(f.raw)
		got2 := f.shape.Normalize(f.raw)
		if !reflect.DeepEqual(got1, got2) {
			t.Errorf("%s.Normalize(%q) not deterministic: first call %v, second call %v", f.shape, f.raw, got1, got2)
		}
		if f.accepts && len(got1) == 0 {
			t.Errorf("%s.Normalize(%q) = %v, want a non-empty slice", f.shape, f.raw, got1)
		}
	}
}

// --- EXTR-16-02: the label/value split (D-B) ---------------------------------

// EXTR-16-02 AC-2. A trimmed value that one anchor-lexicon entry matches WHOLLY is the label
// that introduces a value, never the value. Paired with a real name so a nil-returning stub
// cannot satisfy this test on the reject cases alone.
func TestShapes_NameRejectsABareAnchorLabel(t *testing.T) {
	for _, raw := range []string{"Supplier", "Buyer", "TIN", "Currency"} {
		if got := extraction.ShapeName.Normalize(raw); len(got) != 0 {
			t.Errorf("ShapeName.Normalize(%q) = %v, want an empty slice: one anchor-lexicon entry matches the whole trimmed value, so it is a label", raw, got)
		}
	}
	if got, want := extraction.ShapeName.Normalize("Adeyemi Trading Limited"), []string{"Adeyemi Trading Limited"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeName.Normalize(%q) = %v, want %v", "Adeyemi Trading Limited", got, want)
	}
}

// EXTR-16-02 AC-2. Only a WHOLE-value match is rejected: a trading name that happens to open
// with a label word is still a name. "Supplier" matches [0,8] of the first and "Total" [0,5] of
// the second, and neither spans its value.
func TestShapes_NameKeepsANameThatMerelyContainsALabelWord(t *testing.T) {
	for _, raw := range []string{"Supplier Services Limited", "Total Logistics Limited"} {
		if got, want := extraction.ShapeName.Normalize(raw), []string{raw}; !reflect.DeepEqual(got, want) {
			t.Errorf("ShapeName.Normalize(%q) = %v, want %v", raw, got, want)
		}
	}
}

// EXTR-16-02 AC-2. The same split for the invoice number, which today rejects only a date-only
// and an amount-only value. Paired with a real identifier so a nil-returning stub cannot satisfy
// this test alone.
func TestShapes_InvoiceNumberRejectsABareAnchorLabel(t *testing.T) {
	if got := extraction.ShapeInvoiceNumber.Normalize("Supplier"); len(got) != 0 {
		t.Errorf("ShapeInvoiceNumber.Normalize(%q) = %v, want an empty slice: the supplier_name lexicon entry matches the whole value", "Supplier", got)
	}
	if got, want := extraction.ShapeInvoiceNumber.Normalize("INV-1002"), []string{"INV-1002"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ShapeInvoiceNumber.Normalize(%q) = %v, want %v", "INV-1002", got, want)
	}
}
