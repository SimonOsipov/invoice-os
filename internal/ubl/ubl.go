// Package ubl renders a stored invoice as a UBL 2.1 Invoice document.
//
// The output is structurally well-formed UBL declaring the PEPPOL BIS 3.0 profile and
// faithfully reflecting stored invoice content. It is NOT a validator-certified document:
// EN 16931 mandates seller and buyer postal address + country code, and nothing in this
// system stores them -- see [followup-bis-party-address-gap]. No comment, error string or
// test name here may claim otherwise [ubl-conformance-is-structural-not-certified].
package ubl

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// ErrIncomplete is returned when Missing reports content the document cannot be built
// without. Callers branch with errors.Is.
var ErrIncomplete = errors.New("ubl: invoice is missing content the document needs")

const (
	nsInvoice = "urn:oasis:names:specification:ubl:schema:xsd:Invoice-2"
	nsCAC     = "urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
	nsCBC     = "urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2"

	// Same values as internal/submission/mock_wire.go:37-40 -- two citations of one external
	// standard, not two derivations [ubl-bis-constants-mirror-the-wire].
	customizationID = "urn:cen.eu:en16931:2017#compliant#urn:fdc:peppol.eu:2017:poacc:billing:3.0"
	profileID       = "urn:fdc:peppol.eu:2017:poacc:billing:01:1.0"
	invoiceTypeCode = "380" // commercial invoice
	taxSchemeID     = "TIN"
	issueDateLayout = "2006-01-02"
)

// Field declaration order below IS the UBL document sequence -- nothing else pins it.
// Optional members are pointers: `omitempty` on a value struct does nothing.
type document struct {
	XMLName  xml.Name `xml:"urn:oasis:names:specification:ubl:schema:xsd:Invoice-2 Invoice"`
	XMLNSCAC string   `xml:"xmlns:cac,attr"`
	XMLNSCBC string   `xml:"xmlns:cbc,attr"`

	CustomizationID      string        `xml:"cbc:CustomizationID"`
	ProfileID            string        `xml:"cbc:ProfileID"`
	ID                   string        `xml:"cbc:ID"`
	IssueDate            string        `xml:"cbc:IssueDate"` // pre-formatted, never time.Time
	InvoiceTypeCode      string        `xml:"cbc:InvoiceTypeCode"`
	DocumentCurrencyCode string        `xml:"cbc:DocumentCurrencyCode,omitempty"`
	Supplier             party         `xml:"cac:AccountingSupplierParty"`
	Buyer                party         `xml:"cac:AccountingCustomerParty"`
	TaxTotal             *taxTotal     `xml:"cac:TaxTotal,omitempty"`
	LegalMonetaryTotal   monetaryTotal `xml:"cac:LegalMonetaryTotal"`
	Lines                []line        `xml:"cac:InvoiceLine"`
}

type party struct {
	Party partyBody `xml:"cac:Party"`
}

type partyBody struct {
	PartyName      *partyName      `xml:"cac:PartyName,omitempty"`
	PartyTaxScheme *partyTaxScheme `xml:"cac:PartyTaxScheme,omitempty"`
}

type partyName struct {
	Name string `xml:"cbc:Name"`
}

type partyTaxScheme struct {
	CompanyID string    `xml:"cbc:CompanyID"`
	TaxScheme taxScheme `xml:"cac:TaxScheme"`
}

type taxScheme struct {
	ID string `xml:"cbc:ID"`
}

// Value carries no omitempty: an amount is only built from a non-nil *string, so an empty
// element can only mean pointer-to-"" -- hiding that would make absent and blank identical.
type amount struct {
	CurrencyID string `xml:"currencyID,attr,omitempty"`
	Value      string `xml:",chardata"`
}

type taxTotal struct {
	TaxAmount amount `xml:"cbc:TaxAmount"`
}

type monetaryTotal struct {
	LineExtensionAmount *amount `xml:"cbc:LineExtensionAmount,omitempty"`
	TaxExclusiveAmount  *amount `xml:"cbc:TaxExclusiveAmount,omitempty"`
	PayableAmount       *amount `xml:"cbc:PayableAmount,omitempty"`
}

type item struct {
	Name string `xml:"cbc:Name"`
}

type price struct {
	PriceAmount amount `xml:"cbc:PriceAmount"`
}

