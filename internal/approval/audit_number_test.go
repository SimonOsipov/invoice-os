// AUDIT-11-02 Mode A: the acceptance tests for "the four invoice-scoped approval
// writers carry invoice_number", authored RED before any writer emits the key.
// Mirrors internal/invoice/audit_number_test.go (subtask 01) in shape and naming.
//
// Sites are addressed by EVENT NAME and function, never by line number alone
// (CF-11). Reuses the dbTestPools/policyTenant/seedBusinessEntity/seedInvoice/
// seedApprovalPolicy*/arm/approve/reject/tracedAppPool harness from this package.
//
// Run: `DEV_DB_PORT=5442 make test-approvals` (go test -p 1).
package approval

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// auditNumberKey is the ONE spelling, verbatim from invoices.invoice_number (D-1, D-2).
const auditNumberKey = "invoice_number"

// approvalAuditSiteCount floors approvalAuditSites: internal/approval holds exactly
// four invoice-scoped audit.Record calls (ArmTx, CancelLiveRunTx, and
// commitDecisionTx's two). The other eight are workspace-level and out of scope
// (D-12). A short table would satisfy every assertion inside the loops vacuously.
const approvalAuditSiteCount = 4

// approvalAuditSite is one invoice-scoped audit.Record call site: how to drive it,
// which id spelling it uses, and the payload key set it wrote before this story.
type approvalAuditSite struct {
	label string
	site  string // file:line at c52b0fa -- re-locate by event name, never by line
	event string
	// idKey is "id" (engine.go) or "invoice_id" (decision.go). The split stays (D-3).
	idKey    string
	baseKeys []string
	// drive seeds a fresh tenant, runs the real writer, and returns
	// (tenantID, invoiceID).
	drive func(t *testing.T, super, app *pgxpool.Pool, label string) (string, string)
}

func approvalAuditSites() []approvalAuditSite {
	return []approvalAuditSite{
		{
			label: "approval_armed", site: "engine.go:239 (ArmTx)", event: "invoice.approval_armed",
			idKey:    "id",
			baseKeys: []string{"id", "run_id", "policy_version_id", "steps"},
			drive:    driveApprovalArmed,
		},
		{
			label: "approval_cancelled", site: "engine.go:300 (CancelLiveRunTx)", event: "invoice.approval_cancelled",
			idKey:    "id",
			baseKeys: []string{"id", "run_id"},
			drive:    driveApprovalCancelled,
		},
		{
			label: "approval_approved", site: "decision.go:283 (commitDecisionTx)", event: "invoice.approval_approved",
			idKey:    "invoice_id",
			baseKeys: []string{"invoice_id", "run_id", "step_ord", "reason"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, label string) (string, string) {
				return driveApprovalDecision(t, super, app, label, "approved")
			},
		},
		{
			label: "approval_rejected", site: "decision.go:311 (commitDecisionTx)", event: "invoice.approval_rejected",
			idKey:    "invoice_id",
			baseKeys: []string{"invoice_id", "run_id", "step_ord", "reason"},
			drive: func(t *testing.T, super, app *pgxpool.Pool, label string) (string, string) {
				return driveApprovalDecision(t, super, app, label, "rejected")
			},
		},
	}
}

// --- fixtures ------------------------------------------------------------------------

// seedActivePolicyTenant is a fresh tenant with one active sealed version naming a
// single approval step -- what ArmTx materialises against.
func seedActivePolicyTenant(t *testing.T, super *pgxpool.Pool, label string) (tenantID, entityID string) {
	t.Helper()
	tenantID = policyTenant(t, super, "AUDIT-11-02 "+label)
	entityID = seedBusinessEntity(t, super, tenantID, "AUDIT-11-02 "+label+" Corp")
	policyID := seedApprovalPolicy(t, super, tenantID, "AUDIT-11-02 "+label+" policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalPolicyStepInLane(t, super, tenantID, versionID, seedStepSpec{
		Ord: 0, Kind: "approval", WorkflowRoleKey: ptr("an-approver"), SLAHours: ptr(48),
	})
	activateApprovalPolicyVersion(t, super, versionID)
	return tenantID, entityID
}

func driveApprovalArmed(t *testing.T, super, app *pgxpool.Pool, label string) (string, string) {
	t.Helper()
	tenantID, entityID := seedActivePolicyTenant(t, super, "armed-"+label)
	invoiceID := seedInvoice(t, super, tenantID, entityID, "AN-ARMED-"+label)
	if _, err := arm(t, app, tenantID, invoiceID, "fp-an-armed-"+label, "an-arm-actor"); err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	return tenantID, invoiceID
}

