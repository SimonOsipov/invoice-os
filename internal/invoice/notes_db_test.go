package invoice

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// inLine is one seeded original line; every field differs per line so a mix-up shows.
type inLine struct {
	desc, qty, price, cat, pct, hsn, isic, prod, serv, sid, unit, base string
}

func inThreeLines() []inLine {
	return []inLine{
		{"Widget", "2.000", "100.00", "STANDARD_VAT", "7.50", "1111.11", "ISIC-1", "PROD-1", "SERV-1", "SID-1", "PCS", "1.000"},
		{"Gadget", "1.000", "50.00", "STANDARD_VAT", "7.50", "2222.22", "ISIC-2", "PROD-2", "SERV-2", "SID-2", "BOX", "2.000"},
		{"Service", "3.000", "10.00", "ZERO_VAT", "0.00", "3333.33", "ISIC-3", "PROD-3", "SERV-3", "SID-3", "HUR", "3.000"},
	}
}

// inOriginal seeds a commercial invoice as the superuser, so IssueNote is the only code under test.
func inOriginal(t *testing.T, super *pgxpool.Pool, tenant, entity, number string, status Status, irn *string, lines []inLine) (string, []string) {
	t.Helper()
	ctx := context.Background()
	id := seedInvoiceAtStatus(t, super, tenant, entity, number, status)
	if irn != nil {
		if _, err := super.Exec(ctx, `UPDATE invoices SET irn = $1 WHERE id = $2`, *irn, id); err != nil {
			t.Fatalf("seed irn: %v", err)
		}
	}
	var lineIDs []string
	for i, l := range lines {
		var lid string
		if err := super.QueryRow(ctx,
			`INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total, line_tax,
			   tax_category, tax_percent, hsn_code, isic_code, product_category, service_category, sellers_item_identification, price_unit, base_quantity)
			 VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $5::numeric * $6::numeric, 1.00, $7, $8::numeric, $9, $10, $11, $12, $13, $14, $15::numeric)
			 RETURNING id`,
			tenant, id, i+1, l.desc, l.qty, l.price, l.cat, l.pct, l.hsn, l.isic, l.prod, l.serv, l.sid, l.unit, l.base,
		).Scan(&lid); err != nil {
			t.Fatalf("seed line %d: %v", i+1, err)
		}
		lineIDs = append(lineIDs, lid)
	}
	return id, lineIDs
}

type inFixture struct {
	super, app *pgxpool.Pool
	store      *Store
	tenant     string
	entity     string
	ctx        context.Context
	original   string
	lineIDs    []string
}

// newInFixture is one tenant, one client and an accepted original with an IRN and three lines.
func newInFixture(t *testing.T, label string) inFixture {
	t.Helper()
	super, app := dbTestPools(t)
	f := inFixture{super: super, app: app, store: NewStore(app)}
	f.tenant = seedTenant(t, super, label)
	f.entity = seedEntity(t, super, f.tenant, "IN client")
	f.ctx = dtCtx(f.tenant)
	f.original, f.lineIDs = inOriginal(t, super, f.tenant, f.entity, "IN-ORIG", StatusAccepted, strPtr("IRN-IN-1"), inThreeLines())
	return f
}

var inIssueDate = time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)

func (f inFixture) req(number string, lines ...NoteLineInput) NoteInput {
	if len(lines) == 0 {
		lines = []NoteLineInput{{LineID: f.lineIDs[0], Quantity: "1.000", UnitPrice: "100.00"}}
	}
	d := inIssueDate
	return NoteInput{DocumentType: submission.CreditNote, InvoiceNumber: number, IssueDate: &d, Lines: lines}
}

type inCounts struct{ invoices, lines, history, audit int }

func (f inFixture) counts(t *testing.T) inCounts {
	t.Helper()
	return inCounts{
		invoices: mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, f.tenant),
		lines:    mustCount(t, f.super, `SELECT count(*) FROM line_items WHERE tenant_id = $1`, f.tenant),
		history:  mustCount(t, f.super, `SELECT count(*) FROM invoice_status_history WHERE tenant_id = $1`, f.tenant),
		audit:    auditCount(t, f.app, f.tenant, "invoice.created"),
	}
}

