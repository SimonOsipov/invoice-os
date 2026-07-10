package portfolio

import "errors"

// ErrInvalidTIN is returned when a TIN fails structural, format, or checksum
// validation. Callers should use errors.Is to detect it; the wrapped reason
// (via fmt.Errorf("%w: ...", ErrInvalidTIN)) is diagnostic only.
var ErrInvalidTIN = errors.New("portfolio: invalid tin")

// ValidateTIN validates a Nigerian Tax Identification Number and returns its
// canonical (digits-only, hyphen-stripped) form on success.
//
// TODO(M3-03-01): not yet implemented -- see internal/portfolio/tin_test.go
// for the acceptance-criteria tests this must satisfy.
func ValidateTIN(raw string) (string, error) {
	return "", errors.New("not implemented: M3-03-01")
}
