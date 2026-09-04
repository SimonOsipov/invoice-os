// @vitest-environment jsdom
// QA Stage 4 (task-329, BUG-01-03) — component-level coverage for the register's own
// pagination. invoices.test.ts's RED specs (Mode A) pin the pure helpers
// (listInvoices/invoiceListIsEmpty/selectableIds); nothing before this file rendered
// InvoicesList itself, so the wiring between those helpers and the DOM (does Next
// actually reach the last page, does select-all stay page-scoped, does the poll tick
// really carry the current offset) was unverified. The poll-tick call-shape source-scan
// lives in InvoicesList.pollShape.test.ts (node env) -- jsdom rewrites import.meta.url
// off file: scheme, breaking readFileSync(fileURLToPath(...)) here.
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import { skipReasonLabel, type InvoiceListResponse, type InvoiceRecord } from '../lib/invoices'
import { BULK_COPY, bulkBarView } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { InvoicesList } from './InvoicesList'

// Only the two APPR-16-03 specs that set this (A16-3b, the guard-order test below) make
// bulkBarView(...).eligible diverge from the real, pruneSelection-derived value; every
// other test in this file gets the REAL bulkBarView untouched (getEligibleOverride()
// returns null, a pure passthrough). This is deliberately NOT a timing race: an empirical
// probe against ApprovalsView (the already-shipped sibling sharing this exact [rows]-
// effect shape) showed React flushes the effect that prunes `selected`/resets `phase`
// atomically with the render that first makes a `rows` change observable -- a
// 15-macrotask-tick sweep after a manually-resolved refetch found the FIRST tick at which
// new content painted already reflected the fully-settled (pruned+disarmed) state, with
// no observable gap in between. `selected` and `pruneSelection(selected, rows)` can
// therefore never be caught diverging naturally in jsdom; this override makes the
// divergence deterministic instead ([A16-3b-non-vacuous]).
const { getEligibleOverride, setEligibleOverride } = vi.hoisted(() => {
  let override: string[] | null = null
  return {
    getEligibleOverride: () => override,
    setEligibleOverride: (next: string[] | null) => {
      override = next
    },
  }
})

