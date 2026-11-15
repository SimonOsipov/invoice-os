package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

type clearedFixture struct {
	super, app *pgxpool.Pool
	store      *Store
	tenantID   string
	policyID   string
	c          context.Context
	inv        Invoice
}

// newClearedFixture seeds a draft invoice with two lines; withPolicy puts a step-less
// (zero-step, so auto-approved) catch-all policy in force.
func newClearedFixture(t *testing.T, withPolicy bool) *clearedFixture {
	t.Helper()
	super, app := dbTestPools(t)
	f := &clearedFixture{super: super, app: app, store: NewStore(app)}
	f.tenantID = seedTenant(t, super, "CLEARED tenant")
	entityID := seedEntity(t, super, f.tenantID, "CLEARED entity")
	if withPolicy {
		f.policyID = seedApprovalPolicyFor(t, super, f.tenantID, "CLEARED policy")
		versionID := seedApprovalPolicyVersionFor(t, super, f.tenantID, f.policyID)
		activateApprovalPolicyVersionFor(t, super, versionID)
	}
	f.c = auth.WithIdentity(context.Background(), auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: f.tenantID})
	inv, err := f.store.Create(f.c, CreateInput{
		EntityID: entityID, InvoiceNumber: "CLEARED-" + uuid.NewString()[:8],
		Currency: strPtr("NGN"), Total: strPtr("100.00"),
		LineItems: []LineItemInput{
			{Description: strPtr("one"), LineTotal: strPtr("40.00")},
			{Description: strPtr("two"), LineTotal: strPtr("60.00")},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f.inv = inv
	return f
}

func (f *clearedFixture) validate(t *testing.T) {
	t.Helper()
	cur, err := f.store.Get(f.c, f.inv.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	v, err := f.store.ApplyValidation(f.c, f.inv.ID, []Violation{}, seedRuleSetVersionID(t, f.super), contentFingerprint(cur, cur.LineItems))
	if err != nil {
		t.Fatalf("ApplyValidation: %v", err)
	}
	if v.Status != StatusValidated {
		t.Fatalf("status after ApplyValidation = %q, want validated", v.Status)
	}
}

func (f *clearedFixture) setStatus(t *testing.T, status Status) {
	t.Helper()
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET status = $2 WHERE id = $1`, f.inv.ID, string(status)); err != nil {
		t.Fatalf("set status %s: %v", status, err)
	}
}

// queued validates the invoice and moves it to queued by superuser.
func (f *clearedFixture) queued(t *testing.T) {
	t.Helper()
	f.validate(t)
	f.setStatus(t, StatusQueued)
}

func (f *clearedFixture) cleared(t *testing.T, id string) (bool, error) {
	t.Helper()
	var ok bool
	err := db.WithinTenantTx(context.Background(), f.app, f.tenantID, func(tx pgx.Tx) error {
		var err error
		ok, err = f.store.ContentCleared(context.Background(), tx, id)
		return err
	})
	return ok, err
}

func (f *clearedFixture) want(t *testing.T, want bool, why string) {
	t.Helper()
	got, err := f.cleared(t, f.inv.ID)
	if err != nil {
		t.Fatalf("ContentCleared: %v", err)
	}
	if got != want {
		t.Errorf("ContentCleared = %v, want %v (%s)", got, want, why)
	}
}

func TestContentCleared_SwappedInvoiceIsNotCleared(t *testing.T) {
	f := newClearedFixture(t, true)
	f.queued(t)
	f.setStatus(t, StatusRejected)
	if _, err := f.store.Edit(f.c, f.inv.ID, EditInput{UpdateInput: UpdateInput{Total: strPtr("999.00")}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	f.want(t, false, "edited, now draft: the canEdit rung")
	f.validate(t)
	f.want(t, false, "re-validated, fingerprints match: the canEdit rung alone rejects")
}

func TestContentCleared_UnchangedRejectedInvoiceIsNotCleared(t *testing.T) {
	f := newClearedFixture(t, true)
	f.queued(t)
	f.setStatus(t, StatusRejected)
	f.want(t, false, "rejected with equal fingerprints: the canEdit rung alone")
}

func TestContentCleared_QueuedHeaderChangeIsNotCleared(t *testing.T) {
	f := newClearedFixture(t, true)
	f.queued(t)
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET total = 999 WHERE id = $1`, f.inv.ID); err != nil {
		t.Fatalf("change total: %v", err)
	}
	f.want(t, false, "header changed after approval")
}

func TestContentCleared_QueuedLineChangeIsNotCleared(t *testing.T) {
	f := newClearedFixture(t, true)
	f.queued(t)
	if _, err := f.super.Exec(context.Background(),
		`UPDATE line_items SET line_total = 1 WHERE invoice_id = $1 AND line_no = 1`, f.inv.ID); err != nil {
		t.Fatalf("change line: %v", err)
	}
	f.want(t, false, "line changed after approval")
}

func TestContentCleared_RunFingerprintMismatchIsNotCleared(t *testing.T) {
	f := newClearedFixture(t, true)
	f.queued(t)
	f.want(t, true, "precondition: verdict and run both match")
	if _, err := f.super.Exec(context.Background(),
		`UPDATE approval_runs SET content_fingerprint = 'other' WHERE invoice_id = $1 AND state = 'approved'`, f.inv.ID); err != nil {
		t.Fatalf("change run fingerprint: %v", err)
	}
	f.want(t, false, "the run comparison alone rejects")
}

func TestContentCleared_UnchangedQueuedInvoiceIsCleared(t *testing.T) {
	f := newClearedFixture(t, true)
	f.queued(t)
	f.want(t, true, "unchanged, approved run")

	g := newClearedFixture(t, false)
	g.queued(t)
	g.want(t, true, "unchanged, no policy in force")
}

func TestContentCleared_LegacyNullVerdictWithoutARunIsCleared(t *testing.T) {
	f := newClearedFixture(t, false)
	f.queued(t)
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET verdict_fingerprint = NULL WHERE id = $1`, f.inv.ID); err != nil {
		t.Fatalf("null verdict: %v", err)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1`, f.inv.ID); n != 0 {
		t.Fatalf("precondition: runs = %d, want 0", n)
	}
	f.want(t, true, "legacy row passes on the status rung only")
}

func TestContentCleared_LegacyNullVerdictComparesTheRun(t *testing.T) {
	f := newClearedFixture(t, true)
	f.queued(t)
	if _, err := f.super.Exec(context.Background(), `UPDATE invoices SET verdict_fingerprint = NULL WHERE id = $1`, f.inv.ID); err != nil {
		t.Fatalf("null verdict: %v", err)
	}
	if _, err := f.super.Exec(context.Background(),
		`UPDATE approval_runs SET content_fingerprint = 'other' WHERE invoice_id = $1 AND state = 'approved'`, f.inv.ID); err != nil {
		t.Fatalf("change run fingerprint: %v", err)
	}
	f.want(t, false, "NULL verdict still compares the approved run")
}

func TestContentCleared_ARevalidationThatArmsNothingIsCleared(t *testing.T) {
	f := newClearedFixture(t, true)
	f.validate(t)
	if n := mustCount(t, f.super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'approved'`, f.inv.ID); n != 1 {
		t.Fatalf("precondition: approved runs = %d, want 1", n)
	}
	if _, err := f.store.Transition(f.c, f.inv.ID, StatusDraft); err != nil {
		t.Fatalf("Transition to draft: %v", err)
	}
	if _, err := f.store.Edit(f.c, f.inv.ID, EditInput{UpdateInput: UpdateInput{Total: strPtr("120.00")}}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if _, err := f.super.Exec(context.Background(), `UPDATE approval_policies SET deleted_at = now() WHERE id = $1`, f.policyID); err != nil {
		t.Fatalf("delete policy: %v", err)
	}
	f.validate(t)
	f.setStatus(t, StatusQueued)
	f.want(t, true, "the old run's fingerprint must not be compared")
	if n := mustCount(t, f.super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'cancelled'`, f.inv.ID); n != 1 {
		t.Errorf("cancelled runs = %d, want 1 (the old run)", n)
	}
	if n := mustCount(t, f.super, `SELECT count(*) FROM approval_runs WHERE invoice_id = $1 AND state = 'approved'`, f.inv.ID); n != 0 {
		t.Errorf("approved runs = %d, want 0", n)
	}
}

func TestContentCleared_UnknownIDIsAnError(t *testing.T) {
	f := newClearedFixture(t, false)
	ok, err := f.cleared(t, uuid.NewString())
	if err == nil || ok {
		t.Errorf("ContentCleared(unknown id) = %v, %v; want false and an error", ok, err)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want pgx.ErrNoRows or ErrNotFound", err)
	}
}
