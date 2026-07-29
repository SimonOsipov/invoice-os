// M4-02-02 (task-97): tests for internal/invoice's Store.Transition — the
// ONE guarded state-machine method that writes invoices.status — written
// BEFORE the real implementation exists (RED against store.go's
// not-implemented stub: Transition currently always returns errNotImplemented,
// so every assertion below fails for that reason, not a compile error).
// Reuses the dbTestPools/seedTenant/seedEntity/seedInvoice/mustCount/
// auditCount/auditActor harness from store_test.go (same package).
//
// Spec-to-test map (Test Specs table, M4-02-02 story / task-97):
//
//	INV-SM-01 TestTransition_LegalEdgesSucceedWithTripleWrite
//	INV-SM-02 TestTransition_IllegalEdgesRejectedNoWrites
//	INV-SM-03 TestTransition_RedundantRejectedBeforeLegalityCheck
//	INV-SM-04 TestTransition_NotFoundAndCrossTenant
//	INV-SM-05 TestTransition_AtomicityRollsBackOnActorCheckFailure
//	INV-SM-06 TestTransition_ConcurrentSameEdgeSerializesToOneWinner
//	INV-SM-07 TestTransition_HistoryAndAuditActorMatchCallerSubject
//	(sole-writer, optional) TestTransition_RejectedTransitionLeavesStatusByteIdentical
//
// M5-09-02 (task-255) adds two more, at the bottom of this file:
//
//	AC-3 TestStoreTransition_AcceptedViaHandlerPathClearsRejectionReasons
//	AC-4 TestStoreTransition_AcceptedClearFailureStillRollsBack
//
// TestRLS_InvoicesTransitionCrossTenantRefused lives in
// cross_tenant_integration_test.go, alongside M4-02-01's
// TestRLS_InvoicesStoreChildWritesTenantScoped.
//
// Run: `make test-rls` (or `make test-audit`), or directly, e.g.:
//
//	DATABASE_URL="postgres://invoice_app:app@localhost:5432/invoice_os?sslmode=disable" \
//	DATABASE_SUPERUSER_URL="postgres://postgres:postgres@localhost:5432/invoice_os?sslmode=disable" \
//	go test -count=1 ./internal/invoice/...
package invoice

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// --- INV-SM-01..07 + sole-writer invariant ---------------------------------

// legalEdgeCase is one row of the 6-edge legal-transition table. prehops
// drives the invoice from its Create-time draft state to `from` via real
// (legal) Transition calls before the tested transition (from -> target) is
// issued -- this exercises the real state machine end-to-end once Transition
// is implemented (Stage 3), rather than bypassing it via a superuser status
// seed.
type legalEdgeCase struct {
	name    string
	prehops []Status
	from    Status
	target  Status
}

var legalEdges = []legalEdgeCase{
	{"draft->validated", nil, StatusDraft, StatusValidated},
	{"validated->queued", []Status{StatusValidated}, StatusValidated, StatusQueued},
	{"queued->submitted", []Status{StatusValidated, StatusQueued}, StatusQueued, StatusSubmitted},
	{"submitted->accepted", []Status{StatusValidated, StatusQueued, StatusSubmitted}, StatusSubmitted, StatusAccepted},
	{"submitted->rejected", []Status{StatusValidated, StatusQueued, StatusSubmitted}, StatusSubmitted, StatusRejected},
	{"submitted->failed", []Status{StatusValidated, StatusQueued, StatusSubmitted}, StatusSubmitted, StatusFailed},
	{"validated->draft", []Status{StatusValidated}, StatusValidated, StatusDraft},
	// M5-05-01 (task-237) (AC#3): the synchronous-verdict edges -- a
	// worker that gets an immediate accept/reject from the APP reaches its
	// final state without ever passing through submitted.
	{"queued->accepted", []Status{StatusValidated, StatusQueued}, StatusQueued, StatusAccepted},
	{"queued->rejected", []Status{StatusValidated, StatusQueued}, StatusQueued, StatusRejected},
	// M5-05-01 (task-237) (AC#4): the rework edge -- a rejected invoice can be
	// demoted back to draft for correction.
	{"rejected->draft", []Status{StatusValidated, StatusQueued, StatusSubmitted, StatusRejected}, StatusRejected, StatusDraft},
}

