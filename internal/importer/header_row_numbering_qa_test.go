package importer

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// headerRowGate flags one invoice number with a warning on both paths, so
// invoice_violations[].rows is populated on a dry run and a real run alike.
type headerRowGate struct {
	target string
}

func (g *headerRowGate) violation() []invoice.Violation {
	return []invoice.Violation{{RuleKey: "qa-header-row", Severity: "warning", Message: "synthetic"}}
}

func (g *headerRowGate) Evaluate(ctx context.Context, items []invoice.EvalItem) (invoice.EvalResult, error) {
	byRef := map[string][]invoice.Violation{}
	for _, it := range items {
		if it.Ref == g.target {
			byRef[it.Ref] = g.violation()
		}
	}
	return invoice.EvalResult{RuleSetVersion: 2, ByRef: byRef}, nil
}

func (g *headerRowGate) ValidateBatch(ctx context.Context, invs []invoice.Invoice) (invoice.BatchOutcome, error) {
	byID := map[string][]invoice.Violation{}
	clean, withViolations := 0, 0
	for _, inv := range invs {
		if inv.InvoiceNumber == g.target {
			byID[inv.ID] = g.violation()
			withViolations++
			continue
		}
		clean++
	}
	return invoice.BatchOutcome{RuleSetVersion: 2, Clean: clean, WithViolations: withViolations, ByID: byID}, nil
}

// headerRowEveryKindRows: one row per pre-create classification, read below a
// header at row 5, so rows[i] is file row 6+i.
func headerRowEveryKindRows() [][]string {
	return [][]string{
		mkRow("INV-C", "2026-01-10", "TIN", "Buyer", "NGN", "100.00", "7.50", "107.50", "Item", "1", "100.00"),  // 6: conflict
		mkRow("INV-D", "2026/01/10", "TIN", "Buyer", "NGN", "100.00", "7.50", "107.50", "Item", "1", "100.00"),  // 7: bad date
		mkRow("INV-C", "2026-01-11", "TIN", "Buyer", "NGN", "100.00", "7.50", "107.50", "Item", "1", "100.00"),  // 8: conflict
		mkRow("INV-N", "2026-01-10", "TIN", "Buyer", "NGN", "abc", "7.50", "107.50", "Item", "1", "100.00"),     // 9: bad number
		mkRow("INV-S", "2026-01-10", "TIN", "Buyer", "NGN", "100.00", "7.50", "107.50", "Item", "1", "100.00"),  // 10: stored duplicate
		mkRow("", "2026-01-10", "TIN", "Buyer", "NGN", "100.00", "7.50", "107.50", "Item", "1", "100.00"),       // 11: blank number
		mkRow("INV-OK", "2026-01-10", "TIN", "Buyer", "NGN", "100.00", "7.50", "107.50", "Item", "1", "100.00"), // 12: ready, flagged
	}
}

func findRowError(t *testing.T, errs []RowError, field, message string) RowError {
	t.Helper()
	for _, e := range errs {
		if e.Field == field && e.Message == message {
			return e
		}
	}
	t.Fatalf("no RowError with field %q and message %q in %+v", field, message, errs)
	return RowError{}
}

func assertEveryKindCountsFromRow5(t *testing.T, res BatchResult, storedID string) {
	t.Helper()
	if len(res.Errors) != 5 {
		t.Fatalf("Errors = %+v, want 5 entries", res.Errors)
	}
	checks := []struct {
		name, field, message string
		want                 []int
	}{
		{"header conflict", "issue_date", "rows disagree on issue_date", []int{6, 8}},
		{"bad date", "issue_date", "", []int{7}},
		{"bad number", "subtotal", "subtotal is not a valid number", []int{9}},
		{"stored duplicate", "invoice_number", msgDuplicateInvoiceNumber, []int{10}},
	}
	for _, c := range checks {
		var got RowError
		if c.message == "" {
			found := false
			for _, e := range res.Errors {
				if e.Field == c.field && e.Message != "rows disagree on issue_date" {
					got, found = e, true
				}
			}
			if !found {
				t.Fatalf("%s: no RowError with field %q in %+v", c.name, c.field, res.Errors)
			}
		} else {
			got = findRowError(t, res.Errors, c.field, c.message)
		}
		if got.Row != 0 || !intSliceEqual(got.Rows, c.want) {
			t.Errorf("%s: Row/Rows = %d/%v, want 0/%v", c.name, got.Row, got.Rows, c.want)
		}
	}
	dup := findRowError(t, res.Errors, "invoice_number", msgDuplicateInvoiceNumber)
	if dup.InvoiceID != storedID || dup.RuleKey != ruleKeyDuplicateInvoiceNumber {
		t.Errorf("stored duplicate InvoiceID/RuleKey = %q/%q, want %q/%q", dup.InvoiceID, dup.RuleKey, storedID, ruleKeyDuplicateInvoiceNumber)
	}
	blank := findRowError(t, res.Errors, "", "blank invoice number: row cannot be grouped")
	if blank.Row != 11 || len(blank.Rows) != 0 {
		t.Errorf("blank number Row/Rows = %d/%v, want 11/[]", blank.Row, blank.Rows)
	}

	if len(res.InvoiceViolations) != 1 {
		t.Fatalf("InvoiceViolations = %+v, want exactly INV-OK", res.InvoiceViolations)
	}
	if iv := res.InvoiceViolations[0]; iv.InvoiceNumber != "INV-OK" || !intSliceEqual(iv.Rows, []int{12}) {
		t.Errorf("InvoiceViolations[0] = %s %v, want INV-OK [12]", iv.InvoiceNumber, iv.Rows)
	}
}

