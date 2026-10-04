// @vitest-environment jsdom
// Mode A RED specs (task-334, BUG-01-08) -- CustomersView still renders the KPI cards
// and fetches only page 1 (listInvoices, no limit/offset). DOM-level per the
// Implementation Notes override: a source-scan can't catch a renamed-class KPI card
// reappearing. fmtShort stays a text scan since imports aren't DOM.
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import { AGGREGATE_MAX_PAGES, AGGREGATE_PAGE_SIZE } from '../lib/invoices'
import type { InvoiceListResponse, InvoiceRecord } from '../lib/invoices'
import type { PlatformCtx } from '../types'
import { CustomersView } from './CustomersView'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function mockFetchSequence(responses: MockResponse[]) {
  const fetchMock = vi.fn()
  for (const r of responses) fetchMock.mockResolvedValueOnce(r)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function listResponse(invoices: InvoiceRecord[], pagination: { limit: number; offset: number; total: number }): MockResponse {
  const body: InvoiceListResponse = { invoices, pagination }
  return { ok: true, status: 200, json: () => Promise.resolve(body) }
}

function row(over: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return {
    id: 'inv-x',
    entity_id: 'ent-1',
    import_batch_id: null,
    invoice_number: 'INV-X',
    status: 'draft',
    issue_date: '2026-07-01',
    supplier_tin: '10000000-0001',
    supplier_name: 'Acme Ltd',
    buyer_tin: '20000000-0001',
    buyer_name: 'Beta Traders',
    currency: 'NGN',
    subtotal: '1000.00',
    vat: '75.00',
    total: '1075.00',
    violations: [],
    rule_set_version_id: null,
    created_at: '2026-07-01T00:00:00Z',
    irn: null,
    csid: null,
    qr_payload: null,
    rejection_reasons: [],
    kept_as_is_at: null,
    kept_as_is_by: null,
    kept_as_is_reason: null,
    failure_kind: null,
    approval: null,
    rule_set_version: null,
    can_approve: false,
    approve_blocked_reason: null,
    can_submit: false,
    submit_blocked_reason: null,
    ...over,
  }
}

// 8-digit-hyphen-4-digit, unique per index -- keeps aggregateCustomers grouping by
// distinctness alone, never falling back to name-keying.
function tinFor(i: number): string {
  return `9${String(i).padStart(7, '0')}-0001`
}

function urlParams(url: string): URLSearchParams {
  return new URL(url).searchParams
}

function custCtx(entityId = 'ent-1'): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { name: 'Acme Co', short: 'Acme', entityId },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    openCreate: () => {},
  }
  return ctx as unknown as PlatformCtx
}

// The queued responses fetchAllInvoices' own paging produces for a given total: page 1
// (caller-supplied rows) + one filler row per remaining page up to the cap.
function pagedFetchMock(total: number, firstPageRows: InvoiceRecord[]) {
  const limit = AGGREGATE_PAGE_SIZE
  const pages = Math.min(Math.ceil(total / limit), AGGREGATE_MAX_PAGES)
  const responses = [listResponse(firstPageRows, { limit, offset: 0, total })]
  for (let page = 2; page <= pages; page++) {
    responses.push(listResponse([row({ id: `filler-${page}`, buyer_tin: tinFor(9000 + page), buyer_name: `Filler Buyer ${page}` })], { limit, offset: (page - 1) * limit, total }))
  }
  return mockFetchSequence(responses)
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('CustomersView: no KPI card row (task-334, BUG-01-08, AC #1)', () => {
  it('renders no .pf-grid-4 wrapper and none of the four card labels', async () => {
    const rows = [
      row({ id: 'a', buyer_tin: tinFor(1), buyer_name: 'Alpha Traders' }),
      row({ id: 'b', buyer_tin: tinFor(2), buyer_name: 'Beta Traders' }),
    ]
    mockFetchSequence([listResponse(rows, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 2 })])

    const { container } = render(<CustomersView ctx={custCtx()} />)
    await screen.findByText('Alpha Traders')

    expect(container.querySelector('.pf-grid-4'), 'no KPI card row wrapper').toBeNull()
    expect(screen.queryAllByText('Customers'), 'no Customers card label').toHaveLength(0)
    expect(screen.queryAllByText('Valid TINs'), 'no Valid TINs card label').toHaveLength(0)
    expect(screen.queryAllByText('Flagged'), 'no Flagged card label').toHaveLength(0)
    // 'Total billed' also names a surviving TABLE column header -- exactly one match
    // (the header), never two (a card copy alongside it).
    expect(screen.queryAllByText('Total billed'), 'card copy must be gone, the table header must not').toHaveLength(1)
  })
})

