// handlers_correction_db_test.go: what POST /v1/extractions/{id}/fields/{name}/corrections
// commits, and what it refuses. Shares store_db_test.go's harness (stRequire/stTenant) and
// reader_db_test.go's rdTenant, so this file adds no second skip site.
//
// The invoice seam is the test's own closure, not internal/invoice: deps_test.go fences this
// package off everything outside internal/platform/* in BOTH scans, test imports included. What
// the closure cannot prove -- the fix-loop rules the shared edit path buys -- is proved against
// EditBySourceDocumentTx in internal/invoice/edit_by_source_document_test.go.
//
// Helpers use a cx* prefix.
package extraction_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
)

// cxEvent is the event name cmd/submission spells for production. Spelled here because the
// assertion needs a value to compare against; the rule keeping it out of internal/extraction
// covers non-test source only (TestExtractionPackage_DeclaresNoEventName).
const cxEvent = "extraction.field_corrected"

// The 201 body's complete key set, sorted. Asserted as a WHOLE: an added omitempty drops a field
// from the wire without touching a single value assertion.
var cxResponseKeys = []string{"created_at", "field_name", "id", "invoice_id", "method", "region", "value"}

// cxJob seeds a tenant with an ACTIVE membership, its document and one extraction job, and
// returns the request context that names the caller.
func cxJob(t *testing.T, ctx context.Context) (reqCtx context.Context, tenantID, documentID, jobID string) {
	t.Helper()
	reqCtx, tenantID, documentID = rdTenant(t, ctx, "active")
	jobID = uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1, $2, $3, $4, $5)`,
		jobID, tenantID, documentID, stExtractor, stExtractorVersion); err != nil {
		t.Fatalf("seed extraction job: %v", err)
	}
	return reqCtx, tenantID, documentID, jobID
}

func cxEntity(t *testing.T, ctx context.Context, tenantID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO business_entities (id, tenant_id, name) VALUES ($1, $2, $3)`,
		id, tenantID, "extr-12 "+id[:8]); err != nil {
		t.Fatalf("seed business entity: %v", err)
	}
	return id
}

// cxInvoice files one invoice from documentID. total starts at cxTotalBefore so an unchanged
// column is distinguishable from a NULL one.
func cxInvoice(t *testing.T, ctx context.Context, tenantID, entityID, documentID, number, status string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO invoices (id, tenant_id, entity_id, invoice_number, status, total, source_document_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, tenantID, entityID, number, status, cxTotalBefore, documentID); err != nil {
		t.Fatalf("seed invoice %q at status %q: %v", number, status, err)
	}
	return id
}

const (
	cxTotalBefore = "100.00"
	cxTotalAfter  = "1500.00"
)

// cxApplier is the invoice seam: it resolves the invoice filed from documentID on the CALLER's
// transaction, optionally writes the value, then reports failAfter. Writing before failing is
// what makes the rollback arm mean something -- a seam that wrote nothing would satisfy the
// unchanged-column assertion without any rollback at all.
func cxApplier(write bool, failAfter error) extraction.ApplyFieldToInvoice {
	return func(ctx context.Context, tx pgx.Tx, documentID, field, value string) (string, error) {
		var id string
		if err := tx.QueryRow(ctx,
			`SELECT id FROM invoices WHERE source_document_id = $1`, documentID).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", extraction.ErrNoInvoiceForDocument
			}
			return "", err
		}
		if write {
			if _, err := tx.Exec(ctx,
				`UPDATE invoices SET total = $1::text::numeric WHERE id = $2`, value, id); err != nil {
				return "", err
			}
		}
		if failAfter != nil {
			return "", failAfter
		}
		return id, nil
	}
}

// cxRefusingApplier reports one domain sentinel without touching a row.
func cxRefusingApplier(err error) extraction.ApplyFieldToInvoice {
	return func(context.Context, pgx.Tx, string, string, string) (string, error) { return "", err }
}

