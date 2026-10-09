package invoice

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

type nrsFixture struct {
	super, app *pgxpool.Pool
	store      *Store
	tenantID   string
	entityID   string
	c          context.Context
}

func newNRSFixture(t *testing.T, label string) nrsFixture {
	t.Helper()
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, label+" tenant")
	entityID := seedEntityWithTIN(t, super, tenantID, "Acme", "SUP-TIN-1")
	c := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	return nrsFixture{super: super, app: app, store: NewStore(app), tenantID: tenantID, entityID: entityID, c: c}
}

func (f nrsFixture) create(t *testing.T, number string, in CreateInput) Invoice {
	t.Helper()
	in.EntityID, in.InvoiceNumber = f.entityID, number
	inv, err := f.store.Create(f.c, in)
	if err != nil {
		t.Fatalf("Create(%s): %v", number, err)
	}
	return inv
}

func (f nrsFixture) get(t *testing.T, id string) Invoice {
	t.Helper()
	inv, err := f.store.Get(f.c, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return inv
}

func (f nrsFixture) forceStatus(t *testing.T, id string, s Status) {
	t.Helper()
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET status = $1 WHERE id = $2`, string(s), id); err != nil {
		t.Fatalf("force status: %v", err)
	}
}

func nrsLine(desc, qty, price string) LineItemInput {
	return LineItemInput{Description: strPtr(desc), Quantity: strPtr(qty), UnitPrice: strPtr(price), LineTotal: strPtr(price), LineTax: strPtr("0.00")}
}

func fullNRSLine() LineItemInput {
	l := nrsLine("Widget", "1", "100.00")
	l.TaxCategory, l.HSNCode, l.ISICCode = strPtr("STANDARD_VAT"), strPtr("8471.30"), strPtr("6201")
	l.ProductCategory, l.ServiceCategory = strPtr("Electronics"), strPtr("Software")
	l.SellersItemIdentification, l.PriceUnit = strPtr("SKU-1"), strPtr("KGM")
	l.TaxPercent, l.BaseQuantity = strPtr("7.5"), strPtr("1")
	return l
}

func fullNRSHeader(in *CreateInput) {
	d1 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	in.InvoiceKind, in.TaxCurrencyCode, in.PaymentStatus = strPtr("B2B"), strPtr("NGN"), strPtr("PENDING")
	in.DueDate, in.TaxPointDate, in.IssueTime = &d1, &d2, strPtr("14:30:05")
	in.SupplierEmail, in.SupplierTelephone, in.SupplierStreet, in.SupplierCity = strPtr("s@x.ng"), strPtr("+2348000000001"), strPtr("1 Marina"), strPtr("Lagos")
	in.SupplierPostalZone, in.SupplierCountry, in.SupplierState, in.SupplierLGA = strPtr("100001"), strPtr("NG"), strPtr("NG-LA"), strPtr("NG-LA-001")
	in.BuyerEmail, in.BuyerTelephone, in.BuyerStreet, in.BuyerCity = strPtr("b@x.ng"), strPtr("+2348000000002"), strPtr("2 Wuse"), strPtr("Abuja")
	in.BuyerPostalZone, in.BuyerCountry, in.BuyerState, in.BuyerLGA = strPtr("900001"), strPtr("NG"), strPtr("NG-FC"), strPtr("NG-FC-001")
}

func TestStoreCreate_NRSFieldsRoundTrip(t *testing.T) {
	f := newNRSFixture(t, "NRS-RT")
	in := CreateInput{LineItems: []LineItemInput{fullNRSLine()}}
	fullNRSHeader(&in)
	created := f.create(t, "NRS-RT-1", in)
	got := f.get(t, created.ID)

	pairs := map[string][2]*string{
		"InvoiceKind": {in.InvoiceKind, got.InvoiceKind}, "TaxCurrencyCode": {in.TaxCurrencyCode, got.TaxCurrencyCode},
		"PaymentStatus": {in.PaymentStatus, got.PaymentStatus}, "IssueTime": {in.IssueTime, got.IssueTime},
		"SupplierEmail": {in.SupplierEmail, got.SupplierEmail}, "SupplierTelephone": {in.SupplierTelephone, got.SupplierTelephone},
		"SupplierStreet": {in.SupplierStreet, got.SupplierStreet}, "SupplierCity": {in.SupplierCity, got.SupplierCity},
		"SupplierPostalZone": {in.SupplierPostalZone, got.SupplierPostalZone}, "SupplierCountry": {in.SupplierCountry, got.SupplierCountry},
		"SupplierState": {in.SupplierState, got.SupplierState}, "SupplierLGA": {in.SupplierLGA, got.SupplierLGA},
		"BuyerEmail": {in.BuyerEmail, got.BuyerEmail}, "BuyerTelephone": {in.BuyerTelephone, got.BuyerTelephone},
		"BuyerStreet": {in.BuyerStreet, got.BuyerStreet}, "BuyerCity": {in.BuyerCity, got.BuyerCity},
		"BuyerPostalZone": {in.BuyerPostalZone, got.BuyerPostalZone}, "BuyerCountry": {in.BuyerCountry, got.BuyerCountry},
		"BuyerState": {in.BuyerState, got.BuyerState}, "BuyerLGA": {in.BuyerLGA, got.BuyerLGA},
	}
	for name, p := range pairs {
		if p[1] == nil || *p[0] != *p[1] {
			t.Errorf("%s = %v, want %q", name, p[1], *p[0])
		}
	}
	if got.DueDate == nil || !got.DueDate.Equal(*in.DueDate) {
		t.Errorf("DueDate = %v, want %v", got.DueDate, in.DueDate)
	}
	if got.TaxPointDate == nil || !got.TaxPointDate.Equal(*in.TaxPointDate) {
		t.Errorf("TaxPointDate = %v, want %v", got.TaxPointDate, in.TaxPointDate)
	}

	if len(got.LineItems) != 1 {
		t.Fatalf("lines = %d, want 1", len(got.LineItems))
	}
	l, w := got.LineItems[0], fullNRSLine()
	want := map[string]struct{ got, want *string }{
		"TaxCategory": {l.TaxCategory, w.TaxCategory}, "HSNCode": {l.HSNCode, w.HSNCode}, "ISICCode": {l.ISICCode, w.ISICCode},
		"ProductCategory": {l.ProductCategory, w.ProductCategory}, "ServiceCategory": {l.ServiceCategory, w.ServiceCategory},
		"SellersItemIdentification": {l.SellersItemIdentification, w.SellersItemIdentification}, "PriceUnit": {l.PriceUnit, w.PriceUnit},
		"TaxPercent": {l.TaxPercent, strPtr("7.50")}, "BaseQuantity": {l.BaseQuantity, strPtr("1.000")},
	}
	for name, p := range want {
		if p.got == nil || *p.got != *p.want {
			t.Errorf("line %s = %v, want %q", name, p.got, *p.want)
		}
	}
}

func TestStoreCreate_IssueTimeNormalisesToSeconds(t *testing.T) {
	f := newNRSFixture(t, "NRS-TIME")
	created := f.create(t, "NRS-TIME-1", CreateInput{IssueTime: strPtr("14:30")})
	if got := f.get(t, created.ID).IssueTime; got == nil || *got != "14:30:00" {
		t.Errorf("IssueTime = %v, want 14:30:00", got)
	}
}

func TestStoreCreate_SupplierContactIsTheCallersIdentityIsTheEntitys(t *testing.T) {
	f := newNRSFixture(t, "NRS-SUP")
	created := f.create(t, "NRS-SUP-1", CreateInput{
		SupplierName: strPtr("Other"), SupplierEmail: strPtr("s@x.ng"), SupplierState: strPtr("NG-LA"),
	})
	got := f.get(t, created.ID)
	if got.SupplierName == nil || *got.SupplierName != "Acme" {
		t.Errorf("SupplierName = %v, want the entity's Acme", got.SupplierName)
	}
	if got.SupplierEmail == nil || *got.SupplierEmail != "s@x.ng" || got.SupplierState == nil || *got.SupplierState != "NG-LA" {
		t.Errorf("supplier email/state = %v/%v, want as sent", got.SupplierEmail, got.SupplierState)
	}
}

func TestStoreEdit_OneNRSHeaderFieldWritesAuditsAndDemotes(t *testing.T) {
	f := newNRSFixture(t, "NRS-EDIT")
	inv := f.create(t, "NRS-EDIT-1", CreateInput{})
	if _, err := f.store.Transition(f.c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	got, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{BuyerState: strPtr("NG-FC")}})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if got.BuyerState == nil || *got.BuyerState != "NG-FC" {
		t.Errorf("BuyerState = %v, want NG-FC", got.BuyerState)
	}
	if got.Status != StatusDraft {
		t.Errorf("status = %q, want draft", got.Status)
	}
	if fields := auditFields(t, f.app, f.tenantID, "invoice.updated"); !reflect.DeepEqual(fields, []string{"buyer_state"}) {
		t.Errorf("audit fields = %v, want [buyer_state]", fields)
	}
}

func TestStoreEdit_IdenticalNRSResendIsANoOp(t *testing.T) {
	f := newNRSFixture(t, "NRS-NOOP")
	inv := f.create(t, "NRS-NOOP-1", CreateInput{InvoiceKind: strPtr("B2B")})
	if _, err := f.store.Transition(f.c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	before := auditCount(t, f.app, f.tenantID, "invoice.updated")
	got, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{InvoiceKind: strPtr("B2B")}})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("status = %q, want validated", got.Status)
	}
	if n := auditCount(t, f.app, f.tenantID, "invoice.updated"); n != before {
		t.Errorf("invoice.updated rows = %d, want %d", n, before)
	}
}

func TestStoreEdit_LineReplaceCarriesNRSLineFields(t *testing.T) {
	f := newNRSFixture(t, "NRS-LINES")
	inv := f.create(t, "NRS-LINES-1", CreateInput{})
	a, b := nrsLine("A", "1", "10.00"), nrsLine("B", "2", "20.00")
	a.TaxCategory, a.PriceUnit = strPtr("STANDARD_VAT"), strPtr("KGM")
	b.TaxCategory, b.PriceUnit = strPtr("ZERO_VAT"), strPtr("EA")
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{a, b}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	lines := f.get(t, inv.ID).LineItems
	if len(lines) != 2 || *lines[0].TaxCategory != "STANDARD_VAT" || *lines[0].PriceUnit != "KGM" ||
		*lines[1].TaxCategory != "ZERO_VAT" || *lines[1].PriceUnit != "EA" {
		t.Errorf("lines = %+v, want the categories and units in order", lines)
	}
}

func TestStoreEdit_ClearSentinelNullsAnNRSField(t *testing.T) {
	f := newNRSFixture(t, "NRS-CLEAR")
	in := CreateInput{}
	fullNRSHeader(&in)
	inv := f.create(t, "NRS-CLEAR-1", in)
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{
		BuyerEmail: ClearText, IssueTime: ClearText, DueDate: ClearDate, TaxPointDate: ClearDate,
	}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got := f.get(t, inv.ID)
	if got.BuyerEmail != nil || got.IssueTime != nil || got.DueDate != nil || got.TaxPointDate != nil {
		t.Errorf("after clear: email=%v time=%v due=%v taxpoint=%v, want all nil", got.BuyerEmail, got.IssueTime, got.DueDate, got.TaxPointDate)
	}
	if got.BuyerCity == nil {
		t.Error("BuyerCity was cleared too; only the named fields should be")
	}
}

func TestUpdateContentTx_RefusesACopiedClearSentinelOnNRSFields(t *testing.T) {
	f := newNRSFixture(t, "NRS-COPY")
	in := CreateInput{BuyerEmail: strPtr("b@x.ng")}
	fullNRSHeader(&in)
	inv := f.create(t, "NRS-COPY-1", in)

	copiedText, copiedDate := *ClearText, *ClearDate
	for name, u := range map[string]UpdateInput{
		"buyer_email": {BuyerEmail: &copiedText},
		"issue_time":  {IssueTime: &copiedText},
		"due_date":    {DueDate: &copiedDate},
	} {
		_, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: u})
		if !errors.Is(err, ErrValidation) || err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want ErrValidation naming the column", name, err)
		}
	}
	if got := f.get(t, inv.ID); got.BuyerEmail == nil || *got.BuyerEmail != "b@x.ng" || got.IssueTime == nil || got.DueDate == nil {
		t.Error("row changed after a refused sentinel copy")
	}
}

func categorised(desc, cat string) LineItemInput {
	l := nrsLine(desc, "1", "10.00")
	l.TaxCategory = strPtr(cat)
	return l
}

func legacyOf(l LineItem) LineItemInput {
	return LineItemInput{Description: l.Description, Quantity: l.Quantity, UnitPrice: l.UnitPrice, LineTotal: l.LineTotal, LineTax: l.LineTax}
}

func TestStoreEdit_UnchangedLinesKeepTheirNRSFields(t *testing.T) {
	f := newNRSFixture(t, "NRS-KEEP")
	inv := f.create(t, "NRS-KEEP-1", CreateInput{LineItems: []LineItemInput{
		categorised("one", "STANDARD_VAT"), categorised("two", "ZERO_VAT"), categorised("three", "EXEMPTED"),
	}})
	stored := f.get(t, inv.ID).LineItems
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{legacyOf(stored[2]), legacyOf(stored[0])}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got := f.get(t, inv.ID).LineItems
	if len(got) != 2 || *got[0].TaxCategory != "EXEMPTED" || *got[1].TaxCategory != "STANDARD_VAT" {
		t.Errorf("lines = %+v, want EXEMPTED then STANDARD_VAT", got)
	}
}

func TestStoreEdit_LineSendingNRSFieldsIsWrittenAsSent(t *testing.T) {
	f := newNRSFixture(t, "NRS-SENT")
	inv := f.create(t, "NRS-SENT-1", CreateInput{LineItems: []LineItemInput{categorised("one", "STANDARD_VAT")}})
	stored := f.get(t, inv.ID).LineItems
	sent := legacyOf(stored[0])
	sent.TaxCategory = strPtr("ZERO_VAT")
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{sent}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if got := f.get(t, inv.ID).LineItems[0].TaxCategory; got == nil || *got != "ZERO_VAT" {
		t.Errorf("TaxCategory = %v, want ZERO_VAT", got)
	}
}

func TestStoreEdit_NumericFormatDoesNotBreakTheCarry(t *testing.T) {
	f := newNRSFixture(t, "NRS-FMT")
	l := categorised("one", "STANDARD_VAT")
	l.UnitPrice = strPtr("100.00")
	inv := f.create(t, "NRS-FMT-1", CreateInput{LineItems: []LineItemInput{l}})
	stored := f.get(t, inv.ID).LineItems[0]
	sent := legacyOf(stored)
	sent.UnitPrice = strPtr("100")
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{sent}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if got := f.get(t, inv.ID).LineItems[0].TaxCategory; got == nil || *got != "STANDARD_VAT" {
		t.Errorf("TaxCategory = %v, want kept", got)
	}
}

func TestStoreEdit_LineIDCarriesNRSFieldsThroughAContentEdit(t *testing.T) {
	f := newNRSFixture(t, "NRS-ID")
	la := categorised("A", "STANDARD_VAT")
	la.PriceUnit = strPtr("KGM")
	inv := f.create(t, "NRS-ID-1", CreateInput{LineItems: []LineItemInput{la, nrsLine("B", "1", "5.00")}})
	stored := f.get(t, inv.ID).LineItems

	a := nrsLine("A renamed", "9", "99.00")
	a.ID = &stored[0].ID
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{legacyOf(stored[1]), a}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got := f.get(t, inv.ID).LineItems
	if got[1].TaxCategory == nil || *got[1].TaxCategory != "STANDARD_VAT" || got[1].PriceUnit == nil || *got[1].PriceUnit != "KGM" {
		t.Errorf("moved line = %+v, want STANDARD_VAT and KGM kept at its new position", got[1])
	}
	if got[0].TaxCategory != nil {
		t.Errorf("line B gained TaxCategory %v", *got[0].TaxCategory)
	}
}

func TestStoreEdit_LineIDKeepsOmittedFieldsAndTakesSentOnes(t *testing.T) {
	f := newNRSFixture(t, "NRS-ID2")
	inv := f.create(t, "NRS-ID2-1", CreateInput{LineItems: []LineItemInput{fullNRSLine()}})
	stored := f.get(t, inv.ID).LineItems[0]
	in := legacyOf(stored)
	in.ID, in.TaxCategory = &stored.ID, strPtr("ZERO_VAT")
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{in}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	got := f.get(t, inv.ID).LineItems[0]
	want := stored
	want.TaxCategory = strPtr("ZERO_VAT")
	want.ID = got.ID
	if !reflect.DeepEqual(got, want) {
		t.Errorf("line = %+v, want %+v", got, want)
	}
}

func TestStoreEdit_ForeignLineIDIsRefused(t *testing.T) {
	f := newNRSFixture(t, "NRS-FOREIGN")
	inv := f.create(t, "NRS-FOREIGN-1", CreateInput{LineItems: []LineItemInput{categorised("mine", "STANDARD_VAT")}})
	other := f.create(t, "NRS-FOREIGN-2", CreateInput{LineItems: []LineItemInput{categorised("other", "ZERO_VAT")}})
	otherLine := f.get(t, other.ID).LineItems[0].ID

	tenantB := seedTenant(t, f.super, "NRS-FOREIGN B")
	entityB := seedEntity(t, f.super, tenantB, "B Co")
	cB := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB})
	invB, err := f.store.Create(cB, CreateInput{EntityID: entityB, InvoiceNumber: "NRS-FOREIGN-B", LineItems: []LineItemInput{categorised("b", "EXEMPTED")}})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}
	var tenantBLine string
	if err := f.super.QueryRow(context.Background(), `SELECT id FROM line_items WHERE invoice_id = $1`, invB.ID).Scan(&tenantBLine); err != nil {
		t.Fatalf("read tenant B line: %v", err)
	}

	staleID := f.get(t, inv.ID).LineItems[0].ID
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{categorised("mine", "STANDARD_VAT")}}); err != nil {
		t.Fatalf("replace line: %v", err)
	}
	mine := f.get(t, inv.ID).LineItems[0]

	entry := func(id string) LineItemInput { l := legacyOf(mine); l.ID = &id; return l }
	cases := map[string][]LineItemInput{
		"another invoice, same tenant": {entry(otherLine)},
		"another tenant":               {entry(tenantBLine)},
		"replaced line":                {entry(staleID)},
		"not a uuid":                   {entry("not-a-uuid")},
		"id used twice":                {entry(mine.ID), entry(mine.ID)},
	}
	beforeAudit := auditCount(t, f.app, f.tenantID, "invoice.updated")
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{BuyerCity: strPtr("Changed")}, LineItems: &lines})
			if !errors.Is(err, ErrUnknownLineID) {
				t.Fatalf("err = %v, want ErrUnknownLineID", err)
			}
			got := f.get(t, inv.ID)
			if got.BuyerCity != nil || len(got.LineItems) != 1 || got.LineItems[0].ID != mine.ID || got.Status != StatusDraft {
				t.Errorf("invoice changed after a refused edit: %+v", got)
			}
			if n := auditCount(t, f.app, f.tenantID, "invoice.updated"); n != beforeAudit {
				t.Errorf("invoice.updated rows = %d, want %d", n, beforeAudit)
			}
		})
	}
}

func TestStoreEdit_EntryWithoutIDAndChangedContentLosesItsNRSFields(t *testing.T) {
	f := newNRSFixture(t, "NRS-LOSS")
	inv := f.create(t, "NRS-LOSS-1", CreateInput{LineItems: []LineItemInput{categorised("one", "STANDARD_VAT")}})
	stored := f.get(t, inv.ID).LineItems[0]
	changed := legacyOf(stored)
	changed.Quantity = strPtr("5")
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{changed}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if got := f.get(t, inv.ID).LineItems[0].TaxCategory; got != nil {
		t.Errorf("TaxCategory = %q, want nil (D23 residual)", *got)
	}
}

func TestStoreCreate_MalformedIssueTimeIsValidationAndWritesNothing(t *testing.T) {
	f := newNRSFixture(t, "NRS-BADTIME")
	_, err := f.store.Create(f.c, CreateInput{EntityID: f.entityID, InvoiceNumber: "NRS-BADTIME-1", IssueTime: strPtr("nope")})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE entity_id = $1`, f.entityID); n != 0 {
		t.Errorf("invoices for the entity = %d, want 0", n)
	}
}

