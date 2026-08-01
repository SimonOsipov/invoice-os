// seed_evidence_honesty_test.go: QA gap-fill (task-323, Stage 4 re-verify). Two fabricated
// evidence trails escaped db/seed.dev.sql's outcome-coverage block undetected -- -0004 seeded
// as "accepted" (an outcome the trigger can never reach) and -0003's two-attempt trail (the
// real adapter needs three) -- and both times the only thing that caught it was a directed
// skeptical read, not an automated check. This closes that gap.
//
// Package `submission` (whitebox), not `submission_test`: mockPendingPolls and the reserved
// TIN constants are unexported, and this file's entire point is to read them as ground truth
// rather than restate their implied arithmetic as a second, independently-typed literal. A
// black-box test could not see mockPendingPolls at all.
//
// Every expected value here comes from actually DRIVING a real, zero-config MockAdapter
// against each reserved TIN (sehDeriveTrail) -- never a hand-computed formula. If
// mockPendingPolls, MaxAttempts or a TIN's trigger allocation ever changes, this file's
// expectations move with it automatically; nothing here needs updating in lockstep by hand.
//
// Gated on DATABASE_SUPERUSER_URL only, same as internal/platform/db's seed suite: db.Seed
// runs as the superuser (tenants/business_entities are FORCE RLS).
package submission

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	dbsql "github.com/SimonOsipov/invoice-os/db"
	db "github.com/SimonOsipov/invoice-os/internal/platform/db"
)

// sehRequireSuperuserDSN mirrors internal/platform/db's own requireSuperuserDSN. Duplicated,
// not imported: the two suites gate independently and internal/platform/db does not export it.
func sehRequireSuperuserDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_SUPERUSER_URL")
	if dsn == "" {
		t.Skip("seed-evidence-honesty suite skipped: set DATABASE_SUPERUSER_URL (db.Seed runs as the superuser)")
	}
	return dsn
}

// sehCanonical builds a minimal but complete Canonical whose buyer TIN is the trigger channel
// ([trigger-read-from-the-real-bis-field]), mirroring mock_adapter_test.go's maCanonical. That
// helper lives in package submission_test and cannot see this file's unexported ground truth,
// so the handful of lines are duplicated rather than shared across the package boundary.
func sehCanonical(buyerTIN string) Canonical {
	issue := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	strPtr := func(s string) *string { return &s }
	return Canonical{
		InvoiceID:     "inv-seh-probe",
		InvoiceNumber: "SEH-PROBE-0001",
		IssueDate:     &issue,
		Supplier:      Party{TIN: strPtr("11111111-2222"), Name: strPtr("Supplier Co")},
		Buyer:         Party{TIN: strPtr(buyerTIN), Name: strPtr("Buyer Ltd")},
		Currency:      strPtr("NGN"),
		Subtotal:      strPtr("1000.00"),
		VAT:           strPtr("75.00"),
		Total:         strPtr("1075.00"),
		Lines: []CanonicalLine{{
			LineID: "line-1", LineNo: 1, Description: strPtr("Widget"),
			Quantity: strPtr("2"), UnitPrice: strPtr("500.00"), LineTotal: strPtr("1000.00"), LineTax: strPtr("75.00"),
		}},
	}
}

// sehStep is one attempt this file DERIVED by actually running the adapter -- never a
// restatement of db/seed.dev.sql's own literals.
type sehStep struct {
	operation  Operation
	httpStatus *int
}

// sehDeriveTrail drives a real MockAdapter against buyerTIN the same way SubmitWorker /
// PollWorker do: one Submit; a Pending result is followed by Poll (never a re-Submit); a
// Retryable result is followed by a fresh Submit (River's own retry re-invokes Submit, never
// Poll, for a submit-phase failure -- worker.go's SubmitWorker.Work). The loop is capped at
// maxAttempts, read from SubmitArgs{}.InsertOpts().MaxAttempts rather than a bare literal, so
// a trigger that never converges (-0004, -0006) still terminates instead of looping forever.
func sehDeriveTrail(t *testing.T, buyerTIN string) []sehStep {
	t.Helper()
	a := NewMockAdapter(MockConfig{})
	ctx := context.Background()

	w, err := a.Transform(ctx, sehCanonical(buyerTIN))
	if err != nil {
		t.Fatalf("Transform(buyerTIN=%q): %v", buyerTIN, err)
	}

	maxAttempts := SubmitArgs{}.InsertOpts().MaxAttempts

	var steps []sehStep
	result, ev := a.Submit(ctx, w, "seh-probe")
	steps = append(steps, sehStep{operation: OpSubmit, httpStatus: ev.HTTPStatus})

	for len(steps) < maxAttempts {
		switch r := result.(type) {
		case Pending:
			result, ev = a.Poll(ctx, r.Ref)
			steps = append(steps, sehStep{operation: OpPoll, httpStatus: ev.HTTPStatus})
		case Retryable:
			result, ev = a.Submit(ctx, w, "seh-probe")
			steps = append(steps, sehStep{operation: OpSubmit, httpStatus: ev.HTTPStatus})
		default: // Accepted or Rejected: converged, nothing further to drive.
			return steps
		}
	}
	// Budget exhausted without converging -- the -0004/-0006 shape (dead-letter on the worker
	// side). Returning here rather than looping again matches job.Attempt >= MaxAttempts.
	return steps
}

