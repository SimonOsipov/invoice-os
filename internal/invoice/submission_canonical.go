// This file is 03 (the Invoice context)'s own knowledge of how a
// Store.Get-hydrated invoice projects onto 05 (internal/submission)'s
// Canonical -- the type M6's real adapter is written against
// ([canonical-is-05-owned]). 03 imports 05; 05 imports NOTHING from 03
// ([mapper-lives-in-03]) -- reversed, M5-04's job-args import from 03 into
// 05 would close a cycle.
//
// Everything here is PURE: no DB, no HTTP, no clock -- mirrors payload.go's
// MBSPayload discipline exactly.
//
// inv MUST be Store.Get-hydrated. Store.Get orders LineItems by line_no
// (store.go:210-232); Store.List returns headers with LineItems left nil
// (store.go:331) -- a List-sourced invoice silently maps to zero lines, the
// same hazard MBSPayload's header already documents, and this mapper is
// equally unable to detect it (it is pure and has no way to tell "empty
// because List" from "empty because zero line items").
package invoice

import "github.com/SimonOsipov/invoice-os/internal/submission"

// SubmissionCanonical projects inv onto 05's Canonical
// ([canonical-is-invoice-content]). Carries invoice CONTENT only: no tenant
// id, no status, no violations -- Canonical has no such fields, so this is
// enforced at compile time, not by this function. Nil pointers pass through
// as nil, never coerced to "".
//
// STUB (Stage 2.5, M5-02-02 Mode A / QA red): deliberately returns the zero
// value so the package compiles and the red-first specs fail on real
// assertions instead of a build error. The executor replaces this body.
func SubmissionCanonical(inv Invoice) submission.Canonical {
	return submission.Canonical{}
}
