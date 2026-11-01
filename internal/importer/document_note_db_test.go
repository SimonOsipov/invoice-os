package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
)

// dnFix is one tenant and client with an accepted original (IRN, 1075.00, three lines) and a
// document read as a note.
type dnFix struct {
	super, app *pgxpool.Pool
	svc        *Service
	gate       *invoice.Gate
	tenant     string
	entity     string
	doc        string
	orig       string
}

func dnReading() map[string]*string {
	return map[string]*string{
		"invoice_number": sxPtr("READ-NUM"), "issue_date": sxPtr("2026-03-19"), "due_date": sxPtr("2026-04-30"),
		"buyer_tin": sxPtr("99999999-0001"), "buyer_name": sxPtr("Read Buyer Ltd"), "buyer_email": sxPtr("read@buyer.test"),
		"buyer_telephone": sxPtr("+2348000009999"), "buyer_street": sxPtr("9 Read St"), "buyer_city": sxPtr("Kano"),
		"buyer_postal_zone": sxPtr("700001"), "buyer_country": sxPtr("NG"), "buyer_state": sxPtr("KN"), "buyer_lga": sxPtr("Nasarawa"),
		"currency": sxPtr("NGN"), "subtotal": sxPtr("500.00"), "vat": sxPtr("37.50"), "total": sxPtr("537.50"),
		"line_items[1].description": sxPtr("Returned widgets"), "line_items[1].quantity": sxPtr("2"),
		"line_items[1].unit_price": sxPtr("100.00"), "line_items[1].line_total": sxPtr("200.00"), "line_items[1].line_tax": sxPtr("15.00"),
		"line_items[2].description": sxPtr("Returned gadgets"), "line_items[2].quantity": sxPtr("3"),
		"line_items[2].unit_price": sxPtr("100.00"), "line_items[2].line_total": sxPtr("300.00"), "line_items[2].line_tax": sxPtr("22.50"),
	}
}

// dnSeedOriginal seeds an accepted commercial invoice with an IRN, a full buyer, and three lines.
func dnSeedOriginal(t *testing.T, super *pgxpool.Pool, tenant, entity, number string) string {
	t.Helper()
	ctx := context.Background()
	id := seedInvoice(t, super, tenant, entity, number)
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET status = 'accepted', irn = $2, issue_date = '2026-03-04', currency = 'NGN', tax_currency_code = 'USD', invoice_kind = 'B2B',
		        subtotal = 1000.00, vat = 75.00, total = 1075.00, due_date = '2026-04-01',
		        buyer_tin = '87654321-0001', buyer_name = 'Original Buyer Ltd', buyer_email = 'orig@buyer.test', buyer_telephone = '+2348000000001',
		        buyer_street = '1 Buyer St', buyer_city = 'Lagos', buyer_postal_zone = '100001', buyer_country = 'NG', buyer_state = 'LA', buyer_lga = 'Ikeja'
		  WHERE id = $1`, id, "IRN-"+number); err != nil {
		t.Fatalf("make %s accepted: %v", number, err)
	}
	for i, l := range [][4]string{{"Orig widget", "5", "100.00", "500.00"}, {"Orig gadget", "2", "100.00", "200.00"}, {"Orig service", "1", "300.00", "300.00"}} {
		if _, err := super.Exec(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total, line_tax)
			 VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, 1.00)`, tenant, id, i+1, l[0], l[1], l[2], l[3]); err != nil {
			t.Fatalf("seed original line %d: %v", i+1, err)
		}
	}
	return id
}

// dnNewFix seeds the tenant, a reading (nil takes dnReading) and a verdict ("" seeds none).
func dnNewFix(t *testing.T, label, verdict string, values map[string]*string) *dnFix {
	t.Helper()
	super, app := dbTestPools(t)
	f := &dnFix{super: super, app: app, gate: rbRealGate(t, app)}
	f.svc = newTestServiceWithGate(app, f.gate)
	f.tenant, f.entity = rbTenant(t, super, label)
	f.orig = dnSeedOriginal(t, super, f.tenant, f.entity, "INV-ORIG-1")
	if values == nil {
		values = dnReading()
	}
	f.doc = docSeedDocument(t, super, f.tenant)
	docSeedExtraction(t, super, f.tenant, f.doc, values)
	if verdict != "" {
		dtSetVerdict(t, super, f.doc, verdict)
	}
	return f
}

func (f *dnFix) ctx() context.Context { return sxIdentity(context.Background(), f.tenant) }

func (f *dnFix) in(typ, number string) DocumentImport {
	return DocumentImport{EntityID: f.entity, DocumentID: f.doc, DocumentType: typ, OriginalInvoiceID: f.orig, InvoiceNumber: number}
}

func (f *dnFix) run(in DocumentImport) (BatchResult, error) {
	return f.svc.ImportDocumentNote(f.ctx(), in)
}

// importOK is the control: the import succeeds and returns its note id.
func (f *dnFix) importOK(t *testing.T, typ, number string) (BatchResult, string) {
	t.Helper()
	res, err := f.run(f.in(typ, number))
	if err != nil {
		t.Fatalf("ImportDocumentNote %s %s: %v", typ, number, err)
	}
	if res.ReadyInvoices != 1 {
		t.Fatalf("ImportDocumentNote %s %s: result = %+v, want one ready invoice", typ, number, res)
	}
	return res, f.noteID(t)
}

func (f *dnFix) noteID(t *testing.T) string {
	t.Helper()
	if n := countInvoicesCitingDocument(t, f.super, f.doc); n != 1 {
		t.Fatalf("invoices citing the document = %d, want 1", n)
	}
	return mustDNString(t, f.super, `SELECT id::text FROM invoices WHERE source_document_id = $1`, f.doc)
}

