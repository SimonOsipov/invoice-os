// mock_wire_test.go: M5-03-01 (task-224) RED specs (QA Mode A) for the BIS Billing 3.0 wire
// envelope -- the nine specs transcribed from the story's Test Specs table, plus one extra
// (TestMockWire_UsesNoDynamicMaps) recommended by the Stage-1 architecture.
//
// What this file proves, once green: that the projection Canonical -> mockEnvelope -> Wire is
// LOSSLESS AND HONEST at the byte level. Specifically that a nil Lines becomes [] and not null
// ([zero-line-invoice]); that a nil *string vanishes rather than becoming "" or "0" (AC-4);
// that money and quantity strings reach the wire byte-for-byte, never through a float
// ([money-passes-through-verbatim]); that the buyer TIN survives the round trip through the
// real BIS field ([trigger-read-from-the-real-bis-field]), which is the only channel the
// content-keyed trigger has, since Submit never sees the Canonical; and that the whole thing is
// pure -- deterministic, non-mutating, panic-free on the zero value.
//
// PACKAGE submission (IN-PACKAGE), unlike every other test file in this directory. This is
// deliberate and is story decision [test-package-follows-the-symbol]: mockEnvelope,
// buildMockEnvelope, mockWireFrom, parseMockEnvelope and mockBuyerTIN are all UNEXPORTED and
// MockAdapter -- the exported seam -- does not exist until M5-03-03, so an external test
// package would have nothing to call and this file would be literally unimplementable. Do not
// "correct" it to package submission_test. Mixing both packages in one directory is legal Go,
// compiles to one test binary, and in-package tests are the repo's dominant convention
// (internal/importer, internal/invoice); internal/submission's all-external status is an
// artifact of M5-01/M5-02, whose subject matter was the exported seam.
//
// RED STATE AT AUTHORING TIME: mock_wire.go ships the complete type set with deliberately
// no-op bodies, so every failure below is an ASSERTION failure -- not a compile error. Two
// tests are honestly labelled as NOT red-first in their own doc comments
// (TestMockWire_UsesNoDynamicMaps, and the negative-space halves noted inline); the other eight
// are genuine red-to-green specs.
//
// No testify -- standard library only. No TestMain: one already exists at
// failure_modes_test.go:57, one per test binary. No t.Skip anywhere: these tests are pure Go
// with no DB, no network and no clock, and internal/tools/rlsgate/rlsgate.go fails the CI queue
// step on any test-level skip, so they must run unconditionally.
package submission

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------
// Local corpus. A twin of contract_test.go:99-182's canonicalCorpus, rebuilt here because
// that one lives in package submission_test and is unreachable from in-package code.
//
// Two deliberate differences from the original:
//   - each case is a BUILD FUNC, not a value, so every test gets a fresh, independent
//     Canonical. contract_red_test.go:57-58,275-285 documents an adapter mutating a shared
//     PACKAGE-LEVEL corpus through an aliased pointer -- L04's live failure mode -- and
//     TestMockWire_DoesNotMutateCanonical mutates its input on purpose, so a shared var here
//     would leak that mutation into every later test in the file.
//   - the build funcs let TestMockWire_IsDeterministic construct two DISTINCT-but-equal
//     Canonicals and assert they marshal identically (L03's cross-instance property).
//
// The "no-lines" case's Lines field is NIL, not empty -- SubmissionCanonical builds Lines with
// `var lines []CanonicalLine` + append, so a zero-line invoice always yields nil. That nil is
// the whole point of AC-3.
// ---------------------------------------------------------------------------------------

type mwCase struct {
	name  string
	build func() Canonical
}

func mwCorpus() []mwCase {
	return []mwCase{
		{name: "full", build: mwFullCanonical},
		{name: "minimal", build: mwMinimalCanonical},
		{name: "no-lines", build: mwNoLinesCanonical},
		{name: "all-nil-money", build: mwAllNilMoneyCanonical},
		{name: "multi-byte-long-text", build: mwMultiByteLongTextCanonical},
		{name: "zero", build: func() Canonical { return Canonical{} }},
	}
}

func mwStrPtr(s string) *string { return &s }

func mwFullCanonical() Canonical {
	issue := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	return Canonical{
		InvoiceID:     "inv-full-1",
		InvoiceNumber: "INV-FULL-0001",
		IssueDate:     &issue,
		Supplier:      Party{TIN: mwStrPtr("SUP-TIN-1"), Name: mwStrPtr("Supplier Co")},
		Buyer:         Party{TIN: mwStrPtr("BUY-TIN-1"), Name: mwStrPtr("Buyer Ltd")},
		Currency:      mwStrPtr("NGN"),
		Subtotal:      mwStrPtr("1000.00"),
		VAT:           mwStrPtr("75.00"),
		Total:         mwStrPtr("1075.00"),
		Lines: []CanonicalLine{
			{
				LineID:      "line-1",
				LineNo:      1,
				Description: mwStrPtr("Widget"),
				Quantity:    mwStrPtr("2"),
				UnitPrice:   mwStrPtr("500.00"),
				LineTotal:   mwStrPtr("1000.00"),
				LineTax:     mwStrPtr("75.00"),
			},
		},
	}
}

func mwMinimalCanonical() Canonical {
	return Canonical{InvoiceNumber: "INV-MIN-0001"}
}

func mwNoLinesCanonical() Canonical {
	return Canonical{
		InvoiceID:     "inv-no-lines-1",
		InvoiceNumber: "INV-NOLINES-0001",
		Currency:      mwStrPtr("NGN"),
		Subtotal:      mwStrPtr("0.00"),
		VAT:           mwStrPtr("0.00"),
		Total:         mwStrPtr("0.00"),
		// Lines deliberately left as the zero value -- nil, not []CanonicalLine{}.
	}
}

