// M5-09-02 (task-255): QA Mode B adversarial coverage ON TOP OF the AC-1..6
// tests authored RED at f4a789e and re-baselined green at 9019e1f
// (edit_test.go/outcome_loop_test.go/transition_test.go/system_actor_test.go).
// Those files prove the AC-shaped happy paths; this file targets the gaps a
// skeptical read of the retention flip surfaces: cross-tenant leakage of the
// now-populated demoted-draft state, second-rejection overwrite (not
// accumulation), concurrency between Store.Edit and MarkAcceptedTx, the
// `failed` dead-letter edge, MarkAcceptedTx's idempotent-replay short-circuit,
// the API read surface across every endpoint that returns an Invoice, and a
// CHARACTERIZATION test for the named residual risk (Stage-1 finding F3,
// task-255's implementation notes) -- documenting current behavior, not
// asserting it as desired.
//
// Reuses the dbTestPools/seedTenant/seedEntity/seedInvoiceAtStatus/
// seedRuleSetVersionID/mustCount/auditCount/contentFingerprint/
// newInsertOnlyQueueClient/doInvoiceGet/doInvoiceList/doInvoiceEdit/
// doInvoiceTransition/doInvoiceValidate harness (same package). No new
// harness is invented.
//
// Run: `make test-rls` (or `DEV_DB_PORT=5433 make test-rls` in this
// worktree), or directly:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_MIGRATION_URL="postgres://invoice_migrator:migrator@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5433/invoice_os?sslmode=disable" \
//	DATABASE_READER_URL="postgres://invoice_tenant_reader:reader@localhost:5433/invoice_os?sslmode=disable" \
//	go test -race -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
	"github.com/SimonOsipov/invoice-os/internal/platform/db"
	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// TestRLS_RetainedDemotedDraftReasonsCrossTenantRefused (QA Mode B, Part
// 2.1): M5-09-02 removes the upstream wipe that used to GUARANTEE a demoted
// draft carried '[]' -- before this subtask, no cross-tenant read of a
// rejected->draft demotion could ever observe populated reasons, because
// nothing survived the demotion to observe. This proves that widening never
// leaks the now-populated value across the tenant boundary: tenant A's
// rejected invoice is demoted via a REAL Store.Edit call (not a raw seed),
// retaining its rejection reasons, and tenant B must not be able to read
// them via Get, via List, or via a raw SQL SELECT under B's OWN GUC
// (RLS-level isolation, not merely Go-level filtering -- mirrors
// TestStoreCrossTenant_UpdateGetListRefused's shape, store_test.go).
func TestRLS_RetainedDemotedDraftReasonsCrossTenantRefused(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantA := seedTenant(t, super, "RETAIN-RLS tenant A")
	tenantB := seedTenant(t, super, "RETAIN-RLS tenant B")
	entityA := seedEntity(t, super, tenantA, "RETAIN-RLS A entity")

	cA := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantA})

	invA := seedInvoiceAtStatus(t, super, tenantA, entityA, "RETAIN-RLS-A", StatusRejected)
	reasonsJSON := `[{"code":"TIN_MISMATCH","message":"supplier TIN does not match","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, reasonsJSON, invA,
	); err != nil {
		t.Fatalf("seed rejection_reasons: %v", err)
	}
	// jsonb round-trips through Postgres's own key-ordering/whitespace
	// normalisation ([D13]-adjacent), so the seed's literal string is never
	// byte-identical to what a later ::text read returns -- read back the
	// NORMALISED form once, and compare against THAT from here on (mirrors
	// every existing retention test's own idiom, e.g.
	// TestStoreEdit_RetainedReasonsSurviveRevalidate, outcome_loop_test.go).
	var seededReasons string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invA).Scan(&seededReasons); err != nil {
		t.Fatalf("read back seeded rejection_reasons: %v", err)
	}

	newVAT := "6.60"
	edited, err := store.Edit(cA, invA, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
	if err != nil {
		t.Fatalf("Edit (tenant A, rejected->draft): %v (want nil)", err)
	}
	if edited.Status != StatusDraft {
		t.Fatalf("Edit returned status = %q, want %q (demoted)", edited.Status, StatusDraft)
	}
	// Precondition: confirm the demotion actually RETAINED the reasons --
	// otherwise the cross-tenant refusal below would pass vacuously (nothing
	// sensitive to leak).
	if string(edited.RejectionReasons) != seededReasons {
		t.Fatalf("precondition: Edit-returned rejection_reasons = %s, want %s (retained, not cleared)", edited.RejectionReasons, seededReasons)
	}

	cB := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantB})

	if _, err := store.Get(cB, invA); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(tenant A's demoted+retained-reasons invoice) as tenant B err = %v, want ErrNotFound", err)
	}

	items, _, err := store.List(cB, ListFilter{Limit: 50, Offset: 0})
	if err != nil {
		t.Fatalf("List (as tenant B): %v", err)
	}
	for _, inv := range items {
		if inv.ID == invA {
			t.Errorf("List (as tenant B) leaked tenant A's invoice %s (rejection_reasons=%s)", invA, inv.RejectionReasons)
		}
	}

	// RLS-level, not merely Go-level: a raw SELECT under B's OWN GUC (not
	// the superuser bypass) must find zero rows for A's id.
	err = db.WithinTenantTx(ctx, app, tenantB, func(tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM invoices WHERE id = $1)`, invA).Scan(&exists); e != nil {
			return e
		}
		if exists {
			t.Errorf("SELECT EXISTS under tenant B's own GUC found tenant A's invoice %s -- RLS did not isolate the retained-reasons row", invA)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTenantTx (tenant B RLS-level read check): %v", err)
	}
}

