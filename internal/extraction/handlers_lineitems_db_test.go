// handlers_lineitems_db_test.go: what POST /v1/extractions/{id}/line-items commits, and what it
// refuses. Shares store_db_test.go's harness (stRequire/stTenant) and reader_db_test.go's
// rdTenant, so this file adds no second skip site. Every case runs over stRequire(t).app --
// invoice_app, never the superuser -- so a cross-tenant refusal is proved against the policy,
// not merely against application code.
//
// The invoice seam is the test's own closure, not internal/invoice: deps_test.go fences this
// package off everything outside internal/platform/* in BOTH scans, test imports included. So
// lixApplier writes line_items with raw SQL, mirroring replaceLinesTx's shape; the actual
// demotion RULE lives in internal/invoice and is proved there -- this file proves only that the
// handler's ONE transaction carries through whatever the seam does.
//
// Helpers use a lix* prefix; hnd dtl up rd st wk dc fx mx px pr ps pt pd pb pe rx de rp eq cs
// rvd cor cx rda li are taken.
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// lixApplyOpts configures lixApplier's closure. write performs the DELETE+INSERT replace;
// demote additionally moves a validated invoice to draft, mirroring what the real seam
// (internal/invoice, out of reach here) is expected to do.
type lixApplyOpts struct {
	write     bool
	demote    bool
	failAfter error
}

// lixApplier resolves the invoice filed from documentID on the CALLER's transaction, optionally
// replaces its lines and demotes it, then reports failAfter. Writing before failing is what
// makes the rollback arms mean something.
func lixApplier(opts lixApplyOpts) extraction.ApplyLineItemsToInvoice {
	return func(ctx context.Context, tx pgx.Tx, documentID string, lines []extraction.LineItemInput) (string, error) {
		var id, tenantID string
		if err := tx.QueryRow(ctx,
			`SELECT id, tenant_id FROM invoices WHERE source_document_id = $1`, documentID).Scan(&id, &tenantID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", extraction.ErrNoInvoiceForDocument
			}
			return "", err
		}
		if opts.write {
			if _, err := tx.Exec(ctx, `DELETE FROM line_items WHERE invoice_id = $1`, id); err != nil {
				return "", err
			}
			for i, l := range lines {
				if _, err := tx.Exec(ctx,
					`INSERT INTO line_items (tenant_id, invoice_id, line_no, description, quantity, unit_price, line_total)
					 VALUES ($1, $2, $3, $4, $5::text::numeric, $6::text::numeric, $7::text::numeric)`,
					tenantID, id, i+1, l.Description, l.Quantity, l.UnitPrice, l.LineTotal); err != nil {
					return "", err
				}
			}
		}
		if opts.demote {
			if _, err := tx.Exec(ctx, `UPDATE invoices SET status = 'draft' WHERE id = $1 AND status = 'validated'`, id); err != nil {
				return "", err
			}
		}
		if opts.failAfter != nil {
			return "", opts.failAfter
		}
		return id, nil
	}
}

// lixRefusingApplier reports one domain sentinel without touching a row.
func lixRefusingApplier(err error) extraction.ApplyLineItemsToInvoice {
	return func(context.Context, pgx.Tx, string, []extraction.LineItemInput) (string, error) { return "", err }
}

// lixDBServe drives the handler once over the real app pool.
func lixDBServe(t *testing.T, ctx context.Context, jobID, body string,
	apply extraction.ApplyLineItemsToInvoice, record extraction.RecordFieldCorrected) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/extractions/"+jobID+"/line-items", strings.NewReader(body))
	r.SetPathValue(corPathID, jobID)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	extraction.LineItemsHandler(stRequire(t).app, apply, record, nil)(w, r)
	return w
}

