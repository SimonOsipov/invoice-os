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
	in := CreateInput{SupplierName: strPtr("Other"), SupplierTIN: strPtr("OTHER-TIN")}
	fullNRSHeader(&in)
	created := f.create(t, "NRS-SUP-1", in)
	got := f.get(t, created.ID)
	if got.SupplierName == nil || *got.SupplierName != "Acme" {
		t.Errorf("SupplierName = %v, want the entity's Acme", got.SupplierName)
	}
	if got.SupplierTIN == nil || *got.SupplierTIN == "OTHER-TIN" {
		t.Errorf("SupplierTIN = %v, want the entity's, not the caller's", got.SupplierTIN)
	}
	contact := []string{"SupplierEmail", "SupplierTelephone", "SupplierStreet", "SupplierCity",
		"SupplierPostalZone", "SupplierCountry", "SupplierState", "SupplierLGA"}
	for _, field := range contact {
		sent, stored := headerValue(Invoice{SupplierEmail: in.SupplierEmail, SupplierTelephone: in.SupplierTelephone, SupplierStreet: in.SupplierStreet,
			SupplierCity: in.SupplierCity, SupplierPostalZone: in.SupplierPostalZone, SupplierCountry: in.SupplierCountry,
			SupplierState: in.SupplierState, SupplierLGA: in.SupplierLGA}, field), headerValue(got, field)
		if sent == "<nil>" || stored != sent {
			t.Errorf("%s = %q, want %q as sent", field, stored, sent)
		}
	}
}

var nrsHeaderColumns = []struct{ field, col string }{
	{"InvoiceKind", "invoice_kind"}, {"TaxCurrencyCode", "tax_currency_code"}, {"DueDate", "due_date"},
	{"IssueTime", "issue_time"}, {"TaxPointDate", "tax_point_date"}, {"PaymentStatus", "payment_status"},
	{"SupplierEmail", "supplier_email"}, {"SupplierTelephone", "supplier_telephone"}, {"SupplierStreet", "supplier_street"},
	{"SupplierCity", "supplier_city"}, {"SupplierPostalZone", "supplier_postal_zone"}, {"SupplierCountry", "supplier_country"},
	{"SupplierState", "supplier_state"}, {"SupplierLGA", "supplier_lga"},
	{"BuyerEmail", "buyer_email"}, {"BuyerTelephone", "buyer_telephone"}, {"BuyerStreet", "buyer_street"},
	{"BuyerCity", "buyer_city"}, {"BuyerPostalZone", "buyer_postal_zone"}, {"BuyerCountry", "buyer_country"},
	{"BuyerState", "buyer_state"}, {"BuyerLGA", "buyer_lga"},
}

// headerValue renders a header pointer field of inv the way an edit sends it.
func headerValue(inv Invoice, field string) string {
	v := reflect.ValueOf(inv).FieldByName(field)
	if v.IsNil() {
		return "<nil>"
	}
	if d, ok := v.Interface().(*time.Time); ok {
		return d.Format("2006-01-02")
	}
	return *v.Interface().(*string)
}

