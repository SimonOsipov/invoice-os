package archive

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xuri/excelize/v2"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

const registerEvent = "invoice_register.exported" // audit event in the invoice_register.exported writer

type registerRig struct {
	tx     pgx.Tx
	tenant string
	entity string
}

// newRegisterRig plants a tenant and one entity on the rollback-wrapped superuser tx.
// Call act() after the last fixture insert.
func newRegisterRig(t *testing.T) *registerRig {
	t.Helper()
	tx := beginFixtureTx(t, dbSuperPool(t))
	tenant := mustCreateTenant(t, tx, "archive-register-"+uuid.NewString())
	return &registerRig{tx: tx, tenant: tenant, entity: mustCreateEntity(t, tx, tenant, "Register Co", "")}
}

func (g *registerRig) invoice(t *testing.T, number, status, issueDate string) string {
	t.Helper()
	return mustCreateInvoice(t, g.tx, invoiceFixture{tenantID: g.tenant, entityID: g.entity, invoiceNumber: number, status: status, issueDate: issueDate})
}

func (g *registerRig) act(t *testing.T) { t.Helper(); actingAs(t, g.tx, g.tenant) }

func (g *registerRig) build(t *testing.T, from, to string, maxInvoices int) (*excelize.File, string, error) {
	t.Helper()
	return buildRegister(context.Background(), g.tx, registerRequest(from, to).withEntity(g.entity), registerOpts{subject: "subject-1", maxInvoices: maxInvoices})
}

func (r Request) withEntity(id string) Request { r.EntityID = id; return r }

// sheetRows writes f to bytes and reads the register sheet back, as a client would.
func sheetRows(t *testing.T, f *excelize.File) [][]string {
	t.Helper()
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write workbook: %v", err)
	}
	back, err := excelize.OpenReader(&buf)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer back.Close()
	if sheets := back.GetSheetList(); len(sheets) != 1 || sheets[0] != "Invoice register" {
		t.Fatalf("sheets = %v, want [Invoice register]", sheets)
	}
	rows, err := back.GetRows("Invoice register")
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}
	// GetRows drops trailing empty cells; pad so rows compare cell for cell.
	for i, r := range rows {
		for len(r) < len(invoicesCSVHeader) {
			r = append(r, "")
		}
		rows[i] = r
	}
	return rows
}

func mustBuild(t *testing.T, g *registerRig, from, to string, maxInvoices int) [][]string {
	t.Helper()
	f, _, err := g.build(t, from, to, maxInvoices)
	if err != nil {
		t.Fatalf("buildRegister: %v", err)
	}
	defer f.Close()
	return sheetRows(t, f)
}

// numbers returns the invoice_number column of the data rows.
func numbers(t *testing.T, rows [][]string) []string {
	t.Helper()
	var out []string
	for _, r := range rows[1:] {
		out = append(out, r[headerIndex(t, "invoice_number")])
	}
	return out
}

func wantNumbers(t *testing.T, rows [][]string, want ...string) {
	t.Helper()
	got := numbers(t, rows)
	if !reflect.DeepEqual(got, append([]string(nil), want...)) && !(len(got) == 0 && len(want) == 0) {
		t.Errorf("invoice numbers = %v, want %v", got, want)
	}
}

