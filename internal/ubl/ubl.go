// Package ubl renders a stored invoice as a UBL 2.1 Invoice document.
//
// The output is structurally well-formed UBL declaring the PEPPOL BIS 3.0 profile and
// faithfully reflecting stored invoice content. Postal address and contact render when
// stored; EN 16931 mandates them and nothing here enforces that. It is NOT a
// validator-certified document. No comment, error string or test name here may claim
// otherwise [ubl-conformance-is-structural-not-certified].
package ubl

import (
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// ErrIncomplete is returned when Missing reports content the document cannot be built
// without. Callers branch with errors.Is.
var ErrIncomplete = errors.New("ubl: invoice is missing content the document needs")

const (
	// The Invoice-2 root URI has no constant: it lives in document's XMLName tag, and struct
	// tags take no constants.
	nsCAC = "urn:oasis:names:specification:ubl:schema:xsd:CommonAggregateComponents-2"
	nsCBC = "urn:oasis:names:specification:ubl:schema:xsd:CommonBasicComponents-2"

	// Same values as internal/submission/mock_wire.go:37-40 -- two citations of one external
	// standard, not two derivations [ubl-bis-constants-mirror-the-wire].
	customizationID = "urn:cen.eu:en16931:2017#compliant#urn:fdc:peppol.eu:2017:poacc:billing:3.0"
	profileID       = "urn:fdc:peppol.eu:2017:poacc:billing:01:1.0"
	invoiceTypeCode = "380" // commercial invoice
	taxSchemeID     = "TIN"
	vatSchemeID     = "VAT"
	issueDateLayout = "2006-01-02"
)

// Field declaration order below IS the UBL document sequence; TestRender_ElementOrderFollowsTheUBLSequence
// pins it. Optional members are pointers: `omitempty` on a value struct does nothing.
type document struct {
	XMLName  xml.Name `xml:"urn:oasis:names:specification:ubl:schema:xsd:Invoice-2 Invoice"`
	XMLNSCAC string   `xml:"xmlns:cac,attr"`
	XMLNSCBC string   `xml:"xmlns:cbc,attr"`

	CustomizationID      string         `xml:"cbc:CustomizationID"`
	ProfileID            string         `xml:"cbc:ProfileID"`
	ID                   string         `xml:"cbc:ID"`
	IssueDate            string         `xml:"cbc:IssueDate"` // pre-formatted, never time.Time
	IssueTime            *string        `xml:"cbc:IssueTime,omitempty"`
	DueDate              *string        `xml:"cbc:DueDate,omitempty"`
	InvoiceTypeCode      string         `xml:"cbc:InvoiceTypeCode"`
	TaxPointDate         *string        `xml:"cbc:TaxPointDate,omitempty"`
	DocumentCurrencyCode string         `xml:"cbc:DocumentCurrencyCode,omitempty"`
	TaxCurrencyCode      *string        `xml:"cbc:TaxCurrencyCode,omitempty"`
	Supplier             party          `xml:"cac:AccountingSupplierParty"`
	Buyer                party          `xml:"cac:AccountingCustomerParty"`
	TaxTotal             *taxTotal      `xml:"cac:TaxTotal,omitempty"`
	LegalMonetaryTotal   *monetaryTotal `xml:"cac:LegalMonetaryTotal,omitempty"`
	Lines                []line         `xml:"cac:InvoiceLine"`
}

type party struct {
	Party partyBody `xml:"cac:Party"`
}

type partyBody struct {
	PartyName      *partyName      `xml:"cac:PartyName,omitempty"`
	PostalAddress  *postalAddress  `xml:"cac:PostalAddress,omitempty"`
	PartyTaxScheme *partyTaxScheme `xml:"cac:PartyTaxScheme,omitempty"`
	Contact        *contact        `xml:"cac:Contact,omitempty"`
}

type postalAddress struct {
	StreetName           *string  `xml:"cbc:StreetName,omitempty"`
	CityName             *string  `xml:"cbc:CityName,omitempty"`
	PostalZone           *string  `xml:"cbc:PostalZone,omitempty"`
	CountrySubentityCode *string  `xml:"cbc:CountrySubentityCode,omitempty"`
	District             *string  `xml:"cbc:District,omitempty"`
	Country              *country `xml:"cac:Country,omitempty"`
}

type country struct {
	IdentificationCode string `xml:"cbc:IdentificationCode"`
}

type contact struct {
	Telephone      *string `xml:"cbc:Telephone,omitempty"`
	ElectronicMail *string `xml:"cbc:ElectronicMail,omitempty"`
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
	TaxAmount   amount        `xml:"cbc:TaxAmount"`
	TaxSubtotal []taxSubtotal `xml:"cac:TaxSubtotal"`
}

type taxSubtotal struct {
	TaxableAmount *amount     `xml:"cbc:TaxableAmount,omitempty"`
	TaxAmount     amount      `xml:"cbc:TaxAmount"`
	TaxCategory   taxCategory `xml:"cac:TaxCategory"`
}

type taxCategory struct {
	ID        string    `xml:"cbc:ID"`
	Percent   *string   `xml:"cbc:Percent,omitempty"`
	TaxScheme taxScheme `xml:"cac:TaxScheme"`
}

type monetaryTotal struct {
	LineExtensionAmount *amount `xml:"cbc:LineExtensionAmount,omitempty"`
	TaxExclusiveAmount  *amount `xml:"cbc:TaxExclusiveAmount,omitempty"`
	PayableAmount       *amount `xml:"cbc:PayableAmount,omitempty"`
}

type item struct {
	Name                      *string              `xml:"cbc:Name,omitempty"`
	SellersItemIdentification *itemIdentification  `xml:"cac:SellersItemIdentification,omitempty"`
	CommodityClassification   []commodityClass     `xml:"cac:CommodityClassification"`
	ClassifiedTaxCategory     *taxCategory         `xml:"cac:ClassifiedTaxCategory,omitempty"`
	AdditionalItemProperty    []additionalProperty `xml:"cac:AdditionalItemProperty"`
}

type itemIdentification struct {
	ID string `xml:"cbc:ID"`
}

type commodityClass struct {
	Code classificationCode `xml:"cbc:ItemClassificationCode"`
}

type classificationCode struct {
	ListID string `xml:"listID,attr"`
	Value  string `xml:",chardata"`
}

type additionalProperty struct {
	Name  string `xml:"cbc:Name"`
	Value string `xml:"cbc:Value"`
}

type quantity struct {
	UnitCode string `xml:"unitCode,attr,omitempty"`
	Value    string `xml:",chardata"`
}

type price struct {
	PriceAmount  amount    `xml:"cbc:PriceAmount"`
	BaseQuantity *quantity `xml:"cbc:BaseQuantity,omitempty"`
}

type line struct {
	ID                  string    `xml:"cbc:ID"`
	InvoicedQuantity    *quantity `xml:"cbc:InvoicedQuantity,omitempty"`
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
// strings is safe here. Assumes Missing(c) is empty -- cac:Party has no unconditional member
// and relies on that gate for a name.
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
		// Formatted in the value's OWN location, never .UTC() -- see
		// TestRender_IssueDateIsNotTimezoneShifted.
		doc.IssueDate = c.IssueDate.Format(issueDateLayout)
	}
	if c.Currency != nil {
		doc.DocumentCurrencyCode = *c.Currency
	}
	doc.IssueTime = c.IssueTime
	doc.DueDate = dateText(c.DueDate)
	doc.TaxPointDate = dateText(c.TaxPointDate)
	doc.TaxCurrencyCode = c.TaxCurrencyCode
	if a := amountFrom(c.VAT, c.Currency); a != nil {
		doc.TaxTotal = &taxTotal{TaxAmount: *a, TaxSubtotal: subtotalsFrom(c)}
	}
	// LineExtensionAmount and TaxExclusiveAmount both read Subtotal; separate calls so the two
	// elements never share a pointer.
	mt := monetaryTotal{
		LineExtensionAmount: amountFrom(c.Subtotal, c.Currency),
		TaxExclusiveAmount:  amountFrom(c.Subtotal, c.Currency),
		PayableAmount:       amountFrom(c.Total, c.Currency),
	}
	// A container with no members is omitted, not emitted empty -- TestRender_EmitsNoEmptyContainer.
	if mt.LineExtensionAmount != nil || mt.TaxExclusiveAmount != nil || mt.PayableAmount != nil {
		doc.LegalMonetaryTotal = &mt
	}

	// Slice order, not LineNo order -- Store.Get already sorts by line_no.
	for _, l := range c.Lines {
		ln := line{
			ID:                  strconv.Itoa(l.LineNo),
			LineExtensionAmount: amountFrom(l.LineTotal, c.Currency),
		}
		if l.Quantity != nil {
			ln.InvoicedQuantity = &quantity{UnitCode: deref(l.PriceUnit), Value: *l.Quantity}
		}
		if a := amountFrom(l.LineTax, c.Currency); a != nil {
			ln.TaxTotal = &taxTotal{TaxAmount: *a}
		}
		ln.Item = itemFrom(l)
		if a := amountFrom(l.UnitPrice, c.Currency); a != nil {
			ln.Price = &price{PriceAmount: *a}
			if l.BaseQuantity != nil {
				ln.Price.BaseQuantity = &quantity{UnitCode: deref(l.PriceUnit), Value: *l.BaseQuantity}
			}
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
	addr := postalAddress{
		StreetName:           p.Street,
		CityName:             p.City,
		PostalZone:           p.PostalZone,
		CountrySubentityCode: p.State,
		District:             p.LGA,
	}
	if p.Country != nil {
		addr.Country = &country{IdentificationCode: *p.Country}
	}
	if addr != (postalAddress{}) {
		body.PostalAddress = &addr
	}
	if ct := (contact{Telephone: p.Telephone, ElectronicMail: p.Email}); ct != (contact{}) {
		body.Contact = &ct
	}
	return party{Party: body}
}

// subtotalsFrom skips a subtotal with no tax amount: TaxAmount is mandatory and not ours to invent.
func subtotalsFrom(c submission.Canonical) []taxSubtotal {
	var out []taxSubtotal
	for _, s := range c.TaxSubtotals {
		ta := amountFrom(s.TaxAmount, c.Currency)
		if ta == nil {
			continue
		}
		out = append(out, taxSubtotal{
			TaxableAmount: amountFrom(s.TaxableAmount, c.Currency),
			TaxAmount:     *ta,
			TaxCategory:   taxCategory{ID: s.Category, Percent: s.Percent, TaxScheme: taxScheme{ID: vatSchemeID}},
		})
	}
	return out
}

// itemFrom is nil when the line stores no item member. A percent without a category renders nowhere.
func itemFrom(l submission.CanonicalLine) *item {
	it := item{Name: l.Description}
	if l.SellersItemIdentification != nil {
		it.SellersItemIdentification = &itemIdentification{ID: *l.SellersItemIdentification}
	}
	if l.HSNCode != nil {
		it.CommodityClassification = append(it.CommodityClassification, commodityClass{classificationCode{ListID: "HS", Value: *l.HSNCode}})
	}
	if l.ISICCode != nil {
		it.CommodityClassification = append(it.CommodityClassification, commodityClass{classificationCode{ListID: "ISIC", Value: *l.ISICCode}})
	}
	if l.TaxCategory != nil {
		it.ClassifiedTaxCategory = &taxCategory{ID: *l.TaxCategory, Percent: l.TaxPercent, TaxScheme: taxScheme{ID: vatSchemeID}}
	}
	if l.ProductCategory != nil {
		it.AdditionalItemProperty = append(it.AdditionalItemProperty, additionalProperty{"product_category", *l.ProductCategory})
	}
	if l.ServiceCategory != nil {
		it.AdditionalItemProperty = append(it.AdditionalItemProperty, additionalProperty{"service_category", *l.ServiceCategory})
	}
	if it.Name == nil && it.SellersItemIdentification == nil && len(it.CommodityClassification) == 0 &&
		it.ClassifiedTaxCategory == nil && len(it.AdditionalItemProperty) == 0 {
		return nil
	}
	return &it
}

// dateText formats in the value's own location, as the issue date does.
func dateText(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(issueDateLayout)
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
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
