package importer

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/invoicefields"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// Copy of internal/invoice/handlers.go issueTimeMsg.
const nrsIssueTimeMsg = "issue_time must be HH:MM:SS"

func nrsNotNumber(field string) string { return field + " is not a valid number" }

func nrsCommaDecimal(field string) string {
	return field + " uses a comma as the decimal mark; write it with a dot, e.g. 1234.56"
}

func nrsBadDate(field, v string) string {
	return fmt.Sprintf("%s %q is not in YYYY-MM-DD format", field, v)
}

// Design "Range" row (D17): numeric(14,2) and numeric(14,3) columns.
func nrsRange(field string) string {
	if field == "line_base_quantity" {
		return field + " must have at most 11 digits before the decimal point and 3 after"
	}
	return field + " must have at most 12 digits before the decimal point and 2 after"
}

// nrsFile builds a header, an identity mapping (header name = key) and rows
// from per-row cell maps. invoice_number is column 0; missing cells are "".
func nrsFile(rows ...map[string]string) ([]string, map[string]string, [][]string) {
	seen := map[string]bool{"invoice_number": true}
	cols := []string{"invoice_number"}
	var rest []string
	for _, r := range rows {
		for k := range r {
			if !seen[k] {
				seen[k] = true
				rest = append(rest, k)
			}
		}
	}
	sort.Strings(rest)
	cols = append(cols, rest...)
	mapping := make(map[string]string, len(cols))
	for _, c := range cols {
		mapping[c] = c
	}
	data := make([][]string, len(rows))
	for i, r := range rows {
		data[i] = make([]string, len(cols))
		for j, c := range cols {
			data[i][j] = r[c]
		}
	}
	return cols, mapping, data
}

// nrsRow is a clean one-line row; kv pairs override or add cells.
func nrsRow(num string, kv ...string) map[string]string {
	r := map[string]string{
		"invoice_number": num, "issue_date": "2026-07-01", "buyer_tin": "87654321-0002",
		"buyer_name": "Beta Ltd", "currency": "NGN", "subtotal": "100.00", "vat": "7.50", "total": "107.50",
		"line_description": "Item", "line_quantity": "1", "line_unit_price": "100.00",
	}
	for i := 0; i+1 < len(kv); i += 2 {
		r[kv[i]] = kv[i+1]
	}
	return r
}

// nrsFullRow sets all 36 import keys; kv pairs override.
func nrsFullRow(num string, kv ...string) map[string]string {
	r := nrsRow(num,
		"invoice_kind", "B2B", "tax_currency_code", "NGN", "due_date", "2026-07-31", "issue_time", "9:05",
		"tax_point_date", "2026-07-02", "payment_status", "PENDING", "buyer_email", "ada@beta.example",
		"buyer_telephone", "+2348012345678", "buyer_street", "1 Marina Road", "buyer_city", "Lagos",
		"buyer_postal_zone", "101233", "buyer_country", "NG", "buyer_state", "LA", "buyer_lga", "IKJ",
		"line_total", "100.00", "line_tax", "7.50", "line_tax_category", "VAT", "line_hsn_code", "8471.30",
		"line_isic_code", "6201", "line_product_category", "Electronics", "line_service_category", "Consulting",
		"line_sellers_item_identification", "SKU-42", "line_price_unit", "EA", "line_tax_percent", "7.5%",
		"line_base_quantity", "2.125")
	for i := 0; i+1 < len(kv); i += 2 {
		r[kv[i]] = kv[i+1]
	}
	return r
}

// nrsBuild runs buildCreateInput once per distinct invoice_number, in file order.
func nrsBuild(t *testing.T, rows ...map[string]string) []invoice.CreateInput {
	t.Helper()
	header, mapping, data := nrsFile(rows...)
	colIndex, err := resolveMapping(mapping, header)
	if err != nil {
		t.Fatalf("resolveMapping: %v", err)
	}
	var order []string
	groups := map[string]*invoiceGroup{}
	for i, r := range data {
		num := r[colIndex["invoice_number"]]
		g, ok := groups[num]
		if !ok {
			g = &invoiceGroup{number: num}
			groups[num] = g
			order = append(order, num)
		}
		g.rowIdxs = append(g.rowIdxs, i)
	}
	out := make([]invoice.CreateInput, len(order))
	for i, num := range order {
		out[i] = buildCreateInput("entity-1", data, colIndex, groups[num], "", "", 1, "Acme", nil)
	}
	return out
}