// registerAuditRows counts export rows of the given tenants, as the superuser.
func registerAuditRows(t *testing.T, tx pgx.Tx, tenants ...string) int {
	t.Helper()
	if _, err := tx.Exec(context.Background(), `RESET ROLE`); err != nil {
		t.Fatalf("RESET ROLE: %v", err)
	}
	var n int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE event = $1 AND tenant_id = ANY($2::uuid[])`, registerEvent, tenants).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

func TestRegister_IssueDateBoundsAreInclusive(t *testing.T) {
	g := newRegisterRig(t)
	g.invoice(t, "INV-BEFORE", "validated", "2026-08-31")
	g.invoice(t, "INV-FROM", "validated", "2026-09-01")
	g.invoice(t, "INV-TO", "validated", "2026-09-30")
	g.invoice(t, "INV-AFTER", "validated", "2026-10-01")
	g.act(t)
	wantNumbers(t, mustBuild(t, g, "2026-09-01", "2026-09-30", 10), "INV-FROM", "INV-TO")
}

func TestRegister_FiltersOnIssueDateNotCreatedAt(t *testing.T) {
	g := newRegisterRig(t)
	inPeriod := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	mustCreateInvoice(t, g.tx, invoiceFixture{tenantID: g.tenant, entityID: g.entity, invoiceNumber: "INV-A", status: "validated", issueDate: "2026-09-15", createdAt: inPeriod.AddDate(-1, 0, 0)})
	mustCreateInvoice(t, g.tx, invoiceFixture{tenantID: g.tenant, entityID: g.entity, invoiceNumber: "INV-B", status: "validated", issueDate: "2026-07-15", createdAt: inPeriod})
	g.act(t)
	wantNumbers(t, mustBuild(t, g, "2026-09-01", "2026-09-30", 10), "INV-A")
}

func TestRegister_NullIssueDateIsNeverInThePeriod(t *testing.T) {
	g := newRegisterRig(t)
	g.invoice(t, "INV-NULL", "validated", "")
	g.invoice(t, "INV-CONTROL", "validated", "2026-09-10")
	g.act(t)
	wantNumbers(t, mustBuild(t, g, "2026-09-01", "2026-09-30", 10), "INV-CONTROL")
}

func TestRegister_SingleDayPeriod(t *testing.T) {
	g := newRegisterRig(t)
	g.invoice(t, "INV-D-1", "validated", "2026-09-09")
	g.invoice(t, "INV-D", "validated", "2026-09-10")
	g.invoice(t, "INV-D+1", "validated", "2026-09-11")
	g.act(t)
	wantNumbers(t, mustBuild(t, g, "2026-09-10", "2026-09-10", 10), "INV-D")
}

func TestRegister_OtherClientOfTheSameTenantIsExcluded(t *testing.T) {
	g := newRegisterRig(t)
	other := mustCreateEntity(t, g.tx, g.tenant, "Other Co", "")
	g.invoice(t, "INV-E1", "validated", "2026-09-10")
	mustCreateInvoice(t, g.tx, invoiceFixture{tenantID: g.tenant, entityID: other, invoiceNumber: "INV-E2", status: "validated", issueDate: "2026-09-10"})
	g.act(t)
	wantNumbers(t, mustBuild(t, g, "2026-09-01", "2026-09-30", 10), "INV-E1")
}

func TestRegister_ExcludesDraftsAndKeepsEveryOtherStatus(t *testing.T) {
	g := newRegisterRig(t)
	states := []string{"draft", "validated", "queued", "submitted", "accepted", "rejected", "failed"}
	for i, s := range states {
		g.invoice(t, fmt.Sprintf("INV-%d-%s", i, s), s, "2026-09-10")
	}
	g.act(t)
	rows := mustBuild(t, g, "2026-09-01", "2026-09-30", 10)
	var got []string
	for _, r := range rows[1:] {
		got = append(got, r[headerIndex(t, "status")])
	}
	if want := states[1:]; !reflect.DeepEqual(got, want) {
		t.Errorf("status cells = %v, want %v", got, want)
	}
}

func TestRegister_HeaderRowIsTheInvoicesCSVHeader(t *testing.T) {
	g := newRegisterRig(t)
	g.act(t)
	rows := mustBuild(t, g, "2026-09-01", "2026-09-30", 10)
	if len(rows) != 1 || !reflect.DeepEqual(rows[0], invoicesCSVHeader) {
		t.Errorf("rows = %v, want the header row alone equal to invoicesCSVHeader", rows)
	}
	if n := len(rows[0]); rows[0][n-2] != "document_type" || rows[0][n-1] != "original_invoice_number" {
		t.Errorf("header ends %v, want document_type, original_invoice_number", rows[0][n-2:])
	}
}

func TestRegister_RowCellsEqualTheBundleCSVCells(t *testing.T) {
	g := newRegisterRig(t)
	full := mustCreateInvoice(t, g.tx, invoiceFixture{tenantID: g.tenant, entityID: g.entity, invoiceNumber: "INV-FULL", status: "accepted", issueDate: "2026-09-10", createdAt: time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)})
	if _, err := g.tx.Exec(context.Background(), `
		UPDATE invoices SET currency='NGN', subtotal=100.50, vat=7.54, total=108.04,
		  supplier_tin='S-TIN', supplier_name='Sup, "Ltd"', buyer_tin='B-TIN', buyer_name='Buyer',
		  irn='IRN-1', csid='CSID-1', qr_payload='QR==',
		  rejection_reasons='[{"code": "E1", "message": "m"}]',
		  invoice_kind='B2B', tax_currency_code='NGN', payment_status='PENDING',
		  due_date='2026-09-20', tax_point_date='2026-09-02', issue_time='10:11:12',
		  supplier_email='s@x.test', supplier_telephone='1', supplier_street='1 Main', supplier_city='Lagos',
		  supplier_postal_zone='100001', supplier_country='NG', supplier_state='LA', supplier_lga='Ikeja',
		  buyer_email='b@x.test', buyer_telephone='2', buyer_street='2 Main', buyer_city='Abuja',
		  buyer_postal_zone='900001', buyer_country='NG', buyer_state='FC', buyer_lga='AMAC'
		WHERE id = $1`, full); err != nil {
		t.Fatalf("fill nullable columns: %v", err)
	}
	mustCreateInvoice(t, g.tx, invoiceFixture{tenantID: g.tenant, entityID: g.entity, invoiceNumber: "INV-NULLS", status: "validated", issueDate: "2026-09-11", createdAt: time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)})
	credit := mustCreateInvoice(t, g.tx, invoiceFixture{tenantID: g.tenant, entityID: g.entity, invoiceNumber: "CN-FULL", status: "accepted", issueDate: "2026-09-12", createdAt: time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)})
	if _, err := g.tx.Exec(context.Background(),
		`UPDATE invoices SET document_type='credit_note', original_invoice_id=$2, subtotal=300.00, vat=22.50, total=322.50 WHERE id=$1`, credit, full); err != nil {
		t.Fatalf("make the credit note: %v", err)
	}
	g.act(t)

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	req := Request{EntityID: g.entity, From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)}
	if _, err := selectInvoices(context.Background(), g.tx, req, w); err != nil {
		t.Fatalf("selectInvoices: %v", err)
	}
	w.Flush()
	csvRows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	regRows := mustBuild(t, g, "2026-09-01", "2026-09-30", 10)
	if len(csvRows) != 4 || len(regRows) != 4 {
		t.Fatalf("csv rows %d, register rows %d, want 4 each", len(csvRows), len(regRows))
	}
	byID := map[string][]string{}
	for _, r := range csvRows[1:] {
		byID[r[0]] = r
	}
	for _, r := range regRows[1:] {
		if want := byID[r[0]]; !reflect.DeepEqual(r, want) {
			t.Errorf("register row %v != csv row %v", r, want)
		}
	}
	if r := byID[credit]; r[headerIndex(t, "document_type")] != "credit_note" || r[headerIndex(t, "original_invoice_number")] != "INV-FULL" {
		t.Errorf("credit note cells = %v, want credit_note and INV-FULL", r)
	}
}

func TestRegister_SortsByIssueDateThenNumber(t *testing.T) {
	g := newRegisterRig(t)
	g.invoice(t, "INV-C", "validated", "2026-09-20")
	for _, n := range []string{"INV-F", "INV-E", "INV-D", "INV-B", "INV-A", "INV-G"} {
		g.invoice(t, n, "validated", "2026-09-10")
	}
	g.invoice(t, "INV-Z", "validated", "2026-09-05")
	g.act(t)
	wantNumbers(t, mustBuild(t, g, "2026-09-01", "2026-09-30", 10), "INV-Z", "INV-A", "INV-B", "INV-D", "INV-E", "INV-F", "INV-G", "INV-C")
}

func TestRegister_OverTheCapRefusesBeforeAnyByte(t *testing.T) {
	g := newRegisterRig(t)
	for i := range 3 {
		g.invoice(t, fmt.Sprintf("INV-CAP-%d", i), "validated", "2026-09-10")
	}
	g.act(t)
	f, _, err := g.build(t, "2026-09-01", "2026-09-30", 2)
	var tooMany *TooManyInvoicesError
	if !errors.As(err, &tooMany) || tooMany.Count != 3 || tooMany.Limit != 2 {
		t.Fatalf("buildRegister error = %v, want *TooManyInvoicesError{3, 2}", err)
	}
	if f != nil {
		t.Error("buildRegister returned a file with the error")
	}
	if n := registerAuditRows(t, g.tx, g.tenant); n != 0 {
		t.Errorf("%d audit rows after a refused register, want 0", n)
	}
}

func TestRegister_ExactlyTheCapSucceeds(t *testing.T) {
	g := newRegisterRig(t)
	g.invoice(t, "INV-1", "validated", "2026-09-10")
	g.invoice(t, "INV-2", "validated", "2026-09-11")
	g.act(t)
	wantNumbers(t, mustBuild(t, g, "2026-09-01", "2026-09-30", 2), "INV-1", "INV-2")
}

func TestRegister_DraftsDoNotCountTowardTheCap(t *testing.T) {
	g := newRegisterRig(t)
	g.invoice(t, "INV-1", "validated", "2026-09-10")
	g.invoice(t, "INV-2", "validated", "2026-09-11")
	g.invoice(t, "INV-DRAFT", "draft", "2026-09-12")
	g.act(t)
	wantNumbers(t, mustBuild(t, g, "2026-09-01", "2026-09-30", 2), "INV-1", "INV-2")
}

func TestRegister_NonPositiveCapIsAnError(t *testing.T) {
	g := newRegisterRig(t)
	g.act(t)
	f, _, err := g.build(t, "2026-09-01", "2026-09-30", 0)
	if err == nil || f != nil {
		t.Errorf("buildRegister(maxInvoices 0) = %v, %v, want an error and no file", f, err)
	}
}

func TestRegister_ForeignTenantClientIsNotFound(t *testing.T) {
	g := newRegisterRig(t)
	tenantB := mustCreateTenant(t, g.tx, "archive-register-foreign-"+uuid.NewString())
	foreign := mustCreateEntity(t, g.tx, tenantB, "Foreign Co", "")
	mustCreateInvoice(t, g.tx, invoiceFixture{tenantID: tenantB, entityID: foreign, invoiceNumber: "INV-FOREIGN", status: "validated", issueDate: "2026-09-10"})
	g.act(t)
	f, _, err := buildRegister(context.Background(), g.tx, registerRequest("2026-09-01", "2026-09-30").withEntity(foreign), registerOpts{subject: "subject-1", maxInvoices: 10})
	if !errors.Is(err, ErrEntityNotFound) || f != nil {
		t.Fatalf("buildRegister(foreign client) = %v, %v, want ErrEntityNotFound", f, err)
	}
	if n := registerAuditRows(t, g.tx, g.tenant, tenantB); n != 0 {
		t.Errorf("%d audit rows after a foreign-client attempt, want 0", n)
	}
}

func TestRegister_UnknownClientIsNotFound(t *testing.T) {
	g := newRegisterRig(t)
	g.act(t)
	_, _, err := buildRegister(context.Background(), g.tx, registerRequest("2026-09-01", "2026-09-30").withEntity(uuid.NewString()), registerOpts{subject: "subject-1", maxInvoices: 10})
	if !errors.Is(err, ErrEntityNotFound) {
		t.Errorf("buildRegister(unknown client) error = %v, want ErrEntityNotFound", err)
	}
}

type auditRow struct {
	actor, payload string
	entityID       *string
}

func readRegisterAudit(t *testing.T, tx pgx.Tx) []auditRow {
	t.Helper()
	rows, err := tx.Query(context.Background(), `SELECT actor, payload::text, entity_id::text FROM audit_log WHERE event = $1`, registerEvent)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var a auditRow
		if err := rows.Scan(&a.actor, &a.payload, &a.entityID); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func assertRegisterAudit(t *testing.T, got []auditRow, entity string, rowCount int) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("%d %s rows, want exactly 1", len(got), registerEvent)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(got[0].payload), &p); err != nil {
		t.Fatalf("payload %q: %v", got[0].payload, err)
	}
	want := map[string]any{"entity_id": entity, "from": "2026-09-01", "to": "2026-09-30", "row_count": float64(rowCount)}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("payload = %v, want %v", p, want)
	}
	if got[0].actor != "subject-1" {
		t.Errorf("actor = %q, want subject-1", got[0].actor)
	}
	if got[0].entityID == nil || *got[0].entityID != entity {
		t.Errorf("audit_log.entity_id = %v, want %s", got[0].entityID, entity)
	}
}

func TestRegister_WritesOneAuditRowWithTheClientPeriodAndCount(t *testing.T) {
	g := newRegisterRig(t)
	g.invoice(t, "INV-1", "validated", "2026-09-10")
	g.invoice(t, "INV-2", "accepted", "2026-09-11")
	g.act(t)
	mustBuild(t, g, "2026-09-01", "2026-09-30", 10)
	assertRegisterAudit(t, readRegisterAudit(t, g.tx), g.entity, 2)
}

func TestRegister_EmptyPeriodIsAHeaderOnlyFileAndStillAudited(t *testing.T) {
	g := newRegisterRig(t)
	g.act(t)
	rows := mustBuild(t, g, "2026-09-01", "2026-09-30", 10)
	if len(rows) != 1 {
		t.Errorf("%d rows, want the header only", len(rows))
	}
	assertRegisterAudit(t, readRegisterAudit(t, g.tx), g.entity, 0)
}

func TestRegister_TooLongCellRefusesBeforeAnyByte(t *testing.T) {
	super := dbSuperPool(t)
	app, _ := dbAppPoolTraced(t)
	var entityID string
	tenantID := mustCommitFixture(t, super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-register-toolong-"+uuid.NewString())
		entityID = mustCreateEntity(t, tx, tid, "Too Long Co", "")
		inv := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tid, entityID: entityID, invoiceNumber: "INV-LONG", status: "validated", issueDate: "2026-09-10"})
		if _, err := tx.Exec(context.Background(), `UPDATE invoices SET supplier_street = $2 WHERE id = $1`, inv, strings.Repeat("s", 40000)); err != nil {
			t.Fatalf("set long street: %v", err)
		}
		return tid
	})

	s := NewStore(app)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: tenantID})
	var cw countingWriter
	started := false
	err := s.Register(ctx, registerRequest("2026-09-01", "2026-09-30").withEntity(entityID), &cw, func(string) { started = true })

	var tooLong *CellTooLongError
	if !errors.As(err, &tooLong) || tooLong.InvoiceNumber != "INV-LONG" || tooLong.Column != "supplier_street" {
		t.Fatalf("Register error = %v, want *CellTooLongError{INV-LONG, supplier_street}", err)
	}
	if started || cw.n != 0 {
		t.Errorf("onStart called = %v, bytes written = %d, want neither", started, cw.n)
	}
	assertNoCommittedRegisterAudit(t, super, tenantID)
}

func assertNoCommittedRegisterAudit(t *testing.T, super interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenantID string) {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`, tenantID, registerEvent).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 0 {
		t.Errorf("%d audit rows, want 0", n)
	}
}

