package portfolio

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidTIN is returned when a TIN fails structural, format, or checksum
// validation. Callers should use errors.Is to detect it.
var ErrInvalidTIN = errors.New("portfolio: invalid tin")

// TINError names why ValidateTIN refused a TIN; errors.Is(err, ErrInvalidTIN) holds.
type TINError struct{ Reason string }

func (e *TINError) Error() string        { return ErrInvalidTIN.Error() + ": " + e.Reason }
func (e *TINError) Is(target error) bool { return target == ErrInvalidTIN }

// Refusal reasons, as sent on the wire. TINLengthMessage takes the digit count.
const (
	TINRequiredMessage = "Enter the TIN."
	TINShapeMessage    = "A TIN is digits only. Only the 12-digit FIRS TIN takes a hyphen, after the 8th digit: ########-####."
	TINLengthMessage   = "A TIN has 10 digits (JTB) or 12 digits (FIRS). This one has %d."
	TINChecksumMessage = "This TIN's last digit is a check digit, and it does not match the other digits. Check the number on the tax certificate."
)

// ValidateTIN validates a Nigerian Tax Identification Number and returns its
// canonical (digits-only, hyphen-stripped) form on success.
//
// Accepted: a bare 10-digit JTB TIN, a bare 12-digit FIRS TIN, or an 8+4
// hyphenated FIRS TIN, passing Luhn (well-formedness only, story Decision
// [A1]). Every refusal is a *TINError of one class: required, shape, length
// or check digit.
func ValidateTIN(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", &TINError{Reason: TINRequiredMessage}
	}

	digits := 0
	for _, r := range trimmed {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '-':
		default:
			return "", &TINError{Reason: TINShapeMessage}
		}
	}
	if digits != 10 && digits != 12 {
		return "", &TINError{Reason: fmt.Sprintf(TINLengthMessage, digits)}
	}

	if hyphens := len(trimmed) - digits; hyphens > 1 || (hyphens == 1 && (digits != 12 || trimmed[8] != '-')) {
		return "", &TINError{Reason: TINShapeMessage}
	}

	canonical := strings.Replace(trimmed, "-", "", 1)
	if !luhnValid(canonical) {
		return "", &TINError{Reason: TINChecksumMessage}
	}
	return canonical, nil
}

// luhnValid reports whether digits (a string of ASCII digits) passes the
// Luhn (mod-10) checksum: from the rightmost digit, double every second
// digit, subtracting 9 if the result exceeds 9, then sum all digits; valid
// iff the sum is a multiple of 10.
func luhnValid(digits string) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}
