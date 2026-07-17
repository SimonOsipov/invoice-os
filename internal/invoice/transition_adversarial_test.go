// M4-02-02 (task-97): QA adversarial coverage ON TOP OF INV-SM-01..07 +
// TestRLS_InvoicesTransitionCrossTenantRefused (transition_test.go /
// cross_tenant_integration_test.go), written during the Mode B
// (post-implementation) verify pass. transition_test.go's INV-SM-01/02/03
// exercise 7 legal + 12 representative-illegal + 3 redundant cases -- a
// deliberately partial sample of the 7x7 = 49 ordered (from,target) pairs.
// This file closes that gap with an EXHAUSTIVE matrix over all 49 pairs
// (independent of legalTransitions/canTransition, so it actually locks the
// edge table rather than restating it), an explicit terminal-dead-end pin,
// and a multi-hop invoice_status_history chain-integrity check that
// INV-SM-01 never exercises (each of its subtests only asserts the ONE newest
// row after its own final hop, never the full accumulated chain across
// several real Transition calls). Reuses the dbTestPools/seedTenant/
// seedEntity/seedInvoice/mustCount/auditCount/strPtr harness from
// store_test.go (same package).
package invoice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SimonOsipov/invoice-os/internal/platform/auth"
)

// allStatuses is the full 7-state universe for the exhaustive matrix below,
// listed independently of legalTransitions' map keys in store.go.
var allStatuses = []Status{
	StatusDraft, StatusValidated, StatusQueued, StatusSubmitted,
	StatusAccepted, StatusRejected, StatusFailed,
}

// wantLegalEdge is a HARD-CODED, independent restatement of the story's
// 7-edge table (System Design / Test Specs, M4-02-02) -- deliberately NOT
// derived from canTransition/legalTransitions in store.go. If the matrix test
// below instead asked canTransition for the expected outcome, a future edit
// that silently added or dropped an edge in legalTransitions would make the
// oracle drift in lockstep with the bug and the test would never catch it --
// exactly the regression this file exists to catch.
var wantLegalEdge = map[[2]Status]bool{
	{StatusDraft, StatusValidated}:    true,
	{StatusValidated, StatusQueued}:   true,
	{StatusQueued, StatusSubmitted}:   true,
	{StatusSubmitted, StatusAccepted}: true,
	{StatusSubmitted, StatusRejected}: true,
	{StatusSubmitted, StatusFailed}:   true,
	{StatusValidated, StatusDraft}:    true,
}

// seedInvoiceAtStatus creates a normal draft invoice (via seedInvoice) then,
// unless status is 'draft' itself, force-writes invoices.status directly as
// the superuser -- bypassing Store.Transition entirely. This is the
// "superuser status write helper" the exhaustive matrix needs to seed all 7
// starting states without driving 1-4 real hops per one of the 49 cases;
// invoices.status's own CHECK constraint only enumerates the 7 values (no
// state-machine awareness at the schema layer,
// migrations/20260714103137_invoices.sql), so any raw value is accepted
// regardless of Transition's app-level legality rules.
func seedInvoiceAtStatus(t *testing.T, super *pgxpool.Pool, tenantID, entityID, number string, status Status) string {
	t.Helper()
	id := seedInvoice(t, super, tenantID, entityID, number)
	if status != StatusDraft {
		if _, err := super.Exec(context.Background(),
			`UPDATE invoices SET status = $1 WHERE id = $2`, string(status), id,
		); err != nil {
			t.Fatalf("force-seed invoice status to %q: %v", status, err)
		}
	}
	return id
}

