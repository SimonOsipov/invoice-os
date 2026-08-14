// @vitest-environment jsdom
// Mode A RED specs (task-335, BUG-01-09) -- ReportsView still calls listInvoices un-paged
// (server default limit 50), so "Invoices in period"/"Top customers by value" undercount
// past 50 rows, same defect BUG-01-08 fixed on Customers. Structural assertions (KPI tile /
// top-customers card layout) are pinned to ReportsView.tsx's CURRENT markup, which AC #5
// keeps unchanged -- only the data source + a truncation notice + fresh-gating are added.
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import { AGGREGATE_MAX_PAGES, AGGREGATE_PAGE_SIZE } from '../lib/invoices'
import type { InvoiceListResponse, InvoiceRecord } from '../lib/invoices'
import type { Counts, Metrics, Rollup, RollupClient } from '../lib/dashboard'
import type { PlatformCtx } from '../types'
import { EXPORTS_BLOCKED_REASON, EXPORTS_BLOCKED_REASON_ID, ReportsView } from './ReportsView'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

// ReportsView also fires getRollup (GET .../rollup) for the Validation summary card, a
// second, independent async ladder ([dashboard-scope-per-client]) -- its own effect can run
// (and its fetch land) interleaved with fetchAllInvoices' own page 1/2/... calls, so a plain
// FIFO response queue would attach the wrong body to the wrong request. Routed by pathname
// instead: the rollup call always gets mockFetch's `rollup` argument (ZERO_ROLLUP unless a
// test supplies one), and only invoices calls draw from the queue -- keeping the queue's
// order exactly page 1, page 2, ... regardless of hook timing.
const ZERO_ROLLUP: Rollup = {
  totals: {
    counts: { draft: 0, validated: 0, queued: 0, submitted: 0, accepted: 0, rejected: 0, failed: 0 },
    needs_attention: 0,
    awaiting_approval: 0,
    metrics: {},
    top_violations: [],
  },
  clients: [],
  top_violations: [],
}

function isRollupUrl(url: string): boolean {
  return new URL(url).pathname.endsWith('/rollup')
}