// lixLineRows returns invoiceID's line_items descriptions, ordered by line_no, as the
// SUPERUSER: an app-pool read is RLS-scoped and would read the same whether or not a row was
// written.
func lixLineRows(t *testing.T, ctx context.Context, invoiceID string) []string {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT description FROM line_items WHERE invoice_id = $1 ORDER BY line_no`, invoiceID)
	if err != nil {
		t.Fatalf("read line_items for invoice %s: %v", invoiceID, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var d *string
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scan line_items row: %v", err)
		}
		if d == nil {
			out = append(out, "<null>")
			continue
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read line_items for invoice %s: %v", invoiceID, err)
	}
	return out
}

func lixInvoiceStatus(t *testing.T, ctx context.Context, invoiceID string) string {
	t.Helper()
	var s string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&s); err != nil {
		t.Fatalf("read invoices.status for %s: %v", invoiceID, err)
	}
	return s
}

// lixAuditGeneratedInvoiceID reads the audit_log.invoice_id GENERATED column for this tenant's
// most recent extraction.field_corrected row.
func lixAuditGeneratedInvoiceID(t *testing.T, ctx context.Context, tenantID string) *string {
	t.Helper()
	var id *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT invoice_id::text FROM audit_log WHERE tenant_id = $1 AND event = $2 ORDER BY id DESC LIMIT 1`,
		tenantID, cxEvent).Scan(&id); err != nil {
		t.Fatalf("read audit_log.invoice_id for tenant %s: %v", tenantID, err)
	}
	return id
}