// Each of the 22 fields alone: written, named in `fields`, verdict demoted, and the 21 others untouched.
func TestStoreEdit_OneNRSHeaderFieldWritesAuditsAndDemotes(t *testing.T) {
	if len(nrsHeaderColumns) != 22 {
		t.Fatalf("table has %d fields, want 22", len(nrsHeaderColumns))
	}
	f := newNRSFixture(t, "NRS-EDIT")
	for i, c := range nrsHeaderColumns {
		t.Run(c.col, func(t *testing.T) {
			in := CreateInput{}
			fullNRSHeader(&in)
			inv := f.create(t, "NRS-EDIT-"+c.col, in)
			if _, err := f.store.Transition(f.c, inv.ID, StatusValidated); err != nil {
				t.Fatalf("Transition: %v", err)
			}
			before := f.get(t, inv.ID)
			var u UpdateInput
			fv := reflect.ValueOf(&u).Elem().FieldByName(c.field)
			want := "changed" + strings.Repeat("x", i)
			switch fv.Type() {
			case reflect.TypeOf((*time.Time)(nil)):
				d := time.Date(2027, 3, 1+i%27, 0, 0, 0, 0, time.UTC)
				fv.Set(reflect.ValueOf(&d))
				want = d.Format("2006-01-02")
			default:
				if c.field == "IssueTime" {
					want = "01:02:03"
				}
				fv.Set(reflect.ValueOf(&want))
			}
			got, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: u})
			if err != nil {
				t.Fatalf("Edit: %v", err)
			}
			if v := headerValue(got, c.field); v != want {
				t.Errorf("%s = %q, want %q", c.field, v, want)
			}
			for _, o := range nrsHeaderColumns {
				if o.field != c.field && headerValue(got, o.field) != headerValue(before, o.field) {
					t.Errorf("%s changed from %q to %q while only %s was edited", o.field, headerValue(before, o.field), headerValue(got, o.field), c.field)
				}
			}
			if got.Status != StatusDraft {
				t.Errorf("status = %q, want draft", got.Status)
			}
			if fields := auditFields(t, f.app, f.tenantID, "invoice.updated"); !reflect.DeepEqual(fields, []string{c.col}) {
				t.Errorf("audit fields = %v, want [%s]", fields, c.col)
			}
		})
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
		"buyer_email":    {BuyerEmail: &copiedText},
		"issue_time":     {IssueTime: &copiedText},
		"due_date":       {DueDate: &copiedDate},
		"tax_point_date": {TaxPointDate: &copiedDate},
		"supplier_lga":   {SupplierLGA: &copiedText},
	} {
		_, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: u})
		if !errors.Is(err, ErrValidation) || err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want ErrValidation naming the column", name, err)
		}
	}
	if got := f.get(t, inv.ID); got.BuyerEmail == nil || *got.BuyerEmail != "b@x.ng" || got.IssueTime == nil || got.DueDate == nil || got.TaxPointDate == nil || got.SupplierLGA == nil {
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

var nrsLineColumns = []string{"TaxCategory", "HSNCode", "ISICCode", "ProductCategory", "ServiceCategory",
	"SellersItemIdentification", "PriceUnit", "TaxPercent", "BaseQuantity"}

func lineValue(l LineItem, field string) string {
	p := reflect.ValueOf(l).FieldByName(field).Interface().(*string)
	if p == nil {
		return "<nil>"
	}
	return *p
}

// Each of the 9 line fields alone, sent with the line's id: written, the other 8 kept, the verdict demoted.
func TestStoreEdit_EachNRSLineFieldSentWithIDIsWrittenAndMovesTheVerdict(t *testing.T) {
	f := newNRSFixture(t, "NRS-LINEFIELD")
	for i, field := range nrsLineColumns {
		t.Run(field, func(t *testing.T) {
			inv := f.create(t, "NRS-LINEFIELD-"+field, CreateInput{LineItems: []LineItemInput{fullNRSLine()}})
			if _, err := f.store.Transition(f.c, inv.ID, StatusValidated); err != nil {
				t.Fatalf("Transition: %v", err)
			}
			stored := f.get(t, inv.ID).LineItems[0]
			sent := legacyOf(stored)
			sent.ID = &stored.ID
			want := "changed" + strings.Repeat("x", i)
			switch field {
			case "TaxPercent":
				want = "9.50"
			case "BaseQuantity":
				want = "2.000"
			}
			reflect.ValueOf(&sent).Elem().FieldByName(field).Set(reflect.ValueOf(&want))
			if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{sent}}); err != nil {
				t.Fatalf("Edit: %v", err)
			}
			got := f.get(t, inv.ID)
			if v := lineValue(got.LineItems[0], field); v != want {
				t.Errorf("%s = %q, want %q", field, v, want)
			}
			for _, o := range nrsLineColumns {
				if o != field && lineValue(got.LineItems[0], o) != lineValue(stored, o) {
					t.Errorf("%s = %q, want the stored %q (kept)", o, lineValue(got.LineItems[0], o), lineValue(stored, o))
				}
			}
			if got.Status != StatusDraft {
				t.Errorf("status = %q, want draft", got.Status)
			}
			if fields := auditFields(t, f.app, f.tenantID, "invoice.updated"); !reflect.DeepEqual(fields, []string{"line_items"}) {
				t.Errorf("audit fields = %v, want [line_items]", fields)
			}
		})
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
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{categorised("mine", "STANDARD_VAT"), categorised("second", "ZERO_VAT")}}); err != nil {
		t.Fatalf("replace line: %v", err)
	}
	mine, second := f.get(t, inv.ID).LineItems[0], f.get(t, inv.ID).LineItems[1]

	entry := func(id string) LineItemInput { l := legacyOf(mine); l.ID = &id; return l }
	cases := map[string][]LineItemInput{
		"another invoice, same tenant": {entry(otherLine)},
		"another tenant":               {entry(tenantBLine)},
		"replaced line":                {entry(staleID)},
		"not a uuid":                   {entry("not-a-uuid")},
		"id used twice":                {entry(mine.ID), entry(mine.ID)},
		"own id then another tenant's": {entry(second.ID), entry(tenantBLine)},
		"another tenant's then own id": {entry(tenantBLine), entry(second.ID)},
	}
	beforeAudit := auditCount(t, f.app, f.tenantID, "invoice.updated")
	if len(cases) < 7 {
		t.Fatalf("cases = %d, want at least 7", len(cases))
	}
	messages := map[string]string{}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{BuyerCity: strPtr("Changed")}, LineItems: &lines})
			if !errors.Is(err, ErrUnknownLineID) {
				t.Fatalf("err = %v, want ErrUnknownLineID", err)
			}
			messages[name] = err.Error()
			status, msg := statusForErr(err)
			if status != 400 || msg != "line_items id must name a line of this invoice" {
				t.Errorf("statusForErr = (%d, %q), want the one 400 sentence", status, msg)
			}
			got := f.get(t, inv.ID)
			if got.BuyerCity != nil || len(got.LineItems) != 2 || got.LineItems[0].ID != mine.ID || got.LineItems[1].ID != second.ID ||
				got.Status != StatusDraft || catOf(got.LineItems[0]) != "STANDARD_VAT" {
				t.Errorf("invoice changed after a refused edit: %+v", got)
			}
			if n := auditCount(t, f.app, f.tenantID, "invoice.updated"); n != beforeAudit {
				t.Errorf("invoice.updated rows = %d, want %d", n, beforeAudit)
			}
		})
	}
	// No existence oracle: a real line of another tenant answers exactly like a made-up id.
	if messages["another tenant"] != messages["not a uuid"] || messages["another invoice, same tenant"] != messages["not a uuid"] {
		t.Errorf("refusal messages differ by id kind: %v", messages)
	}
}