// TestOutcomeLoop_SecondRejectionOverwritesNotAccumulates (QA Mode B, Part
// 2.3): reject with reason set A, fix, resubmit, reject AGAIN with a
// DIFFERENT reason set B -- the invoice must carry exactly B, not A union B.
// MarkRejectedTx's outcome closure does a wholesale `SET rejection_reasons =
// $1::jsonb` (actor.go), never an append, so this should hold structurally;
// pinned explicitly because retention (this subtask's whole point) makes
// accumulation the FIRST plausible bug a reviewer would suspect -- if a
// future change replaced that wholesale SET with a JSONB `||` concat "to
// preserve rejection history", this test is what would catch it.
func TestOutcomeLoop_SecondRejectionOverwritesNotAccumulates(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	q := newInsertOnlyQueueClient(t, app)
	submitter := NewSubmitter(store, q)

	tenantID := seedTenant(t, super, "LOOP-2X tenant")
	entityID := seedEntity(t, super, tenantID, "LOOP-2X entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "LOOP-2X", StatusQueued)

	reasonsA := []submission.Reason{{Code: "TIN_MISMATCH", Message: "supplier TIN does not match (cycle A)", Path: "supplier_tin"}}
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, reasonsA)
		return err
	})
	if err != nil {
		t.Fatalf("MarkRejectedTx (cycle A, queued->rejected): %v (want nil)", err)
	}

	newVAT := "8.88"
	edited, err := store.Edit(c, invID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
	if err != nil {
		t.Fatalf("Edit (rejected->draft, fixing cycle A): %v (want nil)", err)
	}
	if edited.Status != StatusDraft {
		t.Fatalf("Edit returned status = %q, want %q", edited.Status, StatusDraft)
	}

	freshFP := contentFingerprint(edited, edited.LineItems)
	versionID := seedRuleSetVersionID(t, super)
	revalidated, err := store.ApplyValidation(c, invID, []Violation{}, versionID, freshFP)
	if err != nil {
		t.Fatalf("ApplyValidation (draft->validated): %v (want nil)", err)
	}
	if revalidated.Status != StatusValidated {
		t.Fatalf("ApplyValidation returned status = %q, want %q", revalidated.Status, StatusValidated)
	}

	freshKey := "LOOP-2X-fresh-" + uuid.NewString()
	if _, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: freshKey}); err != nil {
		t.Fatalf("BatchSubmit (resubmit with fresh key): %v (want nil)", err)
	}

	// A DIFFERENT reason set -- disjoint code/message/path from cycle A, so
	// any accumulation bug (concat instead of SET) is unambiguously visible
	// in the readback below.
	reasonsB := []submission.Reason{{Code: "VAT_RATE_INVALID", Message: "VAT rate not in the standard schedule (cycle B)", Path: "line_items[0].vat_rate"}}
	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, reasonsB)
		return err
	})
	if err != nil {
		t.Fatalf("MarkRejectedTx (cycle B, queued->rejected): %v (want nil)", err)
	}

	var status, reasonsAfterB string
	if err := super.QueryRow(ctx,
		`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &reasonsAfterB); err != nil {
		t.Fatalf("read back after cycle B MarkRejectedTx: %v", err)
	}
	if Status(status) != StatusRejected {
		t.Fatalf("status after cycle B MarkRejectedTx = %q, want %q", status, StatusRejected)
	}

	var gotReasons []submission.Reason
	if err := json.Unmarshal([]byte(reasonsAfterB), &gotReasons); err != nil {
		t.Fatalf("unmarshal rejection_reasons: %v (raw=%s)", err, reasonsAfterB)
	}
	if !reflect.DeepEqual(gotReasons, reasonsB) {
		t.Errorf("rejection_reasons after cycle B's MarkRejectedTx = %+v, want EXACTLY cycle B's %+v (not accumulated with cycle A)", gotReasons, reasonsB)
	}
	if len(gotReasons) != 1 {
		t.Errorf("rejection_reasons after cycle B = %d entries, want exactly 1 (cycle A's entry must not survive alongside cycle B's)", len(gotReasons))
	}
	for _, r := range gotReasons {
		if r.Code == reasonsA[0].Code {
			t.Errorf("rejection_reasons after cycle B still contains cycle A's code %q -- accumulation, not overwrite", reasonsA[0].Code)
		}
	}
}

// TestConcurrency_EditRacesMarkAcceptedTxLeavesNoReasonsOnAccepted (QA Mode
// B, Part 2.4): a concurrent Store.Edit and a MarkAcceptedTx targeting the
// SAME invoice, seeded at `queued` carrying STALE reasons from an earlier
// rejection cycle (truth-table row "queued/submitted | populated" --
// exactly the state a resubmit-after-fix produces). This is a
// DETERMINISTIC, order-independent race, not a flaky one: Store.Edit's
// fixable-state guard (step 3, store.go) only allows draft/validated/
// rejected, and `queued`/`accepted` are BOTH refused -- so regardless of
// which goroutine wins the row's FOR UPDATE lock first, every concurrent
// Edit call must fail ErrNotFixable and MarkAcceptedTx must be the only
// writer, landing the invoice on `accepted` with reasons cleared. Run with
// `-race` to additionally confirm no data race in the Go-level code driving
// the two calls.
func TestConcurrency_EditRacesMarkAcceptedTxLeavesNoReasonsOnAccepted(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "CONC-ACC tenant")
	entityID := seedEntity(t, super, tenantID, "CONC-ACC entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "CONC-ACC", StatusQueued)
	staleReasons := `[{"code":"APP-ERR-0417","message":"Supplier TIN not registered","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, staleReasons, invID,
	); err != nil {
		t.Fatalf("seed stale rejection_reasons: %v", err)
	}

	editTargets := []string{"11.00", "22.00", "33.00", "44.00", "55.00"}
	editErrs := make([]error, len(editTargets))
	var acceptErr error

	var wg sync.WaitGroup
	wg.Add(len(editTargets) + 1)
	for i, target := range editTargets {
		i, target := i, target
		go func() {
			defer wg.Done()
			vat := target
			_, editErrs[i] = store.Edit(c, invID, EditInput{UpdateInput: UpdateInput{VAT: &vat}})
		}()
	}
	go func() {
		defer wg.Done()
		acceptErr = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
			_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, "IRN-CONC-ACC", "CSID-CONC-ACC", "QR-CONC-ACC")
			return err
		})
	}()
	wg.Wait()

	if acceptErr != nil {
		t.Fatalf("concurrent MarkAcceptedTx: %v (want nil -- queued is the only status among the racers this is legal from)", acceptErr)
	}
	for i, e := range editErrs {
		if !errors.Is(e, ErrNotFixable) {
			t.Errorf("concurrent Edit[%d] err = %v, want ErrNotFixable (queued/accepted are both non-editable, regardless of race ordering)", i, e)
		}
	}

	var status, reasons, typeofReasons string
	if err := super.QueryRow(ctx,
		`SELECT status, rejection_reasons::text, jsonb_typeof(rejection_reasons) FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &reasons, &typeofReasons); err != nil {
		t.Fatalf("read back after the race: %v", err)
	}
	if Status(status) != StatusAccepted {
		t.Errorf("status after Edit-vs-MarkAcceptedTx race = %q, want %q", status, StatusAccepted)
	}
	if reasons != "[]" {
		t.Errorf("rejection_reasons after Edit-vs-MarkAcceptedTx race = %q, want %q (no interleaving may leave stale reasons on an accepted invoice)", reasons, "[]")
	}
	if typeofReasons != "array" {
		t.Errorf("jsonb_typeof(rejection_reasons) after the race = %q, want %q (never JSON null)", typeofReasons, "array")
	}
}

// TestOutcomeLoop_RejectEditValidateQueueFailRetainsReasons (QA Mode B,
// Part 2.5): drives reject -> edit -> re-validate -> resubmit -> FAIL (the
// queued->failed dead-letter edge, MarkFailedTx) and asserts the resulting
// `failed` invoice carries the retained reasons byte-identical to what
// MarkRejectedTx originally wrote -- matching the story's own per-status
// truth table row ("failed | possibly populated from earlier cycle"), not
// contradicting it. MarkFailedTx's outcome callback (BUG-06-02, task-384)
// writes failure_kind only -- rejection_reasons is untouched by this path
// either way, so this still pins that fact.
func TestOutcomeLoop_RejectEditValidateQueueFailRetainsReasons(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)
	q := newInsertOnlyQueueClient(t, app)
	submitter := NewSubmitter(store, q)

	tenantID := seedTenant(t, super, "LOOP-FAIL tenant")
	entityID := seedEntity(t, super, tenantID, "LOOP-FAIL entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "LOOP-FAIL", StatusQueued)

	reasons := []submission.Reason{{Code: "TIN_MISMATCH", Message: "supplier TIN does not match", Path: "supplier_tin"}}
	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkRejectedTx(ctx, tx, invID, tenantID, reasons)
		return err
	})
	if err != nil {
		t.Fatalf("MarkRejectedTx (queued->rejected): %v (want nil)", err)
	}

	var reasonsAfterReject string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID).Scan(&reasonsAfterReject); err != nil {
		t.Fatalf("read back rejection_reasons after MarkRejectedTx: %v", err)
	}

	newVAT := "7.77"
	edited, err := store.Edit(c, invID, EditInput{UpdateInput: UpdateInput{VAT: &newVAT}})
	if err != nil {
		t.Fatalf("Edit (rejected->draft): %v (want nil)", err)
	}
	if edited.Status != StatusDraft {
		t.Fatalf("Edit returned status = %q, want %q", edited.Status, StatusDraft)
	}

	freshFP := contentFingerprint(edited, edited.LineItems)
	versionID := seedRuleSetVersionID(t, super)
	revalidated, err := store.ApplyValidation(c, invID, []Violation{}, versionID, freshFP)
	if err != nil {
		t.Fatalf("ApplyValidation (draft->validated): %v (want nil)", err)
	}
	if revalidated.Status != StatusValidated {
		t.Fatalf("ApplyValidation returned status = %q, want %q", revalidated.Status, StatusValidated)
	}

	freshKey := "LOOP-FAIL-fresh-" + uuid.NewString()
	if _, err := submitter.BatchSubmit(c, BatchSubmitInput{InvoiceIDs: []string{invID}, IdempotencyKey: freshKey}); err != nil {
		t.Fatalf("BatchSubmit (resubmit with fresh key): %v (want nil)", err)
	}

	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkFailedTx(ctx, tx, invID, tenantID, submission.FailurePayloadNotBuilt)
		return err
	})
	if err != nil {
		t.Fatalf("MarkFailedTx (queued->failed): %v (want nil)", err)
	}

	var status, reasonsAfterFail, typeofReasons string
	if err := super.QueryRow(ctx,
		`SELECT status, rejection_reasons::text, jsonb_typeof(rejection_reasons) FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &reasonsAfterFail, &typeofReasons); err != nil {
		t.Fatalf("read back after MarkFailedTx: %v", err)
	}
	if Status(status) != StatusFailed {
		t.Fatalf("status after MarkFailedTx = %q, want %q", status, StatusFailed)
	}
	if reasonsAfterFail != reasonsAfterReject {
		t.Errorf("rejection_reasons after MarkFailedTx = %q, want byte-identical to what MarkRejectedTx wrote %q (matches the story's own truth-table row \"failed | possibly populated from earlier cycle\")", reasonsAfterFail, reasonsAfterReject)
	}
	if typeofReasons != "array" {
		t.Errorf("jsonb_typeof(rejection_reasons) after MarkFailedTx = %q, want %q (never JSON null)", typeofReasons, "array")
	}
}