// lixCommitControl seeds its own fixture and drives one unimpeded write through. Every refusal
// case below asserts zero rows, and that assertion is satisfied by a handler that writes
// nothing ever -- this is what shows they can fail.
func lixCommitControl(t *testing.T, ctx context.Context) {
	t.Helper()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-CONTROL", "draft")

	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("control: an unimpeded write answered %d (body=%q), want 201 -- the assertions below are then vacuous", w.Code, w.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 1 {
		t.Fatalf("control: an unimpeded write left %d correction row(s), want 1", n)
	}
	if got := lixLineRows(t, ctx, invoiceID); len(got) != 1 {
		t.Fatalf("control: an unimpeded write left %d line_items row(s), want 1", len(got))
	}
}

// --- AC 3: the correction row, the invoice write and the audit row share one transaction ------

func TestRLS_LineItemsWriteIsOneTransaction(t *testing.T) {
	ctx := t.Context()
	boom := errors.New("lix: induced failure")

	// The control: an unimpeded write commits all three together, so the zero-row assertions in
	// the arms below mean something.
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-TX-CTRL", "draft")
	ctrl := lixDBServe(t, reqCtx, jobID, lixLinesBody(2), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	if ctrl.Code != http.StatusCreated {
		t.Fatalf("control: %d (body=%q), want 201 -- the arms below then prove nothing", ctrl.Code, ctrl.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 1 {
		t.Fatalf("control: %d correction row(s), want 1", n)
	}
	if rows := cxCorrectionAudit(t, ctx, tenantID); len(rows) != 1 {
		t.Fatalf("control: %d audit row(s), want 1", len(rows))
	}
	if got := lixLineRows(t, ctx, invoiceID); len(got) != 2 {
		t.Fatalf("control: %d line_items row(s), want 2", len(got))
	}

	for _, tc := range []struct {
		name   string
		apply  extraction.ApplyLineItemsToInvoice
		record extraction.RecordFieldCorrected
	}{
		{"the invoice write fails after touching the lines", lixApplier(lixApplyOpts{write: true, failAfter: boom}), cxAuditor(nil)},
		{"the audit write fails after the correction row", lixApplier(lixApplyOpts{write: true}), cxAuditor(boom)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
			t.Cleanup(func() { rdaPurge(t, tenantID) })
			entityID := cxEntity(t, ctx, tenantID)
			invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-TX", "draft")

			w := lixDBServe(t, reqCtx, jobID, lixLinesBody(2), tc.apply, tc.record)

			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want %d for an induced failure (body=%q)", w.Code, http.StatusInternalServerError, w.Body.String())
			}
			if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
				t.Errorf("%d correction row(s) survived a failed write, want 0", n)
			}
			if rows := cxCorrectionAudit(t, ctx, tenantID); len(rows) != 0 {
				t.Errorf("%d audit row(s) survived a failed write, want 0", len(rows))
			}
			if got := lixLineRows(t, ctx, invoiceID); len(got) != 0 {
				t.Errorf("%d line_items row(s) survived a failed write, want 0 -- the invoice write did not share the correction's fate", len(got))
			}
		})
	}
}

// --- AC 6: another tenant's job reads exactly like an absent one -------------------------

func TestRLS_LineItemsCrossTenantIsIndistinguishableFromAbsent(t *testing.T) {
	ctx := t.Context()
	reqCtxA, tenantA, documentA, jobA := cxJob(t, ctx)
	_, tenantB, _, jobB := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantA, tenantB) })
	entityA := cxEntity(t, ctx, tenantA)
	cxInvoice(t, ctx, tenantA, entityA, documentA, "EXTR13-03-XT", "draft")

	// The positive control: A's own job is answerable, so the two refusals below are refusing
	// the job rather than refusing every request.
	own := lixDBServe(t, reqCtxA, jobA, lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	if own.Code != http.StatusCreated {
		t.Fatalf("control: A posting to its OWN job answered %d (body=%q), want 201 -- the comparison below then proves nothing", own.Code, own.Body.String())
	}

	cross := lixDBServe(t, reqCtxA, jobB, lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	absent := lixDBServe(t, reqCtxA, uuid.NewString(), lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))

	if cross.Code != http.StatusNotFound {
		t.Errorf("A posting to B's job answered %d (body=%q), want 404", cross.Code, cross.Body.String())
	}
	if cross.Code != absent.Code || cross.Body.String() != absent.Body.String() {
		t.Errorf("A posting to B's job answered %d %q; an unknown job answered %d %q -- a caller must not tell B's job exists",
			cross.Code, cross.Body.String(), absent.Code, absent.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobB); n != 0 {
		t.Errorf("%d correction row(s) landed on B's job, want 0", n)
	}
}

// The 999 cap's boundary, proved by actually reaching the store: handlers_lineitems_test.go
// proves 1000 is refused and 999 is not refused with that message, but only a real transaction
// shows 999 lands.
func TestRLS_LineItemsHandlerAcceptsExactly999Lines(t *testing.T) {
	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-999", "draft")

	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(999), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d for exactly 999 lines (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := lixLineRows(t, ctx, invoiceID); len(got) != 999 {
		t.Errorf("%d line_items row(s) landed, want 999 -- the cap must not be off by one", len(got))
	}
}

// --- AC 7: identity, then a suspended member ----------------------------------------------

func TestRLS_LineItemsSuspendedMemberIs403(t *testing.T) {
	ctx := t.Context()
	reqCtx, tenantID, documentID := rdTenant(t, ctx, "suspended")
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	jobID := uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1, $2, $3, $4, $5)`, jobID, tenantID, documentID, stExtractor, stExtractorVersion); err != nil {
		t.Fatalf("seed extraction job: %v", err)
	}

	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))

	hndAssert(t, w, http.StatusForbidden, hndErrBody(t, db.NotActiveMemberMessage))
}

// --- AC 9: the two state conflicts --------------------------------------------------------

func TestRLS_LineItemsWithNoInvoiceFiledFromTheDocumentIsRefused(t *testing.T) {
	ctx := t.Context()
	lixCommitControl(t, ctx)
	reqCtx, tenantID, _, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })

	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))

	hndAssert(t, w, http.StatusConflict, hndErrBody(t, corMsgNoInvoice))
	if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
		t.Errorf("%d correction row(s) landed with no invoice to apply them to, want 0", n)
	}
	if rows := cxCorrectionAudit(t, ctx, tenantID); len(rows) != 0 {
		t.Errorf("%d audit row(s) were written with no invoice, want 0", len(rows))
	}
}

func TestRLS_LineItemsOnANonFixableInvoiceIsRefused(t *testing.T) {
	ctx := t.Context()
	lixCommitControl(t, ctx)
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-NOFIX", "submitted")

	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(1), lixRefusingApplier(extraction.ErrInvoiceNotEditable), cxAuditor(nil))

	hndAssert(t, w, http.StatusConflict, hndErrBody(t, corMsgNotFixable))
	if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
		t.Errorf("%d correction row(s) landed on a refused write, want 0", n)
	}
	if got := lixLineRows(t, ctx, invoiceID); len(got) != 0 {
		t.Errorf("%d line_items row(s) landed on a refused write, want 0", len(got))
	}
}

// --- AC 8: a numeric the invoice store refuses ----------------------------------------------

func TestRLS_LineItemsValueRefusedByStoreIs400(t *testing.T) {
	ctx := t.Context()
	lixCommitControl(t, ctx)
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-REFUSED", "draft")

	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(1), lixRefusingApplier(extraction.ErrValueRefused), cxAuditor(nil))

	hndAssert(t, w, http.StatusBadRequest, hndErrBody(t, "the invoice refused this value"))
	if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
		t.Errorf("%d correction row(s) landed on a refused value, want 0", n)
	}
	if got := lixLineRows(t, ctx, invoiceID); len(got) != 0 {
		t.Errorf("%d line_items row(s) landed on a refused value, want 0", len(got))
	}
}

// --- AC 4, AC 1 (second clause): the row and the echoed response -------------------------

func TestRLS_LineItemsAppendsOneCorrectionRowAndEchoesTheStoredSet(t *testing.T) {
	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-ROW", "draft")

	body := `{"lines":[` +
		`{"description":"Widget","quantity":"2","unit_price":"10.00","line_total":"20.00"},` +
		`{"description":"Gadget","quantity":"1","unit_price":"5.00","line_total":"5.00"}]}`
	wantValue := `[{"description":"Widget","quantity":"2","unit_price":"10.00","line_total":"20.00"},` +
		`{"description":"Gadget","quantity":"1","unit_price":"5.00","line_total":"5.00"}]`

	w := lixDBServe(t, reqCtx, jobID, body, lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}

	row := cxCorrectionRow(t, ctx, jobID)
	if row.fieldName != "line_items" {
		t.Errorf("field_name = %q, want %q", row.fieldName, "line_items")
	}
	if row.method != "typed" {
		t.Errorf("method = %q, want %q", row.method, "typed")
	}
	if row.page != nil {
		t.Errorf("page = %v, want NULL -- a replace-all set carries no region", *row.page)
	}
	if row.value != wantValue {
		t.Errorf("value = %q, want the canonical JSON of the posted set, %q", row.value, wantValue)
	}

	var got struct {
		InvoiceID string                     `json:"invoice_id"`
		Lines     []extraction.LineItemInput `json:"lines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode the 201 body: %v", err)
	}
	if got.InvoiceID != invoiceID {
		t.Errorf("the 201 body names invoice_id %v, want %s", got.InvoiceID, invoiceID)
	}
	if len(got.Lines) != 2 || *got.Lines[0].Description != "Widget" || *got.Lines[1].Description != "Gadget" {
		t.Errorf("the 201 body's lines = %+v, want the two posted lines in order", got.Lines)
	}
}

