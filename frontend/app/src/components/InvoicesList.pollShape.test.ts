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
