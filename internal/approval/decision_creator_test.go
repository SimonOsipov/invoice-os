package approval

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/audit"
	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// The story's copy ([message copy]), restated on purpose: the oracle for the shipped constants.
const (
	creatorSentence  = "You created this invoice, so another approver must approve it."
	noHolderSentence = "No other approver holds the role this step is waiting on. An admin must give one of them that role."
)

// memberSpec is one membership: all-clients unless limited, staffed to roleID when set.
type memberSpec struct {
	role, status string
	limited      bool
	entityIDs    []string
	roleID       string
}

func seedMemberOf(t *testing.T, super *pgxpool.Pool, tenantID string, s memberSpec) string {
	t.Helper()
	status := s.status
	if status == "" {
		status = "active"
	}
	user := uuid.NewString()
	seedMembership(t, super, tenantID, user, s.role, status)
	if s.limited {
		limitMemberTo(t, super, tenantID, user, s.entityIDs...)
	}
	if s.roleID != "" {
		var ord int
		if err := super.QueryRow(context.Background(),
			`SELECT count(*) FROM workflow_role_members WHERE workflow_role_id = $1`, s.roleID).Scan(&ord); err != nil {
			t.Fatalf("count role members: %v", err)
		}
		staffWorkflowRole(t, super, tenantID, s.roleID, user, ord)
	}
	return user
}

func limitMemberTo(t *testing.T, super *pgxpool.Pool, tenantID, user string, entityIDs ...string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`UPDATE memberships SET all_clients = false WHERE tenant_id = $1 AND user_id = $2`, tenantID, user); err != nil {
		t.Fatalf("limit member: %v", err)
	}
	for _, e := range entityIDs {
		if _, err := super.Exec(context.Background(),
			`INSERT INTO membership_clients (tenant_id, user_id, entity_id) VALUES ($1, $2, $3)`, tenantID, user, e); err != nil {
			t.Fatalf("seed membership_clients: %v", err)
		}
	}
}

// seedHistory appends one invoice_status_history row; from nil is the creation row.
func seedHistory(t *testing.T, super *pgxpool.Pool, tenantID, invoiceID string, from *string, actor string) {
	t.Helper()
	if _, err := super.Exec(context.Background(),
		`INSERT INTO invoice_status_history (tenant_id, invoice_id, from_status, to_status, actor)
		 VALUES ($1, $2, $3, 'draft', $4)`, tenantID, invoiceID, from, actor); err != nil {
		t.Fatalf("seed invoice_status_history: %v", err)
	}
}

