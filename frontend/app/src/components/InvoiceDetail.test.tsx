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
import { APPROVAL_CARD_COPY, type ApprovalRun } from '../lib/approvals'
import type { AuditEvent, AuditResponse } from '../lib/audit'
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
  type InvoiceRecord,
  type InvoiceStatus,
  type StatusChange,
} from '../lib/invoices'
import type { Member } from '../lib/members'
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

// The activity card's log, empty unless a test passes `auditLog`. That emptiness is why a
// page-wide text sweep has to carve the card out explicitly -- see AC-6's banner spec.
const EMPTY_AUDIT_LOG: AuditResponse = {
  events: [],
  page: { limit: 100, has_more: false, next_cursor: null },
  total: 0,
  log_is_empty: false,
  facets: { event: [], actor: [], company: [] },
}

function auditLogOf(events: AuditEvent[]): AuditResponse {
  return { ...EMPTY_AUDIT_LOG, events, total: events.length }
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

// onUnauthorized defaults to a throwaway spy -- exposed as a param (not read back off ctx)
// so a 401 test can pass its own spy and assert on it directly.
function detailCtx(importedInvoiceId: string, onUnauthorized: () => void = vi.fn()): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', onUnauthorized),
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

interface DecideCall {
  body: { decision: 'approved' | 'rejected'; reason?: string }
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
  // GET .../approval (APPR-13-03). Defaults to 404 inside the dispatcher below so no
  // pre-existing test's behaviour changes. Only the FIRST call reads this -- calls after
  // the first consult `approvalSequence` (below), mirroring `detailSequence`'s own
  // first-call/later-calls split.
  approvalResponse?: MockResponse
  // Real GET .../approval calls AFTER the first resolve from here in order (null -> 404
  // no-run), the last entry repeating once exhausted. Undefined (the default) repeats
  // `approvalResponse` forever, unchanged from pre-task-547 behaviour.
  approvalSequence?: (ApprovalRun | null)[]
  // Real GET .../history calls AFTER the first resolve from here in order, the last entry
  // repeating once exhausted. Undefined (the default) repeats `history` forever, unchanged
  // from pre-task-547 behaviour.
  historySequence?: StatusChange[][]
  // GET .../history, FIRST call only. Overrides `history` so a non-2xx can be produced.
  historyResponse?: MockResponse
  // POST .../approvals (task-547, D-29's decide arm). Every call is recorded into the
  // returned `decideCalls`, win or lose. Defaults to a 200 carrying a plausible ApprovalRun
  // shaped by the posted decision.
  decideResponse?: MockResponse
  // GET .../source-document. Defaults to the invoice record itself -- a body carrying no
  // `document` key, which every pre-existing test reads as "no source document".
  sourceDocumentResponse?: MockResponse
  // GET .../audit-log, the activity card's read. Defaults to EMPTY_AUDIT_LOG.
  auditLog?: AuditResponse
  // GET .../audit-log, overriding `auditLog` so a non-2xx can be produced. Absent (the
  // default) leaves every pre-existing test's behaviour byte-identical.
  auditLogResponse?: MockResponse
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
  let historyCalls = 0
  let approvalCalls = 0
  const submitCalls: SubmitCallBody[] = []
  const resolveCalls: ResolveCall[] = []
  const decideCalls: DecideCall[] = []
  const NO_RUN: MockResponse = { ok: false, status: 404, json: () => Promise.resolve({ error: 'no approval run for this invoice' }) }

  const fetchMock = vi.fn((url: string, init: RequestInit = {}) => {
    const method = init.method ?? 'GET'

    if (url.endsWith('/history')) {
      const call = historyCalls
      historyCalls++
      if (call === 0 && opts.historyResponse) return Promise.resolve<MockResponse>(opts.historyResponse)
      const rows = call === 0 ? history : (opts.historySequence?.[call - 1] ?? opts.historySequence?.at(-1) ?? history)
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(rows) })
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
      return Promise.resolve<MockResponse>(
        opts.sourceDocumentResponse ?? { ok: true, status: 200, json: () => Promise.resolve(detail) },
      )
    }
    // The source-document card's extraction lookup (EXTR-11-08), dispatched before the
    // detail-refetch counter like /source-document above -- the fallback would answer it
    // with an invoice record, whose missing `jobs` array throws inside the card's reducer.
    // Only a fixture carrying a `document` reaches it; every other test stays at zero calls.
    if (method === 'GET' && url.includes('/v1/extractions')) {
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve({ jobs: [] }) })
    }
    // The activity card's GET .../audit-log, dispatched before the detail-refetch counter
    // like /ubl and /source-document above: the fallback would answer it with an invoice
    // record and eat a detailSequence slot. `includes`, not `endsWith` -- it carries a query.
    if (method === 'GET' && url.includes('/audit-log')) {
      return Promise.resolve<MockResponse>(
        opts.auditLogResponse ?? {
          ok: true,
          status: 200,
          json: () => Promise.resolve(opts.auditLog ?? EMPTY_AUDIT_LOG),
        },
      )
    }
    // GET .../approval (APPR-13-03, D-29), dispatched before the detail-refetch counter
    // like /ubl and /source-document above. `.endsWith('/approval')` is false for
    // '/approvals' (the POST decide route), so that arm is unaffected. First call always
    // answers `approvalResponse`; later calls consume `approvalSequence` in order like
    // `detailSequence` does, repeating the last entry once exhausted -- or, if no sequence
    // was configured, repeating `approvalResponse` forever (unchanged pre-task-547 shape).
    if (method === 'GET' && url.endsWith('/approval')) {
      const call = approvalCalls
      approvalCalls++
      if (call === 0) {
        return Promise.resolve<MockResponse>(opts.approvalResponse ?? NO_RUN)
      }
      const next = opts.approvalSequence?.[call - 1] ?? opts.approvalSequence?.at(-1)
      if (next === undefined) return Promise.resolve<MockResponse>(opts.approvalResponse ?? NO_RUN)
      if (next === null) return Promise.resolve<MockResponse>(NO_RUN)
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(next) })
    }
    // decideInvoice (lib/approvals.ts:342-354) -- POST .../approvals. Dispatched before the
    // detail-refetch counter below (D-29's convention, task-547): without its own arm a
    // decide POST falls through and silently consumes a detailSequence slot instead of
    // being recorded here.
    if (method === 'POST' && url.endsWith('/approvals')) {
      const body = JSON.parse(String(init.body)) as { decision: 'approved' | 'rejected'; reason?: string }
      decideCalls.push({ body })
      return Promise.resolve<MockResponse>(
        opts.decideResponse ?? {
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              run_id: 'run-decided-1',
              state: body.decision,
              opened_at: '2026-08-01T00:00:00Z',
              closed_at: '2026-08-01T01:00:00Z',
              closed_by: APP_PERSONAS.firm.subject,
              steps: [],
              decisions: [],
            }),
        },
      )
    }
    // getInvoice GET: the first call is always `detail`; later calls consume
    // detailSequence in order, repeating the last entry once exhausted.
    const record =
      detailCalls === 0 ? detail : opts.detailSequence?.[detailCalls - 1] ?? opts.detailSequence?.at(-1) ?? detail
    detailCalls++
    return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(record) })
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, submitCalls, resolveCalls, decideCalls }
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
    // task-554/APPR-13-04 (AC-1, AC-2): the decision pair renders on every status,
    // including this failed/terminal one, but lives in the action column, not this card.
    expect(screen.getByTestId('detail-approve')).toBeTruthy()
    expect(screen.getByTestId('detail-reject')).toBeTruthy()
    expect(card.contains(screen.getByTestId('detail-approve'))).toBe(false)
    expect(card.contains(screen.getByTestId('detail-reject'))).toBe(false)
  })
})