// INV-SM-01: each of the 7 legal edges succeeds, writing status + exactly one
// invoice_status_history row (from,to) + exactly one invoice.transitioned
// audit row, atomically; actor on both new rows is the caller's Subject.
func TestTransition_LegalEdgesSucceedWithTripleWrite(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	for _, tc := range legalEdges {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tenantID := seedTenant(t, super, "INV-SM-01 "+tc.name+" tenant")
			entityID := seedEntity(t, super, tenantID, "INV-SM-01 entity")
			subject := uuid.NewString()
			c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

			inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "INV-SM-01-" + tc.name})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			for _, hop := range tc.prehops {
				if _, err := store.Transition(c, inv.ID, hop); err != nil {
					t.Fatalf("pre-hop Transition(-> %s): %v", hop, err)
				}
			}

			beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
			beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

			got, err := store.Transition(c, inv.ID, tc.target)
			if err != nil {
				t.Fatalf("Transition(%s -> %s): %v", tc.from, tc.target, err)
			}
			if got.Status != tc.target {
				t.Errorf("Transition returned status = %q, want %q", got.Status, tc.target)
			}
			if got.ID != inv.ID {
				t.Errorf("Transition returned id = %q, want %q", got.ID, inv.ID)
			}

			var status string
			if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
				t.Fatalf("read back status: %v", err)
			}
			if Status(status) != tc.target {
				t.Errorf("invoices.status after Transition = %q, want %q", status, tc.target)
			}

			if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory+1 {
				t.Errorf("invoice_status_history rows for invoice = %d, want %d (exactly one new row)", n, beforeHistory+1)
			}
			var fromStatus *string
			var toStatus, actor string
			if err := super.QueryRow(ctx,
				`SELECT from_status, to_status, actor FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`,
				inv.ID,
			).Scan(&fromStatus, &toStatus, &actor); err != nil {
				t.Fatalf("read newest history row: %v", err)
			}
			if fromStatus == nil || Status(*fromStatus) != tc.from {
				t.Errorf("newest history from_status = %v, want %q", fromStatus, tc.from)
			}
			if Status(toStatus) != tc.target {
				t.Errorf("newest history to_status = %q, want %q", toStatus, tc.target)
			}
			if actor != subject {
				t.Errorf("newest history actor = %q, want %q", actor, subject)
			}

			if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeAudit+1 {
				t.Errorf("audit_log invoice.transitioned rows = %d, want %d (exactly one new row)", n, beforeAudit+1)
			}
			if a := auditActor(t, app, tenantID, "invoice.transitioned"); a != subject {
				t.Errorf("audit actor = %q, want %q", a, subject)
			}
		})
	}
}

// illegalEdgeCase is one row of the representative-illegal-edges table.
type illegalEdgeCase struct {
	name    string
	prehops []Status
	from    Status
	target  Status
}

var illegalEdges = []illegalEdgeCase{
	{"draft->accepted", nil, StatusDraft, StatusAccepted},
	{"draft->queued", nil, StatusDraft, StatusQueued},
	{"draft->submitted", nil, StatusDraft, StatusSubmitted},
	{"validated->submitted", []Status{StatusValidated}, StatusValidated, StatusSubmitted},
	{"validated->accepted", []Status{StatusValidated}, StatusValidated, StatusAccepted},
	// M5-05-01 (task-237): queued->accepted and rejected->draft moved OUT of this
	// table -- they are now legal (see legalEdges above / wantLegalEdge in
	// transition_adversarial_test.go). failed->queued stays illegal
	// ([failed-invoices] -- M5-05-01 (task-237) does NOT add this edge).
	{"submitted->draft", []Status{StatusValidated, StatusQueued, StatusSubmitted}, StatusSubmitted, StatusDraft},
	{"failed->queued", []Status{StatusValidated, StatusQueued, StatusSubmitted, StatusFailed}, StatusFailed, StatusQueued},
	{"accepted->validated", []Status{StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted}, StatusAccepted, StatusValidated},
	{"rejected->queued", []Status{StatusValidated, StatusQueued, StatusSubmitted, StatusRejected}, StatusRejected, StatusQueued},
	{"failed->submitted", []Status{StatusValidated, StatusQueued, StatusSubmitted, StatusFailed}, StatusFailed, StatusSubmitted},
}

// INV-SM-02: every illegal edge -> ErrIllegalTransition, with NO status
// change, NO new history row, NO new audit row -- covers illegal edges out of
// draft/validated/queued/submitted, plus a representative outgoing edge from
// each of the 3 terminals (accepted/rejected/failed have no legal outgoing
// edge at all, AC-1).
func TestTransition_IllegalEdgesRejectedNoWrites(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	for _, tc := range illegalEdges {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tenantID := seedTenant(t, super, "INV-SM-02 "+tc.name+" tenant")
			entityID := seedEntity(t, super, tenantID, "INV-SM-02 entity")
			c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

			inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "INV-SM-02-" + tc.name})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			for _, hop := range tc.prehops {
				if _, err := store.Transition(c, inv.ID, hop); err != nil {
					t.Fatalf("pre-hop Transition(-> %s): %v", hop, err)
				}
			}

			beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
			beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

			_, err = store.Transition(c, inv.ID, tc.target)
			if !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("Transition(%s -> %s) err = %v, want ErrIllegalTransition", tc.from, tc.target, err)
			}

			var status string
			if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
				t.Fatalf("read back status: %v", err)
			}
			if Status(status) != tc.from {
				t.Errorf("invoices.status after illegal Transition = %q, want unchanged %q", status, tc.from)
			}
			if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
				t.Errorf("invoice_status_history rows after illegal Transition = %d, want unchanged %d", n, beforeHistory)
			}
			if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeAudit {
				t.Errorf("audit_log invoice.transitioned rows after illegal Transition = %d, want unchanged %d", n, beforeAudit)
			}
		})
	}
}

// redundantCase is one row of the no-op (current==target) table, including a
// terminal->same case, so the ordering assertion below ("redundant checked
// BEFORE legality") is exercised on a state that would ALSO be illegal if the
// implementation (wrongly) checked legality first.
type redundantCase struct {
	name    string
	prehops []Status
	status  Status
}