func sv(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func dv(p *time.Time) string {
	if p == nil {
		return "<nil>"
	}
	return p.Format("2006-01-02")
}

type nrsWant struct{ name, got, want string }

func checkWants(t *testing.T, ws []nrsWant) {
	t.Helper()
	for _, w := range ws {
		if w.got != w.want {
			t.Errorf("%s = %q, want %q", w.name, w.got, w.want)
		}
	}
}

// --- pure: cell reading into CreateInput -----------------------------------

func TestBuildCreateInput_SetsEveryNewImportKey(t *testing.T) {
	ins := nrsBuild(t, nrsFullRow("NRS-1"))
	if len(ins) != 1 || len(ins[0].LineItems) != 1 {
		t.Fatalf("want 1 invoice with 1 line, got %+v", ins)
	}
	in, li := ins[0], ins[0].LineItems[0]
	checkWants(t, []nrsWant{
		{"InvoiceKind", sv(in.InvoiceKind), "B2B"},
		{"TaxCurrencyCode", sv(in.TaxCurrencyCode), "NGN"},
		{"DueDate", dv(in.DueDate), "2026-07-31"},
		{"IssueTime", sv(in.IssueTime), "09:05:00"},
		{"TaxPointDate", dv(in.TaxPointDate), "2026-07-02"},
		{"PaymentStatus", sv(in.PaymentStatus), "PENDING"},
		{"BuyerEmail", sv(in.BuyerEmail), "ada@beta.example"},
		{"BuyerTelephone", sv(in.BuyerTelephone), "+2348012345678"},
		{"BuyerStreet", sv(in.BuyerStreet), "1 Marina Road"},
		{"BuyerCity", sv(in.BuyerCity), "Lagos"},
		{"BuyerPostalZone", sv(in.BuyerPostalZone), "101233"},
		{"BuyerCountry", sv(in.BuyerCountry), "NG"},
		{"BuyerState", sv(in.BuyerState), "LA"},
		{"BuyerLGA", sv(in.BuyerLGA), "IKJ"},
		{"LineTotal", sv(li.LineTotal), "100.00"},
		{"LineTax", sv(li.LineTax), "7.50"},
		{"TaxCategory", sv(li.TaxCategory), "VAT"},
		{"HSNCode", sv(li.HSNCode), "8471.30"},
		{"ISICCode", sv(li.ISICCode), "6201"},
		{"ProductCategory", sv(li.ProductCategory), "Electronics"},
		{"ServiceCategory", sv(li.ServiceCategory), "Consulting"},
		{"SellersItemIdentification", sv(li.SellersItemIdentification), "SKU-42"},
		{"PriceUnit", sv(li.PriceUnit), "EA"},
		{"TaxPercent", sv(li.TaxPercent), "7.5"},
		{"BaseQuantity", sv(li.BaseQuantity), "2.125"},
	})
}

// flatValues lists every string, *string and *time.Time value in in and its lines.
func flatValues(in invoice.CreateInput) map[string]bool {
	out := map[string]bool{}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			out[v.String()] = true
		case reflect.Ptr:
			if v.IsNil() {
				return
			}
			if tm, ok := v.Interface().(*time.Time); ok {
				out[tm.Format("2006-01-02")] = true
				return
			}
			walk(v.Elem())
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				walk(v.Field(i))
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i))
			}
		}
	}
	walk(reflect.ValueOf(in))
	return out
}

// A key added to the import field list and not read by buildCreateInput fails here.
func TestBuildCreateInput_ReadsEveryImportKey(t *testing.T) {
	keys := invoicefields.ImportKeys()
	if len(keys) == 0 {
		t.Fatal("ImportKeys() is empty")
	}
	typeOf := map[string]invoicefields.Type{}
	for _, f := range invoicefields.All {
		if f.Import {
			typeOf[f.ImportKey()] = f.Type
		}
	}
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i, key := range keys {
		var val string
		switch {
		case key == "invoice_number":
			val = "INV-NRS"
		case key == "issue_time":
			val = fmt.Sprintf("10:%02d:00", i)
		case key == "line_tax_percent": // Money at head, Percent after 02
			val = fmt.Sprintf("%d.5", 100+i)
		case typeOf[key] == invoicefields.Date:
			val = base.AddDate(0, 0, i).Format("2006-01-02")
		case typeOf[key] == invoicefields.Money:
			val = fmt.Sprintf("%d.25", 1000+i)
		case typeOf[key] == invoicefields.Quantity:
			val = fmt.Sprintf("%d.125", 1000+i)
		default:
			val = fmt.Sprintf("v%d-%s", i, key)
		}
		t.Run(key, func(t *testing.T) {
			ins := nrsBuild(t, map[string]string{"invoice_number": "INV-NRS", key: val})
			if len(ins) != 1 {
				t.Fatalf("got %d invoices, want 1", len(ins))
			}
			if got := flatValues(ins[0]); !got[val] {
				t.Errorf("value %q mapped from %q is in no CreateInput field", val, key)
			}
		})
	}
}

