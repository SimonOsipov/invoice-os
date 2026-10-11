package importer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/invoice"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// The importer records which rules a document reading breaks. Real-gate tests run
// against the rule set in force on the fixtures' issue dates (v4: buyer-tin-format, vat-standard-rate).

// rbGate reports one fixed violation list for the invoice it is handed. hang makes Evaluate block
// until its ctx is done and sends that ctx's error on ctxErr. cancel, when set, cancels the
// caller's request ctx as Evaluate starts.
type rbGate struct {
	fakeGate
	violations []invoice.Violation
	versionID  string
	hang       bool
	ctxErr     chan error
	cancel     context.CancelFunc
}

func (g *rbGate) Evaluate(ctx context.Context, items []invoice.EvalItem) (invoice.EvalResult, error) {
	_, _ = g.fakeGate.Evaluate(ctx, items)
	if g.cancel != nil {
		g.cancel()
		return invoice.EvalResult{}, ctx.Err()
	}
	if g.hang {
		<-ctx.Done()
		g.ctxErr <- ctx.Err()
		return invoice.EvalResult{}, ctx.Err()
	}
	return invoice.EvalResult{StampByRef: map[string]invoice.Stamp{items[0].Ref: {ID: g.versionID}}, ByRef: map[string][]invoice.Violation{items[0].Ref: g.violations}}, nil
}

func (g *rbGate) ValidateBatch(ctx context.Context, invs []invoice.Invoice) (invoice.BatchOutcome, error) {
	_, _ = g.fakeGate.ValidateBatch(ctx, invs)
	return invoice.BatchOutcome{StampByID: map[string]invoice.Stamp{invs[0].ID: {ID: g.versionID}}, ByID: map[string][]invoice.Violation{invs[0].ID: g.violations}}, nil
}

var (
	rbBuyerTINViolation = invoice.Violation{RuleKey: "buyer-tin-format", Severity: "error", Message: "Buyer TIN, when present, must be in the format NNNNNNNN-NNNN.", Path: "buyer.tin"}
	rbNumberViolation   = invoice.Violation{RuleKey: "invoice-number-required", Severity: "error", Message: "Invoice number is required.", Path: "invoice_number"}
)

type rbRow struct{ tenantID, jobID, field, ruleKey, message, versionID string }

