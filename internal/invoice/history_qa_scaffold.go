// ===========================================================================
// QA MODE-A SCAFFOLD -- internal/invoice/history_qa_scaffold.go
// ===========================================================================
// task-160 / M4-22-01 ("status-history read endpoint") is Test-first: yes.
// The real StatusChange wire type, Store.History and handlers.go's
// HistoryHandler do NOT exist yet -- QA (RALPH Stage 2.5, Mode A) authors
// the corrected Test Specs table RED, BEFORE implementation. A bare
// reference to an undeclared StatusChange/Store.History/HistoryHandler
// would be a COMPILE error, not a valid red -- the story's own
// precedent-setting rule, applied repeatedly already in this package:
// gate_qa_scaffold.go [M4-04-06], apply_validation_qa_scaffold.go
// [M4-04-05], validator_qa_scaffold.go [M4-04-04]. This file exists ONLY to
// give the new test files something to compile and call, so every new test
// fails on a REAL assertion (a wrong status code, an errors.Is mismatch, a
// wrong slice length/order) instead of an undefined symbol.
//
// THE EXECUTOR MUST DELETE THIS ENTIRE FILE and write the real
// StatusChange (invoice.go), Store.History (store.go), and handlers.go's
// HistoryHandler per task-160's plan + Stage-1 addendum -- in particular
// the two CRITICAL corrections: (GAP 1) a malformed (non-uuid) id must map
// 22P02 -> ErrValidation -> 400, exactly like Get/Update/Transition, NOT
// ErrNotFound/404; (GAP 2) Store.History is a multi-row tx.Query, which
// never yields pgx.ErrNoRows on an RLS-filtered zero-row result -- the real
// implementation needs an explicit `if len(result) == 0 { return nil,
// ErrNotFound }` check AFTER the query loop, so an unknown or cross-tenant
// id 404s instead of silently 200-ing `[]`. Do NOT "fix" this file in
// place -- replace it.
//
// Blanket not-implemented stubs (mirrors apply_validation_qa_scaffold.go's
// / gate_qa_scaffold.go's shape, not validator_qa_scaffold.go's
// single-axis-wrong shape): the real query, its RLS/tenant resolution, and
// the zero-rows->ErrNotFound post-check are independent mechanisms QA Mode
// A does not attempt to partially reproduce.
package invoice

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// errHistoryNotImplemented is the RED-stage stub body Store.History
// returns -- same not-implemented-sentinel idiom as
// errGateNotImplemented/errApplyValidationNotImplemented. A distinct name
// since this scaffold is a standalone file the executor deletes whole,
// never merged with those.
var errHistoryNotImplemented = errors.New("invoice: Store.History not implemented (QA Mode-A scaffold)")

// StatusChange -- SCAFFOLD SHAPE, per task-160's story body + Stage-1
// addendum Minor-2 (ToStatus/FromStatus typed Status, not bare string, for
// consistency with Invoice.Status -- the wire JSON is unaffected, Status
// marshals as a plain string). id/tenant_id/invoice_id are deliberately
// absent (AC #7).
type StatusChange struct {
	FromStatus *Status   `json:"from_status"`
	ToStatus   Status    `json:"to_status"`
	Actor      string    `json:"actor"`
	ChangedAt  time.Time `json:"changed_at"`
}

// History -- SCAFFOLD VERSION. Deliberately implements NONE of task-160's
// design: no db.WithinRequestTenantTx, no query, no zero-rows->ErrNotFound
// post-check (GAP 2), no 22P02->ErrValidation mapping (GAP 1). Always
// returns errHistoryNotImplemented, so every TestRLS_InvoiceHistory_* test
// and the malformed_id_test.go "History" subtest fail on a real
// errors.Is/length mismatch, never a compile error.
func (s *Store) History(ctx context.Context, id string) ([]StatusChange, error) {
	return nil, errHistoryNotImplemented
}

// HistoryHandler -- SCAFFOLD VERSION. task-160: GET
// /v1/invoices/{id}/history, the existing factory idiom (identity-first-
// 401, r.PathValue("id"), call the injected closure, statusForErr, bare
// JSON array). Deliberately implements NONE of it: no identity check, no
// path-id read, no call to history, no statusForErr mapping. Always
// answers 501 with a real JSON {"error":"..."} envelope (via the shipped
// writeError, so any TestHistoryHandler_* assertion on a non-empty error
// message still passes even against the stub, isolating each RED failure
// to the status-code/body assertion it actually targets).
func HistoryHandler(history func(ctx context.Context, id string) ([]StatusChange, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "HistoryHandler not implemented (QA Mode-A scaffold)")
	}
}
