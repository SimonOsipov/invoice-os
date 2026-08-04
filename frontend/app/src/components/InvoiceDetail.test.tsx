// @vitest-environment jsdom
// RED specs (task-332, BUG-01-06, Mode A) -- the failed-dead-end card (InvoiceDetail.tsx
// :533-542) says nothing about rejection_reasons being empty yet, so the first test below
// fails on the card's actual textContent, not an import/compile error. First render test
// for this component; mirrors InvoicesList.test.tsx's fetch-mock + ctx-cast idiom.
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import { DETAIL_SUBMIT_COPY, skipReasonLabel, type InvoiceDetailRecord, type StatusChange } from '../lib/invoices'
import type { PlatformCtx } from '../types'
import { InvoiceDetail } from './InvoiceDetail'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function detailRecord(over: Partial<InvoiceDetailRecord> = {}): InvoiceDetailRecord {
  return {
    id: 'inv-failed-1',
    entity_id: 'ent-1',
    import_batch_id: null,
    invoice_number: 'INV-FAILED-1',
    status: 'failed',
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
    line_items: [],
    // null (never validated) sidesteps ViolationsTable entirely -- irrelevant to this
    // story's honest-line assertion, which only depends on status/rejection_reasons.
    rule_set_version: null,
    qr_png_base64: null,
    can_edit: false,
    can_revalidate: false,
    revalidate_blocked_reason: null,
    can_submit: false,
    submit_blocked_reason: null,
    ...over,
  }
}

function detailCtx(importedInvoiceId: string): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    selectedId: null,
    importedInvoiceId,
    nav: () => {},
  }
  return ctx as unknown as PlatformCtx
}

interface SubmitCallBody {
  invoice_ids: string[]
  idempotency_key: string
}

interface DetailFetchOptions {
  // Real getInvoice() GET calls after the first (always `detail`) resolve from here in
  // order; the last entry repeats once exhausted. The source-document GET never touches
  // this -- see below.
  detailSequence?: InvoiceDetailRecord[]
  // POST .../invoices/submissions responses, consumed FIFO (last entry repeats once
  // exhausted). Every call is recorded into the returned `submitCalls`, win or lose.
  submitResponses?: MockResponse[]
  editResponse?: MockResponse
  revalidateResponse?: MockResponse
}