func rbRows(t *testing.T, super *pgxpool.Pool, jobID string) []rbRow {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT tenant_id::text, extraction_job_id::text, field_name, rule_key, message, rule_set_version_id::text
		   FROM extraction_rule_breaks WHERE extraction_job_id = $1 ORDER BY field_name, rule_key`, jobID)
	if err != nil {
		t.Fatalf("read extraction_rule_breaks for job %s: %v", jobID, err)
	}
	defer rows.Close()
	out := []rbRow{}
	for rows.Next() {
		var r rbRow
		if err := rows.Scan(&r.tenantID, &r.jobID, &r.field, &r.ruleKey, &r.message, &r.versionID); err != nil {
			t.Fatalf("scan extraction_rule_breaks: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate extraction_rule_breaks: %v", err)
	}
	return out
}

func rbCountTenants(t *testing.T, super *pgxpool.Pool, tenantIDs ...string) int {
	t.Helper()
	var n int
	if err := super.QueryRow(context.Background(),
		`SELECT count(*) FROM extraction_rule_breaks WHERE tenant_id::text = ANY($1)`, tenantIDs).Scan(&n); err != nil {
		t.Fatalf("count extraction_rule_breaks: %v", err)
	}
	return n
}

func rbActiveVersionID(t *testing.T, super *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(), `SELECT id::text FROM rule_set_versions WHERE id = rule_set_version_for((now() AT TIME ZONE 'UTC')::date)`).Scan(&id); err != nil {
		t.Fatalf("read the active rule-set version: %v", err)
	}
	return id
}

// rbActiveRule reads key from the version in force on docCleanValues' issue date, the one the gate judges.
func rbActiveRule(t *testing.T, super *pgxpool.Pool, key string) (message, versionID string) {
	t.Helper()
	if err := super.QueryRow(context.Background(),
		`SELECT r.message, r.rule_set_version_id::text FROM rules r
		   JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		  WHERE v.id = rule_set_version_for($1::date) AND r.key = $2`, *docCleanValues("")["issue_date"], key).Scan(&message, &versionID); err != nil {
		t.Fatalf("read the rule %q in force on the fixture's issue date: %v", key, err)
	}
	return message, versionID
}

// rbAddLines adds two lines that sum to the 1000.00 header subtotal.
func rbAddLines(values map[string]*string) map[string]*string {
	values["line_items[1].description"] = sxPtr("Widget")
	values["line_items[1].quantity"] = sxPtr("2")
	values["line_items[1].unit_price"] = sxPtr("100.00")
	values["line_items[1].line_total"] = sxPtr("200.00")
	values["line_items[2].description"] = sxPtr("Gadget")
	values["line_items[2].quantity"] = sxPtr("1")
	values["line_items[2].unit_price"] = sxPtr("800.00")
	values["line_items[2].line_total"] = sxPtr("800.00")
	return values
}

// rbSeed files one document with a seeded reading and returns its ids.
func rbSeed(t *testing.T, super *pgxpool.Pool, tenantID string, values map[string]*string) (documentID, jobID string) {
	t.Helper()
	documentID = docSeedDocument(t, super, tenantID)
	docSeedExtraction(t, super, tenantID, documentID, values)
	return documentID, extractionJobIDForDocument(t, super, documentID)
}

func rbBadTIN(values map[string]*string) map[string]*string {
	values["buyer_tin"] = sxPtr("BAD-TIN")
	return values
}

func rbRealGate(t *testing.T, app *pgxpool.Pool) *invoice.Gate {
	t.Helper()
	srv := startInProcess04ForImporter(t, app)
	return invoice.NewGate(invoice.NewStore(app), invoice.NewValidator(srv.URL, impvS2SToken, nil))
}

func rbTenant(t *testing.T, super *pgxpool.Pool, label string) (tenantID, entityID string) {
	t.Helper()
	tenantID = seedTenant(t, super, label+" tenant")
	return tenantID, seedEntityWithTIN(t, super, tenantID, label+" entity", "12345678-0001")
}

// --- real gate ------------------------------------------------------------------------

func TestRLS_ADocumentImportRecordsTheRuleItsReadingBreaks(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-TIN")
	documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-TIN-INV")))
	wantMessage, wantVersion := rbActiveRule(t, super, "buyer-tin-format")

	res, err := newTestServiceWithGate(app, rbRealGate(t, app)).ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	rows := rbRows(t, super, jobID)
	if len(rows) != 1 {
		t.Fatalf("extraction_rule_breaks rows = %+v, want exactly one (buyer_tin / buyer-tin-format)", rows)
	}
	want := rbRow{tenantID: tenantID, jobID: jobID, field: "buyer_tin", ruleKey: "buyer-tin-format", message: wantMessage, versionID: wantVersion}
	if rows[0] != want {
		t.Errorf("row = %+v, want %+v", rows[0], want)
	}

	if res.ReadyInvoices != 1 || res.RuleSetVersion != nil || len(res.InvoiceViolations) != 0 {
		t.Errorf("ReadyInvoices/RuleSetVersion/InvoiceViolations = %d/%v/%d, want 1/nil/0", res.ReadyInvoices, res.RuleSetVersion, len(res.InvoiceViolations))
	}
	number, status, rsv := e1InvoiceState(t, super, documentID)
	if number != "RB-TIN-INV" || status != string(invoice.StatusDraft) || rsv != nil {
		t.Errorf("filed invoice = %q/%q/rule_set_version_id %v, want RB-TIN-INV/draft/NULL", number, status, rsv)
	}
	_, violations, _ := readInvoiceVerdict(t, super, invoiceIDByNumber(t, super, entityID, "RB-TIN-INV"))
	var stamped []invoice.Violation
	if len(violations) > 0 {
		if err := json.Unmarshal(violations, &stamped); err != nil {
			t.Fatalf("unmarshal invoices.violations %s: %v", violations, err)
		}
	}
	if len(stamped) != 0 {
		t.Errorf("invoices.violations = %+v, want none: the import evaluates and stamps nothing", stamped)
	}
}

func TestRLS_ADocumentImportRecordsAVatRateBreak(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-VAT")
	values := rbAddLines(docCleanValues("RB-VAT-INV"))
	values["vat"] = sxPtr("50.00")
	documentID, jobID := rbSeed(t, super, tenantID, values)
	wantMessage, wantVersion := rbActiveRule(t, super, "vat-standard-rate")

	if _, err := newTestServiceWithGate(app, rbRealGate(t, app)).ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	rows := rbRows(t, super, jobID)
	if len(rows) != 1 {
		t.Fatalf("extraction_rule_breaks rows = %+v, want exactly one (vat / vat-standard-rate)", rows)
	}
	want := rbRow{tenantID: tenantID, jobID: jobID, field: "vat", ruleKey: "vat-standard-rate", message: wantMessage, versionID: wantVersion}
	if rows[0] != want {
		t.Errorf("row = %+v, want %+v", rows[0], want)
	}
}

// Guards against over-flagging. It passes before the feature exists; the positive controls are
// the filed invoice, its lines and a clean verdict from the same real gate.
func TestRLS_ACleanDocumentImportRecordsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-CLEAN")
	documentID, jobID := rbSeed(t, super, tenantID, rbAddLines(docCleanValues("RB-CLEAN-INV")))
	gate := rbRealGate(t, app)
	callCtx := sxIdentity(ctx, tenantID)

	res, err := newTestServiceWithGate(app, gate).ImportDocument(callCtx, entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	if res.ReadyInvoices != 1 {
		t.Fatalf("ReadyInvoices = %d, want 1", res.ReadyInvoices)
	}
	invID := invoiceIDByNumber(t, super, entityID, "RB-CLEAN-INV")
	if n := countLineItems(t, super, invID); n != 2 {
		t.Fatalf("filed invoice holds %d line items, want 2", n)
	}
	inv, err := invoice.NewStore(app).Get(callCtx, invID)
	if err != nil {
		t.Fatalf("Get filed invoice: %v", err)
	}
	verdict, err := gate.Evaluate(callCtx, []invoice.EvalItem{{Ref: invID, Invoice: inv}})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if vs := verdict.ByRef[invID]; len(vs) != 0 {
		t.Fatalf("the real gate finds %+v on the clean fixture, so the zero-row check below proves nothing", vs)
	}

	if rows := rbRows(t, super, jobID); len(rows) != 0 {
		t.Errorf("extraction_rule_breaks rows = %+v, want none", rows)
	}
}

func TestRLS_ARuleBreakImportStartsNoNewRead(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-NOREAD")
	documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-NOREAD-INV")))
	countJobs := func() (forDocument, forTenant int) {
		if err := super.QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE document_id = $1), count(*) FROM extraction_jobs WHERE tenant_id = $2`,
			documentID, tenantID).Scan(&forDocument, &forTenant); err != nil {
			t.Fatalf("count extraction_jobs: %v", err)
		}
		return forDocument, forTenant
	}
	if d, n := countJobs(); d != 1 || n != 1 {
		t.Fatalf("extraction_jobs before the import = %d for the document / %d for the tenant, want 1/1", d, n)
	}

	if _, err := newTestServiceWithGate(app, rbRealGate(t, app)).ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	if rows := rbRows(t, super, jobID); len(rows) != 1 {
		t.Fatalf("extraction_rule_breaks rows = %+v, want one: the flagged path ran", rows)
	}
	if d, n := countJobs(); d != 1 || n != 1 {
		t.Errorf("extraction_jobs after the import = %d for the document / %d for the tenant, want 1/1 (no AI re-read, Q15)", d, n)
	}
}