func (f *dnFix) setOrig(t *testing.T, col, val string) {
	t.Helper()
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET `+col+` = $1 WHERE id = $2`, val, f.orig); err != nil {
		t.Fatalf("set original %s: %v", col, err)
	}
}

// validate is Re-validate: the real gate on the stored note.
func (f *dnFix) validate(t *testing.T, noteID string) (invoice.Invoice, []invoice.Violation) {
	t.Helper()
	inv, _, err := f.gate.Validate(f.ctx(), noteID)
	if err != nil {
		t.Fatalf("Gate.Validate: %v", err)
	}
	var vs []invoice.Violation
	if err := json.Unmarshal(inv.Violations, &vs); err != nil {
		t.Fatalf("decode violations %s: %v", inv.Violations, err)
	}
	return inv, vs
}

func mustDNString(t *testing.T, super *pgxpool.Pool, q string, args ...any) string {
	t.Helper()
	var s string
	if err := super.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		t.Fatalf("read (%s): %v", q, err)
	}
	return s
}

// dnCol reads one invoices column as text; "<NULL>" for NULL.
func dnCol(t *testing.T, super *pgxpool.Pool, id, col string) string {
	t.Helper()
	return mustDNString(t, super, `SELECT coalesce(to_jsonb(i)->>$2, '<NULL>') FROM invoices i WHERE id = $1`, id, col)
}

func dnWantCol(t *testing.T, super *pgxpool.Pool, id, col, want string) {
	t.Helper()
	if got := dnCol(t, super, id, col); got != want {
		t.Errorf("%s = %s, want %s", col, got, want)
	}
}

func dnWantMoney(t *testing.T, what, got, want string) {
	t.Helper()
	g, errG := decimal.NewFromString(got)
	w, errW := decimal.NewFromString(want)
	if errG != nil || errW != nil || !g.Equal(w) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

type dnBatch struct {
	ID, Status            string
	Total, Valid, Invalid int
}

func dnBatches(t *testing.T, super *pgxpool.Pool, doc string) []dnBatch {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT id::text, status, rows_total, rows_valid, rows_invalid FROM import_batches WHERE document_id = $1 ORDER BY created_at, id`, doc)
	if err != nil {
		t.Fatalf("read batches: %v", err)
	}
	defer rows.Close()
	out := []dnBatch{}
	for rows.Next() {
		var b dnBatch
		if err := rows.Scan(&b.ID, &b.Status, &b.Total, &b.Valid, &b.Invalid); err != nil {
			t.Fatalf("scan batch: %v", err)
		}
		out = append(out, b)
	}
	return out
}

type dnLine struct{ Desc, Qty, Price, Total, Tax string }

