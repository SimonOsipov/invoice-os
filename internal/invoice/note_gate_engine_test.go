package invoice

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// negV6Lists are the NRS codes the 2027 note below uses.
var negV6Lists = map[string][]string{
	"currencies": {"NGN"}, "countries": {"NG"}, "states": {"NG-LA", "NG-FC"},
	"lgas": {"NG-LA-AGE", "NG-FC-AML"}, "invoice-quantity-codes": {"EA"},
	"hs-codes": {"8471.30"}, "services-codes": {"6201"}, "tax-categories": {"STANDARD_VAT", "ZERO_VAT"},
}

// negSeedLists inserts the v5 code lists; cleanup deletes only the rows this call inserted.
func negSeedLists(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	type row struct{ list, code string }
	var inserted []row
	for list, codes := range negV6Lists {
		for _, code := range codes {
			tag, err := super.Exec(ctx,
				`INSERT INTO nrs_codes (list, code, entries) VALUES ($1, $2, '[{}]') ON CONFLICT DO NOTHING`, list, code)
			if err != nil {
				t.Fatalf("seed nrs_codes %s/%s: %v", list, code, err)
			}
			if tag.RowsAffected() == 1 {
				inserted = append(inserted, row{list, code})
			}
		}
	}
	t.Cleanup(func() {
		for _, r := range inserted {
			_, _ = super.Exec(context.Background(), `DELETE FROM nrs_codes WHERE list = $1 AND code = $2`, r.list, r.code)
		}
	})
}

// negInput2027 is a v6-compliant B2B document dated 2027-01-15.
func negInput2027(entity, number string, typ submission.DocumentType, original string) CreateInput {
	date := time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)
	line := func(category, price, percent, tax string) LineItemInput {
		return LineItemInput{
			Description: strPtr("Laptop"), Quantity: strPtr("1"), UnitPrice: strPtr(price), LineTotal: strPtr(price),
			LineTax: strPtr(tax), TaxCategory: strPtr(category), TaxPercent: strPtr(percent),
			HSNCode: strPtr("8471.30"), ProductCategory: strPtr("Computers"), SellersItemIdentification: strPtr("SKU-1"),
			PriceUnit: strPtr("EA"), BaseQuantity: strPtr("1"),
		}
	}
	return CreateInput{
		EntityID: entity, InvoiceNumber: number, IssueDate: &date, Currency: strPtr("NGN"),
		InvoiceKind: strPtr("B2B"), TaxCurrencyCode: strPtr("NGN"),
		SupplierTIN: strPtr("12345678-0001"), SupplierName: strPtr("Acme Nigeria Ltd"), SupplierEmail: strPtr("accounts@acme.ng"),
		SupplierStreet: strPtr("1 Marina Road"), SupplierCity: strPtr("Lagos"), SupplierPostalZone: strPtr("100001"),
		SupplierLGA: strPtr("NG-LA-AGE"), SupplierState: strPtr("NG-LA"), SupplierCountry: strPtr("NG"),
		BuyerTIN: strPtr("87654321-0002"), BuyerName: strPtr("Beta Ltd"), BuyerEmail: strPtr("ap@beta.ng"),
		BuyerStreet: strPtr("2 Side Street"), BuyerCity: strPtr("Abuja"), BuyerPostalZone: strPtr("900001"),
		BuyerLGA: strPtr("NG-FC-AML"), BuyerState: strPtr("NG-FC"), BuyerCountry: strPtr("NG"),
		Subtotal: strPtr("150.00"), VAT: strPtr("7.50"), Total: strPtr("157.50"),
		DocumentType: typ, OriginalInvoiceID: &original,
		LineItems: []LineItemInput{line("STANDARD_VAT", "100.00", "7.50", "7.50"), line("ZERO_VAT", "50.00", "0", "0.00")},
	}
}

func (f ngFix) create(in CreateInput) string {
	f.t.Helper()
	inv, err := f.store.Create(f.ctx, in)
	if err != nil {
		f.t.Fatalf("create %s: %v", in.InvoiceNumber, err)
	}
	return inv.ID
}

