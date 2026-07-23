// mock_wire.go: M5-03-01 (task-224). The BIS Billing 3.0 / EN 16931-shaped wire envelope the
// mock APP adapter puts on the wire ([wire-payload]), plus the read-back path the content-keyed
// trigger needs ([trigger-read-from-the-real-bis-field]).
//
// The TYPE SET below (every struct, every field, every json tag) and the FUNCTION SIGNATURES are
// the Stage-1 architecture output and are final.
//
// Decisions this file implements:
//   - [wire-payload] structs only, never map[string]any, so field order is fixed by
//     declaration and json.Marshal is byte-deterministic for a given input (L03).
//   - [zero-line-invoice] / [wire-lines-empty-not-omitted] InvoiceLine is built with
//     make([]mockLine, 0, len(c.Lines)) AND carries no omitempty, so a nil Canonical.Lines
//     marshals to [] -- never null, never an absent key. Both halves are required: make()
//     alone with omitempty omits the key; omitempty alone with a nil slice emits null.
//   - [money-passes-through-verbatim] every money and quantity value is carried as the
//     ::text-read decimal string it arrived as; nothing is ever parsed into a number.
//   - [trigger-read-from-the-real-bis-field] the buyer TIN lands at
//     AccountingCustomerParty.Party.PartyTaxScheme.CompanyID -- the real BIS field it belongs
//     in -- because Submit never sees the Canonical (adapter.go), only the Wire bytes, so the
//     trigger must round-trip through the wire.
//
// Container-vs-member pointer rule: a BIS-mandatory CONTAINER stays a value struct so its key
// can never vanish (omitempty has no effect on a non-pointer struct in encoding/json); the
// AMOUNT inside it is the pointer, so a nil *string in the Canonical deletes the whole amount
// object rather than coercing to "" or "0". This is why "LegalMonetaryTotal":{} is the
// CORRECT output for an all-nil-money canonical.
package submission

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const (
	mockCustomizationID = "urn:cen.eu:en16931:2017#compliant#urn:fdc:peppol.eu:2017:poacc:billing:3.0"
	mockProfileID       = "urn:fdc:peppol.eu:2017:poacc:billing:01:1.0"
	mockInvoiceTypeCode = "380" // commercial invoice
	mockTaxSchemeID     = "TIN"
	// mockIssueDateLayout is named, not inlined, because M5-03-02 parses the date part of the
	// IRN back out of it. Mirrors internal/invoice/payload.go:31 mbsDateLayout.
	mockIssueDateLayout = "2006-01-02"
)

// ErrMockUnparseableWire is the sentinel parseMockEnvelope wraps, so M5-03-03 can branch with
// errors.Is rather than matching on message text (the registry.go:47-51 pattern).
var ErrMockUnparseableWire = errors.New("submission: mock wire is not a parseable envelope")

type mockEnvelope struct {
	CustomizationID         string            `json:"CustomizationID"`
	ProfileID               string            `json:"ProfileID"`
	ID                      string            `json:"ID"`                  // Canonical.InvoiceNumber
	UUID                    string            `json:"UUID,omitempty"`      // Canonical.InvoiceID
	IssueDate               string            `json:"IssueDate,omitempty"` // YYYY-MM-DD
	InvoiceTypeCode         string            `json:"InvoiceTypeCode"`
	DocumentCurrencyCode    string            `json:"DocumentCurrencyCode,omitempty"`
	AccountingSupplierParty mockParty         `json:"AccountingSupplierParty"`
	AccountingCustomerParty mockParty         `json:"AccountingCustomerParty"`
	TaxTotal                *mockTaxTotal     `json:"TaxTotal,omitempty"` // Canonical.VAT
	LegalMonetaryTotal      mockMonetaryTotal `json:"LegalMonetaryTotal"`
	InvoiceLine             []mockLine        `json:"InvoiceLine"` // NEVER omitempty -- [zero-line-invoice]
}

type mockParty struct {
	Party mockPartyBody `json:"Party"`
}

type mockPartyBody struct {
	PartyName      *mockPartyName      `json:"PartyName,omitempty"`
	PartyTaxScheme *mockPartyTaxScheme `json:"PartyTaxScheme,omitempty"`
}

type mockPartyName struct {
	Name string `json:"Name"`
}

type mockPartyTaxScheme struct {
	CompanyID string        `json:"CompanyID"` // <- the buyer TIN; the trigger channel
	TaxScheme mockTaxScheme `json:"TaxScheme"`
}

type mockTaxScheme struct {
	ID string `json:"ID"`
}