func dnLines(t *testing.T, super *pgxpool.Pool, invoiceID string) []dnLine {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT description, quantity::text, unit_price::text, line_total::text, coalesce(line_tax::text, '<NULL>')
		   FROM line_items WHERE invoice_id = $1 ORDER BY line_no`, invoiceID)
	if err != nil {
		t.Fatalf("read lines: %v", err)
	}
	defer rows.Close()
	out := []dnLine{}
	for rows.Next() {
		var l dnLine
		if err := rows.Scan(&l.Desc, &l.Qty, &l.Price, &l.Total, &l.Tax); err != nil {
			t.Fatalf("scan line: %v", err)
		}
		out = append(out, l)
	}
	return out
}

func dnHasViolation(vs []invoice.Violation, key string) bool {
	for _, v := range vs {
		if v.RuleKey == key {
			return true
		}
	}
	return false
}

// dnWantDraftNote asserts the columns and batch every imported note shares.
func dnWantDraftNote(t *testing.T, f *dnFix, res BatchResult, noteID, docType, number string) {
	t.Helper()
	if res.Status != "completed" || res.ReadyInvoices != 1 || res.RowsTotal != 1 || res.RowsValid != 1 || res.RowsInvalid != 0 || len(res.Errors) != 0 {
		t.Errorf("result = %+v, want completed, rows 1/1/0, one ready invoice, no errors", res)
	}
	batches := dnBatches(t, f.super, f.doc)
	if len(batches) != 1 || batches[0] != (dnBatch{ID: res.ID, Status: "completed", Total: 1, Valid: 1}) {
		t.Errorf("batches = %+v, want the one completed batch %s with ready 1", batches, res.ID)
	}
	dnWantCol(t, f.super, noteID, "status", "draft")
	dnWantCol(t, f.super, noteID, "document_type", docType)
	dnWantCol(t, f.super, noteID, "original_invoice_id", f.orig)
	dnWantCol(t, f.super, noteID, "invoice_number", number)
	dnWantCol(t, f.super, noteID, "source_document_id", f.doc)
	dnWantCol(t, f.super, noteID, "import_batch_id", res.ID)
}

func TestImportDocumentNote_CreditNoteIsADraftLinkedToItsOriginal(t *testing.T) {
	f := dnNewFix(t, "DN credit", "credit note", nil)
	res, noteID := f.importOK(t, "credit_note", "CN-3401")
	dnWantDraftNote(t, f, res, noteID, "credit_note", "CN-3401")
	// the typed number wins over the read one
	if n := countInvoicesByNumber(t, f.super, f.entity, "READ-NUM"); n != 0 {
		t.Errorf("invoices numbered with the read number = %d, want 0", n)
	}
}

func TestImportDocumentNote_DebitNote(t *testing.T) {
	f := dnNewFix(t, "DN debit", "debit note", func() map[string]*string {
		v := dnReading()
		v["subtotal"], v["vat"], v["total"] = sxPtr("1395.35"), sxPtr("104.65"), sxPtr("1500.00") // above the original's 1075.00
		return v
	}())
	res, noteID := f.importOK(t, "debit_note", "DN-77")
	dnWantDraftNote(t, f, res, noteID, "debit_note", "DN-77")

	inv, vs := f.validate(t, noteID)
	if inv.RuleSetVersionID == nil {
		t.Fatal("Re-validate stamped no rule set, so it judged nothing")
	}
	if dnHasViolation(vs, "credit-note-over-cap") {
		t.Errorf("a debit note of 1500.00 on 1075.00 broke the credit cap: %+v", vs)
	}
}

func TestImportDocumentNote_PartiesComeFromTheOriginal(t *testing.T) {
	f := dnNewFix(t, "DN parties", "credit note", nil)
	_, noteID := f.importOK(t, "credit_note", "CN-P1")

	for _, col := range []string{"buyer_tin", "buyer_name", "buyer_email", "buyer_telephone", "buyer_street", "buyer_city",
		"buyer_postal_zone", "buyer_country", "buyer_state", "buyer_lga", "invoice_kind", "tax_currency_code"} {
		want := dnCol(t, f.super, f.orig, col)
		if want == "<NULL>" {
			t.Fatalf("seed: the original's %s is NULL, the comparison would hold over nothing", col)
		}
		dnWantCol(t, f.super, noteID, col, want)
	}
	dnWantCol(t, f.super, noteID, "tax_currency_code", "USD")
	dnWantCol(t, f.super, noteID, "due_date", "<NULL>")
	row := mustDNString(t, f.super, `SELECT to_jsonb(i)::text FROM invoices i WHERE id = $1`, noteID)
	for _, read := range []string{"Read Buyer Ltd", "read@buyer.test", "99999999-0001"} {
		if strings.Contains(row, read) {
			t.Errorf("the stored note carries the read buyer value %q", read)
		}
	}
}

func TestImportDocumentNote_AnUnreadableDueDateIsNotARefusal(t *testing.T) {
	v := dnReading()
	v["due_date"] = sxPtr("31st of March")
	f := dnNewFix(t, "DN due date", "credit note", v)
	_, noteID := f.importOK(t, "credit_note", "CN-D1")
	dnWantCol(t, f.super, noteID, "status", "draft")
	dnWantCol(t, f.super, noteID, "due_date", "<NULL>")
}

func TestImportDocumentNote_LinesAndAmountsComeFromTheReading(t *testing.T) {
	v := dnReading()
	delete(v, "currency")
	f := dnNewFix(t, "DN lines", "credit note", v)
	f.setOrig(t, "currency", "EUR")
	if n := mustDNString(t, f.super, `SELECT count(*)::text FROM line_items WHERE invoice_id = $1`, f.orig); n != "3" {
		t.Fatalf("seed: the original has %s lines, want 3", n)
	}
	_, noteID := f.importOK(t, "credit_note", "CN-L1")

	dnWantCol(t, f.super, noteID, "issue_date", "2026-03-19")
	for col, want := range map[string]string{"subtotal": "500.00", "vat": "37.50", "total": "537.50"} {
		dnWantMoney(t, col, dnCol(t, f.super, noteID, col), want)
	}
	dnWantCol(t, f.super, noteID, "currency", "EUR") // no read currency takes the original's
	lines := dnLines(t, f.super, noteID)
	if len(lines) != 2 {
		t.Fatalf("stored lines = %+v, want the two read lines", lines)
	}
	for i, want := range []dnLine{{"Returned widgets", "2", "100.00", "200.00", "15.00"}, {"Returned gadgets", "3", "100.00", "300.00", "22.50"}} {
		got := lines[i]
		if got.Desc != want.Desc {
			t.Errorf("line %d description = %q, want %q", i+1, got.Desc, want.Desc)
		}
		dnWantMoney(t, fmt.Sprintf("line %d quantity", i+1), got.Qty, want.Qty)
		dnWantMoney(t, fmt.Sprintf("line %d unit_price", i+1), got.Price, want.Price)
		dnWantMoney(t, fmt.Sprintf("line %d line_total", i+1), got.Total, want.Total)
		dnWantMoney(t, fmt.Sprintf("line %d line_tax", i+1), got.Tax, want.Tax)
	}
}

func TestImportDocumentNote_NegativeReadingsStorePositive(t *testing.T) {
	t.Run("negative header and lines", func(t *testing.T) {
		v := dnReading()
		v["subtotal"], v["vat"], v["total"] = sxPtr("-500.00"), sxPtr("-37.50"), sxPtr("-537.50")
		v["line_items[1].quantity"], v["line_items[1].unit_price"] = sxPtr("-2"), sxPtr("-100.00")
		v["line_items[1].line_total"], v["line_items[1].line_tax"] = sxPtr("-200.00"), sxPtr("-15.00")
		v["line_items[2].quantity"], v["line_items[2].unit_price"] = sxPtr("-3"), sxPtr("-100.00")
		v["line_items[2].line_total"], v["line_items[2].line_tax"] = sxPtr("-300.00"), sxPtr("-22.50")
		f := dnNewFix(t, "DN negative", "credit note", v)
		_, noteID := f.importOK(t, "credit_note", "CN-N1")

		for col, want := range map[string]string{"subtotal": "500.00", "vat": "37.50", "total": "537.50"} {
			dnWantMoney(t, col, dnCol(t, f.super, noteID, col), want)
		}
		lines := dnLines(t, f.super, noteID)
		if len(lines) != 2 {
			t.Fatalf("stored lines = %+v, want two", lines)
		}
		for i, want := range []dnLine{{"", "2", "100.00", "200.00", "15.00"}, {"", "3", "100.00", "300.00", "22.50"}} {
			dnWantMoney(t, fmt.Sprintf("line %d quantity", i+1), lines[i].Qty, want.Qty)
			dnWantMoney(t, fmt.Sprintf("line %d unit_price", i+1), lines[i].Price, want.Price)
			dnWantMoney(t, fmt.Sprintf("line %d line_total", i+1), lines[i].Total, want.Total)
			dnWantMoney(t, fmt.Sprintf("line %d line_tax", i+1), lines[i].Tax, want.Tax)
		}
	})

	// Each money field alone: a mutant that skips one field is red on that field only.
	for _, key := range []string{"subtotal", "vat", "total", "line_items[1].quantity", "line_items[1].unit_price", "line_items[1].line_total", "line_items[1].line_tax",
		"line_items[2].quantity", "line_items[2].unit_price", "line_items[2].line_total", "line_items[2].line_tax"} {
		t.Run("only "+key+" negative", func(t *testing.T) {
			v := dnReading()
			if v[key] == nil {
				t.Fatalf("seed: reading has no %s", key)
			}
			v[key] = sxPtr("-" + *v[key])
			f := dnNewFix(t, "DN neg one", "credit note", v)
			_, noteID := f.importOK(t, "credit_note", "CN-N2")
			for col, want := range map[string]string{"subtotal": "500.00", "vat": "37.50", "total": "537.50"} {
				dnWantMoney(t, col, dnCol(t, f.super, noteID, col), want)
			}
			lines := dnLines(t, f.super, noteID)
			if len(lines) != 2 {
				t.Fatalf("stored lines = %+v, want two", lines)
			}
			for i, want := range []dnLine{{"", "2", "100.00", "200.00", "15.00"}, {"", "3", "100.00", "300.00", "22.50"}} {
				dnWantMoney(t, fmt.Sprintf("line %d quantity", i+1), lines[i].Qty, want.Qty)
				dnWantMoney(t, fmt.Sprintf("line %d unit_price", i+1), lines[i].Price, want.Price)
				dnWantMoney(t, fmt.Sprintf("line %d line_total", i+1), lines[i].Total, want.Total)
				dnWantMoney(t, fmt.Sprintf("line %d line_tax", i+1), lines[i].Tax, want.Tax)
			}
		})
	}

	t.Run("scale is kept on a stored negative", func(t *testing.T) {
		v := dnReading()
		v["total"], v["line_items[1].unit_price"] = sxPtr("-537.50"), sxPtr("-100.10")
		f := dnNewFix(t, "DN scale", "credit note", v)
		_, noteID := f.importOK(t, "credit_note", "CN-SC")
		dnWantCol(t, f.super, noteID, "total", "537.50")
		if lines := dnLines(t, f.super, noteID); len(lines) != 2 || lines[0].Price != "100.10" {
			t.Errorf("lines = %+v, want line 1 unit_price 100.10", lines)
		}
	})

	// A value a person would read as an amount still refuses: the note path takes plain decimals only.
	for name, tc := range map[string]struct{ key, val string }{
		"a non-decimal header total":      {"total", "abc"},
		"a non-decimal line price":        {"line_items[1].unit_price", "abc"},
		"a thousands separator on total":  {"total", "1,500.00"},
		"a negative thousands separator":  {"subtotal", "-1,500.00"},
		"a currency mark on a line total": {"line_items[2].line_total", "\u20a6300.00"},
		"a currency code on vat":          {"vat", "NGN 37.50"},
		"a non-decimal line tax":          {"line_items[2].line_tax", "n/a"},
		"a non-decimal quantity":          {"line_items[1].quantity", "two"},
	} {
		t.Run(name, func(t *testing.T) {
			v := dnReading()
			v[tc.key] = sxPtr(tc.val)
			f := dnNewFix(t, "DN abc", "credit note", v)
			_, err := f.run(f.in("credit_note", "CN-ABC"))
			if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), tc.val) {
				t.Errorf("err = %v, want ErrValidation naming %s", err, tc.val)
			}
			if n := len(dnBatches(t, f.super, f.doc)); n != 0 {
				t.Errorf("batches = %d, want 0", n)
			}
			if n := countInvoicesCitingDocument(t, f.super, f.doc); n != 0 {
				t.Errorf("invoices citing the document = %d, want 0", n)
			}
		})
	}
}

func TestImportDocumentNote_EachRefusalWritesNothing(t *testing.T) {
	notFound := []error{invoice.ErrNotFound, invoice.ErrOriginalNotFound, ErrNotFound}
	for _, tc := range []struct {
		name    string
		verdict string
		setup   func(t *testing.T, f *dnFix, in *DocumentImport)
		want    []error
	}{
		{"draft original", "credit note", func(t *testing.T, f *dnFix, _ *DocumentImport) { f.setOrig(t, "status", "draft") },
			[]error{invoice.ErrOriginalNotIssued}},
		{"accepted original without an IRN", "credit note", func(t *testing.T, f *dnFix, _ *DocumentImport) {
			if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET irn = NULL WHERE id = $1`, f.orig); err != nil {
				t.Fatalf("clear irn: %v", err)
			}
		}, []error{invoice.ErrOriginalNotIssued}},
		{"an accepted note as the original", "credit note", func(t *testing.T, f *dnFix, in *DocumentImport) {
			note := dnSeedOriginal(t, f.super, f.tenant, f.entity, "CN-AS-ORIG")
			if _, err := f.super.Exec(context.Background(),
				`UPDATE invoices SET document_type = 'credit_note', original_invoice_id = $2 WHERE id = $1`, note, f.orig); err != nil {
				t.Fatalf("make a note: %v", err)
			}
			in.OriginalInvoiceID = note
		}, []error{invoice.ErrOriginalIsANote}},
		{"an original of another client", "credit note", func(t *testing.T, f *dnFix, in *DocumentImport) {
			other := seedEntity(t, f.super, f.tenant, "DN other client")
			in.OriginalInvoiceID = dnSeedOriginal(t, f.super, f.tenant, other, "INV-OTHER-CLIENT")
		}, []error{invoice.ErrOriginalOtherEntity}},
		{"an original of another tenant", "credit note", func(t *testing.T, f *dnFix, in *DocumentImport) {
			tenant, entity := rbTenant(t, f.super, "DN foreign")
			in.OriginalInvoiceID = dnSeedOriginal(t, f.super, tenant, entity, "INV-FOREIGN")
		}, notFound},
		{"a random original id", "credit note", func(t *testing.T, f *dnFix, in *DocumentImport) { in.OriginalInvoiceID = uuid.NewString() }, notFound},
		{"the verdict is the other note type", "credit note", func(t *testing.T, f *dnFix, in *DocumentImport) { in.DocumentType = "debit_note" },
			[]error{ErrNoteTypeNotRead}},
		{"the document has no verdict", "", func(t *testing.T, f *dnFix, _ *DocumentImport) {}, []error{ErrNoteTypeNotRead}},
		{"the number is taken", "credit note", func(t *testing.T, f *dnFix, in *DocumentImport) {
			seedInvoice(t, f.super, f.tenant, f.entity, "CN-TAKEN")
			in.InvoiceNumber = "CN-TAKEN"
		}, []error{invoice.ErrDuplicateNumber}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := dnNewFix(t, "DN refuse", tc.verdict, nil)
			in := f.in("credit_note", "CN-REF")
			tc.setup(t, f, &in)
			batches := mustDNString(t, f.super, `SELECT count(*)::text FROM import_batches WHERE tenant_id = $1`, f.tenant)
			invoices := mustDNString(t, f.super, `SELECT count(*)::text FROM invoices WHERE tenant_id = $1`, f.tenant)

			res, err := f.run(in)

			ok := false
			for _, w := range tc.want {
				ok = ok || errors.Is(err, w)
			}
			if !ok {
				t.Errorf("err = %v, want one of %v", err, tc.want)
			}
			if res.ID != "" {
				t.Errorf("result = %+v, want none", res)
			}
			if got := mustDNString(t, f.super, `SELECT count(*)::text FROM import_batches WHERE tenant_id = $1`, f.tenant); got != batches {
				t.Errorf("batches %s -> %s, want unchanged", batches, got)
			}
			if got := mustDNString(t, f.super, `SELECT count(*)::text FROM invoices WHERE tenant_id = $1`, f.tenant); got != invoices {
				t.Errorf("invoices %s -> %s, want unchanged", invoices, got)
			}
		})
	}

	t.Run("control: the same fixture imports when nothing is wrong", func(t *testing.T) {
		f := dnNewFix(t, "DN refuse control", "credit note", nil)
		f.importOK(t, "credit_note", "CN-REF")
	})
}

