// QA Stage 4 (task-328, BUG-01-02) — structural coverage for the Pager relocation. No
// .test.tsx anywhere renders ReviewInvoicesTab or Pager, and no e2e spec targets
// 'review-pager' or clicks Prev/Next (grep confirmed) — a copy-paste slip during the move
// (swapped canPrev/canNext, a dropped busy gate, a lost testid) would compile clean and
// ship silently wrong. Source-scan only, matching reviewBatch.test.ts's TAB-7b/BULK-15
// by-path idiom: environment:'node' (vitest.config.ts) has no jsdom, so there is no render
// oracle here regardless.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const pagerSrc = readFileSync(fileURLToPath(new URL('./Pager.tsx', import.meta.url)), 'utf8')
const tabSrc = readFileSync(fileURLToPath(new URL('./ReviewInvoicesTab.tsx', import.meta.url)), 'utf8')

describe('Pager.tsx: exported, and ReviewInvoicesTab.tsx pulls it from there instead of declaring its own (AC-1/AC-2)', () => {
  it('Pager.tsx exports Pager exactly once', () => {
    const exportSites = pagerSrc.match(/export function Pager\(/g) ?? []
    expect(exportSites, 'exactly one `export function Pager(`').toHaveLength(1)
  })

  it('ReviewInvoicesTab.tsx imports Pager from ./Pager and declares no local Pager', () => {
    expect(tabSrc, "must import { Pager } from './Pager'").toMatch(/import\s*\{\s*Pager\s*\}\s*from\s*'\.\/Pager'/)
    // Catches both a re-declared `function Pager(` and a `const Pager = (...) => ...`.
    expect(tabSrc, 'must not re-declare a local Pager').not.toMatch(/\b(?:function|const)\s+Pager\s*[(=]/)
  })
})

describe('Pager: busy gate and canPrev/canNext are wired to the right button, not transposed (AC-3)', () => {
  it('canPrev/canNext each read their OWN nav field, both gated by !busy', () => {
    expect(pagerSrc, 'canPrev must read nav.canPrev, gated by !busy').toMatch(/const canPrev = nav\.canPrev && !busy/)
    expect(pagerSrc, 'canNext must read nav.canNext, gated by !busy').toMatch(/const canNext = nav\.canNext && !busy/)
  })

  it('the Previous button pairs canPrev with prevOffset, and the Next button pairs canNext with nextOffset', () => {
    expect(
      pagerSrc,
      'Previous button: onClick must use nav.prevOffset and disabled must use !canPrev',
    ).toMatch(/<button onClick=\{\(\) => onGo\(nav\.prevOffset\)\} disabled=\{!canPrev\}[^>]*>\s*← Previous/)

    expect(
      pagerSrc,
      'Next button: onClick must use nav.nextOffset and disabled must use !canNext',
    ).toMatch(/<button onClick=\{\(\) => onGo\(nav\.nextOffset\)\} disabled=\{!canNext\}[^>]*>\s*Next →/)
  })
})

describe('Pager: testId prop defaults to review-pager and drives data-testid (AC-3/AC-5)', () => {
  it('the default parameter is the literal review-pager', () => {
    expect(pagerSrc, "testId must default to 'review-pager'").toMatch(/testId\s*=\s*'review-pager'/)
  })

  it('the wrapper renders data-testid={testId}, never a hardcoded literal', () => {
    expect(pagerSrc, 'data-testid must read the testId variable').toMatch(/data-testid=\{testId\}/)
    expect(pagerSrc, 'data-testid must not be hardcoded back to a literal').not.toMatch(/data-testid="review-pager"/)
  })

  it("ReviewInvoicesTab's call site passes no testId, so the default keeps the review screen's markup byte-identical", () => {
    const callSite = /<Pager[\s\S]*?\/>/.exec(tabSrc)?.[0]
    expect(callSite, 'exactly one <Pager ... /> call site').toBeTruthy()
    expect(callSite, 'must not pass an explicit testId prop').not.toMatch(/testId=/)
  })
})

// APPR-16-04 (task-536, D-28 carve-out): ReviewInvoicesTab's pager is deliberately left
// un-frozen -- it has no component test file, so widening its `busy` here would trace to
// no acceptance criterion. Not itself a red-today check (this call site is untouched by
// this subtask either way); it guards against a LATER subtask quietly widening it.
describe("Pager: ReviewInvoicesTab.tsx's call site stays out of the in-flight freeze (D-28 carve-out)", () => {
  it('busy is still busy={loading} alone -- no phase/submitting term, no reason prop', () => {
    const callSite = /<Pager[\s\S]*?\/>/.exec(tabSrc)?.[0]
    expect(callSite, 'exactly one <Pager ... /> call site').toBeTruthy()
    expect(callSite, "D-28: ReviewInvoicesTab's pager must stay busy={loading} alone, never widened").toMatch(/busy=\{loading\}/)
    expect(callSite, 'no reason prop belongs on the one call site D-28 carves out').not.toMatch(/reason=/)
  })
})

// A defect fix, not a widening (task-539): title never fires on a disabled element in
// Chromium (e2e/topology/roles.spec.ts's own expectDisabledWithReason helper requires
// both channels), so the frozen pager owed a VISIBLE reason, not just an attribute.
// Combined with the D-28 pin above (ReviewInvoicesTab passes no `reason=`), the `reason !=
// null` gate below proves that call site renders no reason node at all.
describe('Pager: the frozen reason is visible text, not title alone (D-25 fix)', () => {
  it('the reason node is gated on reason != null, sharing one id with both buttons via aria-describedby', () => {
    expect(pagerSrc, 'the visible reason must be conditioned on reason != null').toMatch(/reason != null && \(/)
    expect(pagerSrc, 'the reason span must carry a stable data-testid').toMatch(/data-testid="pager-blocked-reason"/)
    const describedBySites = pagerSrc.match(/aria-describedby=\{reason != null \? reasonId : undefined\}/g) ?? []
    expect(describedBySites, 'both Previous and Next must wire aria-describedby to the same reasonId').toHaveLength(2)
  })

  it('title is kept as a secondary channel on both buttons, not replaced', () => {
    const titleSites = pagerSrc.match(/title=\{reason\}/g) ?? []
    expect(titleSites, 'both buttons must still carry title={reason}').toHaveLength(2)
  })
})
