package invoice

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/SimonOsipov/invoice-os/internal/submission"
)

// errPortNotImplemented is the RED-stage stub body Canonical/HasFiscalOutcome/
// MarkSubmitted/MarkFailed/getTx return below (M5-04-03, Mode A / RALPH Stage
// 2.5): the real bodies (getTx extracted out of Store.Get, Canonical wrapping
// getTx+SubmissionCanonical, HasFiscalOutcome's `irn IS NOT NULL` read,
// MarkSubmitted/MarkFailed as thin forwards onto 02's MarkSubmittedTx/
// MarkFailedTx) land in this subtask's Mode B (Executor, Stage 3) pass. It
// returns (rather than panics) so submission_port_test.go's T03-2..T03-5
// specs reach their assertions and fail for the right reason (this
// sentinel), not a compile error. Mirrors the errActorNotImplemented
// precedent from M5-04-02's own RED commit (e13e753, actor.go).
var errPortNotImplemented = errors.New("invoice: submission port not implemented")

// var _ submission.InvoicePort = (*Store)(nil) proves *Store satisfies
// submission.InvoicePort at compile time (T03-1, AC#1) — true the moment
// this file and internal/submission/invoice_port.go both exist, independent
// of whether the method bodies below are real or stubbed.
var _ submission.InvoicePort = (*Store)(nil)

// Canonical will hydrate invoiceID inside tx via getTx and project it onto
// submission.Canonical via SubmissionCanonical (Stage 3,
// [canonical-is-05-owned]). Stubbed here only so it compiles; body is
// Stage 3's.
func (s *Store) Canonical(ctx context.Context, tx pgx.Tx, invoiceID string) (submission.Canonical, error) {
	return submission.Canonical{}, errPortNotImplemented
}

// HasFiscalOutcome will report invoices.irn IS NOT NULL for invoiceID
// (Stage 3). Stubbed here only so it compiles; body is Stage 3's.
func (s *Store) HasFiscalOutcome(ctx context.Context, tx pgx.Tx, invoiceID string) (bool, error) {
	return false, errPortNotImplemented
}

// MarkSubmitted will be a thin 1:1 forward onto MarkSubmittedTx (Stage 3) --
// never a reimplementation of markTerminalTx's lock/idempotency/transition
// sequence. Stubbed here only so it compiles; body is Stage 3's.
func (s *Store) MarkSubmitted(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string) error {
	return errPortNotImplemented
}

// MarkFailed is MarkSubmitted's sibling, a thin forward onto MarkFailedTx
// (Stage 3). Stubbed here only so it compiles; body is Stage 3's.
func (s *Store) MarkFailed(ctx context.Context, tx pgx.Tx, invoiceID, tenantID string) error {
	return errPortNotImplemented
}

// getTx will be Store.Get's tx-scoped body, extracted verbatim out of
// store.go:216-275 with byte-identical observable behaviour preserved
// (Stage 3, T03-2's regression proof). Stubbed here only so it compiles;
// Store.Get itself is UNTOUCHED by this subtask — getTx is a new,
// independent, not-yet-wired function.
func getTx(ctx context.Context, tx pgx.Tx, id string) (Invoice, error) {
	return Invoice{}, errPortNotImplemented
}
