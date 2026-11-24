package erp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

var ErrUnmappable = errors.New("erp: document cannot be mapped")

// HashVersion versions the hashedContent projection; a stored hash of another version is never compared.
const HashVersion = 1

// hashedContent and hashedLine are the projection ContentHash covers. Field order is part of the hash.
type hashedContent struct {
	InvoiceNumber string
	IssueDate     string
	Currency      string
	BuyerName     string
	BuyerTIN      string
	BuyerEmail    string
	Subtotal      string
	VAT           string
	Total         string
	Lines         []hashedLine
}

type hashedLine struct {
	Description string
	Quantity    string
	UnitPrice   string
	TaxPercent  string
	LineTotal   string
	LineTax     string
}

// invoiceAmounts and lineAmounts are the stored values Differs compares against; nil is NULL.
type invoiceAmounts struct{ Subtotal, VAT, Total *string }

type lineAmounts struct{ Description, Quantity, UnitPrice, LineTotal, LineTax *string }

func unmappable(field string) error {
	return fmt.Errorf("%w: %s", ErrUnmappable, field)
}

func parseDec(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}

// round2 rounds half away from zero to two places.
func round2(r *big.Rat) *big.Rat {
	scaled := new(big.Rat).Mul(r, big.NewRat(100, 1))
	q, rem := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	// Compare 2*|rem| with the denominator; a tie moves away from zero.
	if new(big.Int).Mul(new(big.Int).Abs(rem), big.NewInt(2)).Cmp(scaled.Denom()) >= 0 {
		q.Add(q, big.NewInt(int64(scaled.Sign())))
	}
	return new(big.Rat).SetFrac(q, big.NewInt(100))
}

func ptr(s string) *string { return &s }

// canon is the value's exact decimal without trailing zeros, so 100 and 100.00 agree;
// an unparseable value stays as written.
func canon(s *string) string {
	if s == nil {
		return ""
	}
	r, ok := parseDec(*s)
	if !ok {
		return strings.TrimSpace(*s)
	}
	out := r.FloatString(10)
	if strings.Contains(out, ".") {
		out = strings.TrimRight(strings.TrimRight(out, "0"), ".")
	}
	return out
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ContentHash is the hex SHA-256 of the hashedContent projection of in.
func ContentHash(in invoice.CreateInput) string {
	h := hashedContent{
		InvoiceNumber: in.InvoiceNumber,
		Currency:      str(in.Currency),
		BuyerName:     str(in.BuyerName),
		BuyerTIN:      str(in.BuyerTIN),
		BuyerEmail:    str(in.BuyerEmail),
		Subtotal:      canon(in.Subtotal),
		VAT:           canon(in.VAT),
		Total:         canon(in.Total),
		Lines:         []hashedLine{},
	}
	if in.IssueDate != nil {
		h.IssueDate = in.IssueDate.Format("2006-01-02")
	}
	for _, l := range in.LineItems {
		h.Lines = append(h.Lines, hashedLine{
			Description: strings.TrimSpace(str(l.Description)),
			Quantity:    canon(l.Quantity),
			UnitPrice:   canon(l.UnitPrice),
			TaxPercent:  canon(l.TaxPercent),
			LineTotal:   canon(l.LineTotal),
			LineTax:     canon(l.LineTax),
		})
	}
	b, _ := json.Marshal(h) // plain strings only; cannot fail
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// decEqual compares as decimals; NULL equals only NULL.
func decEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, okx := parseDec(*a)
	y, oky := parseDec(*b)
	if !okx || !oky {
		return strings.TrimSpace(*a) == strings.TrimSpace(*b)
	}
	return x.Cmp(y) == 0
}

// Differs reports whether the mapped header amounts or any line differ from the stored invoice.
func Differs(in invoice.CreateInput, inv invoiceAmounts, lines []lineAmounts) bool {
	if !decEqual(in.Subtotal, inv.Subtotal) || !decEqual(in.VAT, inv.VAT) || !decEqual(in.Total, inv.Total) {
		return true
	}
	if len(in.LineItems) != len(lines) {
		return true
	}
	for i, l := range in.LineItems {
		s := lines[i]
		if strings.TrimSpace(str(l.Description)) != strings.TrimSpace(str(s.Description)) ||
			!decEqual(l.Quantity, s.Quantity) || !decEqual(l.UnitPrice, s.UnitPrice) ||
			!decEqual(l.LineTotal, s.LineTotal) || !decEqual(l.LineTax, s.LineTax) {
			return true
		}
	}
	return false
}
