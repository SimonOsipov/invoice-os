// invoices_db_test.go: RED specs for AUDIT-05-03 (Mode A) -- selectInvoices and
// countInvoices against a real Postgres. Reuses entity_db_test.go's rollback-wrapped
// harness (dbSuperPool/beginFixtureTx/actingAs/mustCreateTenant).
package archive

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// mustCreateEntity inserts as superuser (bypasses RLS) -- same rationale as
// mustCreateTenant.
func mustCreateEntity(t *testing.T, tx pgx.Tx, tenantID, name, tin string) string {
	t.Helper()
	id := uuid.NewString()
	var tinArg any
	if tin != "" {
		tinArg = tin
	}
	if _, err := tx.Exec(context.Background(),
		`INSERT INTO business_entities (id, tenant_id, name, tin) VALUES ($1, $2, $3, $4)`,
		id, tenantID, name, tinArg); err != nil {
		t.Fatalf("insert business_entities fixture: %v", err)
	}
	return id
}

// invoiceFixture is mustCreateInvoice's input. Zero-value fields take the column
// default: id (random), status ("draft"), createdAt (now), rejectionReasons ("[]"),
// irn/csid/qrPayload (NULL).
type invoiceFixture struct {
	id                                string
	tenantID, entityID, invoiceNumber string
	status                            string
	createdAt                         time.Time
	irn, csid, qrPayload              *string
	rejectionReasons                  string
}

func mustCreateInvoice(t *testing.T, tx pgx.Tx, f invoiceFixture) string {
	t.Helper()
	id := f.id
	if id == "" {
		id = uuid.NewString()
	}
	status := f.status
	if status == "" {
		status = "draft"
	}
	createdAt := f.createdAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	rejectionReasons := f.rejectionReasons
	if rejectionReasons == "" {
		rejectionReasons = "[]"
	}
	_, err := tx.Exec(context.Background(), `
		INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, status, created_at, irn, csid, qr_payload, rejection_reasons)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`,
		id, f.tenantID, f.entityID, f.invoiceNumber, status, createdAt, f.irn, f.csid, f.qrPayload, rejectionReasons)
	if err != nil {
		t.Fatalf("insert invoices fixture: %v", err)
	}
	return id
}

func headerIndex(t *testing.T, column string) int {
	t.Helper()
	for i, h := range invoicesCSVHeader {
		if h == column {
			return i
		}
	}
	t.Fatalf("invoicesCSVHeader has no column %q", column)
	return -1
}

func findCSVRow(t *testing.T, raw []byte, invoiceID string) []string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("csv has %d rows, want a header plus at least one data row", len(rows))
	}
	idIdx := headerIndex(t, "invoice_id")
	for _, row := range rows[1:] {
		if row[idIdx] == invoiceID {
			return row
		}
	}
	t.Fatalf("csv has no row for invoice %s", invoiceID)
	return nil
}

