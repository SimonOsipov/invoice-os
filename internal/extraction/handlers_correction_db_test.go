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
	"strconv"
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
	return func(ctx context.Context, tx pgx.Tx, documentID, field string, value *string, _ extraction.CorrectionMethod) (string, error) {
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
	return func(context.Context, pgx.Tx, string, string, *string, extraction.CorrectionMethod) (string, error) {
		return "", err
	}
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

// cxServe drives the handler once over the real app pool. The variadic learning recorder
// defaults to one that writes nothing, so only a case that names it can see an anchor.learned
// row; laLearnedAuditor is what writes a real one.
func cxServe(t *testing.T, ctx context.Context, jobID, field, body string,
	apply extraction.ApplyFieldToInvoice, record extraction.RecordFieldCorrected,
	learned ...extraction.RecordAnchorLearned) *httptest.ResponseRecorder {
	t.Helper()
	recordLearned := extraction.RecordAnchorLearned(func(context.Context, pgx.Tx, string, extraction.AnchorLearned) error { return nil })
	if len(learned) == 1 {
		recordLearned = learned[0]
	} else if len(learned) > 1 {
		t.Fatalf("cxServe was handed %d learning recorders, want at most 1", len(learned))
	}
	r := httptest.NewRequest(http.MethodPost,
		"/v1/extractions/"+jobID+"/fields/"+field+"/corrections", strings.NewReader(body))
	r.SetPathValue(corPathID, jobID)
	r.SetPathValue(corPathName, field)
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	extraction.CorrectionHandler(stRequire(t).app, apply, record, recordLearned, nil)(w, r)
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

// cxRow is one stored correction, read as the SUPERUSER: an app-pool read is RLS-scoped, and a
// NULL box reads the same as a row that is not there.
type cxRow struct {
	fieldName, value, method, actor string
	page                            *int
	x0, y0, x1, y1                  *float64
	anchor                          *string
}

func cxCorrectionRow(t *testing.T, ctx context.Context, jobID string) cxRow {
	t.Helper()
	var r cxRow
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT field_name, value, method, actor, page, bbox_x0, bbox_y0, bbox_x1, bbox_y1, anchor_label
		   FROM extraction_field_corrections WHERE extraction_job_id = $1`, jobID).
		Scan(&r.fieldName, &r.value, &r.method, &r.actor,
			&r.page, &r.x0, &r.y0, &r.x1, &r.y1, &r.anchor); err != nil {
		t.Fatalf("read the correction row for job %s: %v", jobID, err)
	}
	return r
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

	hndAssert(t, w, http.StatusConflict, hndErrBody(t, corMsgNotFixable))
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

	hndAssert(t, w, http.StatusConflict, hndErrBody(t, corMsgNoInvoice))
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
			apply := func(ctx context.Context, tx pgx.Tx, documentID, field string, value *string, m extraction.CorrectionMethod) (string, error) {
				gotField, gotValue = field, cxShowValue(value)
				return cxApplier(false, nil)(ctx, tx, documentID, field, value, m)
			}

			w := cxServe(t, reqCtx, jobID, "issue_date", cxBody(tc.sent), apply, cxAuditor(nil))

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
			}
			if gotField != "issue_date" || gotValue != `"`+tc.want+`"` {
				t.Errorf("the seam was handed (%q, %s), want (%q, %q)", gotField, gotValue, "issue_date", tc.want)
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

// --- what a pointed correction stores ----------------------------------------------------

// The region the reviewer drew, the anchor label and the caller's own subject, read off the
// STORED row. The 201 body echoes the request region, so it agrees with the request whatever
// regionFromWire did to the box on the way to the database; only this read can say the stored
// box is the one that was sent. Four distinct coordinates and page 2, so a transposed pair or a
// hardcoded page fails here rather than satisfying the region CHECK set anyway.
func TestRLS_PointedCorrectionStoresTheBoxTheAnchorAndTheActor(t *testing.T) {
	const (
		cxPage   = 2
		cxX0     = 0.11
		cxY0     = 0.37
		cxX1     = 0.62
		cxY1     = 0.44
		cxAnchor = "Grand Total"
	)
	region := `"region":{"page":2,"x0":0.11,"y0":0.37,"x1":0.62,"y1":0.44},"anchor_label":"  ` + cxAnchor + `  "`

	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-04-POINTED", "draft")

	w := cxServe(t, reqCtx, jobID, "total", corBody(cxTotalAfter, "pointed", region),
		cxApplier(true, nil), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}

	row := cxCorrectionRow(t, ctx, jobID)
	if row.fieldName != "total" || row.value != cxTotalAfter || row.method != "pointed" {
		t.Errorf("the stored row is (%q, %q, %q), want (%q, %q, %q)",
			row.fieldName, row.value, row.method, "total", cxTotalAfter, "pointed")
	}
	// A row naming the wrong actor is worse than no row: this table is the append-only record of
	// who changed what.
	if row.actor != rdMemberSubject {
		t.Errorf("the stored row names actor %q, want the caller's own subject %q", row.actor, rdMemberSubject)
	}
	if row.anchor == nil || *row.anchor != cxAnchor {
		t.Errorf("the stored row carries anchor_label %v, want %q trimmed of its surrounding space", row.anchor, cxAnchor)
	}
	if row.page == nil {
		t.Fatalf("the stored row carries no page; a pointed correction always does")
	}
	if *row.page != cxPage {
		t.Errorf("the stored row is on page %d, want %d", *row.page, cxPage)
	}
	for _, c := range []struct {
		name string
		got  *float64
		want float64
	}{
		{"bbox_x0", row.x0, cxX0}, {"bbox_y0", row.y0, cxY0},
		{"bbox_x1", row.x1, cxX1}, {"bbox_y1", row.y1, cxY1},
	} {
		if c.got == nil {
			t.Errorf("the stored row carries no %s", c.name)
			continue
		}
		if *c.got != c.want {
			t.Errorf("the stored row carries %s = %v, want %v -- the box points somewhere the reviewer did not click", c.name, *c.got, c.want)
		}
	}

	// The 201 body and the stored row must describe ONE box.
	var body struct {
		Region *extraction.ExtractionRegion `json:"region"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode the 201 body: %v", err)
	}
	if body.Region == nil {
		t.Fatalf("the 201 body carries region null for a pointed correction")
	}
	if want := (extraction.ExtractionRegion{Page: cxPage, X0: cxX0, Y0: cxY0, X1: cxX1, Y1: cxY1}); *body.Region != want {
		t.Errorf("the 201 body carries region %+v, want %+v", *body.Region, want)
	}

	// The negative half, on its own job: a typed correction stores no box and no anchor, so the
	// assertions above are reading what was SENT rather than a box the handler always writes.
	reqCtx2, tenant2, document2, job2 := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenant2) })
	entity2 := cxEntity(t, ctx, tenant2)
	cxInvoice(t, ctx, tenant2, entity2, document2, "EXTR12-04-TYPED", "draft")

	w2 := cxServe(t, reqCtx2, job2, "total", cxBody(cxTotalAfter), cxApplier(true, nil), cxAuditor(nil))
	if w2.Code != http.StatusCreated {
		t.Fatalf("the typed control answered %d (body=%q), want 201", w2.Code, w2.Body.String())
	}
	typed := cxCorrectionRow(t, ctx, job2)
	if typed.page != nil || typed.x0 != nil || typed.y0 != nil || typed.x1 != nil || typed.y1 != nil {
		t.Errorf("a typed correction stored a box (page %v, x0 %v); only a pointed one carries one", cxShowInt(typed.page), cxShow(typed.x0))
	}
	if typed.anchor != nil {
		t.Errorf("a typed correction stored anchor_label %q; an empty label binds as NULL", *typed.anchor)
	}
}

