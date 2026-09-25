// @vitest-environment jsdom
// D-28 closed (post-APPR-16 gap): this tab's own pager freeze had no component test file
// when APPR-16-04 shipped (Pager.test.ts's source-scan carved it out deliberately). This
// is that harness -- minimal, scoped to the freeze itself, not broad coverage of the tab.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { ImportBatch } from '../lib/importApi'
import type { InvoiceListResponse, InvoiceRecord } from '../lib/invoices'
import { BULK_COPY } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { ReviewInvoicesTab } from './ReviewInvoicesTab'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

/** A promise the spec resolves by hand (WorkflowBuilder.test.tsx's deferred idiom). */
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

function listResponse(invoices: InvoiceRecord[], pagination: { limit: number; offset: number; total: number }): MockResponse {
  const body: InvoiceListResponse = { invoices, pagination }
  return { ok: true, status: 200, json: () => Promise.resolve(body) }
}

function rulesResponse(): MockResponse {
  return { ok: true, status: 200, json: () => Promise.resolve({ rules: [] }) }
}

function submitErrorResponse(status: number, message: string): MockResponse {
  return { ok: false, status, json: () => Promise.resolve({ error: message }) }
}

function row(over: Partial<InvoiceRecord> = {}): InvoiceRecord {
  const built = {
    id: 'inv-a',
    entity_id: 'ent-1',
    import_batch_id: 'b1',
    invoice_number: 'INV-A',
    status: 'validated',
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
    submit_blocked_reason: null,
    ...over,
  } as InvoiceRecord
  // Stands in for the server's answer on an unarmed tenant. Derived from status ONLY --
  // deriving the approval half too would put the deleted client rule back in a fixture.
  // Specs about the gate set can_submit explicitly.
  return { ...built, can_submit: over.can_submit ?? built.status === 'validated' }
}

function batch(over: Partial<ImportBatch> = {}): ImportBatch {
  return {
    id: 'b1',
    entity_id: 'ent-1',
    filename: 'june.csv',
    document_id: null,
    status: 'completed',
    rows_total: 1,
    rows_valid: 1,
    rows_invalid: 0,
    errors: [],
    rule_set_version: 3,
    created_at: '2026-08-01T00:00:00Z',
    ...over,
  }
}

function tabCtx(): PlatformCtx {
  const ctx = { authedFetch: createAuthedFetch(() => 'tok', vi.fn()) }
  return ctx as unknown as PlatformCtx
}