func mwAllNilMoneyCanonical() Canonical {
	return Canonical{
		InvoiceID:     "inv-nil-money-1",
		InvoiceNumber: "INV-NILMONEY-0001",
		Supplier:      Party{TIN: mwStrPtr("SUP-TIN-2"), Name: mwStrPtr("Supplier Co")},
		Buyer:         Party{TIN: mwStrPtr("BUY-TIN-2"), Name: mwStrPtr("Buyer Ltd")},
		Currency:      mwStrPtr("NGN"),
		// Subtotal/VAT/Total deliberately nil.
		Lines: []CanonicalLine{
			{
				LineID:      "line-nil-1",
				LineNo:      1,
				Description: mwStrPtr("Service with no priced fields yet"),
				// Quantity/UnitPrice/LineTotal/LineTax deliberately nil too.
			},
		},
	}
}

func mwMultiByteLongTextCanonical() Canonical {
	long := strings.Repeat("Πολύ μεγάλο κείμενο με πολλαπλά bytes 你好世界 🎉 ", 200)
	return Canonical{
		InvoiceID:     "inv-mb-1",
		InvoiceNumber: "INV-Ελληνικά-你好-0001",
		Supplier:      Party{TIN: mwStrPtr("SUP-TIN-3"), Name: mwStrPtr("Ελληνική Εταιρεία 你好")},
		Buyer:         Party{TIN: mwStrPtr("BUY-TIN-3"), Name: mwStrPtr(long)},
		Currency:      mwStrPtr("NGN"),
		Subtotal:      mwStrPtr("1.00"),
		VAT:           mwStrPtr("0.00"),
		Total:         mwStrPtr("1.00"),
		Lines: []CanonicalLine{
			{LineID: "line-mb-1", LineNo: 1, Description: mwStrPtr(long)},
		},
	}
}

// mwDeepCopyCanonical is the in-package twin of contract_test.go:349-377's deepCopyCanonical --
// entirely independent copies of every pointer and slice, so a DeepEqual comparison can tell a
// genuine mutation apart from a shared backing pointer both sides happen to reach. A nil Lines
// stays nil, never becomes an empty-but-non-nil slice (which DeepEqual would report as unequal).
func mwDeepCopyCanonical(c Canonical) Canonical {
	cp := c
	if c.IssueDate != nil {
		d := *c.IssueDate
		cp.IssueDate = &d
	}
	cp.Supplier = mwDeepCopyParty(c.Supplier)
	cp.Buyer = mwDeepCopyParty(c.Buyer)
	cp.Currency = mwDeepCopyStringPtr(c.Currency)
	cp.Subtotal = mwDeepCopyStringPtr(c.Subtotal)
	cp.VAT = mwDeepCopyStringPtr(c.VAT)
	cp.Total = mwDeepCopyStringPtr(c.Total)
	if c.Lines != nil {
		lines := make([]CanonicalLine, len(c.Lines))
		for i, l := range c.Lines {
			lines[i] = CanonicalLine{
				LineID:      l.LineID,
				LineNo:      l.LineNo,
				Description: mwDeepCopyStringPtr(l.Description),
				Quantity:    mwDeepCopyStringPtr(l.Quantity),
				UnitPrice:   mwDeepCopyStringPtr(l.UnitPrice),
				LineTotal:   mwDeepCopyStringPtr(l.LineTotal),
				LineTax:     mwDeepCopyStringPtr(l.LineTax),
			}
		}
		cp.Lines = lines
	}
	return cp
}

func mwDeepCopyParty(p Party) Party {
	return Party{TIN: mwDeepCopyStringPtr(p.TIN), Name: mwDeepCopyStringPtr(p.Name)}
}

func mwDeepCopyStringPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

// mwWire builds and marshals c, failing the test if the marshal errors. It returns the RAW
// bytes as a string: several specs below can only be expressed on raw bytes, because a decoded
// [] and a decoded null are indistinguishable, and a decoded "0.00" and a decoded 0.0 are not
// distinguishable once encoding/json has been through them either.
func mwWire(t *testing.T, c Canonical) string {
	t.Helper()
	w, err := mockWireFrom(c)
	if err != nil {
		t.Fatalf("mockWireFrom returned an unexpected error: %v", err)
	}
	return string(w)
}

func mwWantContains(t *testing.T, wire, want, why string) {
	t.Helper()
	if !strings.Contains(wire, want) {
		t.Errorf("wire must contain %s (%s)\nwire: %s", want, why, mwTrunc(wire))
	}
}

func mwWantAbsent(t *testing.T, wire, unwanted, why string) {
	t.Helper()
	if strings.Contains(wire, unwanted) {
		t.Errorf("wire must NOT contain %s (%s)\nwire: %s", unwanted, why, mwTrunc(wire))
	}
}

func mwTrunc(s string) string {
	const max = 600
	if len(s) <= max {
		return s
	}
	return s[:max] + "... (truncated)"
}

// ---------------------------------------------------------------------------------------
// AC-3: a nil Lines marshals to [], never null, never an absent key. [zero-line-invoice]
// ---------------------------------------------------------------------------------------