func TestStoreEdit_EntryWithoutIDAndChangedContentLosesItsNRSFields(t *testing.T) {
	f := newNRSFixture(t, "NRS-LOSS")
	for _, field := range []string{"Description", "Quantity", "UnitPrice", "LineTotal", "LineTax"} {
		for _, variant := range []string{"changed", "nil"} {
			t.Run(field+"/"+variant, func(t *testing.T) {
				inv := f.create(t, "NRS-LOSS-"+field+variant, CreateInput{LineItems: []LineItemInput{categorised("one", "STANDARD_VAT")}})
				stored := f.get(t, inv.ID).LineItems[0]
				changed := legacyOf(stored)
				fv := reflect.ValueOf(&changed).Elem().FieldByName(field)
				if variant == "nil" {
					fv.Set(reflect.Zero(fv.Type()))
				} else {
					v := "5"
					if field == "Description" {
						v = "other"
					}
					fv.Set(reflect.ValueOf(&v))
				}
				if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{changed}}); err != nil {
					t.Fatalf("Edit: %v", err)
				}
				if got := f.get(t, inv.ID).LineItems[0].TaxCategory; got != nil {
					t.Errorf("TaxCategory = %q, want nil (D23 residual)", *got)
				}
			})
		}
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

func catOf(l LineItem) string {
	if l.TaxCategory == nil {
		return "<nil>"
	}
	return *l.TaxCategory
}

