// DB-backed specs for the transitions route refusing every fiscal status
// (SECAUD-09-01). The real Store.Transition sits behind the real handler, so
// only the handler clause can produce the 409; each refusal test pairs with a
// twin driven straight through Store.Transition to prove the edge was legal.
package invoice

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type fiscalFixture struct {
	super, app *pgxpool.Pool
	store      *Store
	tenantID   string
	entityID   string
	admin      auth.Identity
}

func newFiscalFixture(t *testing.T, label string) fiscalFixture {
	t.Helper()
	super, app := dbTestPools(t)
	tenantID := seedTenant(t, super, label+" tenant")
	entityID := seedEntity(t, super, tenantID, label+" entity")
	subject := uuid.NewString()
	seedMembership(t, super, tenantID, subject, "admin")
	return fiscalFixture{
		super: super, app: app, store: NewStore(app), tenantID: tenantID, entityID: entityID,
		admin: auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID},
	}
}

func (f fiscalFixture) ctx() context.Context {
	return auth.WithIdentity(context.Background(), f.admin)
}

func (f fiscalFixture) seedAt(t *testing.T, number string, status Status) string {
	t.Helper()
	return seedInvoiceAtStatus(t, f.super, f.tenantID, f.entityID, number, status)
}

type fiscalRow struct {
	status, reasons                     string
	irn, csid, qr, failureKind          *string
	history, transitionedAudit, jobRows int
}