func (f inFixture) wantCounts(t *testing.T, want inCounts, what string) {
	t.Helper()
	if got := f.counts(t); got != want {
		t.Errorf("%s: rows (invoices, lines, history, audit) = %+v, want unchanged %+v", what, got, want)
	}
}

// issueOK issues the valid request and fails the test on any error: the positive control for a refusal.
func (f inFixture) issueOK(t *testing.T, number string) Invoice {
	t.Helper()
	inv, err := f.store.IssueNote(f.ctx, f.original, f.req(number))
	if err != nil {
		t.Fatalf("IssueNote %s (control, a valid request on an accepted original): %v", number, err)
	}
	return inv
}

func inCol(t *testing.T, super *pgxpool.Pool, id, col string) *string {
	t.Helper()
	var v *string
	if err := super.QueryRow(context.Background(), `SELECT to_jsonb(i)->>$2 FROM invoices i WHERE id = $1`, id, col).Scan(&v); err != nil {
		t.Fatalf("read %s of %s: %v", col, id, err)
	}
	return v
}

func inStr(p *string) string {
	if p == nil {
		return "<NULL>"
	}
	return *p
}

type inStoredLine struct{ lineNo, desc, qty, price, total, tax, cat, pct, hsn, isic, prod, serv, sid, unit, base string }

func inStoredLines(t *testing.T, super *pgxpool.Pool, invoice string) []inStoredLine {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT line_no::text, description, quantity::text, unit_price::text, line_total::text, coalesce(line_tax::text, '<NULL>'),
		        tax_category, tax_percent::text, hsn_code, isic_code, product_category, service_category, sellers_item_identification, price_unit, base_quantity::text
		   FROM line_items WHERE invoice_id = $1 ORDER BY line_no`, invoice)
	if err != nil {
		t.Fatalf("read lines: %v", err)
	}
	defer rows.Close()
	var out []inStoredLine
	for rows.Next() {
		var l inStoredLine
		if err := rows.Scan(&l.lineNo, &l.desc, &l.qty, &l.price, &l.total, &l.tax, &l.cat, &l.pct, &l.hsn, &l.isic, &l.prod, &l.serv, &l.sid, &l.unit, &l.base); err != nil {
			t.Fatalf("scan line: %v", err)
		}
		out = append(out, l)
	}
	return out
}

func TestIssueNote_CreatesADraftFromChosenLines(t *testing.T) {
	f := newInFixture(t, "IN creates")
	// Lines 1 and 3 of 3: line 3 is ZERO_VAT, so the tax is 7.50 on line 1 and 0.00 on line 3.
	req := f.req("CN-IN-1",
		NoteLineInput{LineID: f.lineIDs[0], Quantity: "1.000", UnitPrice: "100.00"},
		NoteLineInput{LineID: f.lineIDs[2], Quantity: "2.000", UnitPrice: "10.00"})

	inv, err := f.store.IssueNote(f.ctx, f.original, req)
	if err != nil {
		t.Fatalf("IssueNote: %v", err)
	}
	if inv.Status != StatusDraft || inv.DocumentType != submission.CreditNote {
		t.Errorf("returned (status, type) = (%q, %q), want (draft, credit_note)", inv.Status, inv.DocumentType)
	}
	if inv.OriginalInvoiceID == nil || *inv.OriginalInvoiceID != f.original {
		t.Errorf("returned original_invoice_id = %v, want %s", inv.OriginalInvoiceID, f.original)
	}
	typ, orig := dtRow(t, f.super, inv.ID)
	if typ != "credit_note" || orig == nil || *orig != f.original {
		t.Errorf("row (document_type, original) = (%q, %v), want (credit_note, %s)", typ, orig, f.original)
	}
	if inv.InvoiceNumber != "CN-IN-1" || inv.IssueDate == nil || !inv.IssueDate.Equal(inIssueDate) {
		t.Errorf("returned (number, issue date) = (%q, %v), want (CN-IN-1, %v)", inv.InvoiceNumber, inv.IssueDate, inIssueDate)
	}
	if len(inv.LineItems) != 2 {
		t.Fatalf("returned lines = %d, want 2 (the chosen ones only)", len(inv.LineItems))
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM line_items WHERE invoice_id = $1`, inv.ID); n != 2 {
		t.Errorf("stored lines = %d, want 2", n)
	}
	if inStr(inCol(t, f.super, inv.ID, "subtotal")) != "120.00" || inStr(inCol(t, f.super, inv.ID, "vat")) != "7.50" || inStr(inCol(t, f.super, inv.ID, "total")) != "127.50" {
		t.Errorf("stored (subtotal, vat, total) = (%s, %s, %s), want (120.00, 7.50, 127.50)",
			inStr(inCol(t, f.super, inv.ID, "subtotal")), inStr(inCol(t, f.super, inv.ID, "vat")), inStr(inCol(t, f.super, inv.ID, "total")))
	}
	lines := inStoredLines(t, f.super, inv.ID)
	if len(lines) != 2 {
		t.Fatalf("stored lines = %d, want 2", len(lines))
	}
	want := []struct{ desc, qty, price, total, tax string }{
		{"Widget", "1.000", "100.00", "100.00", "7.50"},
		{"Service", "2.000", "10.00", "20.00", "0.00"},
	}
	for i, w := range want {
		g := lines[i]
		if g.desc != w.desc || g.qty != w.qty || g.price != w.price || g.total != w.total || g.tax != w.tax {
			t.Errorf("line %d = (%s, %s, %s, %s, %s), want %+v", i+1, g.desc, g.qty, g.price, g.total, g.tax, w)
		}
	}
}