// TestMarkAcceptedTx_IdempotentReplayReasonsStayCleared (QA Mode B, Part
// 2.6): markTerminalTx (actor.go) short-circuits when the row is already at
// target -- a replayed MarkAcceptedTx on an already-accepted invoice never
// re-enters transitionTx, so its [reason-lifecycle] clear never re-runs
// (actor.go's own doc note on the idempotent branch). This proves that
// non-self-healing is harmless in practice: the FIRST call already cleared
// the reasons, so the replay's skip leaves the invariant intact anyway. A
// SECOND, DIFFERENT irn/csid/qr triple on the replay proves the outcome
// closure genuinely never re-runs (not merely that it happens to write the
// same values again) -- the stored irn must stay the FIRST call's value.
func TestMarkAcceptedTx_IdempotentReplayReasonsStayCleared(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "MATX-IDEMP tenant")
	entityID := seedEntity(t, super, tenantID, "MATX-IDEMP entity")
	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "MATX-IDEMP", StatusQueued)

	staleReasons := `[{"code":"APP-ERR-0417","message":"Supplier TIN not registered","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, staleReasons, invID,
	); err != nil {
		t.Fatalf("seed stale rejection_reasons: %v", err)
	}

	err := db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, "IRN-FIRST", "CSID-FIRST", "QR-FIRST")
		return err
	})
	if err != nil {
		t.Fatalf("first MarkAcceptedTx: %v (want nil)", err)
	}

	var reasonsAfterFirst string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID).Scan(&reasonsAfterFirst); err != nil {
		t.Fatalf("read back rejection_reasons after first MarkAcceptedTx: %v", err)
	}
	if reasonsAfterFirst != "[]" {
		t.Fatalf("precondition: rejection_reasons after first MarkAcceptedTx = %q, want %q", reasonsAfterFirst, "[]")
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

	// The replay: same id, already accepted, DIFFERENT outcome arguments.
	err = db.WithinTenantTx(ctx, app, tenantID, func(tx pgx.Tx) error {
		_, err := store.MarkAcceptedTx(ctx, tx, invID, tenantID, "IRN-REPLAY-SHOULD-NOT-WRITE", "CSID-REPLAY", "QR-REPLAY")
		return err
	})
	if err != nil {
		t.Fatalf("replayed MarkAcceptedTx: %v (want nil -- idempotent no-op, not an error)", err)
	}

	var status, reasons, typeofReasons, irn string
	if err := super.QueryRow(ctx,
		`SELECT status, rejection_reasons::text, jsonb_typeof(rejection_reasons), irn FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &reasons, &typeofReasons, &irn); err != nil {
		t.Fatalf("read back after replayed MarkAcceptedTx: %v", err)
	}
	if Status(status) != StatusAccepted {
		t.Errorf("status after replay = %q, want unchanged %q", status, StatusAccepted)
	}
	if reasons != "[]" {
		t.Errorf("rejection_reasons after replayed MarkAcceptedTx = %q, want still %q (the invariant already held; the idempotent short-circuit does not need to re-run the clear to preserve it)", reasons, "[]")
	}
	if typeofReasons != "array" {
		t.Errorf("jsonb_typeof(rejection_reasons) after replay = %q, want %q (never JSON null)", typeofReasons, "array")
	}
	if irn != "IRN-FIRST" {
		t.Errorf("irn after replayed MarkAcceptedTx = %q, want unchanged %q (idempotent short-circuit must never re-run the outcome write -- an already-stored outcome must not be clobbered by a replay's possibly-different arguments)", irn, "IRN-FIRST")
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows after replay = %d, want unchanged %d (idempotent no-op writes no new row)", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeAudit {
		t.Errorf("audit_log invoice.transitioned rows after replay = %d, want unchanged %d", n, beforeAudit)
	}
}