func TestImportDocumentNote_ARacedNumberFailsItsBatch(t *testing.T) {
	f := dnNewFix(t, "DN race", "credit note", nil)
	ctx := context.Background()
	name := "dn_race_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// The trigger takes the number once the batch row exists, which is after the precheck.
	for _, stmt := range []string{
		fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $f$
			BEGIN
			  IF NEW.document_id = '%s'::uuid THEN
			    INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES (NEW.tenant_id, NEW.entity_id, 'CN-RACED');
			  END IF;
			  RETURN NEW;
			END $f$`, name, f.doc),
		fmt.Sprintf(`CREATE TRIGGER %s AFTER INSERT ON import_batches FOR EACH ROW EXECUTE FUNCTION %s()`, name, name),
	} {
		if _, err := f.super.Exec(ctx, stmt); err != nil {
			t.Fatalf("create the race trigger: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.super.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON import_batches`, name))
		_, _ = f.super.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, name))
	})

	_, err := f.run(f.in("credit_note", "CN-RACED"))

	if !errors.Is(err, invoice.ErrDuplicateNumber) {
		t.Errorf("err = %v, want ErrDuplicateNumber", err)
	}
	if got := countInvoicesByNumber(t, f.super, f.entity, "CN-RACED"); got != 1 {
		t.Errorf("invoices numbered CN-RACED = %d, want only the racing one", got)
	}
	batches := dnBatches(t, f.super, f.doc)
	if len(batches) != 1 || batches[0].Status != "failed" {
		t.Errorf("batches = %+v, want one batch, failed", batches)
	}
	if n := countInvoicesCitingDocument(t, f.super, f.doc); n != 0 {
		t.Errorf("invoices citing the document = %d, want 0", n)
	}
}