func TestIssueNote_CopiesHeaderAndLeavesSaleFieldsNull(t *testing.T) {
	f := newInFixture(t, "IN header")
	set := map[string]string{
		"buyer_tin": "12345678-0001", "buyer_name": "Buyer Ltd", "currency": "NGN", "tax_currency_code": "USD", "invoice_kind": "B2B",
		"buyer_email": "b@x.test", "buyer_telephone": "+2348000000001", "buyer_street": "1 Buyer St", "buyer_city": "Lagos",
		"buyer_postal_zone": "100001", "buyer_country": "NG", "buyer_state": "LA", "buyer_lga": "Ikeja",
		"supplier_email": "s@x.test", "supplier_telephone": "+2348000000002", "supplier_street": "2 Supplier St", "supplier_city": "Abuja",
		"supplier_postal_zone": "900001", "supplier_country": "NG", "supplier_state": "FC", "supplier_lga": "Garki",
	}
	for col, v := range set {
		if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET `+col+` = $1 WHERE id = $2`, v, f.original); err != nil {
			t.Fatalf("seed %s: %v", col, err)
		}
	}
	if _, err := f.super.Exec(context.Background(),
		`UPDATE invoices SET due_date = '2026-11-30', issue_time = '10:30:00', tax_point_date = '2026-10-01', payment_status = 'PAID' WHERE id = $1`, f.original); err != nil {
		t.Fatalf("seed sale fields: %v", err)
	}
	for _, col := range []string{"due_date", "issue_time", "tax_point_date", "payment_status"} {
		if inCol(t, f.super, f.original, col) == nil {
			t.Fatalf("seed: original %s is NULL, the test would pass vacuously", col)
		}
	}

	inv := f.issueOK(t, "CN-IN-HDR")

	for col, v := range set {
		if got := inStr(inCol(t, f.super, inv.ID, col)); got != v {
			t.Errorf("note %s = %s, want the original's %s", col, got, v)
		}
	}
	for _, col := range []string{"due_date", "issue_time", "tax_point_date", "payment_status", "import_batch_id", "source_document_id", "source_rows"} {
		if got := inCol(t, f.super, inv.ID, col); got != nil {
			t.Errorf("note %s = %s, want NULL", col, *got)
		}
	}
}

func TestIssueNote_LinesKeepTheOriginalsOrderAndFields(t *testing.T) {
	f := newInFixture(t, "IN order")
	inv, err := f.store.IssueNote(f.ctx, f.original, f.req("CN-IN-ORD",
		NoteLineInput{LineID: f.lineIDs[2], Quantity: "5.000", UnitPrice: "9.00"},
		NoteLineInput{LineID: f.lineIDs[0], Quantity: "2.000", UnitPrice: "90.00"}))
	if err != nil {
		t.Fatalf("IssueNote: %v", err)
	}
	lines := inStoredLines(t, f.super, inv.ID)
	if len(lines) != 2 {
		t.Fatalf("stored lines = %d, want 2", len(lines))
	}
	want := []inStoredLine{
		{"1", "Widget", "2.000", "90.00", "180.00", "13.50", "STANDARD_VAT", "7.50", "1111.11", "ISIC-1", "PROD-1", "SERV-1", "SID-1", "PCS", "1.000"},
		{"2", "Service", "5.000", "9.00", "45.00", "0.00", "ZERO_VAT", "0.00", "3333.33", "ISIC-3", "PROD-3", "SERV-3", "SID-3", "HUR", "3.000"},
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("stored line %d = %+v, want %+v", i+1, lines[i], want[i])
		}
	}
}

func TestIssueNote_RefusesANoteAsOriginal(t *testing.T) {
	f := newInFixture(t, "IN note original")
	// Control: the commercial original is accepted with an IRN, and issues.
	f.issueOK(t, "CN-IN-CTRL")
	req := f.req("CN-IN-ON-NOTE", NoteLineInput{LineID: f.lineIDs[0], Quantity: "1.000", UnitPrice: "1.00"})

	for _, c := range []struct {
		name   string
		status Status
		irn    *string
	}{
		{"accepted with an IRN: only the note clause can refuse", StatusAccepted, strPtr("IRN-IN-NOTE")},
		{"draft without an IRN: the note clause comes before the status test", StatusDraft, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := seedInvoiceAtStatus(t, f.super, f.tenant, f.entity, "IN-NOTE-"+string(c.status), c.status)
			if _, err := f.super.Exec(context.Background(),
				`UPDATE invoices SET document_type = 'credit_note', original_invoice_id = $1, irn = $2 WHERE id = $3`, f.original, c.irn, id); err != nil {
				t.Fatalf("seed note: %v", err)
			}
			before := f.counts(t)
			_, err := f.store.IssueNote(f.ctx, id, req)
			if !errors.Is(err, ErrOriginalIsANote) {
				t.Fatalf("IssueNote on a note: err = %v, want ErrOriginalIsANote", err)
			}
			if errors.Is(err, ErrOriginalNotIssued) {
				t.Errorf("err = %v also matches ErrOriginalNotIssued", err)
			}
			f.wantCounts(t, before, "IssueNote on a note")
		})
	}
}

func TestIssueNote_RefusesEachStatusBeforeAccepted(t *testing.T) {
	f := newInFixture(t, "IN statuses")
	f.issueOK(t, "CN-IN-CTRL")
	for _, st := range []Status{StatusDraft, StatusValidated, StatusQueued, StatusSubmitted, StatusRejected, StatusFailed} {
		t.Run(string(st), func(t *testing.T) {
			id, lines := inOriginal(t, f.super, f.tenant, f.entity, "IN-ST-"+string(st), st, strPtr("IRN-ST-"+string(st)), inThreeLines())
			before := f.counts(t)
			_, err := f.store.IssueNote(f.ctx, id, f.req("CN-"+string(st), NoteLineInput{LineID: lines[0], Quantity: "1.000", UnitPrice: "1.00"}))
			if !errors.Is(err, ErrOriginalNotIssued) {
				t.Fatalf("IssueNote at %s with an IRN: err = %v, want ErrOriginalNotIssued", st, err)
			}
			if errors.Is(err, ErrOriginalIsANote) {
				t.Errorf("err = %v also matches ErrOriginalIsANote", err)
			}
			f.wantCounts(t, before, "IssueNote at "+string(st))
		})
	}
}

func TestIssueNote_RefusesAcceptedWithoutIRN(t *testing.T) {
	f := newInFixture(t, "IN no irn")
	f.issueOK(t, "CN-IN-CTRL")
	id, lines := inOriginal(t, f.super, f.tenant, f.entity, "IN-NOIRN", StatusAccepted, nil, inThreeLines())
	before := f.counts(t)
	_, err := f.store.IssueNote(f.ctx, id, f.req("CN-NOIRN", NoteLineInput{LineID: lines[0], Quantity: "1.000", UnitPrice: "1.00"}))
	if !errors.Is(err, ErrOriginalNotIssued) {
		t.Fatalf("IssueNote on accepted with NULL irn: err = %v, want ErrOriginalNotIssued", err)
	}
	f.wantCounts(t, before, "IssueNote on accepted without an IRN")
}

func TestIssueNote_OtherTenantAndOutsideClientsAreNotFound(t *testing.T) {
	f := newInFixture(t, "IN scope")
	entB := seedEntity(t, f.super, f.tenant, "IN client B")
	origB, linesB := inOriginal(t, f.super, f.tenant, entB, "IN-B-ORIG", StatusAccepted, strPtr("IRN-IN-B"), inThreeLines())
	limited := csMember(t, f.super, f.tenant, "reviewer", f.entity)

	otherTenant := seedTenant(t, f.super, "IN scope other tenant")
	otherEnt := seedEntity(t, f.super, otherTenant, "Other tenant client")
	_, otherLines := inOriginal(t, f.super, otherTenant, otherEnt, "IN-T2-ORIG", StatusAccepted, strPtr("IRN-IN-T2"), inThreeLines())
	otherOrig := mustString(t, f.super, `SELECT id::text FROM invoices WHERE tenant_id = $1`, otherTenant)

	// Controls: the same two originals issue for a caller who can see them.
	if _, err := f.store.IssueNote(f.ctx, origB, f.req("CN-B-CTRL", NoteLineInput{LineID: linesB[0], Quantity: "1.000", UnitPrice: "1.00"})); err != nil {
		t.Fatalf("control: all-clients caller on client B's original: %v", err)
	}
	if _, err := f.store.IssueNote(dtCtx(otherTenant), otherOrig, f.req("CN-T2-CTRL", NoteLineInput{LineID: otherLines[0], Quantity: "1.000", UnitPrice: "1.00"})); err != nil {
		t.Fatalf("control: the other tenant on its own original: %v", err)
	}

	before := f.counts(t)
	beforeOther := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, otherTenant)
	cases := []struct {
		name string
		ctx  context.Context
		id   string
		line string
	}{
		{"client-limited member names an original in client B", limited, origB, linesB[0]},
		{"tenant 1 names tenant 2's original", f.ctx, otherOrig, otherLines[0]},
		{"unknown id", f.ctx, uuid.NewString(), f.lineIDs[0]},
	}
	for _, c := range cases {
		_, err := f.store.IssueNote(c.ctx, c.id, f.req("CN-SCOPE", NoteLineInput{LineID: c.line, Quantity: "1.000", UnitPrice: "1.00"}))
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", c.name, err)
		}
	}
	f.wantCounts(t, before, "refused scope cases")
	if n := mustCount(t, f.super, `SELECT count(*) FROM invoices WHERE tenant_id = $1`, otherTenant); n != beforeOther {
		t.Errorf("tenant 2 invoices = %d, want %d", n, beforeOther)
	}
}

func mustString(t *testing.T, super *pgxpool.Pool, query string, args ...any) string {
	t.Helper()
	var s string
	if err := super.QueryRow(context.Background(), query, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return s
}

func TestIssueNote_RefusesBadInput(t *testing.T) {
	f := newInFixture(t, "IN bad input")
	f.issueOK(t, "CN-IN-CTRL") // a valid request passes, so each refusal below is its one changed clause
	good := NoteLineInput{LineID: f.lineIDs[0], Quantity: "1.000", UnitPrice: "100.00"}
	with := func(q, p string) NoteLineInput { return NoteLineInput{LineID: f.lineIDs[0], Quantity: q, UnitPrice: p} }

	cases := []struct {
		name   string
		mutate func(*NoteInput)
	}{
		{"type commercial_invoice", func(in *NoteInput) { in.DocumentType = submission.CommercialInvoice }},
		{"unknown type", func(in *NoteInput) { in.DocumentType = "invoice" }},
		{"blank number", func(in *NoteInput) { in.InvoiceNumber = "" }},
		{"whitespace number", func(in *NoteInput) { in.InvoiceNumber = "   " }},
		{"nil date", func(in *NoteInput) { in.IssueDate = nil }},
		{"nil lines", func(in *NoteInput) { in.Lines = nil }},
		{"empty lines", func(in *NoteInput) { in.Lines = []NoteLineInput{} }},
		{"repeated line id", func(in *NoteInput) { in.Lines = []NoteLineInput{good, good} }},
		{"quantity 0", func(in *NoteInput) { in.Lines = []NoteLineInput{with("0", "100.00")} }},
		{"quantity -1", func(in *NoteInput) { in.Lines = []NoteLineInput{with("-1", "100.00")} }},
		{"quantity 1.2345", func(in *NoteInput) { in.Lines = []NoteLineInput{with("1.2345", "100.00")} }},
		{"quantity 1e5", func(in *NoteInput) { in.Lines = []NoteLineInput{with("1e5", "100.00")} }},
		{"quantity 10^11", func(in *NoteInput) { in.Lines = []NoteLineInput{with("100000000000", "1.00")} }},
		{"quantity blank", func(in *NoteInput) { in.Lines = []NoteLineInput{with("", "100.00")} }},
		{"quantity text", func(in *NoteInput) { in.Lines = []NoteLineInput{with("abc", "100.00")} }},
		{"price -1", func(in *NoteInput) { in.Lines = []NoteLineInput{with("1.000", "-1")} }},
		{"price 1.234", func(in *NoteInput) { in.Lines = []NoteLineInput{with("1.000", "1.234")} }},
		{"price 10^12", func(in *NoteInput) { in.Lines = []NoteLineInput{with("1.000", "1000000000000")} }},
		{"price blank", func(in *NoteInput) { in.Lines = []NoteLineInput{with("1.000", "")} }},
		{"line total reaches 10^12", func(in *NoteInput) { in.Lines = []NoteLineInput{with("1000.000", "1000000000.00")} }},
		{"total reaches 10^12 through the VAT", func(in *NoteInput) { in.Lines = []NoteLineInput{with("1.000", "999999999999.00")} }},
	}
	before := f.counts(t)
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := f.req(fmt.Sprintf("CN-BAD-%d", i))
			c.mutate(&in)
			_, err := f.store.IssueNote(f.ctx, f.original, in)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("IssueNote: err = %v, want ErrValidation", err)
			}
			if errors.Is(err, ErrUnknownLineID) || errors.Is(err, ErrDuplicateNumber) {
				t.Errorf("err = %v, want only ErrValidation", err)
			}
			f.wantCounts(t, before, c.name)
		})
	}

	// Boundaries just inside the bounds are accepted.
	for i, in := range []NoteLineInput{with("0.001", "0.00"), with("99999999999.999", "0.00")} {
		if _, err := f.store.IssueNote(f.ctx, f.original, f.req(fmt.Sprintf("CN-EDGE-%d", i), in)); err != nil {
			t.Errorf("boundary line (%s x %s): err = %v, want created", in.Quantity, in.UnitPrice, err)
		}
	}
}

func TestIssueNote_RefusesALineOfAnotherInvoice(t *testing.T) {
	f := newInFixture(t, "IN foreign line")
	_, otherLines := inOriginal(t, f.super, f.tenant, f.entity, "IN-ORIG-2", StatusAccepted, strPtr("IRN-IN-2"), inThreeLines())
	f.issueOK(t, "CN-IN-CTRL")
	before := f.counts(t)

	for name, id := range map[string]string{"a line of a second original": otherLines[0], "a random uuid": uuid.NewString()} {
		_, err := f.store.IssueNote(f.ctx, f.original, f.req("CN-FOREIGN",
			NoteLineInput{LineID: f.lineIDs[0], Quantity: "1.000", UnitPrice: "1.00"},
			NoteLineInput{LineID: id, Quantity: "1.000", UnitPrice: "1.00"}))
		if !errors.Is(err, ErrUnknownLineID) {
			t.Errorf("%s: err = %v, want ErrUnknownLineID", name, err)
		}
		if errors.Is(err, ErrValidation) {
			t.Errorf("%s: err = %v also matches ErrValidation", name, err)
		}
	}
	f.wantCounts(t, before, "unknown line ids")
}

func TestIssueNote_DuplicateNumberWritesNothing(t *testing.T) {
	f := newInFixture(t, "IN duplicate")
	seedInvoice(t, f.super, f.tenant, f.entity, "CN-TAKEN")
	before := f.counts(t)

	_, err := f.store.IssueNote(f.ctx, f.original, f.req("CN-TAKEN"))
	if !errors.Is(err, ErrDuplicateNumber) {
		t.Fatalf("IssueNote with a taken number: err = %v, want ErrDuplicateNumber", err)
	}
	f.wantCounts(t, before, "duplicate number")

	f.issueOK(t, "CN-FREE") // the same request with a free number is created
}

func TestIssueNote_SameNumberInAnotherEntityIsAllowed(t *testing.T) {
	f := newInFixture(t, "IN other entity")
	entB := seedEntity(t, f.super, f.tenant, "IN client B")
	seedInvoice(t, f.super, f.tenant, entB, "CN-SHARED")

	inv, err := f.store.IssueNote(f.ctx, f.original, f.req("CN-SHARED"))
	if err != nil {
		t.Fatalf("IssueNote with a number used only in another entity: %v", err)
	}
	if inv.EntityID != f.entity || inv.InvoiceNumber != "CN-SHARED" {
		t.Errorf("note (entity, number) = (%s, %s), want (%s, CN-SHARED)", inv.EntityID, inv.InvoiceNumber, f.entity)
	}
}

func TestIssueNote_WritesOneHistoryRowAndOneAudit(t *testing.T) {
	f := newInFixture(t, "IN history")
	before := f.counts(t)
	inv := f.issueOK(t, "CN-IN-HIST")

	var n int
	var from *string
	var to string
	if err := f.super.QueryRow(context.Background(),
		`SELECT count(*) OVER (), from_status, to_status FROM invoice_status_history WHERE invoice_id = $1`, inv.ID).Scan(&n, &from, &to); err != nil {
		t.Fatalf("read history of the note: %v", err)
	}
	if n != 1 || from != nil || to != "draft" {
		t.Errorf("history = %d rows, (%v -> %s), want 1 row NULL -> draft", n, from, to)
	}
	if got := mustCount(t, f.super,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.created' AND payload->>'id' = $2`, f.tenant, inv.ID); got != 1 {
		t.Errorf("invoice.created entries naming %s = %d, want 1", inv.ID, got)
	}
	after := f.counts(t)
	want := inCounts{invoices: before.invoices + 1, lines: before.lines + 1, history: before.history + 1, audit: before.audit + 1}
	if after != want {
		t.Errorf("rows after one success = %+v, want %+v", after, want)
	}
}

func TestIssueNote_NoLineCapOnTheServer(t *testing.T) {
	f := newInFixture(t, "IN no cap")
	// Original line 1 is 2.000 at 100.00; the note takes 5.000 at 150.00.
	inv, err := f.store.IssueNote(f.ctx, f.original, f.req("CN-IN-OVER",
		NoteLineInput{LineID: f.lineIDs[0], Quantity: "5.000", UnitPrice: "150.00"}))
	if err != nil {
		t.Fatalf("IssueNote above the original's quantity and price: %v", err)
	}
	lines := inStoredLines(t, f.super, inv.ID)
	if len(lines) != 1 || lines[0].qty != "5.000" || lines[0].price != "150.00" {
		t.Errorf("stored lines = %+v, want one line of 5.000 at 150.00", lines)
	}
	if got := inStr(inCol(t, f.super, inv.ID, "subtotal")); got != "750.00" {
		t.Errorf("subtotal = %s, want 750.00", got)
	}
}