// TestHandlers_RejectionReasonsSurfaceConsistentlyAcrossEndpoints (QA Mode
// B, Part 2.7): under retention, rejection_reasons is populated on states
// (draft, demoted from rejected) where it used to always be '[]' -- so this
// pins that EVERY endpoint returning an Invoice actually surfaces the SAME
// value, byte-identical, on the wire: GET, LIST, PATCH (Edit), POST
// .../transitions, and POST .../validate. A single seeded Invoice value
// feeds all five mocked store closures (the handlers_test.go
// doInvoiceGet/doInvoiceList/doInvoiceEdit/doInvoiceTransition/
// doInvoiceValidate idiom, same package) so any endpoint that filtered,
// dropped, or otherwise rewrote the field would show up as a byte
// mismatch against the other four, not just against a hard-coded literal.
func TestHandlers_RejectionReasonsSurfaceConsistentlyAcrossEndpoints(t *testing.T) {
	id := auth.Identity{Subject: "user-1", Role: "authenticated", TenantID: uuid.NewString()}
	invoiceID := uuid.NewString()
	seededReasons := json.RawMessage(`[{"code":"TIN_MISMATCH","message":"supplier TIN does not match","path":"supplier_tin"}]`)

	base := Invoice{
		ID: invoiceID, EntityID: uuid.NewString(), InvoiceNumber: "RETAIN-API-1", Status: StatusDraft,
		RejectionReasons: seededReasons,
	}

	get := func(ctx context.Context, gotID string) (Invoice, error) { return base, nil }
	_, getResp := doInvoiceGet(t, get, &id, invoiceID)
	if string(getResp.RejectionReasons) != string(seededReasons) {
		t.Errorf("GetHandler rejection_reasons = %s, want %s", getResp.RejectionReasons, seededReasons)
	}

	list := func(ctx context.Context, f ListFilter) ([]Invoice, int, error) { return []Invoice{base}, 1, nil }
	_, listResp := doInvoiceList(t, list, &id, "")
	if len(listResp.Invoices) != 1 {
		t.Fatalf("ListHandler invoices length = %d, want 1", len(listResp.Invoices))
	}
	if string(listResp.Invoices[0].RejectionReasons) != string(seededReasons) {
		t.Errorf("ListHandler rejection_reasons = %s, want %s", listResp.Invoices[0].RejectionReasons, seededReasons)
	}

	edit := func(ctx context.Context, gotID string, in EditInput) (Invoice, error) { return base, nil }
	_, editResp := doInvoiceEdit(t, edit, &id, invoiceID, `{"vat":"12.00"}`)
	if string(editResp.RejectionReasons) != string(seededReasons) {
		t.Errorf("EditHandler (PATCH) rejection_reasons = %s, want %s", editResp.RejectionReasons, seededReasons)
	}

	transition := func(ctx context.Context, gotID string, target Status) (Invoice, error) { return base, nil }
	_, transResp := doInvoiceTransition(t, transition, &id, invoiceID, `{"target":"accepted"}`)
	if string(transResp.RejectionReasons) != string(seededReasons) {
		t.Errorf("TransitionHandler rejection_reasons = %s, want %s", transResp.RejectionReasons, seededReasons)
	}

	validate := func(ctx context.Context, gotID string) (Invoice, int, error) { return base, 1, nil }
	_, validateResp := doInvoiceValidate(t, validate, &id, invoiceID)
	if string(validateResp.RejectionReasons) != string(seededReasons) {
		t.Errorf("ValidateHandler rejection_reasons = %s, want %s", validateResp.RejectionReasons, seededReasons)
	}

	// Internal consistency: every endpoint's wire value equals every other
	// endpoint's, not merely the shared literal -- catches a hypothetical
	// endpoint that filtered to a DIFFERENT (but still non-empty, so the
	// checks above alone wouldn't catch it) value.
	all := map[string]json.RawMessage{
		"get": getResp.RejectionReasons, "list": listResp.Invoices[0].RejectionReasons,
		"edit": editResp.RejectionReasons, "transition": transResp.RejectionReasons,
		"validate": validateResp.RejectionReasons,
	}
	for name, got := range all {
		if string(got) != string(seededReasons) {
			t.Errorf("endpoint %q disagrees with the seeded value: %s != %s", name, got, seededReasons)
		}
	}
}

