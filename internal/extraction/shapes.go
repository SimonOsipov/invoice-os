// shapes.go: the six value normalisers behind Shape. Each output is exactly what the matching
// invoices column wants, so the writer needs no second parser.
package extraction

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// isoDate is both the accepted ISO input layout and the only output format.
const isoDate = "2006-01-02"

// maxNameRunes bounds ShapeName. supplier_name is bare text with no DB cap; this is the
// shape's own bound.
const maxNameRunes = 256

var (
	// Matches seed_mbs_v1.sql's ^[0-9]{8}-[0-9]{4}$ once the interior whitespace is dropped.
	reTIN = regexp.MustCompile(`^\s*([0-9]{8})\s*-\s*([0-9]{4})\s*$`)
	// Grouping is strict, so 1,50.00 rejects. The fraction caps at two digits because
	// numeric(14,2) has no third one to hold -- a longer fraction rejects rather than rounds.
	reAmount   = regexp.MustCompile(`^\s*(?:NGN|₦|N)?\s*(-?)([0-9]{1,3}(?:,[0-9]{3})+|[0-9]+)(?:\.([0-9]{1,2}))?\s*$`)
	reCurrency = regexp.MustCompile(`^\s*([A-Za-z]{3})\s*$`)
	reNaira    = regexp.MustCompile(`^\s*₦\s*$`)
	reInvNum   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9/_.-]{0,63}$`)
	// One per separator: a mixed pair such as 12/03-2026 matches none and rejects.
	reNumDateSlash = regexp.MustCompile(`^\s*([0-9]{1,2})/([0-9]{1,2})/([0-9]{4})\s*$`)
	reNumDateDash  = regexp.MustCompile(`^\s*([0-9]{1,2})-([0-9]{1,2})-([0-9]{4})\s*$`)
	reNumDateDot   = regexp.MustCompile(`^\s*([0-9]{1,2})\.([0-9]{1,2})\.([0-9]{4})\s*$`)
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

func normalizeTIN(raw string) []string {
	m := reTIN.FindStringSubmatch(raw)
	if m == nil {
		return nil
	}
	return append([]string(nil), m[1]+"-"+m[2])
}

// normalizeAmount validates the digit shape textually and never converts to a float, so a
// third fraction digit rejects instead of rounding away.
func normalizeAmount(raw string) []string {
	m := reAmount.FindStringSubmatch(raw)
	if m == nil {
		return nil
	}
	out := m[1] + strings.ReplaceAll(m[2], ",", "")
	if m[3] != "" {
		out += "." + m[3]
	}
	return append([]string(nil), out)
}

// normalizeDate tries the layouts in a fixed order; the first structural match decides, with
// no fallthrough once a numeric regexp has claimed the input. No two-digit-year layout is
// accepted: that is the only clock-dependent construct time.Parse offers.
func normalizeDate(raw string) []string {
	trimmed := strings.TrimSpace(raw)

	if t, err := time.Parse(isoDate, trimmed); err == nil {
		return append([]string(nil), t.Format(isoDate))
	}
	for _, re := range []*regexp.Regexp{reNumDateSlash, reNumDateDash, reNumDateDot} {
		if m := re.FindStringSubmatch(trimmed); m != nil {
			return numericDateReadings(m[1], m[2], m[3])
		}
	}
	for _, layout := range []string{"02 Jan 2006", "Jan 02, 2006"} {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return append([]string(nil), t.Format(isoDate))
		}
	}
	return nil
}

// numericDateReadings returns the readings of an NN/NN/YYYY date in a FIXED positional order:
// index 0 is day-first, index 1 is month-first, appended only when it parses and differs.
// Never value-sorted -- the resolver applies its own total order later, a different layer.
// Both range checks are time.Parse's own, so an impossible date needs no calendar code here.
func numericDateReadings(a, b, year string) []string {
	// The regexps allow one digit per component; the 02/01 layouts are fixed-width.
	canon := pad2(a) + "/" + pad2(b) + "/" + year

	var out []string
	if t, err := time.Parse("02/01/2006", canon); err == nil {
		out = append(out, t.Format(isoDate))
	}
	if t, err := time.Parse("01/02/2006", canon); err == nil {
		iso := t.Format(isoDate)
		if len(out) == 0 || out[0] != iso {
			out = append(out, iso)
		}
	}
	return out
}

func pad2(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

// normalizeCurrency upper-cases because enumEval compares the seeded {"values":["NGN"]} with
// reflect.DeepEqual -- an exact, case-sensitive match.
func normalizeCurrency(raw string) []string {
	if m := reCurrency.FindStringSubmatch(raw); m != nil {
		return append([]string(nil), strings.ToUpper(m[1]))
	}
	if reNaira.MatchString(raw) {
		return append([]string(nil), "NGN")
	}
	return nil
}

// normalizeInvoiceNumber rejects a value that is only a date, only an amount, or only an anchor
// label. The amount guard earns its place separately: 1,500.00 already fails reInvNum, but 1500
// passes it.
func normalizeInvoiceNumber(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if !reInvNum.MatchString(trimmed) {
		return nil
	}
	if len(normalizeDate(trimmed)) > 0 || len(normalizeAmount(trimmed)) > 0 || isBareAnchorLabel(trimmed) {
		return nil
	}
	return append([]string(nil), trimmed)
}

func normalizeName(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > maxNameRunes {
		return nil
	}
	if isBareAnchorLabel(trimmed) {
		return nil
	}
	return append([]string(nil), trimmed)
}

// isBareAnchorLabel reports whether one anchor-lexicon entry matches value whole. Such a value
// is the label that introduces a value, never the value. The match must span value, so a
// trading name that merely opens with a label word ("Supplier Services Limited") survives.
func isBareAnchorLabel(value string) bool {
	for _, m := range anchorLabelMatchers {
		if loc := m.RE.FindStringIndex(value); loc != nil && loc[0] == 0 && loc[1] == len(value) {
			return true
		}
	}
	return false
}
