// Adversarial siblings of audit_number_test.go for the four approval writers: a
// hostile number through both payload spellings, interleaved arms, a concurrent
// arm/cancel, and the FK that stops a cancel's number outliving its row.
// Run: `DEV_DB_PORT=5442 make test-approvals`.
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// hostileNumber closes the "id" string, re-points it at decoyID, then trails a
// backslash, an SQL wildcard pair, a tab, a newline and a 4-byte rune.
func hostileNumber(decoyID string) string {
	return `", "id": "` + decoyID + `", "z": "\" %_` + "\t\n" + `🧾`
}

// setInvoiceNumber rewrites the column the four writers read. No trigger guards it.
func setInvoiceNumber(t *testing.T, super *pgxpool.Pool, invoiceID, number string) {
	t.Helper()
	tag, err := super.Exec(context.Background(),
		`UPDATE invoices SET invoice_number = $2 WHERE id = $1`, invoiceID, number)
	if err != nil {
		t.Fatalf("set invoice %s number: %v", invoiceID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("set invoice %s number affected %d rows, want 1", invoiceID, tag.RowsAffected())
	}
}

// oneActivePolicyVersionID is the tenant's single active version -- what
// seedActivePolicyTenant left behind and what a seeded run must reference.
func oneActivePolicyVersionID(t *testing.T, super *pgxpool.Pool, tenantID string) string {
	t.Helper()
	var id string
	if err := super.QueryRow(context.Background(),
		`SELECT id FROM approval_policy_versions WHERE tenant_id = $1 AND is_active`, tenantID,
	).Scan(&id); err != nil {
		t.Fatalf("read the active policy version for %s: %v", tenantID, err)
	}
	return id
}

// approvalAuditRowForInvoice reads the newest audit_log row for event whose generated
// invoice_id column is invoiceID. Addressing by that column rather than by recency is
// what lets the concurrency specs tell interleaved writers' rows apart.
func approvalAuditRowForInvoice(t *testing.T, super *pgxpool.Pool, event, invoiceID string) approvalAuditRow {
	t.Helper()
	ctx := context.Background()
	var r approvalAuditRow
	if err := super.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE event = $1 AND invoice_id = $2::uuid`, event, invoiceID,
	).Scan(&r.rows); err != nil {
		t.Fatalf("count %s rows for %s: %v", event, invoiceID, err)
	}
	if r.rows == 0 {
		return r
	}
	if err := super.QueryRow(ctx,
		`SELECT payload, payload->>'`+auditNumberKey+`', invoice_id::text, entity_id::text
		   FROM audit_log WHERE event = $1 AND invoice_id = $2::uuid
		  ORDER BY created_at DESC, id DESC LIMIT 1`, event, invoiceID,
	).Scan(&r.payload, &r.number, &r.invoiceID, &r.entityID); err != nil {
		t.Fatalf("read %s row for %s: %v", event, invoiceID, err)
	}
	return r
}

// assertNumberStayedInItsValue: the number reached the payload byte-identically, it
// did not become a second "id", and both payload-derived columns still resolve to the
// invoice the row is about.
func assertNumberStayedInItsValue(t *testing.T, super *pgxpool.Pool, row approvalAuditRow, event, idKey, invoiceID string, baseKeys []string, want string) {
	t.Helper()
	if row.rows != 1 {
		t.Fatalf("%s rows = %d, want exactly 1 -- the fixture did not drive the writer", event, row.rows)
	}

	wantKeys := append(append([]string{}, baseKeys...), auditNumberKey)
	sort.Strings(wantKeys)
	got := approvalAuditPayloadKeys(t, row.payload)
	if strings.Join(got, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("%s: payload keys = [%s], want [%s] -- the number escaped its own value; payload = %s",
			event, strings.Join(got, ","), strings.Join(wantKeys, ","), row.payload)
	}

	var decoded map[string]any
	if err := json.Unmarshal(row.payload, &decoded); err != nil {
		t.Fatalf("unmarshal %s payload %s: %v", event, row.payload, err)
	}
	if decoded[idKey] != invoiceID {
		t.Errorf("%s: payload %s = %v, want %q -- the number re-pointed the row", event, idKey, decoded[idKey], invoiceID)
	}
	if row.invoiceID == nil || *row.invoiceID != invoiceID {
		t.Errorf("%s: audit_log.invoice_id = %v, want %q", event, row.invoiceID, invoiceID)
	}
	if wantEntity := mustInvoiceEntityIDFor(t, super, invoiceID); row.entityID == nil || *row.entityID != wantEntity {
		t.Errorf("%s: audit_log.entity_id = %v, want %q", event, row.entityID, wantEntity)
	}
	if row.number == nil {
		t.Fatalf("%s: payload->>'%s' is SQL NULL; payload = %s", event, auditNumberKey, row.payload)
	}
	if *row.number != want {
		t.Errorf("%s: payload->>'%s' = %q, want %q byte-identically", event, auditNumberKey, *row.number, want)
	}
}

// A number is caller-controlled text with no CHECK behind it, and both payload
// spellings put it beside the "id"/"invoice_id" key that audit_log's generated column
// and its entity trigger read. The acceptance fixtures all use tame numbers.
func TestAuditNumberAdversarial_AJSONShapedNumberCannotRewriteTheApprovalIdKeys(t *testing.T) {
	super, app := dbTestPools(t)

	t.Run("arm and cancel", func(t *testing.T) {
		tenantID, entityID := seedActivePolicyTenant(t, super, "hostile-arm-cancel")
		decoyID := seedInvoice(t, super, tenantID, entityID, "AN-ADV-APPR-DECOY")
		want := hostileNumber(decoyID)
		invoiceID := seedInvoice(t, super, tenantID, entityID, want)

		if _, err := arm(t, app, tenantID, invoiceID, "fp-an-adv-hostile", "an-adv-arm-actor"); err != nil {
			t.Fatalf("ArmTx: %v", err)
		}
		assertNumberStayedInItsValue(t, super,
			approvalAuditRowForInvoice(t, super, "invoice.approval_armed", invoiceID),
			"invoice.approval_armed", "id", invoiceID,
			[]string{"id", "run_id", "policy_version_id", "steps"}, want)

		if _, err := cancelCarryingNumber(t, app, tenantID, invoiceID, want, "an-adv-cancel-actor"); err != nil {
			t.Fatalf("CancelLiveRunTx: %v", err)
		}
		assertNumberStayedInItsValue(t, super,
			approvalAuditRowForInvoice(t, super, "invoice.approval_cancelled", invoiceID),
			"invoice.approval_cancelled", "id", invoiceID,
			[]string{"id", "run_id"}, want)
	})

	// decision.go spells it "invoice_id" and the number rides decideTx's FOR UPDATE
	// read into commitDecisionTx -- a different carrier from ArmTx's.
	t.Run("approved", func(t *testing.T) {
		f := newApproveFixture(t, super, app, "AUDIT-11-02 hostile-approve", "an-adv-hostile-role")
		decoyID := seedInvoice(t, super, f.tenantID, f.entityID, "AN-ADV-APPR-DECOY-2")
		want := hostileNumber(decoyID)
		setInvoiceNumber(t, super, f.invoiceID, want)

		adminID := uuid.NewString()
		seedMembership(t, super, f.tenantID, adminID, "admin", "active")
		staffWorkflowRole(t, super, f.tenantID, f.roleID, adminID, 0)
		if _, err := approve(t, app, f.tenantID, adminID, f.invoiceID, ptr("AN adv hostile reason")); err != nil {
			t.Fatalf("Decide(approved): %v", err)
		}

		assertNumberStayedInItsValue(t, super,
			approvalAuditRowForInvoice(t, super, "invoice.approval_approved", f.invoiceID),
			"invoice.approval_approved", "invoice_id", f.invoiceID,
			[]string{"invoice_id", "run_id", "step_ord", "reason"}, want)
	})
}

// Every acceptance spec arms ONE invoice per tenant, serially, so a number bound to
// the wrong request -- a hoisted variable, a memoised lookup -- still reads back
// correctly there. ArmTx's number now rides the SELECT it shares with the total, so
// interleaved arms over distinct invoices are what make a cross visible.
func TestAuditNumberAdversarial_ConcurrentArmsNeverCrossNumbers(t *testing.T) {
	super, app := dbTestPools(t)

	const writers = 8
	tenantID, entityID := seedActivePolicyTenant(t, super, "concurrent-arms")

	ids := make([]string, writers)
	numbers := make([]string, writers)
	for i := range ids {
		numbers[i] = fmt.Sprintf("AN-ADV-APPR-CONC-%02d", i)
		ids[i] = seedInvoice(t, super, tenantID, entityID, numbers[i])
	}

	var wg sync.WaitGroup
	errs := make([]error, writers)
	wg.Add(writers)
	for i := range ids {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = arm(t, app, tenantID, ids[i], fmt.Sprintf("fp-an-adv-conc-%02d", i), "an-adv-conc-actor")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent ArmTx %d: %v", i, err)
		}
	}

	for i, id := range ids {
		row := approvalAuditRowForInvoice(t, super, "invoice.approval_armed", id)
		if row.rows != 1 {
			t.Fatalf("invoice %s (%s) has %d invoice.approval_armed rows, want 1", numbers[i], id, row.rows)
		}
		if row.number == nil {
			t.Fatalf("invoice %s: payload->>'%s' is SQL NULL; payload = %s", numbers[i], auditNumberKey, row.payload)
		}
		if *row.number != numbers[i] {
			t.Errorf("invoice %s (%s): payload->>'%s' = %q, want %q -- a concurrent arm's number reached the wrong row",
				numbers[i], id, auditNumberKey, *row.number, numbers[i])
		}
	}
}

// The two engine.go writers race on ONE invoice: CancelLiveRunTx closes the seeded
// open run while ArmTx opens another. CancelLiveRunTx's number is a parameter and
// ArmTx's rides a SELECT, so an interleaving that mixed the two would show up nowhere
// else. Whichever order approval_runs_one_open admits, no row may carry a blank or
// another invoice's number.
func TestAuditNumberAdversarial_ConcurrentArmAndCancelCarryTheSameNumber(t *testing.T) {
	super, app := dbTestPools(t)

	tenantID, entityID := seedActivePolicyTenant(t, super, "concurrent-arm-cancel")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "AN-ADV-APPR-RACE-1")
	want := mustInvoiceNumberFor(t, super, invoiceID)
	seedApprovalRun(t, super, tenantID, invoiceID, oneActivePolicyVersionID(t, super, tenantID))

	var wg sync.WaitGroup
	var armErr, cancelErr error
	var cancelled bool
	wg.Add(2)
	go func() {
		defer wg.Done()
		cancelled, cancelErr = cancelCarryingNumber(t, app, tenantID, invoiceID, want, "an-adv-race-cancel")
	}()
	go func() {
		defer wg.Done()
		_, armErr = arm(t, app, tenantID, invoiceID, "fp-an-adv-race", "an-adv-race-arm")
	}()
	wg.Wait()

	if cancelErr != nil {
		t.Fatalf("CancelLiveRunTx under the race: %v", cancelErr)
	}
	if !cancelled {
		t.Fatal("CancelLiveRunTx returned false, want true -- the seeded run was open, so there is no cancelled row to assert on")
	}
	// armErr is tolerated: approval_runs_one_open legitimately refuses the arm when
	// the cancel has not committed yet. The rows that DID land are the subject.

	rows, err := super.Query(context.Background(),
		`SELECT event, payload->>'`+auditNumberKey+`', payload::text
		   FROM audit_log WHERE invoice_id = $1::uuid ORDER BY id`, invoiceID)
	if err != nil {
		t.Fatalf("read the race's audit rows: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var event, payload string
		var number *string
		if err := rows.Scan(&event, &number, &payload); err != nil {
			t.Fatalf("scan a race audit row: %v", err)
		}
		seen++
		if number == nil {
			t.Errorf("%s: payload->>'%s' is SQL NULL (key absent); payload = %s", event, auditNumberKey, payload)
			continue
		}
		if *number != want {
			t.Errorf("%s: payload->>'%s' = %q, want %q -- the race crossed the two writers' numbers", event, auditNumberKey, *number, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the race's audit rows: %v", err)
	}
	// Floor: the cancelled row alone, plus the armed row when the arm won its turn.
	minRows := 1
	if armErr == nil {
		minRows = 2
	}
	if seen < minRows {
		t.Fatalf("the invoice holds %d audit rows, want at least %d (armErr = %v) -- the loop above asserted nothing", seen, minRows, armErr)
	}
}

// CancelLiveRunTx's number is a PARAMETER, so "can it outlive the row it was read
// from?" needs an answer. The answer is the FK: while any run references the invoice,
// the row cannot be deleted, so a caller's number can never describe an invoice that
// vanished mid-transaction.
func TestAuditNumberAdversarial_TheInvoiceCannotBeDeletedUnderALiveRun(t *testing.T) {
	super, _ := dbTestPools(t)
	ctx := context.Background()

	tenantID, entityID := seedActivePolicyTenant(t, super, "delete-under-run")
	invoiceID := seedInvoice(t, super, tenantID, entityID, "AN-ADV-APPR-DELETE-1")
	runID := seedApprovalRun(t, super, tenantID, invoiceID, oneActivePolicyVersionID(t, super, tenantID))

	_, err := super.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, invoiceID)
	if got := pgCode(err); got != "23001" {
		t.Fatalf("DELETE the invoice under a live run: err = %v (SQLSTATE %q), want 23001 (ON DELETE RESTRICT; NO ACTION would be 23503) -- a caller's number could then describe a row that no longer exists", err, got)
	}

	// Positive control: the refusal is the run's reference, not an undeletable table.
	if _, err := super.Exec(ctx, `DELETE FROM approval_runs WHERE id = $1`, runID); err != nil {
		t.Fatalf("delete the run: %v", err)
	}
	if _, err := super.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, invoiceID); err != nil {
		t.Fatalf("DELETE the invoice with no run referencing it: %v, want success -- without this the refusal above proves nothing", err)
	}
}
