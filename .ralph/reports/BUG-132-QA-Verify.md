# BUG-132 QA verify
HEAD f7ba46cbc + one QA commit. tsc 0; e2e vitest 44 files / 816 tests pass. No runnable test added: mutations file empty. Playwright specs not run (nightly only).

## Verdict: PASS after one QA fix (AC 3 gap in SUP-03 nightly path)

## Per spec, per target
AIR07-E2E-04 (import-wizard.spec.ts:617-): fake: source none, mapping empty, 11 columns, no badge, "Map invoice number" visible, hand placement enables Import, no SUGGESTED badge: all kept (inside !real). Nightly: source 'ai' asserted, 11 columns asserted, hand-placement block skipped (real mapping places fields and badges, so those assertions would be wrong). console-error gate still runs. Holds.
SUP-03 AI usage (support-console.spec.ts): fake: failed=true filter, "left a failed row" polls, forced `failed` grows: all kept. Nightly: FAILURES view may be empty (served [] equals screen rows, liveAiRows with no keys makes no read, ok-row poll still asserts none ok); DAILY TOTALS forced row exists (calls grow), failed >= only. Holds. GAP found: nightly asserted nothing about the oversized call being answered (AC 3). Fixed: recordOversizedAiCall now asserts `source === 'ai'` on the nightly.
contract-staff-ai-usage (failed=true): fake unchanged (forB watched, row listed under failed=true). Nightly: watches the unfiltered list, asserts outcome 'ok', then reads failed=true and asserts the id absent. Race (executor's concern): none. ai.Call writes one row via airecord.Record after the final outcome (client.go:101); a row has its final outcome when listed, and both reads query the same table. Poll could match a foreign tenant-B ai row only if another spec ran in parallel; e2e rules fix workers:1.

## AC table
1 reproduction: nightly run 43181827064 (per Decisions; no new test). 2 fake kept: AIR07 :641-653, SUP-03 helper/FAILURES guards `!nightlyTarget()`, contract `watched=forB`; nothing weakened. 3 nightly: AIR07 `source ai`+11 columns; contract `outcome ok`; SUP-03 `source ai` (added). 4 comments: fixture, helper, contract, AIR07 updated. 5: next nightly.

## Other occurrences
`go run ./internal/tools/breaklist 'buildOversizedCellCsv|\.repeat\(.{0,12}[0-9_]{5,}|real AI (refus|fail)|AI refus|refused by the (real )?AI|blank answer|fall to the no-answer|source.{0,12}none' e2e` -> TOTAL 13 hits in 8 files. Only one other: e2e/api/contract-invoice.spec.ts:2539 "explain says unavailable when the AI call fails" (2M digits, comment "overflow the model's context so a real call is refused"). Explain input is not bounded by SECAUD-05 (explainPromptText sends the whole payload, `ceiling:` in explain.go:44), so the claim is not proven false by this change and the test was not red in the failing nightly. Out of scope; unchanged. Advisory: its refusal depends on a model context limit, not a deterministic check.
Wide-column / maxPromptColumns / 48000-byte search: no e2e fixture hits.

## Comment sweep
Changed-file comments are true. Remaining "real ... refused" claim: contract-invoice.spec.ts:2538 (explain, see above). docs/, README, .claude/rules/e2e.md: no false claim.

## Pinned surprises
- Nightly AIR07 skips the hand-placement and Import-enabled path; real mapping covers it elsewhere (import-wizard-real-ai.spec.ts).
- Nightly `outcome toBe('ok')` fails on a transient OpenRouter `unavailable` (stricter than AI_OUTCOMES at :160); matches AC 3.
- SUP-03 nightly does not assert `failed` is unchanged by the ok call (advisory).

## Phase 3.5 assertion citations
AC2: import-wizard.spec.ts:641-653 (fake); contract spec `failed=true` poll at :215-223 (fake); support-console.spec.ts FAILURES polls `!nightlyTarget()`. AC3: import-wizard.spec.ts:641 (source ai) + 11-column count; contract spec `oversized.outcome toBe('ok')` and `not.toContain`; support-console.spec.ts recordOversizedAiCall `source toBe('ai')`.

Memory notes applied: none