func TestBuildCreateInput_DropsAPercentSignFromTaxPercent(t *testing.T) {
	ins := nrsBuild(t,
		nrsRow("P-1", "line_tax_percent", "7.5%"),
		nrsRow("P-2", "line_tax_percent", " 7.5 % "),
		nrsRow("P-3", "line_tax_percent", "7.5"),
	)
	if len(ins) != 3 {
		t.Fatalf("got %d invoices, want 3", len(ins))
	}
	for i, in := range ins {
		if len(in.LineItems) != 1 {
			t.Fatalf("invoice %d has %d lines", i, len(in.LineItems))
		}
		if got := sv(in.LineItems[0].TaxPercent); got != "7.5" {
			t.Errorf("invoice %d TaxPercent = %q, want %q", i, got, "7.5")
		}
	}
}

func TestBuildCreateInput_PadsAShortIssueTime(t *testing.T) {
	cells := []string{"9:05", "09:05", "9:05:07", "09:05:00"}
	want := []string{"09:05:00", "09:05:00", "09:05:07", "09:05:00"}
	var rows []map[string]string
	for i, c := range cells {
		rows = append(rows, nrsRow(fmt.Sprintf("T-%d", i), "issue_time", c))
	}
	ins := nrsBuild(t, rows...)
	if len(ins) != len(cells) {
		t.Fatalf("got %d invoices, want %d", len(ins), len(cells))
	}
	for i, in := range ins {
		if got := sv(in.IssueTime); got != want[i] {
			t.Errorf("issue_time %q -> %q, want %q", cells[i], got, want[i])
		}
	}
}

// One field per type: Text, Code, Date, Time, Money, Quantity, Percent.
var nrsBlankKeys = []string{"buyer_email", "buyer_state", "line_hsn_code", "due_date", "issue_time", "line_tax", "line_base_quantity", "line_tax_percent"}

func TestBuildCreateInput_BlankNewCellsAreNil(t *testing.T) {
	filled := map[string]string{
		"buyer_email": "ada@beta.example", "buyer_state": "LA", "line_hsn_code": "8471.30", "due_date": "2026-07-31",
		"issue_time": "09:05:00", "line_tax": "7.50", "line_base_quantity": "2.125", "line_tax_percent": "7.5",
	}
	blank, spaces := nrsRow("B-1", "buyer_name", ""), nrsRow("B-2")
	pop := nrsRow("B-3")
	for _, k := range nrsBlankKeys {
		blank[k], spaces[k], pop[k] = "", " \t ", filled[k]
	}
	ins := nrsBuild(t, blank, spaces, pop)
	if len(ins) != 3 {
		t.Fatalf("got %d invoices, want 3", len(ins))
	}
	// Populated control first: a reader that sets nothing must not pass.
	p := ins[2]
	checkWants(t, []nrsWant{
		{"populated BuyerEmail", sv(p.BuyerEmail), "ada@beta.example"},
		{"populated BuyerState", sv(p.BuyerState), "LA"},
		{"populated DueDate", dv(p.DueDate), "2026-07-31"},
		{"populated IssueTime", sv(p.IssueTime), "09:05:00"},
	})
	for i, in := range ins[:2] {
		if len(in.LineItems) != 1 {
			t.Fatalf("invoice %d has %d lines", i, len(in.LineItems))
		}
		li := in.LineItems[0]
		got := map[string]bool{
			"buyer_email": in.BuyerEmail == nil, "buyer_state": in.BuyerState == nil, "due_date": in.DueDate == nil,
			"issue_time": in.IssueTime == nil, "line_hsn_code": li.HSNCode == nil, "line_tax": li.LineTax == nil,
			"line_base_quantity": li.BaseQuantity == nil, "line_tax_percent": li.TaxPercent == nil,
		}
		for _, k := range nrsBlankKeys {
			if !got[k] {
				t.Errorf("invoice %d: blank %s is not nil", i, k)
			}
		}
	}
	// Control: today's Text keys keep "" (02 AC8).
	if ins[0].BuyerName == nil || *ins[0].BuyerName != "" {
		t.Errorf("blank buyer_name = %v, want pointer to \"\"", ins[0].BuyerName)
	}
}

