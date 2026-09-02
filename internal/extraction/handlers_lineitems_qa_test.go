// handlers_lineitems_qa_test.go: the adversarial half of POST /v1/extractions/{id}/line-items --
// the full status map including the 500 arm, the RLS isolation proved against the policy rather
// than against absence, the append-only grant, and the empty set's correction value.
//
// Helpers use a lxq* prefix; lix hnd dtl up rd st wk dc fx mx px pr ps pt pd pb pe rx de rp eq
// cs rvd cor cx rda li are taken.
package extraction_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/extraction"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// lxqUnrecognised is the arm statusForErr's default must catch: a seam error naming no domain
// sentinel is a 500, never a leaked sentence.
var lxqUnrecognised = errors.New("lxq: an error this route has never heard of")

// lxqDraftJob seeds a tenant, its job and one draft invoice filed from the job's document.
func lxqDraftJob(t *testing.T, ctx context.Context, number string) (reqCtx context.Context, tenantID, jobID, invoiceID string) {
	t.Helper()
	reqCtx, tenantID, documentID, jobID := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantID) })
	entityID := cxEntity(t, ctx, tenantID)
	invoiceID = cxInvoice(t, ctx, tenantID, entityID, documentID, number, "draft")
	return reqCtx, tenantID, jobID, invoiceID
}

// --- the whole status map, all ten outcomes -------------------------------------------------

// The individual arms above each prove one refusal. This enumerates every outcome the route can
// answer in one table, so an arm silently collapsing into its neighbour has somewhere to fail --
// and it is the only place the unrecognised-error 500 is asserted on purpose rather than reached
// incidentally by an induced transaction failure.
func TestRLS_LineItemsStatusMap(t *testing.T) {
	ctx := t.Context()

	// Two live fixtures: one active member with a draft invoice, one suspended member. The
	// remaining arms need neither.
	okCtx, _, okJob, _ := lxqDraftJob(t, ctx, "EXTR13-03-MAP-OK")
	suspCtx, suspTenant, suspDocument := rdTenant(t, ctx, "suspended")
	t.Cleanup(func() { rdaPurge(t, suspTenant) })
	suspJob := cxJobIn(t, ctx, suspTenant, suspDocument)

	// The control: the fixture answers 201 when nothing is wrong, so a table of refusals below
	// is not merely watching a route that refuses everything.
	if w := lixDBServe(t, okCtx, okJob, lixLinesBody(1),
		lixApplier(lixApplyOpts{write: true}), cxAuditor(nil)); w.Code != http.StatusCreated {
		t.Fatalf("control: an unimpeded POST answered %d (body=%q), want 201 -- the table below then proves nothing", w.Code, w.Body.String())
	}

	for _, tc := range []struct {
		name       string
		reqCtx     context.Context
		jobID      string
		body       string
		apply      extraction.ApplyLineItemsToInvoice
		wantStatus int
		wantMsg    string
	}{
		{"no identity", ctx, okJob, lixLinesBody(1), lixApplier(lixApplyOpts{}), http.StatusUnauthorized, hndMsgUnauthorized},
		{"suspended member", suspCtx, suspJob, lixLinesBody(1), lixApplier(lixApplyOpts{}), http.StatusForbidden, db.NotActiveMemberMessage},
		{"malformed job id", okCtx, "not-a-uuid", lixLinesBody(1), lixApplier(lixApplyOpts{}), http.StatusBadRequest, corMsgMalformedID},
		{"unparseable body", okCtx, okJob, "{not json", lixApplier(lixApplyOpts{}), http.StatusBadRequest, lixMsgInvalidBody},
		{"absent lines key", okCtx, okJob, `{}`, lixApplier(lixApplyOpts{}), http.StatusBadRequest, lixMsgNoLinesKey},
		{"1000 lines", okCtx, okJob, lixLinesBody(1000), lixApplier(lixApplyOpts{}), http.StatusBadRequest, lixMsgTooManyLines},
		{"unknown job", okCtx, uuid.NewString(), lixLinesBody(1), lixApplier(lixApplyOpts{}), http.StatusNotFound, "not found"},
		{"no invoice filed", okCtx, okJob, lixLinesBody(1), lixRefusingApplier(extraction.ErrNoInvoiceForDocument), http.StatusConflict, corMsgNoInvoice},
		{"invoice not editable", okCtx, okJob, lixLinesBody(1), lixRefusingApplier(extraction.ErrInvoiceNotEditable), http.StatusConflict, corMsgNotFixable},
		{"value refused", okCtx, okJob, lixLinesBody(1), lixRefusingApplier(extraction.ErrValueRefused), http.StatusBadRequest, "the invoice refused this value"},
		{"an error this route has never heard of", okCtx, okJob, lixLinesBody(1), lixRefusingApplier(lxqUnrecognised), http.StatusInternalServerError, hndMsgInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := lixDBServe(t, tc.reqCtx, tc.jobID, tc.body, tc.apply, cxAuditor(nil))
			hndAssert(t, w, tc.wantStatus, hndErrBody(t, tc.wantMsg))
			if strings.Contains(w.Body.String(), lxqUnrecognised.Error()) {
				t.Errorf("the wire carried the seam's own sentence: %q", w.Body.String())
			}
		})
	}
}