func cxShow(p *float64) string {
	if p == nil {
		return "<null>"
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

func cxShowInt(p *int) string {
	if p == nil {
		return "<null>"
	}
	return strconv.Itoa(*p)
}

// --- the method the invoice seam is handed ----------------------------------------------

// cxSeamCall is what the handler handed ApplyFieldToInvoice on the last call.
type cxSeamCall struct {
	calls  int
	field  string
	value  *string
	method extraction.CorrectionMethod
}

// cxRecorder records the seam's arguments and then defers to cxApplier, so the invoice write
// still happens (or not) exactly as it would without the recorder.
func cxRecorder(seen *cxSeamCall, write bool) extraction.ApplyFieldToInvoice {
	return func(ctx context.Context, tx pgx.Tx, documentID, field string, value *string, method extraction.CorrectionMethod) (string, error) {
		seen.calls++
		seen.field, seen.value, seen.method = field, value, method
		return cxApplier(write, nil)(ctx, tx, documentID, field, value, method)
	}
}

// The seam is handed the METHOD the caller posted, not the value alone. An undo and a typed
// correction write different things to the invoice, and before this parameter existed nothing
// downstream could tell the two apart.
func TestRLS_CorrectionHandsTheInvoiceSeamThePostedMethod(t *testing.T) {
	region := `"region":{"page":2,"x0":0.11,"y0":0.37,"x1":0.62,"y1":0.44}`

	for _, tc := range []struct {
		method string
		extra  string
		want   extraction.CorrectionMethod
	}{
		{"typed", "", extraction.MethodTyped},
		{"chosen", "", extraction.MethodChosen},
		{"pointed", region, extraction.MethodPointed},
		{"undone", "", extraction.MethodUndone},
	} {
		t.Run(tc.method, func(t *testing.T) {
			ctx := t.Context()
			reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
			t.Cleanup(func() { rdaPurge(t, tenantID) })
			entityID := cxEntity(t, ctx, tenantID)
			cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-07-METHOD-"+jobID[:8], "draft")

			var seen cxSeamCall
			w := cxServe(t, reqCtx, jobID, "total", corBody(cxTotalAfter, tc.method, tc.extra),
				cxRecorder(&seen, false), cxAuditor(nil))

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
			}
			if seen.calls != 1 {
				t.Fatalf("the invoice seam ran %d time(s), want 1 -- the method assertion below is vacuous", seen.calls)
			}
			if seen.method != tc.want {
				t.Errorf("the invoice seam was handed method %q for a %q correction, want %q -- nothing downstream can tell an undo from a typed correction",
					seen.method, tc.method, tc.want)
			}
			if seen.field != "total" {
				t.Errorf("the invoice seam was handed field %q, want %q", seen.field, "total")
			}
		})
	}
}

// --- EXTR-12-07: what an undo writes to the invoice --------------------------------------

const (
	// The extractor's own rank-0 reading for total, distinct from cxTotalBefore (what the
	// invoice starts at) and cxTotalAfter (what the caller posts), so no shortcut passes.
	cxReadingTotal = "777.00"
	// The rank-1 alternative, the same field's reading on ANOTHER job, and a SECOND field's
	// reading on this one. A lookup that forgets candidate_rank, extraction_job_id or
	// field_name lands on one of these instead.
	cxAltTotal      = "888.00"
	cxOtherJobTotal = "999.00"
	cxReadingSub    = "666.00"
)

// cxReading seeds one extraction_field_results row. A nil value is the shape mockDefaultResult
// gives buyer_tin and vat -- a field the extractor never read.
func cxReading(t *testing.T, ctx context.Context, tenantID, jobID, field string, rank int, value *string) {
	t.Helper()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_field_results (tenant_id, extraction_job_id, field_name, value, candidate_rank)
		 VALUES ($1, $2, $3, $4, $5)`,
		tenantID, jobID, field, value, rank); err != nil {
		t.Fatalf("seed %s reading rank %d for job %s: %v", field, rank, jobID, err)
	}
}

func cxStr(s string) *string { return &s }

// cxJobIn seeds a second extraction job inside a tenant that already has one. No UNIQUE over
// (tenant_id, document_id), so the document is shared deliberately.
func cxJobIn(t *testing.T, ctx context.Context, tenantID, documentID string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := stRequire(t).super.Exec(ctx,
		`INSERT INTO extraction_jobs (id, tenant_id, document_id, extractor, extractor_version)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, tenantID, documentID, stExtractor, stExtractorVersion); err != nil {
		t.Fatalf("seed a second extraction job: %v", err)
	}
	return id
}