// cxAuditor writes the same three columns internal/audit.Record writes, through the tx the
// handler hands it -- so a recorder called outside the transaction writes nothing and the counts
// below stay at zero.
func cxAuditor(fail error) extraction.RecordFieldCorrected {
	return func(ctx context.Context, tx pgx.Tx, subject string, c extraction.FieldCorrection) error {
		if fail != nil {
			return fail
		}
		if tx == nil {
			return errors.New("audit: the recorder was handed no transaction, so the row cannot share the write's fate")
		}
		payload, err := json.Marshal(map[string]string{
			"invoice_id": c.InvoiceID,
			"field":      c.FieldName,
			"method":     string(c.Method),
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO audit_log (actor, event, payload) VALUES ($1, $2, $3)`,
			subject, cxEvent, string(payload))
		return err
	}
}

// cxServe drives the handler once over the real app pool.
func cxServe(t *testing.T, ctx context.Context, jobID, field, body string,
	apply extraction.ApplyFieldToInvoice, record extraction.RecordFieldCorrected) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost,
		"/v1/extractions/"+jobID+"/fields/"+field+"/corrections", strings.NewReader(body))
	r.SetPathValue(corPathID, jobID)
	r.SetPathValue(corPathName, field)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	extraction.CorrectionHandler(stRequire(t).app, apply, record, nil)(w, r)
	return w
}

// cxCorrectionRows counts as the SUPERUSER: an app-pool count is RLS-filtered and would read the
// same whether or not a row was written.
func cxCorrectionRows(t *testing.T, ctx context.Context, jobID string) int {
	t.Helper()
	var n int
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT count(*) FROM extraction_field_corrections WHERE extraction_job_id = $1`, jobID).Scan(&n); err != nil {
		t.Fatalf("count corrections for job %s: %v", jobID, err)
	}
	return n
}

// cxCorrectionValue reads the one correction row a job holds, as the superuser.
func cxCorrectionValue(t *testing.T, ctx context.Context, jobID string) string {
	t.Helper()
	var out string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT value FROM extraction_field_corrections WHERE extraction_job_id = $1`, jobID).Scan(&out); err != nil {
		t.Fatalf("read the correction value for job %s: %v", jobID, err)
	}
	return out
}

func cxTotal(t *testing.T, ctx context.Context, invoiceID string) string {
	t.Helper()
	var out *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT total::text FROM invoices WHERE id = $1`, invoiceID).Scan(&out); err != nil {
		t.Fatalf("read invoices.total for %s: %v", invoiceID, err)
	}
	if out == nil {
		return "<null>"
	}
	return *out
}

// cxCorrectionAudit returns the correction audit rows one tenant holds, with the entity the
// write-time trigger resolved.
type cxAuditRow struct {
	actor    string
	entityID *string
	payload  map[string]any
}

func cxCorrectionAudit(t *testing.T, ctx context.Context, tenantID string) []cxAuditRow {
	t.Helper()
	rows, err := stRequire(t).super.Query(ctx,
		`SELECT actor, entity_id::text, payload FROM audit_log
		  WHERE tenant_id = $1 AND event = $2 ORDER BY id`, tenantID, cxEvent)
	if err != nil {
		t.Fatalf("read %s rows for tenant %s: %v", cxEvent, tenantID, err)
	}
	defer rows.Close()

	var out []cxAuditRow
	for rows.Next() {
		var r cxAuditRow
		var raw []byte
		if err := rows.Scan(&r.actor, &r.entityID, &raw); err != nil {
			t.Fatalf("scan %s row: %v", cxEvent, err)
		}
		if err := json.Unmarshal(raw, &r.payload); err != nil {
			t.Fatalf("decode %s payload %s: %v", cxEvent, raw, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s rows for tenant %s: %v", cxEvent, tenantID, err)
	}
	return out
}

func cxJSONKeys(t *testing.T, body string) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode the response body %q: %v", body, err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cxBody is one well-formed request body for a typed correction.
func cxBody(value string) string { return corBody(value, "typed", "") }

// cxCommitControl seeds its own fixture and drives one UNIMPEDED correction through. Every
// refusal case below asserts zero rows and an unchanged column, and those assertions are
// satisfied by a handler that writes nothing ever -- this is what shows they can fail.
func cxCommitControl(t *testing.T, ctx context.Context) {
	t.Helper()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-04-CONTROL", "draft")

	w := cxServe(t, reqCtx, jobID, "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("control: an unimpeded correction answered %d (body=%q), want 201 -- the zero-row and unchanged-column assertions in this test are then vacuous",
			w.Code, w.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 1 {
		t.Fatalf("control: an unimpeded correction left %d correction row(s), want 1 -- a zero-row assertion elsewhere proves nothing", n)
	}
	if got := cxTotal(t, ctx, invoiceID); got != cxTotalAfter {
		t.Fatalf("control: an unimpeded correction left invoices.total = %s, want %s -- an unchanged-column assertion elsewhere proves nothing", got, cxTotalAfter)
	}
	if rows := cxCorrectionAudit(t, ctx, tenantID); len(rows) != 1 {
		t.Fatalf("control: an unimpeded correction left %d %s audit row(s), want 1", len(rows), cxEvent)
	}
}

// --- AC 1: both writes, or neither ------------------------------------------------------

// Two arms, because the transaction has two places to break. The first breaks inside the invoice
// seam AFTER it wrote; the second breaks in the audit seam AFTER the correction row was
// appended. A handler that opened a second transaction for either write passes neither.
func TestRLS_CorrectionWritesTheRowAndTheInvoiceOrNeither(t *testing.T) {
	cxCommitControl(t, t.Context())
	boom := errors.New("cx: induced failure")

	for _, tc := range []struct {
		name   string
		apply  extraction.ApplyFieldToInvoice
		record extraction.RecordFieldCorrected
	}{
		{"the invoice write fails after touching the row", cxApplier(true, boom), cxAuditor(nil)},
		{"the audit write fails after the correction row", cxApplier(true, nil), cxAuditor(boom)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
			t.Cleanup(func() { rdaPurge(t, tenantID) })
			entityID := cxEntity(t, ctx, tenantID)
			invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-04-AC1", "draft")

			w := cxServe(t, reqCtx, jobID, "total", cxBody(cxTotalAfter), tc.apply, tc.record)

			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want %d for an induced failure (body=%q)", w.Code, http.StatusInternalServerError, w.Body.String())
			}
			if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
				t.Errorf("%d correction row(s) survived a failed write, want 0", n)
			}
			if got := cxTotal(t, ctx, invoiceID); got != cxTotalBefore {
				t.Errorf("invoices.total = %s after a failed write, want the unchanged %s -- the invoice write did not share the correction's fate", got, cxTotalBefore)
			}
			if rows := cxCorrectionAudit(t, ctx, tenantID); len(rows) != 0 {
				t.Errorf("%d %s audit row(s) survived a failed write, want 0", len(rows), cxEvent)
			}
		})
	}
}

// --- AC 2: the payload key, and the entity the trigger resolves from it -----------------

// The shipped resolver already dispatches this event on payload->>'invoice_id'. What is not yet
// proved is that the HANDLER hands the seam the invoice it actually reached, spelled under that
// key -- a NULL entity_id is a positive claim that the correction was firm-wide.
func TestRLS_CorrectionAuditRowCarriesInvoiceIdAndResolvesToTheEntity(t *testing.T) {
	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-04-AC2", "draft")

	w := cxServe(t, reqCtx, jobID, "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := cxJSONKeys(t, w.Body.String()); !reflect.DeepEqual(got, cxResponseKeys) {
		t.Errorf("the 201 body carries %v, want exactly %v", got, cxResponseKeys)
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode the 201 body: %v", err)
	}
	if got["invoice_id"] != invoiceID {
		t.Errorf("the 201 body names invoice_id %v, want the invoice filed from the document, %s", got["invoice_id"], invoiceID)
	}
	if got["value"] != cxTotalAfter {
		t.Errorf("the 201 body carries value %v, want %q", got["value"], cxTotalAfter)
	}
	if got["region"] != nil {
		t.Errorf("the 201 body carries region %v for a typed correction, want an explicit null", got["region"])
	}

	if stored := cxTotal(t, ctx, invoiceID); stored != cxTotalAfter {
		t.Errorf("invoices.total = %s, want %s -- the register and the correction row must agree", stored, cxTotalAfter)
	}

	rows := cxCorrectionAudit(t, ctx, tenantID)
	if len(rows) != 1 {
		t.Fatalf("the correction wrote %d %s audit row(s), want exactly 1", len(rows), cxEvent)
	}
	row := rows[0]
	if row.payload["invoice_id"] != invoiceID {
		t.Errorf("the audit payload names invoice_id %v, want %s -- the resolver dispatches on this exact key", row.payload["invoice_id"], invoiceID)
	}
	if row.entityID == nil {
		t.Errorf("the audit row resolved entity_id NULL, which claims the correction was firm-wide; want %s", entityID)
	} else if *row.entityID != entityID {
		t.Errorf("the audit row resolved entity_id %s, want %s", *row.entityID, entityID)
	}
	if row.actor != rdMemberSubject {
		t.Errorf("the audit row names actor %q, want the caller's own subject %q", row.actor, rdMemberSubject)
	}
}

// --- AC 5: another tenant's job reads exactly like an absent one -------------------------

func TestRLS_CorrectionCrossTenantIsIndistinguishableFromAbsent(t *testing.T) {
	ctx := t.Context()
	reqCtxA, tenantA, documentA, jobA := cxJob(t, ctx)
	_, tenantB, _, jobB := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantA, tenantB) })

	entityA := cxEntity(t, ctx, tenantA)
	cxInvoice(t, ctx, tenantA, entityA, documentA, "EXTR12-04-AC5", "draft")

	// The positive control: A's own job is answerable, so the two refusals below are refusing
	// the job rather than refusing every request.
	own := cxServe(t, reqCtxA, jobA, "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil))
	if own.Code != http.StatusCreated {
		t.Fatalf("control: A posting to its OWN job answered %d (body=%q), want 201 -- the comparison below then proves nothing", own.Code, own.Body.String())
	}

	cross := cxServe(t, reqCtxA, jobB, "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil))
	absent := cxServe(t, reqCtxA, uuid.NewString(), "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil))

	if cross.Code != http.StatusNotFound {
		t.Errorf("A posting to B's job answered %d (body=%q), want 404", cross.Code, cross.Body.String())
	}
	if cross.Code != absent.Code || cross.Body.String() != absent.Body.String() {
		t.Errorf("A posting to B's job answered %d %q; an unknown job answered %d %q -- a caller must not be able to tell that B's job exists",
			cross.Code, cross.Body.String(), absent.Code, absent.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobB); n != 0 {
		t.Errorf("%d correction row(s) landed on B's job, want 0", n)
	}
}

// --- AC 6: the two state conflicts ------------------------------------------------------

// An invoice past the fixable states cannot take a correction: the value would diverge from what
// the APP already received.
func TestRLS_CorrectionOnANonFixableInvoiceIsRefused(t *testing.T) {
	ctx := t.Context()
	cxCommitControl(t, ctx)
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-04-AC6a", "submitted")

	w := cxServe(t, reqCtx, jobID, "total", cxBody(cxTotalAfter),
		cxRefusingApplier(extraction.ErrInvoiceNotEditable), cxAuditor(nil))

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d for an invoice past the fixable states (body=%q)", w.Code, http.StatusConflict, w.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
		t.Errorf("%d correction row(s) landed on a refused correction, want 0", n)
	}
	if got := cxTotal(t, ctx, invoiceID); got != cxTotalBefore {
		t.Errorf("invoices.total = %s, want the unchanged %s", got, cxTotalBefore)
	}
}

// The quarantine state is reachable by design: ImportDocument quarantines instead of creating
// when invoice_number is blank or duplicate, leaving a job whose document has no invoice.
func TestRLS_CorrectionWithNoInvoiceFiledFromTheDocumentIsRefused(t *testing.T) {
	ctx := t.Context()
	cxCommitControl(t, ctx)
	reqCtx, tenantID, _, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })

	w := cxServe(t, reqCtx, jobID, "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil))

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d for a document with no invoice (body=%q)", w.Code, http.StatusConflict, w.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
		t.Errorf("%d correction row(s) landed with no invoice to apply them to, want 0 -- a row with a NULL entity_id claims the action was firm-wide", n)
	}
	if rows := cxCorrectionAudit(t, ctx, tenantID); len(rows) != 0 {
		t.Errorf("%d %s audit row(s) were written with no invoice, want 0", len(rows), cxEvent)
	}
}