describe('CustomersView: no fmtShort import (task-334, BUG-01-08, AC #2)', () => {
  it('the import block carries no fmtShort reference', () => {
    // import.meta.url is rewritten off the file: scheme under jsdom (breaks
    // fileURLToPath), unlike the node-env source-scan siblings (Header.searchShape.test.ts)
    // -- cwd is stable instead (pnpm --filter runs vitest from the package root).
    const src = readFileSync(path.join(process.cwd(), 'src/components/CustomersView.tsx'), 'utf8')
    expect(src).not.toMatch(/\bfmtShort\b/)
  })
})

describe('CustomersView: whole-set aggregation via fetchAllInvoices (task-334, BUG-01-08, AC #3/#4)', () => {
  it('fetches both pages of a 259-row set -- not a single un-paged call', async () => {
    const TOTAL = 259
    const page1 = Array.from({ length: AGGREGATE_PAGE_SIZE }, (_, i) => row({ id: `inv-${i}`, buyer_tin: tinFor(i), buyer_name: `Buyer ${i}` }))
    const page2 = Array.from({ length: TOTAL - AGGREGATE_PAGE_SIZE }, (_, i) => {
      const idx = AGGREGATE_PAGE_SIZE + i
      return row({ id: `inv-${idx}`, buyer_tin: tinFor(idx), buyer_name: `Buyer ${idx}` })
    })
    const fetchMock = mockFetchSequence([
      listResponse(page1, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: TOTAL }),
      listResponse(page2, { limit: AGGREGATE_PAGE_SIZE, offset: AGGREGATE_PAGE_SIZE, total: TOTAL }),
    ])

    const { container } = render(<CustomersView ctx={custCtx()} />)
    await screen.findByText(`Buyer ${TOTAL - 1}`)

    expect(fetchMock, 'the whole-set fetch must span both pages, not stop at page 1').toHaveBeenCalledTimes(2)
    const [firstUrl, secondUrl] = fetchMock.mock.calls.map((c) => c[0] as string)
    expect(urlParams(firstUrl).get('limit')).toBe(String(AGGREGATE_PAGE_SIZE))
    expect(urlParams(firstUrl).get('offset')).toBe('0')
    expect(urlParams(secondUrl).get('limit')).toBe(String(AGGREGATE_PAGE_SIZE))
    expect(urlParams(secondUrl).get('offset')).toBe(String(AGGREGATE_PAGE_SIZE))
    // QA (task-334): entity scoping must survive onto every page fetchAllInvoices makes,
    // not just page 1 -- a page-2+ call that drops entity_id would silently widen to
    // tenant-wide for every buyer past the first page.
    expect(urlParams(firstUrl).get('entity_id'), 'page 1 must be scoped to the active entity').toBe('ent-1')
    expect(urlParams(secondUrl).get('entity_id'), 'page 2 must also be scoped to the active entity, not tenant-wide').toBe('ent-1')

    // Every one of the 259 distinct buyers reaches the table -- a naive single-page fetch
    // would render 200 rows, not 259.
    expect(container.querySelectorAll('.pf-list-row')).toHaveLength(TOTAL)
  })
})

describe('CustomersView: truncation disclosure (task-334, BUG-01-08, [aggregate-cap-with-disclosure], AC #5)', () => {
  it('a capped fetch discloses fetched vs. total', async () => {
    const firstPage = [
      row({ id: 'inv-a', buyer_tin: tinFor(1), buyer_name: 'Alpha Traders' }),
      row({ id: 'inv-b', buyer_tin: tinFor(2), buyer_name: 'Beta Traders' }),
    ]
    // 2500 > AGGREGATE_MAX_PAGES(10) * AGGREGATE_PAGE_SIZE(200) -> truncated. fetched =
    // 2 page-1 rows + 9 one-row filler pages (pagedFetchMock) = 11.
    pagedFetchMock(2500, firstPage)

    const { container } = render(<CustomersView ctx={custCtx()} />)
    await screen.findByText('Alpha Traders')

    // New testid contract -- no [aggregate-cap-with-disclosure] UI consumer exists yet.
    const notice = await screen.findByTestId('customers-truncated-notice')
    expect(notice.textContent, 'must name what was actually fetched').toContain('11')
    expect(notice.textContent, 'must name the true set total').toContain('2500')
    // QA (task-334): the disclosed "fetched" number must equal the rows the table ACTUALLY
    // aggregated, not a number lifted from elsewhere (e.g. `total`) -- a notice that lies
    // about what's on screen is worse than no notice at all.
    expect(container.querySelectorAll('.pf-list-row'), 'fetched=11 must match the actual aggregated row count').toHaveLength(11)
  })

  it('an un-truncated fetch discloses nothing', async () => {
    const rows = [row({ id: 'inv-c', buyer_tin: tinFor(3), buyer_name: 'Gamma Traders' })]
    mockFetchSequence([listResponse(rows, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 1 })])

    render(<CustomersView ctx={custCtx()} />)
    await screen.findByText('Gamma Traders')

    expect(screen.queryByTestId('customers-truncated-notice')).toBeNull()
  })
})