var redundantCases = []redundantCase{
	{"draft->draft", nil, StatusDraft},
	{"validated->validated", []Status{StatusValidated}, StatusValidated},
	{"accepted->accepted (terminal)", []Status{StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted}, StatusAccepted},
}

// INV-SM-03: a no-op (current==target) -> ErrRedundantTransition, with no
// history/audit writes, and this check fires BEFORE the legality check (a
// self-edge is never a member of legalTransitions, so a legality-first
// implementation would wrongly return ErrIllegalTransition instead).
func TestTransition_RedundantRejectedBeforeLegalityCheck(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	for _, tc := range redundantCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tenantID := seedTenant(t, super, "INV-SM-03 "+tc.name+" tenant")
			entityID := seedEntity(t, super, tenantID, "INV-SM-03 entity")
			c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

			inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "INV-SM-03-" + tc.name})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			for _, hop := range tc.prehops {
				if _, err := store.Transition(c, inv.ID, hop); err != nil {
					t.Fatalf("pre-hop Transition(-> %s): %v", hop, err)
				}
			}

			beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
			beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

			_, err = store.Transition(c, inv.ID, tc.status)
			if !errors.Is(err, ErrRedundantTransition) {
				t.Fatalf("Transition(%s -> %s, no-op) err = %v, want ErrRedundantTransition (checked before legality)", tc.status, tc.status, err)
			}

			var status string
			if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
				t.Fatalf("read back status: %v", err)
			}
			if Status(status) != tc.status {
				t.Errorf("invoices.status after redundant Transition = %q, want unchanged %q", status, tc.status)
			}
			if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != beforeHistory {
				t.Errorf("invoice_status_history rows after redundant Transition = %d, want unchanged %d", n, beforeHistory)
			}
			if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeAudit {
				t.Errorf("audit_log invoice.transitioned rows after redundant Transition = %d, want unchanged %d", n, beforeAudit)
			}
		})
	}
}

// INV-SM-04: a transition targeting a nonexistent id, or a cross-tenant id
// under the wrong tenant's GUC, resolves to ErrNotFound; the target row (when
// it exists, cross-tenant case) is left unchanged. The dedicated RLS
// isolation angle on the cross-tenant sub-case (re-reading under the OWNING
// tenant's own GUC, not just the superuser bypass) lives in
// TestRLS_InvoicesTransitionCrossTenantRefused (cross_tenant_integration_test.go).
func TestTransition_NotFoundAndCrossTenant(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	t.Run("nonexistent id", func(t *testing.T) {
		tenantID := seedTenant(t, super, "INV-SM-04 tenant")
		c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		bogusID := uuid.NewString()
		if _, err := store.Transition(c, bogusID, StatusValidated); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Transition(nonexistent id) err = %v, want ErrNotFound", err)
		}
	})

	t.Run("cross-tenant id", func(t *testing.T) {
		tenantA := seedTenant(t, super, "INV-SM-04 tenant A")
		tenantB := seedTenant(t, super, "INV-SM-04 tenant B")
		entityB := seedEntity(t, super, tenantB, "INV-SM-04 B entity")
		invoiceB := seedInvoice(t, super, tenantB, entityB, "INV-SM-04-B")

		cA := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantA})

		beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invoiceB)

		if _, err := store.Transition(cA, invoiceB, StatusValidated); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Transition(tenant B's invoice) as tenant A err = %v, want ErrNotFound", err)
		}

		var status string
		if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceB).Scan(&status); err != nil {
			t.Fatalf("read back status: %v", err)
		}
		if status != string(StatusDraft) {
			t.Errorf("tenant B's invoice status after refused cross-tenant Transition = %q, want unchanged %q", status, StatusDraft)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invoiceB); n != beforeHistory {
			t.Errorf("tenant B's invoice_status_history rows after refused cross-tenant Transition = %d, want unchanged %d", n, beforeHistory)
		}
	})
}

