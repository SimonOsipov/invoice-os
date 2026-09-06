// QA (task-331, BUG-01-05) -- source-scan guard mirroring InvoicesList.pollShape.test.ts's
// own idiom: environment:'node' (vitest.config.ts default) so import.meta.url stays a real
// file: URL.
//
// ROUTE-04-03 REPOINTED this guard, it did not relax it. Its invariant is unchanged -- the
// submit path clamps and never passes the raw field value -- but submit now calls
// ctx.searchInvoices (a committed search, one pushed entry) while the clear button keeps
// ctx.setInvoiceQuery (a replace). Both verbs are held to the same rule.
//
// AC #7 ("never sends more than 200 chars, including on paste") holds only if EVERY path
// that reaches the value InvoicesList forwards to listInvoices as `q` is either clamped or
// the literal empty string. maxLength={200} is a browser CHARACTER cap (enforced on the
// input, not on this call), not a byte cap, so it cannot be this guard by itself -- see
// Header.test.tsx's paste-bypass tests for the runtime proof that maxLength alone is
// insufficient.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const src = readFileSync(fileURLToPath(new URL('./Header.tsx', import.meta.url)), 'utf8')

// Balanced to one nesting level: a `[^)]*` argument stops at the FIRST `)`, so it can never
// return the whole of `ctx.searchInvoices(clampFilterText(query))` the assertions below pin.
const callSites = (text: string, verb: string) =>
  text.match(new RegExp(`ctx\\.${verb}\\((?:[^()]|\\([^()]*\\))*\\)`, 'g')) ?? []

describe('search call-shape guard (AC #7, [search-input-is-capped-client-side])', () => {
  it('exactly one committing call site, and it clamps', () => {
    const sites = callSites(src, 'searchInvoices')
    expect(sites, 'submit is the only path that may commit a search').toHaveLength(1)
    expect(sites[0]).toBe('ctx.searchInvoices(clampFilterText(query))')
  })

  it('exactly one clearing call site, and it is the literal empty string', () => {
    const sites = callSites(src, 'setInvoiceQuery')
    expect(sites, 'the clear button is the only path that may set invoiceQuery directly').toHaveLength(1)
    expect(sites[0]).toBe("ctx.setInvoiceQuery('')")
  })

  it('the box seeds from the committed query, never a blank string', () => {
    // No runtime spec can discriminate this: render() flushes the mirror effect, so
    // useState('') and useState(ctx.invoiceQuery) read identically by the first assertion.
    // The seed is what stops a blank box painting for a frame on a cold /invoices?q= boot.
    expect(src).toContain('useState(ctx.invoiceQuery)')
    expect(src, 'the blank seed is the regression this pins').not.toContain("useState('')")
    // Control needle: an absence assertion over a typo'd pattern reads like a clean absence.
    expect("const [query, setQuery] = useState('')").toContain("useState('')")
  })

  it('neither verb ever receives the raw, unclamped field value', () => {
    expect(src).not.toMatch(/ctx\.searchInvoices\(query\)/)
    expect(src).not.toMatch(/ctx\.setInvoiceQuery\(query\)/)
    // Control needles: an absence assertion over a typo'd pattern reads exactly like a
    // clean absence, so prove both patterns can match the shape they forbid.
    expect('ctx.searchInvoices(query)').toMatch(/ctx\.searchInvoices\(query\)/)
    expect('ctx.setInvoiceQuery(query)').toMatch(/ctx\.setInvoiceQuery\(query\)/)
  })
})
