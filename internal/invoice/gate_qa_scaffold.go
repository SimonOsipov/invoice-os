// ===========================================================================
// QA MODE-A SCAFFOLD -- internal/invoice/gate_qa_scaffold.go
// ===========================================================================
// task-113 / M4-04-06 ("03: gate orchestration, POST /v1/invoices/{id}/
// validate, and the transitions guard") is Test-first: yes. This subtask's
// real internal/invoice/gate.go (Gate/NewGate/Evaluate/Validate/
// ValidateBatch) and handlers.go's ValidateHandler do NOT exist yet -- QA
// (RALPH Stage 2.5, Mode A) authors GAPI-01..17 (handlers_test.go's new
// Validate-handler/guard tests + gate_test.go) RED, BEFORE implementation.
// A bare reference to an undeclared Gate/EvalItem/EvalResult/NewGate/
// ValidateHandler would be a COMPILE error, not a valid red -- the story's
// own precedent-setting rule, applied three times already this story:
// internal/invoice/validator_qa_scaffold.go [M4-04-04],
// internal/validation/{batch,store}_qa_scaffold.go [M4-04-03], and
// internal/invoice/apply_validation_qa_scaffold.go [M4-04-05]. This file
// exists ONLY to give the new test files something to compile and call, so
// every GAPI-* test that names one of these new symbols fails on a REAL
// assertion (a wrong status code, an errors.Is mismatch, an unexpected call
// count) instead of an undefined symbol.
//
// THE EXECUTOR MUST DELETE THIS ENTIRE FILE and write the real, correct
// internal/invoice/gate.go + handlers.go's ValidateHandler per task-113's
// plan + Stage-1 addendum. Do NOT "fix" this file in place -- replace it.
//
// Mirrors apply_validation_qa_scaffold.go's chosen shape (a BLANKET
// not-implemented sentinel, not a "mostly correct plumbing wrong on one
// narrow axis" scaffold like validator_qa_scaffold.go): GAPI-01..14 span
// many independent mechanisms (HTTP status mapping across 6+ error
// sentinels, single-round-trip batching, an advisory pre-check that must
// skip a network call, and a full real-DB clean/blocked/re-validate flow)
// and a single-axis-wrong implementation cannot make all of them fail for a
// real reason without reproducing most of the real design first -- which is
// the executor's job, not QA Mode A's ("You do NOT write the Gate or the
// handler"). Every GAPI-* test against Gate/ValidateHandler below either (a)
// expects success and gets errGateNotImplemented / a 501, a genuine
// err!=nil or status-code assertion failure, or (b) expects a SPECIFIC
// sentinel (ErrNotDraft, etc.) or call count and gets a different one, a
// genuine errors.Is/count mismatch -- never a compile or setup error.
//
// GAPI-15/16/17 (the transitions guard) are NOT covered by this scaffold --
// they run against the REAL, shipped TransitionHandler (handlers.go), which
// this file does not touch. The guard itself (a `target == StatusValidated`
// -> 409 pre-call check) is simply absent from that real handler today, so
// GAPI-15 fails for a real reason (200+called instead of 409+not-called)
// with no scaffold needed.
//
// EvalItem/EvalResult's exact shape is UNSPECIFIED by task-113's plan
// (Stage-1 addendum: "Executor's call; no plan change needed") -- the choice
// below (Ref + a domain Invoice in, RuleSetVersion(ID)+ByRef out, mirroring
// Validator's own ValidateItem/ValidateResult one level up) is QA's own
// reasonable placeholder so GAPI-10 has something concrete to call. The
// executor is free to reshape it; GAPI-10 tests the OBSERVABLE property
// (the validator receives exactly one request covering every item), which
// survives a reshape.
package invoice

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

// errGateNotImplemented is the RED-stage stub body every Gate method below
// returns -- the same not-implemented-sentinel idiom
// apply_validation_qa_scaffold.go used for Store.ApplyValidation. A
// separate name (not that file's errApplyValidationNotImplemented) since
// this scaffold is a standalone file the executor deletes whole, never
// merged with that one.
var errGateNotImplemented = errors.New("invoice: Gate not implemented (QA Mode-A scaffold)")

// EvalItem is one invoice submitted to Gate.Evaluate -- SCAFFOLD SHAPE, see
// file header. Ref is caller-opaque (the invoice id on the real path, the
// invoice_number on dry-run), Invoice is the domain record Evaluate maps
// through MBSPayload before handing it to the Validator.
type EvalItem struct {
	Ref     string
	Invoice Invoice
}

// EvalResult is Gate.Evaluate's return -- SCAFFOLD SHAPE, mirrors
// Validator.ValidateResult one level up (RuleSetVersion/RuleSetVersionID
// stamped once per batch, ByRef keyed by the sent refs).
type EvalResult struct {
	RuleSetVersion   int
	RuleSetVersionID string
	ByRef            map[string][]Violation
}

// Gate -- SCAFFOLD VERSION. task-113 §a: "Gate{store, validator}, mirroring
// importer.Service's shape (a struct over the stores/clients it drives)".
// Concrete types (not interfaces), matching internal/importer/service.go's
// Service{batch *Store, inv *invoice.Store} precedent.
type Gate struct {
	store     *Store
	validator *Validator
}

// NewGate -- SCAFFOLD VERSION. Wires the two concrete dependencies; the
// real gate.go's own doc should explain ownership of their pool/client
// lifecycles, mirroring NewService's.
func NewGate(store *Store, v *Validator) *Gate {
	return &Gate{store: store, validator: v}
}

// Evaluate -- SCAFFOLD VERSION. Deliberately implements NONE of task-113
// §a's design: no MBSPayload mapping, no Validator.Validate call, no writes
// (it should have none regardless -- dry-run's whole point). Always returns
// errGateNotImplemented, so GAPI-10's "the validator is called exactly
// once" assertion fails for a real reason (0 calls observed, not 1) rather
// than a compile error.
func (g *Gate) Evaluate(ctx context.Context, items []EvalItem) (EvalResult, error) {
	return EvalResult{}, errGateNotImplemented
}

// Validate -- SCAFFOLD VERSION. Deliberately implements NONE of task-113
// §a's design: no Store.Get, no draft pre-check, no Evaluate call, no
// Store.ApplyValidation call. Always returns errGateNotImplemented, so
// every GAPI-02/03/04/05/06/07/08/11/12/13/14 assertion (a specific
// success shape or a specific sentinel) fails on a real errors.Is/value
// mismatch, never a compile error.
func (g *Gate) Validate(ctx context.Context, id string) (Invoice, error) {
	return Invoice{}, errGateNotImplemented
}

// ValidateHandler -- SCAFFOLD VERSION. task-113 §b: POST
// /v1/invoices/{id}/validate, the existing factory idiom (identity-first-
// 401, decode/validate, call the injected closure, statusForErr, JSON).
// Deliberately implements NONE of it: no identity check, no path-id read,
// no call to validate, no statusForErr mapping. Always answers 501 with a
// real JSON {"error":"..."} envelope (via the shipped writeError, so
// GAPI-*'s "expected a non-empty error message" checks pass even against
// the stub, isolating each RED failure to the status-code assertion it
// actually targets), so every GAPI-01..09 test fails on the status-code
// mismatch it names, never a compile or setup error.
func ValidateHandler(validate func(ctx context.Context, id string) (Invoice, error), log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotImplemented, "ValidateHandler not implemented (QA Mode-A scaffold)")
	}
}
