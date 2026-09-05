// QA Stage 4 (task-329, BUG-01-03) — complements InvoicesList.test.tsx's AC-6 runtime
// test. environment:'node' (vitest.config.ts default) so import.meta.url stays a real
// file: URL -- Pager.test.ts's own source-scan idiom needs the same.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const src = readFileSync(fileURLToPath(new URL('./InvoicesList.tsx', import.meta.url)), 'utf8')

describe('poll-tick call-shape guard ([poll-tick-follows-the-page])', () => {
  it('the main fetch and the poll tick pass the literally identical listInvoices options shape', () => {
    const callSites = src.match(/listInvoices\(ctx\.authedFetch, base, \{ needsAttention, entityId: activeEntityId, limit: REGISTER_PAGE_SIZE, offset, q: ctx\.invoiceQuery \|\| undefined \}\)/g) ?? []
    expect(callSites, 'exactly two listInvoices call sites (main fetch + poll tick), same options shape').toHaveLength(2)
  })
})

// QA (bug-01-03 cycle 3): evergreen guard against re-introducing a content-shape proxy
// (f3b2b54's `invoices.length` mistake) for envelope staleness. `fresh` must stay keyed
// on identity (fetchedEntityId) alone -- a future edit that lets `invoices.length` back
// into the `!fresh` condition reopens the poll-empties-the-page trap.
describe('the !fresh branch is keyed on identity alone, never row count', () => {
  it('the Loading-on-stale-envelope condition does not reference list.data.invoices', () => {
    // Anchored on the CODE line (the JSX that actually renders <Loading>), not any
    // comment mentioning "!fresh" -- a comment can go stale and still contain the
    // substring after the guarding condition underneath it changes back to a
    // content-shape check, which would make a comment-anchored search pass vacuously.
    // `list.data != null` disambiguates from the unconditional `state === 'loading'`
    // spinner higher up, which renders the identical label with no such guard.
    const line = src.split('\n').find((l) => l.includes('<Loading label="Loading invoices…" />') && l.includes('list.data != null'))
    expect(line, 'the Loading-on-stale-envelope render line must exist').toBeDefined()
    expect(line, 'that render must be gated on !fresh').toMatch(/!fresh/)
    expect(line, 'must not reintroduce an invoices.length check into the freshness gate').not.toMatch(/invoices\.length/)
  })

  it('fresh is defined as fetchedEntityId identity, not derived from invoices/rows', () => {
    const freshDef = src.match(/const fresh = [^\n]+/)?.[0]
    expect(freshDef, 'fresh must be defined').toBeDefined()
    expect(freshDef).toMatch(/fetchedEntityId === activeEntityId/)
    expect(freshDef, 'must not fall back to a content-shape check').not.toMatch(/invoices\.length|rows\.length/)
  })
})

// BUG-10-02. Runs in CI on push, unlike any browser spec ([dev-env-skips-e2e-on-push]) --
// the only mechanical fence against either half of the 23px jump coming back.
describe('the needs-attention filter mounts nothing of its own above the table (BUG-10-02)', () => {
  it('no element is gated on needsAttention alone, and no margin is keyed on it', () => {
    // Control needle first: a rename, a moved file or an unreadable read would make the two
    // negations below pass over an empty string, proving nothing.
    expect(src, 'the toggle itself must still be in this file, or the negations below are vacuous').toContain('data-testid="needs-attention-toggle"')

    expect(src, 'no element above the table may mount on the filter alone').not.toMatch(/\{needsAttention && \(/)
    expect(src, 'the header margin must not move with the filter -- the other half of the 23px jump').not.toMatch(/marginBottom: needsAttention/)
  })

  // QA adversarial: the two negations above are construct-shaped, so a ternary form of the
  // same copy nested one level deeper slips past both. This one is shaped on the copy.
  it('the explainer sentence is in no code path (AC-1)', () => {
    expect(src, 'the register may not explain the filter in a line of its own').not.toContain('Includes invoices an approver sent back.')
  })
})

// BUG-10-03 (task-866). A source scan, not a runtime spec: the toggle click that reaches the
// filtered-empty state has already zeroed the offset and cleared the selection, so no reachable
// click sequence can observe those three calls from there -- a runtime assertion would pass
// vacuously whether or not the clear handler makes them.
describe('the clear control performs the same reset as the toggle (BUG-10-03)', () => {
  // Both controls put onClick immediately before their data-testid, so slice back from the
  // testid to the nearest preceding onClick.
  function handlerFor(testid: string): string {
    const at = src.indexOf(`data-testid="${testid}"`)
    expect(at, `${testid} must exist in InvoicesList.tsx`).toBeGreaterThan(-1)
    const before = src.slice(0, at)
    const onClickAt = before.lastIndexOf('onClick=')
    expect(onClickAt, `${testid} must carry an onClick handler`).toBeGreaterThan(-1)
    return before.slice(onClickAt)
  }

  it('both handlers zero the offset, clear the selection and disarm', () => {
    // Control needle: a rename, a moved file or an unreadable read would leave every slice
    // below scanning an empty string, proving nothing.
    expect(src, 'the toggle must still be in this file, or the scans below are vacuous').toContain('data-testid="needs-attention-toggle"')

    for (const testid of ['needs-attention-toggle', 'clear-needs-attention']) {
      const body = handlerFor(testid)
      expect(body, `${testid} must zero the offset -- a stale offset pages past the end of the new set`).toContain('setOffset(0)')
      expect(body, `${testid} must clear the selection -- it names rows that are no longer on screen`).toContain('setSelected([])')
      expect(body, `${testid} must disarm the bulk bar`).toContain('disarm()')
    }

    expect(handlerFor('clear-needs-attention'), 'the clear control is a one-way exit, never a flip').toContain('setNeedsAttention(false)')
  })
})