vi.mock('../lib/reviewBatch', async (orig) => {
  const actual = await orig<typeof import('../lib/reviewBatch')>()
  return {
    ...actual,
    bulkBarView: (...args: Parameters<typeof actual.bulkBarView>) => {
      const real = actual.bulkBarView(...args)
      const override = getEligibleOverride()
      return override === null ? real : { ...real, eligible: override }
    },
  }
})

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
    failure_kind: null,
    approval: null,
    rule_set_version: null,
    can_approve: false,
    approve_blocked_reason: null,
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
  setEligibleOverride(null)
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

  // QA Mode B adversarial (task-332, BUG-01-06, point d): errorCount filters by severity,
  // it does not count the array length. A row with 1 error + 2 warnings must read exactly
  // "1 ERROR", never "3 ERRORS" -- untested by the RED specs above, which only ever mix
  // all-error or all-warning violations.
  it('a mix of 1 error and 2 warnings renders "1 ERROR", not "3 ERRORS" -- errorCount filters by severity, not array length', async () => {
    const mixed = row({
      id: 'mixed-1',
      invoice_number: 'INV-MIXED',
      violations: [
        { rule_key: 'r1', severity: 'warning', message: 'm1' },
        { rule_key: 'r2', severity: 'error', message: 'm2' },
        { rule_key: 'r3', severity: 'warning', message: 'm3' },
      ],
    })
    mockFetchSequence([listResponse([mixed], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-MIXED')

    const text = screen.getByTestId('invoice-row').textContent
    expect(text, 'must count only the error-severity violation').toMatch(/1 ERROR\b/)
    expect(text, 'must not count the two warnings into the chip').not.toMatch(/3 ERRORS?\b/)
  })
})

// RED specs (task-413, BUG-05-04, Mode A) -- no data-testid="buyer-tin" exists on the
// register row yet, so getByTestId throws below rather than reaching an assertion.
describe('buyer TIN missing signal (task-413, BUG-05-04)', () => {
  it('AC-3: null and empty buyer TIN both read TIN MISSING in red', async () => {
    const cases: Array<{ label: string; buyer_tin: string | null; invoiceNumber: string }> = [
      { label: 'null', buyer_tin: null, invoiceNumber: 'INV-TIN-NULL' },
      { label: 'empty string', buyer_tin: '', invoiceNumber: 'INV-TIN-EMPTY' },
    ]

    for (const { label, buyer_tin, invoiceNumber } of cases) {
      mockFetchSequence([
        listResponse([row({ id: `tin-${label}`, invoice_number: invoiceNumber, buyer_tin })], { limit: 50, offset: 0, total: 1 }),
      ])

      render(<InvoicesList ctx={listCtx()} />)
      await screen.findByText(invoiceNumber)

      const tin = screen.getByTestId('buyer-tin')
      expect(tin.textContent, label).toBe('TIN MISSING')
      expect(tin.style.color, label).toBe('var(--status-red-text)')

      cleanup()
    }
  })

  // Stage 1 gap-fill: today `r.buyer_tin ? grey : red` reads any non-empty string
  // (including whitespace) as present, so whitespace renders grey and invisible --
  // worse than the null/'' cases above, and untested at component level until now.
  it('AC-3 gap-fill: a whitespace-only buyer TIN also reads TIN MISSING in red, not grey', async () => {
    mockFetchSequence([
      listResponse([row({ id: 'tin-ws', invoice_number: 'INV-TIN-WS', buyer_tin: '   ' })], { limit: 50, offset: 0, total: 1 }),
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-TIN-WS')

    const tin = screen.getByTestId('buyer-tin')
    expect(tin.textContent).toBe('TIN MISSING')
    expect(tin.style.color).toBe('var(--status-red-text)')
  })
})

// RED specs (Mode A) -- no resolved marker exists yet, so AC-1 fails on the row's
// actual textContent, not an import/compile error.
describe('InvoicesList: resolved-failed marker', () => {
  it('a resolved failed row is marked and still listed', async () => {
    const resolved = row({
      id: 'resolved-1',
      invoice_number: 'INV-RESOLVED',
      status: 'failed',
      kept_as_is_at: '2026-08-01T00:00:00Z',
      kept_as_is_by: 'user-1',
      kept_as_is_reason: 'Filed manually with FIRS',
    })
    mockFetchSequence([listResponse([resolved], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-RESOLVED')

    const invRow = screen.getByTestId('invoice-row')
    expect(invRow.querySelector('[data-testid="invoice-status-badge"]')?.textContent, 'the row keeps its FAILED pill').toMatch(/FAILED/)
    expect(screen.getByTestId('invoice-resolved-marker'), 'a resolved marker must render alongside the pill').toBeDefined()
  })

  it('an unresolved failed row has no marker', async () => {
    const unresolved = row({
      id: 'unresolved-1',
      invoice_number: 'INV-UNRESOLVED',
      status: 'failed',
      kept_as_is_at: null,
    })
    mockFetchSequence([listResponse([unresolved], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-UNRESOLVED')

    expect(screen.queryAllByTestId('invoice-resolved-marker'), 'an unresolved failed row must carry no resolved marker').toHaveLength(0)
  })

  it('a kept blocked draft gets no resolved marker', async () => {
    const keptBlocked = row({
      id: 'kept-1',
      invoice_number: 'INV-KEPT',
      status: 'draft',
      kept_as_is_at: '2026-08-01T00:00:00Z',
      kept_as_is_by: 'user-1',
      kept_as_is_reason: 'Client accepted as-is',
      violations: [{ rule_key: 'vat-standard-rate', severity: 'error', message: 'bad vat' }],
    })
    mockFetchSequence([listResponse([keptBlocked], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-KEPT')

    const text = screen.getByTestId('invoice-row').textContent
    expect(screen.queryAllByTestId('invoice-resolved-marker'), 'the marker is status-gated (failed only), not mark-gated -- a kept draft must not get it').toHaveLength(0)
    expect(text, 'the existing ERROR chip must still render for the kept draft').toMatch(/1 ERROR\b/)
  })

  it('the marker adds nothing to the row count', async () => {
    const resolved = row({ id: 'r-1', invoice_number: 'INV-A', status: 'failed', kept_as_is_at: '2026-08-01T00:00:00Z' })
    const draft = row({ id: 'r-2', invoice_number: 'INV-B', status: 'draft' })
    const failed = row({ id: 'r-3', invoice_number: 'INV-C', status: 'failed', kept_as_is_at: null })
    mockFetchSequence([listResponse([resolved, draft, failed], { limit: 50, offset: 0, total: 3 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    const rows = screen.getAllByTestId('invoice-row')
    expect(rows, 'the resolved mark must never hide, remove, or add a row').toHaveLength(3)
    expect(rows.map((r) => r.textContent?.includes('INV-A') ? 'A' : r.textContent?.includes('INV-B') ? 'B' : 'C')).toEqual(['A', 'B', 'C'])
  })

  // QA adversarial: resolvedOutside and hasBlockingViolation gate independently, so a
  // resolved failed row can still carry a blocking violation -- both markers must stack
  // without either swallowing the other. Uses .toContain, not a \b-anchored regex: the
  // two chips are adjacent sibling spans with no text separator between them, so
  // textContent reads "...1 ERRORRESOLVED" and a trailing \b on /1 ERROR\b/ never matches.
  it('a resolved failed row with a blocking violation renders both markers', async () => {
    const both = row({
      id: 'both-1',
      invoice_number: 'INV-BOTH',
      status: 'failed',
      kept_as_is_at: '2026-08-01T00:00:00Z',
      kept_as_is_by: 'user-1',
      kept_as_is_reason: 'Filed manually with FIRS',
      violations: [{ rule_key: 'vat-standard-rate', severity: 'error', message: 'bad vat' }],
    })
    mockFetchSequence([listResponse([both], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-BOTH')

    const text = screen.getByTestId('invoice-row').textContent
    expect(text, 'the ERROR chip must still render alongside the resolved mark').toContain('1 ERROR')
    expect(screen.getByTestId('invoice-resolved-marker').textContent, 'the resolved marker itself must not fold into the ERROR count').toBe('RESOLVED')
    expect(screen.getAllByTestId('invoice-resolved-marker'), 'exactly one resolved marker, not one per violation').toHaveLength(1)
  })
})

// RED spec (APPR-08-09, task-500, Stage 2.5/Mode A) — the observable form of AC #3's
// parity claim. An awaiting-approval row renders the SAME disabled checkbox a not-yet-
// validated row already renders: present, disabled, unchecked, no title, no tooltip, no
// new copy ([selectable-parity-not-new-copy]). It fails today because isRowSelectable's
// body is still status-only (its `// stub` marker), so the open-run row's checkbox is
// enabled and select-all sweeps it in.
describe('InvoicesList: an open approval run disables the row checkbox (APPR-08-09)', () => {
  const openRun = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }

  it('AC-3 parity: an awaiting-approval row keeps a PRESENT, disabled, unchecked checkbox and select-all counts 1', async () => {
    const rows = [
      row({ id: 'clear', invoice_number: 'INV-CLEAR', status: 'validated' }),
      row({ id: 'awaiting', invoice_number: 'INV-AWAIT', status: 'validated', approval: openRun }),
    ]
    mockFetchSequence([listResponse(rows, { limit: 50, offset: 0, total: 2 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-AWAIT')

    const awaiting = screen.getByLabelText('Select invoice INV-AWAIT') as HTMLInputElement
    // PRESENT, not hidden -- parity with a draft row, whose checkbox also renders disabled.
    expect(awaiting, 'the checkbox renders; it is not absent').toBeTruthy()
    expect(awaiting.disabled).toBe(true)
    expect((screen.getByLabelText('Select invoice INV-CLEAR') as HTMLInputElement).disabled).toBe(false)

    fireEvent.click(screen.getByTestId('invoice-select-all'))

    const summary = await screen.findByTestId('batch-submit-summary')
    expect(summary.textContent).toContain('1 selected on this page')
    expect(awaiting.checked, 'select-all must not sweep in an awaiting-approval row').toBe(false)
    expect((screen.getByLabelText('Select invoice INV-CLEAR') as HTMLInputElement).checked).toBe(true)
  })
})

// BUG-09 deleted the visible sibling and its per-row aria-describedby id -- a
// full-width line of prose under every blocked row. The reason copy is still
// skipReasonLabel's own (GAP-3), never an SPA-authored literal.
describe("InvoicesList: a blocked checkbox is disabled, dimmed, and carries the SERVER's own reason in its title attribute (APPR-12-06, BUG-09)", () => {
  const openRun = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }

  it("A06-5: a blocked checkbox is genuinely disabled and carries the server's reason in its title attribute", async () => {
    const blocked = row({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', status: 'draft' })
    mockFetchSequence([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-BLOCKED')

    const checkbox = screen.getByTestId('invoice-select') as HTMLInputElement
    const reason = skipReasonLabel('not_validated')

    // The real disabled attribute -- a keyboard user cannot reach it.
    expect(checkbox.disabled).toBe(true)
    checkbox.focus()
    expect(document.activeElement, 'a disabled control must be genuinely out of the tab order').not.toBe(checkbox)

    expect(checkbox.getAttribute('title'), "the SPA must carry skipReasonLabel's own sentence, not a substitute").toBe(reason)
  })

  it('A06-5b: two blocked rows on the same page each carry their OWN reason in their title', async () => {
    const notValidated = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'draft' })
    const awaitingApproval = row({ id: 'inv-b', invoice_number: 'INV-B', status: 'validated', approval: openRun })
    mockFetchSequence([listResponse([notValidated, awaitingApproval], { limit: 50, offset: 0, total: 2 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-B')

    const checkboxes = screen.getAllByTestId('invoice-select') as HTMLInputElement[]
    expect(checkboxes).toHaveLength(2)
    const titles = checkboxes.map((c) => c.getAttribute('title'))
    expect(titles[0]).toBe(skipReasonLabel('not_validated'))
    expect(titles[1]).toBe(skipReasonLabel('awaiting_approval'))
    expect(titles[0], 'two blocked rows must not share one reason').not.toBe(titles[1])
  })

  // QA Mode B adversarial (task-531): A06-5 never asserted GAP-2's layer 2 (the
  // disabled-only style swap that outranks the unguarded :hover) -- stripping it
  // entirely left all 33 specs in this file green.
  it('QA-1: layer 2 -- the disabled-only cursor/opacity swap lands on a blocked checkbox and NOT on a selectable one', async () => {
    const blocked = row({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', status: 'draft' })
    const selectable = row({ id: 'inv-ok', invoice_number: 'INV-OK', status: 'validated', approval: null })
    mockFetchSequence([listResponse([blocked, selectable], { limit: 50, offset: 0, total: 2 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-OK')

    const [blockedBox, selectableBox] = screen.getAllByTestId('invoice-select') as HTMLInputElement[]

    expect(blockedBox.style.cursor, 'a blocked checkbox must not keep the enabled hover cursor').toBe('not-allowed')
    expect(blockedBox.style.opacity, 'a blocked checkbox must visibly dim').toBe('0.5')

    expect(selectableBox.style.cursor, 'a selectable checkbox must not inherit the disabled cursor override').not.toBe('not-allowed')
    expect(selectableBox.style.opacity, 'a selectable checkbox must not be dimmed').not.toBe('0.5')
  })

  // QA Mode B adversarial (task-531): every existing spec here starts from a BLOCKED
  // row -- none proves the inverse, that a selectable row renders nothing at all.
  it('QA-2: a selectable row carries no reason -- no title', async () => {
    const selectable = row({ id: 'inv-ok', invoice_number: 'INV-OK', status: 'validated', approval: null })
    mockFetchSequence([listResponse([selectable], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-OK')

    const checkbox = screen.getByTestId('invoice-select') as HTMLInputElement
    expect(checkbox.disabled).toBe(false)
    expect(checkbox.getAttribute('title')).toBeNull()
  })

  // Ten deployed rows once read "Not validated — validate it first" beside an ACCEPTED
  // pill: every non-selectable row got a sentence whether or not one was true.
  it('A06-5c: a post-submission row keeps its disabled checkbox and carries no title', async () => {
    const rows = [
      row({ id: 'inv-acc', invoice_number: 'INV-ACC', status: 'accepted' }),
      row({ id: 'inv-fail', invoice_number: 'INV-FAIL', status: 'failed' }),
      row({ id: 'inv-rej', invoice_number: 'INV-REJ', status: 'rejected' }),
    ]
    mockFetchSequence([listResponse(rows, { limit: 50, offset: 0, total: 3 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-REJ')

    for (const label of ['Select invoice INV-ACC', 'Select invoice INV-FAIL', 'Select invoice INV-REJ']) {
      const checkbox = screen.getByLabelText(label) as HTMLInputElement
      expect(checkbox.disabled, `${label}: a filed invoice is not submittable`).toBe(true)
      expect(
        checkbox.getAttribute('title'),
        `${label}: telling the operator to validate an already-filed invoice is false`,
      ).toBeNull()
    }
  })
})

// The reason span carries gridColumn '2 / -1', which starts an implicit second grid line.
// Element children only -- the `{' '}` separator is a text node, neither a child element
// nor a grid item.
describe('BUG-09: a blocked register row costs no extra grid line', () => {
  const openRun = {
    run_state: 'open',
    pending_ord: 1,
    pending_role_title: 'Reviewer',
    pending_holder_warn: false,
    due_at: null,
    overdue: false,
  }

  it("B09-1: a blocked register row renders exactly the head's grid children, and keeps its title", async () => {
    const blocked = row({ id: 'inv-blocked', invoice_number: 'INV-BLOCKED', status: 'draft' })
    mockFetchSequence([listResponse([blocked], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-BLOCKED')

    const head = screen.getByTestId('invoices-list').querySelector('.pf-list-head')
    expect(head, 'no list head -- the comparison below would be vacuous').not.toBeNull()
    const rowEl = screen.getByTestId('invoice-row')

    // Non-vacuity: the row must really be blocked, or two equal counts prove nothing.
    expect((screen.getByTestId('invoice-select') as HTMLInputElement).getAttribute('title')).toBe(skipReasonLabel('not_validated'))
    expect(rowEl.children.length).toBe((head as HTMLElement).children.length)
  })

  it('B09-2: an awaiting-approval register row renders the same grid children as a selectable one', async () => {
    const awaiting = row({ id: 'inv-await', invoice_number: 'INV-AWAIT', status: 'validated', approval: openRun })
    const selectable = row({ id: 'inv-ok', invoice_number: 'INV-OK', status: 'validated', approval: null })
    mockFetchSequence([listResponse([awaiting, selectable], { limit: 50, offset: 0, total: 2 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-OK')

    const [awaitingRow, cleanRow] = screen.getAllByTestId('invoice-row')
    const [awaitingBox, cleanBox] = screen.getAllByTestId('invoice-select') as HTMLInputElement[]

    // Non-vacuity: one row really blocked, the other really selectable.
    expect(awaitingBox.disabled).toBe(true)
    expect(awaitingBox.getAttribute('title')).toBe(skipReasonLabel('awaiting_approval'))
    expect(cleanBox.disabled).toBe(false)

    expect(awaitingRow.children.length).toBe(cleanRow.children.length)
  })
})

// Mode A RED spec (AC-3). The toggle now sweeps in drafts an approver sent back; the label
// alone ("Needs attention") does not say so.
const TOGGLE_EXPLAINER = 'Includes invoices an approver sent back.'

describe('InvoicesList: the needs-attention toggle says what it now includes', () => {
  it('the line is absent while the toggle is off, present while it is on, and gone again when it is off', async () => {
    // Three responses: mount, the ON refetch, the OFF refetch (needsAttention is in `deps`).
    mockFetchSequence([
      listResponse([row({ id: 'o1', invoice_number: 'INV-OFF-1' })], { limit: 50, offset: 0, total: 1 }),
      listResponse([row({ id: 'n1', invoice_number: 'INV-ON' })], { limit: 50, offset: 0, total: 1 }),
      listResponse([row({ id: 'o2', invoice_number: 'INV-OFF-2' })], { limit: 50, offset: 0, total: 1 }),
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-OFF-1')
    expect(screen.queryByText(TOGGLE_EXPLAINER), 'the unfiltered register must not carry the line').toBeNull()

    fireEvent.click(screen.getByTestId('needs-attention-toggle'))
    await screen.findByText('INV-ON')
    // Exact-text match: the copy is its own line, not a clause inside a longer paragraph.
    expect(screen.queryByText(TOGGLE_EXPLAINER), 'the ON filter must name what it sweeps in').not.toBeNull()

    fireEvent.click(screen.getByTestId('needs-attention-toggle'))
    await screen.findByText('INV-OFF-2')
    expect(screen.queryByText(TOGGLE_EXPLAINER), 'toggling back off must remove it').toBeNull()
  })

  // QA adversarial. The zero-row branch is the case the explainer matters most in — the
  // register looks identical to a genuinely invoice-less one. The generic "No invoices yet"
  // beside it is a KNOWN GAP with no owner: that empty state never consults the toggle.
  // Deliberately unasserted here so fixing the copy does not have to delete this test.
  it('the line survives a filtered result set that comes back empty', async () => {
    mockFetchSequence([
      listResponse([row({ id: 'a1', invoice_number: 'INV-A1' })], { limit: 50, offset: 0, total: 1 }),
      listResponse([], { limit: 50, offset: 0, total: 0 }),
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A1')

    fireEvent.click(screen.getByTestId('needs-attention-toggle'))
    await screen.findByTestId('invoices-empty')

    expect(screen.queryByText(TOGGLE_EXPLAINER), 'the explainer is not nested under the populated branch').not.toBeNull()
    expect(screen.getByTestId('needs-attention-toggle'), 'and the toggle stays reachable to clear the filter').toBeDefined()
  })
})

// RED specs (APPR-16-03, task-537, Stage 2.5/Mode A), authored before the fix in
// afba8c8: batch-submit called submitSelection directly on click (pre-fix
// InvoicesList.tsx:426/264), one click transmitting every selected invoice with no
// brake -- every spec below failed at the time on a missing element / an assertion on
// behavior that didn't exist yet, never an import or compile error. The fix (570d19b)
// now makes them green; see the arm/confirm/cancel wiring below.
function submitOkResponse(items: { invoice_id: string; enqueued: boolean; reason?: string }[] = []): MockResponse {
  return { ok: true, status: 200, json: () => Promise.resolve({ results: items }) }
}

function submitErrorResponse(status: number, message: string): MockResponse {
  return { ok: false, status, json: () => Promise.resolve({ error: message }) }
}

function postedIds(fetchMock: ReturnType<typeof vi.fn>, callIndex: number): string[] {
  const [, init] = fetchMock.mock.calls[callIndex] as [string, RequestInit]
  const body = JSON.parse(init.body as string) as { invoice_ids: string[] }
  return body.invoice_ids
}

describe('InvoicesList: batch-submit arms before it confirms (APPR-16-03, task-537)', () => {
  it('A16-3a: arming sends nothing -- zero POSTs, and the confirm control appears', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const b = row({ id: 'inv-b', invoice_number: 'INV-B', status: 'validated' })
    const fetchMock = mockFetchSequence([listResponse([a, b], { limit: 50, offset: 0, total: 2 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('invoice-select-all'))
    fireEvent.click(screen.getByTestId('batch-submit')) // arm

    expect(screen.getByTestId('batch-submit-confirm'), 'arming must reveal the confirm stage').toBeTruthy()
    expect(fetchMock, 'arming alone must issue no request').toHaveBeenCalledTimes(1) // only the initial GET
  })

  it('A16-3b: the confirm-time payload is bulkBarView(...).eligible, never the raw selected array [A16-3b-non-vacuous]', async () => {
    // See the module-level comment on getEligibleOverride/setEligibleOverride above for
    // why this uses a deterministic override rather than racing the poll tick: the two
    // arrays are provably equal by the time anything is observable in jsdom, so a
    // "select 2, confirm, assert both ids" test would pass IDENTICALLY whether
    // submitSelection reads bar.eligible or raw `selected` (Stage 2's own vacuity
    // warning). This override makes `selected` (still ['inv-a','inv-b']) and
    // bar.eligible (forced to just ['inv-a']) diverge, so only a submitSelection that
    // actually reads bar.eligible can produce the assertion below.
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const b = row({ id: 'inv-b', invoice_number: 'INV-B', status: 'validated' })
    const fetchMock = mockFetchSequence([
      listResponse([a, b], { limit: 50, offset: 0, total: 2 }),
      submitOkResponse([{ invoice_id: 'inv-a', enqueued: true }]),
      listResponse([a, b], { limit: 50, offset: 0, total: 2 }), // the post-success refetch
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('invoice-select-all'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    expect(screen.getByTestId('batch-submit-confirm')).toBeTruthy()

    setEligibleOverride(['inv-a'])
    fireEvent.click(screen.getByTestId('batch-submit-confirm'))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3))
    const ids = postedIds(fetchMock, 1)
    expect(ids, 'must send bar.eligible, not raw selected -- exactly one id').toHaveLength(1)
    expect(ids).toEqual(['inv-a'])
    expect(ids, 'specifically must NOT carry the now-ineligible id').not.toContain('inv-b')
  })

  it('guard order (AC-15): an empty eligible set at confirm time enters no submitting state and issues no request', async () => {
    // Wrong order (toPhase({type:'confirm'}) before the empty-eligible bail) strands the
    // bar in 'submitting' forever, since `settled` only fires from a `finally` that a
    // pre-try `return` never reaches (ReviewInvoicesTab.tsx:304-310 names this bug).
    // Uses the same override technique as A16-3b -- an empty eligible set with a
    // non-empty `selected` cannot be reached via the UI alone (bar.visible gates the
    // whole bar on eligible.length > 0, so a genuinely empty eligible set hides the
    // confirm control entirely; this override keeps the control visible/clickable while
    // forcing its OWN 'eligible' to empty, isolating the guard-order check).
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const fetchMock = mockFetchSequence([listResponse([a], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    expect(screen.getByTestId('batch-submit-confirm')).toBeTruthy()

    setEligibleOverride([])
    fireEvent.click(screen.getByTestId('batch-submit-confirm'))

    expect(fetchMock, 'an empty eligible set must not POST').toHaveBeenCalledTimes(1)
    expect(screen.queryByText(BULK_COPY.sending), 'must never enter the submitting/sending state').toBeNull()
  })

  it('A16-3c: a double-click on confirm produces exactly one request', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const fetchMock = mockFetchSequence([
      listResponse([a], { limit: 50, offset: 0, total: 1 }),
      submitOkResponse([{ invoice_id: 'inv-a', enqueued: true }]),
      listResponse([a], { limit: 50, offset: 0, total: 1 }), // the post-success refetch
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    const confirmBtn = screen.getByTestId('batch-submit-confirm')
    fireEvent.click(confirmBtn) // confirm #1
    fireEvent.click(confirmBtn) // confirm #2, SAME tick -- `disabled` has not re-rendered yet (ApprovalsView.test.tsx A04-3 precedent: jsdom's fireEvent still dispatches to a disabled button, so the ref/phase guard is what actually has to stop this, not the attribute)

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3))
    expect(fetchMock, 'a double-click must send exactly one POST, not two').toHaveBeenCalledTimes(3) // 1 GET + 1 POST + 1 refetch
  })

  it('A16-3d: any selection change while armed returns the bar to idle', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const b = row({ id: 'inv-b', invoice_number: 'INV-B', status: 'validated' })
    mockFetchSequence([listResponse([a, b], { limit: 50, offset: 0, total: 2 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    expect(screen.getByTestId('batch-submit-confirm')).toBeTruthy()

    fireEvent.click(screen.getByLabelText('Select invoice INV-B')) // selection changes while armed

    expect(screen.queryByTestId('batch-submit-confirm'), 'any selection change while armed must return to idle').toBeNull()
    expect(screen.getByTestId('batch-submit'), 'idle shows the arm control again').toBeTruthy()
  })

  it('A16-3e: cancel returns to idle and sends nothing; cancel while submitting is a no-op', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const fetchMock = mockFetchSequence([listResponse([a], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    fireEvent.click(screen.getByTestId('batch-submit-cancel'))

    expect(screen.queryByTestId('batch-submit-confirm'), 'cancel from armed must return to idle').toBeNull()
    expect(screen.getByTestId('batch-submit'), 'idle shows the arm control again').toBeTruthy()
    expect(fetchMock, 'cancel must send nothing').toHaveBeenCalledTimes(1)
  })

  it('A16-3e (cancel while submitting): the in-flight request is not cancellable', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const resolvers: Array<(r: MockResponse) => void> = []
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('/submissions')) return new Promise<MockResponse>((resolve) => resolvers.push(resolve))
      return Promise.resolve(listResponse([a], { limit: 50, offset: 0, total: 1 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    fireEvent.click(screen.getByTestId('batch-submit-confirm')) // now submitting, POST held pending

    const cancelBtn = screen.getByTestId('batch-submit-cancel') as HTMLButtonElement
    expect(cancelBtn.disabled, 'cancel must carry the real disabled attribute while submitting').toBe(true)

    fireEvent.click(cancelBtn)
    expect(screen.getByTestId('batch-submit-confirm'), 'a no-op cancel must not un-arm a request already in flight').toBeTruthy()

    resolvers[0](submitOkResponse([{ invoice_id: 'inv-a', enqueued: true }]))
    await waitFor(() => expect(screen.queryByTestId('batch-submit-confirm')).toBeNull())
  })

  it('A16-3f: a confirm dispatched without ever arming fires nothing (identity return from bulkPhaseReducer)', async () => {
    // The UI has no affordance to invoke "confirm" while idle -- batch-submit-confirm
    // does not exist until armed (AC #1's own idle/armed gate), so an unarmed confirm
    // cannot be driven through a click the way A16-3c's double-click can. This makes AC
    // #2's guarantee observable via its DOM consequence instead: the confirm control is
    // simply absent before arming, so there is no affordance through which an unarmed
    // confirm could ever fire a request. bulkPhaseReducer's own identity-return for
    // 'confirm' from 'idle' is exhaustively unit-tested in reviewBatch.test.ts and is not
    // re-authored here (D-21, reuse-don't-re-author).
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const fetchMock = mockFetchSequence([listResponse([a], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    expect(screen.queryByTestId('batch-submit-confirm'), 'no confirm affordance exists before arming').toBeNull()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('A16-3g: every visible bar string comes from bulkBarView/BULK_COPY, never an inline literal', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const b = row({ id: 'inv-b', invoice_number: 'INV-B', status: 'validated' })
    mockFetchSequence([listResponse([a, b], { limit: 50, offset: 0, total: 2 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('invoice-select-all'))

    const idle = bulkBarView(['inv-a', 'inv-b'], [a, b], 'idle', false)
    expect(screen.getByTestId('batch-submit-summary').textContent).toContain(idle.countLabel)
    expect(screen.getByText(idle.submitLabel)).toBeTruthy()

    fireEvent.click(screen.getByTestId('batch-submit'))

    const armed = bulkBarView(['inv-a', 'inv-b'], [a, b], 'armed', false)
    expect(screen.getByText(armed.confirmPrompt)).toBeTruthy()
    expect(screen.getByText(armed.confirmDetail)).toBeTruthy()
    expect(screen.getByText(armed.confirmLabel)).toBeTruthy()
    expect(screen.getByText(BULK_COPY.cancel)).toBeTruthy()
  })

  it('A16-3h / D-22 [no-modal]: the confirm block is a DOM descendant of the bar, never a portal or modal', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    mockFetchSequence([listResponse([a], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))

    const armed = bulkBarView(['inv-a'], [a], 'armed', false)
    const summary = screen.getByTestId('batch-submit-summary')
    const confirmPrompt = screen.getByText(armed.confirmPrompt)
    expect(summary.contains(confirmPrompt), 'the confirm block must be a descendant of the bar, not portaled elsewhere').toBe(true)
  })

  it('testid-placement: the wrapper splits into an action row and a confirm block, not a bolted-on third child', async () => {
    // Stage 2's own snippet (task-537) checks actionRow.contains(batch-submit) and
    // summary.contains(confirmPrompt) as if both held at once -- but the plan's OWN
    // testid assignment ("Keep data-testid='batch-submit' on the arm button (idle
    // state)") makes batch-submit an IDLE-ONLY control, mirroring
    // approvals-bulk-submit/approvals-bulk-confirm's swap in ApprovalsView.tsx:239-284,
    // so the two can never coexist. Split across the two states instead, reusing the
    // SAME `actionRow` DOM node reference across the re-render (React reconciles the
    // wrapper div in place, it does not remount) -- this still fails under a bolted-on
    // third child exactly as intended: that shape's `actionRow` IS `summary` itself (no
    // real inner row), so `actionRow.contains(confirmPrompt)` would wrongly read true.
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    mockFetchSequence([listResponse([a], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))

    const summary = screen.getByTestId('batch-submit-summary')
    const actionRow = summary.firstElementChild as HTMLElement
    expect(actionRow.contains(screen.getByTestId('batch-submit')), 'the arm control lives in the action row').toBe(true)

    fireEvent.click(screen.getByTestId('batch-submit'))

    const armed = bulkBarView(['inv-a'], [a], 'armed', false)
    const confirmPrompt = screen.getByText(armed.confirmPrompt)
    expect(actionRow.contains(confirmPrompt), 'the confirm prompt must NOT be nested inside the action row').toBe(false)
    expect(summary.contains(confirmPrompt), 'the confirm prompt must still be a descendant of the outer wrapper').toBe(true)
  })

  it('A16-3l: a rejected confirm returns the bar to idle with the selection intact and shows an error', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('/submissions')) return Promise.resolve(submitErrorResponse(500, 'boom'))
      return Promise.resolve(listResponse([a], { limit: 50, offset: 0, total: 1 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    fireEvent.click(screen.getByTestId('batch-submit-confirm'))

    // `settled` fires from the finally, never the end of the try (AC-6) -- proven by the
    // bar actually recovering here, not sticking in 'submitting' forever the way a
    // pre-finally return would.
    await waitFor(() => expect(screen.queryByTestId('batch-submit-confirm')).toBeNull())
    expect(screen.getByTestId('batch-submit'), 'idle, ready to re-arm').toBeTruthy()
    expect((screen.getByLabelText('Select invoice INV-A') as HTMLInputElement).checked, 'the selection survives a rejected confirm').toBe(true)
    expect(screen.getByText('Something went wrong'), 'ErrorState must render for the rejected confirm').toBeTruthy()
  })

  // QA adversarial coverage (task-537 Stage 4). A16-3c's two SEPARATE fireEvent.click
  // calls each get their own synchronous React flush in this harness, so `disabled`
  // alone already wins there and `submitInFlight` itself is never actually reached --
  // confirmed by reproducing it against a ref-removed mutation (2 POSTs, not 1) before
  // writing the assertions below. Same technique as ApprovalsView.test.tsx's "TRUE
  // same-tick double click" precedent: nesting both dispatches inside ONE outer act()
  // suppresses the intermediate flush, so both onClick handlers run against the SAME
  // pre-render `phase` closure before either commits.
  it('QA adversarial: two mouse clicks landing in the SAME React commit still fire exactly one POST', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const fetchMock = mockFetchSequence([
      listResponse([a], { limit: 50, offset: 0, total: 1 }),
      submitOkResponse([{ invoice_id: 'inv-a', enqueued: true }]),
      listResponse([a], { limit: 50, offset: 0, total: 1 }),
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    const confirmBtn = screen.getByTestId('batch-submit-confirm')

    act(() => {
      fireEvent.click(confirmBtn)
      fireEvent.click(confirmBtn)
    })

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3))
    expect(fetchMock, 'two same-commit clicks must still fire exactly one POST, not two').toHaveBeenCalledTimes(3)
  })

  it('QA adversarial: keyboard activation (Enter) reaches confirm, and a second Enter press does not double-fire', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const fetchMock = mockFetchSequence([
      listResponse([a], { limit: 50, offset: 0, total: 1 }),
      submitOkResponse([{ invoice_id: 'inv-a', enqueued: true }]),
      listResponse([a], { limit: 50, offset: 0, total: 1 }),
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    const confirmBtn = screen.getByTestId('batch-submit-confirm')
    confirmBtn.focus()

    // A native <button> answers Enter/Space with a real click event -- the onClick
    // handler this fires is the SAME one a mouse click reaches, so the guard protecting
    // it must hold for this input path too. userEvent (not fireEvent.keyDown) is what
    // actually simulates that browser default-action behavior in jsdom. NOTE: unlike the
    // mouse test above, userEvent's key dispatch is genuinely async (real awaits between
    // key events), so it cannot be forced into ONE React commit the way two synchronous
    // fireEvent.click calls can -- confirmed empirically (an act()-wrapped
    // user.keyboard('{Enter}{Enter}') still passes even with `submitInFlight`'s check
    // deleted). What IS provably load-bearing here, and what this asserts, is that
    // bulkPhaseReducer's OWN identity return (input-modality-agnostic -- it only reads
    // `phase`) stops the second, naturally-flushed Enter press. The synchronous-ref proof
    // for the true same-tick race lives in the mouse test above.
    const user = userEvent.setup()
    await user.keyboard('{Enter}')
    await user.keyboard('{Enter}')

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3))
    expect(fetchMock, 'a second Enter press after the first must not fire a second POST').toHaveBeenCalledTimes(3)
  })

  it('QA adversarial: the eligible set shrinking to zero between arm and confirm (a live poll tick) collapses the bar instead of stranding it', async () => {
    // status:'queued' on a third, unselected row keeps shouldPollList active so
    // useLiveRefresh actually installs an interval (AC-6's own real-2s-wait precedent
    // above) -- a and b must be 'validated' to be selectable in the first place.
    // NOTE: `bar` recomputes bulkBarView(selected, rows, ...) fresh every render off the
    // CURRENT `rows`, so this end-to-end behavior turns out to be guaranteed by the
    // `bar.visible &&` render gate alone -- confirmed by mutation: deleting either the
    // `[rows]` effect's setPhase reset OR its setSelected prune independently leaves this
    // green (the other one, or bulkBarView's own internal pruneSelection, still hides the
    // bar), matching Stage 2.5's proof that the effect's phase reset is unobservable at
    // this layer. What this test pins is the OUTCOME -- a real poll tick that empties
    // eligibility must not strand the confirm control -- not that one specific line does it.
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const b = row({ id: 'inv-b', invoice_number: 'INV-B', status: 'validated' })
    const c = row({ id: 'inv-c', invoice_number: 'INV-C', status: 'queued' })
    const fetchMock = mockFetchSequence([
      listResponse([a, b, c], { limit: 50, offset: 0, total: 3 }),
      // the poll tick: a and b both moved out from under the arm (someone else
      // submitted them first) -- c stays queued so the tick had a reason to fire.
      listResponse(
        [row({ id: 'inv-a', invoice_number: 'INV-A', status: 'queued' }), row({ id: 'inv-b', invoice_number: 'INV-B', status: 'queued' }), c],
        { limit: 50, offset: 0, total: 3 },
      ),
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByTestId('invoice-select-all'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    expect(screen.getByTestId('batch-submit-confirm'), 'armed over a and b').toBeTruthy()

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2), { timeout: 3500, interval: 100 })
    await waitFor(() => expect(screen.queryByTestId('batch-submit-summary')).toBeNull(), { timeout: 1000 })
    expect(fetchMock, 'the shrink itself must never trigger a submit').toHaveBeenCalledTimes(2)
  }, 8000)

  it('QA adversarial: a rejected confirm, re-armed, still lets a second confirm actually send', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const fetchMock = mockFetchSequence([
      listResponse([a], { limit: 50, offset: 0, total: 1 }),
      submitErrorResponse(500, 'boom'),
      submitOkResponse([{ invoice_id: 'inv-a', enqueued: true }]),
      listResponse([a], { limit: 50, offset: 0, total: 1 }),
    ])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    fireEvent.click(screen.getByTestId('batch-submit-confirm'))

    await waitFor(() => expect(screen.queryByTestId('batch-submit-confirm')).toBeNull())
    expect(screen.getByText('Something went wrong'), 'the first confirm was rejected').toBeTruthy()

    // Re-arm: `settled` firing from the finally has to leave a genuinely USABLE bar, not
    // one that only LOOKS idle -- submitInFlight and phase both have to be back to a
    // state a second confirm can actually pass through.
    fireEvent.click(screen.getByTestId('batch-submit'))
    fireEvent.click(screen.getByTestId('batch-submit-confirm'))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4))
    expect(screen.queryByTestId('batch-submit-confirm'), 'the retry succeeded').toBeNull()
    expect(screen.queryByText('Something went wrong'), 'the stale error from the first attempt must be gone').toBeNull()
  })

  it('QA adversarial (epic Q12): the bar never claims a past-tense transmission outcome, idle or armed', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    mockFetchSequence([listResponse([a], { limit: 50, offset: 0, total: 1 })])

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    const OUTCOME_WORDS = ['sent', 'transmitted', 'accepted', 'delivered', 'received']

    function assertNoOutcomeClaim(label: string) {
      const text = screen.getByTestId('batch-submit-summary').textContent ?? ''
      for (const word of OUTCOME_WORDS) {
        expect(new RegExp(`\\b${word}\\b`, 'i').test(text), `${label}: must never claim "${word}" already happened`).toBe(false)
      }
    }

    assertNoOutcomeClaim('idle')
    fireEvent.click(screen.getByTestId('batch-submit'))
    assertNoOutcomeClaim('armed')
  })
})

// --- APPR-16-04 (task-536, Mode A) -- RED specs for the register's own in-flight pager
// freeze (AC-6/AC-7) and AC-8's regression guard. A16-4e/f (unmount-abort, the approvals
// pager) live in ApprovalsView.test.tsx -- this component has no fan-out to abort
// (submitSelection is one request, AC #9), so only the freeze applies here.
describe('A16-4: the register pager freezes for the whole in-flight window and states why (APPR-16-04)', () => {
  it('A16-4g: the register pager is disabled for the whole in-flight window, and re-enabled even when the request rejects', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const b = row({ id: 'inv-b', invoice_number: 'INV-B', status: 'validated' })
    const resolvers: Array<(r: MockResponse) => void> = []
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('/submissions')) return new Promise<MockResponse>((resolve) => resolvers.push(resolve))
      // limit:1/offset:1/total:3 -- both Prev and Next start enabled absent `busy`, so
      // the freeze under test is the OR clause, not an edge-of-set disable.
      return Promise.resolve(listResponse([a, b], { limit: 1, offset: 1, total: 3 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    const pager = () => screen.getByTestId('invoices-pager')
    const prevBtn = () => within(pager()).getByText('← Previous').closest('button') as HTMLButtonElement
    const nextBtn = () => within(pager()).getByText('Next →').closest('button') as HTMLButtonElement
    expect(prevBtn().disabled, 'both buttons must start enabled -- an edge-of-set disable would make this test vacuous').toBe(false)
    expect(nextBtn().disabled).toBe(false)

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    fireEvent.click(screen.getByTestId('batch-submit-confirm')) // now submitting, POST held pending

    // `loading` is false here (list.data is still the settled page) -- only `phase ===
    // 'submitting'` can be freezing the pager in this window.
    expect(prevBtn().disabled, 'the pager must freeze for the whole in-flight window').toBe(true)
    expect(nextBtn().disabled).toBe(true)

    resolvers[0](submitErrorResponse(500, 'boom')) // the rejection path
    await waitFor(() => expect(screen.queryByTestId('batch-submit-confirm')).toBeNull())
    expect(nextBtn().disabled, 'the pager must re-enable in the finally, even on the rejection path').toBe(false)
    expect(prevBtn().disabled).toBe(false)
  })

  it('A16-4h: the frozen pager states its reason (D-25); InvoicesList.tsx carries no nav-locking machinery (D-31)', async () => {
    const a = row({ id: 'inv-a', invoice_number: 'INV-A', status: 'validated' })
    const resolvers: Array<(r: MockResponse) => void> = []
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('/submissions')) return new Promise<MockResponse>((resolve) => resolvers.push(resolve))
      return Promise.resolve(listResponse([a], { limit: 1, offset: 1, total: 3 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<InvoicesList ctx={listCtx()} />)
    await screen.findByText('INV-A')

    // Not yet frozen -- title never fires on a disabled element in Chromium, so an absent
    // visible node here would mean the reason is unreachable the moment it matters.
    expect(within(screen.getByTestId('invoices-pager')).queryByTestId('pager-blocked-reason'), 'no reason node before the freeze').toBeNull()

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('batch-submit'))
    fireEvent.click(screen.getByTestId('batch-submit-confirm')) // now submitting, POST held pending

    const pager = screen.getByTestId('invoices-pager')
    const buttons = within(pager).getAllByRole('button') as HTMLButtonElement[]
    expect(buttons.length).toBeGreaterThan(0)
    for (const btn of buttons) {
      expect(btn.disabled, 'the pager -- the one control the freeze disables -- must be disabled while submitting').toBe(true)
      expect(btn.title, 'a disabled control must state WHY (D-25/Q6-Q10), never a bare disabled attribute').toBeTruthy()
    }
    // The reason must reach the user as VISIBLE text, not just the inert title.
    expect(within(pager).getByTestId('pager-blocked-reason').textContent, 'the visible reason must be BULK_COPY.pagerReason').toBe(BULK_COPY.pagerReason)
    expect(screen.getByText(BULK_COPY.pagerReason), 'queryable by text, not just by attribute').toBeTruthy()

    // D-31 regression guard: only the pager freezes. Not itself expected to flip red-to-
    // green from this subtask's own work (neither line exists today, for or against) --
    // it exists so a LATER subtask cannot quietly reintroduce the declined nav lock.
    const source = readFileSync(path.join(process.cwd(), 'src/components/InvoicesList.tsx'), 'utf8')
    expect(source, 'no PlatformCtx.navLocked reference belongs in InvoicesList.tsx').not.toMatch(/navLocked/)
    expect(source, "InvoicesList.tsx must never call ctx.nav -- navigation stays Sidebar/App.tsx's job").not.toMatch(/ctx\.nav\(/)
    expect(source, 'InvoicesList.tsx must never call ctx.switchClient -- the entity switcher stays live').not.toMatch(/ctx\.switchClient\(/)

    resolvers[0](submitOkResponse([{ invoice_id: 'inv-a', enqueued: true }]))
    await waitFor(() => expect(screen.queryByTestId('batch-submit-confirm')).toBeNull())
  })
})

describe('A16-4i: submitSelection carries no AbortSignal, and says why (D-05, APPR-16-04)', () => {
  it('submitSelection() takes zero params, and a comment ahead of it names the one-request reason', () => {
    const source = readFileSync(path.join(process.cwd(), 'src/components/InvoicesList.tsx'), 'utf8')

    const declIdx = source.indexOf('async function submitSelection(')
    expect(declIdx, 'submitSelection must still exist').toBeGreaterThan(-1)
    expect(
      source.slice(declIdx, declIdx + 40),
      'submitSelection is one request, not a loop -- there are no rows to check a signal between',
    ).toMatch(/^async function submitSelection\(\)/)

    const preamble = source.slice(Math.max(0, declIdx - 700), declIdx)
    expect(preamble, 'a comment must say WHY submitSelection takes no AbortSignal -- omission alone is not documentation').toMatch(/\bsignal\b/i)
    expect(preamble, 'the stated reason must be single request vs. a loop, not a vague deferral').toMatch(/\b(one|single)\s+request\b/i)
  })
})