function renderTab(onSubmitted: () => void = vi.fn()) {
  return render(
    <ReviewInvoicesTab
      ctx={tabCtx()}
      base="https://gw"
      batchIds={['b1']}
      batches={[batch()]}
      totals={{ allTotal: 3, cleanTotal: 3, failingTotal: 0, queuedTotal: 0 }}
      onSubmitted={onSubmitted}
    />,
  )
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('ReviewInvoicesTab pager: freezes for the whole in-flight window (D-28 closed)', () => {
  it('disables Prev/Next while phase is submitting, and re-enables once settled -- even on the rejection path', async () => {
    const a = row()
    const pending = deferred<MockResponse>()
    // limit:1/offset:1/total:3 -- both Prev and Next start enabled absent the freeze, so
    // the assertion below is the OR clause, not an edge-of-set disable.
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('/violation-summary')) return Promise.resolve(rulesResponse())
      if (url.includes('/submissions')) return pending.promise
      return Promise.resolve(listResponse([a], { limit: 1, offset: 1, total: 3 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    renderTab()
    await screen.findByText('INV-A')

    const pager = () => screen.getByTestId('review-pager')
    const prevBtn = () => within(pager()).getByText('← Previous').closest('button') as HTMLButtonElement
    const nextBtn = () => within(pager()).getByText('Next →').closest('button') as HTMLButtonElement
    expect(prevBtn().disabled, 'both buttons must start enabled -- proves the freeze, not an edge-of-set disable, is under test').toBe(false)
    expect(nextBtn().disabled).toBe(false)

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('review-bulk-submit'))
    fireEvent.click(screen.getByTestId('review-bulk-confirm')) // now submitting, POST held pending

    expect(prevBtn().disabled, 'the pager must freeze for the whole in-flight window').toBe(true)
    expect(nextBtn().disabled).toBe(true)

    pending.resolve(submitErrorResponse(500, 'boom')) // the rejection path
    await waitFor(() => expect(screen.queryByTestId('review-bulk-confirm')).toBeNull())
    expect(prevBtn().disabled, 'the pager must re-enable once settled, even on the rejection path').toBe(false)
    expect(nextBtn().disabled).toBe(false)
  })

  it('states its reason as visible text while frozen, shared with the sibling surfaces via BULK_COPY.pagerReason', async () => {
    const a = row()
    const pending = deferred<MockResponse>()
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('/violation-summary')) return Promise.resolve(rulesResponse())
      if (url.includes('/submissions')) return pending.promise
      return Promise.resolve(listResponse([a], { limit: 1, offset: 1, total: 3 }))
    })
    vi.stubGlobal('fetch', fetchMock)

    renderTab()
    await screen.findByText('INV-A')

    // Not yet frozen -- title never fires on a disabled element in Chromium, so an absent
    // visible node here would mean the reason is unreachable the moment it matters.
    expect(within(screen.getByTestId('review-pager')).queryByTestId('pager-blocked-reason'), 'no reason node before the freeze').toBeNull()

    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('review-bulk-submit'))
    fireEvent.click(screen.getByTestId('review-bulk-confirm')) // now submitting, POST held pending

    const pager = screen.getByTestId('review-pager')
    expect(within(pager).getByTestId('pager-blocked-reason').textContent, 'the visible reason must be BULK_COPY.pagerReason').toBe(BULK_COPY.pagerReason)
    expect(screen.getByText(BULK_COPY.pagerReason), 'queryable by text, not just by attribute').toBeTruthy()
  })
})