// mockAmount.Value is NEVER omitempty: mockAmount is only ever built when its source *string
// is non-nil, so a "value":"" can only mean a pointer-to-empty-string. omitempty there would
// make present-but-empty indistinguishable from absent -- the exact coercion AC-4 forbids,
// inverted.
type mockAmount struct {
	CurrencyID string `json:"currencyID,omitempty"`
	Value      string `json:"value"` // verbatim decimal string [money-passes-through-verbatim]
}

type mockTaxTotal struct {
	TaxAmount mockAmount `json:"TaxAmount"`
}

// LineExtensionAmount and TaxExclusiveAmount are BOTH fed from Canonical.Subtotal -- not a
// typo: they are equal in BIS absent document-level allowances and charges.
type mockMonetaryTotal struct {
	LineExtensionAmount *mockAmount `json:"LineExtensionAmount,omitempty"` // Subtotal
	TaxExclusiveAmount  *mockAmount `json:"TaxExclusiveAmount,omitempty"`  // Subtotal
	PayableAmount       *mockAmount `json:"PayableAmount,omitempty"`       // Total
}

type mockItem struct {
	Name string `json:"Name"`
}

// mockPrice.PriceAmount is a VALUE: the *mockPrice is already the nil-able layer, and a second
// pointer would allow a meaningless {"Price":{}}.
type mockPrice struct {
	PriceAmount mockAmount `json:"PriceAmount"`
}

type mockLine struct {
	ID                  string        `json:"ID"`             // LineNo as a decimal string
	UUID                string        `json:"UUID,omitempty"` // LineID ("" for an unstored line)
	InvoicedQuantity    *string       `json:"InvoicedQuantity,omitempty"`
	LineExtensionAmount *mockAmount   `json:"LineExtensionAmount,omitempty"` // CanonicalLine.LineTotal
	Item                *mockItem     `json:"Item,omitempty"`                // {Name: Description}
	Price               *mockPrice    `json:"Price,omitempty"`               // {PriceAmount: UnitPrice}
	TaxTotal            *mockTaxTotal `json:"TaxTotal,omitempty"`            // CanonicalLine.LineTax
}

// buildMockEnvelope projects a Canonical onto the wire envelope. Pure: no I/O, no clock, no
// randomness, and it never mutates c (L03/L04) -- every *string it carries across is COPIED,
// not aliased. It never panics, including on the zero Canonical.
func buildMockEnvelope(c Canonical) mockEnvelope {
	env := mockEnvelope{
		CustomizationID:         mockCustomizationID,
		ProfileID:               mockProfileID,
		ID:                      c.InvoiceNumber,
		UUID:                    c.InvoiceID,
		InvoiceTypeCode:         mockInvoiceTypeCode,
		AccountingSupplierParty: mockPartyFrom(c.Supplier),
		AccountingCustomerParty: mockPartyFrom(c.Buyer),
	}

	if c.IssueDate != nil {
		// Formatted in the value's OWN location, deliberately NOT .UTC()-normalised: pgx scans
		// a `date` to UTC so it is a no-op in production, but .UTC() on a hand-built FixedZone
		// time would silently shift the date by a day.
		env.IssueDate = c.IssueDate.Format(mockIssueDateLayout)
	}
	if c.Currency != nil {
		env.DocumentCurrencyCode = *c.Currency
	}
	if amt := mockAmountFrom(c.VAT, c.Currency); amt != nil {
		env.TaxTotal = &mockTaxTotal{TaxAmount: *amt}
	}
	// LineExtensionAmount and TaxExclusiveAmount are BOTH fed from Subtotal -- see the type's
	// comment. Two separate mockAmountFrom calls, so the two keys never share a pointer.
	env.LegalMonetaryTotal = mockMonetaryTotal{
		LineExtensionAmount: mockAmountFrom(c.Subtotal, c.Currency),
		TaxExclusiveAmount:  mockAmountFrom(c.Subtotal, c.Currency),
		PayableAmount:       mockAmountFrom(c.Total, c.Currency),
	}

	// [wire-lines-empty-not-omitted] make() UNCONDITIONALLY, before the loop: len(nil slice) is
	// 0, so no branch is needed, and the result is a non-nil empty slice that marshals to [].
	//
	// CONFLICT, deliberate, do not "harmonise": internal/invoice/payload.go:113-121 does the
	// exact OPPOSITE -- it OMITS line_items when there are none, because omission is what makes
	// the MBS line-items-required rule fire ([payload-absence]). That is the validator payload;
	// this is the APP wire envelope, where BIS wants the key present and empty. Both are correct
	// in their own package.
	env.InvoiceLine = make([]mockLine, 0, len(c.Lines))
	for _, l := range c.Lines {
		line := mockLine{
			ID:                  strconv.Itoa(l.LineNo),
			UUID:                l.LineID,
			InvoicedQuantity:    mockCopyString(l.Quantity),
			LineExtensionAmount: mockAmountFrom(l.LineTotal, c.Currency),
		}
		if l.Description != nil {
			line.Item = &mockItem{Name: *l.Description}
		}
		if amt := mockAmountFrom(l.UnitPrice, c.Currency); amt != nil {
			line.Price = &mockPrice{PriceAmount: *amt}
		}
		if amt := mockAmountFrom(l.LineTax, c.Currency); amt != nil {
			line.TaxTotal = &mockTaxTotal{TaxAmount: *amt}
		}
		env.InvoiceLine = append(env.InvoiceLine, line)
	}

	return env
}