function mockFetch(invoiceResponses: MockResponse[], rollup: Rollup = ZERO_ROLLUP) {
  const queue = [...invoiceResponses]
  const rollupResponse: MockResponse = { ok: true, status: 200, json: () => Promise.resolve(rollup) }
  const fetchMock = vi.fn((url: string) => {
    if (isRollupUrl(url)) return Promise.resolve(rollupResponse)
    const next = queue.shift()
    if (!next) throw new Error(`ReportsView test: unexpected extra invoices fetch ${url}`)
    return Promise.resolve(next)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// The invoices-only call URLs, in request order -- excludes the interleaved rollup call.
function invoiceCallUrls(fetchMock: ReturnType<typeof mockFetch>): string[] {
  return fetchMock.mock.calls.map((c) => c[0] as string).filter((url) => !isRollupUrl(url))
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

function reportsCtx(entityId = 'ent-1'): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { name: 'Acme Co', short: 'Acme', entityId },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    openCreate: () => {},
  }
  return ctx as unknown as PlatformCtx
}

// The KPI tile's own markup is unchanged by this fix (AC #5) -- `<div className="label">`
// holding the tile's label, sibling to the `.money` value span one level up.
function kpiValue(container: HTMLElement, label: string): string | undefined {
  const labelEl = Array.from(container.querySelectorAll('div.label')).find((d) => d.textContent === label)
  const tile = labelEl?.parentElement?.parentElement
  return tile?.querySelector('.money')?.textContent ?? undefined
}

// Top-customers card markup is also unchanged (AC #5): a `.card-title` header plus one row
// per buyer, each a bare name span and a `.money` total span. Excludes the title itself so
// index 0 is always the top-ranked buyer.
function topCustomerNames(container: HTMLElement): string[] {
  const title = Array.from(container.querySelectorAll('span.card-title')).find((s) => s.textContent === 'Top customers by value')
  const card = title?.parentElement?.parentElement
  if (!card) return []
  return Array.from(card.querySelectorAll('span'))
    .filter((s) => !s.classList.contains('money') && !s.classList.contains('card-title'))
    .map((s) => s.textContent ?? '')
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
  return mockFetch(responses)
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('ReportsView: whole-set fetch via fetchAllInvoices (task-335, BUG-01-09, AC #1/#2)', () => {
  it('fetches both pages of a 259-row set -- not a single un-paged call -- and "Invoices in period" reflects the whole set', async () => {
    const TOTAL = 259
    const page1 = Array.from({ length: AGGREGATE_PAGE_SIZE }, (_, i) => row({ id: `inv-${i}`, buyer_tin: tinFor(i), buyer_name: `Buyer ${i}` }))
    const page2 = Array.from({ length: TOTAL - AGGREGATE_PAGE_SIZE }, (_, i) => {
      const idx = AGGREGATE_PAGE_SIZE + i
      return row({ id: `inv-${idx}`, buyer_tin: tinFor(idx), buyer_name: `Buyer ${idx}` })
    })
    const fetchMock = mockFetch([
      listResponse(page1, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: TOTAL }),
      listResponse(page2, { limit: AGGREGATE_PAGE_SIZE, offset: AGGREGATE_PAGE_SIZE, total: TOTAL }),
    ])

    const { container } = render(<ReportsView ctx={reportsCtx()} />)
    // Every buyer here ties on `total` (row()'s default), so the top-5 panel alone can't
    // prove page 2 was fetched -- wait on the KPI grid settling instead, then assert the
    // requests and the "Invoices in period" tile directly.
    await screen.findByText('Invoices in period')

    const invoiceUrls = invoiceCallUrls(fetchMock)
    expect(invoiceUrls, 'the whole-set fetch must span both pages, not stop at page 1').toHaveLength(2)
    const [firstUrl, secondUrl] = invoiceUrls
    expect(urlParams(firstUrl).get('limit')).toBe(String(AGGREGATE_PAGE_SIZE))
    expect(urlParams(firstUrl).get('offset')).toBe('0')
    expect(urlParams(secondUrl).get('limit')).toBe(String(AGGREGATE_PAGE_SIZE))
    expect(urlParams(secondUrl).get('offset')).toBe(String(AGGREGATE_PAGE_SIZE))
    expect(urlParams(firstUrl).get('entity_id'), 'page 1 must be scoped to the active entity').toBe('ent-1')
    expect(urlParams(secondUrl).get('entity_id'), 'page 2 must also be scoped to the active entity, not tenant-wide').toBe('ent-1')

    // The KPI tile, not just the fetch: a naive un-paged call would read 50 here.
    expect(kpiValue(container, 'Invoices in period'), 'must equal the whole set, not the server default page').toBe(String(TOTAL))
  })
})

describe('ReportsView: top customers ranks over the whole set (task-335, BUG-01-09, AC #3)', () => {
  it('a buyer that only exists on page 2 outranks a page-1-only bucket', async () => {
    const TOTAL = AGGREGATE_PAGE_SIZE + 1
    // One shared buyer across every page-1 row -- aggregateCustomers collapses them into a
    // single bucket totalling AGGREGATE_PAGE_SIZE x 100 = 20,000.
    const page1 = Array.from({ length: AGGREGATE_PAGE_SIZE }, (_, i) => row({ id: `bulk-${i}`, buyer_tin: '20000000-0001', buyer_name: 'Bulk Buyer', total: '100.00' }))
    // A single page-2 row whose own total (99,999) exceeds that whole bucket -- only
    // reachable in the ranking once the whole set, not just page 1, feeds aggregateCustomers.
    const page2 = [row({ id: 'big', buyer_tin: '30000000-0001', buyer_name: 'Big Buyer', total: '99999.00' })]
    mockFetch([
      listResponse(page1, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: TOTAL }),
      listResponse(page2, { limit: AGGREGATE_PAGE_SIZE, offset: AGGREGATE_PAGE_SIZE, total: TOTAL }),
    ])

    const { container } = render(<ReportsView ctx={reportsCtx()} />)
    await screen.findByText('Bulk Buyer')

    expect(topCustomerNames(container)[0], 'a page-1-only fetch would head the list with the bulk bucket instead').toBe('Big Buyer')
  })
})

describe('ReportsView: truncation disclosure (task-335, BUG-01-09, [aggregate-cap-with-disclosure])', () => {
  it('a capped fetch discloses fetched vs. total', async () => {
    const firstPage = [row({ id: 'inv-a', buyer_tin: tinFor(1), buyer_name: 'Alpha Traders' })]
    // 2500 > AGGREGATE_MAX_PAGES(10) x AGGREGATE_PAGE_SIZE(200) -> truncated. fetched = 1
    // page-1 row + 9 one-row filler pages (pagedFetchMock) = 10.
    pagedFetchMock(2500, firstPage)

    render(<ReportsView ctx={reportsCtx()} />)
    await screen.findByText('Alpha Traders')

    // New testid contract -- no [aggregate-cap-with-disclosure] UI consumer exists yet on
    // this screen.
    const notice = await screen.findByTestId('reports-truncated-notice')
    expect(notice.textContent, 'must name what was actually fetched').toContain('10')
    expect(notice.textContent, 'must name the true set total').toContain('2500')
  })

  it('an un-truncated fetch discloses nothing', async () => {
    const rows = [row({ id: 'inv-c', buyer_tin: tinFor(3), buyer_name: 'Gamma Traders' })]
    mockFetch([listResponse(rows, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 1 })])

    render(<ReportsView ctx={reportsCtx()} />)
    await screen.findByText('Gamma Traders')

    expect(screen.queryByTestId('reports-truncated-notice')).toBeNull()
  })
})