// Content-match fallback: duplicates claim stored lines one by one, in line_no order.
func TestStoreEdit_ContentMatchFallbackWithDuplicateLines(t *testing.T) {
	f := newNRSFixture(t, "NRS-DUP")
	mk := func(cat string) LineItemInput { return categorised("same", cat) }
	setup := func(n string) (Invoice, []LineItem) {
		inv := f.create(t, "NRS-DUP-"+n, CreateInput{LineItems: []LineItemInput{mk("STANDARD_VAT"), mk("ZERO_VAT")}})
		return inv, f.get(t, inv.ID).LineItems
	}
	edit := func(inv Invoice, lines ...LineItemInput) []LineItem {
		t.Helper()
		if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &lines}); err != nil {
			t.Fatalf("Edit: %v", err)
		}
		return f.get(t, inv.ID).LineItems
	}

	t.Run("two entries take the two stored lines in order", func(t *testing.T) {
		inv, stored := setup("a")
		got := edit(inv, legacyOf(stored[0]), legacyOf(stored[1]))
		if len(got) != 2 || catOf(got[0]) != "STANDARD_VAT" || catOf(got[1]) != "ZERO_VAT" {
			t.Errorf("categories = %v %v, want STANDARD_VAT ZERO_VAT", catOf(got[0]), catOf(got[1]))
		}
	})
	t.Run("a stored line is claimed once: the extra entry gets nothing", func(t *testing.T) {
		inv, stored := setup("b")
		l := legacyOf(stored[0])
		got := edit(inv, l, l, l)
		if len(got) != 3 || catOf(got[0]) != "STANDARD_VAT" || catOf(got[1]) != "ZERO_VAT" || catOf(got[2]) != "<nil>" {
			t.Errorf("categories = %v %v %v, want STANDARD_VAT ZERO_VAT <nil>", catOf(got[0]), catOf(got[1]), catOf(got[2]))
		}
	})
	t.Run("one entry takes the first stored line", func(t *testing.T) {
		inv, stored := setup("c")
		got := edit(inv, legacyOf(stored[1]))
		if len(got) != 1 || catOf(got[0]) != "STANDARD_VAT" {
			t.Errorf("category = %v, want the first stored line's STANDARD_VAT", catOf(got[0]))
		}
	})
	t.Run("an id claims its line before a no-id entry ahead of it matches", func(t *testing.T) {
		inv, stored := setup("d")
		withID := legacyOf(stored[0])
		withID.ID = &stored[0].ID
		got := edit(inv, legacyOf(stored[0]), withID)
		if len(got) != 2 || catOf(got[0]) != "ZERO_VAT" || catOf(got[1]) != "STANDARD_VAT" {
			t.Errorf("categories = %v %v, want ZERO_VAT STANDARD_VAT", catOf(got[0]), catOf(got[1]))
		}
	})
	t.Run("an entry that sends one NRS field takes none from the stored line", func(t *testing.T) {
		for i, field := range nrsLineColumns {
			inv := f.create(t, "NRS-DUP-e-"+field, CreateInput{LineItems: []LineItemInput{fullNRSLine()}})
			stored := f.get(t, inv.ID).LineItems[0]
			l := legacyOf(stored)
			val := "changed" + strings.Repeat("x", i)
			if field == "TaxPercent" || field == "BaseQuantity" {
				val = "3"
			}
			reflect.ValueOf(&l).Elem().FieldByName(field).Set(reflect.ValueOf(&val))
			got := edit(inv, l)[0]
			for _, o := range nrsLineColumns {
				if o == field {
					if lineValue(got, o) == "<nil>" {
						t.Errorf("%s sent but not written", field)
					}
				} else if lineValue(got, o) != "<nil>" {
					t.Errorf("sent only %s, but %s = %q was carried", field, o, lineValue(got, o))
				}
			}
		}
	})
	t.Run("a nil description does not match an empty one", func(t *testing.T) {
		nilDesc := categorised("x", "STANDARD_VAT")
		nilDesc.Description = nil
		inv := f.create(t, "NRS-DUP-f", CreateInput{LineItems: []LineItemInput{nilDesc}})
		stored := f.get(t, inv.ID).LineItems[0]
		same := legacyOf(stored)
		if got := edit(inv, same)[0]; catOf(got) != "STANDARD_VAT" {
			t.Errorf("nil description entry category = %v, want STANDARD_VAT", catOf(got))
		}
		empty := legacyOf(stored)
		empty.Description = strPtr("")
		if got := edit(inv, empty)[0]; got.TaxCategory != nil {
			t.Errorf("empty description entry category = %v, want nil", catOf(got))
		}
	})
}