describe('CustomersView: zero-invoice empty state (task-334, BUG-01-08, AC #6, [empty-is-total-zero])', () => {
  it('still renders "No customers yet", fetched via the aggregate page-1 shape', async () => {
    const fetchMock = mockFetchSequence([listResponse([], { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 0 })])

    render(<CustomersView ctx={custCtx()} />)

    expect(await screen.findByText('No customers yet')).toBeDefined()
    const [url] = fetchMock.mock.calls[0] as [string]
    expect(urlParams(url).get('limit'), 'must go through fetchAllInvoices, not the old un-paged call').toBe(String(AGGREGATE_PAGE_SIZE))
    expect(urlParams(url).get('offset')).toBe('0')
  })
})

describe('CustomersView: entity switch settles on the new client (task-334, BUG-01-08, [customers-fresh-gate])', () => {
  it('never sticks on the previous client\'s rows, and the refetch is scoped + page-shaped', async () => {
    // jsdom's act() batching flushes straight through the one-commit transient where
    // list.data still belongs to the OLD entity while ctx.active.entityId already reads
    // the new one (same limitation InvoicesList.test.tsx's own entity-switch test
    // documents) -- this cannot prove no frame ever paints the old total/truncated
    // numbers. It proves what jsdom CAN see: the screen settles on the new entity, never
    // stuck on the old one, via a refetch that is both scoped and page-shaped correctly.
    const entA = [row({ id: 'a1', entity_id: 'ent-1', buyer_tin: tinFor(1), buyer_name: 'Entity A Buyer' })]
    const entB = [row({ id: 'b1', entity_id: 'ent-2', buyer_tin: tinFor(2), buyer_name: 'Entity B Buyer' })]
    const fetchMock = mockFetchSequence([
      listResponse(entA, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 1 }),
      listResponse(entB, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 1 }),
    ])

    const { rerender } = render(<CustomersView ctx={custCtx('ent-1')} />)
    await screen.findByText('Entity A Buyer')

    rerender(<CustomersView ctx={custCtx('ent-2')} />)

    await screen.findByText('Entity B Buyer')
    expect(screen.queryByText('Entity A Buyer'), 'must settle on the new entity, never stay stuck on the old rows').toBeNull()

    const [, secondUrl] = fetchMock.mock.calls.map((c) => c[0] as string)
    expect(urlParams(secondUrl).get('entity_id'), 'the switch-triggered refetch must be scoped to the new entity').toBe('ent-2')
    expect(urlParams(secondUrl).get('limit'), 'the refetch must go through the aggregate page shape too, not just the mount fetch').toBe(String(AGGREGATE_PAGE_SIZE))
  })
})

// QA (task-334, BUG-01-08, Mode B): `fresh` must gate the POPULATED branch (the one that
// also renders the truncation notice's fetched/total numbers), not just the row array
// gateByActiveEntity already filters -- this is the exact bug class BUG-01-03 cost two fix
// cycles. Mutation-verified: dropping `&& fresh` from that condition leaves every render
// test in this file green (the one-commit stale-envelope transient it guards is
// unobservable under jsdom -- React flushes the passive effect that nulls `list.data`
// synchronously inside the same act() as the prop change, so a test can never catch the
// component mid-render with a NEW activeEntityId and the OLD envelope still attached; see
// InvoicesList.test.tsx's own identical caveat). A source scan is therefore the only oracle
// available for this specific regression class.
describe('CustomersView: `fresh` gates the populated/truncation branch, not just row filtering (task-334, [customers-fresh-gate])', () => {
  it('the populated render branch requires `fresh` alongside `list.data != null`', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/components/CustomersView.tsx'), 'utf8')
    expect(src).toMatch(/state === 'ready' && list\.data != null && fresh && customers\.length > 0 && \(/)
  })
})