// TestMockWire_NilLinesMarshalsToEmptyArray asserts on RAW BYTES on purpose. Decoding first
// would destroy the very distinction under test: json.Unmarshal turns both `[]` and `null` into
// the same nil []mockLine, so a decoded assertion passes against exactly the bug M4-16 shipped
// against (a Go []T without omitempty rendering null instead of []).
//
// Three assertions, not one. `!Contains(null)` alone is satisfied by an omitempty regression
// that drops the key entirely, so the key's own presence is asserted separately.
func TestMockWire_NilLinesMarshalsToEmptyArray(t *testing.T) {
	c := mwNoLinesCanonical()
	if c.Lines != nil {
		t.Fatalf("corpus precondition broken: the no-lines case must have a NIL Lines, got %#v", c.Lines)
	}

	wire := mwWire(t, c)

	mwWantContains(t, wire, `"InvoiceLine":[]`, "AC-3: a nil Canonical.Lines must marshal to an empty ARRAY")
	mwWantAbsent(t, wire, `"InvoiceLine":null`,
		"AC-3: a nil []mockLine leaking through as JSON null is the defect this spec exists for")
	mwWantContains(t, wire, `"InvoiceLine"`,
		"AC-3: InvoiceLine carries no omitempty -- the key must never vanish, only ever be []")
}

// ---------------------------------------------------------------------------------------
// AC-2: the buyer TIN round-trips through the real BIS field.
// [trigger-read-from-the-real-bis-field]
// ---------------------------------------------------------------------------------------

// TestMockWire_BuyerTINRoundTrips covers both halves. The POSITIVE half proves the TIN reaches
// AccountingCustomerParty.Party.PartyTaxScheme.CompanyID and comes back out of parsed bytes --
// the only channel the content-keyed trigger has, since Submit is handed the Wire and never the
// Canonical (adapter.go). The NEGATIVE half is what actually catches an implementation that
// reads the SUPPLIER block: with only the supplier's TIN set, the supplier TIN must still be on
// the wire (so we know the projection ran) while mockBuyerTIN returns "".
func TestMockWire_BuyerTINRoundTrips(t *testing.T) {
	t.Run("buyer-tin-reaches-the-customer-block-and-reads-back", func(t *testing.T) {
		const buyerTIN = "99999999-0002"
		c := mwFullCanonical()
		c.Supplier.TIN = mwStrPtr("11111111-1111")
		c.Buyer.TIN = mwStrPtr(buyerTIN)

		w, err := mockWireFrom(c)
		if err != nil {
			t.Fatalf("mockWireFrom returned an unexpected error: %v", err)
		}
		wire := string(w)

		mwWantContains(t, wire, `"CompanyID":"`+buyerTIN+`"`, "AC-2: the buyer TIN must be on the wire")

		// ...and specifically inside the CUSTOMER block, not the supplier one. The envelope
		// declares AccountingSupplierParty before AccountingCustomerParty, so everything from
		// the customer key onward is the customer block.
		at := strings.Index(wire, `"AccountingCustomerParty"`)
		if at < 0 {
			t.Errorf("AC-2: the wire has no AccountingCustomerParty key at all\nwire: %s", mwTrunc(wire))
		} else if !strings.Contains(wire[at:], `"CompanyID":"`+buyerTIN+`"`) {
			t.Errorf("AC-2: the buyer TIN must sit inside AccountingCustomerParty, not the supplier block"+
				"\ncustomer block: %s", mwTrunc(wire[at:]))
		}

		env, err := parseMockEnvelope(w)
		if err != nil {
			t.Fatalf("parseMockEnvelope rejected bytes this package just produced: %v", err)
		}
		if got := mockBuyerTIN(env); got != buyerTIN {
			t.Errorf("mockBuyerTIN(parsed) = %q, want %q -- the TIN must survive the wire round trip", got, buyerTIN)
		}
		if got := env.AccountingCustomerParty.Party.PartyTaxScheme; got == nil {
			t.Errorf("AC-2: the parsed customer party has no PartyTaxScheme block")
		} else if got.TaxScheme.ID != mockTaxSchemeID {
			t.Errorf("AC-2: customer TaxScheme.ID = %q, want the pinned constant %q", got.TaxScheme.ID, mockTaxSchemeID)
		}
	})

	t.Run("supplier-tin-alone-does-not-become-the-buyer-tin", func(t *testing.T) {
		const supplierTIN = "88888888-0001"
		c := mwFullCanonical()
		c.Supplier.TIN = mwStrPtr(supplierTIN)
		c.Buyer.TIN = nil

		w, err := mockWireFrom(c)
		if err != nil {
			t.Fatalf("mockWireFrom returned an unexpected error: %v", err)
		}
		wire := string(w)

		// Positive anchor: the projection really did run and really did carry the supplier TIN.
		// Without this the "" below would pass against an implementation that emits nothing.
		mwWantContains(t, wire, `"CompanyID":"`+supplierTIN+`"`,
			"the supplier TIN must still reach the supplier block")

		env, err := parseMockEnvelope(w)
		if err != nil {
			t.Fatalf("parseMockEnvelope rejected bytes this package just produced: %v", err)
		}
		if got := mockBuyerTIN(env); got != "" {
			t.Errorf("mockBuyerTIN = %q, want \"\" -- a nil Buyer.TIN must NOT be filled in from the "+
				"supplier block; reading the wrong party is exactly what this half catches", got)
		}
	})

	t.Run("zero-envelope-is-nil-safe", func(t *testing.T) {
		// mockBuyerTIN is documented total: no PartyTaxScheme -> "", never a panic.
		if got := mockBuyerTIN(mockEnvelope{}); got != "" {
			t.Errorf("mockBuyerTIN(mockEnvelope{}) = %q, want \"\"", got)
		}
	})
}

