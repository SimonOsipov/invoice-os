// APPR-08-05: (*Store).ApprovalFacts, the read-only sibling of CallerRole that feeds
// GetHandler's third seam. Fixtures come from apply_validation_arming_test.go, the
// tracer from transition_gate_test.go / transition_gate_adversarial_test.go.
package invoice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// approvalFactsFixture is a tenant with an active one-step policy, one invoice at
// validated, and a caller identity. staffed=true also seeds the workflow role the
// policy step names, staffed with the caller and backed by an admin membership --
// the three joins GateFactsTx's AXIS-2 query needs to answer CallerHoldsRole true.
type approvalFactsFixture struct {
	tenantID  string
	entityID  string
	versionID string
	invID     string
	subject   string
	ctx       context.Context
}

func seedApprovalFactsFixture(t *testing.T, super *pgxpool.Pool, label string, staffed bool) approvalFactsFixture {
	t.Helper()
	tenantID, entityID, versionID := seedOneStepActivePolicyTenant(t, super, label)
	subject := uuid.NewString()
	// Unconditional: subject is a real member either way, admin only when
	// staffed also needs workflow_role_members_tenant_user_fk to resolve.
	role := "preparer"
	if staffed {
		role = "admin"
	}
	seedMembership(t, super, tenantID, subject, role)
	if staffed {
		roleID := seedWorkflowRoleFor(t, super, tenantID, "finance-lead", "Finance Lead")
		staffWorkflowRoleFor(t, super, tenantID, roleID, subject, 0)
	}
	return approvalFactsFixture{
		tenantID:  tenantID,
		entityID:  entityID,
		versionID: versionID,
		invID:     seedInvoiceAtStatus(t, super, tenantID, entityID, label, StatusValidated),
		subject:   subject,
		ctx: auth.WithIdentity(context.Background(), auth.Identity{
			Subject: subject, Role: "authenticated", TenantID: tenantID,
		}),
	}
}

// armInvoice replaces the fixture's bare invoice with one armed through the REAL
// path -- Store.ApplyValidation's promoting branch, which writes approval_runs AND
// its approval_run_steps. seedApprovalRunFor writes the run row only, so a run it
// seeds has no pending step and can never answer PendingStepOrd or CallerHoldsRole.
func (fx *approvalFactsFixture) armInvoice(t *testing.T, super, app *pgxpool.Pool, number string) {
	t.Helper()
	store := NewStore(app)
	inv, err := store.Create(fx.ctx, CreateInput{EntityID: fx.entityID, InvoiceNumber: number})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.ApplyValidation(fx.ctx, inv.ID, []Violation{}, seedRuleSetVersionID(t, super), contentFingerprint(inv, inv.LineItems))
	if err != nil {
		t.Fatalf("ApplyValidation (clean, active policy): %v", err)
	}
	if got.Status != StatusValidated {
		t.Fatalf("armed invoice status = %q, want %q", got.Status, StatusValidated)
	}
	if n := mustCount(t, super, `SELECT count(*) FROM approval_run_steps s JOIN approval_runs r ON r.id = s.run_id WHERE r.invoice_id = $1 AND s.kind = 'approval' AND s.state = 'pending'`, inv.ID); n != 1 {
		t.Fatalf("armed invoice has %d pending approval steps, want 1 -- the fixture cannot answer PendingStepOrd", n)
	}
	fx.invID = inv.ID
}

// --- AC-2: the flag folds TransmitClear, and ONLY TransmitClear --------------

// TestStoreApprovalFacts_FoldsTheFlagOff: an open run under an active policy is NOT
// clear, but a flag-off deployment reads clear anyway -- the arm is inert there.
func TestStoreApprovalFacts_FoldsTheFlagOff(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-FLAGOFF", false)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID) // defaults to open

	store := NewStore(app, WithApprovalsEnforced(false))

	got, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts: %v", err)
	}
	if !got.TransmitClear {
		t.Errorf("TransmitClear = false with APPROVALS_ENFORCED off, want true -- the flag folds this field")
	}
}