// Resending the stored legacy content with no id and no NRS field changes nothing: no demotion, no audit row.
func TestStoreEdit_ResendingLegacyContentOfANRSInvoiceIsANoOp(t *testing.T) {
	f := newNRSFixture(t, "NRS-RESEND")
	inv := f.create(t, "NRS-RESEND-1", CreateInput{LineItems: []LineItemInput{fullNRSLine(), categorised("two", "ZERO_VAT")}})
	if _, err := f.store.Transition(f.c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	stored := f.get(t, inv.ID).LineItems
	before := auditCount(t, f.app, f.tenantID, "invoice.updated")
	got, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{legacyOf(stored[0]), legacyOf(stored[1])}})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if got.Status != StatusValidated {
		t.Errorf("status = %q, want validated", got.Status)
	}
	if n := auditCount(t, f.app, f.tenantID, "invoice.updated"); n != before {
		t.Errorf("invoice.updated rows = %d, want %d", n, before)
	}
	if after := f.get(t, inv.ID).LineItems; !reflect.DeepEqual(after[0].TaxPercent, stored[0].TaxPercent) || catOf(after[1]) != "ZERO_VAT" {
		t.Errorf("lines changed: %+v", after)
	}
}

// Legacy lines (all NRS NULL) edited by id, by content and by header stay NULL and never error.
func TestStoreEdit_LegacyInvoiceKeepsNilNRSFieldsThroughEveryEditPath(t *testing.T) {
	f := newNRSFixture(t, "NRS-LEGACY")
	inv := f.create(t, "NRS-LEGACY-1", CreateInput{LineItems: []LineItemInput{nrsLine("a", "1", "1.00"), nrsLine("b", "2", "2.00")}})
	stored := f.get(t, inv.ID).LineItems

	byID := legacyOf(stored[0])
	byID.ID, byID.Description = &stored[0].ID, strPtr("a renamed")
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{byID, legacyOf(stored[1])}}); err != nil {
		t.Fatalf("Edit lines: %v", err)
	}
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{BuyerName: strPtr("Buyer")}}); err != nil {
		t.Fatalf("Edit header: %v", err)
	}
	got := f.get(t, inv.ID)
	if len(got.LineItems) != 2 {
		t.Fatalf("lines = %d, want 2", len(got.LineItems))
	}
	for _, l := range got.LineItems {
		for _, field := range nrsLineColumns {
			if v := lineValue(l, field); v != "<nil>" {
				t.Errorf("line %s %s = %q, want nil", *l.Description, field, v)
			}
		}
	}
	for _, c := range nrsHeaderColumns {
		if v := headerValue(got, c.field); v != "<nil>" {
			t.Errorf("%s = %q, want nil", c.field, v)
		}
	}
}