func TestRegister_StoreOverTheCapRefusesBeforeAnyByte(t *testing.T) {
	super := dbSuperPool(t)
	app, _ := dbAppPoolTraced(t)
	var entityID string
	tenantID := mustCommitFixture(t, super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-register-cap-"+uuid.NewString())
		entityID = mustCreateEntity(t, tx, tid, "Cap Co", "")
		for i := range 3 {
			mustCreateInvoice(t, tx, invoiceFixture{tenantID: tid, entityID: entityID, invoiceNumber: fmt.Sprintf("INV-SC-%d", i), status: "validated", issueDate: "2026-09-10"})
		}
		return tid
	})
	s := &Store{pool: app, maxInvoices: 2}
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: tenantID})
	var cw countingWriter
	started := false
	err := s.Register(ctx, registerRequest("2026-09-01", "2026-09-30").withEntity(entityID), &cw, func(string) { started = true })
	var tooMany *TooManyInvoicesError
	if !errors.As(err, &tooMany) || tooMany.Count != 3 || tooMany.Limit != 2 {
		t.Fatalf("Register error = %v, want *TooManyInvoicesError{3, 2}", err)
	}
	if started || cw.n != 0 {
		t.Errorf("onStart called = %v, bytes written = %d, want neither", started, cw.n)
	}
	assertNoCommittedRegisterAudit(t, super, tenantID)
}