func TestBuildCreateInput_CodeCellsAreTrimmedNotFolded(t *testing.T) {
	ins := nrsBuild(t, nrsRow("C-1",
		"buyer_country", " NG ", "buyer_state", " la ", "buyer_lga", "Lagos", "line_hsn_code", "\t8471.30 "))
	if len(ins) != 1 || len(ins[0].LineItems) != 1 {
		t.Fatalf("want 1 invoice with 1 line, got %+v", ins)
	}
	in := ins[0]
	checkWants(t, []nrsWant{
		{"BuyerCountry", sv(in.BuyerCountry), "NG"},
		{"BuyerState", sv(in.BuyerState), "la"},
		{"BuyerLGA (a name is not turned into a code)", sv(in.BuyerLGA), "Lagos"},
		{"HSNCode", sv(in.LineItems[0].HSNCode), "8471.30"},
	})
}

// --- DB-backed: Service.Import with fakeGate -------------------------------

type nrsRun struct {
	t                  *testing.T
	ctx                context.Context
	super              *pgxpool.Pool
	gate               *fakeGate
	svc                *Service
	tenantID, entityID string
}

func newNRSRun(t *testing.T) *nrsRun {
	t.Helper()
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, "ENGI-07-02 tenant")
	entityID := seedEntityWithTIN(t, super, tenantID, "ENGI-07-02 entity", "12345678-0001")
	g := &fakeGate{}
	return &nrsRun{
		t: t, super: super, gate: g, tenantID: tenantID, entityID: entityID,
		svc: newTestServiceWithGate(app, g),
		ctx: auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID}),
	}
}

func (r *nrsRun) do(dryRun bool, rows ...map[string]string) BatchResult {
	r.t.Helper()
	header, mapping, data := nrsFile(rows...)
	res, err := r.svc.Import(r.ctx, r.entityID, "", "", 1, mapping, header, data, dryRun)
	if err != nil {
		r.t.Fatalf("Import (dryRun=%v): %v", dryRun, err)
	}
	return res
}

func (r *nrsRun) count() int { return countInvoicesForEntity(r.t, r.super, r.entityID) }

// sent returns the invoices the last dry run handed to the gate, by invoice_number.
func (r *nrsRun) sent() map[string]invoice.Invoice {
	out := map[string]invoice.Invoice{}
	for _, it := range r.gate.evaluateItems {
		out[it.Ref] = it.Invoice
	}
	return out
}

func (r *nrsRun) refs() []string {
	var out []string
	for _, it := range r.gate.evaluateItems {
		out = append(out, it.Ref)
	}
	return out
}

// onlyError asserts res holds exactly one RowError, on field, with msg.
func onlyError(t *testing.T, res BatchResult, field, msg string) {
	t.Helper()
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %+v, want exactly one on %s", res.Errors, field)
	}
	if e := res.Errors[0]; e.Field != field || e.Message != msg {
		t.Errorf("error = {Field:%q Message:%q}, want {Field:%q Message:%q}", e.Field, e.Message, field, msg)
	}
}

func errorFor(res BatchResult, row int) (RowError, bool) {
	return findRowErrorWithRows(res.Errors, []int{row})
}

