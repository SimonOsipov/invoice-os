// This file is a QA Mode-A compile scaffold for task-109 / M4-04-03 ("04:
// batch validate endpoint, s2s peer auth, tenant-free rule-set load") -- NOT
// the real BatchValidateHandler/S2SMiddleware, and NOT meant to be reused,
// patched, or extended. It exists for exactly one reason: VB-01..02,
// VB-07..13, VB-17 reference these symbols, and Go cannot compile a test
// file whose symbols don't exist. The RALPH orchestrator's Mode-A brief for
// this subtask is explicit -- "You do NOT write the endpoint. The executor
// drives your reds to green." -- so this scaffold is deliberately NOT added
// to handlers.go/a new s2s.go, and is deliberately NOT a good-faith attempt
// at the real thing.
//
// Every piece below is wired the NAIVE, WRONG way the story's Description/
// Decisions section explicitly warns against, so the VB-* specs fail on a
// real assertion (not a Go compile error):
//
//   - S2SMiddleware drains the request body BEFORE checking the token
//     ([s2s-peer-auth] requires "401 before any body read") -- VB-03.
//   - S2SMiddleware treats a non-empty X-Tenant-ID as an alternate grant,
//     bypassing the token check entirely -- this is [s2s-identity]'s
//     explicitly REJECTED alternative (b), "trust X-Tenant-ID from a known
//     peer" -- VB-05.
//   - BatchValidateHandler has NO http.MaxBytesReader at all -- VB-09.
//   - BatchValidateHandler has NO empty-batch / 5,000-item cap check at all
//     -- VB-07, VB-08.
//   - the injected loader is called ONCE PER ITEM, not once for the whole
//     batch -- the exact anti-pattern (d)'s "the entire point of the
//     endpoint" warns against -- VB-02.
//   - every loader/engine error maps to a flat 500, never checking for
//     ErrNoActiveRuleSet -- VB-10.
//   - each item's payload is passed to Engine.Evaluate UNWRAPPED (never
//     re-rooted as Payload{"invoice": it.Invoice}) -- [batch-payload-
//     rooting]'s warned silent-failure seam -- VB-12 (the discriminating
//     test: a mis-rooted payload resolves every target as absent, so a
//     `required` rule fires on data that should be fully valid). VB-13
//     (an empty invoice fires every required rule) CANNOT catch this --
//     a mis-rooted EMPTY invoice still resolves every path absent, so it
//     also fires every required rule; see that test's doc comment.
//
// The executor deletes this ENTIRE file when authoring the real
// internal/validation/s2s.go and the BatchValidateHandler addition to
// internal/validation/handlers.go (task-109 / M4-04-03).
package validation

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

// batchItem -- QA Mode-A scaffold wire shape: {"ref":"<opaque>","invoice":{...}}.
type batchItem struct {
	Ref     string         `json:"ref"`
	Invoice map[string]any `json:"invoice"`
}

// batchRequest -- QA Mode-A scaffold wire shape: {"invoices":[...]}.
type batchRequest struct {
	Invoices []batchItem `json:"invoices"`
}

// batchItemResult -- QA Mode-A scaffold wire shape: one item's echoed ref +
// its collected violations.
type batchItemResult struct {
	Ref        string      `json:"ref"`
	Violations []Violation `json:"violations"`
}

// batchResponseWire -- QA Mode-A scaffold wire shape: AC#1's
// {rule_set_version, rule_set_version_id, results:[...]}.
type batchResponseWire struct {
	RuleSetVersion   int               `json:"rule_set_version"`
	RuleSetVersionID string            `json:"rule_set_version_id"`
	Results          []batchItemResult `json:"results"`
}

// S2SMiddleware -- QA Mode-A scaffold. See file header: deliberately wrong
// on two axes (reads the body before checking the token; trusts
// X-Tenant-ID as an alternate grant).
func S2SMiddleware(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// WRONG (deliberate): drains the body BEFORE checking the token,
			// so an unauthenticated caller's body IS read -- VB-03 must
			// catch this ([s2s-peer-auth]'s "401 before any body read").
			_, _ = io.Copy(io.Discard, r.Body)

			// WRONG (deliberate): a caller carrying X-Tenant-ID is let
			// through with NO token check at all -- [s2s-identity]'s
			// explicitly REJECTED alternative (b) -- VB-05 must catch it.
			if r.Header.Get("X-Tenant-ID") != "" {
				next.ServeHTTP(w, r)
				return
			}

			supplied := r.Header.Get("X-S2S-Token")
			if subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BatchValidateHandler -- QA Mode-A scaffold. See file header: deliberately
// wrong on five axes (no MaxBytesReader, no empty/cap checks, loads once
// PER ITEM, flat 500 on every error, unwrapped/mis-rooted payload).
func BatchValidateHandler(loadRuleSet func(ctx context.Context) (RuleSet, error), eng *Engine, log *slog.Logger) http.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// WRONG (deliberate): no http.MaxBytesReader at all -- VB-09 (413)
		// must catch this.
		var req batchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		// WRONG (deliberate): no empty-batch / 5,000-item cap check at all
		// -- VB-07 and VB-08 must catch this.

		results := make([]batchItemResult, 0, len(req.Invoices))
		var rs RuleSet
		for _, it := range req.Invoices {
			// WRONG (deliberate): the loader is called ONCE PER ITEM, not
			// once for the whole batch -- VB-02's counting fake must catch
			// this.
			loaded, err := loadRuleSet(r.Context())
			if err != nil {
				// WRONG (deliberate): every loader/engine error maps to a
				// flat 500 -- VB-10 (503 for ErrNoActiveRuleSet) must catch
				// this.
				log.ErrorContext(r.Context(), "validation: batch validate", slog.Any("err", err))
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			rs = loaded

			// WRONG (deliberate, [batch-payload-rooting]): it.Invoice is
			// passed to Evaluate UNWRAPPED, never re-rooted as
			// Payload{"invoice": it.Invoice} -- VB-12 must catch this.
			result, err := eng.Evaluate(it.Invoice, rs)
			if err != nil {
				log.ErrorContext(r.Context(), "validation: batch validate", slog.Any("err", err))
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			results = append(results, batchItemResult{Ref: it.Ref, Violations: result.Violations})
		}

		writeJSON(w, http.StatusOK, batchResponseWire{
			RuleSetVersion:   rs.Version,
			RuleSetVersionID: rs.ID,
			Results:          results,
		})
	}
}
