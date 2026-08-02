// @vitest-environment jsdom
// QA Stage 4 (task-329, BUG-01-03) — component-level coverage for the register's own
// pagination. invoices.test.ts's RED specs (Mode A) pin the pure helpers
// (listInvoices/invoiceListIsEmpty/selectableIds); nothing before this file rendered
// InvoicesList itself, so the wiring between those helpers and the DOM (does Next
// actually reach the last page, does select-all stay page-scoped, does the poll tick
// really carry the current offset) was unverified. The poll-tick call-shape source-scan
// lives in InvoicesList.pollShape.test.ts (node env) -- jsdom rewrites import.meta.url
// off file: scheme, breaking readFileSync(fileURLToPath(...)) here.
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { InvoiceListResponse, InvoiceRecord } from '../lib/invoices'
import type { PlatformCtx } from '../types'
import { InvoicesList } from './InvoicesList'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

// Queues one response per call, in order — unlike invoices.test.ts's mockFetchOnce
// (same value every call), these tests need to distinguish page 1 from page 2 from a
// poll tick.
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
    issue_date: '2026-07-01T00:00:00Z',
    supplier_tin: '00000000001',
    supplier_name: 'Acme Ltd',
    buyer_tin: '00000000002',
    buyer_name: 'Beta Ltd',
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
    rule_set_version: null,
    ...over,
  }
}

// InvoicesList reads exactly these ctx fields (grep-confirmed) — same narrowing idiom
// as Header.test.tsx's HeaderCtx. invoiceQuery (task-331, BUG-01-05) defaults to '' so
// every pre-existing call site (unfiltered register) is unaffected.
function listCtx(entityId = 'ent-1', invoiceQuery = ''): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    openCreate: () => {},
    openImportedInvoice: () => {},
    invoiceQuery,
  }
  return ctx as unknown as PlatformCtx
}

