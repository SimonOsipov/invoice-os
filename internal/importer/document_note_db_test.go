package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

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