type line struct {
	ID                  string    `xml:"cbc:ID"`
	InvoicedQuantity    *string   `xml:"cbc:InvoicedQuantity,omitempty"`
	LineExtensionAmount *amount   `xml:"cbc:LineExtensionAmount,omitempty"`
	TaxTotal            *taxTotal `xml:"cac:TaxTotal,omitempty"`
	Item                *item     `xml:"cac:Item,omitempty"`
	Price               *price    `xml:"cac:Price,omitempty"`
}

// Render returns the UBL document for c, or ErrIncomplete when Missing(c) is non-empty.
func Render(c submission.Canonical) ([]byte, error) {
	if m := Missing(c); len(m) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrIncomplete, strings.Join(m, ", "))
	}

	b, err := xml.MarshalIndent(build(c), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("ubl: marshal invoice document: %w", err)
	}
	return append([]byte(xml.Header), b...), nil
}

// Missing lists the content the document cannot be constructed without, in a fixed order.
// nil when nothing is missing. A constructability gate, not a conformance oracle.
func Missing(c submission.Canonical) []string {
	var m []string
	if strings.TrimSpace(c.InvoiceNumber) == "" {
		m = append(m, "an invoice number")
	}
	if c.IssueDate == nil {
		m = append(m, "an issue date")
	}
	if blank(c.Currency) {
		m = append(m, "a currency")
	}
	if blank(c.Supplier.Name) {
		m = append(m, "a supplier name")
	}
	if blank(c.Buyer.Name) {
		m = append(m, "a buyer name")
	}
	if len(c.Lines) == 0 {
		m = append(m, "at least one line item")
	}
	return m
}

// build reads c only; the document is local to Render and never escapes, so aliasing c's
// strings is safe here.
func build(c submission.Canonical) document {
	doc := document{
		XMLNSCAC:        nsCAC,
		XMLNSCBC:        nsCBC,
		CustomizationID: customizationID,
		ProfileID:       profileID,
		ID:              c.InvoiceNumber,
		InvoiceTypeCode: invoiceTypeCode,
		Supplier:        partyFrom(c.Supplier),
		Buyer:           partyFrom(c.Buyer),
	}

	if c.IssueDate != nil {
		// Formatted in the value's OWN location: .UTC() would shift a WAT 00:30 date a day back.
		doc.IssueDate = c.IssueDate.Format(issueDateLayout)
	}
	if c.Currency != nil {
		doc.DocumentCurrencyCode = *c.Currency
	}
	if a := amountFrom(c.VAT, c.Currency); a != nil {
		doc.TaxTotal = &taxTotal{TaxAmount: *a}
	}
	// LineExtensionAmount and TaxExclusiveAmount both read Subtotal; separate calls so the two
	// elements never share a pointer.
	doc.LegalMonetaryTotal = monetaryTotal{
		LineExtensionAmount: amountFrom(c.Subtotal, c.Currency),
		TaxExclusiveAmount:  amountFrom(c.Subtotal, c.Currency),
		PayableAmount:       amountFrom(c.Total, c.Currency),
	}

	// Slice order, not LineNo order -- Store.Get already sorts by line_no.
	for _, l := range c.Lines {
		ln := line{
			ID:                  strconv.Itoa(l.LineNo),
			InvoicedQuantity:    l.Quantity, // bare: Canonical carries no unit, and unitCode is not ours to invent
			LineExtensionAmount: amountFrom(l.LineTotal, c.Currency),
		}
		if a := amountFrom(l.LineTax, c.Currency); a != nil {
			ln.TaxTotal = &taxTotal{TaxAmount: *a}
		}
		if l.Description != nil {
			ln.Item = &item{Name: *l.Description}
		}
		if a := amountFrom(l.UnitPrice, c.Currency); a != nil {
			ln.Price = &price{PriceAmount: *a}
		}
		doc.Lines = append(doc.Lines, ln)
	}

	return doc
}

func partyFrom(p submission.Party) party {
	var body partyBody
	if p.Name != nil {
		body.PartyName = &partyName{Name: *p.Name}
	}
	if p.TIN != nil {
		body.PartyTaxScheme = &partyTaxScheme{CompanyID: *p.TIN, TaxScheme: taxScheme{ID: taxSchemeID}}
	}
	return party{Party: body}
}

// amountFrom passes the ::text-read decimal through verbatim -- no parse, no rounding.
func amountFrom(v, currency *string) *amount {
	if v == nil {
		return nil
	}
	a := amount{Value: *v}
	if currency != nil {
		a.CurrencyID = *currency
	}
	return &a
}

func blank(p *string) bool { return p == nil || strings.TrimSpace(*p) == "" }