// --- AC 5: the audit row, and the standing fact it is not invoice-scoped readable ----------

func TestRLS_LineItemsAuditRowNamesTheInvoiceAndStaysUnresolvedByInvoiceId(t *testing.T) {
	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-AUDIT", "draft")

	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}

	rows := cxCorrectionAudit(t, ctx, tenantID)
	if len(rows) != 1 {
		t.Fatalf("%d %s audit row(s), want exactly 1", len(rows), cxEvent)
	}
	row := rows[0]
	if row.payload["invoice_id"] != invoiceID {
		t.Errorf("the audit payload names invoice_id %v, want %s", row.payload["invoice_id"], invoiceID)
	}
	if row.payload["field"] != "line_items" {
		t.Errorf("the audit payload names field %v, want %q", row.payload["field"], "line_items")
	}
	if row.payload["method"] != "typed" {
		t.Errorf("the audit payload names method %v, want %q", row.payload["method"], "typed")
	}

	// The standing fact: extraction.field_corrected is absent from the invoice_id generated
	// column's CASE list, so an invoice-scoped audit read returns zero rows by construction.
	// Pinned here so a future migration widening that CASE list is caught.
	if got := lixAuditGeneratedInvoiceID(t, ctx, tenantID); got != nil {
		t.Errorf("audit_log.invoice_id (generated) = %v, want NULL for this event", *got)
	}
}