func TestImportDocumentNote_AFiledDocumentIsNotFiledAgain(t *testing.T) {
	f := dnNewFix(t, "DN filed", "credit note", nil)
	f.importOK(t, "credit_note", "CN-FIRST")

	_, err := f.run(f.in("credit_note", "CN-SECOND"))

	if !errors.Is(err, ErrDocumentAlreadyFiled) {
		t.Errorf("err = %v, want ErrDocumentAlreadyFiled", err)
	}
	if n := countInvoicesCitingDocument(t, f.super, f.doc); n != 1 {
		t.Errorf("invoices citing the document = %d, want 1", n)
	}
	if n := len(dnBatches(t, f.super, f.doc)); n != 1 {
		t.Errorf("batches = %d, want the first import's only", n)
	}
	if n := countInvoicesByNumber(t, f.super, f.entity, "CN-SECOND"); n != 0 {
		t.Errorf("invoices numbered CN-SECOND = %d, want 0", n)
	}
}

func TestImportDocumentNote_OverCapBreaksARuleAndStaysDraftAtRevalidate(t *testing.T) {
	v := dnReading()
	v["subtotal"], v["vat"], v["total"] = sxPtr("139.53"), sxPtr("10.47"), sxPtr("150.00")
	f := dnNewFix(t, "DN over cap", "credit note", v)
	f.setOrig(t, "total", "100.00")
	jobID := extractionJobIDForDocument(t, f.super, f.doc)

	_, noteID := f.importOK(t, "credit_note", "CN-OVER")

	found := false
	for _, r := range rbRows(t, f.super, jobID) {
		found = found || (r.field == "total" && r.ruleKey == "credit-note-over-cap")
	}
	if !found {
		t.Errorf("extraction_rule_breaks = %+v, want credit-note-over-cap on total", rbRows(t, f.super, jobID))
	}
	inv, vs := f.validate(t, noteID)
	if inv.Status != invoice.StatusDraft || !dnHasViolation(vs, "credit-note-over-cap") {
		t.Errorf("after Re-validate: status %s, violations %+v, want draft with credit-note-over-cap", inv.Status, vs)
	}
}

func TestImportDocumentNote_AForeignCurrencyIsCaughtAtRevalidate(t *testing.T) {
	v := dnReading()
	v["currency"] = sxPtr("USD")
	f := dnNewFix(t, "DN currency", "credit note", v)
	_, noteID := f.importOK(t, "credit_note", "CN-USD")
	dnWantCol(t, f.super, noteID, "currency", "USD")

	inv, vs := f.validate(t, noteID)
	if !dnHasViolation(vs, "credit-note-currency-mismatch") {
		t.Errorf("violations = %+v (status %s), want credit-note-currency-mismatch", vs, inv.Status)
	}
}