// TestStoreTransition_RejectedViaHandlerPathCarriesPreviousCycleReasons_F3Characterization
// (QA Mode B, Part 3) CHARACTERIZES a KNOWINGLY ACCEPTED residual risk
// (Stage-1 architecture validation finding F3, task-255's implementation
// notes) -- this test documents CURRENT behavior, it does NOT assert
// desired behavior. Do not "fix" the code to make this test show empty
// reasons; that would wipe the reasons MarkRejectedTx writes moments
// earlier in the SAME tx (markTerminalTx runs the outcome callback BEFORE
// transitionTx), destroying every APP rejection reason in production --
// exactly the trap task-255's own architecture notes forbid.
//
// POST /v1/invoices/{id}/transitions {"target":"rejected"} is exactly as
// legal and unguarded as the {"target":"accepted"} path this subtask DOES
// close (legalTransitions[StatusQueued] includes StatusRejected, store.go;
// TransitionHandler refuses only StatusValidated, handlers.go). Under
// retention, a queued invoice that still carries a PREVIOUS cycle's
// rejection_reasons (e.g. mid-resubmit after an earlier reject/fix loop)
// and is driven straight to `rejected` via THIS path -- bypassing
// MarkRejectedTx entirely, so nothing writes fresh reasons for THIS
// rejection -- lands on `rejected` still carrying the STALE, previous-cycle
// reasons, indistinguishable on the wire from a genuine new rejection.
//
// If this test ever goes RED: either F3 was deliberately fixed elsewhere
// (update this test to match the new behavior and remove the F3 note from
// task-255's implementation notes) or something regressed transitionTx's
// asymmetry -- investigate before touching this assertion either way.
func TestStoreTransition_RejectedViaHandlerPathCarriesPreviousCycleReasons_F3Characterization(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "TR-F3 tenant")
	entityID := seedEntity(t, super, tenantID, "TR-F3 entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: memberSubject, Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "TR-F3", StatusQueued)
	staleReasonsSeed := `[{"code":"APP-ERR-0417","message":"Supplier TIN not registered (FIRST cycle)","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, staleReasonsSeed, invID,
	); err != nil {
		t.Fatalf("seed stale rejection_reasons: %v", err)
	}
	// jsonb normalises key order/whitespace on write -- read back once and
	// compare against the NORMALISED form, not the literal seed string.
	var staleReasons string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID).Scan(&staleReasons); err != nil {
		t.Fatalf("read back seeded rejection_reasons: %v", err)
	}

	got, err := store.Transition(c, invID, StatusRejected)
	if err != nil {
		t.Fatalf("Transition(queued->rejected): %v (want nil -- this path is legal and unguarded today)", err)
	}
	if got.Status != StatusRejected {
		t.Fatalf("Transition returned status = %q, want %q", got.Status, StatusRejected)
	}

	var reasons string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID).Scan(&reasons); err != nil {
		t.Fatalf("read back rejection_reasons: %v", err)
	}

	// CHARACTERIZATION, not desired behavior: the handler path writes no
	// reasons of its own, so the stale, previous-cycle reasons survive onto
	// this NEW `rejected` row -- misattributed as if they belonged to it.
	if reasons != staleReasons {
		t.Fatalf("rejection_reasons after Transition(->rejected) = %q, want byte-unchanged (stale-carryover) %q -- F3's documented behavior changed: either it was fixed (update this test) or something regressed (investigate)", reasons, staleReasons)
	}
}