// Not behaviorally distinguishable at the render layer (same trap CustomersView.test.tsx's
// own equivalent check documents): AllInvoices.invoices is the whole-set concatenation, so
// `invoices.length === 0` iff `total === 0` whenever the fetch behaves normally -- an inline
// arrow of the same shape would pass every render test above unchanged. A source scan is the
// only oracle for the SHARED-predicate requirement ([empty-is-total-zero]) itself.
describe('ReportsView: isEmpty is the shared allInvoicesIsEmpty predicate (task-335, BUG-01-09, [empty-is-total-zero])', () => {
  it('passes allInvoicesIsEmpty by reference, not a local arrow', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/components/ReportsView.tsx'), 'utf8')
    expect(src).toMatch(/isEmpty:\s*allInvoicesIsEmpty\b/)
  })
})

describe('ReportsView: entity switch settles on the new client (task-335, BUG-01-09)', () => {
  it('never sticks on the previous client\'s numbers, and the refetch is scoped + page-shaped', async () => {
    // jsdom's act() batching flushes straight through the one-commit transient where
    // list.data still belongs to the OLD entity while ctx.active.entityId already reads the
    // new one (same limitation CustomersView.test.tsx's own entity-switch test documents) --
    // this cannot prove no frame ever paints the old total/truncated numbers. It proves what
    // jsdom CAN see: the screen settles on the new entity, never stuck on the old one, via a
    // refetch that is both scoped and page-shaped correctly.
    const entA = [row({ id: 'a1', entity_id: 'ent-1', buyer_tin: tinFor(1), buyer_name: 'Entity A Buyer' })]
    const entB = [row({ id: 'b1', entity_id: 'ent-2', buyer_tin: tinFor(2), buyer_name: 'Entity B Buyer' })]
    const fetchMock = mockFetch([
      listResponse(entA, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 1 }),
      listResponse(entB, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 1 }),
    ])

    const { rerender } = render(<ReportsView ctx={reportsCtx('ent-1')} />)
    await screen.findByText('Entity A Buyer')

    rerender(<ReportsView ctx={reportsCtx('ent-2')} />)

    await screen.findByText('Entity B Buyer')
    expect(screen.queryByText('Entity A Buyer'), 'must settle on the new entity, never stay stuck on the old rows').toBeNull()

    const [, secondUrl] = invoiceCallUrls(fetchMock)
    expect(urlParams(secondUrl).get('entity_id'), 'the switch-triggered refetch must be scoped to the new entity').toBe('ent-2')
    expect(urlParams(secondUrl).get('limit'), 'the refetch must go through the aggregate page shape too, not just the mount fetch').toBe(String(AGGREGATE_PAGE_SIZE))
  })
})