func TestImport_TheRealRunStoresEveryNewKey(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(false, nrsFullRow("NRS-REAL", "line_tax_percent", "7.5"))
	if res.ReadyInvoices != 1 || len(res.Errors) != 0 {
		t.Fatalf("ready = %d, errors = %+v, want 1 and none", res.ReadyInvoices, res.Errors)
	}
	id := invoiceIDByNumber(t, r.super, r.entityID, "NRS-REAL")

	var hdr [14]*string
	if err := r.super.QueryRow(r.ctx,
		`SELECT invoice_kind, tax_currency_code, due_date::text, issue_time::text, tax_point_date::text, payment_status,
		        buyer_email, buyer_telephone, buyer_street, buyer_city, buyer_postal_zone, buyer_country, buyer_state, buyer_lga
		   FROM invoices WHERE id = $1`, id,
	).Scan(&hdr[0], &hdr[1], &hdr[2], &hdr[3], &hdr[4], &hdr[5], &hdr[6], &hdr[7], &hdr[8], &hdr[9], &hdr[10], &hdr[11], &hdr[12], &hdr[13]); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	wantHdr := []nrsWant{
		{"invoice_kind", sv(hdr[0]), "B2B"}, {"tax_currency_code", sv(hdr[1]), "NGN"},
		{"due_date", sv(hdr[2]), "2026-07-31"}, {"issue_time", sv(hdr[3]), "09:05:00"},
		{"tax_point_date", sv(hdr[4]), "2026-07-02"}, {"payment_status", sv(hdr[5]), "PENDING"},
		{"buyer_email", sv(hdr[6]), "ada@beta.example"}, {"buyer_telephone", sv(hdr[7]), "+2348012345678"},
		{"buyer_street", sv(hdr[8]), "1 Marina Road"}, {"buyer_city", sv(hdr[9]), "Lagos"},
		{"buyer_postal_zone", sv(hdr[10]), "101233"}, {"buyer_country", sv(hdr[11]), "NG"},
		{"buyer_state", sv(hdr[12]), "LA"}, {"buyer_lga", sv(hdr[13]), "IKJ"},
	}
	checkWants(t, wantHdr)

	var ln [11]*string
	if err := r.super.QueryRow(r.ctx,
		`SELECT line_total::text, line_tax::text, tax_category, hsn_code, isic_code, product_category, service_category,
		        sellers_item_identification, price_unit, tax_percent::text, base_quantity::text
		   FROM line_items WHERE invoice_id = $1`, id,
	).Scan(&ln[0], &ln[1], &ln[2], &ln[3], &ln[4], &ln[5], &ln[6], &ln[7], &ln[8], &ln[9], &ln[10]); err != nil {
		t.Fatalf("read line: %v", err)
	}
	checkWants(t, []nrsWant{
		{"line_total", sv(ln[0]), "100.00"}, {"line_tax", sv(ln[1]), "7.50"}, {"tax_category", sv(ln[2]), "VAT"},
		{"hsn_code", sv(ln[3]), "8471.30"}, {"isic_code", sv(ln[4]), "6201"}, {"product_category", sv(ln[5]), "Electronics"},
		{"service_category", sv(ln[6]), "Consulting"}, {"sellers_item_identification", sv(ln[7]), "SKU-42"},
		{"price_unit", sv(ln[8]), "EA"}, {"tax_percent", sv(ln[9]), "7.50"}, {"base_quantity", sv(ln[10]), "2.125"},
	})
}

func TestImport_DryRunCarriesEveryNewKeyToTheGate(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(true, nrsFullRow("NRS-DRY", "line_tax_percent", "7.5"))
	if res.ReadyInvoices != 1 || len(res.Errors) != 0 {
		t.Fatalf("ready = %d, errors = %+v, want 1 and none", res.ReadyInvoices, res.Errors)
	}
	inv, ok := r.sent()["NRS-DRY"]
	if !ok || len(inv.LineItems) != 1 {
		t.Fatalf("gate got %v, want NRS-DRY with 1 line", r.refs())
	}
	li := inv.LineItems[0]
	checkWants(t, []nrsWant{
		{"InvoiceKind", sv(inv.InvoiceKind), "B2B"}, {"TaxCurrencyCode", sv(inv.TaxCurrencyCode), "NGN"},
		{"DueDate", dv(inv.DueDate), "2026-07-31"}, {"IssueTime", sv(inv.IssueTime), "09:05:00"},
		{"TaxPointDate", dv(inv.TaxPointDate), "2026-07-02"}, {"PaymentStatus", sv(inv.PaymentStatus), "PENDING"},
		{"BuyerEmail", sv(inv.BuyerEmail), "ada@beta.example"}, {"BuyerTelephone", sv(inv.BuyerTelephone), "+2348012345678"},
		{"BuyerStreet", sv(inv.BuyerStreet), "1 Marina Road"}, {"BuyerCity", sv(inv.BuyerCity), "Lagos"},
		{"BuyerPostalZone", sv(inv.BuyerPostalZone), "101233"}, {"BuyerCountry", sv(inv.BuyerCountry), "NG"},
		{"BuyerState", sv(inv.BuyerState), "LA"}, {"BuyerLGA", sv(inv.BuyerLGA), "IKJ"},
		{"LineTotal", sv(li.LineTotal), "100.00"}, {"LineTax", sv(li.LineTax), "7.50"},
		{"TaxCategory", sv(li.TaxCategory), "VAT"}, {"HSNCode", sv(li.HSNCode), "8471.30"},
		{"ISICCode", sv(li.ISICCode), "6201"}, {"ProductCategory", sv(li.ProductCategory), "Electronics"},
		{"ServiceCategory", sv(li.ServiceCategory), "Consulting"},
		{"SellersItemIdentification", sv(li.SellersItemIdentification), "SKU-42"},
		{"PriceUnit", sv(li.PriceUnit), "EA"}, {"TaxPercent", sv(li.TaxPercent), "7.5"},
		{"BaseQuantity", sv(li.BaseQuantity), "2.125"},
	})
}

