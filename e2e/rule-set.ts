// The single source of truth for the ACTIVE global MBS rule-set version across the
// whole e2e package ([e2e-active-version]).
//
// Both deployed-dev suites that observe the version resolve through here:
//   - api/validation.spec.ts   -- asserts the version /v1/validate stamps on its result.
//   - topology/targets.ts      -- VALIDATION_EXPECTED.ruleSetVersion, which
//                                 topology.spec.ts asserts against a LIVE RENDERED
//                                 browser table cell.
// Both are steps of the one gated `e2e` job in dev-env.yml, so a version publish
// breaks them together -- and one constant fixes them together.
//
// ONE module, not two constants in two directories: scattered version literals are
// precisely the bug class [active-version-pinning-is-the-bug] exists to kill (a hand
// list, a second hand list, and a grep each missed a different subset of it). This is
// the single place to bump per version publish.
//
// Currently 2: M4-04-01 reverted the content mutation made to the published v1 and
// re-issued its two line-item rules as v2 (v1's 17 base rules + 2 = 19, active), so
// v1 is frozen and inactive again -- see migrations/20260716185106_rule_set_v2.sql.
export const ACTIVE_RULE_SET_VERSION = 2