// QA (task-336, BUG-01-10, Mode B): [customers-fresh-gate] above covers only the populated
// branch -- BUG-01-09's QA found and closed the identical gap for ReportsView across all
// three branches (loading-insert, empty, populated) and flagged this file's other two as
// still unguarded. Same oracle: jsdom flushes the stale-envelope transient synchronously,
// so a source scan is the only way to pin these.
describe('CustomersView: `fresh` gates the loading-insert and empty-state branches too (task-336, [customers-fresh-gate])', () => {
  const src = () => readFileSync(path.join(process.cwd(), 'src/components/CustomersView.tsx'), 'utf8')

  it('the loading-insert branch requires `!fresh` alongside `list.data != null`', () => {
    expect(src()).toMatch(/state === 'ready' && list\.data != null && !fresh && <Loading/)
  })

  it('the empty-state branch requires `fresh` alongside `list.data != null`', () => {
    expect(src()).toMatch(/state === 'ready' && list\.data != null && fresh && customers\.length === 0/)
  })
})

// QA (task-334, BUG-01-08, Mode B): [empty-is-total-zero] requires the SHARED
// allInvoicesIsEmpty predicate, not a second inline copy of the same rule. Not behaviorally
// distinguishable at this layer (mutation-verified): AllInvoices.invoices is the whole-set
// concatenation across every fetched page, so `invoices.length === 0` iff `total === 0`
// whenever the fetch behaves normally -- an inline `(r) => r.invoices.length === 0` arrow
// passes every render test in this file unchanged. A source scan is the only oracle that
// catches the drift [empty-is-total-zero] exists to prevent (a second, later-diverging copy
// of "empty" for this envelope shape).
describe('CustomersView: isEmpty is the shared allInvoicesIsEmpty predicate (task-334, [empty-is-total-zero])', () => {
  it('passes allInvoicesIsEmpty by reference, not a local arrow', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/components/CustomersView.tsx'), 'utf8')
    expect(src).toMatch(/isEmpty:\s*allInvoicesIsEmpty\b/)
  })
})