// TestTransition_ExhaustiveMatrixLocksLegalEdgeTable drives all 7x7 = 49
// ordered (from,target) pairs through the REAL Store.Transition and asserts
// the outcome class against the hard-coded wantLegalEdge oracle above: the 7
// legal pairs must succeed (status=target, +1 history row, +1
// invoice.transitioned audit row); the 7 self-pairs must resolve to
// ErrRedundantTransition; the remaining 35 must resolve to
// ErrIllegalTransition -- the non-success classes leaving
// status/history/audit completely unchanged. This pins legalTransitions'
// shape completely: a future edit that silently added or dropped ANY edge
// (including a pair transition_test.go's representative sample never
// touches) flips exactly one subtest's expected outcome and fails it.
func TestTransition_ExhaustiveMatrixLocksLegalEdgeTable(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	legalCount, redundantCount, illegalCount := 0, 0, 0

	for _, from := range allStatuses {
		for _, target := range allStatuses {
			from, target := from, target
			name := string(from) + "->" + string(target)
			t.Run(name, func(t *testing.T) {
				tenantID := seedTenant(t, super, "MATRIX "+name+" tenant")
				entityID := seedEntity(t, super, tenantID, "MATRIX entity")
				c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

				invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "MATRIX-"+name, from)

				beforeHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
				beforeAudit := auditCount(t, app, tenantID, "invoice.transitioned")

				_, err := store.Transition(c, invID, target)

				isLegal := from != target && wantLegalEdge[[2]Status{from, target}]

				switch {
				case from == target:
					redundantCount++
					if !errors.Is(err, ErrRedundantTransition) {
						t.Fatalf("%s (self-edge): err = %v, want ErrRedundantTransition", name, err)
					}
				case isLegal:
					legalCount++
					if err != nil {
						t.Fatalf("%s (legal edge): err = %v, want success", name, err)
					}
				default:
					illegalCount++
					if !errors.Is(err, ErrIllegalTransition) {
						t.Fatalf("%s (illegal edge): err = %v, want ErrIllegalTransition", name, err)
					}
				}

				var status string
				if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
					t.Fatalf("read back status: %v", err)
				}
				afterHistory := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, invID)
				afterAudit := auditCount(t, app, tenantID, "invoice.transitioned")

				if isLegal {
					if Status(status) != target {
						t.Errorf("%s: invoices.status = %q, want %q", name, status, target)
					}
					if afterHistory != beforeHistory+1 {
						t.Errorf("%s: history rows = %d, want %d (+1)", name, afterHistory, beforeHistory+1)
					}
					if afterAudit != beforeAudit+1 {
						t.Errorf("%s: invoice.transitioned audit rows = %d, want %d (+1)", name, afterAudit, beforeAudit+1)
					}
				} else {
					if Status(status) != from {
						t.Errorf("%s: invoices.status = %q, want unchanged %q", name, status, from)
					}
					if afterHistory != beforeHistory {
						t.Errorf("%s: history rows = %d, want unchanged %d", name, afterHistory, beforeHistory)
					}
					if afterAudit != beforeAudit {
						t.Errorf("%s: invoice.transitioned audit rows = %d, want unchanged %d", name, afterAudit, beforeAudit)
					}
				}
			})
		}
	}

	if legalCount != 7 {
		t.Errorf("classified as legal = %d, want 7", legalCount)
	}
	if redundantCount != 7 {
		t.Errorf("classified as redundant (self-edge) = %d, want 7", redundantCount)
	}
	if illegalCount != 35 {
		t.Errorf("classified as illegal = %d, want 35", illegalCount)
	}
}

// TestTransition_TerminalStatesHaveNoLegalOutgoingEdges explicitly pins the
// terminal-state invariant (AC-1): accepted/rejected/failed have ZERO legal
// outgoing edges -- every non-self target from each of the 3 terminals ->
// ErrIllegalTransition, status left unchanged. This is already implied,
// pair-by-pair, by TestTransition_ExhaustiveMatrixLocksLegalEdgeTable above;
// it is pinned here as its own explicitly-named, minimal case (18 = 3
// terminals x 6 non-self targets) matching the terminal invariant the story
// calls out separately from general illegal-edge coverage.
func TestTransition_TerminalStatesHaveNoLegalOutgoingEdges(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	terminals := []Status{StatusAccepted, StatusRejected, StatusFailed}
	attempts, illegalCount := 0, 0

	for _, from := range terminals {
		for _, target := range allStatuses {
			if target == from {
				continue // self-edge is INV-SM-03's redundant case, not this invariant
			}
			from, target := from, target
			name := string(from) + "->" + string(target)
			t.Run(name, func(t *testing.T) {
				tenantID := seedTenant(t, super, "TERMINAL "+name+" tenant")
				entityID := seedEntity(t, super, tenantID, "TERMINAL entity")
				c := auth.WithIdentity(ctx, auth.Identity{Subject: uuid.NewString(), Role: "authenticated", TenantID: tenantID})

				invID := seedInvoiceAtStatus(t, super, tenantID, entityID, "TERMINAL-"+name, from)

				attempts++
				_, err := store.Transition(c, invID, target)
				if !errors.Is(err, ErrIllegalTransition) {
					t.Fatalf("%s: err = %v, want ErrIllegalTransition (terminal states have no legal outgoing edge)", name, err)
				}
				illegalCount++

				var status string
				if err := super.QueryRow(ctx, `SELECT status FROM invoices WHERE id = $1`, invID).Scan(&status); err != nil {
					t.Fatalf("read back status: %v", err)
				}
				if Status(status) != from {
					t.Errorf("%s: status = %q, want unchanged %q", name, status, from)
				}
			})
		}
	}

	if attempts != 18 {
		t.Fatalf("attempts = %d, want 18 (3 terminals x 6 non-self targets)", attempts)
	}
	if illegalCount != attempts {
		t.Errorf("illegal-classified = %d, want all %d attempts", illegalCount, attempts)
	}
}

