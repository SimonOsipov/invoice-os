package invoice

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// var _ submission.InvoicePort = (*Store)(nil) proves *Store satisfies
// submission.InvoicePort at compile time (T03-1, AC#1).
var _ submission.InvoicePort = (*Store)(nil)

// Canonical hydrates invoiceID inside tx via getTx (store.go) and projects
// it onto submission.Canonical via the existing pure SubmissionCanonical
// mapper ([canonical-is-05-owned]) -- never reimplemented here. getTx must
// be used (not a lighter read) because SubmissionCanonical requires a
// hydrated invoice (non-nil LineItems); a List-sourced invoice would
// silently map to zero lines (submission_canonical.go's header hazard).
func (s *Store) Canonical(ctx context.Context, tx pgx.Tx, invoiceID string) (submission.Canonical, error) {
	inv, err := getTx(ctx, tx, invoiceID)
	if err != nil {
		return submission.Canonical{}, err
	}
	return SubmissionCanonical(inv), nil
}

// HasFiscalOutcome reports invoices.irn IS NOT NULL for invoiceID -- irn
// only, never csid (no CHECK correlates the two columns). Cross-tenant /
// absent id: RLS 0-rows -> pgx.ErrNoRows -> (false, nil), not an error.
func (s *Store) HasFiscalOutcome(ctx context.Context, tx pgx.Tx, invoiceID string) (bool, error) {
	var has bool
	err := tx.QueryRow(ctx,
		`SELECT irn IS NOT NULL FROM invoices WHERE id = $1`, invoiceID,
	).Scan(&has)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return has, err
}

// MarkSubmitted is a thin 1:1 forward onto 02's already-tested
// MarkSubmittedTx (actor.go) -- NOT a reimplementation of markTerminalTx's
// lock/idempotency/transition sequence. A private markTerminal helper here
// would be a second independent implementation of that sequence, exactly
// the drift risk 02's transition_adversarial_test.go warns about for
// oracle maps.
func (s *Store) MarkSubmitted(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string) error {
	_, err := s.MarkSubmittedTx(ctx, tx, invoiceID, tenantID)
	return err
}

// MarkFailed is MarkSubmitted's sibling, a thin forward onto MarkFailedTx
// (actor.go).
func (s *Store) MarkFailed(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string) error {
	_, err := s.MarkFailedTx(ctx, tx, invoiceID, tenantID)
	return err
}

// MarkAccepted is a thin 1:1 forward onto MarkAcceptedTx (actor.go) -- same
// "no reimplementation" rule as MarkSubmitted/MarkFailed above. Stage 2.5
// SCAFFOLDING for M5-05-03 (task-239): MarkAcceptedTx is currently a stub
// (errOutcomeNotImplemented), so this forward returns that error
// unconditionally until Stage 3 replaces the stub body.
func (s *Store) MarkAccepted(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string, out submission.Accepted) error {
	_, err := s.MarkAcceptedTx(ctx, tx, invoiceID, tenantID, out.IRN, out.CSID, out.QRPayload)
	return err
}

// MarkRejected is MarkAccepted's sibling, a thin forward onto MarkRejectedTx
// (actor.go). Same Stage 2.5 scaffolding note applies.
func (s *Store) MarkRejected(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string, verdict submission.Rejected) error {
	_, err := s.MarkRejectedTx(ctx, tx, invoiceID, tenantID, verdict.Reasons)
	return err
}