// An undo is a full reset to what the extractor read, and the register has to agree with the
// screen. mergeCorrections already resets the SCREEN to the rank-0 reading and ignores the
// undone row's own value (reader.go), so applying the POSTed value to the invoice leaves the
// two disagreeing: after A -> typed B -> typed C -> Undo the screen says A and the invoice
// holds C. The typed arm is the control -- an applier that ALWAYS wrote the reading would
// satisfy the undo arm and fail here.
func TestRLS_UndoAppliesTheExtractorsReadingNotThePostedValue(t *testing.T) {
	for _, tc := range []struct {
		method string
		want   string
	}{
		{"undone", cxReadingTotal},
		{"typed", cxTotalAfter},
	} {
		t.Run(tc.method, func(t *testing.T) {
			ctx := t.Context()
			reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
			t.Cleanup(func() { rdaPurge(t, tenantID) })
			entityID := cxEntity(t, ctx, tenantID)
			invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-07-UNDO-"+jobID[:8], "draft")

			cxReading(t, ctx, tenantID, jobID, "total", 0, cxStr(cxReadingTotal))
			cxReading(t, ctx, tenantID, jobID, "total", 1, cxStr(cxAltTotal))
			cxReading(t, ctx, tenantID, jobID, "subtotal", 0, cxStr(cxReadingSub))
			cxReading(t, ctx, tenantID, cxJobIn(t, ctx, tenantID, documentID), "total", 0, cxStr(cxOtherJobTotal))

			var seen cxSeamCall
			w := cxServe(t, reqCtx, jobID, "total", corBody(cxTotalAfter, tc.method, ""),
				cxRecorder(&seen, true), cxAuditor(nil))

			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
			}
			if seen.calls != 1 {
				t.Fatalf("the invoice seam ran %d time(s), want 1 -- every claim below is vacuous", seen.calls)
			}
			if seen.value == nil || *seen.value != tc.want {
				t.Errorf("the invoice seam was handed %s for a %q correction, want %q -- the register would hold a value the screen has stopped showing",
					cxShowValue(seen.value), tc.method, tc.want)
			}
			if got := cxTotal(t, ctx, invoiceID); got != tc.want {
				t.Errorf("invoices.total = %s after a %q correction, want %s", got, tc.method, tc.want)
			}
			// The row keeps what the caller sent: the correction table is the append-only record
			// of a human action, and the server simply does not trust that value for the invoice.
			if stored := cxCorrectionValue(t, ctx, jobID); stored != cxTotalAfter {
				t.Errorf("the correction row carries value %q, want the posted %q -- the row records the action, not the applied value", stored, cxTotalAfter)
			}
		})
	}
}