// INV-SM-05: atomicity -- status+history+audit are all-or-nothing, the same
// mechanism as INV-STORE-07, exercised on a LEGAL edge (draft->validated) via
// a crafted-identity actor injection at the Transition call itself (a normal
// actor is used for Create, so the genesis history/audit rows are unaffected
// -- only the tested transition's own writes are expected to roll back).
//
//   - (a) empty Subject -> the invoice_status_history INSERT fails
//     `char_length(actor) > 0` (23514) BEFORE audit.Record runs at all --
//     status UPDATE (already issued earlier in the same tx) rolls back too.
//   - (b) 256-char Subject -> history passes (no upper bound there) but
//     audit_log's `char_length(actor) <= 255` CHECK fails (23514) -- by this
//     point status+history were both (transiently) written and must ALL
//     roll back.
//
// After either: status is unchanged (still the pre-transition draft value),
// no new history row, no new audit row for invoice.transitioned.
func TestTransition_AtomicityRollsBackOnActorCheckFailure(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	assertNothingWritten := func(t *testing.T, tenantID, invoiceID string, beforeHistory, beforeAudit int) {
		t.Helper()
		var status string
		if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invoiceID).Scan(&status); err != nil {
			t.Fatalf("read back status: %v", err)
		}
		if status != string(StatusDraft) {
			t.Errorf("invoice status after failed Transition = %q, want unchanged %q", status, StatusDraft)
		}
		if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invoiceID); n != beforeHistory {
			t.Errorf("invoice_status_history rows after failed Transition = %d, want unchanged %d (no new row)", n, beforeHistory)
		}
		if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeAudit {
			t.Errorf("audit_log invoice.transitioned rows after failed Transition = %d, want unchanged %d", n, beforeAudit)
		}
	}

	t.Run("empty actor fails history CHECK before audit (23514)", func(t *testing.T) {
		tenantID := seedTenant(t, super, "INV-SM-05a tenant")
		entityID := seedEntity(t, super, tenantID, "INV-SM-05a entity")
		cNormal := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		inv, err := store.Create(cNormal, CreateInput{EntityID: entityID, InvoiceNumber: "INV-SM-05a"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
		beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

		cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: "", Role: "authenticated", TenantID: tenantID})
		_, err = store.Transition(cCrafted, inv.ID, StatusValidated)
		if err == nil {
			t.Fatal("Transition with an empty-Subject actor succeeded, want a history CHECK violation (SQLSTATE 23514)")
		}
		if code := pgCode(err); code != "23514" {
			t.Fatalf("Transition with an empty-Subject actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
		}

		assertNothingWritten(t, tenantID, inv.ID, beforeHistory, beforeAudit)
	})

	t.Run("256-char actor passes history CHECK but fails audit_log CHECK (23514)", func(t *testing.T) {
		tenantID := seedTenant(t, super, "INV-SM-05b tenant")
		entityID := seedEntity(t, super, tenantID, "INV-SM-05b entity")
		cNormal := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

		inv, err := store.Create(cNormal, CreateInput{EntityID: entityID, InvoiceNumber: "INV-SM-05b"})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID)
		beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

		longSubject := strings.Repeat("a", 256)
		cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: longSubject, Role: "authenticated", TenantID: tenantID})
		_, err = store.Transition(cCrafted, inv.ID, StatusValidated)
		if err == nil {
			t.Fatal("Transition with a 256-char actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
		}
		if code := pgCode(err); code != "23514" {
			t.Fatalf("Transition with a 256-char actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
		}

		assertNothingWritten(t, tenantID, inv.ID, beforeHistory, beforeAudit)
	})
}