// --- issue_date, one spelling -----------------------------------------------------------

// The handler normalises issue_date before either write: UpdateInput.IssueDate is a *time.Time,
// so an unreadable date cannot reach the invoice as text. Both writes take the NORMALISED
// reading, so the correction row and the register cannot disagree about which day it was.
// handlers_correction_test.go proves an ISO date is not refused; only a real transaction can
// show what the seam was handed.
func TestRLS_CorrectionNormalisesIssueDateToOneSpelling(t *testing.T) {
	for _, tc := range []struct {
		sent string
		want string
	}{
		{"2026-03-01", "2026-03-01"},
		{"01 Mar 2026", "2026-03-01"},
		{"Mar 01, 2026", "2026-03-01"},
	} {
		t.Run(tc.sent, func(t *testing.T) {
			ctx := t.Context()
			reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
			t.Cleanup(func() { rdaPurge(t, tenantID) })
			entityID := cxEntity(t, ctx, tenantID)
			cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-04-DATE-"+jobID[:8], "draft")

			var gotField, gotValue string
			apply := func(ctx context.Context, tx pgx.Tx, documentID, field, value string) (string, error) {
				gotField, gotValue = field, value
				return cxApplier(false, nil)(ctx, tx, documentID, field, value)
			}

			w := cxServe(t, reqCtx, jobID, "issue_date", cxBody(tc.sent), apply, cxAuditor(nil))

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
			}
			if gotField != "issue_date" || gotValue != tc.want {
				t.Errorf("the seam was handed (%q, %q), want (%q, %q)", gotField, gotValue, "issue_date", tc.want)
			}
			if stored := cxCorrectionValue(t, ctx, jobID); stored != tc.want {
				t.Errorf("the correction row carries value %q, want %q -- the row and the register must carry one spelling", stored, tc.want)
			}
		})
	}

	// An ambiguous numeric date has two readings and is refused rather than guessed at: the
	// issue date drives the tax period.
	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-04-DATE-AMB", "draft")

	w := cxServe(t, reqCtx, jobID, "issue_date", cxBody("03/04/2026"), cxApplier(false, nil), cxAuditor(nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for a date that reads two ways (body=%q)", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 0 {
		t.Errorf("%d correction row(s) landed for an ambiguous date, want 0", n)
	}
}