func (f fiscalFixture) read(t *testing.T, invID string) fiscalRow {
	t.Helper()
	var r fiscalRow
	if err := f.super.QueryRow(context.Background(),
		`SELECT status, irn, csid, qr_payload, failure_kind, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&r.status, &r.irn, &r.csid, &r.qr, &r.failureKind, &r.reasons); err != nil {
		t.Fatalf("read invoice row: %v", err)
	}
	r.history = mustCount(t, f.super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	r.transitionedAudit = auditCount(t, f.app, f.tenantID, "invoice.transitioned")
	r.jobRows = mustCount(t, f.super, `SELECT count(*) FROM submission_jobs WHERE invoice_id = $1`, invID)
	return r
}

func strPtrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func assertFiscalRowUnchanged(t *testing.T, got, want fiscalRow) {
	t.Helper()
	if got.status != want.status {
		t.Errorf("status = %q, want %q -- a refused call must not move the row", got.status, want.status)
	}
	if !strPtrEq(got.irn, want.irn) || !strPtrEq(got.csid, want.csid) || !strPtrEq(got.qr, want.qr) ||
		!strPtrEq(got.failureKind, want.failureKind) || got.reasons != want.reasons {
		t.Errorf("fiscal columns changed: got %+v, want %+v", got, want)
	}
	if got.history != want.history {
		t.Errorf("history rows = %d, want %d", got.history, want.history)
	}
	if got.transitionedAudit != want.transitionedAudit {
		t.Errorf("invoice.transitioned audit rows = %d, want %d", got.transitionedAudit, want.transitionedAudit)
	}
	if got.jobRows != want.jobRows {
		t.Errorf("submission_jobs rows = %d, want %d", got.jobRows, want.jobRows)
	}
}

func (f fiscalFixture) post(t *testing.T, callerRole func(context.Context) (string, error), id *auth.Identity, invID, target string) (int, string) {
	t.Helper()
	rec, resp := doInvoiceTransitionAs(t, f.store.Transition, callerRole, id, invID, `{"target":"`+target+`"}`)
	return rec.Code, resp.Error
}

func (f fiscalFixture) refuseAndKeep(t *testing.T, invID, target string) {
	t.Helper()
	before := f.read(t, invID)
	code, msg := f.post(t, f.store.CallerRole, &f.admin, invID, target)
	if code != http.StatusConflict {
		t.Errorf("target %s: status = %d, want 409", target, code)
	}
	if msg != wantFiscalStatusRefusal {
		t.Errorf("target %s: error = %q, want %q", target, msg, wantFiscalStatusRefusal)
	}
	assertFiscalRowUnchanged(t, f.read(t, invID), before)
}

// Each fiscal target from a source where the edge is legal: the control twin
// proves the store accepts it, so only the handler clause refuses the HTTP call.
func TestTransitionHandler_RealStore_RefusesEveryFiscalTarget(t *testing.T) {
	cases := []struct {
		source  Status
		targets []Status
	}{
		{StatusValidated, []Status{StatusQueued}},
		{StatusQueued, []Status{StatusSubmitted, StatusAccepted, StatusRejected, StatusFailed}},
		{StatusSubmitted, []Status{StatusAccepted, StatusRejected, StatusFailed}},
	}
	for _, c := range cases {
		for _, target := range c.targets {
			t.Run(string(c.source)+"->"+string(target), func(t *testing.T) {
				f := newFiscalFixture(t, "fiscal refuse "+string(c.source)+string(target))
				invID := f.seedAt(t, "FISCAL-REFUSE", c.source)
				twinID := f.seedAt(t, "FISCAL-TWIN", c.source)

				f.refuseAndKeep(t, invID, string(target))

				twin, err := f.store.Transition(f.ctx(), twinID, target)
				if err != nil {
					t.Fatalf("control: Store.Transition(%s -> %s) = %v, want success (the edge must be legal)", c.source, target, err)
				}
				if twin.Status != target {
					t.Errorf("control: twin status = %q, want %q", twin.Status, target)
				}
			})
		}
	}
}

// A validated invoice moves to draft through the route, with one new history row.
func TestTransitionHandler_RealStore_DraftStillMovesAValidatedInvoice(t *testing.T) {
	f := newFiscalFixture(t, "fiscal draft")
	invID := f.seedAt(t, "FISCAL-DRAFT", StatusValidated)
	before := f.read(t, invID)

	rec, resp := doInvoiceTransitionAs(t, f.store.Transition, f.store.CallerRole, &f.admin, invID, `{"target":"draft"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if resp.Status != string(StatusDraft) {
		t.Errorf("body status = %q, want %q", resp.Status, StatusDraft)
	}
	after := f.read(t, invID)
	if after.history != before.history+1 {
		t.Errorf("history rows = %d, want %d", after.history, before.history+1)
	}
	var from, to string
	if err := f.super.QueryRow(context.Background(),
		`SELECT from_status, to_status FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`, invID,
	).Scan(&from, &to); err != nil {
		t.Fatalf("read newest history row: %v", err)
	}
	if from != string(StatusValidated) || to != string(StatusDraft) {
		t.Errorf("newest history row = %s -> %s, want validated -> draft", from, to)
	}
}

// Core AC 5: an accepted invoice carries its IRN. The twin driven through
// Store.Transition is the pair the refusal prevents (accepted, NULL irn).
func TestFiscalStatus_AcceptedAlwaysCarriesAnIRN(t *testing.T) {
	f := newFiscalFixture(t, "fiscal irn")
	invA := f.seedAt(t, "FISCAL-IRN-A", StatusQueued)
	invB := f.seedAt(t, "FISCAL-IRN-B", StatusQueued)

	f.refuseAndKeep(t, invA, "accepted")
	a := f.read(t, invA)
	if a.status != string(StatusQueued) || a.irn != nil {
		t.Errorf("after the refused hand accepted: status=%q irn=%v, want queued and NULL", a.status, a.irn)
	}

	if _, err := f.store.Transition(f.ctx(), invB, StatusAccepted); err != nil {
		t.Fatalf("control: Store.Transition(accepted) = %v, want success", err)
	}
	if b := f.read(t, invB); b.status != string(StatusAccepted) || b.irn != nil {
		t.Errorf("control twin: status=%q irn=%v, want accepted with NULL irn", b.status, b.irn)
	}

	err := db.WithinTenantTx(context.Background(), f.app, f.tenantID, func(tx pgx.Tx) error {
		_, err := f.store.MarkAcceptedTx(context.Background(), tx, invA, f.tenantID, "IRN-SECAUD09", "", "")
		return err
	})
	if err != nil {
		t.Fatalf("MarkAcceptedTx: %v", err)
	}
	if a := f.read(t, invA); a.status != string(StatusAccepted) || a.irn == nil || *a.irn != "IRN-SECAUD09" {
		t.Errorf("after MarkAcceptedTx: status=%q irn=%v, want accepted with IRN-SECAUD09", a.status, a.irn)
	}
}

// The state-table rows of the write endpoint, by caller state.
func TestTransitionHandler_RealStore_FiscalTargetByCallerState(t *testing.T) {
	t.Run("reviewer", func(t *testing.T) {
		f := newFiscalFixture(t, "fiscal reviewer")
		reviewer := uuid.NewString()
		seedMembership(t, f.super, f.tenantID, reviewer, "reviewer")
		id := auth.Identity{Subject: reviewer, Role: "authenticated", TenantID: f.tenantID}
		invID := f.seedAt(t, "FISCAL-REVIEWER", StatusQueued)
		before := f.read(t, invID)

		code, msg := f.post(t, f.store.CallerRole, &id, invID, "accepted")

		if code != http.StatusConflict || msg != wantFiscalStatusRefusal {
			t.Errorf("reviewer: got %d %q, want 409 %q", code, msg, wantFiscalStatusRefusal)
		}
		assertFiscalRowUnchanged(t, f.read(t, invID), before)
	})

	t.Run("suspended_admin", func(t *testing.T) {
		f := newFiscalFixture(t, "fiscal suspended")
		suspended := uuid.NewString()
		seedMembershipWithStatus(t, f.super, f.tenantID, suspended, "admin", "suspended")
		id := auth.Identity{Subject: suspended, Role: "authenticated", TenantID: f.tenantID}
		invID := f.seedAt(t, "FISCAL-SUSPENDED", StatusQueued)
		before := f.read(t, invID)

		code, msg := f.post(t, f.store.CallerRole, &id, invID, "accepted")

		if code != http.StatusForbidden || msg != wantNotApproverTransmitReason {
			t.Errorf("suspended admin: got %d %q, want 403 %q (role refusal, not 409)", code, msg, wantNotApproverTransmitReason)
		}
		assertFiscalRowUnchanged(t, f.read(t, invID), before)
	})

	t.Run("repeat", func(t *testing.T) {
		f := newFiscalFixture(t, "fiscal repeat")
		invID := f.seedAt(t, "FISCAL-REPEAT", StatusQueued)
		before := f.read(t, invID)

		for i := 1; i <= 2; i++ {
			code, msg := f.post(t, f.store.CallerRole, &f.admin, invID, "accepted")
			if code != http.StatusConflict || msg != wantFiscalStatusRefusal {
				t.Errorf("send %d: got %d %q, want 409 %q", i, code, msg, wantFiscalStatusRefusal)
			}
		}
		assertFiscalRowUnchanged(t, f.read(t, invID), before)
	})
}