// commitSink records, at its first write, how many COMMITs the pool has traced.
type commitSink struct {
	rec     *sqlRecorder
	commits int
	bytes   int
}

func (c *commitSink) Write(p []byte) (int, error) {
	if c.bytes == 0 {
		c.commits = len(c.rec.mentioning("commit"))
	}
	c.bytes += len(p)
	return len(p), nil
}

func TestStoreRegister_CommitsBeforeTheFirstByte(t *testing.T) {
	super := dbSuperPool(t)
	app, rec := dbAppPoolTraced(t)
	var entityID string
	tenantID := mustCommitFixture(t, super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-register-commit-"+uuid.NewString())
		entityID = mustCreateEntity(t, tx, tid, "Commit Co", "")
		mustCreateInvoice(t, tx, invoiceFixture{tenantID: tid, entityID: entityID, invoiceNumber: "INV-COMMIT", status: "validated", issueDate: "2026-09-10"})
		return tid
	})
	s := NewStore(app)
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: tenantID})
	rec.reset()
	sink := &commitSink{rec: rec}
	startCommits := -1
	var name string
	err := s.Register(ctx, registerRequest("2026-09-01", "2026-09-30").withEntity(entityID), sink, func(n string) {
		name = n
		if startCommits == -1 {
			startCommits = len(rec.mentioning("commit"))
		}
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if startCommits != 1 {
		t.Errorf("COMMITs traced when onStart fired = %d, want 1", startCommits)
	}
	if sink.commits != 1 || sink.bytes == 0 {
		t.Errorf("COMMITs traced at the first byte = %d (bytes %d), want 1 and some bytes", sink.commits, sink.bytes)
	}
	if want := "ASComply_invoice-register_Commit-Co_20260901_20260930.xlsx"; name != want {
		t.Errorf("filename = %q, want %q", name, want)
	}
	if begins := rec.mentioning("begin"); len(begins) != 1 || begins[0] != "begin isolation level repeatable read" {
		t.Errorf("begin statements = %q, want one REPEATABLE READ read-write", begins)
	}

	var n int
	if err := super.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2 AND entity_id = $3`, tenantID, registerEvent, entityID).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 1 {
		t.Errorf("%d committed audit rows visible to a new connection, want 1", n)
	}
}

func TestStoreRegister_NoIdentityIsErrNoTenant(t *testing.T) {
	app, rec := dbAppPoolTraced(t)
	rec.reset()
	err := NewStore(app).Register(context.Background(), registerRequest("2026-09-01", "2026-09-30"), io.Discard, nil)
	if !errors.Is(err, db.ErrNoTenant) {
		t.Errorf("Register without identity error = %v, want db.ErrNoTenant", err)
	}
	if got := rec.statements(); len(got) != 0 {
		t.Errorf("statements traced = %q, want none", got)
	}
}

func TestRegister_UppercaseClientIDIsAuditedCanonically(t *testing.T) {
	g := newRegisterRig(t)
	g.invoice(t, "INV-1", "validated", "2026-09-10")
	g.act(t)
	f, _, err := buildRegister(context.Background(), g.tx, registerRequest("2026-09-01", "2026-09-30").withEntity(strings.ToUpper(g.entity)), registerOpts{subject: "subject-1", maxInvoices: 10})
	if err != nil {
		t.Fatalf("buildRegister: %v", err)
	}
	defer f.Close()
	assertRegisterAudit(t, readRegisterAudit(t, g.tx), g.entity, 1)
}

func TestStoreRegister_ForeignTenantClientIsNotFoundAndLeavesNoAuditRow(t *testing.T) {
	super := dbSuperPool(t)
	app, _ := dbAppPoolTraced(t)
	var foreign string
	tenantA := mustCommitFixture(t, super, func(tx pgx.Tx) string {
		return mustCreateTenant(t, tx, "archive-register-xa-"+uuid.NewString())
	})
	tenantB := mustCommitFixture(t, super, func(tx pgx.Tx) string {
		tid := mustCreateTenant(t, tx, "archive-register-xb-"+uuid.NewString())
		foreign = mustCreateEntity(t, tx, tid, "Foreign Co", "")
		mustCreateInvoice(t, tx, invoiceFixture{tenantID: tid, entityID: foreign, invoiceNumber: "INV-F", status: "validated", issueDate: "2026-09-10"})
		return tid
	})
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "system", TenantID: tenantA})
	var cw countingWriter
	started := false
	err := NewStore(app).Register(ctx, registerRequest("2026-09-01", "2026-09-30").withEntity(foreign), &cw, func(string) { started = true })
	if !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("Register error = %v, want ErrEntityNotFound", err)
	}
	if started || cw.n != 0 {
		t.Errorf("onStart called = %v, bytes = %d, want neither", started, cw.n)
	}
	assertNoCommittedRegisterAudit(t, super, tenantA)
	assertNoCommittedRegisterAudit(t, super, tenantB)
}