// Each bad-number test pairs its quarantine with a clean invoice that must carry
// the new value to the gate, so a reader that ignores the column cannot pass.

func TestImport_AnExponentInALineAmountQuarantines(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(true, nrsRow("EXP-BAD", "line_tax", "1e400"))
	onlyError(t, res, "line_tax", nrsNotNumber("line_tax"))
	if r.gate.evaluateCalls != 0 {
		t.Errorf("evaluateCalls = %d for an all-quarantined file, want 0", r.gate.evaluateCalls)
	}

	res = r.do(true, nrsRow("EXP-BAD", "line_tax", "1e400"), nrsRow("EXP-OK", "line_tax", "7.50"))
	onlyError(t, res, "line_tax", nrsNotNumber("line_tax"))
	sent := r.sent()
	if _, bad := sent["EXP-BAD"]; bad || len(sent) != 1 {
		t.Fatalf("gate got %v, want only EXP-OK", r.refs())
	}
	ok := sent["EXP-OK"]
	if len(ok.LineItems) != 1 {
		t.Fatalf("EXP-OK has %d lines, want 1", len(ok.LineItems))
	}
	if got := sv(ok.LineItems[0].LineTax); got != "7.50" {
		t.Errorf("EXP-OK line_tax = %q, want 7.50", got)
	}
}

func TestImport_AnExponentInTaxPercentQuarantines(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(false, nrsRow("EXP-PCT", "line_tax_percent", "7.5e1"))
	onlyError(t, res, "line_tax_percent", nrsNotNumber("line_tax_percent"))
	if n := r.count(); n != 0 {
		t.Errorf("invoices stored = %d, want 0", n)
	}
	if r.gate.validateBatchCalls != 0 {
		t.Errorf("validateBatchCalls = %d, want 0", r.gate.validateBatchCalls)
	}

	res = r.do(false, nrsRow("EXP-PCT-OK", "line_tax_percent", "7.5"))
	if res.ReadyInvoices != 1 || len(res.Errors) != 0 {
		t.Fatalf("ready = %d, errors = %+v, want the clean control stored", res.ReadyInvoices, res.Errors)
	}
	var got *string
	if err := r.super.QueryRow(r.ctx,
		`SELECT l.tax_percent::text FROM line_items l JOIN invoices i ON i.id = l.invoice_id
		  WHERE i.entity_id = $1 AND i.invoice_number = 'EXP-PCT-OK'`, r.entityID).Scan(&got); err != nil {
		t.Fatalf("read tax_percent: %v", err)
	}
	if sv(got) != "7.50" {
		t.Errorf("stored tax_percent = %q, want 7.50", sv(got))
	}
}

func TestImport_ANonNumberInLineTotalOrBaseQuantityQuarantines(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(true,
		nrsRow("NAN-1", "line_total", "abc"),
		nrsRow("NAN-2", "line_base_quantity", "x"),
		nrsRow("NAN-OK", "line_total", "100.00", "line_base_quantity", "1.000"),
	)
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %+v, want 2", res.Errors)
	}
	for row, field := range map[int]string{2: "line_total", 3: "line_base_quantity"} {
		e, ok := errorFor(res, row)
		if !ok || e.Field != field || e.Message != nrsNotNumber(field) {
			t.Errorf("row %d error = %+v (found %v), want %s %q", row, e, ok, field, nrsNotNumber(field))
		}
	}
	sent := r.sent()
	ok := sent["NAN-OK"]
	if len(sent) != 1 || len(ok.LineItems) != 1 || sv(ok.LineItems[0].LineTotal) != "100.00" || sv(ok.LineItems[0].BaseQuantity) != "1.000" {
		t.Errorf("gate got %v, want only NAN-OK with line_total 100.00 and base_quantity 1.000", r.refs())
	}
}

func TestImport_ACommaDecimalTaxPercentQuarantines(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(true, nrsRow("CD-1", "line_tax_percent", "7,5%"), nrsRow("CD-OK", "line_tax_percent", "7.5%"))
	onlyError(t, res, "line_tax_percent", nrsCommaDecimal("line_tax_percent"))
	ok := r.sent()["CD-OK"]
	if len(r.sent()) != 1 || len(ok.LineItems) != 1 || sv(ok.LineItems[0].TaxPercent) != "7.5" {
		t.Errorf("gate got %v, want only CD-OK with tax_percent 7.5", r.refs())
	}
}

