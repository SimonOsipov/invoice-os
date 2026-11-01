package importer

import (
	"strings"
	"testing"
)

func TestNotePositive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      *string
		want    *string
		wantErr string
	}{
		{"negative keeps its scale", sxPtr("-750.00"), sxPtr("750.00"), ""},
		{"positive is unchanged", sxPtr("750.00"), sxPtr("750.00"), ""},
		{"zero", sxPtr("0"), sxPtr("0"), ""},
		{"nil stays nil", nil, nil, ""},
		{"not a decimal names the value", sxPtr("abc"), nil, "abc"},
		{"padded negative is trimmed", sxPtr(" -750.00 "), sxPtr("750.00"), ""},
		{"negative with a long scale", sxPtr("-0.001"), sxPtr("0.001"), ""},
		{"negative whole number", sxPtr("-3"), sxPtr("3"), ""},
		{"negative exponent form", sxPtr("-1e3"), sxPtr("1000"), ""},
		{"a thousands separator is no amount", sxPtr("1,500.00"), nil, "1,500.00"},
		{"a negative thousands separator is no amount", sxPtr("-1,500.00"), nil, "-1,500.00"},
		{"a currency mark is no amount", sxPtr("\u20a6500.00"), nil, "\u20a6500.00"},
		{"a currency code is no amount", sxPtr("NGN 500.00"), nil, "NGN 500.00"},
		{"brackets are no amount", sxPtr("(750.00)"), nil, "(750.00)"},
		{"a unicode minus is no amount", sxPtr("\u2212750.00"), nil, "\u2212750.00"},
		{"an empty string is no amount", sxPtr(""), nil, `""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := notePositive(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("notePositive(%q) err = %v, want one naming %q", *tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("notePositive: %v", err)
			}
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("notePositive = %v, want %v", derefString(got), derefString(tc.want))
			}
		})
	}
}

// ImportDocumentNote swaps this suffix of the invoice path's date sentence for the note hand-off.
func TestReadingCreateInput_IssueDateSentenceEndsWithTheSuffixTheNotePathSwaps(t *testing.T) {
	_, rowErr := readingCreateInput("e", "d", SettledExtraction{Fields: []extractedField{{Name: "issue_date", Value: sxPtr("19 March")}}})
	if rowErr == nil || rowErr.Field != "issue_date" {
		t.Fatalf("rowErr = %+v, want one on issue_date", rowErr)
	}
	if !strings.HasSuffix(rowErr.Message, " Enter this invoice manually to carry on.") {
		t.Errorf("message = %q, want it to end with the suffix ImportDocumentNote swaps", rowErr.Message)
	}
}
