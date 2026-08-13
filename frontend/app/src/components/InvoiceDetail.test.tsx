// @vitest-environment jsdom
// RED specs (task-332, BUG-01-06, Mode A) -- the failed-dead-end card (InvoiceDetail.tsx
// :533-542) says nothing about rejection_reasons being empty yet, so the first test below
// fails on the card's actual textContent, not an import/compile error. First render test
// for this component; mirrors InvoicesList.test.tsx's fetch-mock + ctx-cast idiom.
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS } from '../auth'
import { createAuthedFetch } from '../lib/authedFetch'
import { fmtDateTime } from '../lib/format'
import {
  DETAIL_SUBMIT_COPY,
  FAILURE_EXPLANATION_FALLBACK,
  FAILURE_KINDS,
  LIVE_POLL_MS,
  failureExplanation,
  isBuyerTinMissing,
  skipReasonLabel,
  type InvoiceDetailRecord,
  type InvoiceListResponse,
  type InvoiceStatus,
  type StatusChange,
} from '../lib/invoices'
import { ROW_EXPANSION_COPY } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { InvoiceDetail } from './InvoiceDetail'
import { InvoicesList } from './InvoicesList'
import { Row } from './ReviewRow'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
  // The UBL route is served as text (apiFetch responseType:'text'), never JSON.
  text?: () => Promise<string>
}

const UBL_FIXTURE = '<?xml version="1.0" encoding="UTF-8"?>\n<Invoice/>\n'

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
    failure_kind: null,
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
    can_view_ubl: true,
    ubl_blocked_reason: null,
    can_resolve_outside: false,
    resolve_outside_blocked_reason: null,
    can_approve: false,
    approve_blocked_reason: null,
    can_reject: false,
    reject_blocked_reason: null,
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

interface ResolveCall {
  method: 'POST' | 'DELETE'
  body: { reason: string } | null
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
  resolveResponse?: MockResponse
  unresolveResponse?: MockResponse
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
  const resolveCalls: ResolveCall[] = []