func TestImport_TwoPercentSignsQuarantine(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(true, nrsRow("PP-1", "line_tax_percent", "7.5%%"), nrsRow("PP-OK", "line_tax_percent", " 7.5 % "))
	onlyError(t, res, "line_tax_percent", nrsNotNumber("line_tax_percent"))
	ok := r.sent()["PP-OK"]
	if len(r.sent()) != 1 || len(ok.LineItems) != 1 || sv(ok.LineItems[0].TaxPercent) != "7.5" {
		t.Errorf("gate got %v, want only PP-OK with tax_percent 7.5", r.refs())
	}
}

func TestImport_AnUnparseableDueDateQuarantines(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(true,
		nrsRow("DD-1", "due_date", "30/01/2026"),
		nrsRow("DD-2", "tax_point_date", "31 Jan 2026"),
		nrsRow("DD-OK", "due_date", "2026-07-31", "tax_point_date", "2026-07-02"),
	)
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %+v, want 2", res.Errors)
	}
	for row, w := range map[int][2]string{2: {"due_date", "30/01/2026"}, 3: {"tax_point_date", "31 Jan 2026"}} {
		e, ok := errorFor(res, row)
		if !ok || e.Field != w[0] || e.Message != nrsBadDate(w[0], w[1]) {
			t.Errorf("row %d error = %+v (found %v), want %s %q", row, e, ok, w[0], nrsBadDate(w[0], w[1]))
		}
	}
	ok := r.sent()["DD-OK"]
	if len(r.sent()) != 1 || dv(ok.DueDate) != "2026-07-31" || dv(ok.TaxPointDate) != "2026-07-02" {
		t.Errorf("gate got %v, want only DD-OK with both dates", r.refs())
	}
}

func TestImport_IssueDateMessageIsUnchanged(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(true, nrsRow("ID-1", "issue_date", "bad"), nrsRow("ID-OK", "due_date", "2026-07-31"))
	onlyError(t, res, "issue_date", nrsBadDate("issue_date", "bad"))
	if ok := r.sent()["ID-OK"]; len(r.sent()) != 1 || dv(ok.DueDate) != "2026-07-31" {
		t.Errorf("gate got %v, want only ID-OK with due_date", r.refs())
	}
}

func TestImport_AnIssueTimeOutsideTheAcceptedShapesQuarantines(t *testing.T) {
	r := newNRSRun(t)
	bad := []string{"24:00:00", "09:05:00.5", "9:05 AM", "9.05", "9:5", "24:00", "9:60"}
	var rows []map[string]string
	for i, v := range bad {
		rows = append(rows, nrsRow(fmt.Sprintf("IT-%d", i), "issue_time", v))
	}
	rows = append(rows, nrsRow("IT-OK1", "issue_time", "23:59:59"), nrsRow("IT-OK2", "issue_time", "9:05"))
	res := r.do(true, rows...)
	if len(res.Errors) != len(bad) {
		t.Fatalf("errors = %+v, want %d", res.Errors, len(bad))
	}
	for i, v := range bad {
		e, ok := errorFor(res, i+2)
		if !ok || e.Field != "issue_time" || e.Message != nrsIssueTimeMsg {
			t.Errorf("issue_time %q: error = %+v (found %v), want issue_time %q", v, e, ok, nrsIssueTimeMsg)
		}
	}
	sent := r.sent()
	if len(sent) != 2 || sv(sent["IT-OK1"].IssueTime) != "23:59:59" || sv(sent["IT-OK2"].IssueTime) != "09:05:00" {
		t.Errorf("gate got %v, want IT-OK1 23:59:59 and IT-OK2 09:05:00", r.refs())
	}
}

func TestImport_RowsThatDisagreeOnANewHeaderFieldQuarantine(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(true,
		nrsRow("DIS-1", "buyer_state", "LA"), nrsRow("DIS-1", "buyer_state", "OG"),
		nrsRow("DIS-2", "buyer_state", "LA"), nrsRow("DIS-2", "buyer_state", "LA"),
		nrsRow("DIS-3", "buyer_state", "LA"), nrsRow("DIS-3", "buyer_state", " LA "),
	)
	onlyError(t, res, "buyer_state", "rows disagree on buyer_state")
	if _, ok := findRowErrorWithRows(res.Errors, []int{2, 3}); !ok {
		t.Errorf("errors = %+v, want one citing rows [2 3]", res.Errors)
	}
	sent := r.sent()
	if len(sent) != 2 || sv(sent["DIS-2"].BuyerState) != "LA" || sv(sent["DIS-3"].BuyerState) != "LA" {
		t.Errorf("gate got %v, want DIS-2 and DIS-3 with buyer_state LA (a padded Code agrees with its trim)", r.refs())
	}
}