// Every row-error kind and invoice_violations[].rows count from the header
// row, on the dry run and the real run, and the two agree.
func TestServiceImport_EveryRowErrorKindCountsFromTheHeaderRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "qa header-row every-kind tenant")
	entityID := seedEntity(t, super, tenantID, "qa header-row every-kind entity")
	documentID := seedDocument(t, super, tenantID)
	storedID := seedInvoice(t, super, tenantID, entityID, "INV-S")

	svc := newTestServiceWithGate(app, &headerRowGate{target: "INV-OK"})
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	dry, err := svc.Import(c, entityID, "", documentID, 5, stdMapping, stdHeader, headerRowEveryKindRows(), true)
	if err != nil {
		t.Fatalf("Import (dry run): %v", err)
	}
	assertEveryKindCountsFromRow5(t, dry, storedID)

	realRes, err := svc.Import(c, entityID, "", documentID, 5, stdMapping, stdHeader, headerRowEveryKindRows(), false)
	if err != nil {
		t.Fatalf("Import (real): %v", err)
	}
	assertEveryKindCountsFromRow5(t, realRes, storedID)

	if !reflect.DeepEqual(dry.Errors, realRes.Errors) {
		t.Errorf("dry-run Errors %+v != real Errors %+v", dry.Errors, realRes.Errors)
	}
	if got := sourceRowsOf(t, super, invoiceIDByNumber(t, super, entityID, "INV-OK")); !intSliceEqual(got, []int{12}) {
		t.Errorf("INV-OK source_rows = %v, want [12]", got)
	}

	var headerRow int
	var stored json.RawMessage
	if err := super.QueryRow(ctx, `SELECT header_row, errors FROM import_batches WHERE id = $1`, realRes.ID).Scan(&headerRow, &stored); err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if headerRow != 5 {
		t.Errorf("import_batches.header_row = %d, want 5", headerRow)
	}
	var persisted []RowError
	if err := json.Unmarshal(stored, &persisted); err != nil {
		t.Fatalf("unmarshal persisted errors: %v", err)
	}
	if !reflect.DeepEqual(persisted, realRes.Errors) {
		t.Errorf("persisted errors %+v != returned %+v", persisted, realRes.Errors)
	}
}

// A group Create refuses with a domain validation error is quarantined with
// its rows counted from the header row.
func TestServiceImport_CreateTimeQuarantineCountsFromTheHeaderRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "qa header-row create-time tenant")
	entityID := seedEntity(t, super, tenantID, "qa header-row create-time entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	rows := [][]string{
		mkRow("INV-OK", "2026-01-10", "TIN", "Buyer", "NGN", "100.00", "7.50", "107.50", "Item", "1", "100.00"),
		// numeric(14,3) cannot hold this quantity: 22003 at the line INSERT.
		mkRow("INV-OV", "2026-01-10", "TIN", "Buyer", "NGN", "100.00", "7.50", "107.50", "Item", "99999999999999", "100.00"),
	}

	res, err := svc.Import(c, entityID, "", "", 4, stdMapping, stdHeader, rows, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.ReadyInvoices != 1 || res.QuarantinedInvoices != 1 {
		t.Fatalf("Ready/Quarantined = %d/%d, want 1/1 (errors %+v)", res.ReadyInvoices, res.QuarantinedInvoices, res.Errors)
	}
	got := findRowError(t, res.Errors, "", "one or more fields failed validation")
	if got.Row != 0 || !intSliceEqual(got.Rows, []int{6}) {
		t.Errorf("Row/Rows = %d/%v, want 0/[6]", got.Row, got.Rows)
	}
}