// --- the cross-tenant 404 is the POLICY, not an absent row ----------------------------------

// TestRLS_LineItemsCrossTenantIsIndistinguishableFromAbsent shows A gets the same 404 for B's
// job and for a job that never existed. That holds equally if the route simply never finds
// anything, so this proves the row IS there and only tenant_isolation hides it: the superuser
// reads it, B's own app-role read finds it, A's identical app-role read is zero rows.
func TestRLS_LineItemsCrossTenantJobIsHiddenByThePolicyNotByAbsence(t *testing.T) {
	ctx := t.Context()
	reqCtxA, tenantA, _, _ := cxJob(t, ctx)
	reqCtxB, tenantB, _, jobB := cxJob(t, ctx)
	t.Cleanup(func() { rdaPurge(t, tenantA, tenantB) })

	// The row exists, RLS off: anything below reading zero rows is the policy at work.
	var superDoc string
	if err := stRequire(t).super.QueryRow(ctx,
		`SELECT document_id FROM extraction_jobs WHERE id = $1`, jobB).Scan(&superDoc); err != nil {
		t.Fatalf("the superuser cannot see B's job %s: %v -- the comparison below is then vacuous: %v", jobB, err, err)
	}
	if superDoc == "" {
		t.Fatalf("the superuser read an empty document_id for B's job, so the app-role reads below prove nothing")
	}

	// The handler's own query, verbatim, run as invoice_app inside each tenant's transaction.
	appRoleSees := func(reqCtx context.Context) bool {
		t.Helper()
		found := false
		err := db.WithinRequestTenantTx(reqCtx, stRequire(t).app, func(tx pgx.Tx) error {
			var doc string
			switch err := tx.QueryRow(reqCtx,
				`SELECT document_id FROM extraction_jobs WHERE id = $1`, jobB).Scan(&doc); {
			case err == nil:
				found = true
				return nil
			case errors.Is(err, pgx.ErrNoRows):
				return nil
			default:
				return err
			}
		})
		if err != nil {
			t.Fatalf("app-role read of job %s: %v", jobB, err)
		}
		return found
	}

	if !appRoleSees(reqCtxB) {
		t.Fatalf("control: B's OWN app-role read of B's job found nothing -- the A read below then proves nothing about isolation")
	}
	if appRoleSees(reqCtxA) {
		t.Errorf("A's app-role read reached B's job; the route's 404 is not the policy and would become a 201 the moment tenant_isolation moved")
	}

	// And the route agrees.
	w := lixDBServe(t, reqCtxA, jobB, lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("A posting to B's job answered %d (body=%q), want 404", w.Code, w.Body.String())
	}
}

