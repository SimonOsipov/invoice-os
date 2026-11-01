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