// ---------------------------------------------------------------------------------------
// AC-4: a nil *string omits its field or its enclosing amount object. Never "" and never "0".
// ---------------------------------------------------------------------------------------

// TestMockWire_NilFieldsAreOmittedNotCoerced drives the all-nil-money corpus shape and asserts
// on the MEMBERS of LegalMonetaryTotal, never on the container.
//
// "LegalMonetaryTotal":{} IS the correct output here: LegalMonetaryTotal is a value struct, and
// encoding/json's omitempty has no effect on a non-pointer struct, so the BIS-mandatory key can
// never vanish -- only its *mockAmount members can. Asserting the container absent would be
// asserting a bug.
//
// The presence assertions are load-bearing, not decoration: every absence assertion below is
// trivially satisfied by an empty document, so without them this whole test would pass
// vacuously against an implementation that emits nothing at all.
//
// Deliberately NOT asserted: that the bare substring "0" is absent. mockLine.ID is projected
// from a non-nullable int and legitimately emits "ID":"1"; the digit itself is not the defect.
func TestMockWire_NilFieldsAreOmittedNotCoerced(t *testing.T) {
	wire := mwWire(t, mwAllNilMoneyCanonical())

	if wire == "" {
		t.Fatalf("AC-4: mockWireFrom produced no bytes at all -- every absence assertion below " +
			"would pass vacuously")
	}

	// Present: the containers and the fields whose sources are non-nil.
	mwWantContains(t, wire, `"LegalMonetaryTotal"`,
		"the BIS-mandatory container is a value struct -- its key must survive even with every member nil")
	mwWantContains(t, wire, `"InvoiceLine":[`, "the single line must still be projected")
	mwWantContains(t, wire, `"ID":"1"`, "LineNo is a non-nullable int and must render as a decimal string")
	mwWantContains(t, wire, `"Item":{"Name":"Service with no priced fields yet"}`,
		"the line's non-nil Description must still reach Item.Name")

	// Absent: every member whose canonical source is nil.
	for _, member := range []struct{ key, why string }{
		{`"LineExtensionAmount"`, "Subtotal and LineTotal are nil -- the whole amount object must be omitted, not zeroed"},
		{`"TaxExclusiveAmount"`, "Subtotal is nil -- the whole amount object must be omitted, not zeroed"},
		{`"PayableAmount"`, "Total is nil -- the whole amount object must be omitted, not zeroed"},
		{`"TaxTotal"`, "VAT and LineTax are both nil -- no tax block at either level"},
		{`"InvoicedQuantity"`, "Quantity is nil -- the field must be omitted"},
		{`"Price"`, "UnitPrice is nil -- the whole Price object must be omitted"},
		{`"IssueDate"`, "IssueDate is nil -- no date field, and certainly no epoch"},
	} {
		mwWantAbsent(t, wire, member.key, "AC-4: "+member.why)
	}

	// Absent: the three coercions AC-4 names by hand.
	mwWantAbsent(t, wire, `"value":""`, "AC-4: a nil *string must never be coerced to an empty string")
	mwWantAbsent(t, wire, `"value":"0"`, "AC-4: a nil *string must never be coerced to a zero")
	mwWantAbsent(t, wire, `null`, "AC-4: nothing in this envelope may render as JSON null")
}

// ---------------------------------------------------------------------------------------
// AC-5: money and quantity strings reach the wire byte-for-byte.
// [money-passes-through-verbatim]
// ---------------------------------------------------------------------------------------

