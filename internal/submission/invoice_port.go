package submission

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// InvoicePort is 05's seam onto 03's Invoice context ([invoice-port-in-05]).
// Every method takes the CALLER's transaction (tx) — never opens its own —
// so the worker's tx1/tx2 boundaries stay atomic and RLS tenant scoping
// (already set via db.WithinTenantTx's GUC on tx) applies transparently.
// No status vocabulary crosses this seam — that is why Mark* is four
// methods, not one taking a target Status.
//
// invoiceID is always the FIRST positional arg across all six methods,
// matching 02's shipped MarkSubmittedTx/MarkFailedTx(ctx, tx, id, tenantID)
// order exactly [Stage-2 architect validation, 2026-07-23] — deliberately
// NOT tenantID-first (an earlier draft of this interface had it reversed;
// two same-typed string params in the wrong relative order across sibling
// methods is exactly the kind of thing that compiles and passes a shallow
// test). Aligning means 03's adapters below are trivial 1:1 forwards, not
// a manually-verified swap.
type InvoicePort interface {
	// Canonical hydrates invoiceID inside tx and projects it onto Canonical,
	// identical to SubmissionCanonical(Store.Get(invoiceID)). Lines ordered
	// by line_no.
	Canonical(ctx context.Context, tx pgx.Tx, invoiceID string) (Canonical, error)

	// HasFiscalOutcome reports invoices.irn IS NOT NULL for invoiceID.
	// Cross-tenant / absent id -> RLS 0-rows -> false, nil (not an error).
	HasFiscalOutcome(ctx context.Context, tx pgx.Tx, invoiceID string) (bool, error)

	// MarkSubmitted transitions invoiceID -> submitted as SystemActor(tenantID).
	// Idempotent: a redundant call on an already-submitted invoice returns nil.
	MarkSubmitted(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string) error

	// MarkFailed transitions invoiceID -> failed as SystemActor(tenantID).
	// Idempotent: a redundant call on an already-failed invoice returns nil.
	MarkFailed(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string) error

	// MarkAccepted transitions invoiceID -> accepted as SystemActor(tenantID),
	// writing out.IRN/CSID/QRPayload in the SAME tx as the transition.
	// out.CSID/out.QRPayload == "" become SQL NULL (the authority returned
	// none); out.IRN is REQUIRED non-blank (law L07) and a blank value
	// raises SQLSTATE 23514 -- the deliberate NULLIF asymmetry
	// ([blank-irn-is-the-databases-to-refuse]). Idempotent: a redundant
	// call on an already-accepted invoice returns nil and does not rewrite
	// the outcome columns.
	MarkAccepted(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string, out Accepted) error

	// MarkRejected transitions invoiceID -> rejected as SystemActor(tenantID),
	// writing verdict.Reasons as invoices.rejection_reasons in the SAME tx
	// as the transition. A nil/empty verdict.Reasons normalises to the
	// literal jsonb [], never JSON null ([reasons-never-json-null]).
	// Idempotent: a redundant call on an already-rejected invoice returns
	// nil and does not rewrite the outcome column.
	MarkRejected(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string, verdict Rejected) error
}