// TestStoreApprovalFacts_FoldsTheFlagOn is the biting half: the same open run under
// the same active policy, with the flag on, is not clear.
func TestStoreApprovalFacts_FoldsTheFlagOn(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-FLAGON", false)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

	store := NewStore(app, WithApprovalsEnforced(true))

	got, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts: %v", err)
	}
	if got.TransmitClear {
		t.Errorf("TransmitClear = true with an open run under an active policy and the flag on, want false")
	}
}

// TestStoreApprovalFacts_ApprovedRunIsClearWithTheFlagOn is the permissive control
// for the row above: a method that always answered false would pass that spec for
// free. An approved run clears the gate even with the flag on.
func TestStoreApprovalFacts_ApprovedRunIsClearWithTheFlagOn(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-APPROVED", false)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	store := NewStore(app, WithApprovalsEnforced(true))

	got, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts: %v", err)
	}
	if !got.TransmitClear {
		t.Errorf("TransmitClear = false with an approved run, want true")
	}
	if got.RunState != "approved" {
		t.Errorf("RunState = %q, want %q", got.RunState, "approved")
	}
}

// TestStoreApprovalFacts_CarriesRunStatePendingOrdAndHoldsRole: the three fields
// APPR-08-06 consumes must equal GateFactsTx's own answer, not be silently zeroed
// on the way out.
func TestStoreApprovalFacts_CarriesRunStatePendingOrdAndHoldsRole(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-FIELDS", true)
	fx.armInvoice(t, super, app, "appr-08-05-fields")

	store := NewStore(app, WithApprovalsEnforced(true))

	got, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts: %v", err)
	}
	if got.RunState != "open" {
		t.Errorf("RunState = %q, want %q", got.RunState, "open")
	}
	// t.Error, not t.Fatalf: CallerHoldsRole below is a separate field and must be
	// observable even while PendingStepOrd is still nil.
	switch {
	case got.PendingStepOrd == nil:
		t.Error("PendingStepOrd = nil, want a pointer to 0 -- the armed run has one pending kind='approval' step at ord 0")
	case *got.PendingStepOrd != 0:
		t.Errorf("PendingStepOrd = %d, want 0", *got.PendingStepOrd)
	}
	if !got.CallerHoldsRole {
		t.Error("CallerHoldsRole = false, want true -- the caller is an active admin staffed on the pending step's workflow role")
	}
}

// TestStoreApprovalFacts_UnstaffedCallerDoesNotHoldTheRole is the negative control
// for CallerHoldsRole: a hardcoded true would pass the spec above for free.
func TestStoreApprovalFacts_UnstaffedCallerDoesNotHoldTheRole(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-UNSTAFFED", false)
	fx.armInvoice(t, super, app, "appr-08-05-unstaffed")

	store := NewStore(app, WithApprovalsEnforced(true))

	got, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts: %v", err)
	}
	if got.CallerHoldsRole {
		t.Error("CallerHoldsRole = true for a caller staffed on no workflow role, want false")
	}
	if got.RunState != "open" {
		t.Errorf("RunState = %q, want %q -- the run facts are read for every caller, holder or not", got.RunState, "open")
	}
}

// TestStoreApprovalFacts_ReadsRunFactsEvenWithTheFlagOff is the tripwire against
// "fixing" this method into consistency with the two WRITE doors, which skip the
// approval read entirely when the flag is off. can_approve/can_reject ship
// unflagged (docs/approvals.md section 11), so RunState/PendingStepOrd/
// CallerHoldsRole must be populated in every deployment.
func TestStoreApprovalFacts_ReadsRunFactsEvenWithTheFlagOff(t *testing.T) {
	super, app := dbTestPools(t)
	traced, rec := tracedAppPool(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-OFFREADS", true)
	fx.armInvoice(t, super, app, "appr-08-05-offreads")

	store := NewStore(traced, WithApprovalsEnforced(false))
	rec.reset()

	got, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts: %v", err)
	}
	if stmts := rec.mentioning("approval_runs"); len(stmts) == 0 {
		t.Error("no statement mentioning approval_runs was issued with the flag off -- the flag folds ONLY TransmitClear, never the read")
	}
	if got.RunState != "open" {
		t.Errorf("RunState = %q with the flag off, want %q", got.RunState, "open")
	}
	if got.PendingStepOrd == nil {
		t.Error("PendingStepOrd = nil with the flag off, want a pointer to 0")
	}
	if !got.CallerHoldsRole {
		t.Error("CallerHoldsRole = false with the flag off, want true")
	}
	if !got.TransmitClear {
		t.Error("TransmitClear = false with the flag off, want true")
	}
}