func TestRLS_ASuppliedNumberRecordsItsValidationsBreaks(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	t.Run("real gate", func(t *testing.T) {
		tenantID, entityID := rbTenant(t, super, "RB-SUP")
		values := rbBadTIN(docNoNumberValues())
		documentID, jobID := rbSeed(t, super, tenantID, values)

		inv, err := newTestServiceWithGate(app, rbRealGate(t, app)).SupplyInvoiceNumber(sxIdentity(ctx, tenantID), entityID, documentID, "RB-SUP-INV")
		if err != nil {
			t.Fatalf("SupplyInvoiceNumber: %v", err)
		}
		if inv.InvoiceNumber != "RB-SUP-INV" {
			t.Errorf("InvoiceNumber = %q, want RB-SUP-INV", inv.InvoiceNumber)
		}
		rows := rbRows(t, super, jobID)
		if len(rows) != 1 || rows[0].field != "buyer_tin" || rows[0].ruleKey != "buyer-tin-format" {
			t.Fatalf("extraction_rule_breaks rows = %+v, want exactly buyer_tin / buyer-tin-format", rows)
		}
		_, wantVersion := rbActiveRule(t, super, "buyer-tin-format")
		if rows[0].versionID != wantVersion || rows[0].tenantID != tenantID || rows[0].jobID != jobID {
			t.Errorf("row = %+v, want version %s, tenant %s, job %s", rows[0], wantVersion, tenantID, jobID)
		}
	})

	t.Run("outcome of the existing ValidateBatch call", func(t *testing.T) {
		tenantID, entityID := rbTenant(t, super, "RB-SUP-FAKE")
		documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docNoNumberValues()))
		g := &rbGate{violations: []invoice.Violation{rbBuyerTINViolation, rbNumberViolation}, versionID: rbActiveVersionID(t, super)}

		if _, err := newTestServiceWithGate(app, g).SupplyInvoiceNumber(sxIdentity(ctx, tenantID), entityID, documentID, "RB-SUP-FAKE-INV"); err != nil {
			t.Fatalf("SupplyInvoiceNumber: %v", err)
		}
		if g.evaluateCalls != 0 || g.validateBatchCalls != 1 {
			t.Errorf("gate calls Evaluate/ValidateBatch = %d/%d, want 0/1", g.evaluateCalls, g.validateBatchCalls)
		}
		rows := rbRows(t, super, jobID)
		if len(rows) != 1 || rows[0].field != "buyer_tin" {
			t.Errorf("extraction_rule_breaks rows = %+v, want exactly buyer_tin (an invoice_number violation flags nothing)", rows)
		}
	})
}

// --- fake gate ------------------------------------------------------------------------

func TestRLS_ADocumentImportEvaluatesButNeverValidates(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-EVAL")
	documentID, _ := rbSeed(t, super, tenantID, rbAddLines(docCleanValues("RB-EVAL-INV")))
	g := &fakeGate{}

	res, err := newTestServiceWithGate(app, g).ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	if g.evaluateCalls != 1 || g.validateBatchCalls != 0 {
		t.Fatalf("gate calls Evaluate/ValidateBatch = %d/%d, want 1/0", g.evaluateCalls, g.validateBatchCalls)
	}
	invID := invoiceIDByNumber(t, super, entityID, "RB-EVAL-INV")
	if len(g.evaluateItems) != 1 || g.evaluateItems[0].Ref != invID || g.evaluateItems[0].Invoice.ID != invID {
		t.Fatalf("Evaluate items = %+v, want one item with Ref and Invoice.ID %s", g.evaluateItems, invID)
	}
	if n := len(g.evaluateItems[0].Invoice.LineItems); n != 2 {
		t.Errorf("Evaluate item carries %d line items, want 2: the gate judges the hydrated invoice", n)
	}
	if res.RuleSetVersion != nil || res.InvoicesClean != 0 || res.InvoicesWithViolations != 0 {
		t.Errorf("BatchResult stamped a verdict: %+v", res)
	}
}