// QA (task-335, BUG-01-09, Mode B): `fresh` must gate all three list-half render branches
// (loading-insert, empty, populated+truncation), not just the row array gateByActiveEntity
// already filters -- same bug class BUG-01-03 cost two fix cycles, same posture as
// CustomersView.test.tsx's own [customers-fresh-gate] guard (which only covers its populated
// branch; extended here to all three since mutation-testing shows each is independently
// silent). Mutation-verified: dropping `&& fresh` (or deleting the loading-insert line)
// leaves every render test above green -- the one-commit stale-envelope transient is
// unobservable under jsdom (React flushes the passive effect that nulls `list.data`
// synchronously inside the same act() as the prop change), so a source scan is the only
// oracle for this regression class.
describe('ReportsView: `fresh` gates every list-half render branch (task-335, [reports-fresh-gate])', () => {
  const src = () => readFileSync(path.join(process.cwd(), 'src/components/ReportsView.tsx'), 'utf8')

  it('the loading-insert branch requires `!fresh` alongside `list.data != null`', () => {
    expect(src()).toMatch(/state === 'ready' && list\.data != null && !fresh && <Loading/)
  })

  it('the empty-state branch requires `fresh` alongside `list.data != null`', () => {
    expect(src()).toMatch(/state === 'ready' && list\.data != null && fresh && rows\.length === 0/)
  })

  it('the populated/truncation branch requires `fresh` alongside `list.data != null`', () => {
    expect(src()).toMatch(/state === 'ready' && list\.data != null && fresh && rows\.length > 0 && \(/)
  })
})

// Pinned literal, not EXPORTS_LIST -- these tests must catch an unpinned rename, not just
// echo whatever data.tsx currently says.
const EXPORT_BUTTONS: { name: string; fmt: string }[] = [
  { name: 'VAT return', fmt: 'CSV' },
  { name: 'Audit log', fmt: 'PDF' },
  { name: 'Invoice register', fmt: 'XLSX' },
  { name: 'WHT schedule', fmt: 'CSV' },
]

describe('ReportsView: export buttons are disabled-with-reason', () => {
  // The export card only renders on the populated branch (`fresh && rows.length > 0`).
  async function renderReady() {
    const rows = [row({ id: 'inv-r', buyer_tin: tinFor(1), buyer_name: 'Ready Buyer' })]
    mockFetch([listResponse(rows, { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 1 })])
    render(<ReportsView ctx={reportsCtx()} />)
    await screen.findByText('Ready Buyer')
  }

  it('all four export buttons carry the disabled attribute (AC #1)', async () => {
    await renderReady()
    for (const { name } of EXPORT_BUTTONS) {
      const btn = screen.getByRole('button', { name: new RegExp(name) }) as HTMLButtonElement
      expect(btn.disabled, `${name} button must be disabled`).toBe(true)
    }
  })

  // QA adversarial (mutation survivor): the disabled-attribute test above passes even if
  // the inline mute is stripped entirely -- AC #1 also requires background/color/cursor,
  // and nothing else in this file checks them.
  it('all four export buttons carry the inline mute style (AC #1)', async () => {
    await renderReady()
    for (const { name } of EXPORT_BUTTONS) {
      const btn = screen.getByRole('button', { name: new RegExp(name) }) as HTMLButtonElement
      expect(btn.style.background, `${name} button background`).toBe('var(--bg-3)')
      expect(btn.style.color, `${name} button color`).toBe('var(--fg-4)')
      expect(btn.style.cursor, `${name} button cursor`).toBe('not-allowed')
    }
  })

  it('exactly one reason element renders, carrying the pinned copy verbatim (AC #2)', async () => {
    await renderReady()
    const reasons = screen.queryAllByTestId('exports-blocked-reason')
    expect(reasons).toHaveLength(1)
    expect(reasons[0].id).toBe(EXPORTS_BLOCKED_REASON_ID)
    expect(reasons[0].textContent).toBe(EXPORTS_BLOCKED_REASON)
  })

  it("every button's aria-describedby and title resolve to the one reason element (AC #2)", async () => {
    await renderReady()
    const reasonEl = screen.getByTestId('exports-blocked-reason')
    for (const { name } of EXPORT_BUTTONS) {
      const btn = screen.getByRole('button', { name: new RegExp(name) })
      expect(btn.getAttribute('aria-describedby'), `${name} button`).toBe(reasonEl.id)
      expect(btn.getAttribute('title'), `${name} button`).toBe(EXPORTS_BLOCKED_REASON)
    }
  })

  it('a disabled export button refuses focus and emits no click (AC #3)', async () => {
    await renderReady()
    for (const { name } of EXPORT_BUTTONS) {
      const btn = screen.getByRole('button', { name: new RegExp(name) })
      btn.focus()
      expect(document.activeElement, `${name} button must refuse focus while disabled`).not.toBe(btn)
      fireEvent.click(btn)
      expect(document.activeElement, `${name} button must not gain focus from a click while disabled`).not.toBe(btn)
    }
  })

  it('the four export names and format chips are unchanged (AC #4)', async () => {
    await renderReady()
    for (const { name, fmt } of EXPORT_BUTTONS) {
      const btn = screen.getByRole('button', { name: new RegExp(name) })
      expect(btn.textContent, `${name} button must still show its ${fmt} chip`).toContain(fmt)
    }
  })

  it('the pinned reason copy contains no promissory language (AC #6)', () => {
    expect(EXPORTS_BLOCKED_REASON).not.toMatch(/coming soon|will be|shortly|roadmap/i)
  })
})

// Mode A RED specs. Every render above routes the rollup through ZERO_ROLLUP, which reads 0
// under BOTH the old `needs_attention` source and the new `metrics.blocked_by_rules` one --
// so nothing in this file could tell them apart before these three.
const SUMMARY_COUNTS: Counts = { draft: 3, validated: 4, queued: 1, submitted: 1, accepted: 2, rejected: 1, failed: 1 } // 13

// reportsCtx() selects 'ent-1' in firm mode, so scopedBucket resolves to this `clients` row,
// not to `totals`. Both carry the same numbers anyway, so the fixture survives a mode change.
function summaryRollup(needsAttention: number, metrics: Metrics): Rollup {
  const bucket = { counts: SUMMARY_COUNTS, needs_attention: needsAttention, awaiting_approval: 0, metrics, top_violations: [] }
  const client: RollupClient = { entity_id: 'ent-1', entity_name: 'Acme Co', ...bucket }
  return { totals: bucket, clients: [client], top_violations: [] }
}

// One Validation-summary tile: the `.money` value sharing a parent with its `div.label`.
// (Shallower than kpiValue above -- the KPI tiles wrap their label in a flex row, these don't.)
function summaryTile(container: HTMLElement, label: string): string | undefined {
  const labelEl = Array.from(container.querySelectorAll('div.label')).find((d) => d.textContent === label)
  return labelEl?.parentElement?.querySelector('.money')?.textContent ?? undefined
}

// The `% PASS` chip in the card header, beside the "Validation summary" title.
function passPct(container: HTMLElement): string | undefined {
  const title = Array.from(container.querySelectorAll('span.card-title')).find((s) => s.textContent === 'Validation summary')
  return title?.parentElement?.querySelector('span.mono')?.textContent ?? undefined
}

interface SummaryNumbers {
  passed: string | undefined
  failing: string | undefined
  pct: string | undefined
}

async function renderSummary(rollup: Rollup): Promise<SummaryNumbers> {
  mockFetch([listResponse([row({ id: 'inv-s', buyer_tin: tinFor(1), buyer_name: 'Summary Buyer' })], { limit: AGGREGATE_PAGE_SIZE, offset: 0, total: 1 })], rollup)
  const { container } = render(<ReportsView ctx={reportsCtx()} />)
  // Waits on the card's own ready branch, not just the KPI grid -- the rollup is a second,
  // independent async ladder and settles on its own schedule.
  await screen.findByText('Passed')
  return { passed: summaryTile(container, 'Passed'), failing: summaryTile(container, 'Failing'), pct: passPct(container) }
}

describe('ReportsView: the Validation summary reads blocked_by_rules, not needs_attention (AC-6, D-33/D-37)', () => {
  it('a bucket whose overlay and violation count differ renders the violation count', async () => {
    // The two sources are deliberately different non-zero numbers: 2 (blocked_by_rules) vs 7
    // (the widened overlay). Old source renders 7 / 6 / 46% PASS, new renders 2 / 11 / 85%.
    const summary = await renderSummary(summaryRollup(7, { blocked_by_rules: { num: 2, den: 13 } }))

    expect(summary.failing, 'Failing must be the violation-derived count, not the overlay').toBe('2')
    expect(summary.passed, 'Passed is bucketTotal - repFail (D-37: rejections and transmission failures land here)').toBe('11')
    expect(summary.pct, '11 of 13').toBe('85% PASS')
  })

  it('a widened needs_attention does not move any of the three numbers', async () => {
    const metrics: Metrics = { blocked_by_rules: { num: 2, den: 13 } }
    const narrow = await renderSummary(summaryRollup(3, metrics))
    cleanup()
    vi.unstubAllGlobals()
    const widened = await renderSummary(summaryRollup(9, metrics))

    expect(widened, 'the overlay is the only thing that changed between these two renders').toEqual(narrow)
    // Non-vacuity: an equality over three undefineds would otherwise pass on a card that
    // never rendered.
    expect(narrow).toEqual({ passed: '11', failing: '2', pct: '85% PASS' })
  })

  it('an absent blocked_by_rules metric reads zero failing, not the overlay and not a crash', async () => {
    // EMPTY_BUCKET's shape. metricCount returns null for an absent key, so this pins the ?? 0.
    const summary = await renderSummary(summaryRollup(7, {}))

    expect(summary.failing).toBe('0')
    expect(summary.passed).toBe('13')
    expect(summary.pct).toBe('100% PASS')
  })
})