// An id entry may be written in any letter case; a padded, empty or half-valid id is refused.
func TestStoreEdit_LineIDMatchIgnoresCaseButNothingElse(t *testing.T) {
	f := newNRSFixture(t, "NRS-IDCASE")
	inv := f.create(t, "NRS-IDCASE-1", CreateInput{LineItems: []LineItemInput{categorised("one", "STANDARD_VAT")}})
	stored := f.get(t, inv.ID).LineItems[0]

	upper := legacyOf(stored)
	upper.ID, upper.Description = strPtr(strings.ToUpper(stored.ID)), strPtr("renamed")
	if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{upper}}); err != nil {
		t.Fatalf("Edit with an upper-case id: %v", err)
	}
	now := f.get(t, inv.ID).LineItems[0]
	if catOf(now) != "STANDARD_VAT" {
		t.Errorf("category = %v, want STANDARD_VAT carried through an upper-case id", catOf(now))
	}
	for name, id := range map[string]string{"padded": " " + now.ID, "empty": "", "truncated": now.ID[:35]} {
		l := legacyOf(now)
		l.ID = strPtr(id)
		if _, err := f.store.Edit(f.c, inv.ID, EditInput{LineItems: &[]LineItemInput{l}}); !errors.Is(err, ErrUnknownLineID) {
			t.Errorf("%s id: err = %v, want ErrUnknownLineID", name, err)
		}
	}
}

// A line id is the caller's tenant business only: tenant A cannot reach tenant B's invoice by id,
// and tenant B's lines stay untouched.
func TestStoreEdit_OtherTenantInvoiceWithMyLineIDIsNotFoundAndUntouched(t *testing.T) {
	f := newNRSFixture(t, "NRS-XT")
	mine := f.create(t, "NRS-XT-1", CreateInput{LineItems: []LineItemInput{categorised("mine", "STANDARD_VAT")}})
	myLine := f.get(t, mine.ID).LineItems[0]

	tenantB := seedTenant(t, f.super, "NRS-XT B")
	entityB := seedEntity(t, f.super, tenantB, "B Co")
	cB := auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB})
	invB, err := f.store.Create(cB, CreateInput{EntityID: entityB, InvoiceNumber: "NRS-XT-B", LineItems: []LineItemInput{categorised("b", "EXEMPTED")}})
	if err != nil {
		t.Fatalf("Create B: %v", err)
	}

	l := legacyOf(myLine)
	l.ID = &myLine.ID
	if _, err := f.store.Edit(f.c, invB.ID, EditInput{LineItems: &[]LineItemInput{l}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	gotB, err := f.store.Get(cB, invB.ID)
	if err != nil {
		t.Fatalf("Get B: %v", err)
	}
	if len(gotB.LineItems) != 1 || catOf(gotB.LineItems[0]) != "EXEMPTED" {
		t.Errorf("tenant B lines = %+v, want the one EXEMPTED line", gotB.LineItems)
	}
}

// Store.Create has no line to continue: an id on a create entry is ignored.
func TestStoreCreate_IgnoresALineEntryID(t *testing.T) {
	f := newNRSFixture(t, "NRS-CREATEID")
	other := f.create(t, "NRS-CREATEID-1", CreateInput{LineItems: []LineItemInput{categorised("x", "ZERO_VAT")}})
	foreign := f.get(t, other.ID).LineItems[0].ID
	l := categorised("y", "STANDARD_VAT")
	l.ID = &foreign
	inv := f.create(t, "NRS-CREATEID-2", CreateInput{LineItems: []LineItemInput{l}})
	got := f.get(t, inv.ID).LineItems
	if len(got) != 1 || got[0].ID == foreign || catOf(got[0]) != "STANDARD_VAT" {
		t.Errorf("lines = %+v, want one fresh STANDARD_VAT line", got)
	}
}

// Header and line writes of one edit roll back together on a bad line value.
func TestStoreEdit_MalformedLineNumericRollsBackTheHeaderWrite(t *testing.T) {
	f := newNRSFixture(t, "NRS-ROLL")
	inv := f.create(t, "NRS-ROLL-1", CreateInput{LineItems: []LineItemInput{categorised("one", "STANDARD_VAT")}})
	for field, val := range map[string]string{"TaxPercent": "abc", "BaseQuantity": "abc", "TaxPercentOverflow": "99999999999999"} {
		l := nrsLine("one", "1", "10.00")
		reflect.ValueOf(&l).Elem().FieldByName(strings.TrimSuffix(field, "Overflow")).Set(reflect.ValueOf(strPtr(val)))
		_, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{BuyerCity: strPtr("Changed")}, LineItems: &[]LineItemInput{l}})
		if !errors.Is(err, ErrValidation) {
			t.Errorf("%s=%q: err = %v, want ErrValidation", field, val, err)
		}
	}
	got := f.get(t, inv.ID)
	if got.BuyerCity != nil || len(got.LineItems) != 1 || catOf(got.LineItems[0]) != "STANDARD_VAT" {
		t.Errorf("invoice changed after refused edits: %+v", got)
	}
}