func TestImportDocumentNote_AnUnreadableIssueDateNamesTheNotePath(t *testing.T) {
	v := dnReading()
	v["issue_date"] = sxPtr("19 March")
	f := dnNewFix(t, "DN issue date", "credit note", v)

	_, err := f.run(f.in("credit_note", "CN-DATE"))

	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	for _, want := range []string{"19 March", "Issue this note from its original invoice to carry on."} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "Enter this invoice manually") {
		t.Errorf("err = %q, must not send a note to invoice entry", err.Error())
	}
	if n := len(dnBatches(t, f.super, f.doc)); n != 0 {
		t.Errorf("batches = %d, want 0", n)
	}
	if n := countInvoicesCitingDocument(t, f.super, f.doc); n != 0 {
		t.Errorf("invoices citing the document = %d, want 0", n)
	}
}

func TestImportDocumentNote_ABlankReadCurrencyTakesTheOriginals(t *testing.T) {
	for name, cur := range map[string]*string{"spaces": sxPtr("   "), "a tab": sxPtr("\t")} {
		t.Run(name, func(t *testing.T) {
			v := dnReading()
			v["currency"] = cur
			f := dnNewFix(t, "DN blank currency", "credit note", v)
			f.setOrig(t, "currency", "EUR")
			_, noteID := f.importOK(t, "credit_note", "CN-BC")
			dnWantCol(t, f.super, noteID, "currency", "EUR")
		})
	}
}

func TestImportDocumentNote_AnUnreadableIssueDateSentenceIsExact(t *testing.T) {
	v := dnReading()
	v["issue_date"] = sxPtr(" 19 March ")
	f := dnNewFix(t, "DN issue date exact", "debit note", v)
	_, err := f.run(f.in("debit_note", "DN-DATE"))
	want := `The issue date on this document reads "19 March", which is not a date this importer can read. It accepts dates written as YYYY-MM-DD, such as 2026-03-01. Issue this note from its original invoice to carry on.`
	if !errors.Is(err, ErrValidation) || !strings.HasSuffix(err.Error(), ": "+want) {
		t.Errorf("err = %q, want ErrValidation ending with %q", err, want)
	}
}

// Pinned surprise: an amount beyond the column's range is not refused up front; the batch ends failed.
func TestImportDocumentNote_AnAmountBeyondTheColumnFailsItsBatch(t *testing.T) {
	v := dnReading()
	v["total"] = sxPtr("-99999999999999999999999999.99")
	f := dnNewFix(t, "DN overflow", "credit note", v)

	_, err := f.run(f.in("credit_note", "CN-BIG"))

	if err == nil || errors.Is(err, ErrValidation) {
		t.Errorf("err = %v, want the database's overflow error, not ErrValidation", err)
	}
	if b := dnBatches(t, f.super, f.doc); len(b) != 1 || b[0].Status != "failed" {
		t.Errorf("batches = %+v, want one, failed", b)
	}
	if n := countInvoicesCitingDocument(t, f.super, f.doc); n != 0 {
		t.Errorf("invoices citing the document = %d, want 0", n)
	}
}

// noAge back-dates an invoice so "newest first" has one answer.
func noAge(t *testing.T, f *dnFix, id string, age time.Duration) {
	t.Helper()
	at := time.Now().UTC().Add(-age)
	if _, err := f.super.Exec(context.Background(),
		`UPDATE invoices SET created_at = $2::timestamptz, issue_date = $3::date WHERE id = $1`, id, at, at.Format("2006-01-02")); err != nil {
		t.Fatalf("age invoice %s: %v", id, err)
	}
}

// noBatch files a batch of f.doc under entity, back-dated by age.
func noBatch(t *testing.T, f *dnFix, entity string, age time.Duration) {
	t.Helper()
	if _, err := f.super.Exec(context.Background(),
		`INSERT INTO import_batches (tenant_id, entity_id, document_id, status, rows_total, rows_valid, rows_invalid, errors, created_at)
		 VALUES ($1, $2, $3, 'completed', 0, 0, 0, '[]'::jsonb, $4)`, f.tenant, entity, f.doc, time.Now().UTC().Add(-age)); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
}

func (f *dnFix) originals(t *testing.T) NoteOriginals {
	t.Helper()
	got, err := f.svc.batch.NoteOriginals(f.ctx(), f.doc)
	if err != nil {
		t.Fatalf("NoteOriginals: %v", err)
	}
	return got
}

