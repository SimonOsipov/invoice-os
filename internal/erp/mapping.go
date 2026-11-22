package erp

import (
	"errors"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// Mapper turns one fetched vendor document and its party into a draft's input.
type Mapper func(doc Document, party *Party) (invoice.CreateInput, error)

var (
	ErrNoMapping  = errors.New("erp: no fixed mapping for this system")
	ErrUnmappable = errors.New("erp: document cannot be mapped")
)

// HashVersion is zero in this stub; the implementation sets it to 1.
const HashVersion = 0

type hashedContent struct{}

type hashedLine struct{}

type invoiceAmounts struct{ Subtotal, VAT, Total *string }

type lineAmounts struct{ Description, Quantity, UnitPrice, LineTotal, LineTax *string }

func mapperFor(string) (Mapper, error) { return nil, nil }

func mapFake(Document, *Party) (invoice.CreateInput, error) { return invoice.CreateInput{}, nil }

func ContentHash(invoice.CreateInput) string { return "" }

func Differs(invoice.CreateInput, invoiceAmounts, []lineAmounts) bool { return false }