func TestRLS_AHungEvaluateIsCutOffAndRecordsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, entityID := rbTenant(t, super, "RB-HANG")
	documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-HANG-INV")))
	g := &rbGate{hang: true, ctxErr: make(chan error, 1), violations: []invoice.Violation{rbBuyerTINViolation}, versionID: rbActiveVersionID(t, super)}

	saved := ruleBreakEvaluateTimeout
	ruleBreakEvaluateTimeout = 300 * time.Millisecond
	t.Cleanup(func() { ruleBreakEvaluateTimeout = saved })

	callCtx, cancel := context.WithCancel(sxIdentity(context.Background(), tenantID))
	defer cancel()
	type outcome struct {
		res BatchResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := newTestServiceWithGate(app, g).ImportDocument(callCtx, entityID, documentID)
		done <- outcome{res, err}
	}()

	var out outcome
	select {
	case out = <-done:
	case <-time.After(ruleBreakEvaluateTimeout + 2*time.Second):
		cancel()
		t.Fatalf("ImportDocument still running %v after the Evaluate bound of %v", 2*time.Second, ruleBreakEvaluateTimeout)
	}
	if out.err != nil {
		t.Fatalf("ImportDocument: %v, want nil: a hung Evaluate must not fail the import", out.err)
	}
	if out.res.ReadyInvoices != 1 {
		t.Errorf("ReadyInvoices = %d, want 1", out.res.ReadyInvoices)
	}
	if g.evaluateCalls != 1 {
		t.Fatalf("Evaluate calls = %d, want 1", g.evaluateCalls)
	}
	select {
	case got := <-g.ctxErr:
		if !errors.Is(got, context.DeadlineExceeded) {
			t.Errorf("the gate's ctx ended with %v, want context.DeadlineExceeded", got)
		}
	case <-time.After(2 * time.Second):
		t.Error("the gate's ctx was never cancelled")
	}
	if got := countInvoicesByNumber(t, super, entityID, "RB-HANG-INV"); got != 1 {
		t.Errorf("invoices RB-HANG-INV = %d, want 1", got)
	}
	if rows := rbRows(t, super, jobID); len(rows) != 0 {
		t.Errorf("extraction_rule_breaks rows = %+v, want none", rows)
	}
}

func TestRLS_ARequestCancelledDuringEvaluateStillCompletesTheBatch(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID, entityID := rbTenant(t, super, "RB-CANCEL")
	documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-CANCEL-INV")))
	callCtx, cancel := context.WithCancel(sxIdentity(context.Background(), tenantID))
	defer cancel()
	g := &rbGate{cancel: cancel}

	res, err := newTestServiceWithGate(app, g).ImportDocument(callCtx, entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v, want nil: the rule check runs after the batch is finalized", err)
	}
	if res.Status != "completed" || res.ReadyInvoices != 1 {
		t.Errorf("Status/ReadyInvoices = %q/%d, want completed/1", res.Status, res.ReadyInvoices)
	}
	if _, status, _, _, _ := docBatchRowByEntity(t, super, entityID); status != "completed" {
		t.Errorf("import_batches.status = %q, want completed", status)
	}
	if rows := rbRows(t, super, jobID); len(rows) != 0 {
		t.Errorf("extraction_rule_breaks rows = %+v, want none", rows)
	}
}

func TestRLS_AFinalizeFailureRecordsNoRuleBreaksAndCallsNoGate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-FINFAIL")
	documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-FINFAIL-INV")))
	g := &rbGate{violations: []invoice.Violation{rbBuyerTINViolation}, versionID: rbActiveVersionID(t, super)}

	// Scoped to this entity, so no other test's Finalize meets it.
	if _, err := super.Exec(ctx, `CREATE OR REPLACE FUNCTION rb_finfail() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN RAISE EXCEPTION 'rb_finfail'; END $$`); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	if _, err := super.Exec(ctx, `CREATE TRIGGER rb_finfail BEFORE UPDATE ON import_batches FOR EACH ROW
		WHEN (NEW.entity_id = '`+entityID+`' AND NEW.status = 'completed') EXECUTE FUNCTION rb_finfail()`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = super.Exec(ctx, `DROP TRIGGER IF EXISTS rb_finfail ON import_batches`)
		_, _ = super.Exec(ctx, `DROP FUNCTION IF EXISTS rb_finfail()`)
	})

	_, err := newTestServiceWithGate(app, g).ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err == nil || !strings.Contains(err.Error(), "rb_finfail") {
		t.Fatalf("ImportDocument err = %v, want the Finalize failure", err)
	}
	if got := countInvoicesByNumber(t, super, entityID, "RB-FINFAIL-INV"); got != 1 {
		t.Fatalf("invoices RB-FINFAIL-INV = %d, want 1: the failure must come after the invoice is filed", got)
	}
	if g.evaluateCalls != 0 {
		t.Errorf("Evaluate calls = %d, want 0: a batch that failed to finalize is not rule-checked", g.evaluateCalls)
	}
	if rows := rbRows(t, super, jobID); len(rows) != 0 {
		t.Errorf("extraction_rule_breaks rows = %+v, want none", rows)
	}
}