// TestMockWire_MoneyStringsPassThroughVerbatim uses trailing-zero decimal forms on purpose:
// "1075.000000", "2.5000" and "0.0100" are precisely the shapes a float64 round trip destroys
// (json.Marshal of the float 1075.0 emits 1075, of 2.5 emits 2.5, of 0.01 emits 0.01). A test
// built only on "1000.00" would pass against a strconv.ParseFloat/FormatFloat implementation.
//
// The "0.00" case is the other pole: a real zero must survive as the string "0.00" -- never as
// the number 0, never omitted as "empty". [D13] money is a ::text-read decimal string end to
// end; nothing in this package is allowed to parse one into a number.
func TestMockWire_MoneyStringsPassThroughVerbatim(t *testing.T) {
	tests := []struct {
		name    string
		build   func() Canonical
		present []string
		absent  []string
	}{
		{
			name: "trailing-zero-decimals-survive",
			build: func() Canonical {
				c := mwFullCanonical()
				c.Subtotal = mwStrPtr("1000.00")
				c.VAT = mwStrPtr("0.075")
				c.Total = mwStrPtr("1075.000000")
				c.Lines[0].Quantity = mwStrPtr("2.5000")
				c.Lines[0].UnitPrice = mwStrPtr("0.0100")
				c.Lines[0].LineTotal = mwStrPtr("1000.00")
				c.Lines[0].LineTax = mwStrPtr("0.075")
				return c
			},
			present: []string{
				`"value":"1000.00"`,
				`"value":"0.075"`,
				`"value":"1075.000000"`,
				`"InvoicedQuantity":"2.5000"`,
				`"value":"0.0100"`,
			},
			absent: []string{
				`"value":1075`,   // parsed into a JSON number
				`"value":"1075"`, // parsed into a float and re-formatted
				`"value":1075.0`, // ditto
				`"value":"0.01"`, // 0.0100 float-normalised
				`"value":0.01`,   // ...and as a number
				`"InvoicedQuantity":"2.5"`,
				`"InvoicedQuantity":2.5`,
			},
		},
		{
			name:  "a-real-zero-survives-as-a-string",
			build: mwNoLinesCanonical, // Subtotal/VAT/Total are all "0.00"
			present: []string{
				`"value":"0.00"`,
			},
			absent: []string{
				`"value":0`,
				`"value":0.00`,
				`"value":"0"`,
				`"value":""`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wire := mwWire(t, tc.build())
			for _, want := range tc.present {
				mwWantContains(t, wire, want, "AC-5: the decimal string must reach the wire verbatim")
			}
			for _, unwanted := range tc.absent {
				mwWantAbsent(t, wire, unwanted, "AC-5: nothing may be parsed into a number")
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// AC-6: purity -- no mutation of the argument, no panic on the zero value.
// ---------------------------------------------------------------------------------------

// TestMockWire_DoesNotMutateCanonical is L04 at this layer, in two halves.
//
// The first half (the corpus table) is the direct reading of AC-6: snapshot, build twice,
// compare. On its own it is weak -- a build that does nothing at all passes it.
//
// The second half is the one with teeth. mockLine.InvoicedQuantity is the ONLY *string the
// envelope carries, so it is the only place an implementation can alias its way back into the
// caller's Canonical. Mutating through the ENVELOPE's pointer and re-checking the Canonical
// fails an aliasing implementation and passes a copying one -- and the two are byte-identical
// on the wire, so no marshalling assertion anywhere can tell them apart.
func TestMockWire_DoesNotMutateCanonical(t *testing.T) {
	for _, tc := range mwCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.build()
			snapshot := mwDeepCopyCanonical(c)

			_ = buildMockEnvelope(c)
			_ = buildMockEnvelope(c) // twice: a mutation on the second pass counts too

			if !reflect.DeepEqual(c, snapshot) {
				t.Errorf("AC-6/L04: buildMockEnvelope mutated its Canonical argument\n got: %#v\nwant: %#v", c, snapshot)
			}
		})
	}

	t.Run("envelope-does-not-alias-the-canonical", func(t *testing.T) {
		c := mwFullCanonical()
		c.Lines[0].Quantity = mwStrPtr("2.5000")
		snapshot := mwDeepCopyCanonical(c)

		env := buildMockEnvelope(c)

		if len(env.InvoiceLine) != len(c.Lines) {
			t.Fatalf("AC-6: envelope carries %d InvoiceLine entries, want %d -- the projection must "+
				"emit one line per CanonicalLine before aliasing can even be tested",
				len(env.InvoiceLine), len(c.Lines))
		}
		q := env.InvoiceLine[0].InvoicedQuantity
		if q == nil {
			t.Fatalf("AC-6: envelope line 0 has a nil InvoicedQuantity, but the canonical's Quantity " +
				"is non-nil -- the projection must carry it across")
		}

		*q = "MUTATED-THROUGH-THE-ENVELOPE"

		if !reflect.DeepEqual(c, snapshot) {
			t.Errorf("AC-6/L04: writing through the ENVELOPE's InvoicedQuantity pointer changed the "+
				"caller's Canonical -- the projection aliased instead of copying\n got: %q\nwant: %q",
				*c.Lines[0].Quantity, *snapshot.Lines[0].Quantity)
		}
	})
}

// TestMockWire_ZeroCanonicalDoesNotPanic: the zero Canonical is every pointer nil, an empty
// InvoiceNumber and a nil Lines at once -- the shape most likely to panic. It must still produce
// a well-formed, non-empty envelope, because L05 says a nil error always comes with a non-empty
// Wire. "ID":"" is the honest output for an empty InvoiceNumber; it is a value string with no
// nil to omit.
func TestMockWire_ZeroCanonicalDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("AC-6: building/marshalling the zero Canonical panicked: %v", r)
		}
	}()

	w, err := mockWireFrom(Canonical{})
	if err != nil {
		t.Fatalf("AC-6: mockWireFrom(Canonical{}) returned an error: %v", err)
	}
	if len(w) == 0 {
		t.Fatalf("AC-6/L05: mockWireFrom(Canonical{}) returned a nil error with EMPTY bytes")
	}

	wire := string(w)
	mwWantContains(t, wire, `"CustomizationID":"`+mockCustomizationID+`"`, "AC-6: the BIS constants are unconditional")
	mwWantContains(t, wire, `"ProfileID":"`+mockProfileID+`"`, "AC-6: the BIS constants are unconditional")
	mwWantContains(t, wire, `"InvoiceTypeCode":"`+mockInvoiceTypeCode+`"`, "AC-6: the BIS constants are unconditional")
	mwWantContains(t, wire, `"ID":""`, "AC-6: ID is a value string -- an empty InvoiceNumber renders as \"\", it does not vanish")
	mwWantContains(t, wire, `"InvoiceLine":[]`, "AC-6: a zero Canonical still gets an empty line array")
	mwWantContains(t, wire, `"AccountingSupplierParty"`, "AC-1: both party keys are BIS-mandatory and unconditional")
	mwWantContains(t, wire, `"AccountingCustomerParty"`, "AC-1: both party keys are BIS-mandatory and unconditional")
}

// ---------------------------------------------------------------------------------------
// AC-7 / L03: determinism.
// ---------------------------------------------------------------------------------------