// A duplicate that races past the ExistingNumbers precheck is reported with
// its rows counted from the header row. The competing INSERT stays
// uncommitted until Import's Create is blocked on the unique index.
func TestServiceImport_RacingDuplicateCountsFromTheHeaderRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "qa header-row race tenant")
	entityID := seedEntity(t, super, tenantID, "qa header-row race entity")
	t.Cleanup(func() {
		_, _ = super.Exec(context.Background(), `DELETE FROM invoices WHERE entity_id = $1`, entityID)
	})

	racer, err := super.Begin(ctx)
	if err != nil {
		t.Fatalf("begin racer: %v", err)
	}
	defer func() { _ = racer.Rollback(context.Background()) }()
	if _, err := racer.Exec(ctx,
		`INSERT INTO invoices (tenant_id, entity_id, invoice_number) VALUES ($1, $2, 'INV-RACE')`,
		tenantID, entityID,
	); err != nil {
		t.Fatalf("racer insert: %v", err)
	}

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})
	rows := [][]string{
		mkRow("INV-RACE", "2026-01-10", "TIN", "Buyer", "NGN", "100.00", "7.50", "107.50", "Item", "1", "100.00"),
	}

	type outcome struct {
		res BatchResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := svc.Import(c, entityID, "", "", 7, stdMapping, stdHeader, rows, false)
		done <- outcome{res, err}
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		var waiting int
		if err := super.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE datname = current_database() AND wait_event_type = 'Lock'
			    AND query LIKE '%INSERT INTO invoices%'`,
		).Scan(&waiting); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Import never blocked on the racer's uncommitted row")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := racer.Commit(ctx); err != nil {
		t.Fatalf("racer commit: %v", err)
	}

	out := <-done
	if out.err != nil {
		t.Fatalf("Import: %v", out.err)
	}
	got := findRowError(t, out.res.Errors, "invoice_number", msgDuplicateInvoiceNumber)
	if got.InvoiceID != "" {
		t.Fatalf("InvoiceID = %q: the precheck caught it, so the race backstop was not exercised", got.InvoiceID)
	}
	if got.Row != 0 || !intSliceEqual(got.Rows, []int{8}) {
		t.Errorf("Row/Rows = %d/%v, want 0/[8]", got.Row, got.Rows)
	}
}

// A header-only file still records the header row on its failed batch.
func TestServiceImport_ZeroRowFileRecordsTheHeaderRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "qa header-row zero-row tenant")
	entityID := seedEntity(t, super, tenantID, "qa header-row zero-row entity")

	svc := newTestService(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	res, err := svc.Import(c, entityID, "", "", 4, stdMapping, stdHeader, nil, false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Status != "failed" || res.ID == "" {
		t.Fatalf("Status/ID = %q/%q, want failed with a batch id", res.Status, res.ID)
	}
	var headerRow *int
	if err := super.QueryRow(ctx, `SELECT header_row FROM import_batches WHERE id = $1`, res.ID).Scan(&headerRow); err != nil {
		t.Fatalf("read header_row: %v", err)
	}
	if headerRow == nil || *headerRow != 4 {
		t.Errorf("header_row = %v, want 4", headerRow)
	}
}

// nullif maps only 0 to NULL: a negative header row reaches the CHECK, which
// refuses it, and no batch row is written.
func TestStore_CreateBatchRefusesANegativeHeaderRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "qa header-row negative tenant")
	entityID := seedEntity(t, super, tenantID, "qa header-row negative entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	id, err := store.CreateBatch(c, entityID, "f.csv", "", -1)
	if pgCode(err) != "23514" {
		t.Fatalf("CreateBatch(-1) = %q, %v; want SQLSTATE 23514", id, err)
	}
	var n int
	if err := super.QueryRow(ctx, `SELECT count(*) FROM import_batches WHERE entity_id = $1`, entityID).Scan(&n); err != nil {
		t.Fatalf("count batches: %v", err)
	}
	if n != 0 {
		t.Errorf("%d batch row(s) written, want 0", n)
	}

	// Positive control: the same call at 1 writes.
	if _, err := store.CreateBatch(c, entityID, "f.csv", "", 1); err != nil {
		t.Fatalf("CreateBatch(1): %v", err)
	}
}