func approvedPayloads(t *testing.T, super *pgxpool.Pool, tenantID string) []map[string]any {
	t.Helper()
	rows, err := super.Query(context.Background(),
		`SELECT payload FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.approval_approved' ORDER BY id`, tenantID)
	if err != nil {
		t.Fatalf("read approval_approved audit rows: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("scan payload: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// wantApprovedAs asserts the tenant has exactly one approval_approved row, carrying want.
func wantApprovedAs(t *testing.T, super *pgxpool.Pool, tenantID, want string) {
	t.Helper()
	rows := approvedPayloads(t, super, tenantID)
	if len(rows) != 1 {
		t.Fatalf("invoice.approval_approved rows = %d, want 1", len(rows))
	}
	if got := rows[0]["approved_as"]; got != want {
		t.Errorf("payload approved_as = %v, want %q", got, want)
	}
}

// wantRefused asserts err is the creator refusal and the wire answers 403 with exactly want.
func wantRefused(t *testing.T, err error, want string) {
	t.Helper()
	if !errors.Is(err, ErrCreatorRefused) {
		t.Fatalf("err = %v, want ErrCreatorRefused", err)
	}
	status, msg := decisionStatusForErr(err)
	if status != http.StatusForbidden || msg != want {
		t.Errorf("wire = %d %q, want 403 %q", status, msg, want)
	}
}

// creatorWorld is the one-step fixture with a creator who holds the role and one colleague
// who holds it too. The creator's history row is seeded by the test, not here.
func creatorWorld(t *testing.T, name string) (super, app *pgxpool.Pool, f approveFixture, creator, colleague string) {
	t.Helper()
	super, app = dbTestPools(t)
	f = newApproveFixture(t, super, app, name, "creator-role")
	creator = seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	colleague = seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	return
}

func TestDecide_CreatorRung_RefusedWithTheExactSentence(t *testing.T) {
	super, app, f, creator, colleague := creatorWorld(t, "APPR-17 refused")
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	_, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil)
	wantRefused(t, err, creatorSentence)
	assertNothingWritten(t, super, f)
	if n := auditCount(t, super, f.tenantID, "invoice.approval_approved"); n != 0 {
		t.Errorf("approval_approved audit rows = %d, want 0 after a refusal", n)
	}

	// Control: the colleague holding the same role is not refused.
	run, err := approve(t, app, f.tenantID, colleague, f.invoiceID, nil)
	if err != nil || run.State != "approved" {
		t.Fatalf("colleague approve = %q, %v, want approved", run.State, err)
	}
}

func TestDecide_CreatorRung_RefusedAtTheSecondStep(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-17 second-step")
	entityID := seedBusinessEntity(t, super, tenantID, "Second step Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "second-step-inv")
	setInvoiceStatus(t, super, invoiceID, "validated")
	roleA := seedWorkflowRole(t, super, tenantID, "step-a", "step-a")
	roleB := seedWorkflowRole(t, super, tenantID, "step-b", "step-b")
	policyID := seedApprovalPolicy(t, super, tenantID, "Second step policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	for ord, key := range []string{"step-a", "step-b"} {
		seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
			Ord: ord, Kind: "approval", WorkflowRoleKey: ptr(key), SLAHours: ptr(48),
		})
	}
	activateApprovalPolicyVersion(t, super, versionID)
	res, err := arm(t, app, tenantID, invoiceID, "fp-second-step", "fixture-arm")
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	creator := seedMemberOf(t, super, tenantID, memberSpec{role: "reviewer", roleID: roleA})
	staffWorkflowRole(t, super, tenantID, roleB, creator, 0)
	colleague := seedMemberOf(t, super, tenantID, memberSpec{role: "reviewer", roleID: roleA})
	staffWorkflowRole(t, super, tenantID, roleB, colleague, 1)
	seedHistory(t, super, tenantID, invoiceID, nil, creator)

	_, err = approve(t, app, tenantID, creator, invoiceID, nil)
	wantRefused(t, err, creatorSentence)
	if n := len(decisionsForRun(t, super, res.RunID)); n != 0 {
		t.Fatalf("decisions after the step-0 refusal = %d, want 0", n)
	}
	if _, err := approve(t, app, tenantID, colleague, invoiceID, nil); err != nil {
		t.Fatalf("colleague approves step 0: %v", err)
	}

	_, err = approve(t, app, tenantID, creator, invoiceID, nil)
	wantRefused(t, err, creatorSentence)
	if n := len(decisionsForRun(t, super, res.RunID)); n != 1 {
		t.Errorf("decisions after the step-1 refusal = %d, want 1 (the colleague's)", n)
	}
	if run, err := approve(t, app, tenantID, colleague, invoiceID, nil); err != nil || run.State != "approved" {
		t.Fatalf("colleague approves step 1 = %q, %v, want approved", run.State, err)
	}
}

func TestDecide_CreatorRung_AppliesToARunOpenedBeforeTheHistoryRowIsRead(t *testing.T) {
	super, app, f, creator, colleague := creatorWorld(t, "APPR-17 open-run")
	// The run is armed already (fixture); the creation row lands only now.
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	_, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil)
	wantRefused(t, err, creatorSentence)
	assertNothingWritten(t, super, f)
	if _, err := approve(t, app, f.tenantID, colleague, f.invoiceID, nil); err != nil {
		t.Errorf("colleague control: %v", err)
	}
}

