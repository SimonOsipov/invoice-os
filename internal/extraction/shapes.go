// shapes.go: the six value normalisers behind Shape. Stage 2.5 stub -- bodies land in Stage 3.
package extraction

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Normalize returns every reading of raw this shape accepts: zero when it rejects raw (a
// non-match is not a fault), one normally, two for an ambiguous numeric date. Pure -- no
// clock, no map iteration, no locale.
func (s Shape) Normalize(raw string) []string {
	switch s {
	case ShapeInvoiceNumber:
		return normalizeInvoiceNumber(raw)
	case ShapeDate:
		return normalizeDate(raw)
	case ShapeAmount:
		return normalizeAmount(raw)
	case ShapeTIN:
		return normalizeTIN(raw)
	case ShapeCurrency:
		return normalizeCurrency(raw)
	case ShapeName:
		return normalizeName(raw)
	default:
		return nil
	}
}

// TODO(EXTR-04-02, Stage 3): body.
func normalizeInvoiceNumber(raw string) []string {
	return nil
}

// TODO(EXTR-04-02, Stage 3): body.
func normalizeDate(raw string) []string {
	return nil
}

// TODO(EXTR-04-02, Stage 3): body.
func normalizeAmount(raw string) []string {
	return nil
}

// TODO(EXTR-04-02, Stage 3): body.
func normalizeTIN(raw string) []string {
	return nil
}

// TODO(EXTR-04-02, Stage 3): body.
func normalizeCurrency(raw string) []string {
	return nil
}

// TODO(EXTR-04-02, Stage 3): body.
func normalizeName(raw string) []string {
	return nil
}

// silence unused-import until Stage 3 fills the bodies above.
var _ = fmt.Sprintf
var _ = regexp.MustCompile
var _ = strings.TrimSpace
var _ = time.Parse
var _ = utf8.RuneCountInString