  const fetchMock = vi.fn((url: string, init: RequestInit = {}) => {
    const method = init.method ?? 'GET'

    if (url.endsWith('/history')) {
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(history) })
    }
    // resolveInvoiceOutside/unresolveInvoiceOutside (lib/invoices.ts:614/626) -- must be
    // dispatched before the GET fallback below, or a POST/DELETE here silently falls
    // through and answers with the unrelated detail-fetch branch.
    if (url.endsWith('/resolved-outside')) {
      if (method === 'DELETE') {
        resolveCalls.push({ method: 'DELETE', body: null })
        return Promise.resolve<MockResponse>(
          opts.unresolveResponse ?? {
            ok: true,
            status: 200,
            json: () => Promise.resolve({ ...detail, kept_as_is_at: null, kept_as_is_by: null, kept_as_is_reason: null }),
          },
        )
      }
      const body = JSON.parse(String(init.body)) as { reason: string }
      resolveCalls.push({ method: 'POST', body })
      return Promise.resolve<MockResponse>(
        opts.resolveResponse ?? {
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              ...detail,
              kept_as_is_at: '2026-08-06T12:00:00Z',
              kept_as_is_by: APP_PERSONAS.firm.subject,
              kept_as_is_reason: body.reason,
            }),
        },
      )
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
    // Dispatched BEFORE the detail-refetch counter, like /source-document below: without
    // its own arm the viewer's GET falls through to the fallback, consuming a
    // detailSequence slot and answering with a body that has no text().
    if (url.endsWith('/ubl')) {
      return Promise.resolve<MockResponse>({
        ok: true,
        status: 200,
        json: () => Promise.reject(new Error('the ubl route is text, not json')),
        text: () => Promise.resolve(UBL_FIXTURE),
      })
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
  return { fetchMock, submitCalls, resolveCalls }
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('InvoiceDetail failed-dead-end card (task-388, BUG-06-06, [failed-no-reason-lands-on-the-detail])', () => {
  it('AC-7: a failed invoice with failure_kind null renders the fallback explanation, not the deleted "no reason recorded" line', async () => {
    mockDetailFetch(detailRecord({ failure_kind: null, rejection_reasons: [] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('failed-dead-end')
    expect(card.textContent?.toLowerCase()).not.toContain('no reason recorded')
    expect(screen.getByTestId('failure-headline').textContent).toBe(FAILURE_EXPLANATION_FALLBACK.headline)
    expect(screen.getByTestId('failure-detail').textContent).toBe(FAILURE_EXPLANATION_FALLBACK.detail)
    expect(screen.getByTestId('failure-next-step').textContent).toBe(FAILURE_EXPLANATION_FALLBACK.nextStep)
  })

  // The trap: a fixture with a real failure_kind AND historical rejection reasons must
  // render the REAL kind's copy, not the fallback -- "recorded" appears only in the
  // fallback strings, so its absence proves the real kind rendered.
  it('AC-4: a failed invoice with a recorded kind AND historical rejection reasons renders the real explanation, not the fallback, alongside the rejection card', async () => {
    mockDetailFetch(detailRecord({
      failure_kind: 'never_acknowledged',
      rejection_reasons: [{ code: 'invalid_tin', message: 'bad tin' }],
    }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('failed-dead-end')
    expect(card.textContent?.toLowerCase()).not.toContain('recorded')
    const expected = failureExplanation('never_acknowledged')
    expect(screen.getByTestId('failure-headline').textContent).toBe(expected.headline)
    expect(screen.getByTestId('failure-detail').textContent).toBe(expected.detail)
    expect(screen.getByTestId('failure-next-step').textContent).toBe(expected.nextStep)
    expect(await screen.findByTestId('rejection-reasons')).toBeDefined()
  })

  // QA Mode B adversarial (task-332, BUG-01-06, point e): the explanation is nested
  // inside the `status === 'failed'` gate (failed-dead-end itself), so a non-failed
  // invoice must never render the card or any of its three testid blocks.
  it('a non-failed invoice (rejected, empty rejection_reasons) never renders failed-dead-end or any failure-explanation element', async () => {
    mockDetailFetch(detailRecord({ status: 'rejected', rejection_reasons: [] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByText('INV-FAILED-1') // wait for the record to render before asserting absence
    expect(screen.queryByTestId('failed-dead-end')).toBeNull()
    expect(screen.queryByTestId('failure-headline')).toBeNull()
    expect(screen.queryByTestId('failure-detail')).toBeNull()
    expect(screen.queryByTestId('failure-next-step')).toBeNull()
  })

  // QA Mode B adversarial (task-388): table-driven over all four rendered states -- the
  // three recorded kinds plus null -- so no state can silently lose a line the way the
  // old AC-3/AC-4 tests only ever exercised null and one kind.
  it.each([
    ...FAILURE_KINDS.map((k) => [k, failureExplanation(k)] as const),
    [null, FAILURE_EXPLANATION_FALLBACK] as const,
  ])('failure_kind=%s renders that exact headline/detail/nextStep, nothing else', async (kind, expected) => {
    mockDetailFetch(detailRecord({ failure_kind: kind, rejection_reasons: [] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByTestId('failed-dead-end')
    expect(screen.getByTestId('failure-headline').textContent).toBe(expected.headline)
    expect(screen.getByTestId('failure-detail').textContent).toBe(expected.detail)
    expect(screen.getByTestId('failure-next-step').textContent).toBe(expected.nextStep)
  })

  // Forward-compatibility (AC-7's implication): a kind the SPA has never heard of --
  // e.g. a future backend value -- must still land on the fallback, not a blank card.
  it('an unrecognised failure_kind renders the fallback explanation, not a blank card', async () => {
    mockDetailFetch(detailRecord({ failure_kind: 'something_new', rejection_reasons: [] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByTestId('failed-dead-end')
    expect(screen.getByTestId('failure-headline').textContent).toBe(FAILURE_EXPLANATION_FALLBACK.headline)
    expect(screen.getByTestId('failure-detail').textContent).toBe(FAILURE_EXPLANATION_FALLBACK.detail)
    expect(screen.getByTestId('failure-next-step').textContent).toBe(FAILURE_EXPLANATION_FALLBACK.nextStep)
  })

  // [actions-bar-gate-stands], pinned structurally rather than by inspection: the panel
  // is diagnosis only, so no clickable control may ever appear inside it -- resolve-outside
  // excepted, the one affordance this card is allowed to carry (Core AC #6: it marks, it
  // never re-drives/retries/submits). Rescoped from a blanket zero-button assertion, which
  // the resolve-outside control (always rendered, disabled or not, on a `failed` invoice)
  // makes too broad.
  it('the failed-dead-end card carries no link and no button beyond resolve-outside -- never a re-drive control', async () => {
    mockDetailFetch(detailRecord({ failure_kind: 'acknowledged_no_verdict', rejection_reasons: [] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('failed-dead-end')
    expect(card.querySelectorAll('a').length).toBe(0)
    const buttons = Array.from(card.querySelectorAll('button'))
    expect(buttons.map((b) => b.dataset.testid)).toEqual(['resolve-outside'])
    expect(buttons.some((b) => /submit|retry|re-?drive|resubmit|validate/i.test(b.textContent ?? ''))).toBe(false)
  })
})

// RED specs (Mode A): only Test B is red today -- the rejection card always renders
// below Compliance regardless of status. A, C, D, E characterise properties already
// true today; this story gives them their first oracle.
//
// FIXTURE GOTCHA: detailRecord()'s default rule_set_version is null, which renders
// `not-validated` instead of `violations-table` -- every positional test below overrides
// it to a real number so violations-table exists to compare against.
describe('InvoiceDetail terminal rail order', () => {
  it('AC-1: on a failed invoice, failed-dead-end precedes violations-table', async () => {
    mockDetailFetch(detailRecord({ status: 'failed', rule_set_version: 3 }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('failed-dead-end')
    const table = await screen.findByTestId('violations-table')
    expect(card.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('AC-2: on a rejected invoice, rejection-reasons precedes violations-table', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'rejected',
        rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
        rule_set_version: 3,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('rejection-reasons')
    const table = await screen.findByTestId('violations-table')
    expect(card.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('AC-3: a historical rejection (demoted draft) titles "Last APP rejection" and stays below violations-table', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'draft',
        rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
        rule_set_version: 3,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('rejection-reasons')
    expect(within(card).getByText('Last APP rejection')).toBeTruthy()
    const table = screen.getByTestId('violations-table')
    expect(table.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('AC-4: the rejection card renders at most once per page', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'rejected',
        rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByTestId('rejection-reasons')
    expect(screen.getAllByTestId('rejection-reasons')).toHaveLength(1)
  })

  it('AC-4: the accepted-invoice backstop still fully suppresses the rejection card', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'accepted',
        rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByText('INV-FAILED-1')
    expect(screen.queryByTestId('rejection-reasons')).toBeNull()
  })

  // rejected->draft retains rejection_reasons ([reason-lifecycle], only `accepted`
  // clears them), so draft->validated->queued->failed is a real path to a failed
  // invoice with stale reasons still attached -- both cards render, failed leading.
  it('a failed invoice carrying stale rejection_reasons renders both cards, failed leading', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'failed',
        rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
        rule_set_version: 3,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const failedCard = await screen.findByTestId('failed-dead-end')
    const table = await screen.findByTestId('violations-table')
    const rejectionCards = screen.getAllByTestId('rejection-reasons')
    expect(rejectionCards).toHaveLength(1)
    const rejectionCard = rejectionCards[0]

    expect(failedCard.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(table.compareDocumentPosition(rejectionCard) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(rejectionCard).getByText('Last APP rejection')).toBeTruthy()
  })

  it('a rejected invoice with an empty rejection_reasons array renders no rejection card at all', async () => {
    mockDetailFetch(detailRecord({ status: 'rejected', rejection_reasons: [], rule_set_version: 3 }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByTestId('violations-table')
    expect(screen.queryByTestId('rejection-reasons')).toBeNull()
  })

  it('AC-2 extended: the live rejection card also precedes source-document-card and status-history', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'rejected',
        rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
        rule_set_version: 3,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('rejection-reasons')
    const doc = await screen.findByTestId('source-document-card')
    const history = screen.getByTestId('status-history')
    expect(card.compareDocumentPosition(doc) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(card.compareDocumentPosition(history) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
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
    // Post-INVED-02-05: a rejected, editable invoice renders Submit disabled, not hidden
    // (AC #6) -- this incidentally checks that, not the rejection-reasons assertions above.
    expect((screen.getByTestId('detail-submit') as HTMLButtonElement).disabled).toBe(true)
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

  // APPR-08-04: the detail screen is skipReasonLabel's third production consumer and the
  // only one that renders a single invoice's skip as a banner. submitGate never consults
  // the approval run, so an approver sees an ENABLED Submit on a gated invoice and this
  // banner is the whole explanation. The copy is asserted verbatim, not via
  // skipReasonLabel, because this is where it meets the operator.
  it('an awaiting_approval skip renders the reason and leaves the invoice submittable', async () => {
    const stillSubmittable = detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true })
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }), [], {
      detailSequence: [stillSubmittable],
      submitResponses: [
        {
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              results: [{ invoice_id: ID, enqueued: false, status: 'validated', reason: 'awaiting_approval' }],
            }),
        },
      ],
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))

    const skip = await screen.findByTestId('detail-submit-skipped')
    expect(skip.textContent).toContain('Waiting on approval — an approver must approve it first')
    expect(skip.textContent).not.toContain('awaiting_approval')
    expect(screen.queryByTestId('detail-submit-error')).toBeNull()
    expect(screen.getByTestId('detail-submit')).toBeTruthy()
    await waitFor(() => expect(screen.getByTestId('invoice-status-badge').textContent).toContain('VALIDATED'))
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

  // QA adversarial (mutation survivor): removing `filter: 'none'` from the disabled
  // spread passes every other spec -- .v2-btn-primary:hover sets filter:brightness(1.22)
  // unguarded, so a disabled Submit would still brighten under the cursor without this.
  it('the disabled Submit neutralises filter, guarding .v2-btn-primary:hover from brightening it', async () => {
    const reason = 'Only validated invoices can be submitted — re-validate this invoice first.'
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', can_edit: true, can_revalidate: true, can_submit: false, submit_blocked_reason: reason }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = await screen.findByTestId('detail-submit')

    expect(btn.style.filter).toBe('none')
  })

  // QA adversarial (mutation survivor): widening the bar's gate to `can_edit ||
  // can_submit` passes every other spec, because can_submit:true implies can_edit:true on
  // every REAL wire shape (TestCanSubmit_ImpliesCanEdit) -- the Go tripwire proves the
  // implication, not that the SPA didn't also widen. Synthetic/contradictory fixture
  // isolates the SPA's own gate.
  it('the actions bar stays gated on can_edit alone -- a wire with can_submit:true but can_edit:false renders no bar at all', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: true, submit_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-status-badge')

    expect(screen.queryByTestId('invoice-actions')).toBeNull()
    expect(screen.queryByTestId('detail-submit')).toBeNull()
  })

  // Follow-up flagged in the executor's own Implementation Notes (structural deviation
  // that moved the skip/error banners outside the `can_edit` gate): the banner surviving
  // a can_edit:false refetch must not drag actionable controls out with it.
  //
  // BUG-04-05 amends the boundary, additively: `view-ubl` DOES render here now, and that
  // is not a widening of this rule. What no banner may drag out is a *lifecycle* control
  // -- edit / re-validate / submit, the three that live behind `can_edit`. The read-only
  // UBL viewer never sat behind that gate ([ubl-button-outside-invoice-actions]): it reads
  // can_view_ubl, which tracks CONTENT completeness and is status-independent. Asserted
  // rather than left implicit, so the four `toBeNull()`s below can never be read as
  // "nothing at all renders beside the banner".
  it('a duplicate_request skip banner surviving a can_edit:false refetch renders no actionable buttons alongside it', async () => {
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

    await screen.findByTestId('detail-submit-skipped')
    expect(screen.queryByTestId('invoice-actions')).toBeNull()
    expect(screen.queryByTestId('edit-toggle')).toBeNull()
    expect(screen.queryByTestId('revalidate')).toBeNull()
    expect(screen.queryByTestId('detail-submit')).toBeNull()
    // The read-only viewer is the one control that stays -- see this test's comment.
    expect(screen.getByTestId('view-ubl')).toBeTruthy()
  })

  // Layer 3 (the visible reason text) is the whole justification for rendering disabled
  // rather than hidden -- a disabled button is out of the tab order, so the reason must
  // stay reachable even though the button itself is not.
  it('the disabled Submit refuses focus, and Enter/Space never arm the confirmation', async () => {
    const reason = 'Only validated invoices can be submitted — re-validate this invoice first.'
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', can_edit: true, can_revalidate: true, can_submit: false, submit_blocked_reason: reason }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = await screen.findByTestId('detail-submit')

    btn.focus()
    expect(document.activeElement).not.toBe(btn)

    for (const key of ['Enter', ' ']) {
      fireEvent.keyDown(btn, { key })
      fireEvent.keyUp(btn, { key })
    }
    expect(screen.queryByTestId('detail-submit-confirm-prompt')).toBeNull()
    expect(screen.getByTestId('submit-blocked-reason').textContent).toBe(reason)
  })

  // A contradictory wire (can_submit:true with a non-null submit_blocked_reason -- never
  // produced by the real backend, G1/G2/G4 pin the two mutually exclusive) must not
  // confuse the button's own enabled state, which reads can_submit alone.
  it('a contradictory wire (can_submit:true with a non-null submit_blocked_reason) still enables Submit', async () => {
    const reason = 'Only validated invoices can be submitted — re-validate this invoice first.'
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true, submit_blocked_reason: reason }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = await screen.findByTestId('detail-submit')

    expect((btn as HTMLButtonElement).disabled).toBe(false)
  })

  // Arm, then let a poll tick (synthetic can_edit:true override on 'queued' so
  // shouldPollInvoice stays active while the bar still renders) overwrite the overlay's
  // can_submit to false before Confirm is clicked. The poll never touches submitPhase, so
  // the armed UI survives untouched -- handleSubmit's own `!inv.can_submit` guard, re-read
  // fresh on the render that owns the click, is what must stop the request. Real wait, not
  // fake timers -- matches InvoicesList.test.tsx's AC-6 poll-test convention (fake-timer/
  // act() interaction pitfalls).
  it(
    "an armed confirm honours a poll tick that flips can_submit false before Confirm is clicked",
    async () => {
      const initial = detailRecord({ id: ID, status: 'queued', can_edit: true, can_submit: true, submit_blocked_reason: null })
      const afterPoll = detailRecord({ id: ID, status: 'queued', can_edit: true, can_submit: false, submit_blocked_reason: 'Only validated invoices can be submitted.' })
      const { submitCalls, fetchMock } = mockDetailFetch(initial, [], { detailSequence: [afterPoll] })

      render(<InvoiceDetail ctx={detailCtx(ID)} />)
      fireEvent.click(await screen.findByTestId('detail-submit'))
      await screen.findByTestId('detail-submit-confirm-prompt')

      const detailGetCalls = () =>
        fetchMock.mock.calls.filter(([url, init]: [string, RequestInit?]) => {
          const method = init?.method ?? 'GET'
          const u = String(url)
          return method === 'GET' && !u.endsWith('/history') && !u.endsWith('/source-document')
        })
      // 1 initial mount fetch + 1 poll tick fetch.
      await waitFor(() => expect(detailGetCalls().length).toBeGreaterThanOrEqual(2), { timeout: LIVE_POLL_MS + 1500, interval: 100 })

      fireEvent.click(screen.getByTestId('detail-submit-confirm'))

      expect(submitCalls).toHaveLength(0)
    },
    LIVE_POLL_MS + 5000,
  )
})

// The ROLE refusal (notApproverTransmitReason, handlers.go) rather than a status one. The
// specs above all fixture a status sentence, which a component that re-derived copy from
// `status` could still satisfy; a preparer's block is invisible to any status map. Em dash
// is U+2014, copied from the Go const -- a hyphen makes every toBe below vacuous.
describe('InvoiceDetail: a preparer sees the role refusal, verbatim (APPR-01 AC-5)', () => {
  const ID = 'inv-role-blocked-1'
  const ROLE_REASON = 'Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team.'

  it('a preparer on a validated invoice sees Submit disabled with the role sentence as visible text', async () => {
    // `validated` is the status where a preparer actually meets the block: can_edit is true,
    // so the bar renders, and can_submit is false only because of the role.
    mockDetailFetch(
      detailRecord({ id: ID, status: 'validated', can_edit: true, can_revalidate: false, can_submit: false, submit_blocked_reason: ROLE_REASON }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = await screen.findByTestId('detail-submit')
    expect((btn as HTMLButtonElement).disabled).toBe(true)
    // Visible text node, not only the title attribute ([revalidate-visibility]).
    expect(screen.getByTestId('submit-blocked-reason').textContent).toBe(ROLE_REASON)
  })

  it('the disabled Submit points aria-describedby at the role reason and mirrors it in title', async () => {
    mockDetailFetch(
      detailRecord({ id: ID, status: 'validated', can_edit: true, can_revalidate: false, can_submit: false, submit_blocked_reason: ROLE_REASON }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = await screen.findByTestId('detail-submit')
    const reasonEl = await screen.findByTestId('submit-blocked-reason')
    expect(btn.getAttribute('aria-describedby')).toBe(reasonEl.id)
    expect(btn.getAttribute('title')).toBe(ROLE_REASON)
    expect(btn.getAttribute('aria-describedby')).not.toBe('revalidate-blocked-reason-text')
  })

  it('clicking the role-blocked Submit sends nothing and does not arm', async () => {
    const { submitCalls } = mockDetailFetch(
      detailRecord({ id: ID, status: 'validated', can_edit: true, can_revalidate: false, can_submit: false, submit_blocked_reason: ROLE_REASON }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))

    expect(screen.queryByTestId('detail-submit-confirm-prompt')).toBeNull()
    expect(screen.queryByTestId('detail-submit-confirm')).toBeNull()
    expect(submitCalls).toHaveLength(0)
  })

  // Pins a DELIBERATE silence, so the invariant comment at InvoiceDetail.tsx's submit
  // reason node is not later read as a bug report. submitGate's role arm emits the sentence
  // on every status, but the reason node lives inside the can_edit-gated actions bar -- on a
  // queued invoice there is no Submit control to explain, so nothing renders. Do not widen
  // that gate; 'the actions bar stays gated on can_edit alone' is its mutation oracle.
  it("a preparer's role sentence on a non-editable invoice renders no bar and no reason", async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: false, submit_blocked_reason: ROLE_REASON }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    // Positive companion: the record really rendered, so the absences below are not an
    // empty document.
    await screen.findByTestId('invoice-status-badge')

    expect(screen.queryByTestId('invoice-actions')).toBeNull()
    expect(screen.queryByTestId('detail-submit')).toBeNull()
    expect(screen.queryByTestId('submit-blocked-reason')).toBeNull()
    expect(document.body.textContent).not.toContain(ROLE_REASON)
  })

  // The only shape where BOTH reason nodes render at once: rejected keeps can_edit true, so
  // the bar is up, can_revalidate is false, and the role arm overrides the status sentence
  // for Submit. Existing id-collision guards ('...points aria-describedby at its own reason
  // element...') run on draft, where the revalidate node is absent -- a shared id would go
  // unseen there. Resolves each describedby through getElementById rather than comparing
  // strings, so a swap that keeps both ids distinct still fails.
  it('a preparer on a rejected invoice gets both reasons on distinct nodes, each control describing its own', async () => {
    const revalidateReason = 'Only draft invoices can be re-validated — edit this invoice to return it to draft.'
    mockDetailFetch(
      detailRecord({
        id: ID,
        status: 'rejected',
        can_edit: true,
        can_revalidate: false,
        revalidate_blocked_reason: revalidateReason,
        can_submit: false,
        submit_blocked_reason: ROLE_REASON,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const submitEl = await screen.findByTestId('submit-blocked-reason')
    const revalidateEl = screen.getByTestId('revalidate-blocked-reason')
    expect(submitEl.id).not.toBe(revalidateEl.id)
    expect(submitEl.textContent).toBe(ROLE_REASON)
    expect(revalidateEl.textContent).toBe(revalidateReason)

    const submitBtn = screen.getByTestId('detail-submit')
    const revalidateBtn = screen.getByTestId('revalidate')
    expect(document.getElementById(submitBtn.getAttribute('aria-describedby') ?? '')).toBe(submitEl)
    expect(document.getElementById(revalidateBtn.getAttribute('aria-describedby') ?? '')).toBe(revalidateEl)
    expect(submitBtn.getAttribute('title')).toBe(ROLE_REASON)
    expect(revalidateBtn.getAttribute('title')).toBe(revalidateReason)
  })

  // Anti-drift on the ROLE-blocked shape. The existing sentinel spec ('...not from a client
  // status map') fixtures draft, which a component re-deriving copy from status could still
  // pass by keying its map on draft alone; validated + can_submit:false is reachable ONLY
  // via the role, so a sentinel here has no status the SPA could have derived it from.
  it('a sentence the SPA has never seen renders verbatim on the validated role-blocked shape', async () => {
    const sentinel = 'Sentinel role refusal QQV-42 — not any shipped copy.'
    mockDetailFetch(
      detailRecord({ id: ID, status: 'validated', can_edit: true, can_revalidate: false, can_submit: false, submit_blocked_reason: sentinel }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const reasonEl = await screen.findByTestId('submit-blocked-reason')
    expect(reasonEl.textContent).toBe(sentinel)
    expect(screen.getByTestId('detail-submit').getAttribute('title')).toBe(sentinel)
    // No shipped sentence leaks in beside it.
    expect(document.body.textContent).not.toContain('ask an appro' + 'ver on your team')
    expect(document.body.textContent).not.toContain('Only validated invoices can be sub' + 'mitted')
  })
})

// RED specs (task-392, BUG-03-03, Mode A). Every demo-data fixture carries the literal
// actor 'system', which renders fine today and hides the raw-UUID defect -- these pass a
// real StatusChange[] through mockDetailFetch instead of relying on the [] default.
describe('InvoiceDetail status history: actor resolution ([actor-label-shared])', () => {
  it('AC2: the status history renders a person, not a subject uuid', async () => {
    const history: StatusChange[] = [
      { from_status: null, to_status: 'draft', changed_at: '2026-07-01T00:00:00Z', actor: APP_PERSONAS.firm.subject },
    ]
    mockDetailFetch(detailRecord(), history)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByTestId('status-history-row')
    expect(document.body.textContent).toContain(`${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`)
    expect(document.body.textContent).not.toContain(APP_PERSONAS.firm.subject)
  })

  it('AC2: an unknown subject still renders raw, in mono', async () => {
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    const history: StatusChange[] = [{ from_status: null, to_status: 'draft', changed_at: '2026-07-01T00:00:00Z', actor: unknown }]
    mockDetailFetch(detailRecord(), history)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const row = await screen.findByTestId('status-history-row')
    // Exact match: today the actor and timestamp share one text node ('uuid · date'), so
    // this element-boundary lookup fails until the actor gets its own span.
    const actorEl = within(row).getByText(unknown)
    expect(actorEl.className.split(' ')).toContain('mono')
  })
})

// RED specs (task-392, BUG-03-03, Mode A). Every demo-data fixture nulls kept_as_is_*, so
// these override the three fields explicitly rather than relying on detailRecord()'s
// (unchanged) un-kept defaults.
describe('InvoiceDetail Compliance panel: the kept-as-is banner', () => {
  it('AC4: a kept invoice shows the decision on its own page', async () => {
    const kept = detailRecord({
      status: 'draft',
      kept_as_is_at: '2026-07-31T00:00:00Z',
      kept_as_is_by: APP_PERSONAS.firm.subject,
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    })
    mockDetailFetch(kept)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const banner = await screen.findByTestId('detail-kept-banner')
    expect(banner.textContent?.startsWith(ROW_EXPANSION_COPY.keptPrefix)).toBe(true)
    expect(banner.textContent).toContain('Buyer confirmed the discrepancy is intentional.')
    expect(banner.textContent).toContain(`${APP_PERSONAS.firm.name} · ${APP_PERSONAS.firm.org}`)
    expect(banner.textContent).toContain(fmtDateTime('2026-07-31T00:00:00Z'))
  })

  it('AC5: an un-kept invoice renders no kept banner', async () => {
    mockDetailFetch(detailRecord())

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByText('INV-FAILED-1') // wait for the record to render before asserting absence
    expect(screen.queryAllByTestId('detail-kept-banner')).toHaveLength(0)
  })

  // Trap: detailRecord()'s default rule_set_version is null, which renders `not-validated`
  // instead of `violations-table` -- overridden here to a real number so this targets the
  // element it means to, not a missing one.
  it('AC4: the kept banner leads the Compliance panel, above violations-table', async () => {
    const kept = detailRecord({
      status: 'draft',
      rule_set_version: 3,
      violations: [{ rule_key: 'vat_mismatch', severity: 'error', message: 'VAT does not match.' }],
      kept_as_is_at: '2026-07-31T00:00:00Z',
      kept_as_is_by: APP_PERSONAS.firm.subject,
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    })
    mockDetailFetch(kept)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const banner = await screen.findByTestId('detail-kept-banner')
    const table = screen.getByTestId('violations-table')
    expect(banner.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  // QA adversarial: the AC4 test above only checks banner-vs-violations-table. A kept
  // invoice is DB-constrained to status='draft' (invoices_kept_as_is_draft_only), which is
  // exactly the demotedSinceValidation shape (draft + rule_set_version_id set + no error
  // violations) that also renders stale-verdict -- a state the earlier test's
  // status:'validated' fixture could never reach for real.
  it('AC4: the kept banner also leads stale-verdict, for a demoted draft kept as-is', async () => {
    const kept = detailRecord({
      status: 'draft',
      rule_set_version_id: 'rsv-1',
      rule_set_version: 3,
      violations: [],
      kept_as_is_at: '2026-07-31T00:00:00Z',
      kept_as_is_by: APP_PERSONAS.firm.subject,
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    })
    mockDetailFetch(kept)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const banner = await screen.findByTestId('detail-kept-banner')
    const stale = screen.getByTestId('stale-verdict')
    const table = screen.getByTestId('violations-table')
    expect(banner.compareDocumentPosition(stale) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(banner.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  // QA adversarial: kept_as_is_by is a raw GoTrue subject on the wire same as h.actor --
  // an unrecognised one must still surface (raw), not go blank or silently drop to a
  // placeholder, mirroring AC2's guarantee for the status-history line.
  it('an unrecognised kept_as_is_by renders raw on the banner, not blank', async () => {
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    const kept = detailRecord({
      status: 'draft',
      kept_as_is_at: '2026-07-31T00:00:00Z',
      kept_as_is_by: unknown,
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    })
    mockDetailFetch(kept)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const banner = await screen.findByTestId('detail-kept-banner')
    expect(banner.textContent).toContain(unknown)
    expect(banner.textContent).not.toContain('Not recorded')
  })

  // QA adversarial: kept_as_is_by/_reason null while _at is set violates the all-or-
  // nothing CHECK constraint server-side, so this row should never arrive over the wire --
  // but keptAsIs() (lib/invoices.ts) doesn't re-enforce that, so the render path is what
  // actually decides what a malformed row shows. It must not fabricate a reason nor print
  // a stray "null" (JSX drops a null child silently; a template literal would not have).
  it('a null reason/actor renders no fabricated text and no stray "null" (defensive -- CHECK constraint should make this unreachable)', async () => {
    const kept = detailRecord({
      status: 'draft',
      kept_as_is_at: '2026-07-31T00:00:00Z',
      kept_as_is_by: null,
      kept_as_is_reason: null,
    })
    mockDetailFetch(kept)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const banner = await screen.findByTestId('detail-kept-banner')
    expect(banner.textContent).not.toMatch(/null/i)
    // Reason line: prefix only, nothing after it -- not disastrous, but worth knowing.
    expect(banner.children[0].textContent).toBe(ROW_EXPANSION_COPY.keptPrefix)
    // Actor falls back to actorLabel(null) === 'Not recorded', not blank.
    expect(banner.children[1].textContent).toContain('Not recorded')
  })
})

// RED specs (task-413, BUG-05-04, Mode A) -- no data-testid="buyer-tin" exists on the
// Bill-to block yet, so getByTestId throws below rather than reaching an assertion.
describe('Bill-to buyer TIN signal (task-413, BUG-05-04)', () => {
  it('AC-2: a missing buyer TIN (null) reads TIN MISSING in red, no em-dash in the Bill-to block', async () => {
    mockDetailFetch(detailRecord({ buyer_tin: null }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    const tin = screen.getByTestId('buyer-tin')
    expect(tin.textContent).toBe('TIN MISSING')
    expect(tin.style.color).toBe('var(--status-red-text)')
    // buyer_name is non-null in the default fixture, so the Bill-to block's only
    // possible em-dash source is the TIN line itself.
    expect(tin.parentElement?.textContent).not.toContain('—')
  })

  // The pre-fix regression case: `??` treats whitespace as present, so today this
  // renders empty rather than TIN MISSING.
  it('AC-2: a blank buyer TIN also reads TIN MISSING, not empty', async () => {
    mockDetailFetch(detailRecord({ buyer_tin: '  ' }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    expect(screen.getByTestId('buyer-tin').textContent).toBe('TIN MISSING')
  })

  it('AC-5: a malformed buyer TIN renders the value in grey, not the missing copy', async () => {
    mockDetailFetch(detailRecord({ buyer_tin: 'BADTIN' }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    const tin = screen.getByTestId('buyer-tin')
    expect(tin.textContent).toBe('BADTIN')
    expect(tin.style.color).toBe('var(--fg-3)')
  })
})

// Minimal register ctx/fetch, local to this one cross-component check -- InvoicesList
// is otherwise only exercised by InvoicesList.test.tsx.
function registerCtx(): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    openCreate: () => {},
    openImportedInvoice: () => {},
    invoiceQuery: '',
  }
  return ctx as unknown as PlatformCtx
}

function mockRegisterFetch(invoices: InvoiceDetailRecord[]) {
  const body: InvoiceListResponse = { invoices, pagination: { limit: 50, offset: 0, total: invoices.length } }
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve(body) }))
}

// RED (task-413, BUG-05-04, AC-5, [surfaces-must-agree]) -- a real cross-component
// comparison, not two assertions that happen to match: the SAME record drives both
// components in turn, and their buyer-tin text/colour must be identical.
describe('register/detail buyer TIN agreement (AC-5, task-413, BUG-05-04)', () => {
  it('missing, malformed and present buyer TINs render identical text and colour in both InvoicesList and InvoiceDetail', async () => {
    const states: Array<{ label: string; buyer_tin: string | null }> = [
      { label: 'missing', buyer_tin: null },
      { label: 'malformed', buyer_tin: 'BADTIN' },
      { label: 'present', buyer_tin: '87654321-0002' },
    ]

    for (const { label, buyer_tin } of states) {
      const record = detailRecord({ buyer_tin })

      mockDetailFetch(record)
      render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
      await screen.findByText(record.invoice_number)
      const detailTin = screen.getByTestId('buyer-tin')
      const detailText = detailTin.textContent
      const detailColor = detailTin.style.color
      cleanup()

      mockRegisterFetch([record])
      render(<InvoicesList ctx={registerCtx()} />)
      await screen.findByText(record.invoice_number)
      const listTin = screen.getByTestId('buyer-tin')

      expect(listTin.textContent, label).toBe(detailText)
      expect(listTin.style.color, label).toBe(detailColor)
      cleanup()
    }
  })
})

// QA Stage 4 adversarial (task-413, BUG-05-04): the story's own per-state table, driven
// through all THREE surfaces (not just the register/detail pair above) -- ReviewRow's
// Row needs no fetch mock (rendered directly, expanded=false skips its own fetch).
describe('three-surface buyer TIN agreement table (AC-4/AC-5, task-413, BUG-05-04)', () => {
  it.each([
    ['null', null],
    ['undefined', undefined],
    ['empty string', ''],
    ['whitespace-only', '   '],
    ['malformed', 'BADTIN'],
    ['well-formed', '87654321-0002'],
  ] as const)('%s renders identical text and colour on InvoicesList, InvoiceDetail and ReviewRow', async (_label, buyerTin) => {
    const record = detailRecord({ buyer_tin: buyerTin as unknown as string | null })

    mockDetailFetch(record)
    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText(record.invoice_number)
    const detailTin = screen.getByTestId('buyer-tin')
    const detailText = detailTin.textContent
    const detailColor = detailTin.style.color
    cleanup()

    mockRegisterFetch([record])
    render(<InvoicesList ctx={registerCtx()} />)
    await screen.findByText(record.invoice_number)
    const listTin = screen.getByTestId('buyer-tin')
    expect(listTin.textContent, 'InvoicesList').toBe(detailText)
    expect(listTin.style.color, 'InvoicesList').toBe(detailColor)
    cleanup()

    render(
      <Row
        r={record}
        batches={[]}
        checked={false}
        expanded={false}
        onToggleExpand={() => {}}
        onToggle={() => {}}
        ctx={registerCtx()}
        base="https://gw"
        onChanged={() => {}}
      />,
    )
    const reviewTin = screen.getByTestId('buyer-tin')
    expect(reviewTin.textContent, 'ReviewRow').toBe(detailText)
    expect(reviewTin.style.color, 'ReviewRow').toBe(detailColor)
    cleanup()
  })
})

// Adversarial (task-413, BUG-05-04, QA Stage 4): U+200B is not in Unicode's White_Space
// category in EITHER runtime, so JS's trim() leaves it exactly as untouched as Go's
// strings.TrimSpace does (internal/validation/evaluators.go's requiredEval, pinned by
// TestV4_BuyerTinRequiredZeroWidthSpaceGap) -- front and back AGREE this is "present",
// not missing. Rendered as the raw (invisible) character in grey, not red TIN MISSING.
describe('buyer TIN zero-width-space edge case (task-413, BUG-05-04)', () => {
  it('a U+200B-only buyer TIN is NOT missing -- same non-blank verdict as the backend required rule', () => {
    expect(isBuyerTinMissing('​')).toBe(false)
  })

  it('detail: a U+200B-only buyer TIN renders the raw value in grey, not TIN MISSING', async () => {
    mockDetailFetch(detailRecord({ buyer_tin: '​' }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    const tin = screen.getByTestId('buyer-tin')
    expect(tin.textContent).toBe('​')
    expect(tin.style.color).toBe('var(--fg-3)')
  })
})

// Adversarial (task-413, BUG-05-04, QA Stage 4): a present value surrounded by ordinary
// whitespace is non-blank overall (isBuyerTinMissing trims before checking blankness),
// but the RENDERED text is the raw untrimmed string -- no site trims for display.
describe('buyer TIN surrounded by whitespace (task-413, BUG-05-04)', () => {
  it('detail: a valid TIN padded with spaces counts as present and renders untrimmed', async () => {
    mockDetailFetch(detailRecord({ buyer_tin: ' 87654321-0002 ' }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    const tin = screen.getByTestId('buyer-tin')
    expect(tin.textContent).toBe(' 87654321-0002 ')
    expect(tin.style.color).toBe('var(--fg-3)')
  })
})

// Disclosure, not a fix (task-413 Implementation Notes, Phase 3.5 input #1): the missing
// buyer TIN is stated TWICE on this page in two grammars, both visible without scrolling.
// This subtask closes the register/detail wording GAP (AC-2/3); it does not deduplicate
// the two grammars, which is a legitimate design question for a later story.
describe('missing buyer TIN stated twice, in two grammars (disclosure, task-413)', () => {
  it('a blocked invoice shows both the Bill-to red TIN MISSING and the Compliance panel Error row for the same fact', async () => {
    mockDetailFetch(
      detailRecord({
        buyer_tin: null,
        rule_set_version_id: 'rsv-4',
        rule_set_version: 4,
        violations: [{ rule_key: 'buyer-tin-required', severity: 'error', message: 'Buyer TIN is required.', path: 'buyer.tin' }],
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    expect(screen.getByTestId('buyer-tin').textContent).toBe('TIN MISSING')
    const table = screen.getByTestId('violations-table')
    expect(table.textContent).toContain('Buyer TIN is required.')
    expect(within(table).getByText('Error')).toBeTruthy()
  })
})

// Disclosure, not a fix (task-413 Implementation Notes, Phase 3.5 input #2): [stale-
// violations-honest] (store.go:1067-1135) means an edit demotes validated->draft without
// touching the stored violations. Constructed directly via fixture (a rendering-state
// question, reachable on first load -- verdictStatus's demotedSinceValidation arm needs
// no prior interaction, just status=draft + a stamped rule_set_version_id + a clean
// stored violation set) rather than driving the actual edit flow.
describe('stale violations beside a live-missing buyer TIN (disclosure, task-413)', () => {
  it('a demoted-but-unrevalidated invoice shows red TIN MISSING, a green clean-pass panel, and the amber stale banner between them', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'draft',
        buyer_tin: null,
        rule_set_version_id: 'rsv-4',
        rule_set_version: 4,
        violations: [],
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    expect(screen.getByTestId('buyer-tin').textContent).toBe('TIN MISSING')
    expect(screen.getByTestId('stale-verdict').textContent).toBe(
      'Edited since the last validation — this verdict is stale. Run Re-validate to refresh it.',
    )
    expect(screen.getByTestId('violations-table').textContent).toBe('Passes all rules — no violations. Evaluated against rule-set v4.')
  })
})

// The kept mark means "kept as-is" only on a draft; on a failed invoice it means
// resolved outside the system, which is not this banner's claim.
describe('InvoiceDetail Compliance panel: the kept banner is a draft-only concept, not resolved-failed', () => {
  it('a resolved failed invoice never shows the kept-as-is banner', async () => {
    const resolved = detailRecord({
      status: 'failed',
      kept_as_is_at: '2026-08-06T00:00:00Z',
      kept_as_is_by: APP_PERSONAS.firm.subject,
      kept_as_is_reason: 'Filed manually with the tax authority.',
    })
    mockDetailFetch(resolved)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByText('INV-FAILED-1') // wait for the record to render before asserting absence
    expect(screen.queryAllByTestId('detail-kept-banner')).toHaveLength(0)
  })

  it('a kept draft still shows the kept-as-is banner (BUG-03 unchanged)', async () => {
    const kept = detailRecord({
      status: 'draft',
      kept_as_is_at: '2026-07-31T00:00:00Z',
      kept_as_is_by: APP_PERSONAS.firm.subject,
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    })
    mockDetailFetch(kept)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const banner = await screen.findByTestId('detail-kept-banner')
    expect(banner.textContent?.startsWith(ROW_EXPANSION_COPY.keptPrefix)).toBe(true)
  })
})

// RED specs (Mode A). None of `resolve-outside` / `resolve-outside-reason` /
// `resolve-outside-blocked-reason` / `detail-resolved-banner` / `resolve-outside-undo`
// exist yet, so every spec here fails on a missing element or a wrong disabled/call state,
// never a type/import error.
describe('InvoiceDetail resolve-outside control (Core AC #1/#4/#5/#6)', () => {
  it('T7-1: resolve action renders inside the failed card, not invoice-actions', async () => {
    mockDetailFetch(detailRecord({ status: 'failed', can_resolve_outside: true }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('failed-dead-end')
    expect(within(card).getByTestId('resolve-outside')).toBeTruthy()
    expect(screen.queryByTestId('invoice-actions')).toBeNull()
  })

  it('T7-2: mark-resolved is disabled until a reason is typed', async () => {
    mockDetailFetch(detailRecord({ status: 'failed', can_resolve_outside: true }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    const btn = (await screen.findByTestId('resolve-outside')) as HTMLButtonElement
    const input = screen.getByTestId('resolve-outside-reason')
    expect(btn.disabled, 'empty reason').toBe(true)

    fireEvent.change(input, { target: { value: '  ' } })
    expect(btn.disabled, 'whitespace-only reason').toBe(true)

    fireEvent.change(input, { target: { value: 'filed' } })
    expect(btn.disabled, 'non-empty trimmed reason').toBe(false)
  })

  it('T7-3: a blocked caller sees a disabled button carrying the server reason', async () => {
    const reason = 'Only an approver can mark this invoice resolved outside the system.'
    mockDetailFetch(detailRecord({ status: 'failed', can_resolve_outside: false, resolve_outside_blocked_reason: reason }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const btn = (await screen.findByTestId('resolve-outside')) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(screen.getByTestId('resolve-outside-blocked-reason').textContent).toBe(reason)
  })

  it('T7-4: no reason means no reason element, never a fallback', async () => {
    mockDetailFetch(detailRecord({ status: 'failed', can_resolve_outside: false, resolve_outside_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByTestId('resolve-outside')
    expect(screen.queryAllByTestId('resolve-outside-blocked-reason')).toHaveLength(0)
  })

  it("T7-5: the disabled primary button neutralises its hover filter", async () => {
    mockDetailFetch(detailRecord({ status: 'failed', can_resolve_outside: false }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const btn = await screen.findByTestId('resolve-outside')
    expect(btn.style.filter).toBe('none')
  })

  it('T7-6: a resolved invoice shows the banner and an undo', async () => {
    const at = '2026-08-06T12:00:00Z'
    const reason = 'Filed manually with the tax authority.'
    mockDetailFetch(
      detailRecord({
        status: 'failed',
        can_resolve_outside: true,
        kept_as_is_at: at,
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: reason,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const banner = await screen.findByTestId('detail-resolved-banner')
    expect(banner.textContent).toContain(reason)
    expect(banner.textContent).toContain(fmtDateTime(at))
    expect(screen.getByTestId('resolve-outside-undo')).toBeTruthy()
  })

  it('T7-7: marking resolved fires exactly one POST and no transition', async () => {
    const { fetchMock, resolveCalls, submitCalls } = mockDetailFetch(
      detailRecord({ status: 'failed', can_resolve_outside: true }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    fireEvent.change(await screen.findByTestId('resolve-outside-reason'), { target: { value: 'Filed manually.' } })
    fireEvent.click(screen.getByTestId('resolve-outside'))

    await waitFor(() => expect(resolveCalls).toHaveLength(1))
    expect(resolveCalls[0].method).toBe('POST')
    expect(submitCalls).toHaveLength(0)
    const transitionCalls = fetchMock.mock.calls.filter(([url]) => /\/transitions|\/validate/.test(String(url)))
    expect(transitionCalls).toHaveLength(0)
  })

  it('T7-8: undo fires exactly one DELETE', async () => {
    const { resolveCalls } = mockDetailFetch(
      detailRecord({
        status: 'failed',
        can_resolve_outside: true,
        kept_as_is_at: '2026-08-06T12:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Filed manually with the tax authority.',
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    fireEvent.click(await screen.findByTestId('resolve-outside-undo'))

    await waitFor(() => expect(resolveCalls).toHaveLength(1))
    expect(resolveCalls[0].method).toBe('DELETE')
  })

  // QA adversarial: T7-5 only covers the persistent (`can_resolve_outside: false`) disabled
  // reason; the far more common one -- an empty reason -- must neutralise the filter too.
  it('T7-9: the disabled button still neutralises its hover filter when blocked only by an empty reason', async () => {
    mockDetailFetch(detailRecord({ status: 'failed', can_resolve_outside: true }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const btn = (await screen.findByTestId('resolve-outside')) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(btn.style.filter).toBe('none')
  })

  // QA adversarial: existing coverage checks each banner's absence in isolation; neither
  // spans both banners in the same render, so a co-render regression would slip through.
  it('T7-10: a resolved failed invoice shows the resolved banner and never the kept-as-is banner', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'failed',
        can_resolve_outside: true,
        kept_as_is_at: '2026-08-06T12:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Filed manually.',
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByTestId('detail-resolved-banner')
    expect(screen.queryAllByTestId('detail-kept-banner')).toHaveLength(0)
  })

  it('T7-11: a kept draft shows the kept-as-is banner and never the resolved banner', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'draft',
        kept_as_is_at: '2026-07-31T00:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByTestId('detail-kept-banner')
    expect(screen.queryAllByTestId('detail-resolved-banner')).toHaveLength(0)
  })

  it('T7-12: a rejected resolve surfaces the error and leaves the control usable, not stuck disabled', async () => {
    const { resolveCalls } = mockDetailFetch(
      detailRecord({ status: 'failed', can_resolve_outside: true }),
      [],
      { resolveResponse: { ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) } },
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    const input = (await screen.findByTestId('resolve-outside-reason')) as HTMLInputElement
    fireEvent.change(input, { target: { value: 'Filed manually.' } })
    fireEvent.click(screen.getByTestId('resolve-outside'))

    await screen.findByText('boom')
    expect(resolveCalls).toHaveLength(1)
    const btn = screen.getByTestId('resolve-outside') as HTMLButtonElement
    expect(btn.disabled, 'reason is still populated and nothing is in flight -- must not stay disabled').toBe(false)
    expect(input.value).toBe('Filed manually.') // not cleared on failure
  })

  it('T7-13: a fast double-click on resolve-outside fires exactly one POST', async () => {
    const { resolveCalls } = mockDetailFetch(detailRecord({ status: 'failed', can_resolve_outside: true }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    fireEvent.change(await screen.findByTestId('resolve-outside-reason'), { target: { value: 'Filed manually.' } })
    const btn = screen.getByTestId('resolve-outside')
    fireEvent.click(btn)
    fireEvent.click(btn)

    await waitFor(() => expect(resolveCalls.length).toBeGreaterThan(0))
    expect(resolveCalls).toHaveLength(1)
  })

  it('T7-14: a fast double-click on undo fires exactly one DELETE', async () => {
    const { resolveCalls } = mockDetailFetch(
      detailRecord({
        status: 'failed',
        can_resolve_outside: true,
        kept_as_is_at: '2026-08-06T12:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Filed manually with the tax authority.',
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    const btn = await screen.findByTestId('resolve-outside-undo')
    fireEvent.click(btn)
    fireEvent.click(btn)

    await waitFor(() => expect(resolveCalls.length).toBeGreaterThan(0))
    expect(resolveCalls).toHaveLength(1)
  })

  // CodeRabbit review finding 1: a `live` overlay left over from watching queued -> failed
  // in this session must not mask a successful resolve/undo behind a stale render. Real
  // wait, not fake timers -- matches the poll-tick convention above (:928-960).
  it(
    'T7-15: a poll tick that observed queued -> failed does not mask a successful resolve behind a stale overlay',
    async () => {
      const initial = detailRecord({ status: 'queued', can_resolve_outside: false })
      const afterPoll = detailRecord({ status: 'failed', can_resolve_outside: true })
      const resolved = detailRecord({
        status: 'failed',
        can_resolve_outside: true,
        kept_as_is_at: '2026-08-06T12:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Filed manually.',
      })
      const { resolveCalls, fetchMock } = mockDetailFetch(initial, [], { detailSequence: [afterPoll, resolved] })

      render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

      const detailGetCalls = () =>
        fetchMock.mock.calls.filter(([url, init]: [string, RequestInit?]) => {
          const method = init?.method ?? 'GET'
          const u = String(url)
          return method === 'GET' && !u.endsWith('/history') && !u.endsWith('/source-document')
        })
      // 1 initial mount fetch + 1 poll tick that observes the queued -> failed transition.
      await waitFor(() => expect(detailGetCalls().length).toBeGreaterThanOrEqual(2), { timeout: LIVE_POLL_MS + 1500, interval: 100 })

      fireEvent.change(await screen.findByTestId('resolve-outside-reason'), { target: { value: 'Filed manually.' } })
      fireEvent.click(screen.getByTestId('resolve-outside'))

      await waitFor(() => expect(resolveCalls).toHaveLength(1))
      await screen.findByTestId('detail-resolved-banner')
    },
    LIVE_POLL_MS + 5000,
  )

  it(
    'T7-16: a poll tick that observed queued -> failed does not mask a successful undo behind a stale overlay',
    async () => {
      const initial = detailRecord({ status: 'queued', can_resolve_outside: false })
      const afterPoll = detailRecord({
        status: 'failed',
        can_resolve_outside: true,
        kept_as_is_at: '2026-08-06T12:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Filed manually with the tax authority.',
      })
      const unresolved = detailRecord({ status: 'failed', can_resolve_outside: true })
      const { resolveCalls, fetchMock } = mockDetailFetch(initial, [], { detailSequence: [afterPoll, unresolved] })

      render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

      const detailGetCalls = () =>
        fetchMock.mock.calls.filter(([url, init]: [string, RequestInit?]) => {
          const method = init?.method ?? 'GET'
          const u = String(url)
          return method === 'GET' && !u.endsWith('/history') && !u.endsWith('/source-document')
        })
      await waitFor(() => expect(detailGetCalls().length).toBeGreaterThanOrEqual(2), { timeout: LIVE_POLL_MS + 1500, interval: 100 })

      fireEvent.click(await screen.findByTestId('resolve-outside-undo'))

      await waitFor(() => expect(resolveCalls).toHaveLength(1))
      await screen.findByTestId('resolve-outside-reason')
      expect(screen.queryAllByTestId('detail-resolved-banner')).toHaveLength(0)
    },
    LIVE_POLL_MS + 5000,
  )

  // CodeRabbit review finding 2 (Core AC #4): a blocked Undo must carry the server reason,
  // same as the mark-resolved button, never a silently disabled control.
  it('T7-17: a blocked Undo shows the server reason, not a silently disabled control', async () => {
    const reason = 'Only an approver can mark an invoice resolved outside the system — ask an admin or a reviewer on your team.'
    mockDetailFetch(
      detailRecord({
        status: 'failed',
        can_resolve_outside: false,
        resolve_outside_blocked_reason: reason,
        kept_as_is_at: '2026-08-06T12:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Filed manually with the tax authority.',
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const btn = (await screen.findByTestId('resolve-outside-undo')) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    const reasonEl = screen.getByTestId('resolve-outside-blocked-reason')
    expect(reasonEl.textContent).toBe(reason)
    expect(btn.getAttribute('aria-describedby')).toBe(reasonEl.id)
  })

  // CodeRabbit review finding 3: resolveOutsideError must render in BOTH the resolved and
  // unresolved branches -- a failed undo has no branch to render its error in otherwise.
  it('T7-18: a rejected undo surfaces its error while the resolved banner is still showing', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'failed',
        can_resolve_outside: true,
        kept_as_is_at: '2026-08-06T12:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Filed manually with the tax authority.',
      }),
      [],
      { unresolveResponse: { ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) } },
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    fireEvent.click(await screen.findByTestId('resolve-outside-undo'))

    await screen.findByText('boom')
    expect(screen.getByTestId('detail-resolved-banner')).toBeTruthy()
  })

  // Pins the declarations only -- jsdom applies no CSS, so this cannot prove the label
  // stops wrapping on screen, only that the fix stays wired up.
  it('T7-19: resolve-outside is pinned to its own width and its row wraps instead', async () => {
    mockDetailFetch(detailRecord({ status: 'failed', can_resolve_outside: true }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const btn = (await screen.findByTestId('resolve-outside')) as HTMLButtonElement
    expect(btn.style.flexShrink).toBe('0')
    expect(btn.style.whiteSpace).toBe('nowrap')
    expect(btn.parentElement?.style.flexWrap).toBe('wrap')
  })
})

// RED specs (task-401, BUG-04-05, Mode A). None of `view-ubl` /
// `view-ubl-blocked-reason` / `ubl-modal` / `ubl-modal-close` exist yet, so every spec
// here fails on a missing element or a wrong string, never a type/import error (this
// file's convention, :391-393). Tests-only, no stub: the App.tsx/types.ts teardown of
// `xmlOpen`/`openXml`/`closeXml` and the local mount MUST land in one commit, because a
// window where both App.tsx and LiveInvoiceDetail mount XmlModal double-mounts it and
// every browser spec on this path is a console-error gate.
// Local literal, not a runtime export: no status list exists to import (lib/invoices.ts
// :120-128 is a type union, erased at runtime). QA hoisted it to module scope, unchanged,
// so Q11 below can guard it against the union it mirrors -- task-401 §I-2 flagged it as
// silently lagging.
const ALL_STATUSES: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']

describe('InvoiceDetail View UBL/XML control (task-401, BUG-04-05, [ubl-button-outside-invoice-actions])', () => {
  const ID = 'inv-ubl-1'
  // The backend's own copy (internal/ubl/ubl.go:16 + :149) -- em dash U+2014, single
  // spaces. Asserted with toBe so a client-side re-authoring of it cannot pass
  // ([ubl-reason-copy-is-server-authored]).
  const REASON = 'This invoice cannot be rendered as a UBL document — it is missing at least one line item.'
  const editable = { status: 'validated' as InvoiceStatus, can_edit: true, can_revalidate: false, can_submit: true }

  it('T1/AC1: renders the prototype control -- label, ghost classes, repo-neighbour sizing, leading glyph', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = (await screen.findByTestId('view-ubl')) as HTMLButtonElement
    // toBe, not toContain: 'View XML' / 'View UBL' must fail (.ralph/design-spec.md §1).
    // The svg contributes no text, so the leading space is all trim() has to remove.
    expect(btn.textContent?.trim()).toBe('View UBL/XML')
    expect(btn.className.split(' ')).toEqual(expect.arrayContaining(['v2-btn', 'v2-btn-ghost', 'pf-btn']))
    expect(btn.type).toBe('button')
    // 32, not the prototype's 36 ([ubl-button-height-follows-the-repo]). .v2-btn's base is
    // height:40 / padding:0 20px (app-layer.css:206-211), so all three overrides carry.
    expect(btn.style.height).toBe('32px')
    expect(btn.style.padding).toBe('0px 14px')
    expect(btn.style.fontSize).toBe('13px')
    expect(btn.querySelector('svg'), 'the leading docGlyph2').toBeTruthy()
  })

  it.each(ALL_STATUSES)('T2/AC2: renders on a %s invoice even with can_edit false', async (status) => {
    mockDetailFetch(detailRecord({ id: ID, status, can_edit: false, can_submit: false }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    expect(await screen.findByTestId('view-ubl')).toBeTruthy()
  })

  // The whole reason the control sits outside `invoice-actions`: a compliance user needs
  // the document most on the statuses where that bar is gone.
  it('T3/AC2: renders where the actions bar does not', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: false }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    expect(await screen.findByTestId('view-ubl')).toBeTruthy()
    expect(screen.queryByTestId('invoice-actions')).toBeNull()
  })

  it('T4/AC3: is a sibling of invoice-actions -- never inside it, never in a wrapper of its own', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const bar = await screen.findByTestId('invoice-actions')
    const btn = screen.getByTestId('view-ubl')
    expect(within(bar).queryByTestId('view-ubl')).toBeNull()
    // Parent IDENTITY, not merely "outside the bar": a wrapping <div> around the
    // button+reason pair fuses them into ONE flex item of the outer column and breaks its
    // alignItems:'flex-end' / gap:8. Only a fragment keeps them as two direct children.
    expect(btn.parentElement).toBe(bar.parentElement)
  })

  it('T5/AC3: the actions bar keeps all three lifecycle controls alongside it', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const bar = await screen.findByTestId('invoice-actions')
    expect(within(bar).getByTestId('edit-toggle')).toBeTruthy()
    expect(within(bar).getByTestId('revalidate')).toBeTruthy()
    expect(within(bar).getByTestId('detail-submit')).toBeTruthy()
    expect(screen.getByTestId('view-ubl')).toBeTruthy()
  })

  it('T6/AC2: is hidden while editing', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    expect(await screen.findByTestId('view-ubl')).toBeTruthy()

    fireEvent.click(screen.getByTestId('edit-toggle'))

    expect(screen.queryAllByTestId('view-ubl')).toHaveLength(0)
  })

  // MUTATION ORACLE for the `!editing` guard -- do not drop. T6 passes with NO guard at
  // all, because with no banner live the whole outer column is already gone while
  // editing. Here a skip banner keeps that column mounted during edit mode, so an
  // unguarded control leaks straight into the editor. Only this row catches it.
  it('T7/AC2: stays hidden while editing even with a live skip banner holding the column open', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }), [], {
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
    await screen.findByTestId('detail-submit-skipped')
    // Anchor: with the banner up and NOT editing, the control is there.
    expect(screen.getByTestId('view-ubl')).toBeTruthy()

    fireEvent.click(screen.getByTestId('edit-toggle'))

    expect(screen.getByTestId('detail-submit-skipped'), 'the column is still mounted').toBeTruthy()
    expect(screen.queryAllByTestId('view-ubl')).toHaveLength(0)
  })

  it('T8/AC4: a blocked control is disabled and carries the wire reason verbatim', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_view_ubl: false, ubl_blocked_reason: REASON }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = (await screen.findByTestId('view-ubl')) as HTMLButtonElement
    const reasonEl = screen.getByTestId('view-ubl-blocked-reason')
    expect(btn.disabled, 'layer 1: a real HTML disabled attribute').toBe(true)
    expect(btn.getAttribute('title')).toBe(REASON)
    expect(reasonEl.textContent, 'layer 3: the em dash and every byte, unmodified').toBe(REASON)
    // Second half of T4's fragment oracle: the reason is the column's own child too.
    expect(reasonEl.parentElement).toBe(btn.parentElement)
  })

  it('T9/AC4: aria-describedby points at its OWN reason node, colliding with neither existing one', async () => {
    mockDetailFetch(
      detailRecord({
        id: ID,
        status: 'rejected',
        can_edit: true,
        can_revalidate: false,
        can_submit: false,
        revalidate_blocked_reason: 'Only draft invoices can be re-validated — edit this invoice to return it to draft.',
        submit_blocked_reason: 'Only validated invoices can be submitted — edit this invoice and re-validate it first.',
        can_view_ubl: false,
        ubl_blocked_reason: REASON,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = await screen.findByTestId('view-ubl')
    const reasonEl = screen.getByTestId('view-ubl-blocked-reason')
    expect(reasonEl.id).not.toBe('')
    expect(btn.getAttribute('aria-describedby')).toBe(reasonEl.id)
    // All THREE disabled controls render together on this fixture, so a copy-pasted id
    // would be a live collision, not a hypothetical one (mirrors :766-768).
    expect(reasonEl.id).not.toBe(screen.getByTestId('revalidate-blocked-reason').id)
    expect(reasonEl.id).not.toBe(screen.getByTestId('submit-blocked-reason').id)
  })

  it('T10/AC4: a blocked control is muted inline, not merely disabled -- and takes no filter', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_view_ubl: false, ubl_blocked_reason: REASON }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = await screen.findByTestId('view-ubl')
    expect(btn.style.cursor).toBe('not-allowed')
    expect(btn.style.background, 'layer 2: outranks the unguarded .v2-btn-ghost:hover').not.toBe('')
    expect(btn.style.color).not.toBe('')
    // NO `filter: 'none'`. That belongs to the Submit recipe only: .v2-btn-primary:hover
    // (app-layer.css:213) brightens, .v2-btn-ghost:hover (:215) sets background and
    // border-color and nothing else. Pins this control to `revalidate`'s recipe.
    expect(btn.style.filter).toBe('')
  })

  // MUTATION ORACLE: an UNCONDITIONAL style spread satisfies T1 and T8-T10 and silently
  // kills the enabled button's :hover affordance -- the hazard InvoiceDetail.tsx:512-513
  // documents. Only this row sees it.
  it('T11/AC4: an enabled control carries no muted style at all', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_view_ubl: true, ubl_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = (await screen.findByTestId('view-ubl')) as HTMLButtonElement
    expect(btn.disabled).toBe(false)
    expect(btn.style.cursor).toBe('')
    expect(btn.style.background).toBe('')
    expect(btn.style.color).toBe('')
  })

  // Proves layer 1 is a REAL `disabled` and not an onClick early-return: there is
  // deliberately no `if (!can_view_ubl) return` guard in the handler, so this row tests
  // the actual mechanism.
  it('T12/AC4: a blocked control does not open the viewer', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_view_ubl: false, ubl_blocked_reason: REASON }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('view-ubl'))

    expect(screen.queryAllByTestId('ubl-modal')).toHaveLength(0)
  })

  // The degenerate wire shape -- representable (a dropped key normalises to
  // can_view_ubl:false with a null reason, invoices.test.ts G7), and the SPA has no
  // authority to author copy for it. Disabled, silent, nothing invented.
  it('T13/AC5: a null ubl_blocked_reason renders no reason node and no invented copy', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_view_ubl: false, ubl_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = (await screen.findByTestId('view-ubl')) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(screen.queryAllByTestId('view-ubl-blocked-reason')).toHaveLength(0)
    expect(btn.hasAttribute('title')).toBe(false)
    expect(btn.hasAttribute('aria-describedby')).toBe(false)
    expect(screen.queryByText(/cannot be rendered/i)).toBeNull()
  })

  it('T14/AC6: the viewer is not mounted until the control is clicked', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = (await screen.findByTestId('view-ubl')) as HTMLButtonElement
    expect(btn.disabled, 'an enabled control, so the absence below is about mounting').toBe(false)
    expect(screen.queryAllByTestId('ubl-modal')).toHaveLength(0)
  })

  it('T15/AC6: clicking the enabled control mounts the viewer', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('view-ubl'))

    expect(screen.getByTestId('ubl-modal')).toBeTruthy()
  })

  // MUTATION ORACLE for the invoiceNumber PROP. Between 05 and 06 the modal still makes
  // its own transitional getInvoice; this fixture answers that second GET with a
  // DIFFERENT invoice_number, so a subtitle sourced from the fetch instead of the prop
  // reads '—' before it resolves and INV-WRONG after.
  //
  // Scoped to the SUBTITLE, never the whole modal (QA, task-401 §F T16): the modal BODY
  // is that transitional fetch rendered as UBL, so `<cbc:ID>INV-WRONG</cbc:ID>` sits in
  // ubl-modal's textContent by design until 06 deletes the fetch. Only the subtitle
  // discriminates the prop from a refetch, which is the whole claim of this row.
  //
  // Post-06 the viewer issues only the /ubl GET, which the harness answers from its own
  // arm -- INV-WRONG can then reach no part of the modal, and this row degrades from a
  // mutation oracle to a plain prop assertion.
  it('T16/AC6: the viewer is handed THIS invoice number, not a refetched one', async () => {
    const { fetchMock } = mockDetailFetch(detailRecord({ id: ID, invoice_number: 'INV-PROP-1', ...editable }), [], {
      detailSequence: [detailRecord({ id: ID, invoice_number: 'INV-WRONG', ...editable })],
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('view-ubl'))
    // The only 'PEPPOL' in src/ (XmlModal.tsx:63). getByText matches direct text children,
    // so no ancestor of the subtitle competes for it.
    const subtitle = () => within(screen.getByTestId('ubl-modal')).getByText(/^PEPPOL BIS 3\.0 ·/).textContent

    // Synchronous read, deliberately un-awaited: the modal's own fetch cannot have
    // resolved yet, so a fetch-sourced subtitle reads '—' right here.
    expect(subtitle()).toBe('PEPPOL BIS 3.0 · INV-PROP-1')

    // Now let every pending promise settle and re-read: a fetch-sourced subtitle flips to
    // INV-WRONG at this point.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(subtitle()).toBe('PEPPOL BIS 3.0 · INV-PROP-1')
    expect(subtitle()).not.toContain('INV-WRONG')
    expect(fetchMock).toHaveBeenCalled()
  })

  // tsc enforces CONSISTENCY between types.ts and App.tsx's annotated ctx literal; it
  // cannot enforce REMOVAL. A source scan is the only oracle for that. cwd, not
  // import.meta.url: under jsdom the latter is an http: URL and fileURLToPath throws
  // (SourceDocumentSheet.test.tsx:379-380, ReportsView.test.tsx:249).
  it('T17/AC6: the xml wiring is gone from the global context and the App shell', () => {
    const types = readFileSync(path.join(process.cwd(), 'src/types.ts'), 'utf8')
    const app = readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')
    expect(types.length, 'floor: the file really was read').toBeGreaterThan(1000)
    expect(app.length, 'floor: the file really was read').toBeGreaterThan(1000)

    for (const name of ['xmlOpen', 'openXml', 'closeXml', 'XmlModal']) {
      expect(types, `src/types.ts still names ${name}`).not.toContain(name)
      expect(app, `src/App.tsx still names ${name}`).not.toContain(name)
    }
  })

  it('T18/AC6: closing the viewer returns to the detail page', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('view-ubl'))
    fireEvent.click(screen.getByTestId('ubl-modal-close'))

    expect(screen.queryAllByTestId('ubl-modal')).toHaveLength(0)
    expect(screen.getByTestId('invoice-detail')).toBeTruthy()
  })

  // The prototype's action group is `View UBL/XML · PDF · Transmit`; only the first is in
  // scope. PDF rendering is explicitly Out of Scope and a second dead button is exactly
  // what this story exists to remove (.ralph/design-spec.md §1).
  it.each([
    ['an editable', editable],
    ['a non-editable', { status: 'accepted' as InvoiceStatus, can_edit: false, can_submit: false }],
  ])('T19/AC7: adds no PDF and no Transmit control (%s invoice)', async (_label, over) => {
    mockDetailFetch(detailRecord({ id: ID, ...over }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    expect(await screen.findByTestId('view-ubl')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /^PDF$/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /transmit/i })).toBeNull()
  })
})

// QA adversarial coverage (BUG-04-05, Mode B). Every row below is mutation-verified; the
// mutant it exists to kill is named in its comment.
describe('InvoiceDetail View UBL/XML control -- QA adversarial coverage (task-401)', () => {
  const ID = 'inv-ubl-1'
  const REASON = 'This invoice cannot be rendered as a UBL document — it is missing at least one line item.'
  const editable = { status: 'validated' as InvoiceStatus, can_edit: true, can_revalidate: false, can_submit: true }

  async function settle() {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
  }

  // KILLS: `invoiceId={'other'}`. No T-row pinned WHICH invoice the viewer addresses --
  // T16 pins only the number, which is a prop. `invoiceId` is the sole document selector,
  // and the ONLY one once BUG-04-06 swaps the transitional getInvoice for getInvoiceUbl.
  it('Q1/AC6: every request the viewer issues addresses THIS invoice', async () => {
    const { fetchMock } = mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = await screen.findByTestId('view-ubl')
    await settle()

    const before = fetchMock.mock.calls.length
    fireEvent.click(btn)
    await settle()

    const opened = fetchMock.mock.calls.slice(before).map(([url]) => String(url))
    expect(opened.length, 'the viewer issued a request -- otherwise this row is vacuous').toBeGreaterThan(0)
    for (const url of opened) {
      expect(url, `${url} must address ${ID}`).toContain(`/invoices/${ID}`)
    }
  })

  // KILLS: dropping `onClick={onClose}` from the backdrop. 05 rewired BOTH of XmlModal's
  // closers off `ctx.closeXml`; T18 covered only the button. The inner-click half guards
  // the panel's stopPropagation, without which reading the document dismisses it.
  it('Q2/AC6: the backdrop closes the viewer and a click inside the panel does not', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('view-ubl'))
    const modal = screen.getByTestId('ubl-modal')

    fireEvent.click(within(modal).getByText('UBL 2.1 document'))
    expect(screen.queryAllByTestId('ubl-modal'), 'a click on the panel must not dismiss it').toHaveLength(1)

    fireEvent.click(modal)
    expect(screen.queryAllByTestId('ubl-modal')).toHaveLength(0)
    expect(screen.getByTestId('invoice-detail')).toBeTruthy()
  })

  // KILLS: `setUblOpen((o) => !o)`. An open modal is position:fixed/inset:0, so a real
  // browser cannot deliver the second click -- jsdom can, which is what makes this an
  // oracle rather than a scenario.
  it('Q3/AC6: repeated clicks mount exactly one viewer and never toggle it shut', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = await screen.findByTestId('view-ubl')

    fireEvent.click(btn)
    fireEvent.click(btn)
    expect(screen.queryAllByTestId('ubl-modal')).toHaveLength(1)
    fireEvent.click(btn)
    expect(screen.queryAllByTestId('ubl-modal')).toHaveLength(1)
  })

  // KILLS: `aria-label="View"`. T1 asserts textContent, which an aria-label silently
  // overrides for every assistive technology. The glyph is aria-hidden (icons.tsx:25), so
  // the computed name is the label alone.
  it('Q4/AC1: the control is a real button named exactly "View UBL/XML", and is keyboard-reachable', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('view-ubl')

    const btn = screen.getByRole('button', { name: 'View UBL/XML' })
    expect(btn).toBe(screen.getByTestId('view-ubl'))
    // A real <button> is Enter/Space-activated by the platform. jsdom synthesises no click
    // from keydown, so the element type IS the assertion -- a div[role=button] would need
    // hand-rolled key handling this control does not have.
    expect(btn.tagName).toBe('BUTTON')
    btn.focus()
    expect(document.activeElement, 'an enabled control must be in the tab order').toBe(btn)
  })

  // KILLS: `aria-hidden="true"` on the reason node. Once the button leaves the tab order
  // (asserted here), layer 3 is the ONLY layer a screen-reader user can still reach --
  // hiding it makes the refusal silent. Mirrors :835-837 for Submit.
  it('Q5/AC4: a blocked control refuses focus and the keyboard, and its reason stays in the a11y tree', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_view_ubl: false, ubl_blocked_reason: REASON }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = await screen.findByTestId('view-ubl')

    btn.focus()
    expect(document.activeElement, 'a disabled button is out of the tab order').not.toBe(btn)
    for (const key of ['Enter', ' ']) {
      fireEvent.keyDown(btn, { key })
      fireEvent.keyUp(btn, { key })
    }
    expect(screen.queryAllByTestId('ubl-modal')).toHaveLength(0)

    const reasonEl = screen.getByTestId('view-ubl-blocked-reason')
    expect(reasonEl.getAttribute('aria-hidden')).toBeNull()
    expect(reasonEl.hasAttribute('hidden')).toBe(false)
    expect(screen.getByText(REASON)).toBe(reasonEl)
  })

  // The worst case the backend can actually build: all six Missing() gaps joined
  // (internal/invoice/ubl_test.go:361) inside a maxWidth:320 column.
  // KILLS: `whiteSpace: 'nowrap'` / `overflow: 'hidden'` on the reason node.
  it('Q6/AC4: the longest reason the backend can produce renders whole, with nothing forcing it onto one line', async () => {
    const longest =
      'This invoice cannot be rendered as a UBL document — it is missing an invoice number, an issue date, a currency, a supplier name, a buyer name and at least one line item.'
    mockDetailFetch(detailRecord({ id: ID, can_view_ubl: false, ubl_blocked_reason: longest }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const reasonEl = await screen.findByTestId('view-ubl-blocked-reason')
    expect(reasonEl.textContent, 'no client-side truncation or ellipsis').toBe(longest)
    // jsdom does no layout, so this is the reachable half -- nothing in the inline style
    // may stop the text wrapping inside the column's cap. The rendered result belongs to
    // the visual gate (task-401 §G, RALPH 3.5).
    const s = (reasonEl as HTMLElement).style
    expect(s.whiteSpace).toBe('')
    expect(s.overflow).toBe('')
    expect(s.textOverflow).toBe('')
    expect(reasonEl.parentElement?.style.maxWidth, 'still inside the 320 column').toBe('320px')
  })

  // KILLS: dangerouslySetInnerHTML. The reason is server-authored and rendered verbatim
  // ([ubl-reason-copy-is-server-authored]); verbatim must mean as TEXT.
  it('Q7/AC4: a reason carrying markup renders as text, never as HTML', async () => {
    const nasty = 'Missing <script>alert(1)</script> & <b>a supplier name</b>'
    mockDetailFetch(detailRecord({ id: ID, can_view_ubl: false, ubl_blocked_reason: nasty }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const reasonEl = await screen.findByTestId('view-ubl-blocked-reason')
    expect(reasonEl.textContent).toBe(nasty)
    expect(reasonEl.querySelector('script'), 'never parsed as markup').toBeNull()
    expect(reasonEl.querySelector('b')).toBeNull()
    expect(reasonEl.childElementCount, 'one text node, no element children').toBe(0)
    expect(screen.getByTestId('view-ubl').getAttribute('title')).toBe(nasty)
  })

  // KILLS: `disabled={!inv.can_view_ubl || inv.ubl_blocked_reason != null}`. Mirrors
  // :856-859 for Submit. Never produced by the real backend --
  // TestGetHandler_UBLGateIsStatusIndependent{,WhenBlocked} pin the two mutually exclusive
  // -- but the enabled state must read can_view_ubl ALONE and never infer refusal from a
  // reason string merely being present.
  it('Q8/AC4: a contradictory wire (can_view_ubl true with a non-null reason) still enables the control', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable, can_view_ubl: true, ubl_blocked_reason: REASON }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = (await screen.findByTestId('view-ubl')) as HTMLButtonElement
    expect(btn.disabled).toBe(false)
    expect(btn.style.cursor, 'and it is not muted either').toBe('')
    fireEvent.click(btn)
    expect(screen.getByTestId('ubl-modal')).toBeTruthy()
  })

  // T7's sibling branch. The widened condition (:473) has TWO banner arms and T7 walks
  // only `submitSkipped`; a guard keyed to that arm alone passes T7 and leaks the control
  // into edit mode here.
  it('Q9/AC2: stays hidden while editing with a live submit ERROR banner holding the column open', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }), [], {
      submitResponses: [{ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) }],
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-submit'))
    fireEvent.click(screen.getByTestId('detail-submit-confirm'))
    await screen.findByTestId('detail-submit-error')
    expect(screen.getByTestId('view-ubl'), 'anchor: present with the banner up and not editing').toBeTruthy()

    fireEvent.click(screen.getByTestId('edit-toggle'))

    expect(screen.getByTestId('detail-submit-error'), 'the column is still mounted').toBeTruthy()
    expect(screen.queryAllByTestId('view-ubl')).toHaveLength(0)
  })

  // The hazard the widened condition introduces (task-401 §B): an EMPTY maxWidth:320 flex
  // item silently eating header space beside the title block. maxWidth:320 is unique to
  // that column (InvoiceDetail.tsx:474).
  // KILLS: widening the condition all the way to `true`.
  const columns = () =>
    Array.from(document.querySelectorAll<HTMLElement>('div')).filter((d) => d.style.maxWidth === '320px')

  it.each([
    ['can_edit true, not editing', { ...editable }, false],
    ['can_edit false, not editing', { status: 'queued' as InvoiceStatus, can_edit: false, can_submit: false }, false],
    ['can_edit true, editing', { ...editable }, true],
  ])('Q10/AC2: the header column is never rendered empty (%s)', async (_label, over, edit) => {
    mockDetailFetch(detailRecord({ id: ID, ...over }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-detail')
    if (edit) fireEvent.click(await screen.findByTestId('edit-toggle'))

    expect(columns().length, 'exactly one column, or none at all').toBeLessThanOrEqual(1)
    for (const col of columns()) {
      expect(col.children.length, 'a rendered column always carries something').toBeGreaterThan(0)
    }
  })

  it('Q10b/AC2: the column stays non-empty while editing with a banner holding it open', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }), [], {
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
    await screen.findByTestId('detail-submit-skipped')
    fireEvent.click(screen.getByTestId('edit-toggle'))

    expect(columns()).toHaveLength(1)
    expect(columns()[0].children.length).toBeGreaterThan(0)
  })

  // The `!editing` half of AC#3, which nothing covered: every pre-existing
  // `invoice-actions` absence assertion (:794, :827, :1112) is about can_edit:false.
  // It needs the banner, for the same reason T7 does and one step further -- once 05
  // widened the outer column to `!editing || banner`, `editing` with NO banner drops the
  // whole column, so the bar's own `!editing` is load-bearing in exactly one state.
  // KILLS: `{inv.can_edit && (` on InvoiceDetail.tsx:511.
  it('Q12/AC3: the actions bar stays gated on !editing even with a banner holding the column open', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable }), [], {
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
    await screen.findByTestId('detail-submit-skipped')
    expect(screen.getByTestId('invoice-actions'), 'anchor: the bar is up before editing').toBeTruthy()

    fireEvent.click(screen.getByTestId('edit-toggle'))

    expect(screen.getByTestId('detail-submit-skipped'), 'the column is still mounted').toBeTruthy()
    expect(screen.queryAllByTestId('invoice-actions')).toHaveLength(0)
    for (const id of ['edit-toggle', 'revalidate', 'detail-submit']) {
      expect(screen.queryAllByTestId(id), `${id} goes with the bar`).toHaveLength(0)
    }
  })

  // Closes task-401 §I-2: T2's literal lags silently if the union grows. A source scan is
  // the only oracle -- the union is erased at runtime. cwd, never import.meta.url (T17).
  it('Q11/AC2: T2 loops the WHOLE InvoiceStatus union, not a literal that has drifted', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/lib/invoices.ts'), 'utf8')
    const block = src.match(/export type InvoiceStatus =\n((?:\s*\|\s*'[a-z_]+'\n)+)/)
    expect(block, 'the union no longer parses -- re-derive ALL_STATUSES by hand').toBeTruthy()
    const declared = [...(block as RegExpMatchArray)[1].matchAll(/'([a-z_]+)'/g)].map((m) => m[1])
    expect(declared.length, 'floor: the union really was parsed').toBeGreaterThan(1)
    expect([...ALL_STATUSES].sort()).toEqual([...declared].sort())
  })
})
