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
// as Header.test.tsx's HeaderCtx.
function listCtx(): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    openCreate: () => {},
    openImportedInvoice: () => {},
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