func driveApprovalCancelled(t *testing.T, super, app *pgxpool.Pool, label string) (string, string) {
	t.Helper()
	tenantID := policyTenant(t, super, "AUDIT-11-02 cancelled-"+label)
	entityID := seedBusinessEntity(t, super, tenantID, "AUDIT-11-02 cancelled-"+label+" Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "AN-CANCELLED-"+label)
	policyID := seedApprovalPolicy(t, super, tenantID, "AUDIT-11-02 cancelled-"+label+" policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalRun(t, super, tenantID, invoiceID, versionID) // defaults to open

	number := mustInvoiceNumberFor(t, super, invoiceID)
	got, err := cancelCarryingNumber(t, app, tenantID, invoiceID, number, "an-cancel-actor")
	if err != nil {
		t.Fatalf("CancelLiveRunTx: %v", err)
	}
	if !got {
		t.Fatal("CancelLiveRunTx returned false, want true -- the fixture's open run was not cancelled, so no audit row exists to assert on")
	}
	return tenantID, invoiceID
}

func driveApprovalDecision(t *testing.T, super, app *pgxpool.Pool, label, decision string) (string, string) {
	t.Helper()
	f := newApproveFixture(t, super, app, "AUDIT-11-02 "+decision+"-"+label, "an-"+decision+"-"+label+"-role")

	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)

	reason := ptr("AN " + decision + " reason")
	var err error
	if decision == "approved" {
		_, err = approve(t, app, f.tenantID, adminID, f.invoiceID, reason)
	} else {
		_, err = reject(t, app, f.tenantID, adminID, f.invoiceID, reason)
	}
	if err != nil {
		t.Fatalf("Decide(%s): %v", decision, err)
	}
	return f.tenantID, f.invoiceID
}

// cancelCarryingNumber runs CancelLiveRunTx in a fresh tenant-scoped transaction the
// way its three production callers do -- holding the invoice's number already.
// Deliberately NOT cancel_test.go's cancel(): CF-10 makes the number a PARAMETER, the
// carrier form D-11 rejected, so these specs must be the ones that choose what value is
// passed. Subtask 02 threads invoiceNumber into the CancelLiveRunTx call below.
func cancelCarryingNumber(t *testing.T, pool *pgxpool.Pool, tenantID, invoiceID, invoiceNumber, actor string) (bool, error) {
	t.Helper()
	if invoiceNumber == "" {
		t.Fatal("cancelCarryingNumber was handed a blank invoice number; every assertion downstream would compare \"\" against \"\" and pass on the exact defect CF-10 names")
	}
	var cancelled bool
	err := db.WithinTenantTx(context.Background(), pool, tenantID, func(tx pgx.Tx) error {
		var err error
		cancelled, err = CancelLiveRunTx(context.Background(), tx, invoiceID, invoiceNumber, actor)
		return err
	})
	return cancelled, err
}

// --- read-back ------------------------------------------------------------------------

// approvalAuditRow is one audit_log row plus BOTH payload-derived columns.
type approvalAuditRow struct {
	rows      int
	payload   json.RawMessage
	number    *string // payload->>'invoice_number'; nil means the key is absent
	invoiceID *string
	entityID  *string
}

// readApprovalAuditRow returns the newest audit_log row for tenantID+event and how
// many rows that event has. ->> yields NULL for an absent key and "" for a present
// empty string, so number distinguishes the two.
func readApprovalAuditRow(t *testing.T, super *pgxpool.Pool, tenantID, event string) approvalAuditRow {
	t.Helper()
	ctx := context.Background()
	var r approvalAuditRow
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE tenant_id = $1 AND event = $2`, tenantID, event,
	).Scan(&r.rows); err != nil {
		t.Fatalf("count audit_log rows for %q: %v", event, err)
	}
	if r.rows == 0 {
		return r // the caller's floor reports this; there is nothing to scan
	}
	if err := super.QueryRow(ctx,
		`SELECT payload, payload->>'`+auditNumberKey+`', invoice_id::text, entity_id::text
		   FROM audit_log WHERE tenant_id = $1 AND event = $2
		  ORDER BY created_at DESC, id DESC LIMIT 1`, tenantID, event,
	).Scan(&r.payload, &r.number, &r.invoiceID, &r.entityID); err != nil {
		t.Fatalf("read audit_log row for %q: %v", event, err)
	}
	return r
}

// assertOneApprovalAuditRow fails when the drive wrote no audit row -- every assertion
// after that would read another test's row or none at all.
func assertOneApprovalAuditRow(t *testing.T, row approvalAuditRow, event, site string) {
	t.Helper()
	if row.rows != 1 {
		t.Fatalf("%s (%s): the tenant holds %d %s audit rows, want exactly 1 -- the fixture did not drive the writer", event, site, row.rows, event)
	}
}

// mustInvoiceNumberFor reads invoices.invoice_number back out of the table, so no
// assertion below compares the payload against a literal the test itself wrote.
func mustInvoiceNumberFor(t *testing.T, super *pgxpool.Pool, invoiceID string) string {
	t.Helper()
	var n string
	if err := super.QueryRow(context.Background(),
		`SELECT invoice_number FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&n); err != nil {
		t.Fatalf("read invoices.invoice_number for %s: %v", invoiceID, err)
	}
	if n == "" {
		t.Fatalf("fixture invoice %s carries a blank invoice_number; every comparison against it would be vacuous", invoiceID)
	}
	return n
}