// INV-SM-06: SELECT ... FOR UPDATE serializes concurrent transitions on the
// same row -- of N concurrent draft->validated calls on the SAME invoice,
// exactly one succeeds; every other resolves to ErrRedundantTransition (the
// row lock forces the losers to observe the winner's already-applied
// status), and exactly one invoice_status_history row (to_status=validated)
// exists afterward. Using N=6 (rather than the spec's minimal "two") gives
// the race a stronger chance to manifest if serialization is broken.
func TestTransition_ConcurrentSameEdgeSerializesToOneWinner(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-SM-06 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-SM-06 entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "INV-SM-06"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = store.Transition(c, inv.ID, StatusValidated)
		}(i)
	}
	wg.Wait()

	successes, redundants, other := 0, 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			successes++
		case errors.Is(e, ErrRedundantTransition):
			redundants++
		default:
			other++
			t.Errorf("concurrent Transition returned unexpected error: %v", e)
		}
	}
	if successes != 1 {
		t.Errorf("concurrent Transition successes = %d, want exactly 1", successes)
	}
	if redundants != n-1 {
		t.Errorf("concurrent Transition ErrRedundantTransition count = %d, want %d", redundants, n-1)
	}

	var status string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&status); err != nil {
		t.Fatalf("read back status: %v", err)
	}
	if Status(status) != StatusValidated {
		t.Errorf("invoice status after concurrent transitions = %q, want %q", status, StatusValidated)
	}
	if hn := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1 AND to_status = 'validated'`, inv.ID); hn != 1 {
		t.Errorf("invoice_status_history rows (to_status=validated) = %d, want exactly 1 (FOR UPDATE serialized the race)", hn)
	}
}

// INV-SM-07: after a legal transition, the newest invoice_status_history.actor
// and the newest audit_log.actor both equal the caller's Subject.
// (TestTransition_LegalEdgesSucceedWithTripleWrite already asserts this per
// edge; this pins it as its own minimal case, matching the Test Specs table
// 1:1.)
func TestTransition_HistoryAndAuditActorMatchCallerSubject(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-SM-07 tenant")
	entityID := seedEntity(t, super, tenantID, "INV-SM-07 entity")

	store := NewStore(app)
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "INV-SM-07"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.Transition(c, inv.ID, StatusValidated); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	var historyActor string
	if err := super.QueryRow(ctx,
		`SELECT actor FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at DESC LIMIT 1`, inv.ID,
	).Scan(&historyActor); err != nil {
		t.Fatalf("read newest history actor: %v", err)
	}
	if historyActor != subject {
		t.Errorf("newest invoice_status_history.actor = %q, want %q", historyActor, subject)
	}

	if got := auditActor(t, app, tenantID, "invoice.transitioned"); got != subject {
		t.Errorf("newest audit_log.actor for invoice.transitioned = %q, want %q", got, subject)
	}
}

// Sole-writer invariant (AC-4, optional per the story): after a REJECTED
// Transition (illegal edge), status is byte-identical to its pre-call value
// -- reinforcing that no method other than Transition ever writes
// invoices.status (adversarial_test.go's TestStoreUpdate_NeverChangesStatus
// covers the Update side of this same invariant).
func TestTransition_RejectedTransitionLeavesStatusByteIdentical(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()

	tenantID := seedTenant(t, super, "INV-SM-SOLE-WRITER tenant")
	entityID := seedEntity(t, super, tenantID, "INV-SM-SOLE-WRITER entity")

	store := NewStore(app)
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "INV-SM-SOLE-WRITER"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var before string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&before); err != nil {
		t.Fatalf("read back status (before): %v", err)
	}

	if _, err := store.Transition(c, inv.ID, StatusAccepted); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("Transition(draft -> accepted) err = %v, want ErrIllegalTransition", err)
	}

	var after string
	if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, inv.ID).Scan(&after); err != nil {
		t.Fatalf("read back status (after): %v", err)
	}
	if after != before {
		t.Errorf("invoices.status after rejected Transition = %q, want byte-identical to pre-call %q", after, before)
	}
}

// TestTransition_ValidatedToDraftLegalityUnit is a DB-free unit pin on
// canTransition(validated, draft): the M4-05 fix loop needs this demotion
// edge to become legal so a rejected/dirty validated invoice can be sent back
// to draft for correction. This is independent of (and narrower than) the
// DB-backed TestTransition_ExhaustiveMatrixLocksLegalEdgeTable, which already
// covers the same edge as part of its full 49-pair sweep -- this test just
// pins canTransition's own return value directly, with no DB required. Also
// spot-checks that a couple of unrelated illegal edges stay illegal, so a
// change that makes canTransition permissive across the board would not
// pass this test by accident.
func TestTransition_ValidatedToDraftLegalityUnit(t *testing.T) {
	if !canTransition(StatusValidated, StatusDraft) {
		t.Errorf("canTransition(validated, draft) = false, want true (M4-05 demotion edge)")
	}
	if canTransition(StatusDraft, StatusAccepted) {
		t.Errorf("canTransition(draft, accepted) = true, want false (unrelated illegal edge must stay illegal)")
	}
	if canTransition(StatusQueued, StatusDraft) {
		t.Errorf("canTransition(queued, draft) = true, want false (unrelated illegal edge must stay illegal)")
	}
}

// TestTransition_QueuedToFailedLegalityUnit is a DB-free unit pin on
// canTransition(queued, failed) -- M5-04-02's (task-233) dead-letter edge: a
// background worker that gives up on an invoice before it ever reaches
// submitted needs a legal queued->failed edge to drive Store.MarkFailedTx.
// Mirrors TestTransition_ValidatedToDraftLegalityUnit's shape (a direct
// canTransition pin, independent of and narrower than the DB-backed
// TestTransition_ExhaustiveMatrixLocksLegalEdgeTable's full 49-pair sweep).
// M5-05-01 (task-237) extends this same pin with its own three edges:
// queued->accepted/rejected (AC#3, the synchronous-verdict shortcuts around
// submitted) and rejected->draft (AC#4, the rework edge) all flip to legal.
// failed->queued stays illegal -- [failed-invoices] is enforced, not merely
// documented: M5-05-01 (task-237) adds exactly three edges, not four, so a change that
// widened canTransition across the board (or, distinctly, one that added
// failed->queued specifically) would not pass this test by accident.
func TestTransition_QueuedToFailedLegalityUnit(t *testing.T) {
	if !canTransition(StatusQueued, StatusFailed) {
		t.Errorf("canTransition(queued, failed) = false, want true (M5-04-02 dead-letter edge)")
	}
	if !canTransition(StatusQueued, StatusAccepted) {
		t.Errorf("canTransition(queued, accepted) = false, want true (M5-05-01 (task-237)'s synchronous-verdict edge, AC#3)")
	}
	if !canTransition(StatusQueued, StatusRejected) {
		t.Errorf("canTransition(queued, rejected) = false, want true (M5-05-01 (task-237)'s synchronous-verdict edge, AC#3)")
	}
	if canTransition(StatusFailed, StatusQueued) {
		t.Errorf("canTransition(failed, queued) = true, want false ([failed-invoices]: failed stays terminal, M5-05-01 (task-237) does not add this edge)")
	}
	if !canTransition(StatusRejected, StatusDraft) {
		t.Errorf("canTransition(rejected, draft) = false, want true (M5-05-01 (task-237)'s rework edge, AC#4)")
	}
}

// TestTransition_RejectedHasExactlyOneOutgoingEdge is a DB-free unit pin:
// M5-05-01 (task-237) (AC#4) adds exactly ONE outgoing edge from rejected --
// rejected->draft, the rework path -- every other non-self target stays
// illegal. This is independent of (and narrower than) the DB-backed sibling
// assertion in TestTransition_TerminalStatesHaveNoLegalOutgoingEdges
// (transition_adversarial_test.go), which proves the same shape through the
// real Store.Transition rather than canTransition directly.
func TestTransition_RejectedHasExactlyOneOutgoingEdge(t *testing.T) {
	if !canTransition(StatusRejected, StatusDraft) {
		t.Errorf("canTransition(rejected, draft) = false, want true (M5-05-01 (task-237)'s rework edge, AC#4)")
	}
	for _, target := range []Status{StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted, StatusFailed} {
		if canTransition(StatusRejected, target) {
			t.Errorf("canTransition(rejected, %s) = true, want false (rejected has exactly ONE outgoing edge: ->draft)", target)
		}
	}
}

// TestTransition_FailedToQueuedStaysIllegal pins [failed-invoices]: unlike
// rejected, failed stays a true terminal after M5-05-01 (task-237) -- failed->queued is
// explicitly NOT one of the three edges this subtask adds (queued->accepted,
// queued->rejected, rejected->draft). Passes vacuously today (canTransition
// already returns false here, before M5-05-01 (task-237) touches legalTransitions at
// all) -- it exists as its own explicitly-named regression guard, distinct
// from TestTransition_QueuedToFailedLegalityUnit's spot-check of the same
// fact, so a future change that widens failed's map entry trips a
// purpose-built test rather than only a side-assertion buried in another.
func TestTransition_FailedToQueuedStaysIllegal(t *testing.T) {
	if canTransition(StatusFailed, StatusQueued) {
		t.Errorf("canTransition(failed, queued) = true, want false ([failed-invoices]: failed stays terminal, M5-05-01 (task-237) does not add this edge)")
	}
}

// --- INVED-01-03 (task-264): canEdit/canRevalidate derived from the machine -

// TestCanEdit_AllStatuses (INV-03-T1): folded over all 7 statuses, canEdit
// yields exactly {draft, validated, rejected} -- byte-identical to the
// 3-status guard it replaces at Store.Edit (store.go, [A8] fixable-state
// guard) (Core AC 2).
func TestCanEdit_AllStatuses(t *testing.T) {
	want := map[Status]bool{
		StatusDraft:     true,
		StatusValidated: true,
		StatusQueued:    false,
		StatusSubmitted: false,
		StatusAccepted:  false,
		StatusRejected:  true,
		StatusFailed:    false,
	}
	for _, s := range allStatuses {
		if got := canEdit(s); got != want[s] {
			t.Errorf("canEdit(%s) = %v, want %v", s, got, want[s])
		}
	}
}

// TestCanEdit_AgreesWithIndependentLegalEdgeOracle (INV-03-T2, rewritten from
// the story's original self-referential spec): canEdit(s) is checked against
// wantLegalEdge (transition_adversarial_test.go:47-59), an oracle hand-
// written INDEPENDENTLY of legalTransitions/canTransition -- see that file's
// header (:42-46) for why the oracle must stay independent. Comparing
// against canTransition itself here would make this a tautology (the
// original bug this rewrite fixes).
func TestCanEdit_AgreesWithIndependentLegalEdgeOracle(t *testing.T) {
	for _, s := range allStatuses {
		want := s == StatusDraft || wantLegalEdge[[2]Status{s, StatusDraft}]
		if got := canEdit(s); got != want {
			t.Errorf("canEdit(%s) = %v, want %v (per the independent wantLegalEdge oracle, not canTransition)", s, got, want)
		}
	}
}

// TestCanRevalidate_AllStatuses (INV-03-T3): canRevalidate yields exactly
// {draft} -- the gate's draft-only rule, stated once (Core AC 3).
func TestCanRevalidate_AllStatuses(t *testing.T) {
	for _, s := range allStatuses {
		want := s == StatusDraft
		if got := canRevalidate(s); got != want {
			t.Errorf("canRevalidate(%s) = %v, want %v", s, got, want)
		}
	}
}

// TestLegalTransitions_SetEqualsIndependentOracle (INV-03-T4, strengthened
// from a mere edge-count check): flattens legalTransitions to its
// (from,target) pairs and asserts SET EQUALITY against the independent
// wantLegalEdge oracle in BOTH directions -- not just a count of 11 -- so
// neither an added nor a dropped edge slips through unnoticed. Pins that this
// subtask adds/removes no lifecycle state or transition edge (AC 5, Out of
// Scope). Does not touch canEdit/canRevalidate, so this is unaffected by
// their R0 stubs.
func TestLegalTransitions_SetEqualsIndependentOracle(t *testing.T) {
	got := map[[2]Status]bool{}
	for from, targets := range legalTransitions {
		for _, target := range targets {
			got[[2]Status{from, target}] = true
		}
	}
	if len(got) != 11 {
		t.Errorf("legalTransitions has %d edges, want 11", len(got))
	}
	for pair := range wantLegalEdge {
		if !got[pair] {
			t.Errorf("legalTransitions is missing edge %s->%s present in the independent wantLegalEdge oracle", pair[0], pair[1])
		}
	}
	for pair := range got {
		if !wantLegalEdge[pair] {
			t.Errorf("legalTransitions has edge %s->%s NOT present in the independent wantLegalEdge oracle", pair[0], pair[1])
		}
	}
	if !got[[2]Status{StatusValidated, StatusDraft}] {
		t.Error("legalTransitions is missing validated->draft")
	}
	if !got[[2]Status{StatusRejected, StatusDraft}] {
		t.Error("legalTransitions is missing rejected->draft")
	}
}

// edgeTableWithout returns a deep copy of orig with the (from,target) edge
// removed -- a fresh map with fresh slices, never mutating orig or any slice
// it shares. Used by TestCanEdit_TracksLegalTransitions to perturb the
// package-level legalTransitions var safely.
func edgeTableWithout(orig map[Status][]Status, from, target Status) map[Status][]Status {
	cp := make(map[Status][]Status, len(orig))
	for k, v := range orig {
		fresh := make([]Status, 0, len(v))
		for _, s := range v {
			if k == from && s == target {
				continue
			}
			fresh = append(fresh, s)
		}
		cp[k] = fresh
	}
	return cp
}

// edgeTableWith returns a deep copy of orig with the (from,target) edge
// added -- a fresh map with fresh slices, never mutating orig or any slice it
// shares.
func edgeTableWith(orig map[Status][]Status, from, target Status) map[Status][]Status {
	cp := make(map[Status][]Status, len(orig))
	for k, v := range orig {
		fresh := make([]Status, len(v))
		copy(fresh, v)
		cp[k] = fresh
	}
	cp[from] = append(cp[from], target)
	return cp
}

// TestCanEdit_TracksLegalTransitions (INV-03-T8, the AC-1/AC-4 enforcement
// mechanism): T1/T2 pass for a hardcoded []Status{draft, validated, rejected}
// literal TODAY, because the literal and the derivation happen to agree on
// the current table -- neither one actually enforces "canEdit is DERIVED,
// never a literal" (Core AC 4). This test perturbs legalTransitions at
// runtime (deep copy via edgeTableWithout/edgeTableWith, t.Cleanup restore)
// and asserts canEdit tracks the change; a hardcoded literal fails every
// assertion below.
//
// Safe with NO t.Parallel: verified zero t.Parallel() calls anywhere in
// internal/invoice, so this package's test functions run strictly
// sequentially and the global var swap below cannot race another test. The
// day someone adds t.Parallel() to this package, this global-var swap
// becomes unsafe -- this comment is the warning.
func TestCanEdit_TracksLegalTransitions(t *testing.T) {
	orig := legalTransitions
	t.Cleanup(func() { legalTransitions = orig })

	// A: drop rejected->draft. A DERIVED canEdit stops admitting rejected;
	// a hardcoded {draft,validated,rejected} literal keeps admitting it.
	legalTransitions = edgeTableWithout(orig, StatusRejected, StatusDraft)
	if canEdit(StatusRejected) {
		t.Error("canEdit(rejected) = true after rejected->draft was removed -- canEdit is NOT derived from legalTransitions (hardcoded status list?)")
	}
	if !canEdit(StatusDraft) {
		t.Error("canEdit(draft) must stay true -- draft is editable by definition, not by edge")
	}
	if !canEdit(StatusValidated) {
		t.Error("canEdit(validated) must be unaffected by the rejected edge")
	}

	// B: add failed->draft. A DERIVED canEdit admits failed; a literal does
	// not.
	legalTransitions = edgeTableWith(orig, StatusFailed, StatusDraft)
	if !canEdit(StatusFailed) {
		t.Error("canEdit(failed) = false after failed->draft was added -- canEdit is NOT derived from legalTransitions")
	}
}

// TestCanRevalidate_AgreesWithThePromotionEdge (INV-03-T9, tripwire): pins
// the deliberately-NOT-derived canRevalidate literal against the machine by
// asserting canRevalidate(s) == canTransition(s, StatusValidated) for all 7
// statuses. canRevalidate stays a hand-written s == StatusDraft literal on
// purpose (Store.ApplyValidation's promotion hardwires its FROM state to
// draft via transitionTx, so draft-only-ness is the gate's OWN contract, not
// an edge-table property, [gate-scope-draft-only]) -- but if anyone ever adds
// an inbound edge to validated from a state other than draft, this test goes
// red and forces a human decision on whether the gate should widen too.
func TestCanRevalidate_AgreesWithThePromotionEdge(t *testing.T) {
	for _, s := range allStatuses {
		want := canTransition(s, StatusValidated)
		if got := canRevalidate(s); got != want {
			t.Errorf("canRevalidate(%s) = %v, canTransition(%s, validated) = %v -- want equal; an inbound edge to validated was added -- decide whether the gate widens; canRevalidate is deliberately a literal, see its doc", s, got, s, want)
		}
	}
}

// --- M5-09-02 (task-255): the clear moves into transitionTx -----------------

// TestStoreTransition_AcceptedViaHandlerPathClearsRejectionReasons
// (M5-09-02/task-255, AC-3): `accepted` has TWO live writers -- the worker's
// MarkAcceptedTx (proved by TestMarkAcceptedTx_ClearsRejectionReasons,
// outcome_loop_test.go) and THIS one, a direct POST /v1/invoices/{id}/
// transitions {"target":"accepted"} on a queued invoice, which
// TransitionHandler refuses nothing for (it guards only StatusValidated,
// handlers.go:372-374) and cmd/invoice/main.go:60 wires production-reachable.
// This test drives Store.Transition directly and NEVER calls MarkAcceptedTx
// -- so it is deliberately blind to whatever MarkAcceptedTx's own outcome
// closure does. It FAILS if the clear is placed in MarkAcceptedTx instead of
// transitionTx: that placement would leave THIS path's invoice accepted with
// its rejection_reasons still populated, which M5-09-05's detail surface
// would then render as a rejection card sitting on an accepted invoice --
// exactly the regression this subtask's design note names as load-bearing.
func TestStoreTransition_AcceptedViaHandlerPathClearsRejectionReasons(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "TR-ACC-CLR tenant")
	entityID := seedEntity(t, super, tenantID, "TR-ACC-CLR entity")
	c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "TR-ACC-CLR", StatusQueued)
	seededReasons := `[{"code":"APP-ERR-0417","message":"Supplier TIN not registered","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, seededReasons, invID,
	); err != nil {
		t.Fatalf("seed rejection_reasons: %v", err)
	}

	got, err := store.Transition(c, invID, StatusAccepted)
	if err != nil {
		t.Fatalf("Transition(queued->accepted): %v (want nil)", err)
	}
	if got.Status != StatusAccepted {
		t.Errorf("Transition returned status = %q, want %q", got.Status, StatusAccepted)
	}

	var reasons, typeofReasons string
	if err := super.QueryRow(ctx,
		`SELECT rejection_reasons::text, jsonb_typeof(rejection_reasons) FROM invoices WHERE id = $1`, invID,
	).Scan(&reasons, &typeofReasons); err != nil {
		t.Fatalf("read back rejection_reasons: %v", err)
	}
	if reasons != "[]" {
		t.Errorf("rejection_reasons after Transition(->accepted) = %q, want %q (this path bypasses MarkAcceptedTx entirely -- the clear must live in transitionTx, not MarkAcceptedTx's outcome closure)", reasons, "[]")
	}
	if typeofReasons != "array" {
		t.Errorf("jsonb_typeof(rejection_reasons) after Transition(->accepted) = %q, want %q (never JSON null)", typeofReasons, "array")
	}
}