func TestDecide_OnlyApproverSelfApproves_AuditSaysSo(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-17 self-approve", "creator-role")
	creator := seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	// None of these counts as another approver.
	seedMemberOf(t, super, f.tenantID, memberSpec{role: "preparer", roleID: f.roleID})
	seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", status: "suspended", roleID: f.roleID})
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	run, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil)
	if err != nil || run.State != "approved" {
		t.Fatalf("only approver approves = %q, %v, want approved", run.State, err)
	}
	wantApprovedAs(t, super, f.tenantID, "self_approved")
}

func TestDecide_OrdinaryApproval_AuditSaysApprover(t *testing.T) {
	super, app, f, creator, colleague := creatorWorld(t, "APPR-17 ordinary")
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	if _, err := approve(t, app, f.tenantID, colleague, f.invoiceID, nil); err != nil {
		t.Fatalf("colleague approve: %v", err)
	}
	wantApprovedAs(t, super, f.tenantID, "approver")
}

// The audit search is the only way an auditor finds a self-approval: it matches payload values.
func TestDecide_SelfApproved_FoundByAuditSearch(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-17 audit-search", "creator-role")
	creator := seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)
	// Control: a second invoice with no known creator, approved as an ordinary approver.
	control := seedInvoice(t, super, f.tenantID, f.entityID, "audit-search-control")
	setInvoiceStatus(t, super, control, "validated")
	if _, err := arm(t, app, f.tenantID, control, "fp-audit-search-control", "fixture-arm"); err != nil {
		t.Fatalf("arm control: %v", err)
	}
	for _, id := range []string{f.invoiceID, control} {
		if _, err := approve(t, app, f.tenantID, creator, id, nil); err != nil {
			t.Fatalf("approve %s: %v", id, err)
		}
	}

	// A non-UUID subject is the in-process admin reader.
	ctx := auth.WithIdentity(context.Background(), auth.Identity{Subject: "qa-reader", Role: "authenticated", TenantID: f.tenantID})
	resp, err := audit.NewStore(app).List(ctx, audit.Filter{Limit: 20, Q: "self_approved"})
	if err != nil {
		t.Fatalf("audit List: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("events found by q=self_approved = %d, want exactly the self-approved row", len(resp.Events))
	}
	var payload map[string]any
	if err := json.Unmarshal(resp.Events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["invoice_id"] != f.invoiceID {
		t.Errorf("found row is for invoice %v, want the self-approved %s (control %s must be absent)", payload["invoice_id"], f.invoiceID, control)
	}
}

func TestDecide_CreatorRung_NoOtherHolderAddsTheSecondSentence(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-17 no-holder", "creator-role")
	creator := seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer"}) // reaches the client, lacks the key
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	_, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil)
	wantRefused(t, err, creatorSentence+" "+noHolderSentence)
	assertNothingWritten(t, super, f)
}

func TestDecide_CreatorRung_HolderOutsideTheClientDoesNotCountAsHolder(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-17 outside-holder", "creator-role")
	entB := seedBusinessEntity(t, super, f.tenantID, "Outside holder client B")
	creator := seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", limited: true, entityIDs: []string{entB}, roleID: f.roleID})
	seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer"})
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	_, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil)
	wantRefused(t, err, creatorSentence+" "+noHolderSentence)
}