func TestRLS_AnEvaluateOutageLeavesTheImportAsItWas(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"upstream outage", invoice.ErrUpstream},
		{"no active rule set", invoice.ErrNoActiveRuleSet},
		{"any other error", errors.New("boom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenantID, entityID := rbTenant(t, super, "RB-OUT")
			documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-OUT-INV")))
			g := &fakeGate{evaluateErr: tc.err}

			res, err := newTestServiceWithGate(app, g).ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
			if err != nil {
				t.Fatalf("ImportDocument: %v, want nil", err)
			}
			if g.evaluateCalls != 1 {
				t.Fatalf("Evaluate calls = %d, want 1", g.evaluateCalls)
			}
			if res.Status != "completed" || res.RowsTotal != 1 || res.RowsValid != 1 || res.ReadyInvoices != 1 ||
				res.QuarantinedInvoices != 0 || len(res.Errors) != 0 || len(res.InvoiceViolations) != 0 || res.RuleSetVersion != nil {
				t.Errorf("BatchResult = %+v, want the normal completed 1/1/1 result", res)
			}
			number, status, rsv := e1InvoiceState(t, super, documentID)
			if number != "RB-OUT-INV" || status != string(invoice.StatusDraft) || rsv != nil {
				t.Errorf("filed invoice = %q/%q/rule_set_version_id %v, want RB-OUT-INV/draft/NULL", number, status, rsv)
			}
			if rows := rbRows(t, super, jobID); len(rows) != 0 {
				t.Errorf("extraction_rule_breaks rows = %+v, want none", rows)
			}
		})
	}
}

func TestRLS_AQuarantinedImportCallsNoGate(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-QUAR")
	callCtx := sxIdentity(ctx, tenantID)
	g := &rbGate{violations: []invoice.Violation{rbBuyerTINViolation}, versionID: rbActiveVersionID(t, super)}
	svc := newTestServiceWithGate(app, g)

	if _, err := invoice.NewStore(app).Create(callCtx, invoice.CreateInput{EntityID: entityID, InvoiceNumber: "RB-QUAR-DUP"}); err != nil {
		t.Fatalf("seed the invoice that the duplicate collides with: %v", err)
	}
	dupDoc, dupJob := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-QUAR-DUP")))
	unreadable := rbBadTIN(docCleanValues("unused"))
	unreadable["invoice_number"] = nil
	noNumberDoc, noNumberJob := rbSeed(t, super, tenantID, unreadable)

	for name, documentID := range map[string]string{"duplicate number": dupDoc, "unreadable number": noNumberDoc} {
		res, err := svc.ImportDocument(callCtx, entityID, documentID)
		if err != nil {
			t.Fatalf("%s: ImportDocument: %v", name, err)
		}
		if res.QuarantinedInvoices != 1 || res.ReadyInvoices != 0 {
			t.Fatalf("%s: Quarantined/Ready = %d/%d, want 1/0", name, res.QuarantinedInvoices, res.ReadyInvoices)
		}
	}
	if g.evaluateCalls != 0 || g.validateBatchCalls != 0 {
		t.Errorf("gate calls Evaluate/ValidateBatch = %d/%d after two quarantined imports, want 0/0", g.evaluateCalls, g.validateBatchCalls)
	}
	for _, jobID := range []string{dupJob, noNumberJob} {
		if rows := rbRows(t, super, jobID); len(rows) != 0 {
			t.Errorf("job %s: extraction_rule_breaks rows = %+v, want none", jobID, rows)
		}
	}

	// control: the same gate and tenant do record for a filed import
	okDoc, okJob := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-QUAR-OK")))
	if res, err := svc.ImportDocument(callCtx, entityID, okDoc); err != nil || res.ReadyInvoices != 1 {
		t.Fatalf("control import: res=%+v err=%v, want 1 ready invoice", res, err)
	}
	if g.evaluateCalls != 1 {
		t.Errorf("control import: Evaluate calls = %d, want 1", g.evaluateCalls)
	}
	if rows := rbRows(t, super, okJob); len(rows) != 1 {
		t.Errorf("control import: extraction_rule_breaks rows = %+v, want one", rows)
	}
}

func TestRLS_ARuleBreakWriteFailureLeavesTheImportAsItWas(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-WFAIL")
	callCtx := sxIdentity(ctx, tenantID)
	g := &rbGate{violations: []invoice.Violation{rbBuyerTINViolation}, versionID: "not-a-uuid"}
	svc := newTestServiceWithGate(app, g)
	documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-WFAIL-INV")))

	res, err := svc.ImportDocument(callCtx, entityID, documentID)
	if err != nil {
		t.Fatalf("ImportDocument: %v, want nil: the invoice is filed, a 500 would invite a duplicate retry", err)
	}
	if g.evaluateCalls != 1 {
		t.Fatalf("Evaluate calls = %d, want 1: the violation never reached the write", g.evaluateCalls)
	}
	if res.ReadyInvoices != 1 || res.Status != "completed" {
		t.Errorf("Status/ReadyInvoices = %q/%d, want completed/1", res.Status, res.ReadyInvoices)
	}
	if got := countInvoicesByNumber(t, super, entityID, "RB-WFAIL-INV"); got != 1 {
		t.Errorf("invoices RB-WFAIL-INV = %d, want 1", got)
	}
	if rows := rbRows(t, super, jobID); len(rows) != 0 {
		t.Errorf("extraction_rule_breaks rows = %+v, want none", rows)
	}

	// control: a real version id writes the row, so the failure above came from the bad id alone
	g.versionID = rbActiveVersionID(t, super)
	okDoc, okJob := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-WFAIL-OK")))
	if _, err := svc.ImportDocument(callCtx, entityID, okDoc); err != nil {
		t.Fatalf("control ImportDocument: %v", err)
	}
	if rows := rbRows(t, super, okJob); len(rows) != 1 {
		t.Errorf("control: extraction_rule_breaks rows = %+v, want one", rows)
	}
}

