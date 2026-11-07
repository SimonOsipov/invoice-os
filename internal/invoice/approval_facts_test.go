// APPR-08-05: (*Store).ApprovalFacts, the read-only sibling of CallerRole that feeds
// GetHandler's third seam. Fixtures come from apply_validation_arming_test.go, the
// tracer from transition_gate_test.go / transition_gate_adversarial_test.go.
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/approval"
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

// TestStoreApprovalFacts_OpenRunIsNotClear: an open run under an active policy is not clear.
func TestStoreApprovalFacts_OpenRunIsNotClear(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-OPENRUN", false)
	seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)

	store := NewStore(app)

	got, err := store.ApprovalFacts(fx.ctx, fx.invID)
	if err != nil {
		t.Fatalf("ApprovalFacts: %v", err)
	}
	if got.TransmitClear {
		t.Errorf("TransmitClear = true with an open run under an active policy, want false")
	}
}

// TestStoreApprovalFacts_ApprovedRunIsClear is the permissive control
// for the row above: a method that always answered false would pass that spec for
// free. An approved run clears the gate.
func TestStoreApprovalFacts_ApprovedRunIsClear(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-APPROVED", false)
	runID := seedApprovalRunFor(t, super, fx.tenantID, fx.invID, fx.versionID)
	closeApprovalRunFor(t, super, runID, "approved", "fixture")

	store := NewStore(app)

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

	store := NewStore(app)

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

	store := NewStore(app)

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

// TestStoreApprovalFacts_OneTransaction: db.WithinTenantTx issues exactly one
// set_config('app.current_tenant' per transaction, so the count of those statements
// IS the transaction count. An invoice with an open pending step takes a second
// transaction for the creator read, so its failure cannot roll the first back.
func TestStoreApprovalFacts_OneTransaction(t *testing.T) {
	super, app := dbTestPools(t)
	traced, rec := tracedAppPool(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-ONETX", true)
	fx.armInvoice(t, super, app, "appr-08-05-onetx")

	store := NewStore(traced)
	rec.reset()

	if _, err := store.ApprovalFacts(fx.ctx, fx.invID); err != nil {
		t.Fatalf("ApprovalFacts: %v", err)
	}
	// The request seam batches its set_config, which a QueryTracer alone cannot see —
	// tenantTxCount() counts both routes (db.WithinRequestTenantTxOpts).
	if n := rec.tenantTxCount(); n != 2 {
		t.Errorf("ApprovalFacts opened %d tenant transactions, want 2 (facts, creator standing)", n)
	}
}

// TestStoreApprovalFacts_ErrorReturnsTheZeroValue: a tenant-less ctx must return
// db.ErrNoTenant AND the zero value, whose TransmitClear is false -- so a caller
// that ignores the error still fails closed.
func TestStoreApprovalFacts_ErrorReturnsTheZeroValue(t *testing.T) {
	_, app := dbTestPools(t)

	store := NewStore(app)

	got, err := store.ApprovalFacts(context.Background(), uuid.NewString())
	if !errors.Is(err, db.ErrNoTenant) {
		t.Fatalf("err = %v, want db.ErrNoTenant on an unauthenticated ctx", err)
	}
	if got != (ApprovalFacts{}) {
		t.Errorf("ApprovalFacts = %+v on error, want the zero value -- TransmitClear must read false", got)
	}
}

// TestStoreApprovalFacts_NoRunIsNotClear: the seeded backlog shape --
// validated under an active policy with no run at all. TransmitClear fails closed
// on an absent answer, and RunState is "" rather than an error.
func TestStoreApprovalFacts_NoRunIsNotClear(t *testing.T) {
	super, app := dbTestPools(t)

	fx := seedApprovalFactsFixture(t, super, "APPR-08-05-NORUN", false)

	store := NewStore(app)

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

	store := NewStore(app)

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

// armWithDelegation arms a new invoice and sets its pending step's delegation.
func (fx *approvalFactsFixture) armWithDelegation(t *testing.T, super, app *pgxpool.Pool, number string, allow bool, delegate *string) string {
	t.Helper()
	fx.armInvoice(t, super, app, number)
	mustExec(t, super, `UPDATE approval_run_steps SET allow_delegation = $2, delegate_user_id = $3
		WHERE run_id IN (SELECT id FROM approval_runs WHERE invoice_id = $1)`, fx.invID, allow, delegate)
	return fx.invID
}

// creatorFixture is a staffed admin who created the invoice, plus one other reviewer
// who does not hold the step's role. It returns the reviewer's id.
func creatorFixture(t *testing.T, super *pgxpool.Pool, label string) (approvalFactsFixture, string) {
	t.Helper()
	fx := seedApprovalFactsFixture(t, super, label, true)
	other := uuid.NewString()
	seedMembership(t, super, fx.tenantID, other, "reviewer")
	return fx, other
}

// creatorDoors reads the creator's refusal sentence ("" when not refused) from the
// detail wire, the list wire and the POST approve 403 body.
func creatorDoors(t *testing.T, app *pgxpool.Pool, fx approvalFactsFixture, id string) (detail, list, decide string) {
	t.Helper()
	store := NewStore(app)
	facts, err := store.ApprovalFacts(fx.ctx, id)
	if err != nil || facts.CreatorErr != nil {
		t.Fatalf("ApprovalFacts: %v, %v", err, facts.CreatorErr)
	}
	detail, _ = facts.Creator.Refusal()
	_, gate, err := store.RowFacts(fx.ctx, []string{id})
	if err != nil || gate.CreatorErr != nil {
		t.Fatalf("RowFacts: %v, %v", err, gate.CreatorErr)
	}
	st := gate.Creator[id]
	list, _ = st.Refusal()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/invoices/{id}/approvals", approval.DecideHandler(approvalStoreFor(app).DecideSeam, nil))
	req := httptest.NewRequest("POST", "/v1/invoices/"+id+"/approvals", strings.NewReader(`{"decision":"approved"}`)).WithContext(fx.ctx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	switch rec.Code {
	case http.StatusOK:
	case http.StatusForbidden:
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		decide = body["error"]
	default:
		t.Fatalf("approve status = %d: %s", rec.Code, rec.Body.String())
	}
	return detail, list, decide
}

func TestStoreApprovalFacts_CreatorStandingCountsTheStepDelegate(t *testing.T) {
	super, app := dbTestPools(t)
	fx, other := creatorFixture(t, super, "BUG-118-02-DETAIL")
	both := approval.CreatorRefusedReason + " " + approval.CreatorNoRoleHolderReason
	for _, c := range []struct {
		name     string
		allow    bool
		delegate *string
		other    bool
		want     string
	}{
		{"anyone", true, nil, true, approval.CreatorRefusedReason},
		{"named eligible", true, &other, true, approval.CreatorRefusedReason},
		{"delegation off", false, nil, false, both},
	} {
		id := fx.armWithDelegation(t, super, app, "bug118-detail-"+strings.ReplaceAll(c.name, " ", "-"), c.allow, c.delegate)
		got, err := NewStore(app).ApprovalFacts(fx.ctx, id)
		if err != nil || got.CreatorErr != nil {
			t.Fatalf("%s: ApprovalFacts: %v, %v", c.name, err, got.CreatorErr)
		}
		if got.Creator.OtherDelegate != c.other {
			t.Errorf("%s: OtherDelegate = %t, want %t", c.name, got.Creator.OtherDelegate, c.other)
		}
		if reason, _ := got.Creator.Refusal(); reason != c.want {
			t.Errorf("%s: Refusal = %q, want %q", c.name, reason, c.want)
		}
	}
}

func TestCreatorRefusal_DetailListAndDecideAgree(t *testing.T) {
	super, app := dbTestPools(t)
	both := approval.CreatorRefusedReason + " " + approval.CreatorNoRoleHolderReason
	for _, c := range []struct {
		name  string
		allow bool
		named bool
		want  string
	}{
		{"anyone", true, false, approval.CreatorRefusedReason},
		{"named eligible", true, true, approval.CreatorRefusedReason},
		{"no delegation", false, false, both},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx, other := creatorFixture(t, super, "BUG-118-02-AGREE-"+strings.ReplaceAll(strings.ToUpper(c.name), " ", "-"))
			var delegate *string
			if c.named {
				delegate = &other
			}
			id := fx.armWithDelegation(t, super, app, "bug118-agree", c.allow, delegate)
			detail, list, decide := creatorDoors(t, app, fx, id)
			if detail != c.want || list != c.want || decide != c.want {
				t.Errorf("detail %q, list %q, decide %q, want all %q", detail, list, decide, c.want)
			}
		})
	}
}

func TestCreatorRefusal_NoOtherDeciderKeepsBothSentencesOnAllThreeDoors(t *testing.T) {
	super, app := dbTestPools(t)
	both := approval.CreatorRefusedReason + " " + approval.CreatorNoRoleHolderReason

	t.Run("named delegate suspended", func(t *testing.T) {
		fx, _ := creatorFixture(t, super, "BUG-118-02-NOOTHER-NAMED")
		gone := uuid.NewString()
		seedMembershipWithStatus(t, super, fx.tenantID, gone, "reviewer", "suspended")
		id := fx.armWithDelegation(t, super, app, "bug118-named-suspended", true, &gone)
		detail, list, decide := creatorDoors(t, app, fx, id)
		if detail != both || list != both || decide != both {
			t.Errorf("detail %q, list %q, decide %q, want all %q", detail, list, decide, both)
		}
	})

	// With an anyone step, every other active approver is a delegate, so the creator is
	// refused with one sentence or, with none left, not refused at all.
	t.Run("anyone step, only other approver suspended", func(t *testing.T) {
		fx, other := creatorFixture(t, super, "BUG-118-02-NOOTHER-ANYONE")
		mustExec(t, super, `UPDATE memberships SET status = 'suspended' WHERE tenant_id = $1 AND user_id = $2`, fx.tenantID, other)
		id := fx.armWithDelegation(t, super, app, "bug118-anyone-suspended", true, nil)
		detail, list, decide := creatorDoors(t, app, fx, id)
		if detail != "" || list != "" || decide != "" {
			t.Errorf("detail %q, list %q, decide %q, want none refused", detail, list, decide)
		}
	})

	// The DB check forbids a delegate with delegation off, so only the probe is built here.
	t.Run("delegation off, eligible delegate on the probe", func(t *testing.T) {
		fx, other := creatorFixture(t, super, "BUG-118-02-NOOTHER-OFF")
		id := fx.armWithDelegation(t, super, app, "bug118-off", false, nil)
		key := "finance-lead"
		var got map[string]approval.CreatorStanding
		err := db.WithinRequestTenantTx(fx.ctx, app, func(tx pgx.Tx) error {
			var err error
			got, err = approval.CreatorStandingTx(fx.ctx, tx, fx.subject, []approval.CreatorProbe{{InvoiceID: id, RoleKey: &key, DelegateUserID: &other}})
			return err
		})
		if err != nil {
			t.Fatalf("CreatorStandingTx: %v", err)
		}
		if reason, _ := got[id].Refusal(); reason != both {
			t.Errorf("Refusal = %q, want %q", reason, both)
		}
	})
}
