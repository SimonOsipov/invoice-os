// ===========================================================================
// QA MODE-A SCAFFOLD -- internal/invoice/apply_validation_qa_scaffold.go
// ===========================================================================
// task-112 / M4-04-05 ("03: the gate in the store -- transitionTx extraction
// + ApplyValidation") is Test-first: yes. This subtask's real
// Store.ApplyValidation (and the transitionTx extraction it depends on) do
// NOT exist yet -- QA (RALPH Stage 2.5, Mode A) authors GATE-01..18
// (apply_validation_test.go) RED, BEFORE implementation. A bare reference to
// an undeclared Store.ApplyValidation/ErrNotDraft/ErrStaleValidation would be
// a COMPILE error, not a valid red (the story's own precedent-setting rule,
// applied twice already this story: internal/invoice/validator_qa_scaffold.go
// [M4-04-04] and internal/validation/{batch,store}_qa_scaffold.go
// [M4-04-03] -- a compile error tells you nothing about which assertion is
// wrong). This file exists ONLY to give apply_validation_test.go something to
// compile and call, so every GATE-* test fails on a REAL assertion instead.
//
// THE EXECUTOR MUST DELETE THIS ENTIRE FILE and write the real
// Store.ApplyValidation (plus the transitionTx extraction) per task-112's
// plan + Stage-1 architecture validation. Do NOT "fix" this file in place --
// replace it. store.go/invoice.go are UNTOUCHED by this scaffold on purpose
// (Mode A does not implement production code); ErrNotDraft/ErrStaleValidation
// are declared HERE rather than in invoice.go for the same reason
// validator_qa_scaffold.go declared ErrUpstream/ErrNoActiveRuleSet itself
// rather than editing invoice.go -- the executor moves them into invoice.go's
// existing sentinel block (invoice.go:158-180) alongside
// ErrValidation/ErrNotFound/.../ErrUpstream/ErrNoActiveRuleSet, matching that
// block's style.
//
// Unlike the two earlier scaffolds in this story (which implemented mostly-
// correct plumbing wrong on ONE narrow axis), this scaffold implements NO
// real behaviour at all: ApplyValidation unconditionally returns a fixed
// not-implemented sentinel, touching neither the invoices row nor
// invoice_status_history nor audit_log. This mirrors the ORIGINAL
// M4-02/M4-02-02 precedent in this exact file (store.go's Transition/Create/
// Get/List/Update all started as `return Invoice{}, errNotImplemented` --
// see git history c013063^, b96e3c0) and is deliberate given the surface
// area: 18 GATE specs cover many independent mechanisms (promotion,
// collect-all, TOCTOU staleness, same-tx atomicity, RLS, malformed-id, FK,
// concurrency, the violations-write null bug). A single-axis wrong
// implementation cannot make all 18 fail for a real reason without
// reproducing most of the real design first -- which is the executor's job,
// not QA Mode A's ("You do NOT write ApplyValidation or the transitionTx
// extraction"). A fixed not-implemented error is explicitly sanctioned as a
// valid red by this stage's own brief ("prove each FAILS for the right
// reason -- an assertion OR NOT-IMPLEMENTED FAILURE, not a compile error,
// not a setup error"): every GATE test below either (a) expects success and
// gets errApplyValidationNotImplemented, a genuine `err != nil` assertion
// failure, or (b) expects a SPECIFIC sentinel/SQLSTATE (ErrNotDraft,
// ErrStaleValidation, 23514, 23503, ErrNotFound, ErrValidation) and gets a
// different error, a genuine errors.Is/pgCode mismatch -- never a compile or
// setup error. Where a GATE spec also has a "nothing written" clause, that
// clause is deliberately paired in the test with a positive assertion
// (success, or errors.Is/pgCode) so the test cannot pass vacuously merely
// because the scaffold's no-op leaves the row untouched.
package invoice

import (
	"context"
	"errors"
)

// ErrNotDraft / ErrStaleValidation -- SCAFFOLD VERSION. Declared here only so
// GATE-09/10 (ErrNotDraft) and GATE-11 (ErrStaleValidation) compile;
// ApplyValidation below never actually returns either of them, so every spec
// that expects one fails on a real errors.Is mismatch, not a missing symbol.
// The real implementation declares both in invoice.go's existing sentinel
// block per task-112 §c.
var (
	ErrNotDraft        = errors.New("invoice: not draft")
	ErrStaleValidation = errors.New("invoice: stale validation")
)

// errApplyValidationNotImplemented is the RED-stage stub body ApplyValidation
// returns below -- the exact idiom store.go's own Transition/Create/Get/
// List/Update stubs used before M4-02/M4-02-02 implemented them for real
// (git history: c013063^, b96e3c0). A separate name (not store.go's own
// errNotImplemented, which no longer exists now that every method there is
// implemented) since this scaffold is a standalone file the executor deletes
// whole, never merged with store.go.
var errApplyValidationNotImplemented = errors.New("invoice: ApplyValidation not implemented (QA Mode-A scaffold)")

// ApplyValidation -- SCAFFOLD VERSION. Deliberately implements NONE of
// task-112 §b's design: no SELECT ... FOR UPDATE, no status re-check, no
// content-fingerprint re-check, no violations/rule_set_version_id write, no
// transitionTx call, no audit.Record. It touches no table at all -- every
// GATE-* test's assertions about the invoices/invoice_status_history/
// audit_log rows are exercised against WHATEVER STATE THE TEST'S OWN SETUP
// LEFT (via the real, already-implemented Store.Create/Update/Transition),
// never against anything this stub wrote. See the file header for why a
// blanket not-implemented error is the right scope for QA Mode A here.
func (s *Store) ApplyValidation(ctx context.Context, id string, vs []Violation, ruleSetVersionID, evaluatedFingerprint string) (Invoice, error) {
	return Invoice{}, errApplyValidationNotImplemented
}