// --- corrections are append-only to the role this route writes as ---------------------------

// The grant is SELECT, INSERT only, so nothing can rewrite a correction this route appended.
// Asserted against the row the route actually wrote, as invoice_app -- the role the handler
// holds -- rather than against a synthetic one.
func TestRLS_LineItemsCorrectionRowIsAppendOnlyToTheAppRole(t *testing.T) {
	ctx := t.Context()
	reqCtx, _, jobID, _ := lxqDraftJob(t, ctx, "EXTR13-03-APPEND")

	w := lixDBServe(t, reqCtx, jobID, lixLinesBody(1), lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}

	// The control: the role CAN read the row, so the two refusals below are the grant refusing a
	// write and not a row the role simply cannot reach.
	var n int
	if err := db.WithinRequestTenantTx(reqCtx, stRequire(t).app, func(tx pgx.Tx) error {
		return tx.QueryRow(reqCtx,
			`SELECT count(*) FROM extraction_field_corrections WHERE extraction_job_id = $1`, jobID).Scan(&n)
	}); err != nil {
		t.Fatalf("control: the app role cannot read the correction row: %v", err)
	}
	if n != 1 {
		t.Fatalf("control: the app role reads %d correction row(s), want 1 -- the refusals below then prove nothing", n)
	}

	// A refused statement aborts its transaction, so each probe runs on its own.
	for _, probe := range []struct{ name, sql string }{
		{"UPDATE", `UPDATE extraction_field_corrections SET value = '[]' WHERE extraction_job_id = $1`},
		{"DELETE", `DELETE FROM extraction_field_corrections WHERE extraction_job_id = $1`},
	} {
		err := db.WithinRequestTenantTx(reqCtx, stRequire(t).app, func(tx pgx.Tx) error {
			_, err := tx.Exec(reqCtx, probe.sql, jobID)
			return err
		})
		if err == nil {
			t.Errorf("%s on extraction_field_corrections succeeded as invoice_app; the history is not append-only", probe.name)
			continue
		}
		if !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s was refused with %v, want a permission-denied -- an append-only history must rest on the grant", probe.name, err)
		}
	}
	if n := cxCorrectionRows(t, ctx, jobID); n != 1 {
		t.Errorf("%d correction row(s) after the probes, want the original 1", n)
	}
}

// --- the empty set's correction value ------------------------------------------------------

// lines: [] must land as the two-character array, which is what clears the value CHECK
// (char_length(value) > 0). A nil-marshalled "null" would also clear it, so the exact bytes are
// asserted, with a populated write as the control.
func TestRLS_LineItemsEmptySetWritesTheTwoCharacterArray(t *testing.T) {
	ctx := t.Context()
	reqCtx, _, emptyJob, _ := lxqDraftJob(t, ctx, "EXTR13-03-EMPTYVAL")

	w := lixDBServe(t, reqCtx, emptyJob, `{"lines":[]}`, lixApplier(lixApplyOpts{write: true}), cxAuditor(nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := cxCorrectionRow(t, ctx, emptyJob).value; got != "[]" {
		t.Errorf("the correction value for lines: [] = %q, want %q", got, "[]")
	}

	// The control: a populated set writes something else, so the assertion above is reading the
	// posted set rather than a constant.
	reqCtx2, _, fullJob, _ := lxqDraftJob(t, ctx, "EXTR13-03-FULLVAL")
	if w := lixDBServe(t, reqCtx2, fullJob, lixLinesBody(1),
		lixApplier(lixApplyOpts{write: true}), cxAuditor(nil)); w.Code != http.StatusCreated {
		t.Fatalf("control: status = %d, want %d (body=%q)", w.Code, http.StatusCreated, w.Body.String())
	}
	if got := cxCorrectionRow(t, ctx, fullJob).value; got == "[]" || !strings.HasPrefix(got, `[{"description":`) {
		t.Errorf("control: the correction value for a populated set = %q, want the canonical JSON of one line", got)
	}
}
