package importer

import (
	"context"
	"testing"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// btPanics reports whether fn panics; on the zero Service a panic is the lookup reaching the nil store.
func btPanics(fn func()) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	fn()
	return false
}

func TestFillBuyerType_NoBuyerTINLooksNothingUp(t *testing.T) {
	blank := "  "
	for name, tin := range map[string]*string{"nil": nil, "blank": &blank} {
		t.Run(name, func(t *testing.T) {
			in := &invoice.CreateInput{BuyerTIN: tin}
			var err error
			if btPanics(func() { err = (&Service{}).fillBuyerType(context.Background(), in) }) {
				t.Fatal("fillBuyerType looked up with no buyer TIN (the zero Service has a nil store)")
			}
			if err != nil || in.InvoiceKind != nil {
				t.Errorf("err = %v, InvoiceKind = %v, want nil, nil", err, in.InvoiceKind)
			}
		})
	}

	// Control: a printed TIN does look up, so the zero Service's nil store panics.
	tin := "87654321-0001"
	if !btPanics(func() { _ = (&Service{}).fillBuyerType(context.Background(), &invoice.CreateInput{BuyerTIN: &tin}) }) {
		t.Error("control: fillBuyerType with a buyer TIN and no invoice kind did not look up")
	}
}

func TestFillBuyerType_ASetBuyerTypeIsNeverOverridden(t *testing.T) {
	tin, kind := "87654321-0001", "B2C" // B2C: customers.buyer_type CHECK, migrations/20261011140051_buyer_directory.sql
	in := &invoice.CreateInput{BuyerTIN: &tin, InvoiceKind: &kind}
	var err error
	if btPanics(func() { err = (&Service{}).fillBuyerType(context.Background(), in) }) {
		t.Fatal("fillBuyerType looked up although invoice_kind is set (the zero Service has a nil store)")
	}
	if err != nil || in.InvoiceKind == nil || *in.InvoiceKind != "B2C" {
		t.Errorf("err = %v, InvoiceKind = %v, want nil, B2C", err, in.InvoiceKind)
	}

	// Control: with the kind unset the same input does look up.
	if !btPanics(func() { _ = (&Service{}).fillBuyerType(context.Background(), &invoice.CreateInput{BuyerTIN: &tin}) }) {
		t.Error("control: fillBuyerType with a buyer TIN and no invoice kind did not look up")
	}
}
