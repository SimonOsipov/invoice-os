// The single source of truth for the ACTIVE global MBS rule-set version across the
// whole e2e package ([e2e-active-version]).
//
// Every deployed-dev suite that observes the version resolves through here -- READ THIS
// LIST, never trust a count in this header's prose (see [positional-pins-are-invisible]
// below: this list has already under-counted itself once, when api/perf.spec.ts's own
// comment called itself "a FOURTH consumer" against a header that still said "THREE").
// Direct importers of ACTIVE_RULE_SET_VERSION:
//   - api/validation.spec.ts   -- asserts the version /v1/validate stamps on its result.
//   - topology/targets.ts      -- re-exports it as VALIDATION_EXPECTED.ruleSetVersion (see
//                                 below for THAT constant's own consumers).
//   - api/perf.spec.ts         -- asserts the version POST /v1/imports stamps into its
//                                 response body's rule_set_version (M4-04-08's PERF-02).
// Transitive consumers, via topology/targets.ts's VALIDATION_EXPECTED.ruleSetVersion --
// each asserts it against a LIVE RENDERED browser table cell, not an API response body,
// so each is exactly the positional-pin risk this header exists to flag:
//   - topology/validation.spec.ts      -- the M3-09 playground's ViolationsTable.
//   - topology/invoice-surfaces.spec.ts -- the M4-09 invoice-detail surface's OWN mount
//                                 of the same ViolationsTable component (added
//                                 INVCR-01-13/D8 -- it resolved VALIDATION_EXPECTED.
//                                 ruleSetVersion at two call sites for every version this
//                                 module has ever named, but was never added to this list
//                                 until now [positional-pins-are-invisible]).
// All of the above are steps of the one gated `e2e` job in dev-env.yml, so a version
// publish breaks them together -- and one constant fixes them together.
//
// ONE module, not three constants in three directories: scattered version literals are
// precisely the bug class [active-version-pinning-is-the-bug] exists to kill (a hand
// list, a second hand list, and a grep each missed a different subset of it). This is
// the single place to bump per version publish.
//
// WHY THE THIRD CONSUMER WAS MISSED, and the rule it earns [positional-pins-are-invisible]:
// this header once said "both". day30.spec.ts was a live rendered assertion on the SAME
// cell as topology's -- the identical shape, one suite over -- yet SIX successive
// instruments walked past it: the architect's hand-list, the critic's hand-list, a naive
// grep, a corrected grep, a golden-JSON sweep, and RS-V2-14's own detection command
// (rule_set_v2_test.go). The reason is mechanical, not sloppiness: the assertion took the
// shape `expect(row.locator('td').last()).toHaveText(<bare quoted number>)` -- deliberately
// PARAPHRASED here, never reproduced verbatim, so this explanation can never itself become
// the hit that a future sweep for the literal trips over. That assertion named the cell by
// ORDINAL, never by name, so it carried no `version` token and no JSON quote for any
// pattern to match. Verified: both detectors return zero hits on it, by construction. Its
// only clue lived in the prose of the comment above it, which restated the invariant as a
// bare number -- exactly how it survived: a comment asserting what the code was breaking.
// THE RULE: a version pin is not always spelled "version". When this constant is bumped,
// grep is necessary but NEVER sufficient -- READ the rendered/positional assertions
// (toHaveText / toContainText / nth() / last() / snapshots) in every suite that drives a
// surface which displays the version. Consumers get ADDED to the list above; they are not
// discovered by search.
//
// Currently 3: INVCR-01-13 (D8) published v3 -- v2's same 19 rules, SELECT-copied
// verbatim, with `target` filled in on the 4 keys that shipped blank
// (vat-standard-rate/line-items-sum-subtotal/line-cost-non-negative/
// no-duplicate-line-items) so the inline fix editor needs no client-side rule-key map.
// Evaluation-neutral (same rule keys/severities/messages/verdicts as v2 -- only each
// violation's `path` differs on those 4 keys); v2 is deactivated, not mutated -- see
// migrations/20260731090000_rule_set_v3.sql and internal/validation/rule_set_v3_test.go.
export const ACTIVE_RULE_SET_VERSION = 3