// Digit-count and scale limits of the new numeric(14,2) and numeric(14,3) columns (D17).
func TestImport_ANewNumberTooLongForItsColumnQuarantines(t *testing.T) {
	r := newNRSRun(t)
	rows := []map[string]string{
		nrsRow("LONG-1", "line_total", "1234567890123"),
		nrsRow("LONG-2", "line_tax", "1234567890123"),
		nrsRow("LONG-3", "line_tax_percent", "1234567890123"),
		nrsRow("LONG-4", "line_base_quantity", "123456789012"),
		nrsRow("LONG-OK", "line_total", "123456789012.50", "line_tax", "999999999999.99",
			"line_tax_percent", "123456789012", "line_base_quantity", "12345678901.125"),
	}
	fields := []string{"line_total", "line_tax", "line_tax_percent", "line_base_quantity"}

	assertRun := func(res BatchResult) {
		t.Helper()
		if len(res.Errors) != len(fields) || res.QuarantinedInvoices != len(fields) || res.ReadyInvoices != 1 {
			t.Fatalf("errors = %+v quarantined = %d ready = %d, want %d, %d, 1", res.Errors, res.QuarantinedInvoices, res.ReadyInvoices, len(fields), len(fields))
		}
		for i, f := range fields {
			e, ok := errorFor(res, i+2)
			if !ok || e.Field != f || e.Message != nrsRange(f) {
				t.Errorf("%s: error = %+v (found %v), want %q", f, e, ok, nrsRange(f))
			}
		}
	}

	assertRun(r.do(true, rows...))
	if sent := r.sent(); len(sent) != 1 || sent["LONG-OK"].InvoiceNumber != "LONG-OK" {
		t.Errorf("dry run: gate got %v, want only LONG-OK", r.refs())
	}
	if n := r.count(); n != 0 {
		t.Fatalf("dry run stored %d invoices", n)
	}

	assertRun(r.do(false, rows...))
	for _, num := range []string{"LONG-1", "LONG-2", "LONG-3", "LONG-4"} {
		if n := countInvoicesByNumber(t, r.super, r.entityID, num); n != 0 {
			t.Errorf("%s stored %d times, want 0", num, n)
		}
	}
	if n := countInvoicesByNumber(t, r.super, r.entityID, "LONG-OK"); n != 1 {
		t.Errorf("LONG-OK stored %d times, want 1 (the 12 and 11 digit values fit)", n)
	}
}

func TestImport_AnOverScaleNewNumberQuarantines(t *testing.T) {
	r := newNRSRun(t)
	res := r.do(true,
		nrsRow("SC-1", "line_tax_percent", "7.555"),
		nrsRow("SC-2", "line_base_quantity", "1.0005"),
		nrsRow("SC-3", "line_total", "1.005"),
		nrsRow("SC-4", "line_tax", "0.001"),
		nrsRow("SC-OK", "line_tax_percent", "7.500", "line_base_quantity", "1.2340",
			"line_total", "123456789012.500", "line_tax", "0.010"),
	)
	want := map[int]string{2: "line_tax_percent", 3: "line_base_quantity", 4: "line_total", 5: "line_tax"}
	if len(res.Errors) != len(want) {
		t.Fatalf("errors = %+v, want %d", res.Errors, len(want))
	}
	for row, f := range want {
		e, ok := errorFor(res, row)
		if !ok || e.Field != f || e.Message != nrsRange(f) {
			t.Errorf("row %d: error = %+v (found %v), want %s %q", row, e, ok, f, nrsRange(f))
		}
	}
	sent := r.sent()
	ok := sent["SC-OK"]
	if len(sent) != 1 || len(ok.LineItems) != 1 {
		t.Fatalf("gate got %v, want only SC-OK", r.refs())
	}
	checkWants(t, []nrsWant{
		{"SC-OK tax_percent", sv(ok.LineItems[0].TaxPercent), "7.500"},
		{"SC-OK base_quantity", sv(ok.LineItems[0].BaseQuantity), "1.2340"},
		{"SC-OK line_total", sv(ok.LineItems[0].LineTotal), "123456789012.500"},
	})
}