// Every unparseable or out-of-range issue_time is ErrValidation through Create and Edit, never a raw 500.
func TestStoreWrite_BadIssueTimeIsAlwaysValidation(t *testing.T) {
	f := newNRSFixture(t, "NRS-TIMES")
	inv := f.create(t, "NRS-TIMES-1", CreateInput{IssueTime: strPtr("10:00:00")})
	for _, bad := range []string{"", "nope", "25:00:00", "12:61:00", "12:00:61"} {
		if _, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{IssueTime: strPtr(bad)}}); !errors.Is(err, ErrValidation) {
			t.Errorf("Edit(%q): err = %v, want ErrValidation", bad, err)
		}
		if _, err := f.store.Create(f.c, CreateInput{EntityID: f.entityID, InvoiceNumber: "NRS-TIMES-C-" + bad, IssueTime: strPtr(bad)}); !errors.Is(err, ErrValidation) {
			t.Errorf("Create(%q): err = %v, want ErrValidation", bad, err)
		}
	}
	if got := f.get(t, inv.ID).IssueTime; got == nil || *got != "10:00:00" {
		t.Errorf("IssueTime = %v, want unchanged 10:00:00", got)
	}
}

// An explicit clear on a new key leaves the others, old and new, as they were; an absent key leaves it too.
func TestStoreEdit_ClearingOneNRSKeyLeavesTheRest(t *testing.T) {
	f := newNRSFixture(t, "NRS-CLR2")
	in := CreateInput{BuyerName: strPtr("Buyer Co")}
	fullNRSHeader(&in)
	inv := f.create(t, "NRS-CLR2-1", in)
	before := f.get(t, inv.ID)
	got, err := f.store.Edit(f.c, inv.ID, EditInput{UpdateInput: UpdateInput{SupplierLGA: ClearText}})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if got.SupplierLGA != nil {
		t.Errorf("SupplierLGA = %v, want nil", *got.SupplierLGA)
	}
	if got.BuyerName == nil || *got.BuyerName != "Buyer Co" {
		t.Errorf("BuyerName = %v, want unchanged", got.BuyerName)
	}
	for _, c := range nrsHeaderColumns {
		if c.field != "SupplierLGA" && headerValue(got, c.field) != headerValue(before, c.field) {
			t.Errorf("%s changed to %q", c.field, headerValue(got, c.field))
		}
	}
	if fields := auditFields(t, f.app, f.tenantID, "invoice.updated"); !reflect.DeepEqual(fields, []string{"supplier_lga"}) {
		t.Errorf("audit fields = %v, want [supplier_lga]", fields)
	}
}