// getInvoice and getInvoiceHistory fire concurrently (two independent useAsync effects) --
// dispatched by URL suffix rather than call order, unlike invoices.test.ts's
// mockFetchOnce/mockFetchSequence which only ever mock one endpoint at a time.
//
// Extended for the detail-page submit control: method-aware now, not just URL-suffix --
// editInvoice (PATCH) and getInvoice (GET) hit the SAME URL, so method is what tells them
// apart. GET .../source-document keeps the pre-existing fallback to `detail` but is
// dispatched BEFORE the detail-refetch counter below, so it can never skew
// `detailSequence` indexing.
function mockDetailFetch(detail: InvoiceDetailRecord, history: StatusChange[] = [], opts: DetailFetchOptions = {}) {
  let detailCalls = 0
  const submitCalls: SubmitCallBody[] = []

  const fetchMock = vi.fn((url: string, init: RequestInit = {}) => {
    const method = init.method ?? 'GET'

    if (url.endsWith('/history')) {
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(history) })
    }
    if (method === 'POST' && url.endsWith('/invoices/submissions')) {
      const body = JSON.parse(String(init.body)) as SubmitCallBody
      submitCalls.push(body)
      const responses = opts.submitResponses ?? []
      const resp =
        responses[submitCalls.length - 1] ??
        responses[responses.length - 1] ??
        { ok: true, status: 200, json: () => Promise.resolve({ results: [] }) }
      return Promise.resolve(resp)
    }
    if (method === 'POST' && url.endsWith('/validate')) {
      return Promise.resolve(opts.revalidateResponse ?? { ok: true, status: 200, json: () => Promise.resolve(detail) })
    }
    if (method === 'PATCH') {
      return Promise.resolve(opts.editResponse ?? { ok: true, status: 200, json: () => Promise.resolve(detail) })
    }
    if (url.endsWith('/source-document')) {
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(detail) })
    }
    // getInvoice GET: the first call is always `detail`; later calls consume
    // detailSequence in order, repeating the last entry once exhausted.
    const record =
      detailCalls === 0 ? detail : opts.detailSequence?.[detailCalls - 1] ?? opts.detailSequence?.at(-1) ?? detail
    detailCalls++
    return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(record) })
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, submitCalls }
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('InvoiceDetail failed-dead-end card (task-332, BUG-01-06, [failed-no-reason-lands-on-the-detail])', () => {
  it('AC-3: a failed invoice with no recorded reason renders an explicit "no reason recorded" line', async () => {
    mockDetailFetch(detailRecord({ rejection_reasons: [] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('failed-dead-end')
    expect(card.textContent?.toLowerCase()).toContain('no reason recorded')
  })

  it('AC-4: a failed invoice WITH reasons still renders the rejection card, and gets no "no reason" line', async () => {
    mockDetailFetch(detailRecord({ rejection_reasons: [{ code: 'invalid_tin', message: 'bad tin' }] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('failed-dead-end')
    expect(card.textContent?.toLowerCase()).not.toContain('no reason recorded')
    expect(await screen.findByTestId('rejection-reasons')).toBeDefined()
  })

  // QA Mode B adversarial (task-332, BUG-01-06, point e): the honest line is nested
  // inside the `status === 'failed'` gate (failed-dead-end itself), so a non-failed
  // invoice must never render either the card or the line -- even with rejection_reasons
  // empty, the shape that triggers the line on a FAILED invoice.
  it('a non-failed invoice (rejected, empty rejection_reasons) never renders failed-dead-end or the "no reason" line', async () => {
    mockDetailFetch(detailRecord({ status: 'rejected', rejection_reasons: [] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByText('INV-FAILED-1') // wait for the record to render before asserting absence
    expect(screen.queryByTestId('failed-dead-end')).toBeNull()
    expect(screen.queryByText(/no reason recorded/i)).toBeNull()
  })
})

// RED specs: none of `detail-submit`/`-cancel`/`-confirm`/`-confirm-prompt`/`-skipped`/
// `-error` exist in InvoiceDetail.tsx yet -- every spec here fails on a missing element,
// not a harness/import/type error.
describe('InvoiceDetail submit control ([gates-on-the-wire], [no-bulk-on-detail], [fresh-key-per-confirm])', () => {
  const ID = 'inv-submit-1'
  const queuedResult = { invoice_id: ID, enqueued: true, status: 'queued' }

  it('AC1: renders Submit in the actions bar when the wire says can_submit', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_revalidate: false, can_submit: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const bar = await screen.findByTestId('invoice-actions')
    expect(within(bar).getByTestId('detail-submit')).toBeTruthy()
    expect(within(bar).getByTestId('edit-toggle')).toBeTruthy()
    expect(within(bar).getByTestId('revalidate')).toBeTruthy()
  })

  // INVERTS the pre-INVED-02-05 "never renders Submit when can_submit is false" spec --
  // founder decision reversed hidden -> disabled-with-reason, mirroring Re-validate.
  it("AC2/F-255#1: renders Submit DISABLED with the wire's reason when can_submit is false", async () => {
    const reason = 'Only validated invoices can be submitted — re-validate this invoice first.'
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', can_edit: true, can_revalidate: true, can_submit: false, submit_blocked_reason: reason }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = await screen.findByTestId('detail-submit')
    expect((btn as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByTestId('submit-blocked-reason').textContent).toBe(reason)
  })

  // THE MUTATION ORACLE -- do not soften or drop. Presence stopped being discriminating
  // once Submit always renders; this now rides on the `disabled` property and the
  // absence of reason text. A component that re-derives `status === 'validated'` yields
  // disabled=true here; one that derives the reason client-side renders text here.
  it('F-255#1: renders Submit off the wire flag, not the status -- can_submit:true on a rejected invoice renders it enabled', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'rejected', can_edit: true, can_submit: true, submit_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = await screen.findByTestId('detail-submit')
    expect((btn as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByTestId('submit-blocked-reason')).toBeNull()
  })

  it('AC2/F-255#2: clicking Submit arms a confirmation and sends nothing', async () => {
    const { submitCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))

    const prompt = await screen.findByTestId('detail-submit-confirm-prompt')
    expect(prompt.textContent).toContain(DETAIL_SUBMIT_COPY.prompt)
    expect(prompt.textContent).toContain(DETAIL_SUBMIT_COPY.detail)
    expect(screen.getByTestId('detail-submit-confirm')).toBeTruthy()
    expect(screen.getByTestId('detail-submit-cancel')).toBeTruthy()
    expect(submitCalls).toHaveLength(0)
  })

  it('AC2/F-255#2: cancelling the confirmation sends nothing and restores the idle bar', async () => {
    const { submitCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    await screen.findByTestId('detail-submit-confirm-prompt')
    fireEvent.click(screen.getByTestId('detail-submit-cancel'))

    await waitFor(() => expect(screen.queryByTestId('detail-submit-confirm-prompt')).toBeNull())
    expect(screen.getByTestId('detail-submit')).toBeTruthy()
    expect(submitCalls).toHaveLength(0)
  })

  it('AC3/[no-bulk-on-detail]: confirming posts exactly one invoice id with an idempotency key', async () => {
    const { submitCalls } = mockDetailFetch(
      detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }),
      [],
      { submitResponses: [{ ok: true, status: 200, json: () => Promise.resolve({ results: [queuedResult] }) }] },
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))
    await screen.findByTestId('detail-submit') // settled back to idle after the refetch

    expect(submitCalls).toHaveLength(1)
    expect(submitCalls[0].invoice_ids).toEqual([ID])
    expect(typeof submitCalls[0].idempotency_key).toBe('string')
    expect(submitCalls[0].idempotency_key.length).toBeGreaterThan(0)
  })

  it('[fresh-key-per-confirm]: a retry after a failed submit mints a different idempotency key', async () => {
    const afterRetry = detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: false })
    const { submitCalls } = mockDetailFetch(
      detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }),
      [],
      {
        detailSequence: [afterRetry],
        submitResponses: [
          { ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) },
          { ok: true, status: 200, json: () => Promise.resolve({ results: [queuedResult] }) },
        ],
      },
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))
    await screen.findByTestId('detail-submit-error')

    fireEvent.click(screen.getByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))
    await waitFor(() => expect(screen.getByTestId('invoice-status-badge').textContent).toContain('QUEUED'))

    expect(submitCalls).toHaveLength(2)
    expect(submitCalls[0].idempotency_key).not.toBe(submitCalls[1].idempotency_key)
  })

  it('AC2: a double-click on Confirm fires exactly one request', async () => {
    const { submitCalls } = mockDetailFetch(
      detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }),
      [],
      { submitResponses: [{ ok: true, status: 200, json: () => Promise.resolve({ results: [queuedResult] }) }] },
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    const confirmBtn = screen.getByTestId('detail-submit-confirm')
    fireEvent.click(confirmBtn)
    fireEvent.click(confirmBtn) // in-flight ref must win here, not the (not-yet-rerendered) disabled attribute

    await screen.findByTestId('detail-submit') // settled back to idle after the single request resolves
    expect(submitCalls).toHaveLength(1)
  })

  it('AC3/F-255#3: a successful submit refetches the record and the timeline without navigating', async () => {
    const queued = detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: false })
    const { fetchMock } = mockDetailFetch(
      detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }),
      [],
      {
        detailSequence: [queued],
        submitResponses: [{ ok: true, status: 200, json: () => Promise.resolve({ results: [queuedResult] }) }],
      },
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))

    await waitFor(() => expect(screen.getByTestId('invoice-status-badge').textContent).toContain('QUEUED'))
    expect(screen.getByTestId('invoice-detail')).toBeTruthy()
    const historyCalls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/history'))
    expect(historyCalls.length).toBeGreaterThanOrEqual(2)
  })

  it('AC4: a rejected verdict renders its reasons on the page that submitted', async () => {
    const rejected = detailRecord({
      id: ID,
      status: 'rejected',
      can_edit: true,
      can_submit: false,
      rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
    })
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }), [], {
      detailSequence: [rejected],
      submitResponses: [{ ok: true, status: 200, json: () => Promise.resolve({ results: [queuedResult] }) }],
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))

    await screen.findByTestId('rejection-reasons')
    const rows = await screen.findAllByTestId('rejection-reason-row')
    expect(rows.some((r) => r.textContent?.includes('NGE-4102'))).toBe(true)
    expect(screen.getByTestId('invoice-detail')).toBeTruthy()
    expect(screen.queryByTestId('detail-submit')).toBeNull()
  })

  it('[never-report-success-on-a-skip]: a duplicate_request skip is never reported as sent', async () => {
    const trueState = detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: false })
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }), [], {
      detailSequence: [trueState],
      submitResponses: [
        {
          ok: true,
          status: 200,
          json: () => Promise.resolve({ results: [{ invoice_id: ID, enqueued: false, status: 'queued', reason: 'duplicate_request' }] }),
        },
      ],
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))

    const skip = await screen.findByTestId('detail-submit-skipped')
    expect(skip.textContent).toContain(skipReasonLabel('duplicate_request'))
    expect(screen.queryByTestId('detail-submit-error')).toBeNull()
    await waitFor(() => expect(screen.getByTestId('invoice-status-badge').textContent).toContain('QUEUED'))
  })

  it('AC5: a not_validated skip renders the reason and leaves the invoice submittable', async () => {
    const stillSubmittable = detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true })
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }), [], {
      detailSequence: [stillSubmittable],
      submitResponses: [
        {
          ok: true,
          status: 200,
          json: () => Promise.resolve({ results: [{ invoice_id: ID, enqueued: false, status: 'validated', reason: 'not_validated' }] }),
        },
      ],
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))

    const skip = await screen.findByTestId('detail-submit-skipped')
    expect(skip.textContent).toContain(skipReasonLabel('not_validated'))
    expect(screen.getByTestId('detail-submit')).toBeTruthy()
  })

  it('a request-level failure surfaces an error, not a skip', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }), [], {
      submitResponses: [{ ok: false, status: 500, json: () => Promise.resolve({ error: 'Internal error' }) }],
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))

    await screen.findByTestId('detail-submit-error')
    expect(screen.queryByTestId('detail-submit-skipped')).toBeNull()
    expect(screen.getByTestId('detail-submit')).toBeTruthy()
  })

  // Stale-banner oracle 1/2: handleSaved resets independently of handleRevalidate -- a fix
  // applied to one leaves the other green while still broken, hence two separate specs.
  it('a skip banner does not survive a later successful edit', async () => {
    const afterSkip = detailRecord({ id: ID, invoice_number: 'INV-STALE-1', status: 'validated', can_edit: true, can_submit: true, buyer_name: 'Beta Ltd' })
    const afterEdit = detailRecord({ id: ID, invoice_number: 'INV-STALE-1-EDITED', status: 'validated', can_edit: true, can_submit: true, buyer_name: 'Beta Ltd 2' })
    mockDetailFetch(
      detailRecord({ id: ID, invoice_number: 'INV-STALE-1', status: 'validated', can_edit: true, can_submit: true, buyer_name: 'Beta Ltd' }),
      [],
      {
        detailSequence: [afterSkip, afterEdit],
        submitResponses: [
          {
            ok: true,
            status: 200,
            json: () => Promise.resolve({ results: [{ invoice_id: ID, enqueued: false, status: 'validated', reason: 'not_validated' }] }),
          },
        ],
        editResponse: { ok: true, status: 200, json: () => Promise.resolve(afterEdit) },
      },
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))
    await screen.findByTestId('detail-submit-skipped')

    fireEvent.click(screen.getByTestId('edit-toggle'))
    const buyerInput = await screen.findByDisplayValue('Beta Ltd')
    fireEvent.change(buyerInput, { target: { value: 'Beta Ltd 2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))

    await screen.findByText('INV-STALE-1-EDITED')
    expect(screen.queryByTestId('detail-submit-skipped')).toBeNull()
  })

  // Stale-banner oracle 2/2: companion to the edit spec above, for handleRevalidate.
  it('an error banner does not survive a later successful re-validate', async () => {
    const afterRevalidate = detailRecord({
      id: ID,
      invoice_number: 'INV-STALE-2-REVALIDATED',
      status: 'validated',
      can_edit: true,
      can_revalidate: false,
      can_submit: true,
      revalidate_blocked_reason: 'Already validated.',
    })
    mockDetailFetch(
      detailRecord({ id: ID, invoice_number: 'INV-STALE-2', status: 'validated', can_edit: true, can_revalidate: true, can_submit: true }),
      [],
      {
        detailSequence: [afterRevalidate],
        submitResponses: [{ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) }],
        revalidateResponse: { ok: true, status: 200, json: () => Promise.resolve(afterRevalidate) },
      },
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))
    await screen.findByTestId('detail-submit-error')

    fireEvent.click(screen.getByTestId('revalidate'))
    await screen.findByText('INV-STALE-2-REVALIDATED')
    expect(screen.queryByTestId('detail-submit-error')).toBeNull()
  })

  it('clicking the disabled Submit sends nothing and does not arm', async () => {
    const reason = 'Only validated invoices can be submitted — re-validate this invoice first.'
    const { submitCalls } = mockDetailFetch(
      detailRecord({ id: ID, status: 'draft', can_edit: true, can_revalidate: true, can_submit: false, submit_blocked_reason: reason }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))

    expect(screen.queryByTestId('detail-submit-confirm-prompt')).toBeNull()
    expect(screen.queryByTestId('detail-submit-confirm')).toBeNull()
    expect(submitCalls).toHaveLength(0)
  })

  it('the blocked reason renders verbatim from the wire, not from a client status map', async () => {
    const sentinel = 'Sentinel reason ZZQ-7.'
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', can_edit: true, can_revalidate: true, can_submit: false, submit_blocked_reason: sentinel }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const reasonEl = await screen.findByTestId('submit-blocked-reason')
    expect(reasonEl.textContent).toBe(sentinel)
  })

  it('when the wire says submittable, Submit is enabled and no reason text renders', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true, submit_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = await screen.findByTestId('detail-submit')
    expect((btn as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByTestId('submit-blocked-reason')).toBeNull()
  })

  it('a rejected invoice renders both blocked reasons side by side', async () => {
    const revalidateReason = 'Only draft invoices can be re-validated — edit this invoice to return it to draft.'
    const submitReason = 'Only validated invoices can be submitted — edit this invoice and re-validate it first.'
    mockDetailFetch(
      detailRecord({
        id: ID,
        status: 'rejected',
        can_edit: true,
        can_revalidate: false,
        can_submit: false,
        revalidate_blocked_reason: revalidateReason,
        submit_blocked_reason: submitReason,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const revalidateEl = await screen.findByTestId('revalidate-blocked-reason')
    const submitEl = await screen.findByTestId('submit-blocked-reason')
    expect(revalidateEl.textContent).toBe(revalidateReason)
    expect(submitEl.textContent).toBe(submitReason)
    expect(revalidateEl.textContent).not.toBe(submitEl.textContent)
  })

  it('the disabled Submit points aria-describedby at its own reason element and mirrors it in title', async () => {
    const reason = 'Only validated invoices can be submitted — re-validate this invoice first.'
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', can_edit: true, can_revalidate: true, can_submit: false, submit_blocked_reason: reason }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = await screen.findByTestId('detail-submit')
    const reasonEl = await screen.findByTestId('submit-blocked-reason')
    const describedBy = btn.getAttribute('aria-describedby')
    expect(describedBy).toBe(reasonEl.id)
    expect(btn.getAttribute('title')).toBe(reason)
    // 'revalidate-blocked-reason-text' mirrors REVALIDATE_REASON_ID (InvoiceDetail.tsx) --
    // guards against a copy-paste id collision between the two reason elements.
    expect(describedBy).not.toBe('revalidate-blocked-reason-text')
  })
})