// Every comparison in here is rail-internal. `violations-table` lives in the main column
// from BUG-13-02 on, and invoice-main-column unconditionally precedes invoice-rail, so a
// comparison straddling the two containers is true by DOM structure alone. Geometry is
// the only honest oracle for that relationship -- see invoice-surfaces.spec.ts D3/D9.
//
// FIXTURE GOTCHA: detailRecord()'s default rule_set_version is null, which renders
// `not-validated` instead of `violations-table` -- every positional test below overrides
// it to a real number so violations-table exists to compare against.
describe('InvoiceDetail terminal rail order', () => {
  it('AC-3: a historical rejection (demoted draft) titles "Last APP rejection"', async () => {
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
    const rejectionCards = screen.getAllByTestId('rejection-reasons')
    expect(rejectionCards).toHaveLength(1)
    const rejectionCard = rejectionCards[0]!

    // Rail-internal, replacing two comparisons against violations-table: failed-dead-end is
    // the rail's first member and a historical rejection its fifth.
    expect(failedCard.compareDocumentPosition(rejectionCard) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(rejectionCard).getByText('Last APP rejection')).toBeTruthy()
  })

  it('a rejected invoice with an empty rejection_reasons array renders no rejection card at all', async () => {
    mockDetailFetch(detailRecord({ status: 'rejected', rejection_reasons: [], rule_set_version: 3 }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByTestId('violations-table')
    expect(screen.queryByTestId('rejection-reasons')).toBeNull()
  })

  it('AC-2 extended: the live rejection card also precedes source-document-card, and the state strip precedes both', async () => {
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
    const strip = await screen.findByTestId('status-strip')
    expect(card.compareDocumentPosition(doc) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(strip.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

// The page-level approval AsyncState reaching the rail card, which renders no control of
// its own.
describe('InvoiceDetail approval state card', () => {
  it('a 404 renders the no-run empty state', async () => {
    mockDetailFetch(detailRecord({ rule_set_version: 3 }), [], {
      approvalResponse: { ok: false, status: 404, json: () => Promise.resolve({ error: 'no approval run for this invoice' }) },
    })

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByText('INV-FAILED-1')
    expect(screen.getByTestId('approval-empty')).toBeTruthy()
    // Scoped: an unscoped Retry also matches the Activity card's ErrorState (F-U M-2).
    expect(within(screen.getByTestId('approval-card')).queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('a 500 renders ErrorState, not the empty state', async () => {
    mockDetailFetch(detailRecord({ rule_set_version: 3 }), [], {
      approvalResponse: { ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) },
    })

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByText('INV-FAILED-1')
    expect(within(screen.getByTestId('approval-card')).getByRole('button', { name: 'Retry' })).toBeTruthy()
    expect(screen.queryByTestId('approval-empty')).toBeNull()
  })

  it('a rejection reason reaches the activity feed and never the violations table', async () => {
    const REASON = 'Budget exceeded, escalate to finance'
    // The reason's home is the feed's payload expansion now, not a rail ledger (D-AC-11).
    const rejectedEvent: AuditEvent = {
      id: 'ev-rejected-1',
      created_at: '2026-08-02T00:00:00Z',
      event: 'invoice.approval_rejected',
      actor: APP_PERSONAS.firm.subject,
      actor_name: APP_PERSONAS.firm.name,
      actor_kind: 'person',
      entity_id: 'inv-failed-1',
      company_name: 'Northgate Foods',
      company_scope: 'company',
      payload: { invoice_id: 'inv-failed-1', run_id: 'run-1', step_ord: 0, reason: REASON },
    }
    mockDetailFetch(detailRecord({ rule_set_version: 3, violations: [] }), [], {
      auditLog: auditLogOf([rejectedEvent]),
      approvalResponse: {
        ok: true,
        status: 200,
        json: () =>
          Promise.resolve({
            run_id: 'run-1',
            state: 'rejected',
            opened_at: '2026-08-01T00:00:00Z',
            closed_at: '2026-08-02T00:00:00Z',
            closed_by: APP_PERSONAS.firm.subject,
            steps: [],
            decisions: [
              {
                run_step_id: 'step-1',
                ord: 0,
                decision: 'rejected',
                actor: APP_PERSONAS.firm.subject,
                decided_at: '2026-08-02T00:00:00Z',
                reason: REASON,
              },
            ],
          }),
      },
    })

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const table = await screen.findByTestId('violations-table')
    const activity = screen.getByTestId('invoice-activity')
    // Awaited, not queried: /audit-log is its own fetch and the row lands after the detail.
    // Positive control -- the payload only renders once the row is expanded. Without it the
    // two absences below pass on a reason that reached no surface at all.
    fireEvent.click(await within(activity).findByTestId('audit-row'))
    expect(await within(activity).findByText(REASON)).toBeTruthy()
    expect(within(table).queryByText(REASON)).toBeNull()
    // The run still carries the decision; the card no longer restates it.
    expect(within(screen.getByTestId('approval-card')).queryByText(REASON)).toBeNull()
  })

  it('the card sits above Source document, and the state strip precedes the whole grid', async () => {
    mockDetailFetch(detailRecord({ rule_set_version: 3 }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const approvalCard = await screen.findByTestId('approval-card')
    const doc = screen.getByTestId('source-document-card')
    const strip = await screen.findByTestId('status-strip')
    expect(approvalCard.compareDocumentPosition(doc) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(strip.compareDocumentPosition(approvalCard) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('the card sits above a historical rejection card', async () => {
    mockDetailFetch(
      detailRecord({
        status: 'draft',
        rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
        rule_set_version: 3,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const rejection = await screen.findByTestId('rejection-reasons')
    const approvalCard = screen.getByTestId('approval-card')
    expect(approvalCard.compareDocumentPosition(rejection) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('invoiceDetail_trailCardIsGone', async () => {
    mockDetailFetch(detailRecord({ rule_set_version: 3 }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByText('INV-FAILED-1')
    // Positive control: without it the eight absences pass on an unmounted page.
    expect(screen.getByTestId('approval-card')).toBeTruthy()
    for (const retired of [
      'approval-trail',
      'approval-trail-card',
      'approval-trail-state',
      'approval-trail-step',
      'approval-trail-decision',
      'approval-trail-voided',
      'approval-trail-empty',
      'approval-trail-notify-note',
    ]) {
      expect(screen.queryByTestId(retired), retired).toBeNull()
    }
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

  // QA adversarial (mutation survivor): a blanket `disabled={!inv.can_edit}` across the whole
  // bar passes every other spec, because can_submit:true implies can_edit:true on every REAL
  // wire shape (TestCanSubmit_ImpliesCanEdit). Synthetic/contradictory fixture isolates the
  // two gates from each other.
  it('each control reads its own flag -- a wire with can_submit:true but can_edit:false disables Edit and leaves Submit enabled', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: true, submit_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-status-badge')

    expect(screen.queryByTestId('invoice-actions'), 'the bar mounts at every status').not.toBeNull()
    expect((screen.getByTestId('edit-toggle') as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByTestId('detail-submit') as HTMLButtonElement).disabled).toBe(false)
  })

  // Follow-up flagged in the executor's own Implementation Notes (structural deviation
  // that moved the skip/error banners outside the `can_edit` gate): the banner surviving
  // a can_edit:false refetch must not drag actionable controls out with it.
  //
  // BUG-14 restates the rule: what no banner may do is leave a *lifecycle* control
  // CLICKABLE -- edit / re-validate / submit all stay mounted and go disabled. view-ubl and
  // the decision pair are gated on `!editing` alone ([ubl-button-outside-invoice-actions],
  // task-554/APPR-13-04) and are asserted below so the disabled claims are not read as
  // "nothing else renders beside the banner".
  it('a duplicate_request skip banner surviving a can_edit:false refetch leaves the lifecycle controls present but disabled', async () => {
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
    expect(screen.queryByTestId('invoice-actions'), 'the bar survives the refetch').not.toBeNull()
    for (const id of ['edit-toggle', 'revalidate', 'detail-submit']) {
      expect((screen.getByTestId(id) as HTMLButtonElement).disabled, `${id} is disabled, not absent`).toBe(true)
    }
    // view-ubl and the decision pair are gated on !editing alone -- see this test's comment.
    expect(screen.getByTestId('view-ubl')).toBeTruthy()
    // task-554/APPR-13-04 (AC-1): gated on `!editing` only, like view-ubl -- a can_edit:false
    // refetch must not drag the decision pair out with the lifecycle controls it deletes.
    expect(screen.getByTestId('detail-approve')).toBeTruthy()
    expect(screen.getByTestId('detail-reject')).toBeTruthy()
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
          // /audit-log excluded like the rest: counting the activity card's fetch as a
          // detail GET satisfies the >=2 floor below before the poll tick ever lands.
          return method === 'GET' && !u.endsWith('/history') && !u.endsWith('/source-document') && !u.endsWith('/approval') && !u.includes('/audit-log') && !u.includes('/v1/extractions')
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

  // submitGate's role arm emits the sentence on every status, so pin that it never lands on
  // an ENABLED control: on a queued invoice the bar is up (BUG-14) and Submit is disabled.
  it("a preparer's role sentence on a non-editable invoice leaves Submit present and disabled", async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: false, submit_blocked_reason: ROLE_REASON }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    // Positive companion: the record really rendered, so the claims below are not an
    // empty document.
    await screen.findByTestId('invoice-status-badge')

    expect(screen.queryByTestId('invoice-actions'), 'the bar mounts at every status').not.toBeNull()
    expect((screen.getByTestId('detail-submit') as HTMLButtonElement).disabled).toBe(true)
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

describe('InvoiceDetail state strip: the approval-loading gate', () => {
  const RUN: ApprovalRun = {
    run_id: 'run-1',
    state: 'approved',
    opened_at: '2026-08-01T00:00:00Z',
    closed_at: '2026-08-01T01:00:00Z',
    closed_by: 'system',
    steps: [],
    decisions: [],
  }

  it('node 3 never flashes "Not required" while the approval fetch is in flight', async () => {
    let release: (run: ApprovalRun) => void = () => {}
    const gate = new Promise<ApprovalRun>((resolve) => {
      release = resolve
    })
    mockDetailFetch(detailRecord({ status: 'accepted' }), [], {
      approvalResponse: { ok: true, status: 200, json: () => gate },
    })

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    // The invoice itself has rendered, so the absence below is the approval fetch and not
    // the page still loading.
    await screen.findByText('INV-FAILED-1')
    expect(screen.queryByTestId('status-strip'), 'no strip until the run is known').toBeNull()

    release(RUN)
    const node3 = (await screen.findByTestId('status-strip')).querySelector('[data-key="approved"]')
    expect(node3?.getAttribute('data-state')).toBe('done')
    expect(node3?.textContent).not.toContain('Not required')
  })

  it('control: a settled 404 DOES caption node 3 "Not required" at this cursor', async () => {
    // Without this the assertion above would pass on a strip that can never say it.
    mockDetailFetch(detailRecord({ status: 'accepted' }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const node3 = (await screen.findByTestId('status-strip')).querySelector('[data-key="approved"]')
    expect(node3?.getAttribute('data-state')).toBe('not-required')
    expect(node3?.textContent).toContain('Not required')
  })
})

// QA (AUDIT-09-02 Mode B). Three holes the migrated suite leaves open.
describe('InvoiceDetail state strip: mount position and the two unowned error branches', () => {
  it('the strip renders before .pf-detail-grid and outside it, not as a card inside it', async () => {
    // The ordering specs above only pin "strip precedes the rail cards", which a strip
    // moved INSIDE the grid still satisfies -- the whole app suite stays green on that
    // move. AC-1's containment claim is otherwise proved only by the browser sweep
    // (invoice-surfaces.spec.ts "D: the strip stays inside the 96px band").
    mockDetailFetch(detailRecord({ status: 'accepted' }))
    const { container } = render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const strip = await screen.findByTestId('status-strip')
    const grid = container.querySelector('.pf-detail-grid')
    expect(grid, 'the grid is the anchor this assertion is about').not.toBeNull()
    expect(strip.compareDocumentPosition(grid!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(grid!.contains(strip), 'the strip is a sibling of the grid, never a card inside it').toBe(false)
  })

  // ---- AUDIT-09-07 / F-E. D-07-1 accepts the history degradation and upgrades the old
  // characterisation into a guarantee: a history 500 WITHHOLDS attribution, it never
  // ASSERTS a false one. The state-equality row below is exactly the condition under which
  // that decision must be reopened.

  const HISTORY_500: MockResponse = {
    ok: false,
    status: 500,
    json: () => Promise.resolve({ error: 'history unavailable' }),
  }
  const HISTORY_ACTOR = '9a000000-0000-4000-8000-0000000000ab'
  const HISTORY_ACTOR_NAME = 'Adaeze Nwosu'
  const HEALTHY_HISTORY: StatusChange[] = [
    { from_status: null, to_status: 'draft', changed_at: '2026-07-01T09:00:00Z', actor: HISTORY_ACTOR, actor_name: HISTORY_ACTOR_NAME, actor_kind: 'person' },
    { from_status: 'draft', to_status: 'validated', changed_at: '2026-07-01T10:00:00Z', actor: HISTORY_ACTOR, actor_name: HISTORY_ACTOR_NAME, actor_kind: 'person' },
    { from_status: 'validated', to_status: 'queued', changed_at: '2026-07-01T11:00:00Z', actor: HISTORY_ACTOR, actor_name: HISTORY_ACTOR_NAME, actor_kind: 'person' },
    { from_status: 'queued', to_status: 'accepted', changed_at: '2026-07-01T12:00:00Z', actor: HISTORY_ACTOR, actor_name: HISTORY_ACTOR_NAME, actor_kind: 'person' },
  ]

  interface StripReadout {
    key: string
    state: string
    caption: string
  }

  async function stripReadout(): Promise<StripReadout[]> {
    const strip = await screen.findByTestId('status-strip')
    return [...strip.querySelectorAll('[data-testid="strip-node"]')].map((n) => ({
      key: n.getAttribute('data-key') ?? '',
      state: n.getAttribute('data-state') ?? '',
      caption: n.querySelector('[data-testid="strip-actor"]')?.textContent ?? '',
    }))
  }

  it('invoiceDetail_aHistory500WithholdsAttributionWithoutFabricatingIt', async () => {
    mockDetailFetch(detailRecord({ status: 'accepted' }), HEALTHY_HISTORY)
    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    const healthy = await stripReadout()
    cleanup()

    mockDetailFetch(detailRecord({ status: 'accepted' }), [], { historyResponse: HISTORY_500 })
    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    const sick = await stripReadout()

    // Positive control: both renders produced the whole strip.
    expect(healthy).toHaveLength(5)
    expect(sick).toHaveLength(5)
    // THE guarantee. `status` and `run` decide every state; history supplies only at/actor
    // (invoiceStrip.ts's module invariant, proven here through the real fetch ladder rather
    // than at the pure-core level). If history ever leaked into node state, this reds.
    expect(sick.map((n) => [n.key, n.state])).toEqual(healthy.map((n) => [n.key, n.state]))
    // ...and the two renders really did differ, so the equality above is not two identical
    // fixtures agreeing with themselves.
    expect(sick.map((n) => n.caption)).not.toEqual(healthy.map((n) => n.caption))
    // Names the spine explicitly, so the per-node loop below cannot pass on zero iterations
    // and so the retired characterisation's draft-is-done pin survives the replacement.
    expect(sick.filter((n) => n.state === 'done').map((n) => n.key)).toEqual([
      'draft',
      'validated',
      'queued',
      'accepted',
    ])

    for (const [i, node] of sick.entries()) {
      if (node.state !== 'done') continue
      expect(healthy[i]!.caption, `${node.key}: the healthy render must attribute the node`).toMatch(/^\d\d:\d\d · Adaeze$/)
      // Withheld, never fabricated: no invented time, and never a claim word on a done node.
      expect(node.caption, `${node.key}: a 500 must withhold, not invent`).toBe('—')
    }
    // Node 3 follows the approval run, not history, so it reads the same in both renders.
    const node3 = (r: StripReadout[]) => r.find((n) => n.key === 'approved')!
    expect(node3(sick).state).toBe('not-required')
    expect(node3(sick).caption).toBe(node3(healthy).caption)

    // D-07-1, accepted: no error and nothing to retry for history. Page-wide is the
    // strongest form here because nothing else on this render errors.
    expect(screen.queryByText('Something went wrong')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('invoiceDetail_aHistory500LeavesTheTransitionRecordInTheFeed', async () => {
    // The whole basis of D-07-1: every invoice_status_history row is written in the same
    // transaction as an audit row (store.go:262/269, :1846/:1853), so the same transitions
    // -- with actor and time -- arrive on an INDEPENDENT fetch one card below, which does
    // have ErrorState + Retry. F-AI records that seeded demo data has no audit rows.
    const TRANSITION_ACTOR = '5b000000-0000-4000-8000-0000000000cd'
    const TRANSITION_NAME = 'Ibrahim Bello'
    const TRANSITIONED_AT = '2026-07-01T12:00:00Z'
    const transitioned: AuditEvent = {
      id: 'ev-transitioned-1',
      created_at: TRANSITIONED_AT,
      event: 'invoice.transitioned',
      actor: TRANSITION_ACTOR,
      actor_name: TRANSITION_NAME,
      actor_kind: 'person',
      entity_id: 'ent-1',
      company_name: 'Northgate Foods',
      company_scope: 'company',
      payload: { id: 'inv-failed-1', from: 'queued', to: 'accepted' },
    }

    mockDetailFetch(detailRecord({ status: 'accepted' }), [], {
      historyResponse: HISTORY_500,
      auditLog: auditLogOf([transitioned]),
    })
    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    // The degradation is live in this same render, so the feed below is the compensating
    // surface for THIS failure and not a second, healthier page.
    const sick = await stripReadout()
    expect(sick.filter((n) => n.state === 'done').map((n) => n.caption)).toEqual(['—', '—', '—', '—'])

    const activity = screen.getByTestId('invoice-activity')
    const row = await within(activity).findByTestId('audit-row')
    expect(within(row).getByText(TRANSITION_NAME)).toBeTruthy()
    expect(within(row).queryByText(TRANSITION_ACTOR), 'the raw subject never renders').toBeNull()
    expect(fmtDateTime(TRANSITIONED_AT)).not.toBe('—')
    expect(within(row).getByText(fmtDateTime(TRANSITIONED_AT))).toBeTruthy()
    cleanup()

    // ...and when the compensating surface itself fails, THAT one is retryable. Scoped to
    // invoice-activity (F-U M-2): an unscoped Retry now matches the approval card too.
    mockDetailFetch(detailRecord({ status: 'accepted' }), [], {
      historyResponse: HISTORY_500,
      auditLogResponse: { ok: false, status: 500, json: () => Promise.resolve({ error: 'audit log unavailable' }) },
    })
    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const failed = await screen.findByTestId('invoice-activity')
    expect(await within(failed).findByText('Something went wrong')).toBeTruthy()
    expect(within(failed).getByRole('button', { name: 'Retry' })).toBeTruthy()
  })

  it('a 500 on GET /approval captions node 3 "Not required", and the approval card contradicts it', async () => {
    // The mount gate excludes `idle` and `loading` only, so an ERRORED approval fetch
    // renders the strip with run=null and node 3 claims a compliance fact nobody knows.
    // The arch names this residual and accepts it; no test produced it until now.
    mockDetailFetch(detailRecord({ status: 'accepted' }), [], {
      approvalResponse: { ok: false, status: 500, json: () => Promise.resolve({ error: 'approval unavailable' }) },
    })

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const node3 = (await screen.findByTestId('status-strip')).querySelector('[data-key="approved"]')
    expect(node3?.getAttribute('data-state')).toBe('not-required')
    expect(node3?.textContent).toContain('Not required')
    // F-E, settled: the approval card keeps ErrorState + Retry, because it is the only
    // thing contradicting node 3's claim. Asserted positively and INSIDE approval-card --
    // a page-wide queryByText would go green on any other card's error state.
    const approvalCard = screen.getByTestId('approval-card')
    expect(within(approvalCard).getByText('Something went wrong')).toBeTruthy()
    expect(within(approvalCard).getByRole('button', { name: 'Retry' })).toBeTruthy()
  })
})

// RED specs (task-392, BUG-03-03, Mode A). Every demo-data fixture carries the literal
// actor 'system', which renders fine today and hides the raw-UUID defect -- these pass a
// real StatusChange[] through mockDetailFetch instead of relying on the [] default.
describe('InvoiceDetail state strip: actor resolution ([actor-label-shared])', () => {
  it('AC2: the strip renders a person, not a subject uuid', async () => {
    const history: StatusChange[] = [
      { from_status: null, to_status: 'draft', changed_at: '2026-07-01T00:00:00Z', actor: APP_PERSONAS.firm.subject, actor_name: APP_PERSONAS.firm.name, actor_kind: 'person' },
    ]
    mockDetailFetch(detailRecord(), history)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const strip = await screen.findByTestId('status-strip')
    // The wire's actor_name is the whole label; the strip then reduces a resolved person to
    // its first token (invoiceStrip.ts display()). The ' · Okafor & Partners' suffix was
    // APP_PERSONAS' contribution and must be gone.
    const caption = strip.querySelector('[data-key="draft"] [data-testid="strip-actor"]')
    expect(caption?.textContent).toMatch(new RegExp(`^\\d\\d:\\d\\d · ${APP_PERSONAS.firm.name.split(' ')[0]}$`))
    expect(document.body.textContent).not.toContain(APP_PERSONAS.firm.org)
    expect(document.body.textContent).not.toContain(APP_PERSONAS.firm.subject)
  })

  it('AC2: an unknown subject still renders raw, in mono', async () => {
    const unknown = '7f214c0a-9d33-4b21-8e55-0a1b2c3d4e5f'
    const history: StatusChange[] = [
      { from_status: null, to_status: 'draft', changed_at: '2026-07-01T00:00:00Z', actor: unknown, actor_name: unknown, actor_kind: 'raw' },
    ]
    mockDetailFetch(detailRecord(), history)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const strip = await screen.findByTestId('status-strip')
    // The subject and its timestamp share one caption span, so the whole span is mono.
    const actorEl = within(strip).getByText(new RegExp(unknown))
    expect(actorEl.getAttribute('data-testid')).toBe('strip-actor')
    expect(actorEl.className.split(' ')).toContain('mono')
  })

  // AUDIT-02-04 leak regression, at the render layer this time (Core AC-9's actual
  // surface). ctx is firm-mode; the row is actored by the OTHER tenant's admin, which
  // the RLS-scoped server could not name, so it answers 'raw' with the subject verbatim.
  // APP_PERSONAS holds that subject anyway (auth.ts:47-60), so any fall-through prints
  // Honeywell's admin and employer to an Okafor viewer. See actor.test.ts's
  // actorLabel_neverConsultsPersonasWhenTheServerAnswered for the unit-level twin.
  // Reach: the two document.body sweeps below cover the whole page, so they now police
  // the activity card's actor and Company cells too, not just the strip's. Inert only
  // while mockDetailFetch's audit log is empty (F-Q).
  it('AC-9: a row the server could not name never borrows the other tenant\'s persona', async () => {
    const honeywellAdmin = APP_PERSONAS.inhouse.subject
    const history: StatusChange[] = [
      { from_status: null, to_status: 'draft', changed_at: '2026-07-01T00:00:00Z', actor: honeywellAdmin, actor_name: honeywellAdmin, actor_kind: 'raw' },
    ]
    mockDetailFetch(detailRecord(), history)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const strip = await screen.findByTestId('status-strip')
    // Positive control first: the subject IS on screen, in mono. Without it the two
    // absence assertions below would pass on a strip that rendered nothing at all.
    const actorEl = within(strip).getByText(new RegExp(honeywellAdmin))
    expect(actorEl.getAttribute('data-testid')).toBe('strip-actor')
    expect(actorEl.className.split(' ')).toContain('mono')
    expect(document.body.textContent).not.toContain(APP_PERSONAS.inhouse.name)
    expect(document.body.textContent).not.toContain(APP_PERSONAS.inhouse.org)
  })

  // AUDIT-02-04 QA. AC-5's "never a blank cell" lives HERE, not in actor.test.ts's
  // `.not.toBe('')` sweep -- a name of one space clears that sweep and still paints
  // nothing. CHARACTERISATION of today's behaviour, reported not fixed: the '' guard is
  // exact (actor.ts:28) and the server's ladder stops on ' ' for the same reason
  // (internal/actor/actor.go:36), so a membership row with a whitespace display_name
  // reaches this span. The security property still holds and is asserted alongside.
  // Reach: the two document.body sweeps below cover the whole page, so they now police
  // the activity card's actor and Company cells too, not just the strip's. Inert only
  // while mockDetailFetch's audit log is empty (F-Q).
  it('QA: a whitespace-only resolved name paints an empty actor cell, and still no persona', async () => {
    const honeywellAdmin = APP_PERSONAS.inhouse.subject
    const history: StatusChange[] = [
      { from_status: null, to_status: 'draft', changed_at: '2026-07-01T00:00:00Z', actor: honeywellAdmin, actor_name: ' ', actor_kind: 'person' },
    ]
    mockDetailFetch(detailRecord(), history)

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const strip = await screen.findByTestId('status-strip')
    const actors = screen.getAllByTestId('strip-actor')
    expect(actors, 'a caption on every node, whatever its state').toHaveLength(5)
    const caption = strip.querySelector('[data-key="draft"] [data-testid="strip-actor"]')
    // display() returns parts[0] of a trimmed split, so the space collapses to '' and the
    // caption trails its separator -- open finding, characterised not fixed.
    expect(caption?.textContent, 'the attribution paints a bare separator').toMatch(/^\d\d:\d\d · $/)
    expect(caption?.className.split(' '), 'a person kind is never mono').not.toContain('mono')
    expect(document.body.textContent).not.toContain(APP_PERSONAS.inhouse.name)
    expect(document.body.textContent).not.toContain(APP_PERSONAS.inhouse.org)
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
  // placeholder, mirroring AC2's guarantee for the strip's attribution.
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

function mockRegisterFetch(invoices: InvoiceRecord[]) {
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
      // Same invoice, two wires: the list carries `approval`, the detail one does not.
      const listRow: InvoiceRecord = { ...record, approval: null }

      mockDetailFetch(record)
      render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
      await screen.findByText(record.invoice_number)
      const detailTin = screen.getByTestId('buyer-tin')
      const detailText = detailTin.textContent
      const detailColor = detailTin.style.color
      cleanup()

      mockRegisterFetch([listRow])
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
    // Same invoice, two wires: the list carries `approval`, the detail one does not.
    const listRow: InvoiceRecord = { ...record, approval: null }

    mockDetailFetch(record)
    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText(record.invoice_number)
    const detailTin = screen.getByTestId('buyer-tin')
    const detailText = detailTin.textContent
    const detailColor = detailTin.style.color
    cleanup()

    mockRegisterFetch([listRow])
    render(<InvoicesList ctx={registerCtx()} />)
    await screen.findByText(record.invoice_number)
    const listTin = screen.getByTestId('buyer-tin')
    expect(listTin.textContent, 'InvoicesList').toBe(detailText)
    expect(listTin.style.color, 'InvoicesList').toBe(detailColor)
    cleanup()

    render(
      <Row
        r={listRow}
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

// RED specs (BUG-13-01, Mode A). The header chip does not exist yet, so the first spec
// fails on a null query and on the fifth <td> that still restates the version. The second
// is green today only because nothing renders a chip at all -- after the fix it is the
// only oracle for [chip-gating], which stops a never-validated invoice being told a
// rule-set version it was never evaluated against.
describe('InvoiceDetail Compliance panel: the rule-set version is stated once (BUG-13-01)', () => {
  it('invoiceDetail_complianceHeaderStatesTheRuleSetVersionOnce', async () => {
    mockDetailFetch(
      detailRecord({
        rule_set_version_id: 'rsv-4',
        rule_set_version: 4,
        violations: [{ rule_key: 'buyer-tin-required', severity: 'error', message: 'Buyer TIN is required.', path: 'buyer.tin' }],
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    // Floor: an empty violations array renders the clean-pass block instead, and the
    // last assertion would then hold over zero cells.
    const cells = within(screen.getByTestId('violations-table')).getAllByRole('cell')
    expect(cells.length).toBeGreaterThan(0)

    const chip = screen.queryByTestId('compliance-ruleset-version')
    expect(chip, 'the version chip must render beside the Compliance title').not.toBeNull()
    expect(chip?.textContent).toBe('Rule-set v4')

    expect(
      cells.map((c) => c.textContent),
      'no per-row cell restates the rule-set version',
    ).not.toContain('4')
  })

  it('invoiceDetail_unvalidatedInvoiceStatesNoVersion', async () => {
    mockDetailFetch(detailRecord({ rule_set_version: null, rule_set_version_id: null }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    expect(screen.getByTestId('not-validated')).toBeTruthy()
    expect(screen.queryByTestId('compliance-ruleset-version')).toBeNull()
  })

  // QA Mode B (BUG-13-01). `!= null`, not truthiness: rule-set 0 is a validated invoice,
  // and a `&&` gate would silently drop both the chip and the table for it. The two other
  // chip specs use 4, so neither can tell the two gates apart.
  it('invoiceDetail_ruleSetVersionZeroStillStatesTheVersionAndRendersTheTable', async () => {
    mockDetailFetch(
      detailRecord({
        rule_set_version_id: 'rsv-0',
        rule_set_version: 0,
        violations: [{ rule_key: 'buyer-tin-required', severity: 'error', message: 'Buyer TIN is required.', path: 'buyer.tin' }],
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    expect(screen.getByTestId('compliance-ruleset-version').textContent).toBe('Rule-set v0')
    expect(screen.queryByTestId('not-validated')).toBeNull()
    expect(within(screen.getByTestId('violations-table')).getAllByRole('cell')).toHaveLength(4)
  })

  // QA Mode B (BUG-13-01). Two live oracles read the version scoped INSIDE
  // `violations-table` -- the clean-pass sentence here at :2076 and at
  // invoice-surfaces.spec.ts:657-658. Both stop discriminating if the chip ever drifts
  // into that subtree, and `compliance-ruleset-version` alone cannot see the move.
  it('invoiceDetail_theVersionChipSitsOutsideTheViolationsTableSubtree', async () => {
    mockDetailFetch(
      detailRecord({
        rule_set_version_id: 'rsv-4',
        rule_set_version: 4,
        violations: [{ rule_key: 'buyer-tin-required', severity: 'error', message: 'Buyer TIN is required.', path: 'buyer.tin' }],
      }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    const chip = screen.getByTestId('compliance-ruleset-version')
    const table = screen.getByTestId('violations-table')
    expect(table.contains(chip)).toBe(false)
    // Beside the title, not merely elsewhere on the page: same parent as "Compliance".
    expect(chip.parentElement?.querySelector('.card-title')?.textContent).toBe('Compliance')
  })

  // QA Mode B (BUG-13-01). OPEN, adjudication owed: on a CLEAN-PASS validated invoice the
  // version now reads twice inside the Compliance card -- the new header chip, and the
  // green block's own "Evaluated against rule-set v4." Core AC-5 says the version is
  // stated once in the card; `## Out of Scope` fences the clean-pass block's wording.
  // Characterized, not endorsed: flip this to a `toBe(1)` if the user rules it a defect.
  it('invoiceDetail_cleanPassInvoiceStatesTheVersionTwiceInTheCard', async () => {
    mockDetailFetch(
      detailRecord({ status: 'draft', rule_set_version_id: 'rsv-4', rule_set_version: 4, violations: [] }),
    )

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    await screen.findByText('INV-FAILED-1')

    const chip = screen.getByTestId('compliance-ruleset-version')
    const statements = [chip.textContent, screen.getByTestId('violations-table').textContent].filter((t) => t?.includes('rule-set v4') || t === 'Rule-set v4')
    expect(statements, 'the chip and the green block both name the rule-set version').toHaveLength(2)
    expect(chip.textContent).toBe('Rule-set v4')
    expect(screen.getByTestId('violations-table').textContent).toContain('Evaluated against rule-set v4.')
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
    // The bar mounts on failed too (BUG-14), so the claim is containment, not absence.
    expect(within(screen.getByTestId('invoice-actions')).queryByTestId('resolve-outside')).toBeNull()
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
          // /audit-log excluded like the rest: counting the activity card's fetch as a
          // detail GET satisfies the >=2 floor below before the poll tick ever lands.
          return method === 'GET' && !u.endsWith('/history') && !u.endsWith('/source-document') && !u.endsWith('/approval') && !u.includes('/audit-log') && !u.includes('/v1/extractions')
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
          // /audit-log excluded like the rest: counting the activity card's fetch as a
          // detail GET satisfies the >=2 floor below before the poll tick ever lands.
          return method === 'GET' && !u.endsWith('/history') && !u.endsWith('/source-document') && !u.endsWith('/approval') && !u.includes('/audit-log') && !u.includes('/v1/extractions')
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

  // The whole reason the control sits outside `invoice-actions`: can_view_ubl tracks CONTENT,
  // not lifecycle, so the document stays reachable on the statuses where every control inside
  // that bar is disabled.
  it('T3/AC2: renders outside the bar, on a status where the bar itself is disabled', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: false }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const viewUbl = await screen.findByTestId('view-ubl')
    expect(screen.getByTestId('invoice-actions').contains(viewUbl), 'never inside the bar').toBe(false)
    expect((screen.getByTestId('edit-toggle') as HTMLButtonElement).disabled, 'floor: the bar really is disabled here').toBe(true)
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

  // The `!editing` half of AC#3, and since BUG-14-01 the ONLY spec that can see the bar's
  // own `!editing`: it needs the banner, because once 05 widened the outer column to
  // `!editing || banner`, `editing` with NO banner drops the whole column -- so the bar's
  // gate is load-bearing in exactly one state, and every banner-less spec passes with it
  // widened. KILLS: widening the bar's own `{!editing && (`.
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
    // task-554/APPR-13-04 (AC-1) anchors: the decision pair is gated on `!editing` too,
    // and must be up before editing starts for its absence below to be a real assertion.
    expect(screen.getByTestId('detail-approve'), 'anchor: approve is up before editing').toBeTruthy()
    expect(screen.getByTestId('detail-reject'), 'anchor: reject is up before editing').toBeTruthy()

    fireEvent.click(screen.getByTestId('edit-toggle'))

    expect(screen.getByTestId('detail-submit-skipped'), 'the column is still mounted').toBeTruthy()
    expect(screen.queryAllByTestId('invoice-actions')).toHaveLength(0)
    for (const id of ['edit-toggle', 'revalidate', 'detail-submit', 'detail-approve', 'detail-reject']) {
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

// RED specs (task-554, APPR-13-04, Mode A). Neither `detail-approve` nor `detail-reject`
// exists yet, so every row below fails on a missing element, never an import/type error.
// Arm/confirm and the reject-reason input row are APPR-13-05 -- this subtask (and these
// specs) cover only the resting/idle pair and its four-layer disabled recipe.
describe('InvoiceDetail Approve/Reject controls (task-554, APPR-13-04)', () => {
  const ID = 'inv-decision-1'
  const editable = { status: 'validated' as InvoiceStatus, can_edit: true, can_revalidate: false, can_submit: true }
  const S = 'Only a validated invoice can be approved or rejected.'
  // internal/invoice/handlers.go:378-394's five approvalGate rungs, verbatim -- mirrors
  // approvals.test.ts:1064-1070's APPROVAL_GATE_SENTENCES, duplicated here since that
  // const is module-private to that file.
  const APPROVAL_GATE_SENTENCES = [
    'Only an admin or a reviewer can approve or reject an invoice — ask an approver on your team.',
    'Only a validated invoice can be approved or rejected.',
    'This invoice has no approval run to decide on.',
    "This invoice's approval run is already closed.",
    "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role.",
  ]

  it('1: both controls render on a queued, can_edit:false invoice, alongside a disabled actions bar (AC-1)', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_approve: false, approve_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    expect(await screen.findByTestId('detail-approve')).toBeTruthy()
    expect(screen.getByTestId('detail-reject')).toBeTruthy()
    expect(screen.queryByTestId('invoice-actions'), 'the bar is disabled here, never gone').not.toBeNull()
    expect((screen.getByTestId('edit-toggle') as HTMLButtonElement).disabled).toBe(true)
  })

  it('2: the pair sits outside invoice-actions and after view-ubl in document order (AC-1, AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable, can_approve: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const bar = await screen.findByTestId('invoice-actions')
    const viewUbl = screen.getByTestId('view-ubl')
    const approveBtn = screen.getByTestId('detail-approve')
    expect(within(bar).queryByTestId('detail-approve')).toBeNull()
    expect(within(bar).queryByTestId('detail-reject')).toBeNull()
    expect(viewUbl.compareDocumentPosition(approveBtn) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('3: the pair hides while editing (AC-1)', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable, can_approve: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    expect(await screen.findByTestId('detail-approve')).toBeTruthy()

    fireEvent.click(screen.getByTestId('edit-toggle'))

    expect(screen.queryAllByTestId('detail-approve')).toHaveLength(0)
    expect(screen.queryAllByTestId('detail-reject')).toHaveLength(0)
  })

  it('4: permutation 1 -- both allowed, no reason: enabled, unmuted, no reason node (AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: true, approve_blocked_reason: null, can_reject: true, reject_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const approveBtn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement
    const rejectBtn = screen.getByTestId('detail-reject') as HTMLButtonElement
    expect(approveBtn.disabled).toBe(false)
    expect(rejectBtn.disabled).toBe(false)
    expect(approveBtn.style.background).toBe('')
    expect(rejectBtn.style.background).toBe('')
    expect(screen.queryAllByTestId('approve-blocked-reason')).toHaveLength(0)
    expect(screen.queryAllByTestId('reject-blocked-reason')).toHaveLength(0)
  })

  it('5: permutation 2 -- both blocked, same reason: disabled, muted, one visible node (AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S, can_reject: false, reject_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const approveBtn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement
    const rejectBtn = screen.getByTestId('detail-reject') as HTMLButtonElement
    expect(approveBtn.disabled).toBe(true)
    expect(rejectBtn.disabled).toBe(true)
    expect(approveBtn.style.background).not.toBe('')
    expect(rejectBtn.style.background).not.toBe('')
    const nodes = screen.getAllByText(S)
    expect(nodes).toHaveLength(1)
    expect(nodes[0].textContent).toBe(S)
  })

  it('6: permutation 3 -- both blocked, no reason: disabled, muted, no reason node, nothing invented (AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: null, can_reject: false, reject_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const approveBtn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement
    const rejectBtn = screen.getByTestId('detail-reject') as HTMLButtonElement
    expect(approveBtn.disabled).toBe(true)
    expect(rejectBtn.disabled).toBe(true)
    expect(approveBtn.style.background).not.toBe('')
    expect(rejectBtn.style.background).not.toBe('')
    expect(screen.queryAllByTestId('approve-blocked-reason')).toHaveLength(0)
    expect(screen.queryAllByTestId('reject-blocked-reason')).toHaveLength(0)
    expect(approveBtn.hasAttribute('title')).toBe(false)
    expect(approveBtn.hasAttribute('aria-describedby')).toBe(false)
    expect(rejectBtn.hasAttribute('title')).toBe(false)
    expect(rejectBtn.hasAttribute('aria-describedby')).toBe(false)
  })

  it('7: permutation 4 -- a contradictory wire (can_approve:true with a reason set) still enables Approve, and the reason still renders (AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: true, approve_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const approveBtn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement
    expect(approveBtn.disabled).toBe(false)
    expect(screen.getByTestId('approve-blocked-reason').textContent).toBe(S)
  })

  it.each(APPROVAL_GATE_SENTENCES)('8: the visible reason is byte-identical to the wire -- %s (AC-2)', async (sentence) => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: sentence, can_reject: false, reject_blocked_reason: sentence }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const reasonEl = await screen.findByTestId('approve-blocked-reason')
    expect(reasonEl.textContent).toBe(sentence)
  })

  it('9: the disabled Approve neutralises filter, guarding .v2-btn-primary:hover from brightening it (AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement

    expect(btn.style.filter).toBe('none')
  })

  it('10: an enabled Approve carries no muted inline style, preserving :hover (AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: true, approve_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement

    expect(btn.disabled).toBe(false)
    expect(btn.style.background).toBe('')
    expect(btn.style.color).toBe('')
    expect(btn.style.cursor).toBe('')
  })

  it('11: the disabled Approve refuses focus, and its reason stays reachable (AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = await screen.findByTestId('detail-approve')

    btn.focus()
    expect(document.activeElement).not.toBe(btn)
    expect(screen.getByTestId('approve-blocked-reason').textContent).toBe(S)
  })

  it('12: aria-describedby on both controls resolves to a real element in the document (AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S, can_reject: false, reject_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const approveBtn = await screen.findByTestId('detail-approve')
    const rejectBtn = screen.getByTestId('detail-reject')

    const approveDescribedBy = approveBtn.getAttribute('aria-describedby')
    const rejectDescribedBy = rejectBtn.getAttribute('aria-describedby')
    expect(approveDescribedBy).toBeTruthy()
    expect(rejectDescribedBy).toBeTruthy()
    expect(document.getElementById(approveDescribedBy as string)).toBeTruthy()
    expect(document.getElementById(rejectDescribedBy as string)).toBeTruthy()
  })

  it('13: a sentence shared by both controls prints exactly once in the document (AC-2)', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S, can_reject: false, reject_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('detail-approve')

    expect(screen.getAllByText(S)).toHaveLength(1)
  })

  it('14: clicking a disabled Approve sends no request and adds no new control to the page (AC-2)', async () => {
    const { fetchMock } = mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const btn = await screen.findByTestId('detail-approve')
    const callsBefore = fetchMock.mock.calls.length
    const buttonsBefore = screen.getAllByRole('button').length

    fireEvent.click(btn)

    expect(fetchMock.mock.calls.length).toBe(callsBefore)
    expect(screen.getAllByRole('button').length).toBe(buttonsBefore)
  })

  // QA-added (task-554 row 17), corrected during Stage 3 (task-554): the row as originally
  // written asserted `detail-approve`/`detail-reject`'s `parentElement` equals `view-ubl`'s
  // AND `invoice-actions`'s -- i.e. bare siblings with no wrapper. That is not AC-1's
  // invariant: two buttons side by side need a row wrapper (`detail-decision-actions`,
  // this repo's own precedent for a row inside this column being `invoice-actions`'s own
  // inner `<div style={{display:'flex'}}>`), and the plan's markup sketch names that
  // wrapper explicitly. AC-1's "sibling BLOCK... NOT inside that div" constrains which
  // CONTAINER, not whether one exists. Reworked to assert the real invariant, strengthened
  // rather than weakened: (a) neither button is inside `invoice-actions`, the guarantee
  // that survives a `can_edit:false` refetch and is the whole point of AC-1; (b) the shared
  // wrapper is a direct child of the same parent holding `view-ubl` and `invoice-actions`,
  // so all three are siblings in the action column; (c) all three render under the same
  // `!editing` gate, never `can_edit` -- proven by a second render where `can_edit:false`
  // leaves every one of them mounted, the bar merely disabled.
  it('17: detail-decision-actions is a sibling of view-ubl and invoice-actions in the action column, gated on !editing like view-ubl -- never on can_edit', async () => {
    mockDetailFetch(detailRecord({ id: ID, ...editable, can_approve: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const bar = await screen.findByTestId('invoice-actions')
    const viewUbl = screen.getByTestId('view-ubl')
    const decisionActions = screen.getByTestId('detail-decision-actions')
    const approveBtn = screen.getByTestId('detail-approve')
    const rejectBtn = screen.getByTestId('detail-reject')

    expect(bar.contains(approveBtn)).toBe(false)
    expect(bar.contains(rejectBtn)).toBe(false)
    expect(decisionActions.parentElement).toBe(viewUbl.parentElement)
    expect(decisionActions.parentElement).toBe(bar.parentElement)
    cleanup()

    // can_edit:false removes none of the three rows -- all are gated on !editing alone
    // (BUG-14). The bar stays in the column with its controls disabled.
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_approve: true, can_reject: true }))
    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    expect(await screen.findByTestId('detail-decision-actions')).toBeTruthy()
    expect(screen.getByTestId('invoice-actions').parentElement).toBe(screen.getByTestId('view-ubl').parentElement)
    expect((screen.getByTestId('edit-toggle') as HTMLButtonElement).disabled).toBe(true)
  })

  // QA-added (task-554 row 19): closes the Approve/Reject asymmetry left by row 11, which
  // only proves reachability for Approve. AC-2 layer 3's whole justification is that a
  // disabled button is out of the tab order, so Reject's own reason must clear the same
  // bar, not just Approve's.
  it('19: the disabled Reject refuses focus while its reason stays reachable in the accessible tree', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S, can_reject: false, reject_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const rejectBtn = await screen.findByTestId('detail-reject')

    rejectBtn.focus()
    expect(document.activeElement).not.toBe(rejectBtn)
    const describedBy = rejectBtn.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()
    const reasonEl = document.getElementById(describedBy as string)
    expect(reasonEl).toBeTruthy()
    expect(reasonEl?.textContent).toBe(S)
    expect(screen.getByText(S)).toBeTruthy()
  })

  // QA-added (task-554, Mode B adversarial): HTML-significant characters in the reason
  // survive to textContent unchanged -- React's own child-text escaping, not a bespoke
  // sanitiser, so this pins that nothing mangles the wire string on the way to the DOM.
  it('a reason with an ampersand, an em dash and a quote survives to the DOM unchanged', async () => {
    const tricky = 'Blocked — "urgent" review needed & a second signer must confirm.'
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: tricky }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const reasonEl = await screen.findByTestId('approve-blocked-reason')
    expect(reasonEl.textContent).toBe(tricky)
  })

  // QA-added (task-554, Mode B adversarial, defensive): Stage 1 confirmed can_approve and
  // can_reject can never differ on the real wire (handlers.go:481-508 shares one
  // canDecide/decideReason pair) -- this pins that the component still behaves sanely on a
  // shape the type permits but the server never sends, not that the server sends it.
  it('defensive: can_approve true with can_reject false and a reject-only reason does not crash, and Approve stays enabled', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: true, approve_blocked_reason: null, can_reject: false, reject_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const approveBtn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement
    const rejectBtn = screen.getByTestId('detail-reject') as HTMLButtonElement
    expect(approveBtn.disabled).toBe(false)
    expect(rejectBtn.disabled).toBe(true)
  })

  // QA-added (task-554, Mode B adversarial): D-49's accepted density cost -- the pair
  // renders on EVERY status, terminal included. Nothing else in this file's task-554 suite
  // pins the full union; a later narrowing (e.g. excluding 'accepted'/'rejected') would
  // pass every other row here unnoticed without this loop.
  it.each(ALL_STATUSES)('renders on a %s invoice (D-49 density cost)', async (status) => {
    mockDetailFetch(detailRecord({ id: ID, status, can_approve: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    expect(await screen.findByTestId('detail-approve')).toBeTruthy()
    expect(screen.getByTestId('detail-reject')).toBeTruthy()
  })

  // The two surfaces -- the decision pair here and ApprovalStateCard -- must agree on the
  // same invoice. A disabled pair alongside the no-run empty state is a real, reachable
  // state, not a contradiction between the two.
  it('both controls render disabled while the approval card shows its empty state, in agreement', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S, can_reject: false, reject_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    expect(((await screen.findByTestId('detail-approve')) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByTestId('detail-reject') as HTMLButtonElement).disabled).toBe(true)
    expect(await screen.findByTestId('approval-empty')).toBeTruthy()
  })

  // CodeRabbit PR #167 fix: decisionBlockedReasons's array was positional
  // (index 0 always rendered as "approve"), so a reject-only reason was mislabelled
  // as the approve reason. These four rows bind each attribute/node to its own wire
  // field directly and prove the mislabelling is gone.
  it('same sentence on both fields: one visible node under approve-blocked-reason, no reject-blocked-reason, both controls describedby it', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S, can_reject: false, reject_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const approveBtn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement
    const rejectBtn = screen.getByTestId('detail-reject') as HTMLButtonElement

    expect(screen.getByTestId('approve-blocked-reason').textContent).toBe(S)
    expect(screen.queryAllByTestId('reject-blocked-reason')).toHaveLength(0)
    expect(approveBtn.getAttribute('aria-describedby')).toBe('approve-blocked-reason-text')
    expect(rejectBtn.getAttribute('aria-describedby')).toBe('approve-blocked-reason-text')
  })

  it('different sentences on both fields: two nodes, each under its own testid, each control describedby its own node', async () => {
    const reject = 'A different reason entirely.'
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S, can_reject: false, reject_blocked_reason: reject }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const approveBtn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement
    const rejectBtn = screen.getByTestId('detail-reject') as HTMLButtonElement

    expect(screen.getByTestId('approve-blocked-reason').textContent).toBe(S)
    expect(screen.getByTestId('reject-blocked-reason').textContent).toBe(reject)
    expect(approveBtn.getAttribute('aria-describedby')).toBe('approve-blocked-reason-text')
    expect(rejectBtn.getAttribute('aria-describedby')).toBe('reject-blocked-reason-text')
  })

  it('regression: a reject-only reason never mislabels as the approve reason, and Approve stays clean', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: true, approve_blocked_reason: null, can_reject: false, reject_blocked_reason: S }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const approveBtn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement

    expect(screen.getByTestId('reject-blocked-reason').textContent).toBe(S)
    expect(screen.queryAllByTestId('approve-blocked-reason')).toHaveLength(0)
    expect(approveBtn.disabled).toBe(false)
    expect(approveBtn.hasAttribute('title')).toBe(false)
    expect(approveBtn.hasAttribute('aria-describedby')).toBe(false)
  })

  it('approve blocked, reject clear: only approve-blocked-reason renders, Reject carries no title/aria-describedby', async () => {
    mockDetailFetch(detailRecord({ id: ID, can_approve: false, approve_blocked_reason: S, can_reject: true, reject_blocked_reason: null }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const rejectBtn = (await screen.findByTestId('detail-reject')) as HTMLButtonElement

    expect(screen.getByTestId('approve-blocked-reason').textContent).toBe(S)
    expect(screen.queryAllByTestId('reject-blocked-reason')).toHaveLength(0)
    expect(rejectBtn.hasAttribute('title')).toBe(false)
    expect(rejectBtn.hasAttribute('aria-describedby')).toBe(false)
  })
})

// RED specs (task-547, APPR-13-05, Mode A). Neither machine exists yet -- `detail-approve`
// only toggles its own disabled/reason presentation today (APPR-13-04); clicking it fires
// no handler, so every row below fails on a genuine missing element
// (`detail-approve-confirm-prompt`, `detail-reject-reason`, `detail-decision-error`) or a
// real assertion (the two AC-4 refetch-count rows), never an import/type/harness error.
// Does NOT implement handleApprove/handleReject/the approval.run() refetch wiring -- Stage
// 3 does; this file only pins the 18 Test Specs rows plus one extra (the reject row's
// flexWrap pin, matching T7-19's precedent in technique).
describe('InvoiceDetail Approve/Reject decision machines (task-547, APPR-13-05)', () => {
  const ID = 'inv-decide-1'
  const APPROVED_RUN: ApprovalRun = {
    run_id: 'run-1',
    state: 'approved',
    opened_at: '2026-08-01T00:00:00Z',
    closed_at: '2026-08-01T01:00:00Z',
    closed_by: APP_PERSONAS.firm.subject,
    steps: [],
    decisions: [],
  }

  // ---- AC-3: the approve arm->confirm machine ------------------------------------------

  it('AC-3: Approve arms rather than sends', async () => {
    const { decideCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))

    expect(await screen.findByTestId('detail-approve-confirm-prompt')).toBeTruthy()
    expect(screen.getByTestId('detail-approve-confirm')).toBeTruthy()
    expect(screen.getByTestId('detail-approve-cancel')).toBeTruthy()
    expect(decideCalls).toHaveLength(0)
  })

  it('AC-3: cancelling the arm sends nothing and restores the idle bar', async () => {
    const { decideCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    await screen.findByTestId('detail-approve-confirm-prompt')
    fireEvent.click(screen.getByTestId('detail-approve-cancel'))

    await waitFor(() => expect(screen.queryByTestId('detail-approve-confirm-prompt')).toBeNull())
    expect(screen.getByTestId('detail-approve')).toBeTruthy()
    expect(decideCalls).toHaveLength(0)
  })

  it('AC-3: confirming posts exactly one approve', async () => {
    const { decideCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    fireEvent.click(screen.getByTestId('detail-approve-confirm'))
    await screen.findByTestId('detail-approve') // settled back to idle after the refetch

    expect(decideCalls).toHaveLength(1)
    expect(decideCalls[0].body).toEqual({ decision: 'approved' })
  })

  it('AC-3: a double confirm fires one request', async () => {
    const { decideCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    const confirmBtn = screen.getByTestId('detail-approve-confirm')
    fireEvent.click(confirmBtn)
    // Proves the guards *together* send once. It does not isolate the in-flight ref:
    // fireEvent is act-wrapped, so the reducer identity check already blocks click two.
    // The ref covers the same-tick case jsdom cannot stage -- removing it keeps this green.
    fireEvent.click(confirmBtn)

    await screen.findByTestId('detail-approve')
    expect(decideCalls).toHaveLength(1)
  })

  // ---- AC-3: the inline reject row ------------------------------------------------------

  it('AC-3: Reject reveals an inline row, never a modal', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-reject'))

    expect(await screen.findByTestId('detail-reject-reason')).toBeTruthy()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('AC-3: a blank reason cannot be sent', async () => {
    const { decideCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-reject'))
    fireEvent.change(await screen.findByTestId('detail-reject-reason'), { target: { value: '   ' } })

    const confirmBtn = screen.getByTestId('detail-reject-confirm') as HTMLButtonElement
    expect(confirmBtn.disabled).toBe(true)
    fireEvent.click(confirmBtn)
    expect(decideCalls).toHaveLength(0)
  })

  it('AC-3: a filled reason posts it verbatim', async () => {
    const { decideCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-reject'))
    fireEvent.change(await screen.findByTestId('detail-reject-reason'), { target: { value: 'wrong buyer TIN' } })
    fireEvent.click(screen.getByTestId('detail-reject-confirm'))
    await screen.findByTestId('detail-reject') // settled back to idle after the refetch

    expect(decideCalls).toHaveLength(1)
    expect(decideCalls[0].body).toEqual({ decision: 'rejected', reason: 'wrong buyer TIN' })
  })

  it('AC-3: a double reject confirm fires one request', async () => {
    const { decideCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-reject'))
    fireEvent.change(await screen.findByTestId('detail-reject-reason'), { target: { value: 'wrong buyer TIN' } })
    const confirmBtn = screen.getByTestId('detail-reject-confirm')
    fireEvent.click(confirmBtn)
    // Same caveat as the approve row above: the `rejecting` disabled term, not the
    // in-flight ref, is what stops click two here.
    fireEvent.click(confirmBtn)

    await screen.findByTestId('detail-reject')
    expect(decideCalls).toHaveLength(1)
  })

  // Extra (not one of the 18 Test Specs rows): pins the reject row's width-fit declarations
  // the same way T7-19 (:2009-2018) pins resolve-outside's -- jsdom applies no CSS, so this
  // cannot prove the row stops overflowing on screen, only that the fix stays wired up.
  it('extra: the reject row is pinned to the resolve-outside wrap recipe', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-reject'))

    const input = (await screen.findByTestId('detail-reject-reason')) as HTMLInputElement
    const confirmBtn = screen.getByTestId('detail-reject-confirm') as HTMLButtonElement
    expect(input.style.flex).toBe('1 1 220px')
    expect(input.style.minWidth).toBe('160px')
    expect(input.style.height).toBe('32px')
    expect(input.style.fontSize).toBe('12.5px')
    expect(confirmBtn.style.flexShrink).toBe('0')
    expect(confirmBtn.style.whiteSpace).toBe('nowrap')
    expect(input.parentElement?.style.flexWrap).toBe('wrap')
  })

  // ---- AC-6: the non-optimistic refetch sequence ----------------------------------------

  it('AC-6: a successful approve refetches detail, history and the run', async () => {
    const afterApprove = detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true })
    const { fetchMock } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }), [], {
      detailSequence: [afterApprove],
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    fireEvent.click(screen.getByTestId('detail-approve-confirm'))
    await screen.findByTestId('detail-approve')

    const detailGets = fetchMock.mock.calls.filter(([url, init]) => (init?.method ?? 'GET') === 'GET' && String(url).endsWith(`/invoices/${ID}`))
    const historyGets = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/history'))
    const approvalGets = fetchMock.mock.calls.filter(([url, init]) => (init?.method ?? 'GET') === 'GET' && String(url).endsWith('/approval'))
    expect(detailGets.length).toBe(2)
    expect(historyGets.length).toBe(2)
    expect(approvalGets.length).toBe(2)
  })

  it('AC-6: no success banner is rendered', async () => {
    // A loaded log, not the default empty one: the activity card renders AuditRow's
    // 'Approved' label for this event, so the sweep below only means anything against it.
    // Neutral company name -- APP_PERSONAS.inhouse.org is swept for by two specs above.
    const approvedEvent: AuditEvent = {
      id: 'ev-approved-1',
      created_at: '2026-08-01T01:00:00Z',
      event: 'invoice.approval_approved',
      actor: APP_PERSONAS.firm.subject,
      actor_name: APP_PERSONAS.firm.name,
      actor_kind: 'person',
      entity_id: ID,
      company_name: 'Northgate Foods',
      company_scope: 'company',
      payload: { invoice_id: ID },
    }
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }), [], {
      approvalResponse: { ok: true, status: 200, json: () => Promise.resolve(APPROVED_RUN) },
      auditLog: auditLogOf([approvedEvent]),
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    fireEvent.click(screen.getByTestId('detail-approve-confirm'))
    await screen.findByTestId('detail-approve')

    // Scoped to the whole card: the state pill says "Approved" from the header, and that
    // is the record, not a banner.
    const card = screen.getByTestId('approval-card')
    const strip = screen.getByTestId('status-strip')
    // Carved out for the same reason as the approval card: every AuditRow for an
    // `invoice.approval_approved` event prints 'Approved', which is the log, not a banner.
    const activity = screen.getByTestId('invoice-activity')
    const outside = screen
      .queryAllByText(/approved|success|sent/i)
      .filter((el) => !card.contains(el) && !activity.contains(el))
    // The strip's node-3 label is the permanent word 'Approved' -- structure, same as the
    // pill. Pinning the count keeps the three carve-outs from swallowing a real banner.
    expect(outside.filter((el) => !strip.contains(el))).toHaveLength(0)
    expect(outside, "only the strip's node-3 label").toHaveLength(1)
  })

  // Latent guard: no one-line mutation of today's code can fail this row, because
  // useAsync exposes no setter and handleApprove never binds decideInvoice's return.
  // It bites the day someone adds one. Do not count it as proven coverage.
  it("AC-6: the POST's returned run is not installed", async () => {
    const openRun: ApprovalRun = { ...APPROVED_RUN, state: 'open', closed_at: null, closed_by: null }
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }), [], {
      approvalResponse: { ok: true, status: 200, json: () => Promise.resolve(openRun) },
      decideResponse: { ok: true, status: 200, json: () => Promise.resolve(APPROVED_RUN) },
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    fireEvent.click(screen.getByTestId('detail-approve-confirm'))
    await screen.findByTestId('detail-approve')

    expect(screen.getByTestId('approval-state').textContent).toBe(APPROVAL_CARD_COPY.stateOpen)
  })

  it("AC-6: a rejection's demotion surfaces through the refetch", async () => {
    const row1: StatusChange = { from_status: null, to_status: 'validated', actor: APP_PERSONAS.firm.subject, actor_name: APP_PERSONAS.firm.name, actor_kind: 'person', changed_at: '2026-08-01T00:00:00Z' }
    const row2: StatusChange = { from_status: 'validated', to_status: 'draft', actor: APP_PERSONAS.firm.subject, actor_name: APP_PERSONAS.firm.name, actor_kind: 'person', changed_at: '2026-08-02T00:00:00Z' }
    const afterReject = detailRecord({ id: ID, status: 'draft', can_edit: true, can_reject: false })
    const { fetchMock } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_reject: true }), [row1], {
      detailSequence: [afterReject],
      historySequence: [[row1, row2]],
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-reject'))
    fireEvent.change(await screen.findByTestId('detail-reject-reason'), { target: { value: 'wrong buyer TIN' } })
    fireEvent.click(screen.getByTestId('detail-reject-confirm'))

    await waitFor(() => expect(screen.getByTestId('invoice-status-badge').textContent).toContain('DRAFT'))
    // The second row (validated -> draft) is invisible on the strip -- node `draft` is
    // `current` and captions `Waiting`, node `validated` is `unreached`. So the refetch is
    // asserted at the fetch layer instead.
    const historyGets = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/history'))
    expect(historyGets, 'the demotion refetches history').toHaveLength(2)
    const draft = screen.getByTestId('status-strip').querySelector('[data-key="draft"]')
    expect(draft?.getAttribute('data-state')).toBe('current')
  })

  it('AC-6: the error leg unsticks the bar', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }), [], {
      decideResponse: { ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) },
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    fireEvent.click(screen.getByTestId('detail-approve-confirm'))

    expect((await screen.findByTestId('detail-decision-error')).textContent).toBe('boom')
    expect(screen.getByTestId('detail-approve')).toBeTruthy()
  })

  it('AC-6: a 403 renders the server\'s sentence, not ErrorState', async () => {
    const sentence = 'you do not hold the workflow role this step is waiting on'
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }), [], {
      decideResponse: { ok: false, status: 403, json: () => Promise.resolve({ error: sentence }) },
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    fireEvent.click(screen.getByTestId('detail-approve-confirm'))

    const errEl = await screen.findByTestId('detail-decision-error')
    expect(errEl.textContent).toBe(sentence)
    expect(screen.queryByRole('button', { name: 'Retry' })).toBeNull()
  })

  it('AC-6: a 401 still signs out', async () => {
    const onUnauthorized = vi.fn()
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }), [], {
      decideResponse: { ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) },
    })

    render(<InvoiceDetail ctx={detailCtx(ID, onUnauthorized)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    fireEvent.click(screen.getByTestId('detail-approve-confirm'))

    await waitFor(() => expect(onUnauthorized).toHaveBeenCalledTimes(1))
  })

  // ---- AC-4: handleSaved/handleRevalidate also refetch the run --------------------------

  it('AC-4: saving an edit refetches the run', async () => {
    const afterEdit = detailRecord({ id: ID, invoice_number: 'INV-EDIT-1-EDITED', status: 'validated', can_edit: true, buyer_name: 'Beta Ltd 2' })
    const { fetchMock } = mockDetailFetch(
      detailRecord({ id: ID, invoice_number: 'INV-EDIT-1', status: 'validated', can_edit: true, buyer_name: 'Beta Ltd' }),
      [],
      { detailSequence: [afterEdit], editResponse: { ok: true, status: 200, json: () => Promise.resolve(afterEdit) } },
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByText('INV-EDIT-1')
    fireEvent.click(screen.getByTestId('edit-toggle'))
    const buyerInput = await screen.findByDisplayValue('Beta Ltd')
    fireEvent.change(buyerInput, { target: { value: 'Beta Ltd 2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))

    await screen.findByText('INV-EDIT-1-EDITED')
    const approvalGets = fetchMock.mock.calls.filter(([url, init]) => (init?.method ?? 'GET') === 'GET' && String(url).endsWith('/approval'))
    expect(approvalGets.length).toBe(2)
  })

  it('AC-4: re-validating refetches the run', async () => {
    const afterRevalidate = detailRecord({
      id: ID,
      invoice_number: 'INV-REVAL-1-REVALIDATED',
      status: 'validated',
      can_edit: true,
      can_revalidate: false,
      revalidate_blocked_reason: 'Already validated.',
    })
    const { fetchMock } = mockDetailFetch(
      detailRecord({ id: ID, invoice_number: 'INV-REVAL-1', status: 'draft', can_edit: true, can_revalidate: true }),
      [],
      { detailSequence: [afterRevalidate], revalidateResponse: { ok: true, status: 200, json: () => Promise.resolve(afterRevalidate) } },
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('revalidate'))
    await screen.findByText('INV-REVAL-1-REVALIDATED')

    const approvalGets = fetchMock.mock.calls.filter(([url, init]) => (init?.method ?? 'GET') === 'GET' && String(url).endsWith('/approval'))
    expect(approvalGets.length).toBe(2)
  })

  it('AC-4: an edit that voids an approval shows the voided card, not a stale approved one', async () => {
    const cancelledRun: ApprovalRun = { ...APPROVED_RUN, state: 'cancelled' }
    const afterEdit = detailRecord({ id: ID, invoice_number: 'INV-VOID-1-EDITED', status: 'draft', can_edit: true, buyer_name: 'Beta Ltd 2' })
    mockDetailFetch(
      detailRecord({ id: ID, invoice_number: 'INV-VOID-1', status: 'validated', can_edit: true, buyer_name: 'Beta Ltd' }),
      [],
      {
        detailSequence: [afterEdit],
        editResponse: { ok: true, status: 200, json: () => Promise.resolve(afterEdit) },
        approvalResponse: { ok: true, status: 200, json: () => Promise.resolve(APPROVED_RUN) },
        approvalSequence: [cancelledRun],
      },
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByText('INV-VOID-1')
    expect(screen.getByTestId('approval-state').textContent).toBe(APPROVAL_CARD_COPY.stateApproved)

    fireEvent.click(screen.getByTestId('edit-toggle'))
    const buyerInput = await screen.findByDisplayValue('Beta Ltd')
    fireEvent.change(buyerInput, { target: { value: 'Beta Ltd 2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))

    await screen.findByText('INV-VOID-1-EDITED')
    expect(screen.getByTestId('approval-voided')).toBeTruthy()
    expect(screen.getByTestId('approval-state').textContent).not.toBe(APPROVAL_CARD_COPY.stateApproved)
  })

  // ---- QA adversarial (task-547 verification pass): gaps the 18 Test Specs rows left open --

  // Row 14 only proves the AXIS-2 sentence renders exactly; it never checks the bar
  // recovers. Row "the error leg unsticks the bar" only proves recovery with a generic 500.
  // Neither pairs the two, and neither exercises AXIS-1 (ErrNotPermitted).
  it('adversarial: a 403 (AXIS-1) unsticks the bar too, not just a generic 500', async () => {
    const sentence = 'only an approver can decide an approval step'
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }), [], {
      decideResponse: { ok: false, status: 403, json: () => Promise.resolve({ error: sentence }) },
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-approve'))
    fireEvent.click(screen.getByTestId('detail-approve-confirm'))

    expect((await screen.findByTestId('detail-decision-error')).textContent).toBe(sentence)
    expect((await screen.findByTestId('detail-approve') as unknown as HTMLButtonElement).disabled).toBe(false)
  })

  // No row asserts the row actually CLOSES and the reason actually CLEARS on success --
  // only that decideCalls fired and detail-reject reappears (which the button does even if
  // rejectOpen were stuck true, since it just checks the testid exists, not disabled).
  it('adversarial: a successful reject closes the row and clears the reason, not just settles the button', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-reject'))
    fireEvent.change(await screen.findByTestId('detail-reject-reason'), { target: { value: 'wrong buyer TIN' } })
    fireEvent.click(screen.getByTestId('detail-reject-confirm'))

    await waitFor(() => expect(screen.queryByTestId('detail-reject-reason')).toBeNull())
    const rejectBtn = (await screen.findByTestId('detail-reject')) as HTMLButtonElement
    expect(rejectBtn.disabled).toBe(false)

    // Re-opening must show a blank input -- proves rejectReason state itself reset, not
    // just that the row got hidden while still holding the stale text.
    fireEvent.click(rejectBtn)
    expect((await screen.findByTestId('detail-reject-reason') as unknown as HTMLInputElement).value).toBe('')
  })

  // D-24 (Stage-1 section 8): resolve-outside/undo can never touch a live run, so
  // approval.run() must NOT be added to either handler -- unlike handleSaved/handleRevalidate
  // above. Nothing in the resolve-outside describe block (T7-*) checks the approval GET
  // count, so this asymmetry currently has no test on either side of it.
  it('adversarial: resolve-outside does not refetch the run (D-24 asymmetry)', async () => {
    const RID = 'inv-failed-appr13-05a'
    const afterResolve = detailRecord({
      id: RID,
      status: 'failed',
      can_resolve_outside: true,
      kept_as_is_at: '2026-08-06T12:00:00Z',
      kept_as_is_by: APP_PERSONAS.firm.subject,
      kept_as_is_reason: 'Filed manually.',
    })
    const { fetchMock } = mockDetailFetch(detailRecord({ id: RID, status: 'failed', can_resolve_outside: true }), [], {
      detailSequence: [afterResolve],
    })

    render(<InvoiceDetail ctx={detailCtx(RID)} />)
    fireEvent.change(await screen.findByTestId('resolve-outside-reason'), { target: { value: 'Filed manually.' } })
    fireEvent.click(screen.getByTestId('resolve-outside'))

    await screen.findByTestId('detail-resolved-banner')
    const approvalGets = fetchMock.mock.calls.filter(([url, init]) => (init?.method ?? 'GET') === 'GET' && String(url).endsWith('/approval'))
    expect(approvalGets.length).toBe(1) // mount only
  })

  it('adversarial: undo-resolve-outside does not refetch the run either (D-24 asymmetry)', async () => {
    const RID = 'inv-failed-appr13-05b'
    const afterUndo = detailRecord({ id: RID, status: 'failed', can_resolve_outside: true })
    const { fetchMock } = mockDetailFetch(
      detailRecord({
        id: RID,
        status: 'failed',
        can_resolve_outside: true,
        kept_as_is_at: '2026-08-06T12:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Filed manually.',
      }),
      [],
      { detailSequence: [afterUndo] },
    )

    render(<InvoiceDetail ctx={detailCtx(RID)} />)
    fireEvent.click(await screen.findByTestId('resolve-outside-undo'))

    await waitFor(() => expect(screen.queryByTestId('detail-resolved-banner')).toBeNull())
    const approvalGets = fetchMock.mock.calls.filter(([url, init]) => (init?.method ?? 'GET') === 'GET' && String(url).endsWith('/approval'))
    expect(approvalGets.length).toBe(1) // mount only
  })

  // Neither machine's own spec rows exercise the other machine being open at the same
  // time -- Stage-1 section 10 confirmed the two are deliberately not mutually exclusive.
  it('adversarial: arming Approve does not disturb an already-open Reject row, and the reject row survives the approve cycle', async () => {
    const { decideCalls } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('detail-reject'))
    fireEvent.change(await screen.findByTestId('detail-reject-reason'), { target: { value: 'wrong buyer TIN' } })
    fireEvent.click(await screen.findByTestId('detail-approve'))

    expect((screen.getByTestId('detail-reject-reason') as HTMLInputElement).value).toBe('wrong buyer TIN')
    expect(screen.getByTestId('detail-approve-confirm-prompt')).toBeTruthy()

    fireEvent.click(screen.getByTestId('detail-approve-confirm'))
    await screen.findByTestId('detail-approve')

    expect(decideCalls).toHaveLength(1)
    expect(decideCalls[0].body).toEqual({ decision: 'approved' })
    expect((screen.getByTestId('detail-reject-reason') as HTMLInputElement).value).toBe('wrong buyer TIN')
  })
})

describe('InvoiceDetail demo-only blocked-by-role note (task-594, DEMO-06-06)', () => {
  const ID = 'inv-blocked-by-role-1'
  const FOLAKE: Member = {
    id: 'm-folake-001',
    name: 'Folake Adesina',
    initials: 'FA',
    email: 'folake@example.ng',
    role: 'preparer',
    status: 'active',
    isYou: true,
  }
  const MUSA: Member = {
    id: 'm-musa-001',
    name: 'Musa Danjuma',
    initials: 'MD',
    email: 'musa@example.ng',
    role: 'reviewer',
    status: 'active',
    isYou: true,
  }
  const S = 'Only an admin or a reviewer can approve or reject an invoice — ask an approver on your team.'

  // detailCtx (:97-108) does not set `members` -- flag-on rows spread the roster in
  // rather than rely on the helper (hazard C4).
  async function renderDemoDetail(members: Member[]) {
    vi.stubEnv('VITE_DEMO_MODE', 'true')
    vi.resetModules()
    const { InvoiceDetail: DemoInvoiceDetail } = await import('./InvoiceDetail')
    const ctx = { ...detailCtx(ID), members } as unknown as PlatformCtx
    render(<DemoInvoiceDetail ctx={ctx} />)
  }

  it('1: flag off, a blocked preparer sees no demo note (AC-1)', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_approve: false }))
    const ctx = { ...detailCtx(ID), members: [FOLAKE] } as unknown as PlatformCtx

    render(<InvoiceDetail ctx={ctx} />)

    await screen.findByTestId('detail-approve')
    expect(screen.queryByTestId('persona-blocked-note')).toBeNull()
  })

  it('4: the note is a sibling of the disabled Approve, never its replacement (AC-2, AC-4)', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_approve: false, approve_blocked_reason: S }))

    await renderDemoDetail([FOLAKE])

    const approveBtn = (await screen.findByTestId('detail-approve')) as HTMLButtonElement
    expect(approveBtn.disabled).toBe(true)
    expect(screen.getByTestId('approve-blocked-reason').textContent).toBe(S)
    expect(screen.getByTestId('persona-blocked-note')).toBeTruthy()
  })

  it('5: an approver sees no note (AC-3)', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_approve: true }))

    await renderDemoDetail([MUSA])

    await screen.findByTestId('detail-approve')
    expect(screen.queryByTestId('persona-blocked-note')).toBeNull()
  })

  it('6: a blocked reviewer sees no note -- the block is not about their role (AC-3)', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_approve: false }))

    await renderDemoDetail([MUSA])

    await screen.findByTestId('detail-approve')
    expect(screen.queryByTestId('persona-blocked-note')).toBeNull()
  })

  it('7: a preparer on a non-validated invoice sees no note (AC-3)', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'accepted', can_approve: false }))

    await renderDemoDetail([FOLAKE])

    await screen.findByTestId('detail-approve')
    expect(screen.queryByTestId('persona-blocked-note')).toBeNull()
  })

  it('8: an unresolved roster renders no note and does not throw (AC-3)', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_approve: false }))

    await renderDemoDetail([])

    await screen.findByTestId('detail-approve')
    expect(screen.queryByTestId('persona-blocked-note')).toBeNull()
  })
})

// AUDIT-02-04 Stage-4. The no-source canvas is actorLabel's sixth reader and the only one
// that puts the actor mid-prose ("... was typed into ASComply by X on ..."), so it must
// name a PERSON or say nobody. It reads the genesis history row, which every seeded
// invoice actors 'system' (db/seed.dev.sql:628). Proves the whole chain: InvoiceDetail
// -> SourceDocumentModal -> NoSourceCanvas passes the server's resolved pair.
describe('InvoiceDetail no-source canvas: only a person is named ([actor-label-shared])', () => {
  const NO_DOCUMENT: MockResponse = {
    ok: true,
    status: 200,
    json: () => Promise.resolve({ document: null, source_rows: [] }),
  }

  async function openNoSourceCanvas(history: StatusChange[]): Promise<HTMLElement> {
    mockDetailFetch(detailRecord(), history, { sourceDocumentResponse: NO_DOCUMENT })
    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)
    fireEvent.click(await screen.findByTestId('why-no-source-document'))
    return screen.findByTestId('source-document-no-source')
  }

  it('a system genesis actor omits the "by" clause instead of claiming System typed it in', async () => {
    const canvas = await openNoSourceCanvas([
      { from_status: null, to_status: 'draft', changed_at: '2026-07-01T00:00:00Z', actor: 'system', actor_name: 'System', actor_kind: 'system' },
    ])

    expect(canvas.textContent).not.toContain('by System')
    expect(canvas.textContent).toContain('into ASComply on')
  })

  it("a person genesis actor is named from the server's pair, never from APP_PERSONAS", async () => {
    const canvas = await openNoSourceCanvas([
      { from_status: null, to_status: 'draft', changed_at: '2026-07-01T00:00:00Z', actor: APP_PERSONAS.inhouse.subject, actor_name: 'Adaeze Nwosu', actor_kind: 'person' },
    ])

    expect(canvas.textContent).toContain('by Adaeze Nwosu')
    expect(canvas.textContent).not.toContain(APP_PERSONAS.inhouse.name)
    expect(canvas.textContent).not.toContain(APP_PERSONAS.inhouse.org)
  })
})

// AUDIT-09-04 QA. The activity card's own suite proves what the card renders; nothing
// proved it is ON this page. Deleting the <InvoiceActivityCard/> line, moving it into the
// rail and putting it above the record card each left the whole 3146-test app suite green.
// Geometry spec C in e2e/topology/invoice-surfaces.spec.ts is the layout oracle; these are
// the mount oracle, and they run without a browser.
describe('InvoiceDetail mounts the activity card in the main column (AUDIT-09-04 AC-1)', () => {
  const ID = 'inv-activity-mount-1'
  const QA_RUN: ApprovalRun = {
    run_id: 'run-qa-1',
    state: 'approved',
    opened_at: '2026-08-01T00:00:00Z',
    closed_at: '2026-08-01T01:00:00Z',
    closed_by: APP_PERSONAS.firm.subject,
    steps: [],
    decisions: [],
  }

  it('invoiceDetail_complianceCardIsTheMainColumnsSecondChild', async () => {
    mockDetailFetch(
      detailRecord({
        id: ID,
        status: 'accepted',
        rule_set_version: 3,
        violations: [{ rule_key: 'vat_mismatch', severity: 'error', message: 'VAT does not match.' }],
      }),
    )
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const column = await screen.findByTestId('invoice-main-column')
    const rail = screen.getByTestId('invoice-rail')
    const activity = screen.getByTestId('invoice-activity')

    expect(column.contains(activity), 'the activity card must live in the main column').toBe(true)
    // Positive control for the absence: the rail renders and owns a card of its own, so a
    // rail that never mounted cannot be what makes the line below pass.
    expect(rail.contains(screen.getByTestId('source-document-card'))).toBe(true)
    expect(rail.contains(activity), 'the activity card must not be in the right rail').toBe(false)

    const compliance = screen.queryByTestId('compliance-card')
    expect(
      compliance,
      'no element carries data-testid="compliance-card" -- the Compliance card <div> in InvoiceDetail.tsx has no testid of its own',
    ).not.toBeNull()

    // Order, not geometry: record card, Compliance, activity.
    const children = Array.from(column.children)
    expect(children, 'the main column must hold exactly three cards').toHaveLength(3)
    expect(children[0]!.contains(screen.getByTestId('buyer-tin')), 'child 1 must be the record card').toBe(true)
    expect(children[1], 'child 2 must be the Compliance card').toBe(compliance)
    expect(children[2], 'child 3 must be the activity card').toBe(activity)
  })

  it('invoiceDetail_mainColumnKeepsItsGridMinimumAtZero', async () => {
    mockDetailFetch(detailRecord({ id: ID }))
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    // A declaration assertion, deliberately: `1fr` is minmax(auto,1fr) and a grid item with
    // visible overflow takes a content-based automatic minimum, which the card's 868px
    // table would raise until the column pushed the rail off the page. jsdom applies no
    // layout, so geometry spec A1 (.pf-scroll must not scroll sideways) is the real oracle
    // -- this only stops the declaration being deleted between browser runs.
    const column = await screen.findByTestId('invoice-main-column')
    expect((column as HTMLElement).style.minWidth, 'minWidth:0 on the grid item').toMatch(/^0(px)?$/)
  })

  it('invoiceDetail_activityCardUnmountsAndRefetchesOnAMutationRoundTrip', async () => {
    // CHARACTERISATION, reported not fixed. AC-2 reads "fetches once per invoiceId" and the
    // card's own contract holds it; the PAGE does not. handleSaved / handleRevalidate /
    // approve / reject / submit / resolve-outside all run `setLive(null); detail.run()`,
    // and detail.run() dispatches useAsync's 'start', which nulls detail.data and drops the
    // ladder to <Loading label="Loading invoice..."/> -- unmounting the whole
    // .pf-detail-grid, activity card included. The card then remounts, refetches
    // /audit-log, and the user's chip, expansion and Show-all reset with no warning.
    // Arch section 6 checked only that nothing nulls log.data; the ancestor is what
    // unmounts the card.
    //
    // The detail GET is deferred on purpose. With an instantly-resolving mock React's
    // scheduler (a MessageChannel macrotask) never commits the loading render, so this
    // whole path is invisible in jsdom -- which is why no shipped spec sees it.
    const { fetchMock } = mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_approve: true }), [], {
      approvalResponse: { ok: true, status: 200, json: () => Promise.resolve(QA_RUN) },
    })
    const isDetailGet = (url: string, init?: RequestInit) =>
      (init?.method ?? 'GET') === 'GET' &&
      !url.endsWith('/history') &&
      !url.endsWith('/source-document') &&
      !url.endsWith('/approval') &&
      !url.includes('/audit-log')
    const auditGets = () =>
      fetchMock.mock.calls.filter(([url, init]: [string, RequestInit?]) => (init?.method ?? 'GET') === 'GET' && String(url).includes('/audit-log'))

    let defer = false
    vi.stubGlobal('fetch', (url: string, init?: RequestInit) => {
      const res = fetchMock(url, init)
      if (defer && isDetailGet(String(url), init)) {
        return new Promise((resolve) => setTimeout(() => resolve(res as unknown as Response), 60))
      }
      return res
    })

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-activity')
    await waitFor(() => expect(auditGets()).toHaveLength(1))

    defer = true
    fireEvent.click(screen.getByTestId('detail-approve'))
    fireEvent.click(screen.getByTestId('detail-approve-confirm'))

    // The grid is gone while the detail refetch is in flight.
    await waitFor(() => expect(screen.queryByTestId('invoice-activity'), 'the card survived the loading rung').toBeNull())
    // ...and it comes back having asked the server for the log a second time.
    await screen.findByTestId('invoice-activity')
    await waitFor(() => expect(auditGets().length, 'the remounted card refetched /audit-log').toBeGreaterThanOrEqual(2))
  })
})

// BUG-13-02. The Compliance card moves out of the rail and into the main column, and gains
// `data-testid="compliance-card"` so it can be located in either body state (violations-table
// or not-validated). Containment and child order only -- invoice-main-column unconditionally
// precedes invoice-rail, so no compareDocumentPosition across the two proves anything here.
describe('InvoiceDetail moves the Compliance card into the main column (BUG-13-02)', () => {
  const ID = 'inv-compliance-move-1'

  // Every spec below locates the card through this, so the red reads as the missing testid
  // rather than as `expected null`.
  function complianceCard(): HTMLElement {
    const el = screen.queryByTestId('compliance-card')
    expect(
      el,
      'no element carries data-testid="compliance-card" -- the Compliance card <div> in InvoiceDetail.tsx has no testid of its own',
    ).not.toBeNull()
    return el as HTMLElement
  }

  it('invoiceDetail_complianceCardIsNotInTheRail', async () => {
    mockDetailFetch(
      detailRecord({
        id: ID,
        status: 'accepted',
        rule_set_version: 3,
        violations: [{ rule_key: 'vat_mismatch', severity: 'error', message: 'VAT does not match.' }],
      }),
    )
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const rail = await screen.findByTestId('invoice-rail')
    const column = screen.getByTestId('invoice-main-column')
    // Positive control for the absence: the rail mounted and owns a card of its own, so an
    // unmounted rail cannot be what makes the line below pass.
    expect(rail.contains(screen.getByTestId('source-document-card')), 'the rail must have rendered').toBe(true)

    const card = complianceCard()
    expect(rail.contains(card), 'the Compliance card must not be in the right rail').toBe(false)
    expect(column.contains(card), 'the Compliance card must live in the main column').toBe(true)
  })

  it('invoiceDetail_complianceCardHasItsOwnTestidInBothBodyStates', async () => {
    mockDetailFetch(
      detailRecord({
        id: ID,
        rule_set_version: 3,
        violations: [{ rule_key: 'vat_mismatch', severity: 'error', message: 'VAT does not match.' }],
      }),
    )
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    await screen.findByTestId('violations-table')
    const validated = complianceCard()
    expect(within(validated).getByTestId('violations-table'), 'the validated body belongs to the card').toBeTruthy()
    expect(within(validated).queryByTestId('not-validated')).toBeNull()

    cleanup()

    // The other body: rule_set_version null renders not-validated, and the card must still
    // resolve -- a testid on the body rather than the card would only survive one of these.
    mockDetailFetch(detailRecord({ id: ID, rule_set_version: null }))
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    await screen.findByTestId('not-validated')
    const unvalidated = complianceCard()
    expect(within(unvalidated).getByTestId('not-validated'), 'the unvalidated body belongs to the card').toBeTruthy()
    expect(within(unvalidated).queryByTestId('violations-table')).toBeNull()
  })

  // A kept invoice is DB-constrained to status='draft' (invoices_kept_as_is_draft_only),
  // which is also the demotedSinceValidation shape that renders stale-verdict -- so one
  // fixture reaches both banners.
  it('invoiceDetail_keptAndStaleBannersStayInsideTheMovedCard', async () => {
    mockDetailFetch(
      detailRecord({
        id: ID,
        status: 'draft',
        rule_set_version: 3,
        rule_set_version_id: 'rsv-1',
        violations: [{ rule_key: 'vat_mismatch', severity: 'warning', message: 'VAT looks unusual.' }],
        kept_as_is_at: '2026-07-31T00:00:00Z',
        kept_as_is_by: APP_PERSONAS.firm.subject,
        kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
      }),
    )
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const banner = await screen.findByTestId('detail-kept-banner')
    const stale = screen.getByTestId('stale-verdict')
    const table = screen.getByTestId('violations-table')

    const card = complianceCard()
    expect(card.contains(banner), 'the kept-as-is banner must stay inside the Compliance card').toBe(true)
    expect(card.contains(stale), 'the stale-verdict banner must stay inside the Compliance card').toBe(true)
    expect(card.contains(table), 'violations-table must stay inside the Compliance card').toBe(true)
    // Card-internal, so it travels with the block and stays falsifiable after the move.
    expect(banner.compareDocumentPosition(table) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('invoiceDetail_cleanPassBlockStaysInsideTheMovedCard', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'accepted', rule_set_version: 4, violations: [] }))
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const table = await screen.findByTestId('violations-table')
    const card = complianceCard()
    expect(card.contains(table), 'the clean-pass block must stay inside the Compliance card').toBe(true)
    expect(
      within(table).getByText('Passes all rules — no violations. Evaluated against rule-set v4.'),
      'the clean-pass sentence must read off the same rule-set version',
    ).toBeTruthy()
  })

  // QA adversarial. Every other position spec (invoiceDetail_complianceCardIsTheMainColumnsSecondChild)
  // fixtures a validated invoice, so the position claim was never checked against the OTHER
  // body state -- a testid moved onto the not-validated wrapper instead of the card itself
  // would pass that spec and still fail here.
  it('invoiceDetail_notValidatedInvoiceCompliancePositionUnchanged', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', rule_set_version: null }))
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const column = await screen.findByTestId('invoice-main-column')
    await screen.findByTestId('not-validated')
    const card = complianceCard()
    expect(within(card).getByTestId('not-validated'), 'the not-validated body belongs to the card').toBeTruthy()

    const children = Array.from(column.children)
    expect(children, 'the main column must hold exactly three cards').toHaveLength(3)
    expect(children[1], 'child 2 must be the Compliance card, in either body state').toBe(card)
  })

  // QA adversarial. A draft with no IRN and no rejection reasons suppresses fiscal-record-card
  // AND both rejection-card arms, leaving the rail down to its two unconditional members
  // (Approval state, Source document) -- the emptiest the rail gets. The card's departure
  // must not strand the rail's remaining cards or break their own rendering.
  it('invoiceDetail_railStaysSaneWhenNearlyEmptyAfterTheCardLeaves', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', rule_set_version: 3, violations: [] }))
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const rail = await screen.findByTestId('invoice-rail')
    // Positive control: the rail's two unconditional members both mounted.
    expect(within(rail).getByTestId('approval-card'), 'approval-card must still mount').toBeTruthy()
    expect(within(rail).getByTestId('source-document-card'), 'source-document-card must still mount').toBeTruthy()

    expect(within(rail).queryByTestId('fiscal-record-card'), 'no IRN, no fiscal record').toBeNull()
    expect(within(rail).queryByTestId('failed-dead-end'), 'not a failed invoice').toBeNull()
    expect(within(rail).queryByTestId('rejection-reasons'), 'no rejection reasons on this fixture').toBeNull()
    expect(within(rail).queryByTestId('compliance-card'), 'the Compliance card must never re-enter the rail').toBeNull()

    const column = screen.getByTestId('invoice-main-column')
    const children = Array.from(column.children)
    expect(children, 'the main column must still hold exactly three cards').toHaveLength(3)
    expect(children[1]).toBe(complianceCard())
  })
})

// AUDIT-09-05 QA. The card's own suite proves it calls ctx.openAuditForInvoice with the two
// props it was handed; the fifth edit that supplies the SECOND prop was pinned only by a
// source scan for the literal `invoiceNumber={inv.invoice_number}`. This is the rendered
// oracle for the same wire: the number the hand-off carries is the one the server sent.
describe('InvoiceDetail "Open in Audit →" wiring (AUDIT-09-05)', () => {
  const ID = 'inv-handoff-1'
  const NUMBER = 'INV-HANDOFF-77'

  const handoffEvent: AuditEvent = {
    id: 'ev-handoff-1',
    created_at: '2026-08-01T01:00:00Z',
    event: 'invoice.created',
    actor: APP_PERSONAS.firm.subject,
    actor_name: APP_PERSONAS.firm.name,
    actor_kind: 'person',
    entity_id: 'ent-1',
    company_name: 'Northgate Foods',
    company_scope: 'company',
    payload: { invoice_id: ID },
  }

  it('invoiceDetail_openInAuditCarriesTheRecordsOwnNumber', async () => {
    // The number is deliberately NOT the record factory's default: a hardcoded literal or a
    // number read off the audit payload would pass against the default and fail here.
    mockDetailFetch(detailRecord({ id: ID, invoice_number: NUMBER }), [], {
      auditLog: auditLogOf([handoffEvent]),
    })
    const openAuditForInvoice = vi.fn()
    const ctx = { ...detailCtx(ID), openAuditForInvoice } as unknown as PlatformCtx

    render(<InvoiceDetail ctx={ctx} />)
    const btn = await screen.findByTestId('activity-open-in-audit')
    expect(openAuditForInvoice, 'nothing may hand off before the click').not.toHaveBeenCalled()

    fireEvent.click(btn)
    expect(openAuditForInvoice).toHaveBeenCalledTimes(1)
    expect(openAuditForInvoice).toHaveBeenCalledWith(ID, NUMBER)
  })
})

// AUDIT-09-09 (task-680, AC-5). The whole-surface guard.
//
// An EXPLICIT inventory of every data-testid the invoice detail page renders, asserted as
// a set. Never a snapshot: `vitest -u` folds a deletion into the stored file and the suite
// stays green, which is the one failure this guard exists to stop.
//
// THE BOUNDARY, drawn mechanically rather than from memory:
//   git show main:...InvoiceDetail.tsx | grep -o 'data-testid="[^"]*"' | sort -u   -> 57
//   the same on this branch                                                        -> 56
//   the whole diff:  -status-history -status-history-row -status-history-actor
//                    +invoice-main-column +invoice-rail
// Plus two component swaps in the rail/column: ApprovalTrailCard -> ApprovalStateCard, and
// InvoiceActivityCard added. So AUDIT-09 owns status-strip/strip-*, approval-card/approval-*,
// invoice-activity*, and the two new wrappers; everything else is UNTOUCHED. SourceDocumentCard
// and SourceDocumentStates do appear in the branch diff, but only as copy edits (F-H's two
// "state strip" strings) -- no testid moved, so they count as untouched.
//
// The reworked cards' own inventories are exhaustive in StatusStrip.test.tsx,
// ApprovalStateCard.test.tsx and InvoiceActivityCard.test.tsx; what is declared here is only
// what THIS page mounts them into.
describe('InvoiceDetail: the untouched surface survives the AUDIT-09 rework (AUDIT-09-09 AC-5)', () => {
  const ID = 'inv-surface-1'

  // The survivors of InvoiceDetail.tsx's own set, plus SourceDocumentCard's 5. ViolationsTable
  // contributed one (`violations-scroll`) until BUG-13-01 retired it; it now renders none.
  const UNTOUCHED_TESTIDS = [
    'approve-blocked-reason',
    'buyer-tin',
    'computed-line-sum',
    'detail-approve',
    'detail-approve-cancel',
    'detail-approve-confirm',
    'detail-approve-confirm-prompt',
    'detail-decision-actions',
    'detail-decision-error',
    'detail-kept-banner',
    'detail-reject',
    'detail-reject-cancel',
    'detail-reject-confirm',
    'detail-reject-reason',
    'detail-resolved-banner',
    'detail-submit',
    'detail-submit-cancel',
    'detail-submit-confirm',
    'detail-submit-confirm-prompt',
    'detail-submit-error',
    'detail-submit-skipped',
    'edit-cancel',
    'edit-invoice',
    'edit-toggle',
    'failed-dead-end',
    'failure-detail',
    'failure-headline',
    'failure-next-step',
    'field-flag',
    'fiscal-csid',
    'fiscal-irn',
    'fiscal-qr',
    'fiscal-record-card',
    'invoice-actions',
    'invoice-detail',
    'invoice-status-badge',
    'line-add',
    'line-remove',
    'line-row',
    'not-validated',
    'reject-blocked-reason',
    'rejection-reason-row',
    'rejection-reasons',
    'resolve-outside',
    'resolve-outside-blocked-reason',
    'resolve-outside-reason',
    'resolve-outside-undo',
    'revalidate',
    'revalidate-blocked-reason',
    'stale-verdict',
    'submit-blocked-reason',
    'view-ubl',
    'view-ubl-blocked-reason',
    'violations-table',
    // SourceDocumentCard.tsx
    'open-extraction-review',
    'source-document-card',
    'source-document-card-meta',
    'source-document-range',
    'view-source-document',
    'why-no-source-document',
  ]

  // What AUDIT-09 put on this page. Listed so the closed-world check below can tell a
  // deliberate addition from a stray one.
  const AUDIT_09_TESTIDS = [
    'invoice-main-column',
    'invoice-rail',
    'status-strip',
    'strip-node',
    'strip-actor',
    'approval-card',
    'approval-empty',
    'invoice-activity',
    'invoice-activity-body',
    'invoice-activity-empty',
  ]

  // What BUG-13 puts on this page. The chip ships in BUG-13-01 and lands in `seen` on
  // every scenario that mounts a rule_set_version, so it must be declared here now.
  const BUG_13_TESTIDS = ['compliance-ruleset-version', 'compliance-card']

  // Deleted by AUDIT-09-02, AUDIT-09-06 and BUG-13-01. A resurrection is as much a surface
  // change as a deletion, and `git grep` cannot see one that arrives under a new component.
  const RETIRED_TESTIDS = [
    'status-history',
    'status-history-row',
    'status-history-actor',
    'approval-trail-card',
    'approval-trail',
    'approval-trail-step',
    'approval-trail-state',
    'approval-trail-decision',
    'approval-trail-empty',
    'approval-trail-voided',
    'approval-trail-notify-note',
    // BUG-13-01: retired with the scroll recipe the table no longer needs.
    'violations-scroll',
  ]

  const A_DOCUMENT: MockResponse = {
    ok: true,
    status: 200,
    json: () =>
      Promise.resolve({
        document: { id: 'doc-1', filename: 'june.csv', declared_content_type: 'text/csv', size_bytes: 4096, content_hash: 'a'.repeat(64) },
        source_rows: [7],
      }),
  }

  const LINE = { id: 'li-1', line_no: 1, description: 'Widget', quantity: '2', unit_price: '500.00', line_total: '1000.00', line_tax: null }

  // Nine mounts, not one: whole groups of this page are mutually exclusive (failed vs
  // accepted, read vs edit, resolved vs unresolved, a skip vs an error on the same submit),
  // so no single fixture can render the surface. Each one names the ids it is here for.
  const SCENARIOS: { name: string; mount: () => Promise<void> }[] = [
    {
      // failed-dead-end + resolve-outside's unresolved arm + a stale rejection card + every
      // blocked-reason line at once.
      name: 'failed, unvalidated, blocked everywhere',
      mount: async () => {
        mockDetailFetch(
          detailRecord({
            id: ID,
            status: 'failed',
            failure_kind: 'app_rejected',
            rule_set_version: null,
            rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
            can_view_ubl: false,
            ubl_blocked_reason: 'This invoice has no document yet.',
            approve_blocked_reason: 'Only an approver can approve this invoice.',
            reject_blocked_reason: 'A rejection needs the workflow role this step waits on.',
            can_resolve_outside: false,
            resolve_outside_blocked_reason: 'Only an admin can mark this resolved.',
          }),
        )
        render(<InvoiceDetail ctx={detailCtx(ID)} />)
        await screen.findByTestId('failed-dead-end')
        await screen.findByTestId('invoice-activity')
      },
    },
    {
      // resolve-outside's OTHER arm: the banner and Undo replace the input and the button.
      name: 'failed and already resolved outside',
      mount: async () => {
        mockDetailFetch(
          detailRecord({
            id: ID,
            status: 'failed',
            kept_as_is_at: '2026-07-02T09:00:00Z',
            kept_as_is_by: APP_PERSONAS.firm.subject,
            kept_as_is_reason: 'Settled directly with the buyer',
            can_resolve_outside: true,
          }),
        )
        render(<InvoiceDetail ctx={detailCtx(ID)} />)
        await screen.findByTestId('detail-resolved-banner')
      },
    },
    {
      // The fiscal record (accepted + irn only), a violations table with rows, and the
      // source-document card's document arm.
      name: 'accepted, fiscal record, violations, a real source document',
      mount: async () => {
        mockDetailFetch(
          detailRecord({
            id: ID,
            status: 'accepted',
            irn: 'IRN-2026-0001',
            csid: 'CSID-2026-0001',
            qr_png_base64: 'iVBORw0KGgo=',
            rule_set_version: 3,
            rule_set_version_id: 'rsv-3',
            violations: [{ rule_key: 'NGE-1001', severity: 'warning', message: 'Rounding differs by 0.01', path: 'total' }],
          }),
          [],
          { sourceDocumentResponse: A_DOCUMENT },
        )
        render(<InvoiceDetail ctx={detailCtx(ID)} />)
        await screen.findByTestId('fiscal-record-card')
        await screen.findByTestId('view-source-document')
      },
    },
    {
      // The actions bar, both its blocked reasons, the kept-as-is banner and the stale
      // verdict -- a draft demoted after a clean validation is what makes the verdict stale.
      name: 'draft, editable, kept as-is, verdict stale',
      mount: async () => {
        mockDetailFetch(
          detailRecord({
            id: ID,
            status: 'draft',
            rule_set_version: 3,
            rule_set_version_id: 'rsv-3',
            violations: [],
            kept_as_is_at: '2026-07-02T09:00:00Z',
            kept_as_is_by: APP_PERSONAS.firm.subject,
            kept_as_is_reason: 'Supplier confirmed the figures',
            can_edit: true,
            can_revalidate: false,
            revalidate_blocked_reason: 'Re-validate applies to drafts that have changed.',
            can_submit: false,
            submit_blocked_reason: 'This invoice is not validated.',
          }),
        )
        render(<InvoiceDetail ctx={detailCtx(ID)} />)
        await screen.findByTestId('invoice-actions')
        await screen.findByTestId('detail-kept-banner')
      },
    },
    {
      // Edit mode owns the left card outright: the read-mode block, the actions bar and the
      // decision pair all leave. `buyer.tin` is a mapped MBS path, so the reason raises a flag.
      name: 'draft in edit mode',
      mount: async () => {
        mockDetailFetch(
          detailRecord({
            id: ID,
            status: 'draft',
            can_edit: true,
            line_items: [LINE],
            rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation', path: 'buyer.tin' }],
          }),
        )
        render(<InvoiceDetail ctx={detailCtx(ID)} />)
        fireEvent.click(await screen.findByTestId('edit-toggle'))
        await screen.findByTestId('edit-invoice')
      },
    },
    {
      // All three inline arm->confirm machines open at once. They are independent by design
      // (the approve pair swaps in place, reject and submit open their own rows), so one
      // mount reaches all nine armed ids.
      name: 'validated, all three decisions armed',
      mount: async () => {
        mockDetailFetch(
          detailRecord({ id: ID, status: 'validated', rule_set_version: 3, can_edit: true, can_submit: true, can_approve: true, can_reject: true }),
        )
        render(<InvoiceDetail ctx={detailCtx(ID)} />)
        fireEvent.click(await screen.findByTestId('detail-approve'))
        fireEvent.click(screen.getByTestId('detail-reject'))
        fireEvent.click(screen.getByTestId('detail-submit'))
        await screen.findByTestId('detail-approve-confirm')
        await screen.findByTestId('detail-reject-confirm')
        await screen.findByTestId('detail-submit-confirm')
      },
    },
    {
      // [never-report-success-on-a-skip]: the skip banner, which is a different element from
      // the error banner below and never renders beside it.
      name: 'a submit the server skipped',
      mount: async () => {
        mockDetailFetch(detailRecord({ id: ID, status: 'validated', rule_set_version: 3, can_edit: true, can_submit: true }), [], {
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
      },
    },
    {
      name: 'a submit that failed',
      mount: async () => {
        mockDetailFetch(detailRecord({ id: ID, status: 'validated', rule_set_version: 3, can_edit: true, can_submit: true }), [], {
          submitResponses: [{ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) }],
        })
        render(<InvoiceDetail ctx={detailCtx(ID)} />)
        fireEvent.click(await screen.findByTestId('detail-submit'))
        fireEvent.click(screen.getByTestId('detail-submit-confirm'))
        await screen.findByTestId('detail-submit-error')
      },
    },
    {
      name: 'an approval decision the server refused',
      mount: async () => {
        mockDetailFetch(detailRecord({ id: ID, status: 'validated', rule_set_version: 3, can_edit: true, can_approve: true }), [], {
          decideResponse: { ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) },
        })
        render(<InvoiceDetail ctx={detailCtx(ID)} />)
        fireEvent.click(await screen.findByTestId('detail-approve'))
        fireEvent.click(screen.getByTestId('detail-approve-confirm'))
        await screen.findByTestId('detail-decision-error')
      },
    },
  ]

  // Also carries BUG-13-01's `invoiceDetail_retiredScrollTestidIsDeclaredRetired`: the
  // spec is two list edits above, not a second sweep of the same SCENARIOS.
  it('invoiceDetail_untouchedTestidsAreIntact', async () => {
    const seen = new Map<string, string>()
    for (const scenario of SCENARIOS) {
      cleanup()
      await scenario.mount()
      for (const el of Array.from(document.querySelectorAll('[data-testid]'))) {
        const id = el.getAttribute('data-testid')!
        if (!seen.has(id)) seen.set(id, scenario.name)
      }
    }

    // Fails on: any collateral deletion of an untouched testid -- the rename of a card's
    // wrapper, a conditional narrowed until a branch stops rendering, a whole card dropped
    // while its own spec still passes against a sibling.
    const missing = UNTOUCHED_TESTIDS.filter((id) => !seen.has(id))
    expect(
      missing,
      `AUDIT-09 left the invoice detail page without ${missing.length} untouched testid(s): ${missing.join(', ')}. Either the element was deleted, or a fixture above stopped reaching it.`,
    ).toEqual([])

    // The positive control for the two absence checks below: this run rendered the real page,
    // so an empty document cannot be what makes them pass.
    expect(seen.has('status-strip') && seen.has('approval-card'), 'the replacements must have rendered').toBe(true)

    const resurrected = RETIRED_TESTIDS.filter((id) => seen.has(id))
    expect(resurrected, `retired testid(s) back on the page: ${resurrected.join(', ')}`).toEqual([])

    // Closed-world, and deliberately so: "no card gained a testid" is half of AC-5. A new
    // element on this page must be declared, in one of the three lists above, by whoever adds it.
    const declared = new Set([...UNTOUCHED_TESTIDS, ...AUDIT_09_TESTIDS, ...BUG_13_TESTIDS])
    const undeclared = [...seen.keys()].filter((id) => !declared.has(id)).sort()
    expect(
      undeclared,
      `undeclared testid(s) on the invoice detail page: ${undeclared.map((id) => `${id} (${seen.get(id)})`).join(', ')}. Add each to UNTOUCHED_TESTIDS, AUDIT_09_TESTIDS or BUG_13_TESTIDS.`,
    ).toEqual([])
  })

  // The jsdom twin of invoice-surfaces.spec.ts's `detail surface: the untouched rail order
  // is unchanged`. Same read, same two lists, no browser -- so a permutation reds here in
  // one second instead of only on the deploy gate. The e2e twin is still the one that runs
  // against the shipped bundle; this is the one that runs on every push.
  //
  // A LIST, never three presence checks: three presence checks are satisfied by any
  // permutation of the three cards, and reordering the rail's JSX is precisely what
  // AUDIT-09-02 and AUDIT-09-06 both did.
  const RAIL_ORDER = ['fiscal-record-card', 'approval-card', 'source-document-card']
  // Wider than RAIL_ORDER: `status-history` is the retired card, and failed-dead-end /
  // rejection-reasons are the two rail members an accepted invoice suppresses. Any of them
  // mounting lands in the read and breaks the equality, so absence and order are one
  // assertion rather than two.
  //
  // compliance-card AND violations-table are both watched: the card is what BUG-13-02 moves
  // out of the rail, and violations-table is the only thing it exposed there beforehand.
  // Watching just the new testid would make this pass today for want of a testid.
  const RAIL_WATCHED = [...RAIL_ORDER, 'compliance-card', 'violations-table', 'status-history', 'failed-dead-end', 'rejection-reasons']

  it('invoiceDetail_railOrderIsThreeCardsAndExcludesCompliance', async () => {
    mockDetailFetch(
      detailRecord({
        id: ID,
        status: 'accepted',
        irn: 'IRN-2026-0001',
        csid: 'CSID-2026-0001',
        rule_set_version: 3,
        rule_set_version_id: 'rsv-3',
        // Suppressed on `accepted`, and carried anyway: the watched list below is what
        // proves the suppression, not the absence of the data.
        rejection_reasons: [{ code: 'NGE-4102', message: 'Buyer TIN failed validation' }],
      }),
    )
    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    // Positive controls on the same element the order is read from.
    const rail = await screen.findByTestId('invoice-rail')
    for (const id of RAIL_ORDER) expect(within(rail).getByTestId(id), `${id} must render in the rail`).toBeTruthy()

    const order = Array.from(rail.querySelectorAll('[data-testid]'))
      .map((n) => n.getAttribute('data-testid') ?? '')
      .filter((id) => RAIL_WATCHED.includes(id))
    expect(
      order,
      "the rail's cards in document order: Fiscal record -> Approvals -> Source document, with Compliance no longer among them",
    ).toEqual(RAIL_ORDER)
  })
})

// RED specs (BUG-14-01, Mode A). The bar is gated `inv.can_edit && !editing`
// (InvoiceDetail.tsx:875) today, so every spec below that needs it on a can_edit:false
// record fails on a missing element, never an import/compile error.
describe('InvoiceDetail action cluster: the control set is stable (BUG-14-01, AC-2/AC-3)', () => {
  const ID = 'inv-stable-cluster-1'
  const ALL_STATUSES: InvoiceStatus[] = ['draft', 'validated', 'rejected', 'queued', 'submitted', 'accepted', 'failed']
  const EDITABLE_STATUSES: InvoiceStatus[] = ['draft', 'validated', 'rejected']
  // The closed set AC-2/AC-3 are about -- the story's "stable control set" table.
  const CONTROLS = ['view-ubl', 'detail-approve', 'detail-reject', 'edit-toggle', 'revalidate', 'detail-submit']
  // The disabled recipe .v2-btn-primary needs; detail-submit is the sibling to match.
  const MUTED_PROPS = ['background', 'color', 'cursor', 'filter'] as const

  it('the actions bar mounts on every status, never unmounting with can_edit', async () => {
    const mounted: InvoiceStatus[] = []
    for (const status of ALL_STATUSES) {
      mockDetailFetch(detailRecord({ id: ID, status, can_edit: EDITABLE_STATUSES.includes(status) }))
      render(<InvoiceDetail ctx={detailCtx(ID)} />)
      await screen.findByTestId('invoice-status-badge')
      if (screen.queryByTestId('invoice-actions') != null) mounted.push(status)
      cleanup()
    }
    // Equality is both the floor (seven renders really happened) and the claim.
    expect(mounted, 'invoice-actions must mount at every status').toEqual(ALL_STATUSES)
  })

  it('Edit is present and disabled when can_edit is false', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'accepted', can_edit: false }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-status-badge')

    const btn = screen.queryByTestId('edit-toggle') as HTMLButtonElement | null
    expect(btn, 'Edit must mount even when can_edit is false').not.toBeNull()
    expect((btn as HTMLButtonElement).disabled, 'present-and-disabled, never absent').toBe(true)
  })

  it('Edit is present and enabled when can_edit is true, and clicking it still enters the editor', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', can_edit: true, can_revalidate: true, can_submit: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const btn = (await screen.findByTestId('edit-toggle')) as HTMLButtonElement
    expect(btn.disabled, 'a blanket disabled would satisfy the row above and break Edit').toBe(false)
    fireEvent.click(btn)
    expect(screen.getByTestId('edit-invoice'), 'the click still opens the editor').toBeTruthy()
  })

  it('disabled Edit carries the same inline style layers as disabled Submit', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'accepted', can_edit: false, can_submit: false }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-status-badge')
    const edit = screen.queryByTestId('edit-toggle') as HTMLButtonElement | null
    const submit = screen.queryByTestId('detail-submit') as HTMLButtonElement | null
    // Floor first: both buttons mount, and Submit's own recipe is really applied, so the
    // equality below can never be satisfied by two blank styles.
    expect(edit, 'Edit must mount to be compared').not.toBeNull()
    expect(submit, 'Submit must mount to be compared').not.toBeNull()
    const editStyle = (edit as HTMLButtonElement).style
    const submitStyle = (submit as HTMLButtonElement).style
    for (const prop of MUTED_PROPS) expect(submitStyle[prop], `disabled Submit sets ${prop}`).not.toBe('')
    for (const prop of MUTED_PROPS) expect(editStyle[prop], `disabled Edit matches Submit's ${prop}`).toBe(submitStyle[prop])
  })

  // MUTATION ORACLE: an unconditional spread satisfies the parity row above while killing
  // the enabled button's legitimate .v2-btn-primary:hover.
  it('an enabled Edit carries none of the four muted style properties', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', can_edit: true, can_revalidate: true, can_submit: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    const edit = (await screen.findByTestId('edit-toggle')) as HTMLButtonElement

    expect(edit.disabled).toBe(false)
    for (const prop of MUTED_PROPS) expect(edit.style[prop], `enabled Edit must not set ${prop}`).toBe('')
  })

  it('a role that permits nothing yields six controls, all disabled', async () => {
    mockDetailFetch(
      detailRecord({
        id: ID,
        status: 'accepted',
        can_edit: false,
        can_revalidate: false,
        can_submit: false,
        can_view_ubl: false,
        can_approve: false,
        can_reject: false,
        can_resolve_outside: false,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-status-badge')

    // Floor before any disabled claim -- an empty collection satisfies the loop below.
    const resolved = CONTROLS.filter((id) => screen.queryAllByTestId(id).length === 1)
    expect(resolved, 'each of the six resolves exactly once').toEqual(CONTROLS)
    for (const id of CONTROLS) {
      expect((screen.getByTestId(id) as HTMLButtonElement).disabled, `${id} must be disabled, not absent`).toBe(true)
    }
  })

  it('the control set is identical at a can_edit-true and a can_edit-false status', async () => {
    const collect = () => CONTROLS.filter((id) => screen.queryByTestId(id) != null)

    mockDetailFetch(detailRecord({ id: ID, status: 'validated', can_edit: true, can_submit: true }))
    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-status-badge')
    const editableSet = collect()
    expect(editableSet, 'floor: the editable status shows all six').toEqual(CONTROLS)
    cleanup()

    mockDetailFetch(detailRecord({ id: ID, status: 'accepted', can_edit: false, can_submit: false }))
    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-status-badge')
    expect(collect(), 'membership must not shift with status').toEqual(editableSet)
  })

  // The `!editing` half, which this story does not widen. Companion to 'Q12/AC3: the
  // actions bar stays gated on !editing even with a banner holding the column open'.
  it('the editor still owns the screen: clicking Edit unmounts the bar', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'draft', can_edit: true, can_revalidate: true, can_submit: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    fireEvent.click(await screen.findByTestId('edit-toggle'))

    expect(screen.queryByTestId('invoice-actions'), 'the !editing half survives the widen').toBeNull()
  })

  // Reshapes 'T7-1: resolve action renders inside the failed card, not invoice-actions',
  // whose `invoice-actions is null` half goes vacuous once the bar always mounts.
  it('reshaped: resolve-outside is not a descendant of invoice-actions', async () => {
    mockDetailFetch(detailRecord({ status: 'failed', can_resolve_outside: true }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const resolveOutside = await screen.findByTestId('resolve-outside')
    const bar = screen.queryByTestId('invoice-actions')
    expect(bar, 'floor: the bar mounts on failed too, so this claim is not vacuous').not.toBeNull()
    expect((bar as HTMLElement).contains(resolveOutside), 'the resolve action lives in the failed card').toBe(false)
  })

  // Reshapes 'T3/AC2: renders where the actions bar does not' -- same genre.
  it('reshaped: view-ubl is not a descendant of invoice-actions', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: false }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const viewUbl = await screen.findByTestId('view-ubl')
    const bar = screen.queryByTestId('invoice-actions')
    expect(bar, 'floor: the bar mounts on queued too, so this claim is not vacuous').not.toBeNull()
    expect((bar as HTMLElement).contains(viewUbl), 'view-ubl is a sibling of the bar, not a child').toBe(false)
  })

  // Strengthens '17: detail-decision-actions is a sibling of view-ubl and invoice-actions
  // ... never on can_edit', whose name already claimed exactly this while its second
  // render asserted invoice-actions away.
  it('all three action rows are siblings in one column on a can_edit:false invoice', async () => {
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_approve: true, can_reject: true }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)

    const decisionActions = await screen.findByTestId('detail-decision-actions')
    const viewUbl = screen.getByTestId('view-ubl')
    const bar = screen.queryByTestId('invoice-actions')
    expect(bar, 'floor: all three rows mount on a can_edit:false invoice').not.toBeNull()
    expect(decisionActions.parentElement).toBe(viewUbl.parentElement)
    expect((bar as HTMLElement).parentElement, 'the bar shares the column, at every status').toBe(viewUbl.parentElement)
  })
})

// QA-added (Stage 4, BUG-14-01). Three gaps the Mode-A specs leave: AC-1's four named
// statuses are only spot-checked at accepted/queued; nothing isolates Edit's flag from the
// other five; and the executor DELETED rather than flipped the two assertions at main's
// :1312/:1313, dropping the only pin on where a submit reason may appear.
describe('InvoiceDetail action cluster -- QA adversarial coverage (BUG-14-01)', () => {
  const ID = 'inv-qa-cluster-1'
  const NON_EDITABLE: InvoiceStatus[] = ['queued', 'submitted', 'accepted', 'failed']
  const BAR_CONTROLS = ['edit-toggle', 'revalidate', 'detail-submit']

  // AC-1 names four statuses and three controls; the Mode-A specs prove the BAR mounts at
  // all seven but only prove the three controls disabled at accepted (AC-3) and queued.
  it('AC-1: all three bar controls are present and disabled at each of the four non-editable statuses', async () => {
    const seen: string[] = []
    for (const status of NON_EDITABLE) {
      mockDetailFetch(detailRecord({ id: ID, status, can_edit: false, can_revalidate: false, can_submit: false }))
      render(<InvoiceDetail ctx={detailCtx(ID)} />)
      await screen.findByTestId('invoice-status-badge')
      for (const id of BAR_CONTROLS) {
        const el = screen.queryByTestId(id) as HTMLButtonElement | null
        if (el != null && el.disabled) seen.push(`${status}/${id}`)
      }
      cleanup()
    }
    // Equality is the floor: a status that rendered nothing contributes no entries.
    expect(seen).toEqual(NON_EDITABLE.flatMap((s) => BAR_CONTROLS.map((c) => `${s}/${c}`)))
  })

  // Isolates Edit's own flag. A `disabled={!inv.can_edit}` copied onto the bar's wrapper, or
  // onto its siblings, satisfies every all-false fixture; only a can_edit-true/rest-false
  // wire separates them. No REAL wire pairs can_edit:true with can_view_ubl:false, which is
  // the point -- the fixture is synthetic on purpose.
  it('AC-2: can_edit true with every other flag false leaves Edit the one enabled control', async () => {
    mockDetailFetch(
      detailRecord({
        id: ID,
        status: 'draft',
        can_edit: true,
        can_revalidate: false,
        can_submit: false,
        can_view_ubl: false,
        can_approve: false,
        can_reject: false,
      }),
    )

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-status-badge')

    const enabled = ['view-ubl', 'detail-approve', 'detail-reject', 'edit-toggle', 'revalidate', 'detail-submit'].filter(
      (id) => (screen.getByTestId(id) as HTMLButtonElement).disabled === false,
    )
    expect(enabled, 'only Edit reads can_edit').toEqual(['edit-toggle'])
  })

  // Restores the claim the executor deleted with main's `submit-blocked-reason is null` /
  // `body does not contain ROLE_REASON` pair: BUG-14-01 makes the role sentence reachable on
  // four statuses where it never rendered, and nothing else pins that. Scoped to reachability,
  // NOT to the node's existence -- BUG-14-02 deletes the node and retargets this spec with it.
  it('AC-1: the submit reason a non-approver gets on a non-editable status now reaches the page', async () => {
    const ROLE = 'Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team.'
    mockDetailFetch(detailRecord({ id: ID, status: 'queued', can_edit: false, can_submit: false, submit_blocked_reason: ROLE }))

    render(<InvoiceDetail ctx={detailCtx(ID)} />)
    await screen.findByTestId('invoice-status-badge')

    const submit = screen.getByTestId('detail-submit') as HTMLButtonElement
    expect(submit.disabled, 'floor: the control the sentence explains is up and refusing').toBe(true)
    expect(screen.getByTestId('submit-blocked-reason').textContent).toBe(ROLE)
    expect(document.body.textContent, 'the sentence is on the page, not only in an attribute').toContain(ROLE)
  })
})