// TestStoreApprovalFacts_OneTransaction: db.WithinTenantTx issues exactly one
// set_config('app.current_tenant' per transaction, so the count of those statements
// IS the transaction count.
func TestStoreApprovalFacts_OneTransaction(t *testing.T) {
	super, app := dbTestPools(t)
	traced, rec := tracedAppPool(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-ONETX", true)
	fx.armInvoice(t, super, app, "appr-08-05-onetx")

	store := NewStore(traced, WithApprovalsEnforced(true))
	rec.reset()

	if _, err := store.ApprovalFacts(fx.ctx, fx.invID); err != nil {
		t.Fatalf("ApprovalFacts: %v", err)
	}
	// The request seam batches its set_config, which a QueryTracer alone cannot see —
	// tenantTxCount() counts both routes (db.WithinRequestTenantTxOpts).
	if n := rec.tenantTxCount(); n != 1 {
		t.Errorf("ApprovalFacts opened %d tenant transactions, want exactly 1", n)
	}
}

// TestStoreApprovalFacts_ErrorReturnsTheZeroValue: a tenant-less ctx must return
// db.ErrNoTenant AND the zero value, whose TransmitClear is false -- so a caller
// that ignores the error still fails closed.
func TestStoreApprovalFacts_ErrorReturnsTheZeroValue(t *testing.T) {
	_, app := dbTestPools(t)

	store := NewStore(app, WithApprovalsEnforced(true))

	got, err := store.ApprovalFacts(context.Background(), uuid.NewString())
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("err = %v, want db.ErrNoTenant on an unauthenticated ctx", err)
	}
	if got != (ApprovalFacts{}) {
		t.Errorf("ApprovalFacts = %+v on error, want the zero value -- TransmitClear must read false", got)
	}
}

// TestStoreApprovalFacts_NoRunIsNotClearWithTheFlagOn: the seeded backlog shape --
// validated under an active policy with no run at all. TransmitClear fails closed
// on an absent answer, and RunState is "" rather than an error.
func TestStoreApprovalFacts_NoRunIsNotClearWithTheFlagOn(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-NORUN", false)

	store := NewStore(app, WithApprovalsEnforced(true))

	got, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts with no run: %v (want a fields answer, never an error)", err)
	}
	if got.TransmitClear {
		t.Error("TransmitClear = true with no run under an active policy, want false")
	}
	if got.RunState != "" {
		t.Errorf("RunState = %q, want the empty string when the invoice has no run", got.RunState)
	}
	if got.PendingStepOrd != nil {
		t.Errorf("PendingStepOrd = %d, want nil when the invoice has no run", *got.PendingStepOrd)
	}
}

// TestStoreApprovalFacts_UppercaseIdReadsTheSameRow: the id reaching this method is
// GetHandler's fetched inv.ID, but a caller passing Postgres's non-canonical
// spelling must not silently read "no run" and hand back a clear verdict.
func TestStoreApprovalFacts_UppercaseIdReadsTheSameRow(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-UPPER", false)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

	upper := strings.ToUpper(fx.invID)
	if upper == fx.invID {
		t.Fatalf("fixture id %q has no lowercase hex digits -- the case this test exists for is not exercised", fx.invID)
	}

	store := NewStore(app, WithApprovalsEnforced(true))

	got, err := store.ApprovalFacts(fx.ctx, upper)
	if err != nil {
		t.Fatalf("ApprovalFacts(UPPERCASE id): %v", err)
	}
	if got.TransmitClear {
		t.Error("TransmitClear = true for an UPPERCASE spelling of a gated invoice's id, want false")
	}
	if got.RunState != "open" {
		t.Errorf("RunState = %q, want %q -- the uuid comparison is by value, not by text", got.RunState, "open")
	}
}