func TestSelectInvoices_ExcludesAnotherEntityInTheSameTenant(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-invoices-cross-entity")
	entityA := mustCreateEntity(t, tx, tenant, "Entity A", "20000001-0001")
	entityB := mustCreateEntity(t, tx, tenant, "Entity B", "20000001-0002")

	from := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	mid := from.Add(12 * time.Hour)

	idA1 := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entityA, invoiceNumber: "INV-A-01", createdAt: mid})
	idA2 := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entityA, invoiceNumber: "INV-A-02", createdAt: mid.Add(time.Hour)})
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entityB, invoiceNumber: "INV-B-01", createdAt: mid})

	// Control needle (superuser, pre-actingAs): the fixture really planted 3 rows.
	var total int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM invoices WHERE tenant_id = $1`, tenant).Scan(&total); err != nil {
		t.Fatalf("control-needle count: %v", err)
	}
	if total != 3 {
		t.Fatalf("control needle: planted %d invoices, want 3 -- fixture setup is broken", total)
	}

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	ids, err := selectInvoices(context.Background(), tx, Request{EntityID: entityA, From: from, To: to}, w)
	if err != nil {
		t.Fatalf("selectInvoices: unexpected error: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("selectInvoices returned no ids -- want entity A's two invoices")
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if len(got) != 2 || !got[idA1] || !got[idA2] {
		t.Errorf("selectInvoices(entity A) ids = %v, want exactly {%s, %s}", ids, idA1, idA2)
	}
}

func TestSelectInvoices_ExcludesOutsideThePeriod(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-invoices-period")
	entity := mustCreateEntity(t, tx, tenant, "Period Co", "20000002-0001")

	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC)

	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-BEFORE", createdAt: from.Add(-time.Second)})
	idFrom := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-AT-FROM", createdAt: from})
	idTo := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-AT-TO", createdAt: to})
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-AFTER", createdAt: to.Add(time.Second)})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	ids, err := selectInvoices(context.Background(), tx, Request{EntityID: entity, From: from, To: to}, w)
	if err != nil {
		t.Fatalf("selectInvoices: unexpected error: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("selectInvoices returned no ids -- want the two boundary invoices (from, to inclusive)")
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if len(got) != 2 || !got[idFrom] || !got[idTo] {
		t.Errorf("selectInvoices(period) ids = %v, want exactly {%s, %s} (from/to inclusive, D-4)", ids, idFrom, idTo)
	}
}

func TestSelectInvoices_OrdersByCreatedAtThenID(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-invoices-order")
	entity := mustCreateEntity(t, tx, tenant, "Order Co", "20000003-0001")

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	tie := from.Add(time.Hour)
	later := tie.Add(time.Hour)

	// Two invoices share the exact same created_at; explicit ids pin the id tie-break
	// independent of insertion order (inserted high-id first, on purpose).
	const idHigh = "00000000-0000-0000-0000-000000000099"
	const idLow = "00000000-0000-0000-0000-000000000001"
	const idLater = "00000000-0000-0000-0000-000000000050"

	mustCreateInvoice(t, tx, invoiceFixture{id: idHigh, tenantID: tenant, entityID: entity, invoiceNumber: "INV-TIE-HIGH", createdAt: tie})
	mustCreateInvoice(t, tx, invoiceFixture{id: idLow, tenantID: tenant, entityID: entity, invoiceNumber: "INV-TIE-LOW", createdAt: tie})
	mustCreateInvoice(t, tx, invoiceFixture{id: idLater, tenantID: tenant, entityID: entity, invoiceNumber: "INV-LATER", createdAt: later})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	ids, err := selectInvoices(context.Background(), tx, Request{EntityID: entity, From: from, To: later}, w)
	if err != nil {
		t.Fatalf("selectInvoices: unexpected error: %v", err)
	}
	want := []string{idLow, idHigh, idLater}
	if len(ids) != len(want) {
		t.Fatalf("selectInvoices order ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("selectInvoices order ids = %v, want %v (created_at, id)", ids, want)
			break
		}
	}
}

func TestRLS_SelectInvoicesCannotReachAnotherTenantsEntity(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenantA := mustCreateTenant(t, tx, "archive-invoices-rls-a")
	tenantB := mustCreateTenant(t, tx, "archive-invoices-rls-b")
	entityA := mustCreateEntity(t, tx, tenantA, "RLS Target Co", "20000004-0001")

	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenantA, entityID: entityA, invoiceNumber: "INV-RLS-01", createdAt: from.Add(time.Hour)})
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenantA, entityID: entityA, invoiceNumber: "INV-RLS-02", createdAt: from.Add(2 * time.Hour)})

	// Control needle (superuser, pre-actingAs): the fixture really planted 2 rows.
	var planted int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM invoices WHERE entity_id = $1`, entityA).Scan(&planted); err != nil {
		t.Fatalf("control-needle count: %v", err)
	}
	if planted != 2 {
		t.Fatalf("control needle: planted %d invoices for entityA, want 2 -- fixture setup is broken", planted)
	}

	actingAs(t, tx, tenantB)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	ids, err := selectInvoices(context.Background(), tx, Request{EntityID: entityA, From: from, To: to}, w)
	if err != nil {
		t.Errorf("selectInvoices(another tenant's entity) error = %v, want nil (AC-4: no error, just no rows)", err)
	}
	if len(ids) != 0 {
		t.Errorf("selectInvoices(another tenant's entity) ids = %v, want none", ids)
	}
}