// TestMockWire_IsDeterministic covers all six corpus shapes, including the multi-byte/very-long
// text one, and asserts BOTH determinism properties:
//
//	repeated  -- the same Canonical value marshalled twice yields byte-identical Wire;
//	cross-instance -- a freshly built but EQUAL Canonical yields the same bytes too. This is the
//	   half that catches an implementation reaching for a map, a clock, a rand or a pointer
//	   address: repeated calls on one value can hide all four behind caching.
//
// The non-empty check is not decoration either: without it, an implementation that returns nil
// bytes satisfies both equality assertions trivially.
func TestMockWire_IsDeterministic(t *testing.T) {
	for _, tc := range mwCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.build()

			w1, err := mockWireFrom(c)
			if err != nil {
				t.Fatalf("mockWireFrom returned an unexpected error: %v", err)
			}
			if len(w1) == 0 {
				t.Fatalf("AC-7/L05: a nil error must come with non-empty Wire bytes; two nil Wires " +
					"would compare equal for the wrong reason")
			}

			w2, err := mockWireFrom(c)
			if err != nil {
				t.Fatalf("mockWireFrom returned an unexpected error on the second call: %v", err)
			}
			if !bytes.Equal(w1, w2) {
				t.Errorf("AC-7: marshalling the same Canonical twice produced different bytes\n1: %s\n2: %s",
					mwTrunc(string(w1)), mwTrunc(string(w2)))
			}

			w3, err := mockWireFrom(tc.build()) // distinct instance, equal value
			if err != nil {
				t.Fatalf("mockWireFrom returned an unexpected error on the fresh instance: %v", err)
			}
			if !bytes.Equal(w1, w3) {
				t.Errorf("AC-7/L03: a freshly constructed but equal Canonical produced different bytes\n"+
					"1: %s\n3: %s", mwTrunc(string(w1)), mwTrunc(string(w3)))
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// AC-8: parseMockEnvelope rejects empty and non-JSON bytes.
// ---------------------------------------------------------------------------------------

// TestMockWire_ParseRejectsEmptyAndNonJSON is deliberately SCOPED to empty bytes and bytes that
// are not JSON at all. The JSON documents `null` and `{}` are NOT in this table and must not be
// added: they parse without error by design, into the zero envelope -> buyer TIN "" -> the
// accept path ([non-reserved-defaults-to-accept]). Adding them would invent a validation rule
// this function does not own.
//
// "contract-suite-cancelled-ctx-wire" is taken verbatim from contract_test.go:314, so this spec
// tracks the bytes the contract suite actually hands an adapter.
func TestMockWire_ParseRejectsEmptyAndNonJSON(t *testing.T) {
	tests := []struct {
		name string
		w    Wire
	}{
		{name: "empty-non-nil-wire", w: Wire{}},
		{name: "nil-wire", w: Wire(nil)},
		{name: "non-json-bytes", w: Wire("contract-suite-cancelled-ctx-wire")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseMockEnvelope(tc.w)
			if err == nil {
				t.Fatalf("AC-8: parseMockEnvelope(%q) returned a nil error, want a non-nil one", string(tc.w))
			}
			if !errors.Is(err, ErrMockUnparseableWire) {
				t.Errorf("AC-8: parseMockEnvelope(%q) error = %v, want it to wrap ErrMockUnparseableWire "+
					"so M5-03-03 can branch with errors.Is rather than on message text", string(tc.w), err)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// AC-1: the pinned BIS constants.
// ---------------------------------------------------------------------------------------

// TestMockWire_CarriesTheBISConstants compares against the CONSTANTS, not against re-typed
// literals. Re-typing the URNs here would only prove this file and mock_wire.go were typed the
// same way once; comparing against the constant pins that whatever those constants are, they
// reach the wire unconditionally, on every corpus shape including the zero Canonical.
func TestMockWire_CarriesTheBISConstants(t *testing.T) {
	for _, tc := range mwCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			wire := mwWire(t, tc.build())

			mwWantContains(t, wire, `"CustomizationID":"`+mockCustomizationID+`"`,
				"AC-1: CustomizationID is unconditional")
			mwWantContains(t, wire, `"ProfileID":"`+mockProfileID+`"`,
				"AC-1: ProfileID is unconditional")
			mwWantContains(t, wire, `"InvoiceTypeCode":"`+mockInvoiceTypeCode+`"`,
				"AC-1: InvoiceTypeCode 380 (commercial invoice) is unconditional")
		})
	}
}

// ---------------------------------------------------------------------------------------
// Extra (not in the story's table; recommended by the Stage-1 architecture).
// ---------------------------------------------------------------------------------------

// TestMockWire_UsesNoDynamicMaps is a STRUCTURAL GUARD, not a red-first spec: it is GREEN
// against the stub type set, because the stub type set is already correct. It is honest about
// that -- framed like TestContractSuite_UsesNarrowT (contract_test.go:14-20) and
// TestValidatorClient_DoesNotImportValidationPackage -- and exists to lock the property against
// a later draft, not to record a transition.
//
// Why it earns its place: [wire-payload] says the envelope is structs only, never
// map[string]any. That constraint is invisible to every byte-level assertion in this file -- a
// map[string]any implementation can produce byte-identical output for any single input and only
// diverges once Go's map iteration order happens to shuffle keys, which is precisely the kind of
// flake that shows up in CI months later and never locally. reflect is the only oracle that can
// see the constraint directly.
//
// An interface field is failed for the same reason: encoding/json marshals a map[string]any
// hidden behind an `any` field exactly as it would a bare one.
func TestMockWire_UsesNoDynamicMaps(t *testing.T) {
	seen := map[reflect.Type]bool{}

	var walk func(typ reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		if seen[typ] {
			return
		}
		seen[typ] = true

		switch typ.Kind() {
		case reflect.Map:
			t.Errorf("[wire-payload]: %s is a %s -- the envelope must be structs only, never a map: "+
				"map iteration order is unspecified, so a map field makes the wire bytes "+
				"non-deterministic (L03) in a way no byte-level assertion in this file can catch", path, typ)
		case reflect.Interface:
			t.Errorf("[wire-payload]: %s is an interface (%s) -- an `any` field is just a map "+
				"smuggled past the type checker; encoding/json marshals it identically", path, typ)
		case reflect.Ptr, reflect.Slice, reflect.Array:
			walk(typ.Elem(), path+"[]")
		case reflect.Struct:
			for i := range typ.NumField() {
				f := typ.Field(i)
				walk(f.Type, path+"."+f.Name)
			}
		}
	}

	walk(reflect.TypeOf(mockEnvelope{}), "mockEnvelope")
}

// ---------------------------------------------------------------------------------------
// QA Mode B, Part 3: adversarial / edge coverage beyond the story's Test Specs table.
// ---------------------------------------------------------------------------------------

// TestMockWire_EmptyStringMoneyIsEmittedNotOmitted is the mirror image of AC-4/
// TestMockWire_NilFieldsAreOmittedNotCoerced: a *string pointing at "" is NOT nil, so
// mockAmountFrom must still build the amount object and emit "value":"" -- only a genuinely
// nil pointer omits the object. If this test and AC-4's ever disagreed, one of them would be
// wrong: the distinction between "no value" (nil) and "an empty value" (pointer-to-"") is
// exactly what AC-4's own doc comment on mockAmount.Value claims to preserve.
func TestMockWire_EmptyStringMoneyIsEmittedNotOmitted(t *testing.T) {
	c := Canonical{InvoiceNumber: "INV-EMPTYSTR", Subtotal: mwStrPtr("")}
	wire := mwWire(t, c)

	mwWantContains(t, wire, `"LineExtensionAmount":{"value":""}`,
		"a non-nil pointer-to-empty-string must build the amount object with value:\"\", not omit it")
	mwWantContains(t, wire, `"TaxExclusiveAmount":{"value":""}`,
		"Subtotal feeds both LineExtensionAmount and TaxExclusiveAmount")
}

// TestMockWire_DecimalEdgeShapesPassThroughVerbatim extends AC-5 with shapes a numeric
// round-trip is especially likely to mangle: a leading minus sign, leading zeros, and a
// value wide enough to overflow float64's mantissa. Each must land on the wire as the exact
// same string, because [money-passes-through-verbatim] means nothing here may parse a number
// at all, wide or not.
func TestMockWire_DecimalEdgeShapesPassThroughVerbatim(t *testing.T) {
	tests := []struct{ name, value string }{
		{"negative", "-1234.56"},
		{"leading-zeros", "007.500"},
		{"negative-leading-zeros", "-007.500"},
		{"wider-than-float64-mantissa", "999999999999999999999999999999.99999999"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Canonical{InvoiceNumber: "INV-DEC", Total: mwStrPtr(tc.value)}
			wire := mwWire(t, c)
			mwWantContains(t, wire, `"PayableAmount":{"value":"`+tc.value+`"}`,
				"AC-5: an edge-shaped decimal string must reach the wire byte-for-byte, un-renormalised")
		})
	}
}

// TestMockWire_InvalidUTF8IsDeterministic covers Name/Description bytes that are not valid
// UTF-8. encoding/json replaces invalid sequences with U+FFFD when marshalling a Go string,
// which is a lossy but DETERMINISTIC transform -- so this asserts the determinism property,
// not a specific rendering (a specific byte sequence would just be pinning encoding/json's
// implementation detail). It also confirms the field is not silently dropped or the whole
// marshal caused to error.
func TestMockWire_InvalidUTF8IsDeterministic(t *testing.T) {
	bad := string([]byte{0xff, 0xfe, 0xfd, 'A', 0x80})
	c := Canonical{
		InvoiceNumber: "INV-BADUTF8",
		Supplier:      Party{Name: mwStrPtr(bad)},
		Lines:         []CanonicalLine{{LineID: "l1", LineNo: 1, Description: mwStrPtr(bad)}},
	}

	w1, err := mockWireFrom(c)
	if err != nil {
		t.Fatalf("mockWireFrom returned an unexpected error on invalid UTF-8 input: %v", err)
	}
	w2, err := mockWireFrom(c)
	if err != nil {
		t.Fatalf("mockWireFrom returned an unexpected error on the second call: %v", err)
	}
	if !bytes.Equal(w1, w2) {
		t.Errorf("invalid UTF-8 input must still marshal deterministically\n1: %s\n2: %s",
			mwTrunc(string(w1)), mwTrunc(string(w2)))
	}

	wire := string(w1)
	mwWantContains(t, wire, `"PartyName"`, "an invalid-UTF8 Name must still produce a PartyName block, not be silently dropped")
	mwWantContains(t, wire, `"Item":{"Name":"`, "an invalid-UTF8 Description must still produce an Item block")
}

// TestMockWire_ManyLinesIsDeterministicAndFast is the 1000-line case: it re-checks the
// determinism property at a scale where an implementation reaching for a map (unordered
// iteration) or doing anything quadratic in the line count would show up either as
// non-reproducible bytes or as a test that visibly hangs.
func TestMockWire_ManyLinesIsDeterministicAndFast(t *testing.T) {
	const n = 1000
	build := func() Canonical {
		lines := make([]CanonicalLine, n)
		for i := 0; i < n; i++ {
			lines[i] = CanonicalLine{
				LineID:   fmt.Sprintf("line-%d", i),
				LineNo:   i + 1,
				Quantity: mwStrPtr("1"),
			}
		}
		return Canonical{InvoiceNumber: "INV-MANYLINES", Lines: lines}
	}

	start := time.Now()
	w1, err := mockWireFrom(build())
	if err != nil {
		t.Fatalf("mockWireFrom returned an unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("building/marshalling %d lines took %s, want well under 2s -- suspect quadratic behaviour", n, elapsed)
	}

	w2, err := mockWireFrom(build()) // fresh instance, equal value -- AC-7/L03 at scale
	if err != nil {
		t.Fatalf("mockWireFrom returned an unexpected error on the fresh instance: %v", err)
	}
	if !bytes.Equal(w1, w2) {
		t.Errorf("marshalling %d equal-but-distinct lines produced different bytes", n)
	}

	if got := strings.Count(string(w1), `"Item"`); got != 0 {
		t.Errorf("no line here has a Description, so no Item block should appear")
	}
	if got := strings.Count(string(w1), `"InvoicedQuantity":"1"`); got != n {
		t.Errorf("expected all %d lines to carry InvoicedQuantity, counted %d", n, got)
	}
}

// TestMockWire_EmptyNonNilLinesAlsoMarshalsToEmptyArray covers Lines set to a non-nil,
// zero-length slice ([]CanonicalLine{}) -- distinct from the nil case AC-3 already covers.
// [wire-lines-empty-not-omitted]'s make()-based construction does not distinguish nil from
// empty on the way in (both have len 0), so both must produce the identical wire shape.
func TestMockWire_EmptyNonNilLinesAlsoMarshalsToEmptyArray(t *testing.T) {
	c := Canonical{InvoiceNumber: "INV-EMPTYLINES", Lines: []CanonicalLine{}}
	if c.Lines == nil {
		t.Fatalf("corpus precondition broken: Lines must be non-nil-but-empty, got nil")
	}

	wire := mwWire(t, c)
	mwWantContains(t, wire, `"InvoiceLine":[]`,
		"a non-nil, zero-length Lines must ALSO marshal to [] -- the same wire shape as a nil Lines")
	mwWantAbsent(t, wire, `"InvoiceLine":null`, "must not regress to null for this shape either")
}

// TestMockWire_LineNoZeroAndNegativePassThrough covers LineNo values outside the documented
// 1..N range ([D10] in canonical.go). buildMockEnvelope has no validation layer of its own --
// it is a pure projection -- so both must render as their verbatim signed decimal string,
// the same as any other int, rather than being treated as "no line number".
func TestMockWire_LineNoZeroAndNegativePassThrough(t *testing.T) {
	c := Canonical{
		InvoiceNumber: "INV-LINENO",
		Lines: []CanonicalLine{
			{LineID: "l-zero", LineNo: 0},
			{LineID: "l-neg", LineNo: -5},
		},
	}
	wire := mwWire(t, c)

	mwWantContains(t, wire, `"ID":"0"`, "LineNo 0 must render as the decimal string \"0\", not be treated as absent")
	mwWantContains(t, wire, `"ID":"-5"`, "a negative LineNo must render verbatim as a signed decimal string")
}

// TestMockWire_IssueDateUsesItsOwnZoneNotUTC pins the deliberate non-normalisation the
// package comment documents: formatting happens in the time.Time's OWN location, never
// through .UTC() first. The two zones must disagree on the CALENDAR DAY for this to prove
// anything -- 2026-01-01 02:00 +05:30 is 2025-12-31 20:30 UTC, so a regression that added
// .UTC() before formatting would silently shift IssueDate a day earlier.
func TestMockWire_IssueDateUsesItsOwnZoneNotUTC(t *testing.T) {
	loc := time.FixedZone("TEST+05:30", 5*3600+30*60)
	local := time.Date(2026, 1, 1, 2, 0, 0, 0, loc)

	if utcDay := local.UTC().Format(mockIssueDateLayout); utcDay == local.Format(mockIssueDateLayout) {
		t.Fatalf("test precondition broken: local and UTC dates must differ for this pin to mean anything, both are %s", utcDay)
	}

	c := Canonical{InvoiceNumber: "INV-TZ", IssueDate: &local}
	wire := mwWire(t, c)

	mwWantContains(t, wire, `"IssueDate":"2026-01-01"`,
		"IssueDate must format in the time's OWN zone (2026-01-01 local), not its UTC equivalent (2025-12-31)")
	mwWantAbsent(t, wire, `"IssueDate":"2025-12-31"`,
		"a .UTC()-normalised regression would silently shift the rendered date a day earlier")
}

// TestMockWire_ParseRejectsStructurallyWrongJSON extends AC-8's coverage without touching
// TestMockWire_ParseRejectsEmptyAndNonJSON, which must stay scoped to exactly empty and
// non-JSON bytes per [non-reserved-defaults-to-accept] (JSON `null` and `{}` are deliberately
// NOT error cases). These are additional shapes that are wrong in a different way: valid JSON
// syntax that is not an object, and JSON truncated mid-token.
func TestMockWire_ParseRejectsStructurallyWrongJSON(t *testing.T) {
	tests := []struct {
		name string
		w    Wire
	}{
		{"truncated-mid-key", Wire(`{"ID"`)},
		{"truncated-mid-string-value", Wire(`{"ID":"INV-00`)},
		{"top-level-json-array", Wire(`[1,2,3]`)},
		{"deeply-nested-array", Wire(strings.Repeat("[", 10000))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseMockEnvelope(tc.w)
			if err == nil {
				t.Fatalf("parseMockEnvelope(%s) returned a nil error, want non-nil", tc.name)
			}
			if !errors.Is(err, ErrMockUnparseableWire) {
				t.Errorf("parseMockEnvelope(%s) error = %v, want it to wrap ErrMockUnparseableWire", tc.name, err)
			}
		})
	}
}