func (f ngFix) stampedVersion(id string) int {
	f.t.Helper()
	var v int
	if err := f.super.QueryRow(context.Background(),
		`SELECT v.version FROM invoices i JOIN rule_set_versions v ON v.id = i.rule_set_version_id WHERE i.id = $1`, id).Scan(&v); err != nil {
		f.t.Fatalf("read stamped version of %s: %v", id, err)
	}
	return v
}

func TestGateValidate_A2027NoteIsStampedV6(t *testing.T) {
	f := newNGFix(t, "NGE 2027", "1000.00")
	negSeedLists(t, f.super)
	id := f.create(negInput2027(f.entity, "NGE-27-C", submission.CreditNote, f.orig))

	got, version, err := f.realGate().Validate(f.ctx, id)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("status = %s %v, want validated", got.Status, ngKeys(ngStored(t, got)))
	}
	if version != 6 || f.stampedVersion(id) != 6 {
		t.Errorf("returned version %d, stamped %d, want 6", version, f.stampedVersion(id))
	}
}

func TestGateValidate_A2026NoteGetsTheStructuralChecksUnderV4(t *testing.T) {
	f := newNGFix(t, "NGE 2026", "100.00")
	gate := f.realGate()
	id := f.credit("NGE-26-C", "150.00")

	got, version, err := gate.Validate(f.ctx, id)
	if err != nil {
		t.Fatalf("Validate over the cap: %v", err)
	}
	if got.Status != StatusDraft || !slices.Contains(ngKeys(ngStored(t, got)), ruleCreditNoteOverCap) {
		t.Errorf("over cap = %s %v, want draft with %s", got.Status, ngKeys(ngStored(t, got)), ruleCreditNoteOverCap)
	}
	if got.Total == nil || *got.Total != "150.00" {
		t.Errorf("stored total = %v, want 150.00", got.Total)
	}
	if version != 4 || f.stampedVersion(id) != 4 {
		t.Errorf("returned version %d, stamped %d, want 4", version, f.stampedVersion(id))
	}

	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET total = 60.00 WHERE id = $1`, id); err != nil {
		t.Fatalf("lower total: %v", err)
	}
	got, version, err = gate.Validate(f.ctx, id)
	if err != nil {
		t.Fatalf("Validate within the cap: %v", err)
	}
	if got.Status != StatusValidated || version != 4 {
		t.Errorf("within cap = %s v%d %v, want validated v4", got.Status, version, ngKeys(ngStored(t, got)))
	}
}

func TestGateValidate_NoIRNBlocksOnBothDates(t *testing.T) {
	f := newNGFix(t, "NGE noirn", "1000.00")
	negSeedLists(t, f.super)
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET irn = NULL WHERE id = $1`, f.orig); err != nil {
		t.Fatalf("clear irn: %v", err)
	}
	gate := f.realGate()

	n26 := f.credit("NGE-NI-26", "60.00")
	n27 := f.create(negInput2027(f.entity, "NGE-NI-27", submission.CreditNote, f.orig))

	for _, c := range []struct {
		name string
		id   string
		want []string
		not  []string
	}{
		{"2026", n26, []string{ruleNoteOriginalNoIRN}, []string{"billing-reference-irn-required"}},
		{"2027", n27, []string{ruleNoteOriginalNoIRN, "billing-reference-irn-required"}, nil},
	} {
		got, _, err := gate.Validate(f.ctx, c.id)
		if err != nil {
			t.Fatalf("%s: Validate: %v", c.name, err)
		}
		keys := ngKeys(ngStored(t, got))
		if got.Status != StatusDraft {
			t.Errorf("%s: status = %s, want draft", c.name, got.Status)
		}
		for _, k := range c.want {
			if !slices.Contains(keys, k) {
				t.Errorf("%s: stored %v, want %s", c.name, keys, k)
			}
		}
		for _, k := range c.not {
			if slices.Contains(keys, k) {
				t.Errorf("%s: stored %v, want no %s", c.name, keys, k)
			}
		}
	}
}
