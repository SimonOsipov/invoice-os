package portfolio

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidTIN is returned when a TIN fails structural, format, or checksum
// validation. Callers should use errors.Is to detect it; the wrapped reason
// (via fmt.Errorf("%w: ...", ErrInvalidTIN)) is diagnostic only.
var ErrInvalidTIN = errors.New("portfolio: invalid tin")

// tinShapePattern matches the accepted TIN shapes: a bare 10-digit JTB TIN,
// a bare 12-digit FIRS TIN, or an 8+4 hyphenated FIRS TIN (NNNNNNNN-NNNN) --
// the hyphenated and plain-digit spellings of a FIRS TIN both canonicalize to
// the same 12-digit form.
var tinShapePattern = regexp.MustCompile(`^(\d{10}|\d{12}|\d{8}-\d{4})$`)

// ValidateTIN validates a Nigerian Tax Identification Number and returns its
// canonical (digits-only, hyphen-stripped) form on success.
//
// Accepted shapes: a bare 10-digit JTB TIN, a bare 12-digit FIRS TIN, or an
// 8+4 hyphenated FIRS TIN (NNNNNNNN-NNNN). The canonical form strips any
// hyphen, so both spellings of a FIRS TIN persist identically. The canonical digits must also pass a
// Luhn (mod-10) checksum, which proves well-formedness only -- it is not a
// FIRS/JTB registry authenticity check (see story Decision [A1]).
func ValidateTIN(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("%w: tin is required", ErrInvalidTIN)
	}

	if !tinShapePattern.MatchString(trimmed) {
		return "", fmt.Errorf("%w: tin must be a 10-digit JTB TIN or an 8+4 FIRS TIN NNNNNNNN-NNNN", ErrInvalidTIN)
	}

	canonical := strings.Replace(trimmed, "-", "", 1)

	if !luhnValid(canonical) {
		return "", fmt.Errorf("%w: tin checksum is invalid", ErrInvalidTIN)
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