// cxClearingApplier writes buyer_tin -- the column the row below undoes -- so a nil value is
// observable as a NULL column rather than only as a nil argument. What the PRODUCTION applier
// does with a nil is proved where it lives: TestInvoiceEditFor_ANilValueClearsEveryWritableColumn
// (cmd/submission) and TestRLS_EditBySourceDocumentTxClearsAColumnToNull (internal/invoice).
func cxClearingApplier(seen *cxSeamCall) extraction.ApplyFieldToInvoice {
	return func(ctx context.Context, tx pgx.Tx, documentID, field string, value *string, method extraction.CorrectionMethod) (string, error) {
		seen.calls++
		seen.field, seen.value, seen.method = field, value, method
		var id string
		if err := tx.QueryRow(ctx,
			`UPDATE invoices SET buyer_tin = $1 WHERE source_document_id = $2 RETURNING id`,
			value, documentID).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return "", extraction.ErrNoInvoiceForDocument
			}
			return "", err
		}
		return id, nil
	}
}

func cxBuyerTIN(t *testing.T, ctx context.Context, invoiceID string) *string {
	t.Helper()
	var out *string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT buyer_tin FROM invoices WHERE id = $1`, invoiceID).Scan(&out); err != nil {
		t.Fatalf("read invoices.buyer_tin for %s: %v", invoiceID, err)
	}
	return out
}

func cxShowValue(p *string) string {
	if p == nil {
		return "<clear>"
	}
	return `"` + *p + `"`
}

// mockDefaultResult gives buyer_tin and vat a NULL rank-0 value, so two of the five undoable
// deployed fields have no reading to restore -- and buyer_tin is the field a human types into
// first. The screen resets to "no value" and the register must agree; the column is nullable
// with no CHECK (migrations/20260714103137_invoices.sql), so NULL is the representation it was
// built for. The empty string is not: vat and total are numeric(14,2), and casting an
// empty string to numeric raises 22P02.
// The boundary's blank-value 400 gates the REQUEST, not the applied value, which is why the
// POST below still carries one.
func TestRLS_UndoOnAFieldTheExtractorNeverReadClearsTheColumn(t *testing.T) {
	const posted = "31775208-0003"

	ctx := t.Context()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID := cxInvoice(t, ctx, tenantID, entityID, documentID, "EXTR12-07-UNDO-NULL", "draft")
	if _, err := stRequire(t).super.Exec(ctx,
		`UPDATE invoices SET buyer_tin = $1 WHERE id = $2`, posted, invoiceID); err != nil {
		t.Fatalf("seed the buyer_tin the undo has to clear: %v", err)
	}

	// The reading EXISTS and its value is NULL -- not the same as no row at all, and the two
	// must not be conflated: a job with no results row is a job that was never read.
	cxReading(t, ctx, tenantID, jobID, "buyer_tin", 0, nil)
	cxReading(t, ctx, tenantID, jobID, "total", 0, cxStr(cxReadingTotal))

	var seen cxSeamCall
	w := cxServe(t, reqCtx, jobID, "buyer_tin", corBody(posted, "undone", ""),
		cxClearingApplier(&seen), cxAuditor(nil))

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if seen.calls != 1 {
		t.Fatalf("the invoice seam ran %d time(s), want 1 -- every claim below is vacuous", seen.calls)
	}
	if seen.field != "buyer_tin" || seen.method != extraction.MethodUndone {
		t.Fatalf("the invoice seam was handed (%q, %q), want (%q, %q)", seen.field, seen.method, "buyer_tin", extraction.MethodUndone)
	}
	if seen.value != nil {
		t.Errorf("the invoice seam was handed %s for an undo on a field the extractor never read, want the clear signal -- the register would keep a value the screen has stopped showing",
			cxShowValue(seen.value))
	}
	if got := cxBuyerTIN(t, ctx, invoiceID); got != nil {
		t.Errorf("invoices.buyer_tin = %q after the undo, want SQL NULL -- the screen shows no value and the register must agree", *got)
	}
	if stored := cxCorrectionValue(t, ctx, jobID); stored != posted {
		t.Errorf("the correction row carries value %q, want the posted %q", stored, posted)
	}
}