// reservedTINsUsedBySeed is db/seed.dev.sql's own outcome-coverage subset (Core AC-5) -- the
// five reserved TINs it actually seeds an invoice against. -0005/-0007 are allocated in
// mockAllocations but unused by the seed, so they are not asserted here.
var reservedTINsUsedBySeed = []string{
	mockTINAccept,
	mockTINReject,
	mockTINPending,
	mockTINUnavailable,
	mockTINTimeout,
}

// TestSeedEvidenceTrailMatchesDerivedAdapterBehavior: for each reserved-TIN invoice the seed
// writes, asserts submission_jobs.attempts and the app_exchange operation/http_status
// progression against a trail DERIVED by driving the real adapter -- not against a
// second-guessed formula, and not against db/seed.dev.sql's own numbers read back at itself.
func TestSeedEvidenceTrailMatchesDerivedAdapterBehavior(t *testing.T) {
	superDSN := sehRequireSuperuserDSN(t)
	pool, err := pgxpool.New(context.Background(), superDSN)
	if err != nil {
		t.Fatalf("open superuser pool: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()

	if err := db.Seed(ctx, superDSN, dbsql.FS); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	for _, tin := range reservedTINsUsedBySeed {
		tin := tin
		t.Run(tin, func(t *testing.T) {
			want := sehDeriveTrail(t, tin)

			var invoiceID string
			if err := pool.QueryRow(ctx,
				`SELECT id FROM invoices WHERE buyer_tin = $1 AND invoice_number LIKE 'DEMO-2026-%'`,
				tin,
			).Scan(&invoiceID); err != nil {
				t.Fatalf("no seeded DEMO-2026-* invoice carries buyer_tin=%q: %v", tin, err)
			}

			var jobAttempts int
			if err := pool.QueryRow(ctx,
				`SELECT attempts FROM submission_jobs WHERE invoice_id = $1`, invoiceID,
			).Scan(&jobAttempts); err != nil {
				t.Fatalf("read submission_jobs.attempts for buyer_tin=%q: %v", tin, err)
			}
			if jobAttempts != len(want) {
				t.Errorf("buyer_tin=%q: submission_jobs.attempts = %d, want %d -- derived by driving the real adapter, not read off the seed", tin, jobAttempts, len(want))
			}

			rows, err := pool.Query(ctx,
				`SELECT operation, http_status FROM app_exchange WHERE invoice_id = $1 ORDER BY attempt ASC`,
				invoiceID,
			)
			if err != nil {
				t.Fatalf("query app_exchange for buyer_tin=%q: %v", tin, err)
			}
			defer rows.Close()

			var got []sehStep
			for rows.Next() {
				var op string
				var status *int
				if err := rows.Scan(&op, &status); err != nil {
					t.Fatalf("scan app_exchange row: %v", err)
				}
				got = append(got, sehStep{operation: Operation(op), httpStatus: status})
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate app_exchange rows: %v", err)
			}

			if len(got) != len(want) {
				t.Fatalf("buyer_tin=%q: %d app_exchange rows, want %d (derived)", tin, len(got), len(want))
			}
			for i := range want {
				if got[i].operation != want[i].operation {
					t.Errorf("buyer_tin=%q attempt %d: operation = %q, want %q (derived)", tin, i+1, got[i].operation, want[i].operation)
				}
				gotStatus, wantStatus := "NULL", "NULL"
				if got[i].httpStatus != nil {
					gotStatus = fmt.Sprintf("%d", *got[i].httpStatus)
				}
				if want[i].httpStatus != nil {
					wantStatus = fmt.Sprintf("%d", *want[i].httpStatus)
				}
				if gotStatus != wantStatus {
					t.Errorf("buyer_tin=%q attempt %d: http_status = %s, want %s (derived)", tin, i+1, gotStatus, wantStatus)
				}
			}
		})
	}
}