func noIDs(rows []NoteOriginalRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// noMixed seeds, beside f.orig, every invoice the picker must skip and one more original that
// carries a note. It returns that original; f.orig is older, so the order is [it, f.orig].
func noMixed(t *testing.T, f *dnFix) string {
	t.Helper()
	ctx := context.Background()
	withNote := dnSeedOriginal(t, f.super, f.tenant, f.entity, "INV-ORIG-2")
	noAge(t, f, f.orig, 2*time.Hour)
	noAge(t, f, withNote, time.Hour)
	for i, st := range []string{"draft", "validated", "queued", "submitted", "rejected", "failed"} {
		id := seedInvoice(t, f.super, f.tenant, f.entity, fmt.Sprintf("INV-%s", st))
		if _, err := f.super.Exec(ctx, `UPDATE invoices SET status = $2, irn = $3 WHERE id = $1`, id, st, fmt.Sprintf("IRN-%d", i)); err != nil {
			t.Fatalf("make %s invoice: %v", st, err)
		}
	}
	noIRN := seedInvoice(t, f.super, f.tenant, f.entity, "INV-NO-IRN")
	if _, err := f.super.Exec(ctx, `UPDATE invoices SET status = 'accepted' WHERE id = $1`, noIRN); err != nil {
		t.Fatalf("make accepted invoice without IRN: %v", err)
	}
	note := dnSeedOriginal(t, f.super, f.tenant, f.entity, "CN-ACCEPTED")
	if _, err := f.super.Exec(ctx, `UPDATE invoices SET document_type = 'credit_note', original_invoice_id = $2 WHERE id = $1`, note, withNote); err != nil {
		t.Fatalf("make accepted note: %v", err)
	}
	otherEntity := seedEntity(t, f.super, f.tenant, "NO other client")
	dnSeedOriginal(t, f.super, f.tenant, otherEntity, "INV-OTHER-ENTITY")
	return withNote
}

func TestNoteOriginals_ListsOnlyIssuableCommercialInvoices(t *testing.T) {
	f := dnNewFix(t, "NO-list", "credit note", nil)
	csLinkBatch(t, f.super, f.tenant, f.entity, f.doc)
	withNote := noMixed(t, f)

	got := f.originals(t)

	// The reading names no original, so this also proves a missing read number still lists rows.
	if got.EntityID != f.entity {
		t.Errorf("entity_id = %q, want %q", got.EntityID, f.entity)
	}
	if ids := noIDs(got.Originals); len(ids) != 2 || ids[0] != withNote || ids[1] != f.orig {
		t.Fatalf("originals = %v, want [%s %s]: accepted commercial invoices with an IRN in the batch's entity, newest first", ids, withNote, f.orig)
	}
	if a, b := got.Originals[0], got.Originals[1]; a.NoteCount != 1 || b.NoteCount != 0 {
		t.Errorf("note_count = %d, %d, want 1, 0", a.NoteCount, b.NoteCount)
	}
	row := got.Originals[1]
	if row.InvoiceNumber != "INV-ORIG-1" || row.Currency == nil || *row.Currency != "NGN" || row.Total == nil || *row.Total != "1075.00" ||
		row.BuyerName == nil || *row.BuyerName != "Original Buyer Ltd" || row.IssueDate == nil || row.IssueDate.Format("2006-01-02") != time.Now().UTC().Add(-2*time.Hour).Format("2006-01-02") {
		t.Errorf("row = %+v, want its number, NGN, 1075.00, buyer and issue date", row)
	}

	// No candidate: an empty list, not nil.
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET status = 'draft' WHERE document_type = 'commercial_invoice' AND tenant_id = $1`, f.tenant); err != nil {
		t.Fatalf("unissue every invoice: %v", err)
	}
	if none := f.originals(t); none.Originals == nil || len(none.Originals) != 0 {
		t.Errorf("originals with no candidate = %#v, want an empty, non-nil slice", none.Originals)
	}
}

func TestNoteOriginals_TheMatchSortsFirst(t *testing.T) {
	values := dnReading()
	values["original_invoice_number"] = sxPtr("INV-ORIG-1")
	f := dnNewFix(t, "NO-match", "credit note", values)
	csLinkBatch(t, f.super, f.tenant, f.entity, f.doc)
	mid := dnSeedOriginal(t, f.super, f.tenant, f.entity, "INV-MID")
	newest := dnSeedOriginal(t, f.super, f.tenant, f.entity, "INV-NEWEST")
	noAge(t, f, f.orig, 3*time.Hour)
	noAge(t, f, mid, 2*time.Hour)
	noAge(t, f, newest, time.Hour)

	if ids, want := noIDs(f.originals(t).Originals), []string{f.orig, newest, mid}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("originals = %v, want the match first, then newest first: %v", ids, want)
	}

	// A read number that matches nothing leaves plain newest-first.
	if _, err := f.super.Exec(context.Background(),
		`UPDATE extraction_field_results SET value = 'NO-SUCH-NUMBER' WHERE field_name = 'original_invoice_number'`); err != nil {
		t.Fatalf("change the read number: %v", err)
	}
	if ids, want := noIDs(f.originals(t).Originals), []string{newest, mid, f.orig}; fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("originals with no match = %v, want newest first: %v", ids, want)
	}
}

func TestNoteOriginals_AMatchPastTheCapIsReturned(t *testing.T) {
	values := dnReading()
	values["original_invoice_number"] = sxPtr("INV-ORIG-1")
	f := dnNewFix(t, "NO-cap", "credit note", values)
	csLinkBatch(t, f.super, f.tenant, f.entity, f.doc)
	noAge(t, f, f.orig, 10000*time.Hour)
	if _, err := f.super.Exec(context.Background(),
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number, status, irn, created_at)
		 SELECT $1, $2, 'INV-BULK-' || n, 'accepted', 'IRN-BULK-' || n, now() - n * interval '1 minute'
		   FROM generate_series(1, 500) n`, f.tenant, f.entity); err != nil {
		t.Fatalf("seed 500 more originals: %v", err)
	}

	rows := f.originals(t).Originals

	if len(rows) != 500 {
		t.Fatalf("originals = %d rows, want the cap of 500", len(rows))
	}
	if rows[0].ID != f.orig {
		t.Errorf("first row = %s (%s), want the match %s though it is the oldest of 501", rows[0].ID, rows[0].InvoiceNumber, f.orig)
	}
	if last := rows[len(rows)-1].InvoiceNumber; last != "INV-BULK-499" {
		t.Errorf("last row = %s, want INV-BULK-499: the cap drops the oldest non-match", last)
	}
}

func TestNoteOriginals_EveryRowPassesTheRefusal(t *testing.T) {
	f := dnNewFix(t, "NO-refusal", "credit note", nil)
	csLinkBatch(t, f.super, f.tenant, f.entity, f.doc)
	noMixed(t, f)

	rows := f.originals(t).Originals

	if len(rows) == 0 {
		t.Fatal("originals is empty, want the issuable invoices")
	}
	for _, r := range rows {
		if _, err := f.svc.inv.NoteOriginal(f.ctx(), r.ID); err != nil {
			t.Errorf("listed %s (%s): NoteOriginal = %v, want nil", r.ID, r.InvoiceNumber, err)
		}
	}
}

func TestNoteOriginals_FiledFollowsTheInvoice(t *testing.T) {
	f := dnNewFix(t, "NO-filed", "credit note", nil)
	csLinkBatch(t, f.super, f.tenant, f.entity, f.doc)

	if got := f.originals(t); got.FiledInvoiceID != nil || len(got.Originals) != 1 {
		t.Fatalf("held: filed_invoice_id = %v with %d originals, want null with 1", got.FiledInvoiceID, len(got.Originals))
	}

	_, noteID := f.importOK(t, "credit_note", "CN-NO-1")

	got := f.originals(t)
	if got.FiledInvoiceID == nil || *got.FiledInvoiceID != noteID {
		t.Errorf("filed: filed_invoice_id = %v, want the note %s", got.FiledInvoiceID, noteID)
	}
	if got.EntityID != f.entity {
		t.Errorf("entity_id = %q, want %q", got.EntityID, f.entity)
	}
}