func TestSelectInvoices_AcceptedCarriesStoredIRNAndQR(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-invoices-accepted")
	entity := mustCreateEntity(t, tx, tenant, "Accepted Co", "20000005-0001")

	wantIRN := "IRN-2026-000123"
	wantCSID := "CSID-ABCDEF-0001"
	wantQR := "QR-PAYLOAD-BASE64XYZ=="
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	invID := mustCreateInvoice(t, tx, invoiceFixture{
		tenantID: tenant, entityID: entity, invoiceNumber: "INV-ACCEPTED-01", status: "accepted",
		createdAt: from.Add(time.Hour), irn: &wantIRN, csid: &wantCSID, qrPayload: &wantQR,
	})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	ids, err := selectInvoices(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(24 * time.Hour)}, w)
	if err != nil {
		t.Fatalf("selectInvoices: unexpected error: %v", err)
	}
	w.Flush()
	if len(ids) == 0 {
		t.Fatal("selectInvoices returned no ids -- want the accepted invoice")
	}
	record := findCSVRow(t, buf.Bytes(), invID)
	irnIdx, csidIdx, qrIdx := headerIndex(t, "irn"), headerIndex(t, "csid"), headerIndex(t, "qr_payload")
	if record[irnIdx] != wantIRN {
		t.Errorf("csv irn = %q, want %q byte for byte", record[irnIdx], wantIRN)
	}
	if record[csidIdx] != wantCSID {
		t.Errorf("csv csid = %q, want %q byte for byte", record[csidIdx], wantCSID)
	}
	if record[qrIdx] != wantQR {
		t.Errorf("csv qr_payload = %q, want %q byte for byte", record[qrIdx], wantQR)
	}
}

func TestSelectInvoices_NoFiscalOutcomeWritesEmptyNotQuotedEmpty(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-invoices-no-outcome")
	entity := mustCreateEntity(t, tx, tenant, "No Outcome Co", "20000006-0001")

	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	// irn/csid/qrPayload left nil -> NULL columns.
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-DRAFT-01", createdAt: from.Add(time.Hour)})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_, err := selectInvoices(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(24 * time.Hour)}, w)
	if err != nil {
		t.Fatalf("selectInvoices: unexpected error: %v", err)
	}
	w.Flush()

	raw := buf.String()
	if strings.Contains(raw, "\"\"") {
		t.Errorf("csv contains a quoted empty string in %q -- D-8 wants a bare empty cell, never \"\"", raw)
	}
	record := findCSVRow(t, buf.Bytes(), invID)
	for _, col := range []string{"irn", "csid", "qr_payload"} {
		if got := record[headerIndex(t, col)]; got != "" {
			t.Errorf("csv %s = %q, want empty (D-8: NULL -> empty cell)", col, got)
		}
	}
}

// AC-7: the schema's own guard, not new code -- confirms the three CHECKs are still
// in place so an empty cell can only ever mean NULL, never a stored "".
func TestInvoices_FiscalColumnsRejectTheEmptyString(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-invoices-check")
	entity := mustCreateEntity(t, tx, tenant, "Check Co", "20000007-0001")
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-CHECK-01", createdAt: from})

	actingAs(t, tx, tenant)
	ctx := context.Background()
	for _, col := range []string{"irn", "csid", "qr_payload"} {
		func() {
			// A savepoint isolates the expected-to-fail UPDATE so the next column's
			// attempt still runs in a clean transaction state.
			sp, err := tx.Begin(ctx)
			if err != nil {
				t.Fatalf("begin savepoint for %s: %v", col, err)
			}
			defer func() { _ = sp.Rollback(ctx) }()

			_, err = sp.Exec(ctx, fmt.Sprintf(`UPDATE invoices SET %s = '' WHERE id = $1`, col), invID)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
				t.Errorf("UPDATE invoices SET %s = '' error = %v, want a 23514 check_violation", col, err)
			}
		}()
	}
}

func TestSelectInvoices_EmptyRejectionReasonsIsBracketsNotNull(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-invoices-empty-reasons")
	entity := mustCreateEntity(t, tx, tenant, "Empty Reasons Co", "20000008-0001")
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// rejectionReasons left "" -> default '[]'.
	invID := mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-EMPTY-REASONS-01", createdAt: from})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_, err := selectInvoices(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, w)
	if err != nil {
		t.Fatalf("selectInvoices: unexpected error: %v", err)
	}
	w.Flush()
	record := findCSVRow(t, buf.Bytes(), invID)
	if got := record[headerIndex(t, "rejection_reasons")]; got != "[]" {
		t.Errorf("csv rejection_reasons = %q, want exactly \"[]\", never null", got)
	}
}

