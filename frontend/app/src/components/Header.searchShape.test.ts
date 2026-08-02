// QA (task-331, BUG-01-05) -- source-scan guard mirroring InvoicesList.pollShape.test.ts's
// own idiom: environment:'node' (vitest.config.ts default) so import.meta.url stays a real
// file: URL.
//
// AC #7 ("never sends more than 200 chars, including on paste") holds only if EVERY path
// that reaches ctx.setInvoiceQuery -- the sole setter of the value InvoicesList forwards to
// listInvoices as `q` -- is either clamped or the literal empty string. maxLength={200} is a
// browser CHARACTER cap (enforced on the input, not on this call), not a byte cap, so it
// cannot be this guard by itself -- see Header.test.tsx's paste-bypass tests for the runtime
// proof that maxLength alone is insufficient.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const src = readFileSync(fileURLToPath(new URL('./Header.tsx', import.meta.url)), 'utf8')

describe('setInvoiceQuery call-shape guard (AC #7, [search-input-is-capped-client-side])', () => {
  it('exactly two call sites, and every one is either clamped or the literal empty string', () => {
    const callSites = src.match(/ctx\.setInvoiceQuery\([^)]*\)/g) ?? []
    expect(callSites, 'submit + clear are the only two paths that may set invoiceQuery').toHaveLength(2)

    for (const call of callSites) {
      const safe = call === "ctx.setInvoiceQuery('')" || /^ctx\.setInvoiceQuery\(clampFilterText\(/.test(call)
      expect(safe, `unclamped, non-empty call site: ${call}`).toBe(true)
    }
  })

  it('the submit call site clamps, never sends the raw field value straight through', () => {
    expect(src).toContain('ctx.setInvoiceQuery(clampFilterText(query))')
    // The un-clamped raw value must never itself be the sole argument.
    expect(src).not.toMatch(/ctx\.setInvoiceQuery\(query\)/)
  })
})