function urlParams(url: string): URLSearchParams {
  return new URL(url).searchParams
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('InvoicesList pagination (task-329, BUG-01-03)', () => {
  it('AC-1/2/3: the pager reads the RESPONSE\'s own pagination, limit=50 is on the wire, and Next reaches the last page', async () => {
    const page1 = Array.from({ length: 50 }, (_, i) => row({ id: `inv-${i}`, invoice_number: `INV-${i}` }))
    const anchor = row({ id: 'inv-anchor', invoice_number: 'INV-ANCHOR' })
    const fetchMock = mockFetchSequence([
      listResponse(page1, { limit: 50, offset: 0, total: 51 }),
      listResponse([anchor], { limit: 50, offset: 50, total: 51 }),
    ])

    render(<InvoicesList ctx={listCtx()} />)

    const pager = await screen.findByTestId('invoices-pager')
    expect(pager.textContent).toContain('SHOWING 1–50 OF 51')
    expect(pager.textContent).toContain('PAGE 1 / 2')
    expect(screen.queryByText('INV-ANCHOR')).toBeNull()

    const [firstUrl] = fetchMock.mock.calls[0] as [string]
    expect(urlParams(firstUrl).get('limit')).toBe('50')
    expect(urlParams(firstUrl).get('offset')).toBe('0')

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))
    await screen.findByText('INV-ANCHOR')

    expect(screen.getByTestId('invoices-pager').textContent).toContain('SHOWING 51–51 OF 51')
    expect(screen.getByTestId('invoices-pager').textContent).toContain('PAGE 2 / 2')

    const [secondUrl] = fetchMock.mock.calls[1] as [string]
    expect(urlParams(secondUrl).get('limit')).toBe('50')
    expect(urlParams(secondUrl).get('offset')).toBe('50')
  })

  it('AC-4/5: select-all is page-scoped, and paging clears the selection', async () => {
    const p1 = [
      row({ id: 'v1', invoice_number: 'INV-V1', status: 'validated' }),
      row({ id: 'v2', invoice_number: 'INV-V2', status: 'validated' }),
      row({ id: 'v3', invoice_number: 'INV-V3', status: 'validated' }),
      row({ id: 'd1', invoice_number: 'INV-D1', status: 'draft' }),
      row({ id: 'd2', invoice_number: 'INV-D2', status: 'draft' }),
    ]
    const p2 = [row({ id: 'v-p2', invoice_number: 'INV-V-P2', status: 'validated' })]
    mockFetchSequence([listResponse(p1, { limit: 50, offset: 0, total: 60 }), listResponse(p2, { limit: 50, offset: 50, total: 60 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByTestId('invoices-list')

    fireEvent.click(screen.getByTestId('invoice-select-all'))
    const summary = await screen.findByTestId('batch-submit-summary')
    expect(summary.textContent).toContain('3 selected on this page')
    // Only the 3 validated checkboxes are checked -- draft rows stay unselected, not
    // silently swept into a naive "all rows" select-all.
    expect((screen.getByLabelText('Select invoice INV-D1') as HTMLInputElement).checked).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))
    await screen.findByText('INV-V-P2')

    expect(screen.queryByTestId('batch-submit-summary')).toBeNull()
  })

  it('AC-5 (adversarial): paging clears the selection explicitly, not only via pruneSelection dropping departed ids', async () => {
    // The SAME id reappears, still validated, on page 2 (a row can shift pages between
    // two fetches on a live register). pruneSelection alone would keep it selected --
    // only an EXPLICIT clear in the paging handler (not the [rows]-keyed prune effect)
    // makes the bar disappear here.
    const p1 = [row({ id: 'dup', invoice_number: 'INV-DUP', status: 'validated' })]
    const p2 = [row({ id: 'dup', invoice_number: 'INV-DUP', status: 'validated' })]
    mockFetchSequence([listResponse(p1, { limit: 50, offset: 0, total: 60 }), listResponse(p2, { limit: 50, offset: 50, total: 60 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-DUP')

    fireEvent.click(screen.getByLabelText('Select invoice INV-DUP'))
    await screen.findByTestId('batch-submit-summary')

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))
    await waitFor(() => expect(screen.getByTestId('invoices-pager').textContent).toContain('PAGE 2 / 2'))

    expect(screen.queryByTestId('batch-submit-summary'), 'must clear even though "dup" survives pruneSelection on the new page').toBeNull()
  })

  it('AC-5: toggling Needs-attention resets the offset to 0 and clears the selection', async () => {
    const p1 = [row({ id: 'v1', invoice_number: 'INV-V1', status: 'validated' })]
    const p2 = [row({ id: 'v-p2', invoice_number: 'INV-V-P2', status: 'validated' })]
    const filtered = [row({ id: 'f1', invoice_number: 'INV-F1', status: 'validated' })]
    const fetchMock = mockFetchSequence([
      listResponse(p1, { limit: 50, offset: 0, total: 60 }),
      listResponse(p2, { limit: 50, offset: 50, total: 60 }),
      listResponse(filtered, { limit: 50, offset: 0, total: 1 }),
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-V1')

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))
    await screen.findByText('INV-V-P2')

    fireEvent.click(screen.getByLabelText('Select invoice INV-V-P2'))
    const summary = await screen.findByTestId('batch-submit-summary')
    expect(summary.textContent).toContain('1 selected on this page')

    fireEvent.click(screen.getByTestId('needs-attention-toggle'))
    await screen.findByText('INV-F1')

    const [toggleUrl] = fetchMock.mock.calls[2] as [string]
    expect(urlParams(toggleUrl).get('needs_attention')).toBe('true')
    expect(urlParams(toggleUrl).get('offset'), 'the toggle must not leave the page-2 offset behind').toBe('0')
    expect(screen.queryByTestId('batch-submit-summary')).toBeNull()
  })

  it('AC-7: a zero-total entity renders the honest empty state, never the page-empty one', async () => {
    mockFetchSequence([listResponse([], { limit: 50, offset: 0, total: 0 })])

    render(<InvoicesList ctx={listCtx()} />)

    const empty = await screen.findByTestId('invoices-empty')
    expect(empty.textContent).toContain('No invoices yet')
    expect(screen.queryByTestId('invoices-empty-page')).toBeNull()
    expect(screen.queryByTestId('invoices-pager')).toBeNull()
  })

  it('AC-7: a mid-set empty page (total>0, this page []) renders the page-empty state with the pager still usable', async () => {
    const p1 = Array.from({ length: 50 }, (_, i) => row({ id: `inv-${i}`, invoice_number: `INV-${i}` }))
    // total dropped between the two fetches (a concurrent delete/filter) -- the
    // documented [last<first] edge case (reviewBatch.ts pagerLabels), reachable
    // without a client-side bug.
    mockFetchSequence([listResponse(p1, { limit: 50, offset: 0, total: 110 }), listResponse([], { limit: 50, offset: 50, total: 45 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByTestId('invoices-pager')

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))

    const pageEmpty = await screen.findByTestId('invoices-empty-page')
    expect(pageEmpty.textContent).toContain('No invoices on this page')
    expect(screen.queryByTestId('invoices-empty')).toBeNull()

    const pager = screen.getByTestId('invoices-pager')
    expect(pager.textContent).toContain('SHOWING 0 OF 45')
    expect((screen.getByRole('button', { name: '← Previous' }) as HTMLButtonElement).disabled, 'Previous must stay usable so the user can get back').toBe(false)
  })

  it('AC-6: the live-refresh tick re-sends the CURRENT offset, not page 1 ([poll-tick-follows-the-page])', async () => {
    // status:'queued' keeps shouldPollList active so useLiveRefresh actually installs
    // an interval; a real 2s wait avoids fake-timer/act() interaction pitfalls with
    // RTL's async utilities.
    const p1 = [row({ id: 'q0', invoice_number: 'INV-Q0', status: 'queued' })]
    const p2 = [row({ id: 'q50', invoice_number: 'INV-Q50', status: 'queued' })]
    const fetchMock = mockFetchSequence([
      listResponse(p1, { limit: 50, offset: 0, total: 60 }),
      listResponse(p2, { limit: 50, offset: 50, total: 60 }),
      listResponse(p2, { limit: 50, offset: 50, total: 60 }), // the poll tick itself
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-Q0')

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))
    await screen.findByText('INV-Q50')

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3), { timeout: 3500, interval: 100 })

    const [tickUrl] = fetchMock.mock.calls[2] as [string]
    expect(urlParams(tickUrl).get('limit')).toBe('50')
    expect(urlParams(tickUrl).get('offset'), 'the poll tick must follow the visible page, not silently re-install page 1').toBe('50')
  }, 8000)

  it('fix (bug-01-03 cycle 2): a poll tick that empties the live overlay lands on the page-empty state with a working Pager, not a stranding spinner', async () => {
    // On page 2 (offset 50, unlike the f3b2b54 regression's offset-0 repro, so this
    // scenario can actually assert Previous stays usable), both rows are in-flight ->
    // shouldPollList is active. Between one poll tick and the next, BOTH resolve -- the
    // tick's own response is genuinely [] for the SAME query, same entity, no company
    // switch. f3b2b54 used list.data.invoices.length>0 && rows.length===0 (a row-count
    // proxy) to detect a switch-transient, which routed this identical signature into a
    // bare <Loading> with polling torn down and no refetch ever scheduled. The fix reads
    // the envelope's own fetchedEntityId instead: `list.data` (last set by the page-2
    // fetch, never touched by a poll tick -- ticks only ever write `live`) still belongs
    // to the active entity, so this lands in the fresh-and-empty branch, same as a
    // genuine mid-set-empty page. `list.data.pagination` is untouched by the tick too
    // (the tick reads only `r.invoices`), so the Pager still reads page 2's own numbers.
    const p1 = Array.from({ length: 50 }, (_, i) => row({ id: `inv-${i}`, invoice_number: `INV-${i}` }))
    const p2 = [
      row({ id: 'q0', invoice_number: 'INV-Q0', status: 'queued' }),
      row({ id: 'q1', invoice_number: 'INV-Q1', status: 'submitted' }),
    ]
    const fetchMock = mockFetchSequence([
      listResponse(p1, { limit: 50, offset: 0, total: 52 }),
      listResponse(p2, { limit: 50, offset: 50, total: 52 }),
      listResponse([], { limit: 50, offset: 50, total: 52 }), // the poll tick itself
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByTestId('invoices-pager')

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))
    await screen.findByText('INV-Q0')

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3), { timeout: 3500, interval: 100 })

    const pageEmpty = await screen.findByTestId('invoices-empty-page')
    expect(pageEmpty.textContent).toContain('No invoices on this page')
    expect(screen.queryByTestId('invoices-list')).toBeNull()

    // The Pager survives, fed page 2's own pagination, and Previous stays usable -- the
    // escape hatch f3b2b54's bare spinner had removed.
    const pager = screen.getByTestId('invoices-pager')
    expect(pager.textContent).toContain('SHOWING 51–52 OF 52')
    expect((screen.getByRole('button', { name: '← Previous' }) as HTMLButtonElement).disabled, 'Previous stays usable so the user can page back').toBe(false)
  }, 8000)

  it('QA (bug-01-03 cycle 3): switching the active entity settles on the NEW entity\'s rows, never sticks on Loading', async () => {
    // d35a692's `fresh` check is keyed on identity (fetchedEntityId), not row count or
    // content shape -- unlike f3b2b54, it can't get confused by which entity's envelope
    // happens to be empty. This can't observe the one-commit transient itself (jsdom's
    // act() flushes straight through it, same as every other switch-transient claim in
    // this file), but it DOES prove the thing that actually matters: the switch settles
    // on entity B's own rows, not stuck showing entity A's or a permanent spinner.
    const entA = [row({ id: 'a1', invoice_number: 'INV-ENT-A', entity_id: 'ent-1' })]
    const entB = [row({ id: 'b1', invoice_number: 'INV-ENT-B', entity_id: 'ent-2' })]
    const fetchMock = mockFetchSequence([listResponse(entA, { limit: 50, offset: 0, total: 1 }), listResponse(entB, { limit: 50, offset: 0, total: 1 })])

    const { rerender } = render(<InvoicesList ctx={listCtx('ent-1')} />)
    await screen.findByText('INV-ENT-A')

    rerender(<InvoicesList ctx={listCtx('ent-2')} />)

    await screen.findByText('INV-ENT-B')
    expect(screen.queryByText('INV-ENT-A')).toBeNull()

    const [, secondCallUrl] = fetchMock.mock.calls.map((c) => c[0] as string)
    expect(urlParams(secondCallUrl).get('entity_id'), 'the refetch triggered by the switch must be scoped to the NEW entity').toBe('ent-2')
  })

  it('(c) two Next clicks batched into the same commit fire exactly one navigation, not two skipped-ahead ones', async () => {
    const p1 = Array.from({ length: 50 }, (_, i) => row({ id: `inv-${i}`, invoice_number: `INV-${i}` }))
    const p2 = [row({ id: 'p2', invoice_number: 'INV-P2' })]
    const fetchMock = mockFetchSequence([listResponse(p1, { limit: 50, offset: 0, total: 110 }), listResponse(p2, { limit: 50, offset: 50, total: 110 })])

    render(<InvoicesList ctx={listCtx()} />)
    const nextBtn = await screen.findByRole('button', { name: 'Next →' })

    // Both clicks dispatched inside ONE outer act() -- React 18/19 auto-batching
    // coalesces same-tick updates, which is the actual mechanism (not `busy`) that
    // keeps a fast double-click from firing two requests: both onGo calls read the
    // SAME nav.nextOffset off the not-yet-refetched pagination.
    await act(async () => {
      fireEvent.click(nextBtn)
      fireEvent.click(nextBtn)
    })

    await screen.findByText('INV-P2')

    expect(fetchMock, 'exactly one nav-triggered fetch beyond the initial mount fetch').toHaveBeenCalledTimes(2)
    const [secondUrl] = fetchMock.mock.calls[1] as [string]
    expect(urlParams(secondUrl).get('offset'), 'must land on page 2, never skip to an offset=100 double-advance').toBe('50')
  })
})

// QA (task-331, BUG-01-05) — the header search box wired to this register.
describe('InvoicesList: header search wiring (task-331, BUG-01-05)', () => {
  it('AC-6: a new search term (from page 1) resets offset to 0 (no-op) and clears the selection', async () => {
    // The SAME id, still validated, reappears in the "search" response (mirrors the
    // existing AC-5 (adversarial) test's own technique): on the FINAL rows alone,
    // pruneSelection would not explain a clear. Caveat verified by mutation-testing this
    // scenario: it does NOT fully isolate the query-reset effect's explicit
    // setSelected([]) from the transient list.data=null -> rows=[] window every refetch
    // passes through, which independently clears the selection via the same [rows]-keyed
    // prune effect (the AC-5 (adversarial) sibling above has this identical property).
    // What this DOES verify is the user-observable AC #6 outcome — the selection bar is
    // gone after a new search term — regardless of which mechanism did the clearing.
    const p1 = [row({ id: 'dup', invoice_number: 'INV-DUP', status: 'validated' })]
    const filtered = [row({ id: 'dup', invoice_number: 'INV-DUP', status: 'validated' })]
    const fetchMock = mockFetchSequence([
      listResponse(p1, { limit: 50, offset: 0, total: 1 }),
      listResponse(filtered, { limit: 50, offset: 0, total: 1 }),
    ])

    const { rerender } = render(<InvoicesList ctx={listCtx('ent-1', '')} />)
    await screen.findByText('INV-DUP')

    fireEvent.click(screen.getByLabelText('Select invoice INV-DUP'))
    await screen.findByTestId('batch-submit-summary')

    rerender(<InvoicesList ctx={listCtx('ent-1', 'search-term')} />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))

    // From page 1 the offset-reset effect's setOffset(0) is a no-op (already 0) — exactly
    // one extra fetch, not the two a non-zero-offset search produces (see the (d) test
    // below).
    expect(fetchMock, 'from page 1, the offset reset is a no-op — exactly one extra fetch').toHaveBeenCalledTimes(2)
    const [searchUrl] = fetchMock.mock.calls[1] as [string]
    expect(urlParams(searchUrl).get('offset')).toBe('0')
    expect(urlParams(searchUrl).get('q')).toBe('search-term')
    expect(screen.queryByTestId('batch-submit-summary'), 'a new search term must clear the selection too').toBeNull()
  })

  it('clearing the query (ctx.invoiceQuery -> "") emits NO q param at all — never q=', async () => {
    const filteredRows = [row({ id: 'f1', invoice_number: 'INV-F1' })]
    const allRows = [row({ id: 'a1', invoice_number: 'INV-A1' })]
    const fetchMock = mockFetchSequence([
      listResponse(filteredRows, { limit: 50, offset: 0, total: 1 }),
      listResponse(allRows, { limit: 50, offset: 0, total: 5 }),
    ])

    const { rerender } = render(<InvoicesList ctx={listCtx('ent-1', 'term')} />)
    await screen.findByText('INV-F1')
    expect(urlParams(fetchMock.mock.calls[0][0] as string).get('q')).toBe('term')

    rerender(<InvoicesList ctx={listCtx('ent-1', '')} />)
    await screen.findByText('INV-A1')

    const clearedUrl = fetchMock.mock.calls[1][0] as string
    expect(urlParams(clearedUrl).has('q'), 'clearing must omit q entirely, never send an empty q=').toBe(false)
    expect(clearedUrl).not.toContain('q=')
  })

  it('q composes with needs_attention in a single request, not dropped by the toggle', async () => {
    const initial = [row({ id: 'r1', invoice_number: 'INV-R1' })]
    const combined = [row({ id: 'r2', invoice_number: 'INV-R2' })]
    const fetchMock = mockFetchSequence([
      listResponse(initial, { limit: 50, offset: 0, total: 1 }),
      listResponse(combined, { limit: 50, offset: 0, total: 1 }),
    ])

    render(<InvoicesList ctx={listCtx('ent-1', 'term')} />)
    await screen.findByText('INV-R1')
    expect(urlParams(fetchMock.mock.calls[0][0] as string).get('q')).toBe('term')

    fireEvent.click(screen.getByTestId('needs-attention-toggle'))
    await screen.findByText('INV-R2')

    const url = fetchMock.mock.calls[1][0] as string
    expect(urlParams(url).get('needs_attention')).toBe('true')
    expect(urlParams(url).get('q'), 'toggling needs-attention must not silently drop the active search').toBe('term')
  })

  it('[search-is-server-side]: a row that textually matches nothing in the query still renders — proves no client-side re-filter on top of the server response', async () => {
    // The server already decided this row belongs in the result set; the row's own
    // fields deliberately share no substring with the query. A naive client-side
    // `rows.filter(r => r.invoice_number.includes(q))` on top of the server response
    // would hide it even though the server returned it — asserting the row DOES render
    // is the only way to catch that regression (a "does the URL carry q" test alone
    // can't see it).
    const nonMatching = [
      row({
        id: 'x1',
        invoice_number: 'INV-COMPLETELY-UNRELATED',
        buyer_name: 'Zzz Corp',
        supplier_name: 'Www Ltd',
        buyer_tin: '11111111',
        supplier_tin: '22222222',
      }),
    ]
    mockFetchSequence([listResponse(nonMatching, { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx('ent-1', 'needle-shares-nothing-with-this-row')} />)

    await screen.findByText('INV-COMPLETELY-UNRELATED')
  })

  it('AC-4: while a search is active, the count the user reads is still pagination.total, not the on-screen row count', async () => {
    // Server returns exactly 1 row for this page but reports total=37 (a deliberate
    // mismatch, impossible in a real response but exactly what proves the pager reads
    // the ENVELOPE field, not rows.length) -- if InvoicesList ever derived the count from
    // `rows.length` while a query is active, this would read "OF 1", not "OF 37".
    const oneRow = [row({ id: 'r1', invoice_number: 'INV-R1' })]
    mockFetchSequence([listResponse(oneRow, { limit: 50, offset: 0, total: 37 })])

    render(<InvoicesList ctx={listCtx('ent-1', 'term')} />)
    await screen.findByText('INV-R1')

    expect(screen.getByTestId('invoices-pager').textContent).toContain('OF 37')
  })

  it('(c) the poll tick includes q, matching the currently active search — a dropped q would re-install the unfiltered set every 2s', async () => {
    // status:'queued' keeps shouldPollList active (mirrors the existing AC-6 poll test's
    // own real-timer approach, avoiding fake-timer/act() interaction pitfalls).
    const filtered = [row({ id: 'q0', invoice_number: 'INV-Q0', status: 'queued' })]
    const fetchMock = mockFetchSequence([
      listResponse(filtered, { limit: 50, offset: 0, total: 1 }),
      listResponse(filtered, { limit: 50, offset: 0, total: 1 }), // the poll tick itself
    ])

    render(<InvoicesList ctx={listCtx('ent-1', 'ABC123')} />)
    await screen.findByText('INV-Q0')

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2), { timeout: 3500, interval: 100 })

    const [initialUrl] = fetchMock.mock.calls[0] as [string]
    const [tickUrl] = fetchMock.mock.calls[1] as [string]
    expect(urlParams(initialUrl).get('q')).toBe('ABC123')
    expect(urlParams(tickUrl).get('q'), 'a poll tick that dropped q would re-install the UNFILTERED set over a filtered page every 2s').toBe('ABC123')
  }, 8000)

  it('(d) searching from a non-zero offset: traces both fetches fired, and a stale off-page response arriving AFTER the correct one must not win', async () => {
    const p1 = Array.from({ length: 50 }, (_, i) => row({ id: `inv-${i}`, invoice_number: `INV-${i}` }))
    const p2 = [row({ id: 'p2', invoice_number: 'INV-P2' })]
    const staleAtOffset50 = [row({ id: 'stale-hit', invoice_number: 'INV-STALE-HIT' })]
    const correctAtOffset0 = [row({ id: 'correct-hit', invoice_number: 'INV-CORRECT-HIT' })]

    // Manual promise control (unlike mockFetchSequence's auto-resolve) — needed to force
    // the adversarial resolution ORDER below, which is the actual thing under test.
    const calls: { url: string; resolve: (r: MockResponse) => void }[] = []
    const fetchMock = vi.fn((url: string) => new Promise<MockResponse>((resolve) => calls.push({ url, resolve })))
    vi.stubGlobal('fetch', fetchMock)

    const { rerender } = render(<InvoicesList ctx={listCtx('ent-1', '')} />)

    await waitFor(() => expect(calls).toHaveLength(1))
    calls[0].resolve(listResponse(p1, { limit: 50, offset: 0, total: 60 }))
    await screen.findByText('INV-0')

    fireEvent.click(screen.getByRole('button', { name: 'Next →' }))
    await waitFor(() => expect(calls).toHaveLength(2))
    calls[1].resolve(listResponse(p2, { limit: 50, offset: 50, total: 60 }))
    await screen.findByText('INV-P2')

    // Simulate the header committing a new query while this component is still on page 2
    // (its own `offset` state, 50, is untouched by the ctx update — App.tsx re-renders
    // this component with a new ctx.invoiceQuery via a completely separate piece of
    // state).
    rerender(<InvoicesList ctx={listCtx('ent-1', 'search-term')} />)

    // Trace: exactly two fetches fire from this one search action — the query-driven
    // refetch (useAsync's own effect, registered before the offset-reset effect in hook
    // order) fires FIRST, at the OLD offset; only then does the offset-reset effect run
    // and trigger a SECOND, corrected fetch at offset=0.
    await waitFor(() => expect(calls).toHaveLength(4))
    const staleCall = calls[2]
    const correctCall = calls[3]
    expect(urlParams(staleCall.url).get('offset'), 'the query-driven refetch fires before the offset-reset effect, at the OLD offset').toBe('50')
    expect(urlParams(staleCall.url).get('q')).toBe('search-term')
    expect(urlParams(correctCall.url).get('offset'), 'the offset-reset effect then fires its own corrected refetch').toBe('0')
    expect(urlParams(correctCall.url).get('q')).toBe('search-term')

    // Adversarial order: resolve the CORRECT (offset=0) response FIRST, then the STALE
    // (offset=50) one SECOND. If useAsync's runId guard did not actually invalidate the
    // stale call before this point, this late-arriving stale response would overwrite the
    // correct, already-rendered one.
    await act(async () => {
      correctCall.resolve(listResponse(correctAtOffset0, { limit: 50, offset: 0, total: 1 }))
      await new Promise((r) => setTimeout(r, 0))
    })
    await screen.findByText('INV-CORRECT-HIT')

    await act(async () => {
      staleCall.resolve(listResponse(staleAtOffset50, { limit: 50, offset: 50, total: 1 }))
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(screen.queryByText('INV-STALE-HIT'), 'a stale off-page response arriving AFTER the correct one must never overwrite it').toBeNull()
    expect(screen.getByText('INV-CORRECT-HIT')).toBeDefined()
    expect(screen.getByTestId('invoices-pager').textContent).toContain('OF 1')
    // No further fetches beyond the four traced above (no runaway refetch loop).
    expect(calls).toHaveLength(4)
  })
})

// RED specs (task-332, BUG-01-06, Mode A) -- the Status cell renders no violation
// indicator yet, so every assertion below fails on the row's actual textContent, not on
// an import/compile error.
describe('InvoicesList: violation disclosure chip (task-332, BUG-01-06)', () => {
  it('a row with an error-severity violation renders an ERROR chip its clean sibling does not', async () => {
    const blocked = row({
      id: 'blocked-1',
      invoice_number: 'INV-BLOCKED',
      violations: [{ rule_key: 'vat-standard-rate', severity: 'error', message: 'bad vat' }],
    })
    const clean = row({ id: 'clean-1', invoice_number: 'INV-CLEAN', violations: [] })
    mockFetchSequence([listResponse([blocked, clean], { limit: 50, offset: 0, total: 2 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-BLOCKED')

    const rows = screen.getAllByTestId('invoice-row')
    const blockedRow = rows.find((r) => r.textContent?.includes('INV-BLOCKED'))
    const cleanRow = rows.find((r) => r.textContent?.includes('INV-CLEAN'))
    expect(blockedRow?.textContent, 'blocked row must carry a singular "1 ERROR" chip').toMatch(/1 ERROR\b/)
    expect(cleanRow?.textContent, 'a violations:[] row must carry no ERROR chip').not.toMatch(/\d+ ERRORS?\b/)
  })

  it('two error-severity violations render the plural "2 ERRORS" (singular/plural threshold === 1)', async () => {
    const blocked = row({
      id: 'blocked-2',
      invoice_number: 'INV-BLOCKED-2',
      violations: [
        { rule_key: 'vat-standard-rate', severity: 'error', message: 'bad vat' },
        { rule_key: 'supplier-tin-required', severity: 'error', message: 'missing tin' },
      ],
    })
    mockFetchSequence([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-BLOCKED-2')

    expect(screen.getByTestId('invoice-row').textContent).toMatch(/2 ERRORS\b/)
  })

  it('a warning-only violation renders no ERROR chip -- only error severity is blocking', async () => {
    const warnOnly = row({
      id: 'warn-1',
      invoice_number: 'INV-WARN',
      violations: [{ rule_key: 'r', severity: 'warning', message: 'm' }],
    })
    mockFetchSequence([listResponse([warnOnly], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-WARN')

    expect(screen.getByTestId('invoice-row').textContent).not.toMatch(/\d+ ERRORS?\b/)
  })
})