// --- AC 1, AC 2: replace-all, not append; array order becomes line_no --------------------

func TestRLS_LineItemsReplacesTheWholeSet(t *testing.T) {
	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-REPLACE", "draft")
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO line_items (invoice_id, tenant_id, line_no, description) VALUES
		 ($1, $2, 1, 'OLD-A'), ($1, $2, 2, 'OLD-B')`, invoiceID, tenantID); err != nil {
		t.Fatalf("seed pre-existing line_items: %v", err)
	}

	body := `{"lines":[` +
		`{"description":"NEW-1","quantity":"1","unit_price":"1.00","line_total":"1.00"},` +
		`{"description":"NEW-2","quantity":"1","unit_price":"1.00","line_total":"1.00"},` +
		`{"description":"NEW-3","quantity":"1","unit_price":"1.00","line_total":"1.00"}]}`

	w := lixDBServe(t, reqCtx, jobID, body, lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	got := lixLineRows(t, ctx, invoiceID)
	want := []string{"NEW-1", "NEW-2", "NEW-3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("line_items after the write = %v, want %v -- the old pair must be gone and array order must become line_no 1,2,3", got, want)
	}
}

func TestRLS_LineItemsEmptyArrayRemovesEveryLine(t *testing.T) {
	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-EMPTY", "draft")
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO line_items (invoice_id, tenant_id, line_no, description) VALUES ($1, $2, 1, 'OLD-A')`,
		invoiceID, tenantID); err != nil {
		t.Fatalf("seed pre-existing line_items: %v", err)
	}
	if got := lixLineRows(t, ctx, invoiceID); len(got) == 0 {
		t.Fatalf("control: the seeded line did not land, so the removal assertion below is vacuous")
	}

	w := lixDBServe(t, reqCtx, jobID, `{"lines":[]}`, lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := lixLineRows(t, ctx, invoiceID); len(got) != 0 {
		t.Errorf("%d line_items row(s) survived lines: [], want 0", len(got))
	}
	if !strings.Contains(w.Body.String(), `"lines":[]`) {
		t.Errorf("the 201 body = %q, want it to contain \"lines\":[]", w.Body.String())
	}
}

// --- AC 10: demotion ------------------------------------------------------------------------

func TestRLS_LineItemsWriteDemotesAValidatedInvoice(t *testing.T) {
	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR13-03-DEMOTE", "validated")

	if got := lixInvoiceStatus(t, ctx, invoiceID); got != "validated" {
		t.Fatalf("control: the seeded invoice is %q before the POST, want %q -- the after-assertion below is vacuous otherwise", got, "validated")
	}

	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(1), lixApplier(lixApplyOpts{write: true, demote: true}), cxAuditor(nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := lixInvoiceStatus(t, ctx, invoiceID); got != "draft" {
		t.Errorf("invoice status after the write = %q, want %q -- saving lines on a validated invoice must demote it", got, "draft")
	}
}