// TestTransition_MultiHopHistoryIntegrityChain drives one invoice through the
// full draft->validated->queued->submitted->accepted lifecycle via 4 REAL
// Transition calls and asserts invoice_status_history accumulates the exact
// ordered chain -- the genesis NULL->draft row plus one row per hop, 5 total
// -- with monotonically non-decreasing changed_at, and exactly 4
// invoice.transitioned audit rows (one per hop, distinct from Create's 1
// invoice.created row). transition_test.go's INV-SM-01 subtests never chain
// more than one tested hop per invoice (each starts a fresh seed and only
// asserts the single newest history row after ITS hop), so the FULL
// accumulated chain across several real transitions is unexercised without
// this test.
func TestTransition_MultiHopHistoryIntegrityChain(t *testing.T) {
	super, app := dbTestPools(t)
	ctx := context.Background()
	store := NewStore(app)

	tenantID := seedTenant(t, super, "MULTIHOP tenant")
	entityID := seedEntity(t, super, tenantID, "MULTIHOP entity")
	subject := uuid.NewString()
	c := auth.WithIdentity(ctx, auth.Identity{Subject: subject, Role: "authenticated", TenantID: tenantID})

	inv, err := store.Create(c, CreateInput{EntityID: entityID, InvoiceNumber: "MULTIHOP"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	hops := []Status{StatusValidated, StatusQueued, StatusSubmitted, StatusAccepted}
	for _, target := range hops {
		if _, err := store.Transition(c, inv.ID, target); err != nil {
			t.Fatalf("Transition(-> %s): %v", target, err)
		}
	}

	if n := mustCount(t, super, `SELECT count(*) FROM invoice_status_history WHERE invoice_id = $1`, inv.ID); n != 5 {
		t.Fatalf("invoice_status_history row count = %d, want 5 (1 genesis + 4 hops)", n)
	}

	rows, err := super.Query(ctx,
		`SELECT from_status, to_status, changed_at FROM invoice_status_history WHERE invoice_id = $1 ORDER BY changed_at ASC`,
		inv.ID,
	)
	if err != nil {
		t.Fatalf("query history chain: %v", err)
	}
	defer rows.Close()

	type histRow struct {
		from      *string
		to        string
		changedAt time.Time
	}
	var got []histRow
	for rows.Next() {
		var r histRow
		if err := rows.Scan(&r.from, &r.to, &r.changedAt); err != nil {
			t.Fatalf("scan history row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate history rows: %v", err)
	}

	wantChain := []struct {
		from *string
		to   Status
	}{
		{nil, StatusDraft},
		{strPtr(string(StatusDraft)), StatusValidated},
		{strPtr(string(StatusValidated)), StatusQueued},
		{strPtr(string(StatusQueued)), StatusSubmitted},
		{strPtr(string(StatusSubmitted)), StatusAccepted},
	}
	if len(got) != len(wantChain) {
		t.Fatalf("history chain length = %d, want %d", len(got), len(wantChain))
	}
	for i, want := range wantChain {
		g := got[i]
		switch {
		case want.from == nil && g.from != nil:
			t.Errorf("chain[%d] from_status = %q, want nil (genesis row)", i, *g.from)
		case want.from != nil && g.from == nil:
			t.Errorf("chain[%d] from_status = nil, want %q", i, *want.from)
		case want.from != nil && g.from != nil && *g.from != *want.from:
			t.Errorf("chain[%d] from_status = %q, want %q", i, *g.from, *want.from)
		}
		if Status(g.to) != want.to {
			t.Errorf("chain[%d] to_status = %q, want %q", i, g.to, want.to)
		}
		if i > 0 && g.changedAt.Before(got[i-1].changedAt) {
			t.Errorf("chain[%d] changed_at %v is before chain[%d] changed_at %v (not monotonic)", i, g.changedAt, i-1, got[i-1].changedAt)
		}
	}

	if n := auditCount(t, app, tenantID, "invoice.transitioned"); n != 4 {
		t.Errorf("invoice.transitioned audit rows = %d, want 4 (one per hop, excluding the 1 invoice.created row)", n)
	}
}