func TestRLS_ARuleBreakWriteFailureLeavesTheSuppliedInvoiceAsItWas(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-WFAIL-SUP")
	callCtx := sxIdentity(ctx, tenantID)
	g := &rbGate{violations: []invoice.Violation{rbBuyerTINViolation}, versionID: "not-a-uuid"}
	svc := newTestServiceWithGate(app, g)
	documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docNoNumberValues()))

	inv, err := svc.SupplyInvoiceNumber(callCtx, entityID, documentID, "RB-WFAIL-SUP-INV")
	if err != nil {
		t.Fatalf("SupplyInvoiceNumber: %v, want nil", err)
	}
	if g.validateBatchCalls != 1 {
		t.Fatalf("ValidateBatch calls = %d, want 1: the violation never reached the write", g.validateBatchCalls)
	}
	if inv.InvoiceNumber != "RB-WFAIL-SUP-INV" || inv.ID == "" {
		t.Errorf("returned invoice = %q/%q, want the filed RB-WFAIL-SUP-INV", inv.InvoiceNumber, inv.ID)
	}
	if got := countInvoicesByNumber(t, super, entityID, "RB-WFAIL-SUP-INV"); got != 1 {
		t.Errorf("invoices RB-WFAIL-SUP-INV = %d, want 1", got)
	}
	if rows := rbRows(t, super, jobID); len(rows) != 0 {
		t.Errorf("extraction_rule_breaks rows = %+v, want none", rows)
	}

	// control: a real version id writes the row
	g.versionID = rbActiveVersionID(t, super)
	okDoc, okJob := rbSeed(t, super, tenantID, rbBadTIN(docNoNumberValues()))
	if _, err := svc.SupplyInvoiceNumber(callCtx, entityID, okDoc, "RB-WFAIL-SUP-OK"); err != nil {
		t.Fatalf("control SupplyInvoiceNumber: %v", err)
	}
	if rows := rbRows(t, super, okJob); len(rows) != 1 {
		t.Errorf("control: extraction_rule_breaks rows = %+v, want one", rows)
	}
}

// --- Store.RecordRuleBreaks -------------------------------------------------------------

func TestRLS_RecordRuleBreaksTwiceKeepsOneRow(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, _ := rbTenant(t, super, "RB-TWICE")
	_, jobID := rbSeed(t, super, tenantID, docCleanValues("RB-TWICE-INV"))
	callCtx := sxIdentity(ctx, tenantID)
	store := NewStore(app)
	versionID := rbActiveVersionID(t, super)
	breaks := []RuleBreak{
		{Field: "buyer_tin", RuleKey: "buyer-tin-format", Message: "first"},
		{Field: "vat", RuleKey: "vat-standard-rate", Message: "second"},
		{Field: "vat", RuleKey: "vat-other", Message: "third"},
	}

	if err := store.RecordRuleBreaks(callCtx, jobID, "not-a-uuid", nil); err != nil {
		t.Fatalf("RecordRuleBreaks(empty): %v, want a no-op that never reaches the bad version id", err)
	}
	// a no-op opens no transaction: with no identity, an empty list is nil and a real list is refused
	if err := store.RecordRuleBreaks(ctx, jobID, versionID, nil); err != nil {
		t.Errorf("RecordRuleBreaks(empty, no identity): %v, want nil: no transaction opens", err)
	}
	if err := store.RecordRuleBreaks(ctx, jobID, versionID, breaks); err == nil {
		t.Fatal("RecordRuleBreaks(rows, no identity) = nil, want an error: the control for the empty-list no-op above")
	}
	if err := store.RecordRuleBreaks(callCtx, jobID, versionID, breaks); err != nil {
		t.Fatalf("RecordRuleBreaks #1: %v", err)
	}
	if rows := rbRows(t, super, jobID); len(rows) != 3 {
		t.Fatalf("rows after the first write = %+v, want 3", rows)
	}

	if err := store.RecordRuleBreaks(callCtx, jobID, versionID, breaks); err != nil {
		t.Fatalf("RecordRuleBreaks #2 (same rows): %v, want nil", err)
	}
	changed := []RuleBreak{{Field: "buyer_tin", RuleKey: "buyer-tin-format", Message: "reworded"}}
	if err := store.RecordRuleBreaks(callCtx, jobID, versionID, changed); err != nil {
		t.Fatalf("RecordRuleBreaks #3 (same key, new message): %v, want nil (DO NOTHING, not an UPDATE)", err)
	}

	rows := rbRows(t, super, jobID)
	if len(rows) != 3 {
		t.Fatalf("rows after the repeats = %+v, want still 3", rows)
	}
	for _, r := range rows {
		if r.tenantID != tenantID || r.versionID != versionID {
			t.Errorf("row %+v, want tenant %s and version %s", r, tenantID, versionID)
		}
		if r.field == "buyer_tin" && r.message != "first" {
			t.Errorf("buyer_tin message = %q, want the first write's %q", r.message, "first")
		}
	}
}