// --- the v2 register (CU rows) ----------------------------------------
describe('CustomersView: the v2 register (RESKIN2-05-02)', () => {
  function readyRows() {
    return [
      row({ id: 'a', buyer_tin: '20000000-0001', buyer_name: 'Alpha Traders' }),
      row({ id: 'b', buyer_tin: '123', buyer_name: 'Beta Traders' }),
    ]
  }

  async function renderReady() {
    mockFetchSequence([listResponse(readyRows(), { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 2 })])
    const utils = render(<CustomersView ctx={custCtx()} />)
    await screen.findByText('Alpha Traders')
    return utils
  }

  it("CU-01 the h1 takes the heading rule's weight", async () => {
    await renderReady()

    const h1 = screen.getByRole('heading', { level: 1, name: 'Customers & vendors' })
    expect(h1.style.fontSize, 'control: the h1 keeps its size').toBe('26px')
    expect(h1.style.fontWeight, 'the h1 must carry no inline font-weight').toBe('')
  })

  it('CU-02 the tax-status chip is a 4px mono chip with no dot', async () => {
    await renderReady()

    const chips = [screen.getByText('VALID', { exact: true }), screen.getByText('NEEDS TIN', { exact: true })]
    expect(chips).toHaveLength(2)
    for (const chip of chips) {
      const what = chip.textContent
      expect(chip.classList.contains('mono'), `${what} is a mono span`).toBe(true)
      expect(chip.style.borderRadius, `${what} corner`).toBe('var(--radius-sm)')
      expect(chip.style.padding, `${what} padding`).toBe('3px 9px')
      expect(chip.style.letterSpacing, `${what} tracking`).toBe('0.04em')
      expect(chip.childElementCount, `${what} holds no dot`).toBe(0)
      const wrapper = chip.parentElement as HTMLElement
      expect(wrapper.tagName, `${what} sits in a plain span`).toBe('SPAN')
      expect(wrapper.getAttribute('style'), `${what} wrapper takes no style`).toBeNull()
    }
  })

  it('CU-03 avatars are circles', async () => {
    await renderReady()

    const avatars = ['AT', 'BT'].map((i) => screen.getByText(i, { exact: true }))
    expect(avatars).toHaveLength(2)
    for (const a of avatars) expect(a.style.borderRadius, `${a.textContent} avatar`).toBe('50%')
  })

  it('CU-04 the empty title is 700', async () => {
    mockFetchSequence([listResponse([], { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 0 })])
    render(<CustomersView ctx={custCtx()} />)

    const title = await screen.findByText('No customers yet')
    expect(title.style.fontSize, 'control: the title keeps its size').toBe('16px')
    expect(title.style.fontWeight).toBe('700')
  })

  it('CU-06 each row is keyed by TIN and its chip, TIN colour and copy follow validity', async () => {
    const rows = [
      row({ id: 'g1', buyer_tin: '30000000-0001', buyer_name: 'Gamma Ltd', total: '3000.00' }),
      row({ id: 'g2', buyer_tin: '30000000-0002', buyer_name: 'Gamma Ltd', total: '2000.00' }),
      row({ id: 'm1', buyer_tin: '', buyer_name: 'Missing Tin Co', total: '1000.00' }),
    ]
    mockFetchSequence([listResponse(rows, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 3 })])
    render(<CustomersView ctx={custCtx()} />)
    await screen.findByText('Missing Tin Co')

    expect(screen.getAllByText('Gamma Ltd'), 'same name, two TINs: two rows').toHaveLength(2)
    const valid = screen.getAllByText('VALID', { exact: true })
    expect(valid).toHaveLength(2)
    for (const chip of valid) {
      expect(chip.style.color).toBe('var(--status-green-text)')
      expect(chip.style.background).toBe('var(--status-green-bg)')
      expect(chip.style.border).toBe('1px solid var(--status-green-border)')
      expect(chip.style.display).toBe('inline-flex')
      expect(chip.style.fontSize).toBe('10px')
      expect(chip.style.fontWeight).toBe('600')
    }
    const bad = screen.getByText('NEEDS TIN', { exact: true })
    expect(bad.style.color).toBe('var(--status-red-text)')
    expect(bad.style.background).toBe('var(--status-red-bg)')
    expect(bad.style.border).toBe('1px solid var(--status-red-border)')
    expect(screen.getByText('TIN MISSING').style.color, 'a missing TIN reads red').toBe('var(--status-red-text)')
    expect(screen.getByText('30000000-0001').style.color, 'a valid TIN reads muted').toBe('var(--fg-3)')
  })

  it('CU-07 the truncation notice keeps its spacing and 6px corner (D-4) and the grid is untouched', async () => {
    pagedFetchMock(2500, readyRows())
    render(<CustomersView ctx={custCtx()} />)
    const notice = await screen.findByTestId('customers-truncated-notice')

    expect(notice.style.padding).toBe('12px 14px')
    expect(notice.style.marginBottom).toBe('16px')
    expect(notice.style.borderRadius).toBe('var(--radius-md)')
    const head = screen.getByText('Tax status').parentElement as HTMLElement
    expect(head.style.gridTemplateColumns).toBe('minmax(120px, 1.6fr) 140px 70px 140px 104px')
    expect(head.style.padding).toBe('11px 18px')
  })

  it('CU-08 loading and error render the shared state, never the register', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    const loading = render(<CustomersView ctx={custCtx()} />)
    expect(await screen.findByText('Loading customers…')).toBeTruthy()
    expect(screen.queryByText('Customer')).toBeNull()
    expect(screen.queryByText('No customers yet')).toBeNull()
    expect(screen.getByRole('heading', { level: 1, name: 'Customers & vendors' }), 'control: the header still renders').toBeTruthy()
    loading.unmount()

    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 503, json: () => Promise.resolve({ error: 'down' }) })))
    render(<CustomersView ctx={custCtx()} />)
    expect(await screen.findByText('Something went wrong')).toBeTruthy()
    expect(screen.getByRole('button', { name: /retry/i })).toBeTruthy()
    expect(screen.queryByText('Customer')).toBeNull()
    expect(screen.queryByText('No customers yet')).toBeNull()
  })

  it('CU-05 the register carries no v1 vocabulary (boundary)', async () => {
    pagedFetchMock(2500, readyRows())
    const { container } = render(<CustomersView ctx={custCtx()} />)
    await screen.findByText('Alpha Traders')
    await screen.findByTestId('customers-truncated-notice')

    const html = container.innerHTML
    const radii = Array.from(html.matchAll(/(?<![-\w])border-radius:\s*([^;"]+)/g), (m) => m[1].trim())
    expect(radii.length).toBeGreaterThan(0)
    expect(radii.filter((r) => r === 'var(--radius-sm)').length, 'control: the chips carry --radius-sm').toBeGreaterThanOrEqual(2)
    for (const r of radii) expect(['var(--radius-sm)', 'var(--radius-md)', '50%'], `corner "${r}"`).toContain(r)
    for (const needle of ['999px', '99px', 'oklch', 'gradient', 'box-shadow']) expect(html, needle).not.toContain(needle)
  })
})
