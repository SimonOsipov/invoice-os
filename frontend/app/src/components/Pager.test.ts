// Source pins with no render sibling: the review tab uses the shared Pager, its call site's
// `loading` busy arm, and the reason node's aria-describedby gate. Behaviour lives in
// Pager.render.test.tsx and ReviewInvoicesTab.test.tsx.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const pagerSrc = readFileSync(fileURLToPath(new URL('./Pager.tsx', import.meta.url)), 'utf8')
const tabSrc = readFileSync(fileURLToPath(new URL('./ReviewInvoicesTab.tsx', import.meta.url)), 'utf8')

describe('Pager.tsx: exported, and ReviewInvoicesTab.tsx pulls it from there instead of declaring its own (AC-1/AC-2)', () => {
  it('ReviewInvoicesTab.tsx imports Pager from ./Pager and declares no local Pager', () => {
    expect(tabSrc, "must import { Pager } from './Pager'").toMatch(/import\s*\{\s*Pager\s*\}\s*from\s*'\.\/Pager'/)
    // Catches both a re-declared `function Pager(` and a `const Pager = (...) => ...`.
    expect(tabSrc, 'must not re-declare a local Pager').not.toMatch(/\b(?:function|const)\s+Pager\s*[(=]/)
  })
})

// ReviewInvoicesTab.test.tsx covers the freeze at runtime; this pins the call-site wiring,
// including the `loading` arm, which that file does not drive.
describe("Pager: ReviewInvoicesTab.tsx's call site freezes during a bulk submit, same as its siblings (D-28 closed)", () => {
  it("busy folds in phase === 'submitting', and reason is BULK_COPY.pagerReason while submitting", () => {
    const callSite = /<Pager[\s\S]*?\/>/.exec(tabSrc)?.[0]
    expect(callSite, 'exactly one <Pager ... /> call site').toBeTruthy()
    expect(callSite, "the freeze must fold into busy, matching InvoicesList's/ApprovalsView's own call sites").toMatch(
      /busy=\{loading \|\| phase === 'submitting'\}/,
    )
    expect(callSite, 'the reason must be BULK_COPY.pagerReason while submitting, undefined otherwise').toMatch(
      /reason=\{phase === 'submitting' \? BULK_COPY\.pagerReason : undefined\}/,
    )
  })
})

// A defect fix, not a widening (task-539): title never fires on a disabled element in
// Chromium (e2e/topology/roles.spec.ts's own expectDisabledWithReason helper requires
// both channels), so the frozen pager owed a VISIBLE reason, not just an attribute.
// D-28 closed: ReviewInvoicesTab's call site now passes `reason=` too (pin above), so the
// `reason != null` gate below is what renders ITS visible reason node as well.
describe('Pager: the frozen reason is visible text, not title alone (D-25 fix)', () => {
  it('the reason node is gated on reason != null, sharing one id with both buttons via aria-describedby', () => {
    expect(pagerSrc, 'the visible reason must be conditioned on reason != null').toMatch(/reason != null && \(/)
    expect(pagerSrc, 'the reason span must carry a stable data-testid').toMatch(/data-testid="pager-blocked-reason"/)
    const describedBySites = pagerSrc.match(/aria-describedby=\{reason != null \? reasonId : undefined\}/g) ?? []
    expect(describedBySites, 'both Previous and Next must wire aria-describedby to the same reasonId').toHaveLength(2)
  })
})