func TestRLS_RecordRuleBreaksRefusesAnotherTenantsJob(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantA, _ := rbTenant(t, super, "RB-XT-A")
	tenantB, _ := rbTenant(t, super, "RB-XT-B")
	_, jobA := rbSeed(t, super, tenantA, docCleanValues("RB-XT-INV"))
	store := NewStore(app)
	versionID := rbActiveVersionID(t, super)
	breaks := []RuleBreak{{Field: "buyer_tin", RuleKey: "buyer-tin-format", Message: "m"}}

	if err := store.RecordRuleBreaks(sxIdentity(ctx, tenantB), jobA, versionID, breaks); err == nil {
		t.Error("RecordRuleBreaks as tenant B on tenant A's job returned nil, want an error")
	}
	if n := rbCountTenants(t, super, tenantA, tenantB); n != 0 {
		t.Fatalf("extraction_rule_breaks rows in either tenant = %d, want 0", n)
	}

	// control: tenant A writes its own job
	if err := store.RecordRuleBreaks(sxIdentity(ctx, tenantA), jobA, versionID, breaks); err != nil {
		t.Fatalf("RecordRuleBreaks as tenant A: %v", err)
	}
	if rows := rbRows(t, super, jobA); len(rows) != 1 || rows[0].tenantID != tenantA {
		t.Errorf("rows = %+v, want one row stamped with tenant A", rows)
	}

	// the row tenant A wrote is visible to A and invisible to B through the app role
	seenBy := func(tenantID string) (n int) {
		err := db.WithinRequestTenantTx(sxIdentity(ctx, tenantID), app, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM extraction_rule_breaks`).Scan(&n)
		})
		if err != nil {
			t.Fatalf("read extraction_rule_breaks as tenant %s: %v", tenantID, err)
		}
		return n
	}
	if got := seenBy(tenantA); got != 1 {
		t.Errorf("tenant A sees %d rows, want 1", got)
	}
	if got := seenBy(tenantB); got != 0 {
		t.Errorf("tenant B sees %d rows, want 0", got)
	}
}

func TestRLS_AnotherTenantsImportOfMyDocumentRecordsNothing(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantA, entityA := rbTenant(t, super, "RB-IMP-A")
	tenantB, entityB := rbTenant(t, super, "RB-IMP-B")
	documentA, jobA := rbSeed(t, super, tenantA, rbBadTIN(docCleanValues("RB-IMP-INV")))
	g := &rbGate{violations: []invoice.Violation{rbBuyerTINViolation}, versionID: rbActiveVersionID(t, super)}

	if _, err := newTestServiceWithGate(app, g).ImportDocument(sxIdentity(ctx, tenantB), entityB, documentA); err == nil {
		t.Fatal("tenant B imported tenant A's document without error, want ErrNotFound")
	}
	if g.evaluateCalls != 0 {
		t.Errorf("Evaluate calls = %d, want 0: the refused import never reached the gate", g.evaluateCalls)
	}
	if n := rbCountTenants(t, super, tenantA, tenantB); n != 0 {
		t.Errorf("extraction_rule_breaks rows in either tenant = %d, want 0", n)
	}

	// control: tenant A's own import of the same document records the break
	if res, err := newTestServiceWithGate(app, g).ImportDocument(sxIdentity(ctx, tenantA), entityA, documentA); err != nil || res.ReadyInvoices != 1 {
		t.Fatalf("control import: res=%+v err=%v", res, err)
	}
	if rows := rbRows(t, super, jobA); len(rows) != 1 {
		t.Errorf("control: rows = %+v, want one", rows)
	}
}

func TestRLS_AReimportAfterTheDraftIsDeletedAddsOnlyTheNewBreaks(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-REIMP")
	documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-REIMP-INV")))
	callCtx := sxIdentity(ctx, tenantID)
	vatBreak := invoice.Violation{RuleKey: "vat-standard-rate", Severity: "error", Message: "VAT is not 7.5%.", Path: "vat"}
	g := &rbGate{violations: []invoice.Violation{rbBuyerTINViolation}, versionID: rbActiveVersionID(t, super)}
	svc := newTestServiceWithGate(app, g)

	if _, err := svc.ImportDocument(callCtx, entityID, documentID); err != nil {
		t.Fatalf("first ImportDocument: %v", err)
	}
	if rows := rbRows(t, super, jobID); len(rows) != 1 {
		t.Fatalf("rows after the first import = %+v, want one", rows)
	}
	if _, err := super.Exec(ctx, `DELETE FROM invoices WHERE entity_id = $1 AND invoice_number = $2`, entityID, "RB-REIMP-INV"); err != nil {
		t.Fatalf("delete the filed draft: %v", err)
	}

	// the repeat buyer_tin row must not sink the new vat row written in the same transaction
	g.violations = []invoice.Violation{rbBuyerTINViolation, vatBreak}
	res, err := svc.ImportDocument(callCtx, entityID, documentID)
	if err != nil || res.ReadyInvoices != 1 {
		t.Fatalf("second ImportDocument: res=%+v err=%v, want the draft re-filed", res, err)
	}
	rows := rbRows(t, super, jobID)
	if len(rows) != 2 || rows[0].field != "buyer_tin" || rows[1].field != "vat" {
		t.Errorf("rows after the re-import = %+v, want buyer_tin once and the new vat row", rows)
	}
}

func TestRLS_AViolationFiledUnderAnotherRefIsNotRecorded(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-REF")
	documentID, jobID := rbSeed(t, super, tenantID, rbBadTIN(docCleanValues("RB-REF-INV")))
	g := &fakeGate{evaluateResult: invoice.EvalResult{
		StampByRef: map[string]invoice.Stamp{"some-other-invoice": {ID: rbActiveVersionID(t, super)}},
		ByRef:      map[string][]invoice.Violation{"some-other-invoice": {rbBuyerTINViolation}},
	}}

	res, err := newTestServiceWithGate(app, g).ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID)
	if err != nil || res.ReadyInvoices != 1 {
		t.Fatalf("ImportDocument: res=%+v err=%v", res, err)
	}
	if g.evaluateCalls != 1 {
		t.Fatalf("Evaluate calls = %d, want 1", g.evaluateCalls)
	}
	if rows := rbRows(t, super, jobID); len(rows) != 0 {
		t.Errorf("rows = %+v, want none: the new invoice has no violation of its own", rows)
	}
}

func TestRLS_EveryViolationOfOneImportIsRecordedInOneWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	tenantID, entityID := rbTenant(t, super, "RB-MANY")
	values := docCleanValues("RB-MANY-INV")
	values["buyer_tin"] = sxPtr("BAD-TIN")
	documentID, jobID := rbSeed(t, super, tenantID, values)
	warn := invoice.Violation{RuleKey: "vat-standard-rate", Severity: "warning", Message: "VAT looks off.", Path: "vat"}
	g := &rbGate{violations: []invoice.Violation{
		rbBuyerTINViolation, warn, rbNumberViolation,
		{RuleKey: "supplier-tin-required", Severity: "error", Message: "Supplier TIN is required.", Path: "supplier.tin"},
		{RuleKey: "line-items-required", Severity: "error", Message: "Add a line.", Path: "line_items"},
	}, versionID: rbActiveVersionID(t, super)}

	if _, err := newTestServiceWithGate(app, g).ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}
	rows := rbRows(t, super, jobID)
	if len(rows) != 2 || rows[0].field != "buyer_tin" || rows[1].field != "vat" || rows[1].ruleKey != "vat-standard-rate" {
		t.Errorf("rows = %+v, want exactly buyer_tin and the warning-severity vat row (locked, supplier and line paths flag nothing)", rows)
	}
}

// rbSeedV5Lists inserts one real code per v5 code list as the superuser; cleanup deletes only the rows it inserted.
func rbSeedV5Lists(t *testing.T, super *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	codes := map[string]string{
		"currencies": "NGN", "countries": "NG", "states": "NG-LA", "lgas": "NG-LA-AGE",
		"invoice-quantity-codes": "EA", "hs-codes": "8471.30", "services-codes": "6201", "tax-categories": "STANDARD_VAT",
	}
	var inserted [][2]string
	for list, code := range codes {
		tag, err := super.Exec(ctx,
			`INSERT INTO nrs_codes (list, code, entries) VALUES ($1, $2, '[{}]') ON CONFLICT DO NOTHING`, list, code)
		if err != nil {
			t.Fatalf("seed nrs_codes %s/%s: %v", list, code, err)
		}
		if tag.RowsAffected() == 1 {
			inserted = append(inserted, [2]string{list, code})
		}
	}
	t.Cleanup(func() {
		for _, r := range inserted {
			_, _ = super.Exec(context.Background(), `DELETE FROM nrs_codes WHERE list = $1 AND code = $2`, r[0], r[1])
		}
	})
}

// Under v5 a reading carries no tax category, so the header VAT is judged by vat-standard-rate-uncategorised.
func TestRLS_AV5DocumentImportRecordsAnUncategorisedVatBreak(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	rbSeedV5Lists(t, super)
	tenantID, entityID := rbTenant(t, super, "RB-V5")
	values := rbAddLines(docCleanValues("RB-V5-INV"))
	values["issue_date"] = sxPtr("2027-01-15")
	values["vat"] = sxPtr("50.00")
	documentID, jobID := rbSeed(t, super, tenantID, values)

	var wantMessage, wantVersion string
	var version int
	if err := super.QueryRow(ctx,
		`SELECT r.message, v.id::text, v.version FROM rules r JOIN rule_set_versions v ON v.id = r.rule_set_version_id
		  WHERE v.id = rule_set_version_for('2027-01-15'::date) AND r.key = 'vat-standard-rate-uncategorised'`).Scan(&wantMessage, &wantVersion, &version); err != nil {
		t.Fatalf("read vat-standard-rate-uncategorised in force on 2027-01-15: %v", err)
	}
	if version != 5 {
		t.Fatalf("rule set in force on 2027-01-15 = v%d, want v5", version)
	}

	if _, err := newTestServiceWithGate(app, rbRealGate(t, app)).ImportDocument(sxIdentity(ctx, tenantID), entityID, documentID); err != nil {
		t.Fatalf("ImportDocument: %v", err)
	}

	rows := rbRows(t, super, jobID)
	want := rbRow{tenantID: tenantID, jobID: jobID, field: "vat", ruleKey: "vat-standard-rate-uncategorised", message: wantMessage, versionID: wantVersion}
	if len(rows) != 1 || rows[0] != want {
		t.Errorf("extraction_rule_breaks rows = %+v, want exactly %+v", rows, want)
	}
}
