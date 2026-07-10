package portfolio

import (
	"errors"
	"testing"
)

// tinCase is one row of a ValidateTIN acceptance table. wantCanon is only
// checked when wantValid is true.
type tinCase struct {
	name      string
	raw       string
	wantValid bool
	wantCanon string
}

func runTINCases(t *testing.T, cases []tinCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical, err := ValidateTIN(tc.raw)

			if tc.wantValid {
				if err != nil {
					t.Fatalf("ValidateTIN(%q) unexpected error: %v", tc.raw, err)
				}
				if canonical != tc.wantCanon {
					t.Errorf("ValidateTIN(%q) canonical = %q, want %q", tc.raw, canonical, tc.wantCanon)
				}
				return
			}

			if !errors.Is(err, ErrInvalidTIN) {
				t.Fatalf("ValidateTIN(%q) err = %v, want errors.Is(err, ErrInvalidTIN)", tc.raw, err)
			}
			if canonical != "" {
				t.Errorf("ValidateTIN(%q) canonical = %q, want empty string on failure", tc.raw, canonical)
			}
		})
	}
}

// TestValidateTIN_JTB10Valid: a bare 10-digit JTB-issued TIN with a passing
// Luhn checksum is accepted and returned unchanged as the canonical form.
func TestValidateTIN_JTB10Valid(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "10-digit JTB TIN", raw: "1234567897", wantValid: true, wantCanon: "1234567897"},
	})
}

// TestValidateTIN_FIRSDashValid: the FIRS dash-formatted TIN (8 digits, "-",
// 4 digits) is accepted and canonicalized to its 12-digit hyphen-free form.
func TestValidateTIN_FIRSDashValid(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "FIRS dash-formatted TIN", raw: "12345678-0006", wantValid: true, wantCanon: "123456780006"},
	})
}

// TestValidateTIN_ChecksumRejected: input that is structurally valid (10
// digits) but fails the Luhn (mod-10) checksum must be rejected.
func TestValidateTIN_ChecksumRejected(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "format-valid but Luhn checksum fails", raw: "1234567890", wantValid: false},
	})
}

// TestValidateTIN_TooShort: fewer than 10 digits never matches either
// accepted shape and is rejected before checksum is even considered.
func TestValidateTIN_TooShort(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "5-digit input", raw: "12345", wantValid: false},
	})
}

// TestValidateTIN_NonDigit: letters embedded among digits fail both the
// 10-digit and 8-4 dash shapes.
func TestValidateTIN_NonDigit(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "letters embedded in digits", raw: "12ab567897", wantValid: false},
	})
}

// TestValidateTIN_TooLong: more than 12 digits never matches either accepted
// shape.
func TestValidateTIN_TooLong(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "13-digit input", raw: "1234567890123", wantValid: false},
	})
}

// TestValidateTIN_Empty: an empty or whitespace-only TIN is rejected as
// required, prior to any format check.
func TestValidateTIN_Empty(t *testing.T) {
	runTINCases(t, []tinCase{
		{name: "empty string", raw: "", wantValid: false},
		{name: "whitespace only", raw: "  ", wantValid: false},
	})
}

// TestValidateTIN_CanonicalDedup: the dash-formatted and plain-digit spellings
// of the same TIN must canonicalize to the identical persisted string, so
// table-level uniqueness (enforced elsewhere) actually catches the duplicate.
func TestValidateTIN_CanonicalDedup(t *testing.T) {
	const want = "123456780006"

	dashCanonical, dashErr := ValidateTIN("12345678-0006")
	if dashErr != nil {
		t.Fatalf("ValidateTIN(%q) unexpected error: %v", "12345678-0006", dashErr)
	}
	if dashCanonical != want {
		t.Errorf("ValidateTIN(%q) canonical = %q, want %q", "12345678-0006", dashCanonical, want)
	}

	plainCanonical, plainErr := ValidateTIN("123456780006")
	if plainErr != nil {
		t.Fatalf("ValidateTIN(%q) unexpected error: %v", "123456780006", plainErr)
	}
	if plainCanonical != want {
		t.Errorf("ValidateTIN(%q) canonical = %q, want %q", "123456780006", plainCanonical, want)
	}

	if dashCanonical != plainCanonical {
		t.Errorf("canonical mismatch: dash form = %q, plain form = %q, want identical", dashCanonical, plainCanonical)
	}
}