// TestStoreTransition_AcceptedClearFailureStillRollsBack (M5-09-02/task-255,
// AC-4; REPLACES the retired TestStoreEdit_FailureAfterReasonsClearStillRollsBack,
// edit_test.go, re-premised onto the clear's new home): a queued invoice
// with populated rejection_reasons, transitioned to accepted under a
// 256-char actor Subject -- passes invoice_status_history's actor CHECK (no
// upper bound) but fails audit_log's (char_length <= 255), 23514, aborting
// the whole db.WithinRequestTenantTx. Mirrors
// TestTransition_AtomicityRollsBackOnActorCheckFailure's 256-char-actor case
// (above): the conditional `rejection_reasons = '[]'` clause riding on
// transitionTx's single UPDATE ... RETURNING must roll back together with
// the status change it shares one statement with -- no second, separately
// committed write.
func TestStoreTransition_AcceptedClearFailureStillRollsBack(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "TR-ACC-RB tenant")
	entityID := seedEntity(t, super, tenantID, "TR-ACC-RB entity")

	invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "TR-ACC-RB", StatusQueued)
	seededReasons := `[{"code":"APP-ERR-0417","message":"Supplier TIN not registered","path":"supplier_tin"}]`
	if _, err := super.Exec(ctx,
		`UPDATE invoices SET rejection_reasons = $1::jsonb WHERE id = $2`, seededReasons, invID,
	); err != nil {
		t.Fatalf("seed rejection_reasons: %v", err)
	}
	var beforeReasons string
	if err := super.QueryRow(ctx, `SELECT rejection_reasons::text FROM invoices WHERE id = $1`, invID).Scan(&beforeReasons); err != nil {
		t.Fatalf("read back seeded rejection_reasons: %v", err)
	}

	beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
	beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

	longSubject := strings.Repeat("a", 256)
	cCrafted := auth.WithIdentity(ctx, auth.Identity{Subject: longSubject, Role: "authenticated", TenantID: tenantID})
	_, err := store.Transition(cCrafted, invID, StatusAccepted)
	if err == nil {
		t.Fatal("Transition(queued->accepted) with a 256-char actor succeeded, want an audit_log actor CHECK violation (SQLSTATE 23514)")
	}
	if code := pgCode(err); code != "23514" {
		t.Fatalf("Transition with a 256-char actor: pgCode = %q, want 23514 (check_violation): %v", code, err)
	}

	var status, afterReasons string
	if err := super.QueryRow(ctx,
		`SELECT status, rejection_reasons::text FROM invoices WHERE id = $1`, invID,
	).Scan(&status, &afterReasons); err != nil {
		t.Fatalf("read back status/rejection_reasons: %v", err)
	}
	if Status(status) != StatusQueued {
		t.Errorf("status after rolled-back Transition = %q, want unchanged %q", status, StatusQueued)
	}
	if afterReasons != beforeReasons {
		t.Errorf("rejection_reasons after rolled-back Transition = %q, want byte-unchanged %q (the clear rides the same UPDATE as the status change -- it must roll back with it, not survive as a separate write)", afterReasons, beforeReasons)
	}
	if afterReasons == "[]" {
		t.Errorf("rejection_reasons after rolled-back Transition = %q, a rolled-back Transition must never observably clear it", afterReasons)
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID); n != beforeHistory {
		t.Errorf("invoice_status_history rows = %d, want unchanged %d", n, beforeHistory)
	}
	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != beforeAudit {
		t.Errorf("audit_log invoice.transitioned rows = %d, want unchanged %d", n, beforeAudit)
	}
}