// marshalMockEnvelope renders env with json.Marshal -- NOT json.Encoder.Encode, whose trailing
// newline would carry a stray byte into the M5-07 archive via RequestBody = string(w). On
// error it returns a NIL Wire, never a partial one (L05).
func marshalMockEnvelope(env mockEnvelope) (Wire, error) {
	b, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("submission: marshal mock envelope: %w", err)
	}
	return Wire(b), nil
}

// mockWireFrom is the single build+marshal entry point. M5-03-03's Transform is exactly
// `return mockWireFrom(c)` and nothing else, so there is only ever one marshal path.
func mockWireFrom(c Canonical) (Wire, error) {
	return marshalMockEnvelope(buildMockEnvelope(c))
}

// parseMockEnvelope decodes wire bytes back into the envelope, wrapping ErrMockUnparseableWire
// on failure.
//
// ACCEPTED BEHAVIOUR, do not "fix": the JSON documents `null` and `{}` parse WITHOUT error,
// into the zero envelope -> buyer TIN "" -> the accept path
// ([non-reserved-defaults-to-accept]). That is deliberate and is unreachable from any Transform
// in this package. This function owns no validation rule beyond "these bytes are JSON".
func parseMockEnvelope(w Wire) (mockEnvelope, error) {
	var env mockEnvelope
	if err := json.Unmarshal(w, &env); err != nil {
		// Both verbs are %w: the sentinel so M5-03-03 can errors.Is, the decoder error so the
		// underlying reason survives into the M5-07 evidence trail. No extra "submission: ..."
		// context prefix here -- ErrMockUnparseableWire's own text already starts with
		// "submission: ", and every other error in this package (registry.go's bare sentinels,
		// exchange.go:183,187,208's single %w) carries exactly ONE such prefix; adding a second
		// would duplicate it in the rendered message.
		return mockEnvelope{}, fmt.Errorf("%w: %w", ErrMockUnparseableWire, err)
	}
	return env, nil
}

// mockBuyerTIN reads the buyer TIN back out of a parsed envelope. Total and nil-safe: it
// returns "" when there is no PartyTaxScheme, and never errors.
func mockBuyerTIN(env mockEnvelope) string {
	scheme := env.AccountingCustomerParty.Party.PartyTaxScheme
	if scheme == nil {
		return ""
	}
	return scheme.CompanyID
}

// mockCopyString returns an independent copy of s (nil stays nil). Strings are immutable so
// aliasing cannot corrupt today, but internal/invoice's SubmissionCanonical aliases the
// Store-hydrated invoice's pointers into the Canonical, and contract_red_test.go:57-58,275-285
// documents an adapter mutating a shared corpus through exactly such a pointer -- L04's live
// failure mode.
func mockCopyString(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

// mockAmountFrom returns nil when v is nil, so a nil *string deletes the whole amount object
// rather than coercing to "" or "0". currency is the DOCUMENT currency (Canonical.Currency) --
// the canonical carries no per-line currency.
func mockAmountFrom(v, currency *string) *mockAmount {
	if v == nil {
		return nil
	}
	amount := mockAmount{Value: *v}
	if currency != nil {
		amount.CurrencyID = *currency
	}
	return &amount
}

// mockPartyFrom projects a Party. An all-nil Party omits both sub-blocks, but the enclosing
// AccountingSupplierParty/AccountingCustomerParty key is always present -- both are required
// in BIS.
func mockPartyFrom(p Party) mockParty {
	var body mockPartyBody
	if p.Name != nil {
		body.PartyName = &mockPartyName{Name: *p.Name}
	}
	if p.TIN != nil {
		body.PartyTaxScheme = &mockPartyTaxScheme{
			CompanyID: *p.TIN,
			TaxScheme: mockTaxScheme{ID: mockTaxSchemeID},
		}
	}
	return mockParty{Party: body}
}