func TestStoreUpdate_OutOfRangeIssueTimeIsValidation(t *testing.T) {
	f := newNRSFixture(t, "NRS-BADTIME2")
	inv := f.create(t, "NRS-BADTIME2-1", CreateInput{IssueTime: strPtr("10:00:00")})
	_, err := f.store.Update(f.c, inv.ID, UpdateInput{IssueTime: strPtr("25:00:00")})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if got := f.get(t, inv.ID).IssueTime; got == nil || *got != "10:00:00" {
		t.Errorf("IssueTime = %v, want unchanged 10:00:00", got)
	}
}

func TestStoreCreate_MalformedLineTaxPercentIsValidation(t *testing.T) {
	f := newNRSFixture(t, "NRS-BADPCT")
	l := nrsLine("x", "1", "1.00")
	l.TaxPercent = strPtr("abc")
	_, err := f.store.Create(f.c, CreateInput{EntityID: f.entityID, InvoiceNumber: "NRS-BADPCT-1", LineItems: []LineItemInput{l}})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE entity_id = $1`, f.entityID); n != 0 {
		t.Errorf("invoices for the entity = %d, want 0", n)
	}
}

func TestStoreGet_InvoiceWithoutNRSFieldsReadsNil(t *testing.T) {
	f := newNRSFixture(t, "NRS-NIL")
	inv := f.create(t, "NRS-NIL-1", CreateInput{BuyerName: strPtr("Buyer"), LineItems: []LineItemInput{nrsLine("x", "1", "1.00")}})
	got := f.get(t, inv.ID)
	rv := reflect.ValueOf(got)
	for _, name := range []string{"InvoiceKind", "TaxCurrencyCode", "DueDate", "IssueTime", "TaxPointDate", "PaymentStatus",
		"SupplierEmail", "SupplierTelephone", "SupplierStreet", "SupplierCity", "SupplierPostalZone", "SupplierCountry", "SupplierState", "SupplierLGA",
		"BuyerEmail", "BuyerTelephone", "BuyerStreet", "BuyerCity", "BuyerPostalZone", "BuyerCountry", "BuyerState", "BuyerLGA"} {
		if !rv.FieldByName(name).IsNil() {
			t.Errorf("%s is not nil", name)
		}
	}
	lv := reflect.ValueOf(got.LineItems[0])
	for _, name := range []string{"TaxCategory", "HSNCode", "ISICCode", "ProductCategory", "ServiceCategory",
		"SellersItemIdentification", "PriceUnit", "TaxPercent", "BaseQuantity"} {
		if !lv.FieldByName(name).IsNil() {
			t.Errorf("line %s is not nil", name)
		}
	}
}