func TestNoteOriginals_EntityIsTheNewestBatch(t *testing.T) {
	f := dnNewFix(t, "NO-batch", "credit note", nil)
	if _, err := f.svc.batch.NoteOriginals(f.ctx(), f.doc); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a document with no batch: err = %v, want ErrNotFound", err)
	}
	entB := seedEntity(t, f.super, f.tenant, "NO client B")
	origB := dnSeedOriginal(t, f.super, f.tenant, entB, "INV-B-1")
	noBatch(t, f, f.entity, 2*time.Hour)
	noBatch(t, f, entB, time.Hour)

	got := f.originals(t)

	if got.EntityID != entB {
		t.Errorf("entity_id = %q, want the newest batch's %q", got.EntityID, entB)
	}
	if ids := noIDs(got.Originals); len(ids) != 1 || ids[0] != origB {
		t.Errorf("originals = %v, want only client B's %s", ids, origB)
	}

	noBatch(t, f, f.entity, 0)
	if got := f.originals(t); got.EntityID != f.entity {
		t.Errorf("entity_id after a newer batch in A = %q, want %q", got.EntityID, f.entity)
	}
}

func TestNoteOriginals_NoteCountCountsEachOriginalsOwnNotesInEveryStatus(t *testing.T) {
	f := dnNewFix(t, "NO-count", "credit note", nil)
	csLinkBatch(t, f.super, f.tenant, f.entity, f.doc)
	other := dnSeedOriginal(t, f.super, f.tenant, f.entity, "INV-ORIG-2")
	noAge(t, f, f.orig, 2*time.Hour)
	noAge(t, f, other, time.Hour)
	for i, st := range []string{"draft", "rejected"} {
		n := dnSeedOriginal(t, f.super, f.tenant, f.entity, fmt.Sprintf("CN-%s", st))
		if _, err := f.super.Exec(context.Background(),
			`UPDATE invoices SET document_type = 'credit_note', original_invoice_id = $2, status = $3, irn = NULL WHERE id = $1`, n, f.orig, st); err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
	}
	n := dnSeedOriginal(t, f.super, f.tenant, f.entity, "DN-ONE")
	if _, err := f.super.Exec(context.Background(),
		`UPDATE invoices SET document_type = 'debit_note', original_invoice_id = $2, status = 'draft', irn = NULL WHERE id = $1`, n, other); err != nil {
		t.Fatalf("debit note: %v", err)
	}

	rows := f.originals(t).Originals

	if len(rows) != 2 || rows[0].ID != other || rows[1].ID != f.orig {
		t.Fatalf("originals = %v, want [%s %s]", noIDs(rows), other, f.orig)
	}
	if rows[0].NoteCount != 1 || rows[1].NoteCount != 2 {
		t.Errorf("note_count = %d, %d, want 1 (one debit note), 2 (credit notes in draft and rejected)", rows[0].NoteCount, rows[1].NoteCount)
	}
}

func TestNoteOriginals_TheReadNumberComesFromTheNewestSucceededJob(t *testing.T) {
	values := dnReading()
	values["original_invoice_number"] = sxPtr("INV-ORIG-1")
	f := dnNewFix(t, "NO-job", "credit note", values)
	csLinkBatch(t, f.super, f.tenant, f.entity, f.doc)
	other := dnSeedOriginal(t, f.super, f.tenant, f.entity, "INV-OTHER")
	noAge(t, f, f.orig, 2*time.Hour)
	noAge(t, f, other, time.Hour)

	// A newer job that has not succeeded names another original: it must not steer the sort.
	for _, state := range []string{"failed", "extracting"} {
		job := seedExtractionJob(t, f.super, f.tenant, f.doc, state, time.Now().UTC().Add(time.Minute))
		seedExtractionField(t, f.super, f.tenant, job, "original_invoice_number", sxPtr("INV-OTHER"), nil, 0, time.Now().UTC())
	}
	if ids := noIDs(f.originals(t).Originals); len(ids) != 2 || ids[0] != f.orig {
		t.Fatalf("with a newer failed and extracting job: originals = %v, want the succeeded job's match %s first", ids, f.orig)
	}

	// A newer succeeded job replaces the reading, and a candidate_rank > 0 reading never steers it.
	job := seedExtractionJob(t, f.super, f.tenant, f.doc, "succeeded", time.Now().UTC().Add(2*time.Minute))
	seedExtractionField(t, f.super, f.tenant, job, "original_invoice_number", sxPtr("INV-OTHER"), nil, 0, time.Now().UTC())
	seedExtractionField(t, f.super, f.tenant, job, "original_invoice_number", sxPtr("INV-ORIG-1"), nil, 1, time.Now().UTC())
	if ids := noIDs(f.originals(t).Originals); len(ids) != 2 || ids[0] != other {
		t.Errorf("with a newer succeeded job: originals = %v, want its rank-0 match %s first", ids, other)
	}
}

func TestNoteOriginals_ADocumentOfAnotherTenantIsNotFound(t *testing.T) {
	f := dnNewFix(t, "NO-xt", "credit note", nil)
	csLinkBatch(t, f.super, f.tenant, f.entity, f.doc)
	otherTenant, _ := rbTenant(t, f.super, "NO-xt-other")
	ctx := sxIdentity(context.Background(), otherTenant)

	got, err := f.svc.batch.NoteOriginals(ctx, f.doc)

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant's document: err = %v, want ErrNotFound", err)
	}
	if got.Originals == nil || len(got.Originals) != 0 || got.EntityID != "" || got.FiledInvoiceID != nil {
		t.Errorf("answer on refusal = %+v, want the empty value", got)
	}
	if _, err := f.svc.batch.NoteOriginals(f.ctx(), f.doc); err != nil {
		t.Fatalf("control: the owner's read: %v", err)
	}
}