// --- the confirming correction ----------------------------------------------------------

// A correction whose value equals what is already stored, and an undone correction reverting to
// the extractor's own reading, are both real human actions. The audit seam is the handler's own
// step, after the invoice seam returns, so the invoice side no-ops while the record still lands.
func TestRLS_ConfirmingCorrectionStillWritesItsRowAndAuditEvent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
	}{
		{"the same value typed again", "typed"},
		{"undone, reverting to the extractor reading", "undone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
			t.Cleanup(func() { rdaPurge(t, tenantID) })
			entityID := cxEntity(t, ctx, tenantID)
			invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-04-CONF", "draft")

			// write=false: the seam resolves the invoice and changes nothing, which is what the
			// shared edit path does when the value is already stored.
			body := corBody(cxTotalBefore, tc.method, "")
			w := cxServe(t, reqCtx, jobID, "total", body, cxApplier(false, nil), cxAuditor(nil))

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d for a correction that changed nothing (body=%q)", w.Code, http.StatusCreated, w.Body.String())
			}
			if n := cxCorrectionRows(t, ctx, jobID); n != 1 {
				t.Errorf("%d correction row(s) after a confirming correction, want 1 -- the record is of the human action, not of the value change", n)
			}
			rows := cxCorrectionAudit(t, ctx, tenantID)
			if len(rows) != 1 {
				t.Fatalf("%d %s audit row(s) after a confirming correction, want 1", len(rows), cxEvent)
			}
			if rows[0].payload["method"] != tc.method {
				t.Errorf("the audit payload names method %v, want %q", rows[0].payload["method"], tc.method)
			}
			if got := cxTotal(t, ctx, invoiceID); got != cxTotalBefore {
				t.Errorf("invoices.total = %s, want the unchanged %s", got, cxTotalBefore)
			}
			if _, ok := rows[0].payload["value"]; ok {
				t.Errorf("the audit payload carries the corrected value %v; audit_log is append-only and business content does not belong in it", rows[0].payload["value"])
			}
		})
	}
}