func TestSelectInvoices_PopulatedRejectionReasonsHasNoInsignificantWhitespace(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-invoices-populated-reasons")
	entity := mustCreateEntity(t, tx, tenant, "Populated Reasons Co", "20000009-0001")
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	stored := `[{"code":"missing_buyer_tin","message":"Buyer TIN required"}]`
	invID := mustCreateInvoice(t, tx, invoiceFixture{
		tenantID: tenant, entityID: entity, invoiceNumber: "INV-POPULATED-REASONS-01", createdAt: from,
		rejectionReasons: stored,
	})

	actingAs(t, tx, tenant)
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_, err := selectInvoices(context.Background(), tx, Request{EntityID: entity, From: from, To: from.Add(time.Hour)}, w)
	if err != nil {
		t.Fatalf("selectInvoices: unexpected error: %v", err)
	}
	w.Flush()
	record := findCSVRow(t, buf.Bytes(), invID)
	cell := record[headerIndex(t, "rejection_reasons")]
	if strings.Contains(cell, ": ") || strings.Contains(cell, ", ") {
		t.Errorf("csv rejection_reasons = %q, contains insignificant whitespace after ':' or ',' "+
			"(Postgres's jsonb printer inserts it over the wire; compactJSON must strip it)", cell)
	}
	var want, got any
	if err := json.Unmarshal([]byte(stored), &want); err != nil {
		t.Fatalf("test setup: unmarshal stored json: %v", err)
	}
	if err := json.Unmarshal([]byte(cell), &got); err != nil {
		t.Fatalf("csv rejection_reasons = %q is not valid json: %v", cell, err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("csv rejection_reasons parses to %#v, want %#v (same value, just compact)", got, want)
	}
}

func TestCountInvoices_MatchesSelectInvoicesRowCount(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenant := mustCreateTenant(t, tx, "archive-count-matches")
	entity := mustCreateEntity(t, tx, tenant, "Count Co", "20000010-0001")

	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-COUNT-BEFORE", createdAt: from.Add(-time.Second)})
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-COUNT-01", createdAt: from})
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-COUNT-02", createdAt: to})
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenant, entityID: entity, invoiceNumber: "INV-COUNT-AFTER", createdAt: to.Add(time.Second)})

	actingAs(t, tx, tenant)
	req := Request{EntityID: entity, From: from, To: to}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	ids, err := selectInvoices(context.Background(), tx, req, w)
	if err != nil {
		t.Fatalf("selectInvoices: unexpected error: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("selectInvoices returned no ids -- want the two in-period invoices")
	}
	count, err := countInvoices(context.Background(), tx, req)
	if err != nil {
		t.Fatalf("countInvoices: unexpected error: %v", err)
	}
	if count != len(ids) {
		t.Errorf("countInvoices = %d, want %d (len(selectInvoices ids), AC-9)", count, len(ids))
	}
}

func TestRLS_CountInvoicesCannotReachAnotherTenantsEntity(t *testing.T) {
	super := dbSuperPool(t)
	tx := beginFixtureTx(t, super)
	tenantA := mustCreateTenant(t, tx, "archive-count-rls-a")
	tenantB := mustCreateTenant(t, tx, "archive-count-rls-b")
	entityA := mustCreateEntity(t, tx, tenantA, "Count RLS Co", "20000011-0001")
	from := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	mustCreateInvoice(t, tx, invoiceFixture{tenantID: tenantA, entityID: entityA, invoiceNumber: "INV-COUNT-RLS-01", createdAt: from.Add(time.Hour)})

	// Control needle (superuser, pre-actingAs): the fixture really planted >0 rows.
	var planted int
	if err := tx.QueryRow(context.Background(), `SELECT count(*) FROM invoices WHERE entity_id = $1`, entityA).Scan(&planted); err != nil {
		t.Fatalf("control-needle count: %v", err)
	}
	if planted == 0 {
		t.Fatal("control needle: 0 invoices planted for entityA -- fixture setup is broken")
	}

	actingAs(t, tx, tenantB)
	count, err := countInvoices(context.Background(), tx, Request{EntityID: entityA, From: from, To: to})
	if err != nil {
		t.Errorf("countInvoices(another tenant's entity) error = %v, want nil", err)
	}
	if count != 0 {
		t.Errorf("countInvoices(another tenant's entity) = %d, want 0", count)
	}
}