func TestDecide_CreatorRung_NullRoleKeyReadsAsNoHolder(t *testing.T) {
	super, app := dbTestPools(t)
	tenantID := policyTenant(t, super, "APPR-17 null-key")
	entityID := seedBusinessEntity(t, super, tenantID, "Null key Corp")
	policyID := seedApprovalPolicy(t, super, tenantID, "Null key policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: nil, SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)
	invoiceID := seedInvoice(t, super, tenantID, entityID, "null-key-inv")
	setInvoiceStatus(t, super, invoiceID, "validated")
	if _, err := arm(t, app, tenantID, invoiceID, "fp-null-key", "fixture-arm"); err != nil {
		t.Fatalf("arm: %v", err)
	}
	creator := seedMemberOf(t, super, tenantID, memberSpec{role: "reviewer"})
	seedMemberOf(t, super, tenantID, memberSpec{role: "admin"})
	seedHistory(t, super, tenantID, invoiceID, nil, creator)

	_, err := approve(t, app, tenantID, creator, invoiceID, nil)
	wantRefused(t, err, creatorSentence+" "+noHolderSentence)
}

// A creator's reject is never a creator refusal: the two axes alone decide it.
func TestDecide_CreatorRejects_WithTheRole(t *testing.T) {
	super, app, f, creator, _ := creatorWorld(t, "APPR-17 reject")
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	run, err := reject(t, app, f.tenantID, creator, f.invoiceID, ptr("wrong buyer"))
	if err != nil || run.State != "rejected" {
		t.Fatalf("creator reject = %q, %v, want rejected", run.State, err)
	}
}

func TestDecide_CreatorRejects_WithoutTheRoleIsRoleRefused(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-17 reject-no-role", "creator-role")
	creator := seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer"})
	seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	_, err := reject(t, app, f.tenantID, creator, f.invoiceID, ptr("wrong buyer"))
	if !errors.Is(err, ErrNotRoleHolder) || errors.Is(err, ErrCreatorRefused) {
		t.Errorf("err = %v, want ErrNotRoleHolder and not the creator refusal", err)
	}
	assertNothingWritten(t, super, f)
}

func TestDecide_CreatorRung_AColleagueWhoEditedStillApproves(t *testing.T) {
	super, app, f, creator, colleague := creatorWorld(t, "APPR-17 editor")
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)
	seedHistory(t, super, f.tenantID, f.invoiceID, ptr("draft"), colleague) // the colleague's later edit

	if _, err := approve(t, app, f.tenantID, colleague, f.invoiceID, nil); err != nil {
		t.Fatalf("colleague who edited approves: %v", err)
	}
	wantApprovedAs(t, super, f.tenantID, "approver")
}

func TestDecide_NoFirstRow_NobodyRefused(t *testing.T) {
	super, app, f, creator, _ := creatorWorld(t, "APPR-17 no-first-row")
	// A later row only: no NULL-from row, so no known creator.
	seedHistory(t, super, f.tenantID, f.invoiceID, ptr("draft"), creator)

	if _, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil); err != nil {
		t.Fatalf("approve with no creation row: %v", err)
	}
	wantApprovedAs(t, super, f.tenantID, "approver")
}

func TestDecide_SystemFirstRow_NobodyRefused(t *testing.T) {
	super, app, f, creator, _ := creatorWorld(t, "APPR-17 system-first-row")
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, "system")

	if _, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil); err != nil {
		t.Fatalf("approve a system-created invoice: %v", err)
	}
	wantApprovedAs(t, super, f.tenantID, "approver")
}

func TestDecide_RefusalOrder_CreatorMessageBeatsRole(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-17 order-creator", "creator-role")
	creator := seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer"}) // lacks the key
	seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	_, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil)
	wantRefused(t, err, creatorSentence)
	if errors.Is(err, ErrNotRoleHolder) {
		t.Error("err is also ErrNotRoleHolder, want the creator refusal alone")
	}
}

func TestDecide_RefusalOrder_OnlyApproverWithoutRoleGetsRoleMessage(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-17 order-role", "creator-role")
	creator := seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer"}) // sole approver, no key
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)

	_, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil)
	if !errors.Is(err, ErrNotRoleHolder) || errors.Is(err, ErrCreatorRefused) {
		t.Errorf("err = %v, want ErrNotRoleHolder and not the creator refusal", err)
	}
	assertNothingWritten(t, super, f)
}

func TestDecide_CountTiming_SecondApproverEndsTheException(t *testing.T) {
	super, app := dbTestPools(t)
	f := newApproveFixture(t, super, app, "APPR-17 count-timing", "creator-role")
	creator := seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", roleID: f.roleID})
	seedHistory(t, super, f.tenantID, f.invoiceID, nil, creator)
	// The run is open and the creator is the only approver. A reviewer joins with the client.
	seedMemberOf(t, super, f.tenantID, memberSpec{role: "reviewer", limited: true, entityIDs: []string{f.entityID}})

	_, err := approve(t, app, f.tenantID, creator, f.invoiceID, nil)
	wantRefused(t, err, creatorSentence+" "+noHolderSentence)
	assertNothingWritten(t, super, f)
}