describe('ReviewInvoicesTab bulk submit: drives the real component', () => {
  const SUBMIT_URL = 'https://gw/api/invoice/v1/invoices/submissions'
  const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i

  interface Call {
    url: string
    method: string
    auth: string | null
    body: unknown
  }

  // Routes the tab's three endpoints; `submit` answers every POST so a spec can hold it pending.
  function stubFetch(submit: () => Promise<MockResponse>) {
    const calls: Call[] = []
    const rows = [row({ id: 'inv-a', invoice_number: 'INV-A' }), row({ id: 'inv-b', invoice_number: 'INV-B' })]
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init?: { method?: string; headers?: Headers; body?: string }) => {
        const method = init?.method ?? 'GET'
        calls.push({ url, method, auth: init?.headers?.get('Authorization') ?? null, body: init?.body == null ? undefined : JSON.parse(init.body) })
        if (url.includes('/violation-summary')) return Promise.resolve(rulesResponse())
        if (method === 'POST' && url.endsWith('/invoices/submissions')) return submit()
        return Promise.resolve(listResponse(rows, { limit: 50, offset: 0, total: 2 }))
      }),
    )
    return {
      posts: () => calls.filter((c) => c.method === 'POST'),
      listGets: () => calls.filter((c) => c.method === 'GET' && !c.url.includes('/violation-summary')),
    }
  }

  function okResults(results: unknown[]): MockResponse {
    return { ok: true, status: 200, json: () => Promise.resolve({ results }) }
  }

  async function selectBothAndArm() {
    await screen.findByText('INV-A')
    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByLabelText('Select invoice INV-B'))
    fireEvent.click(screen.getByTestId('review-bulk-submit'))
  }

  it('select two, arm, confirm: one POST with exactly those ids', async () => {
    const net = stubFetch(() => Promise.resolve(okResults([])))
    renderTab()
    await selectBothAndArm()
    fireEvent.click(screen.getByTestId('review-bulk-confirm'))

    await waitFor(() => expect(net.posts()).toHaveLength(1))
    const [post] = net.posts()
    expect(post.url).toBe(SUBMIT_URL)
    expect(post.auth).toBe('Bearer tok')
    const body = post.body as { invoice_ids: string[]; idempotency_key: string }
    expect(body.invoice_ids).toEqual(['inv-a', 'inv-b'])
    expect(body.idempotency_key).toMatch(UUID_V4)
  })

  it('a 2xx shows one result row per item, refetches, calls onSubmitted, clears the selection', async () => {
    const net = stubFetch(() =>
      Promise.resolve(
        okResults([
          { invoice_id: 'inv-a', enqueued: true, status: 'queued' },
          { invoice_id: 'inv-b', enqueued: false, status: 'validated', reason: 'duplicate_request' },
        ]),
      ),
    )
    const onSubmitted = vi.fn()
    renderTab(onSubmitted)
    await selectBothAndArm()
    const listBefore = net.listGets().length
    expect(listBefore).toBeGreaterThanOrEqual(1)
    fireEvent.click(screen.getByTestId('review-bulk-confirm'))

    const panel = await screen.findByTestId('review-submit-results')
    // Child 0 is the header row; one row per result item follows.
    const resultRows = Array.from(panel.children).slice(1)
    expect(resultRows).toHaveLength(2)
    // 'Queued': bulkOutcome literal (lib/reviewBatch.ts). Skip label: SKIP_REASON_LABELS.duplicate_request (lib/invoices.ts).
    expect(Array.from(resultRows[0].children).map((c) => c.textContent)).toEqual(['INV-A', 'Queued'])
    expect(Array.from(resultRows[1].children).map((c) => c.textContent)).toEqual(['INV-B', 'Already submitted with this request'])

    expect(onSubmitted).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(net.listGets().length).toBeGreaterThan(listBefore))
    expect(screen.getByLabelText('Select invoice INV-A'), 'rows still render, so the bar is gone because the selection is empty').toBeTruthy()
    expect(screen.queryByTestId('review-bulk-bar')).toBeNull()
  })

  it('arming without confirming sends nothing', async () => {
    const net = stubFetch(() => Promise.resolve(okResults([])))
    renderTab()
    await screen.findByText('INV-A')
    fireEvent.click(screen.getByLabelText('Select invoice INV-A'))
    fireEvent.click(screen.getByTestId('review-bulk-submit'))

    expect(screen.getByTestId('review-bulk-confirm')).toBeTruthy()
    await act(async () => {})
    expect(net.listGets().length, 'the stub is live').toBeGreaterThanOrEqual(1)
    expect(net.posts()).toHaveLength(0)
  })

  it('a request-level failure keeps the selection and shows the error, no results', async () => {
    const net = stubFetch(() => Promise.resolve(submitErrorResponse(500, 'boom')))
    const onSubmitted = vi.fn()
    renderTab(onSubmitted)
    await selectBothAndArm()
    fireEvent.click(screen.getByTestId('review-bulk-confirm'))

    await screen.findByTestId('review-submit-error')
    expect(net.posts()).toHaveLength(1)
    expect(screen.queryByTestId('review-submit-results')).toBeNull()
    expect(screen.getByTestId('review-bulk-bar')).toBeTruthy()
    expect(screen.getByTestId('review-bulk-submit').textContent, 'both ids still selected').toBe('Submit 2 for transmission')
    expect(onSubmitted).not.toHaveBeenCalled()
  })

  it('two confirms inside one act send one POST', async () => {
    const pending = deferred<MockResponse>()
    const net = stubFetch(() => pending.promise)
    renderTab()
    await selectBothAndArm()
    const confirm = screen.getByTestId('review-bulk-confirm')

    // No re-render between the clicks, so the second reaches submit() with the stale `armed` phase.
    act(() => {
      confirm.click()
      confirm.click()
    })
    await act(async () => {})
    expect(net.posts()).toHaveLength(1)

    pending.resolve(okResults([]))
    await waitFor(() => expect(screen.queryByTestId('review-bulk-bar')).toBeNull())
  })
})