func mustInvoiceEntityIDFor(t *testing.T, super *pgxpool.Pool, invoiceID string) string {
	t.Helper()
	var e string
	if err := super.QueryRow(context.Background(),
		`SELECT entity_id::text FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&e); err != nil {
		t.Fatalf("read invoices.entity_id for %s: %v", invoiceID, err)
	}
	return e
}

// approvalAuditPayloadKeys returns payload's top-level keys, sorted.
func approvalAuditPayloadKeys(t *testing.T, payload json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("unmarshal audit payload %s: %v", payload, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- AC-1: all four events carry the number -------------------------------------------

// TestAuditNumber_ArmAndCancelCarryTheNumber (AC-1): ArmTx and CancelLiveRunTx are two
// separate writers over one invoice -- widening only the SELECT ArmTx already runs
// leaves CancelLiveRunTx silent, and this is the spec that sees it.
func TestAuditNumber_ArmAndCancelCarryTheNumber(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID, entityID := seedActivePolicyTenant(t, super, "arm-then-cancel")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "AN-ARM-THEN-CANCEL-1")
	want := mustInvoiceNumberFor(t, super, invoiceID)

	if _, err := arm(t, app, tenantID, invoiceID, "fp-an-arm-then-cancel", "an-arm-actor"); err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	armed := readApprovalAuditRow(t, super, tenantID, "invoice.approval_armed")
	assertOneApprovalAuditRow(t, armed, "invoice.approval_armed", "engine.go:239 (ArmTx)")
	if armed.number == nil {
		t.Errorf("invoice.approval_armed (ArmTx): payload->>'%s' is SQL NULL (key absent); payload = %s, want %q", auditNumberKey, armed.payload, want)
	} else if *armed.number != want {
		t.Errorf("invoice.approval_armed (ArmTx): payload->>'%s' = %q, want %q (invoices.invoice_number)", auditNumberKey, *armed.number, want)
	}

	cancelled, err := cancelCarryingNumber(t, app, tenantID, invoiceID, want, "an-cancel-actor")
	if err != nil {
		t.Fatalf("CancelLiveRunTx: %v", err)
	}
	if !cancelled {
		t.Fatal("CancelLiveRunTx returned false, want true -- ArmTx's run is live, so the assertions below would have no row to read")
	}
	cancelledRow := readApprovalAuditRow(t, super, tenantID, "invoice.approval_cancelled")
	assertOneApprovalAuditRow(t, cancelledRow, "invoice.approval_cancelled", "engine.go:300 (CancelLiveRunTx)")
	if cancelledRow.number == nil {
		t.Errorf("invoice.approval_cancelled (CancelLiveRunTx): payload->>'%s' is SQL NULL (key absent); payload = %s, want %q", auditNumberKey, cancelledRow.payload, want)
	} else if *cancelledRow.number != want {
		t.Errorf("invoice.approval_cancelled (CancelLiveRunTx): payload->>'%s' = %q, want %q (invoices.invoice_number)", auditNumberKey, *cancelledRow.number, want)
	}
}

// TestAuditNumber_ApproveAndRejectCarryTheNumber (AC-1): both decision writers live
// inside commitDecisionTx and are fed by decideTx's FOR UPDATE read -- but they are two
// distinct audit.Record calls, so one can be widened and the other missed.
func TestAuditNumber_ApproveAndRejectCarryTheNumber(t *testing.T) {
	super, app := dbTestPools(t)

	for _, decision := range []string{"approved", "rejected"} {
		t.Run(decision, func(t *testing.T) {
			tenantID, invoiceID := driveApprovalDecision(t, super, app, "carry", decision)
			want := mustInvoiceNumberFor(t, super, invoiceID)
			event := "invoice.approval_" + decision

			row := readApprovalAuditRow(t, super, tenantID, event)
			assertOneApprovalAuditRow(t, row, event, "decision.go (commitDecisionTx)")
			if row.number == nil {
				t.Fatalf("%s (commitDecisionTx): payload->>'%s' is SQL NULL (key absent); payload = %s, want %q", event, auditNumberKey, row.payload, want)
			}
			if *row.number != want {
				t.Errorf("%s (commitDecisionTx): payload->>'%s' = %q, want %q (invoices.invoice_number)", event, auditNumberKey, *row.number, want)
			}
		})
	}
}

// TestAuditNumber_CancelAuditsEveryLiveRunWithTheNumber (AC-1): approval_runs_one_open
// constrains only 'open', so one invoice can hold an 'approved' AND an 'open' run, and
// CancelLiveRunTx audits each in a loop. A single-run fixture cannot tell a number that
// reached every row from one that reached only the first.
func TestAuditNumber_CancelAuditsEveryLiveRunWithTheNumber(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := policyTenant(t, super, "AUDIT-11-02 cancel-every-live-run")
	entityID := seedBusinessEntity(t, super, tenantID, "AUDIT-11-02 Every Live Run Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "AN-CANCEL-EVERY-RUN-1")
	policyID := seedApprovalPolicy(t, super, tenantID, "AUDIT-11-02 every-live-run policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)

	// Seeded approved-first: seedApprovalRun inserts state='open' and
	// approval_runs_one_open admits only one open run per invoice.
	approvedRunID := seedApprovalRun(t, super, tenantID, invoiceID, versionID)
	if _, err := super.Exec(ctx,
		`UPDATE approval_runs SET state = 'approved', closed_at = now(), closed_by = 'system' WHERE id = $1`,
		approvedRunID); err != nil {
		t.Fatalf("seed the approved run: %v", err)
	}
	openRunID := seedApprovalRun(t, super, tenantID, invoiceID, versionID)

	want := mustInvoiceNumberFor(t, super, invoiceID)
	got, err := cancelCarryingNumber(t, app, tenantID, invoiceID, want, "an-cancel-every-actor")
	if err != nil {
		t.Fatalf("CancelLiveRunTx: %v", err)
	}
	if !got {
		t.Fatal("CancelLiveRunTx returned false, want true -- two live runs exist")
	}

	rows, err := super.Query(ctx,
		`SELECT payload->>'run_id', payload->>'`+auditNumberKey+`', payload::text
		   FROM audit_log WHERE tenant_id = $1 AND event = 'invoice.approval_cancelled' ORDER BY id`,
		tenantID)
	if err != nil {
		t.Fatalf("read the invoice.approval_cancelled rows: %v", err)
	}
	defer rows.Close()
	byRun := map[string]*string{}
	payloads := map[string]string{}
	for rows.Next() {
		var runID, payload string
		var number *string
		if err := rows.Scan(&runID, &number, &payload); err != nil {
			t.Fatalf("scan an invoice.approval_cancelled row: %v", err)
		}
		byRun[runID] = number
		payloads[runID] = payload
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the invoice.approval_cancelled rows: %v", err)
	}
	if len(byRun) != 2 {
		t.Fatalf("invoice.approval_cancelled rows = %d, want exactly 2 (one per cancelled run) -- the per-row assertions below would be vacuous", len(byRun))
	}

	for label, runID := range map[string]string{"approved run": approvedRunID, "open run": openRunID} {
		number, ok := byRun[runID]
		if !ok {
			t.Fatalf("no invoice.approval_cancelled row names the %s (%s); rows = %v", label, runID, payloads)
		}
		if number == nil {
			t.Errorf("invoice.approval_cancelled for the %s: payload->>'%s' is SQL NULL (key absent); payload = %s, want %q", label, auditNumberKey, payloads[runID], want)
			continue
		}
		if *number != want {
			t.Errorf("invoice.approval_cancelled for the %s: payload->>'%s' = %q, want %q -- the number must reach EVERY audited run, not just the first", label, auditNumberKey, *number, want)
		}
	}
}

// TestAuditNumber_ApprovalPayloadKeysAreOnlyWidened (AC-1): each payload is WIDENED,
// never rewritten -- every pre-change key survives and exactly one is added, asserted as
// set equality in BOTH directions. A writer that replaced "id"/"invoice_id" with the
// number would NULL audit_log.invoice_id and entity_id for every future row and still
// satisfy a presence-only check.
func TestAuditNumber_ApprovalPayloadKeysAreOnlyWidened(t *testing.T) {
	super, app := dbTestPools(t)

	sites := approvalAuditSites()
	if len(sites) != approvalAuditSiteCount {
		t.Fatalf("approvalAuditSites holds %d rows, want %d -- a short table passes every assertion below vacuously", len(sites), approvalAuditSiteCount)
	}

	for _, s := range sites {
		t.Run(s.label, func(t *testing.T) {
			if len(s.baseKeys) == 0 {
				t.Fatalf("%s (%s): baseKeys is empty; set equality against an empty want proves nothing", s.event, s.site)
			}
			tenantID, invoiceID := s.drive(t, super, app, "keys")

			row := readApprovalAuditRow(t, super, tenantID, s.event)
			assertOneApprovalAuditRow(t, row, s.event, s.site)

			want := append(append([]string{}, s.baseKeys...), auditNumberKey)
			sort.Strings(want)
			got := approvalAuditPayloadKeys(t, row.payload)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("%s (%s): payload keys = [%s], want [%s] (every pre-change key, plus exactly %q); payload = %s",
					s.event, s.site, strings.Join(got, ","), strings.Join(want, ","), auditNumberKey, row.payload)
			}

			// The load-bearing survivor: both payload readers address this key alone.
			var decoded map[string]any
			if err := json.Unmarshal(row.payload, &decoded); err != nil {
				t.Fatalf("unmarshal payload %s: %v", row.payload, err)
			}
			if decoded[s.idKey] != invoiceID {
				t.Errorf("%s (%s): payload %s = %v, want %q (unchanged -- the id/invoice_id split stays, D-3)", s.event, s.site, s.idKey, decoded[s.idKey], invoiceID)
			}
		})
	}
}

// TestAuditNumber_ScopedColumnsStillFillForApprovalEvents (AC-1, CF-4): BOTH
// payload-derived columns still fill once the sibling key is there.
// audit_log.invoice_id is a STORED generated column and audit_log.entity_id is filled by
// the audit_log_entity_on_insert trigger; both read the same ->>'id' / ->>'invoice_id'
// grammar and neither iterates the key set. An invoice_id-only assertion would not see
// entity_id regress.
func TestAuditNumber_ScopedColumnsStillFillForApprovalEvents(t *testing.T) {
	super, app := dbTestPools(t)

	sites := approvalAuditSites()
	if len(sites) != approvalAuditSiteCount {
		t.Fatalf("approvalAuditSites holds %d rows, want %d -- a short table passes every assertion below vacuously", len(sites), approvalAuditSiteCount)
	}

	for _, s := range sites {
		t.Run(s.label, func(t *testing.T) {
			tenantID, invoiceID := s.drive(t, super, app, "scoped")

			row := readApprovalAuditRow(t, super, tenantID, s.event)
			assertOneApprovalAuditRow(t, row, s.event, s.site)

			// "with the new key present" is half the claim: without this the test would
			// stay green on a payload this story never touched.
			if row.number == nil {
				t.Fatalf("%s (%s): payload->>'%s' is SQL NULL (key absent), so this test is not yet asserting what its name says; payload = %s", s.event, s.site, auditNumberKey, row.payload)
			}

			if row.invoiceID == nil {
				t.Errorf("%s (%s): audit_log.invoice_id is NULL with %q present; payload = %s", s.event, s.site, auditNumberKey, row.payload)
			} else if *row.invoiceID != invoiceID {
				t.Errorf("%s (%s): audit_log.invoice_id = %q, want %q", s.event, s.site, *row.invoiceID, invoiceID)
			}

			wantEntity := mustInvoiceEntityIDFor(t, super, invoiceID)
			if row.entityID == nil {
				t.Errorf("%s (%s): audit_log.entity_id is NULL with %q present, which reads as a firm-wide claim; payload = %s", s.event, s.site, auditNumberKey, row.payload)
			} else if *row.entityID != wantEntity {
				t.Errorf("%s (%s): audit_log.entity_id = %q, want %q (the invoice's entity)", s.event, s.site, *row.entityID, wantEntity)
			}
		})
	}
}

// --- AC-2: no new statement on the arm/decide paths -----------------------------------

// TestAuditNumber_ArmIssuesNoExtraStatement (AC-2): ArmTx already reads the invoice
// (SELECT total::text FROM invoices WHERE id = $1). Widening THAT statement is free; a
// second SELECT is a new round trip on the promotion path. The number assertion runs
// first on purpose -- without it the count below passes on unmodified code and asserts
// nothing this story is about.
func TestAuditNumber_ArmIssuesNoExtraStatement(t *testing.T) {
	super, _ := dbTestPools(t)
	traced, rec := tracedAppPool(t)

	tenantID, entityID := seedActivePolicyTenant(t, super, "arm-statement-count")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "AN-ARM-STMT-1")
	setInvoiceTotal(t, super, invoiceID, "1000.00")
	want := mustInvoiceNumberFor(t, super, invoiceID)

	rec.reset()
	if _, err := arm(t, traced, tenantID, invoiceID, "fp-an-arm-stmt", "an-arm-stmt-actor"); err != nil {
		t.Fatalf("ArmTx: %v", err)
	}
	stmts := rec.mentioning("invoices")

	row := readApprovalAuditRow(t, super, tenantID, "invoice.approval_armed")
	assertOneApprovalAuditRow(t, row, "invoice.approval_armed", "engine.go:239 (ArmTx)")
	if row.number == nil {
		t.Fatalf("invoice.approval_armed: payload->>'%s' is SQL NULL (key absent), so the statement count below is not yet asserting what this test's name says; payload = %s", auditNumberKey, row.payload)
	}
	if *row.number != want {
		t.Errorf("invoice.approval_armed: payload->>'%s' = %q, want %q", auditNumberKey, *row.number, want)
	}
	if len(stmts) != 1 {
		t.Errorf("ArmTx issued %d statement(s) against invoices, want exactly 1 (the widened SELECT, same round trip): %v", len(stmts), stmts)
	}
}

// TestAuditNumber_DecideIssuesNoExtraInvoiceStatement (AC-2): decideTx already reads the
// invoice (SELECT status FROM invoices WHERE id = $1 FOR UPDATE) and the number must
// ride that statement through commitDecisionTx -- a lookup inside commitDecisionTx would
// run on the request path. Drives "approved": reject()'s demoter is a stub here, but the
// real one touches invoices and would blur the count.
func TestAuditNumber_DecideIssuesNoExtraInvoiceStatement(t *testing.T) {
	super, app := dbTestPools(t)

	f := newApproveFixture(t, super, app, "AUDIT-11-02 decide-statement-count", "an-decide-stmt-role")
	adminID := uuid.NewString()
	seedMembership(t, super, f.tenantID, adminID, "admin", "active")
	staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)
	want := mustInvoiceNumberFor(t, super, f.invoiceID)

	traced, rec := tracedAppPool(t)
	c := auth.WithIdentity(context.Background(),
		auth.Identity{Subject: adminID, Role: "authenticated", TenantID: f.tenantID})

	rec.reset()
	if _, err := NewStore(traced, stubFingerprinter, nil).Decide(c, f.invoiceID, "approved", ptr("AN decide stmt reason")); err != nil {
		t.Fatalf("Decide(approved): %v", err)
	}
	stmts := rec.mentioning("invoices")

	row := readApprovalAuditRow(t, super, f.tenantID, "invoice.approval_approved")
	assertOneApprovalAuditRow(t, row, "invoice.approval_approved", "decision.go:283 (commitDecisionTx)")
	if row.number == nil {
		t.Fatalf("invoice.approval_approved: payload->>'%s' is SQL NULL (key absent), so the statement count below is not yet asserting what this test's name says; payload = %s", auditNumberKey, row.payload)
	}
	if *row.number != want {
		t.Errorf("invoice.approval_approved: payload->>'%s' = %q, want %q", auditNumberKey, *row.number, want)
	}
	if len(stmts) != 1 {
		t.Errorf("Decide issued %d statement(s) against invoices, want exactly 1 (the widened FOR UPDATE read): %v", len(stmts), stmts)
	}
}

// --- AC-3: the canceller looks nothing up ---------------------------------------------

// TestAuditNumber_CancelTakesTheNumberFromItsCaller (AC-3): CancelLiveRunTx runs once
// per demotion on the request path and must issue NO statement of its own against
// invoices -- the number arrives as an argument. The number assertion is what makes the
// zero-count meaningful: today the count is zero because nothing needs the number yet.
func TestAuditNumber_CancelTakesTheNumberFromItsCaller(t *testing.T) {
	super, _ := dbTestPools(t)
	traced, rec := tracedAppPool(t)

	tenantID := policyTenant(t, super, "AUDIT-11-02 cancel-statement-count")
	entityID := seedBusinessEntity(t, super, tenantID, "AUDIT-11-02 Cancel Stmt Corp")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "AN-CANCEL-STMT-1")
	policyID := seedApprovalPolicy(t, super, tenantID, "AUDIT-11-02 cancel-stmt policy")
	versionID := seedApprovalPolicyVersionN(t, super, tenantID, policyID, 1)
	seedApprovalRun(t, super, tenantID, invoiceID, versionID)
	want := mustInvoiceNumberFor(t, super, invoiceID)

	rec.reset()
	got, err := cancelCarryingNumber(t, traced, tenantID, invoiceID, want, "an-cancel-stmt-actor")
	if err != nil {
		t.Fatalf("CancelLiveRunTx: %v", err)
	}
	if !got {
		t.Fatal("CancelLiveRunTx returned false, want true")
	}
	stmts := rec.mentioning("invoices")

	row := readApprovalAuditRow(t, super, tenantID, "invoice.approval_cancelled")
	assertOneApprovalAuditRow(t, row, "invoice.approval_cancelled", "engine.go:300 (CancelLiveRunTx)")
	if row.number == nil {
		t.Fatalf("invoice.approval_cancelled: payload->>'%s' is SQL NULL (key absent), so the statement count below is not yet asserting what this test's name says; payload = %s", auditNumberKey, row.payload)
	}
	if *row.number != want {
		t.Errorf("invoice.approval_cancelled: payload->>'%s' = %q, want %q", auditNumberKey, *row.number, want)
	}
	if len(stmts) != 0 {
		t.Errorf("CancelLiveRunTx issued %d statement(s) against invoices, want 0 (the number arrives as an argument): %v", len(stmts), stmts)
	}
}

// --- AC-4: the workspace-level writers gain nothing -----------------------------------

// workspaceAuditSite is one workspace-level audit.Record call site in this package.
type workspaceAuditSite struct {
	label string
	site  string // file:line at c52b0fa
	event string
	drive func(t *testing.T, super, app *pgxpool.Pool) string // returns tenantID
}

// workspaceAuditSiteCount floors workspaceAuditSites: internal/approval holds twelve
// audit.Record calls, four invoice-scoped and these eight.
const workspaceAuditSiteCount = 8

func workspaceAuditSites() []workspaceAuditSite {
	return []workspaceAuditSite{
		{
			label: "policy_created", site: "policy_store.go:338", event: "approval_policy.created",
			drive: func(t *testing.T, super, app *pgxpool.Pool) string {
				tenantID := policyTenant(t, super, "AUDIT-11-02 ws policy-created")
				c, _ := activeAdmin(t, super, tenantID)
				if _, err := NewStore(app, stubFingerprinter, nil).CreatePolicy(c, "AN workspace policy", ""); err != nil {
					t.Fatalf("CreatePolicy: %v", err)
				}
				return tenantID
			},
		},
		{
			label: "policy_updated", site: "policy_store.go:508", event: "approval_policy.updated",
			drive: func(t *testing.T, super, app *pgxpool.Pool) string {
				tenantID := policyTenant(t, super, "AUDIT-11-02 ws policy-updated")
				c, _ := activeAdmin(t, super, tenantID)
				seedWorkflowRole(t, super, tenantID, "an-ws-partner", "AN WS Partner")
				policyID := seedApprovalPolicy(t, super, tenantID, "AN workspace put-draft")
				if _, err := NewStore(app, stubFingerprinter, nil).PutDraft(c, policyID, nil, nil, approvalStep("an-ws-partner")); err != nil {
					t.Fatalf("PutDraft: %v", err)
				}
				return tenantID
			},
		},
		{
			label: "policy_published", site: "policy_store.go:680", event: "approval_policy.published",
			drive: func(t *testing.T, super, app *pgxpool.Pool) string {
				tenantID := policyTenant(t, super, "AUDIT-11-02 ws policy-published")
				c, _ := activeAdmin(t, super, tenantID)
				seedWorkflowRole(t, super, tenantID, "an-ws-publisher", "AN WS Publisher")
				policyID := seedApprovalPolicy(t, super, tenantID, "AN workspace publish")
				seedApprovalDraftNamingRole(t, super, tenantID, policyID, "an-ws-publisher")
				if _, err := NewStore(app, stubFingerprinter, nil).PublishPolicy(c, policyID); err != nil {
					t.Fatalf("PublishPolicy: %v", err)
				}
				return tenantID
			},
		},
		{
			label: "policy_deleted", site: "policy_store.go:842", event: "approval_policy.deleted",
			drive: func(t *testing.T, super, app *pgxpool.Pool) string {
				tenantID := policyTenant(t, super, "AUDIT-11-02 ws policy-deleted")
				c, _ := activeAdmin(t, super, tenantID)
				policyID := seedApprovalPolicy(t, super, tenantID, "AN workspace delete")
				seedVersionWithSteps(t, super, tenantID, policyID, 1, 1)
				if _, err := NewStore(app, stubFingerprinter, nil).DeletePolicy(c, policyID); err != nil {
					t.Fatalf("DeletePolicy: %v", err)
				}
				return tenantID
			},
		},
		{
			label: "role_created", site: "store.go:213", event: "workflow_role.created",
			drive: func(t *testing.T, super, app *pgxpool.Pool) string {
				tenantID := policyTenant(t, super, "AUDIT-11-02 ws role-created")
				c, _ := activeAdmin(t, super, tenantID)
				if _, err := NewStore(app, stubFingerprinter, nil).CreateRole(c, "AN Workspace Reviewer", "desc"); err != nil {
					t.Fatalf("CreateRole: %v", err)
				}
				return tenantID
			},
		},
		{
			label: "role_updated", site: "store.go:318", event: "workflow_role.updated",
			drive: func(t *testing.T, super, app *pgxpool.Pool) string {
				tenantID := policyTenant(t, super, "AUDIT-11-02 ws role-updated")
				c, _ := activeAdmin(t, super, tenantID)
				store := NewStore(app, stubFingerprinter, nil)
				created, err := store.CreateRole(c, "AN Workspace Reviewer", "desc")
				if err != nil {
					t.Fatalf("CreateRole: %v", err)
				}
				if _, err := store.UpdateRole(c, created.Key, ptr("AN Workspace Reviewer II"), nil); err != nil {
					t.Fatalf("UpdateRole: %v", err)
				}
				return tenantID
			},
		},
		{
			label: "role_deleted", site: "store.go:368", event: "workflow_role.deleted",
			drive: func(t *testing.T, super, app *pgxpool.Pool) string {
				tenantID := policyTenant(t, super, "AUDIT-11-02 ws role-deleted")
				c, _ := activeAdmin(t, super, tenantID)
				store := NewStore(app, stubFingerprinter, nil)
				created, err := store.CreateRole(c, "AN Workspace Reviewer", "desc")
				if err != nil {
					t.Fatalf("CreateRole: %v", err)
				}
				if _, err := store.DeleteRole(c, created.Key); err != nil {
					t.Fatalf("DeleteRole: %v", err)
				}
				return tenantID
			},
		},
		{
			label: "role_staffed", site: "store.go:467", event: "workflow_role.staffed",
			drive: func(t *testing.T, super, app *pgxpool.Pool) string {
				tenantID := policyTenant(t, super, "AUDIT-11-02 ws role-staffed")
				c, _ := activeAdmin(t, super, tenantID)
				memberID := uuid.NewString()
				seedMembership(t, super, tenantID, memberID, "reviewer", "active")
				store := NewStore(app, stubFingerprinter, nil)
				created, err := store.CreateRole(c, "AN Workspace Reviewer", "desc")
				if err != nil {
					t.Fatalf("CreateRole: %v", err)
				}
				if _, err := store.SetRoleMembers(c, created.Key, []string{memberID}); err != nil {
					t.Fatalf("SetRoleMembers: %v", err)
				}
				return tenantID
			},
		},
	}
}

// TestAuditNumber_WorkspaceLevelApprovalEventsGainNothing (AC-4, D-12): the eight
// workspace-level writers in this package are outside the 17 and gain no key. A blanket
// package edit would add it where both scoped columns ignore it, making the row
// half-claim an invoice scope it does not have -- which is why entity_id and invoice_id
// are asserted NULL beside the key's absence.
//
// GREEN before implementation by construction: this is AC-4's anti-regression oracle,
// and the four pre-existing [policy_id version] exact-key guards are its primary one.
func TestAuditNumber_WorkspaceLevelApprovalEventsGainNothing(t *testing.T) {
	super, app := dbTestPools(t)

	sites := workspaceAuditSites()
	if len(sites) != workspaceAuditSiteCount {
		t.Fatalf("workspaceAuditSites holds %d rows, want %d -- a short table passes every assertion below vacuously", len(sites), workspaceAuditSiteCount)
	}

	for _, s := range sites {
		t.Run(s.label, func(t *testing.T) {
			tenantID := s.drive(t, super, app)

			row := readApprovalAuditRow(t, super, tenantID, s.event)
			assertOneApprovalAuditRow(t, row, s.event, s.site)

			keys := approvalAuditPayloadKeys(t, row.payload)
			if len(keys) == 0 {
				t.Fatalf("%s (%s): payload is empty {}, so the absence assertion below proves nothing", s.event, s.site)
			}
			if row.number != nil {
				t.Errorf("%s (%s): payload carries %q = %q; workspace-level events are outside the 17 (D-12) and neither scoped column reads it, so the row would half-claim an invoice scope; payload = %s",
					s.event, s.site, auditNumberKey, *row.number, row.payload)
			}
			if row.invoiceID != nil {
				t.Errorf("%s (%s): audit_log.invoice_id = %q, want NULL (workspace-level)", s.event, s.site, *row.invoiceID)
			}
			if row.entityID != nil {
				t.Errorf("%s (%s): audit_log.entity_id = %q, want NULL (workspace-level)", s.event, s.site, *row.entityID)
			}
		})
	}
}
