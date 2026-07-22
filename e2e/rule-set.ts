// The single source of truth for the ACTIVE global MBS rule-set version across the
// whole e2e package ([e2e-active-version]).
//
// All THREE deployed-dev suites that observe the version resolve through here:
//   - api/validation.spec.ts   -- asserts the version /v1/validate stamps on its result.
//   - topology/targets.ts      -- VALIDATION_EXPECTED.ruleSetVersion, which
//                                 topology/validation.spec.ts asserts against a LIVE
//                                 RENDERED browser table cell.
//   - api/perf.spec.ts         -- asserts the version POST /v1/imports stamps into its
//                                 response body's rule_set_version (M4-04-08's PERF-02).
// All three are steps of the one gated `e2e` job in dev-env.yml, so a version publish
// breaks them together -- and one constant fixes them together.
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
// Currently 2: M4-04-01 reverted the content mutation made to the published v1 and
// re-issued its two line-item rules as v2 (v1's 17 base rules + 2 = 19, active), so
// v1 is frozen and inactive again -- see migrations/20260716185106_rule_set_v2.sql.
export const ACTIVE_RULE_SET_VERSION = 2
