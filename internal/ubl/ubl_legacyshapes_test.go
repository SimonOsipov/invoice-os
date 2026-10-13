package ubl_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// legacyShapes use only pre-NRS Canonical fields; the goldens were rendered at 10b2e701.
func legacyShapes(t *testing.T) map[string]submission.Canonical {
	t.Helper()
	s := ublStr
	lagos := time.FixedZone("WAT", 3600)
	base := func() submission.Canonical { return completeCanonical(t) }
	out := map[string]submission.Canonical{}

	m := base()
	m.Supplier, m.Buyer = submission.Party{Name: s("S")}, submission.Party{Name: s("B")}
	m.Subtotal, m.VAT, m.Total = nil, nil, nil
	m.Lines = []submission.CanonicalLine{{LineNo: 1}}
	out["minimal"] = m

	v := base()
	v.VAT = nil
	out["nil_vat"] = v

	n := base()
	n.Subtotal, n.Total = nil, nil
	out["only_vat"] = n

	lv := base()
	lv.Lines = []submission.CanonicalLine{
		{LineNo: 1, Quantity: s("3.000")},
		{LineNo: 2, Description: s("Only desc")},
		{LineNo: 3, UnitPrice: s("9.99")},
		{LineNo: 4, LineTax: s("1.00")},
		{LineNo: 5, LineTotal: s("5.00")},
		{LineNo: 7, Description: s(""), Quantity: s("")},
		{LineNo: 0},
	}
	out["line_variants"] = lv

	e := base()
	e.InvoiceNumber = `INV-<&>"'-1`
	e.Supplier = submission.Party{TIN: s(`T&<1>`), Name: s(`A & B "Ltd" <x> 'y' ]]>`)}
	e.Buyer = submission.Party{Name: s("tab\there\nnl")}
	e.Lines[0].Description = s(`W&<idget> "q" 'a' ]]>`)
	e.Lines[0].Quantity = s("1<2")
	out["escaping"] = e

	z := base()
	loc := time.Date(2026, 8, 6, 23, 30, 0, 0, lagos)
	z.IssueDate = &loc
	z.Currency = s(" USD ")
	z.VAT, z.Subtotal, z.Total = s(""), s("-5.00"), s("0")
	out["zone_blank_amounts"] = z

	return out
}

func TestRender_LegacyShapesAreByteIdenticalToHead(t *testing.T) {
	shapes := legacyShapes(t)
	if len(shapes) < 6 {
		t.Fatalf("%d legacy shapes", len(shapes))
	}
	for name, c := range shapes {
		t.Run(name, func(t *testing.T) {
			got := mustRender(t, c)
			want, err := os.ReadFile(filepath.Join("testdata", "legacy_"+name+".xml"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("render differs from the 10b2e701 golden:\n%s", got)
			}
		})
	}
}
