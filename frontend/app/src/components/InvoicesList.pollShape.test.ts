// QA Stage 4 (task-329, BUG-01-03) — complements InvoicesList.test.tsx's AC-6 runtime
// test. environment:'node' (vitest.config.ts default) so import.meta.url stays a real
// file: URL -- Pager.test.ts's own source-scan idiom needs the same.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const src = readFileSync(fileURLToPath(new URL('./InvoicesList.tsx', import.meta.url)), 'utf8')

describe('poll-tick call-shape guard ([poll-tick-follows-the-page])', () => {
  it('the main fetch and the poll tick pass the literally identical listInvoices options shape', () => {
    const callSites = src.match(/listInvoices\(ctx\.authedFetch, base, \{ needsAttention, entityId: activeEntityId, limit: REGISTER_PAGE_SIZE, offset \}\)/g) ?? []
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
