// RED specs (APPR-12-02, task-527, A02-1..A02-18) -- pin the Approvals screen's pure
// core + fan-out client before the executor fills in approvals.ts's stub bodies.
// Every spec below fails today because the stub throws `not implemented` before doing
// any real work -- that IS the correct red reason (assertion / not-implemented), not an
// import/compile/setup error. Mirrors invoices.test.ts's own `vi.stubGlobal('fetch', ...)`
// idiom: `fetch` is stubbed, but `createAuthedFetch`/`apiFetch` are the real
// @invoice-os/api-client + src/lib/authedFetch.ts exports, so a stubbed 200/401/403
// produces a genuine ApiError -- proof at the integration level.

import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from './authedFetch'
import {
  approvalRowView,
  approvalsBarView,
  approveInvoices,
  isApprovableRow,
  listAwaitingApproval,
  pruneApprovalSelection,
  type ApproveResult,
} from './approvals'
import type { InvoiceApproval, InvoiceRecord, ListInvoicesOptions } from './invoices'

interface MockResponse {
  ok: boolean
  status: number
  statusText?: string
  json: () => Promise<unknown>
}

function mockFetchOnce(response: MockResponse) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function stubFetch(impl: (url: string) => MockResponse | Promise<MockResponse>) {
  const fetchMock = vi.fn((url: string, _init?: RequestInit) => Promise.resolve(impl(url)))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function okResponse(): MockResponse {
  return { ok: true, status: 200, json: () => Promise.resolve({ run_id: 'r1', state: 'open', steps: [], decisions: [] }) }
}

// `message` becomes the resolved ApiError's own `.message` (apiFetch's own {error} unwrap
// rule) -- the exact vehicle A02-8 pins as riding through byte-identical.
function errorResponse(status: number, message: string): MockResponse {
  return { ok: false, status, statusText: message, json: () => Promise.resolve({ error: message }) }
}

// One invoice-id segment out of `.../invoices/{id}/approvals` -- throws loudly on a
// malformed url rather than returning undefined, so a wrong route fails fast and
// legibly instead of producing a confusing downstream Map miss.
function idFromUrl(url: string): string {
  const m = /\/invoices\/([^/]+)\/approvals$/.exec(url)
  if (!m) throw new Error(`unexpected url shape: ${url}`)
  return m[1]
}

async function waitUntil(predicate: () => boolean, maxTicks = 50): Promise<void> {
  for (let i = 0; i < maxTicks && !predicate(); i++) await Promise.resolve()
}

afterEach(() => {
  vi.unstubAllGlobals()
})

const base = 'https://gw'

const OPEN_APPROVAL: InvoiceApproval = {
  run_state: 'open',
  pending_ord: 1,
  pending_role_title: 'Reviewer',
  pending_holder_warn: false,
  due_at: null,
  overdue: false,
}

const baseRow: InvoiceRecord = {
  id: 'inv-base',
  entity_id: 'e1',
  import_batch_id: null,
  invoice_number: 'INV-100',
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
  rule_set_version_id: 'rsv-1',
  created_at: '2026-07-01T00:00:00Z',
  irn: null,
  csid: null,
  qr_payload: null,
  rejection_reasons: [],
  kept_as_is_at: null,
  kept_as_is_by: null,
  kept_as_is_reason: null,
  failure_kind: null,
  approval: OPEN_APPROVAL,
  rule_set_version: 2,
  can_approve: true,
  approve_blocked_reason: null,
}

function approvableRow(id: string): InvoiceRecord {
  return { ...baseRow, id, can_approve: true, approve_blocked_reason: null, approval: { ...OPEN_APPROVAL } }
}

function notApprovableRow(id: string): InvoiceRecord {
  return {
    ...baseRow,
    id,
    can_approve: false,
    approve_blocked_reason: 'Only a validated invoice can be approved or rejected.',
    approval: null,
  }
}

describe('listAwaitingApproval', () => {
  it('A02-1: forces awaiting_approval=true regardless of what opts carries', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    // A caller reaching around the Omit<> with a cast is exactly the adversarial case:
    // the wrapper must still win, not just supply a default when the key is absent.
    await listAwaitingApproval(af, base, { entityId: 'e1', awaitingApproval: false } as unknown as ListInvoicesOptions)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    const parsed = new URL(url)
    expect(parsed.searchParams.get('awaiting_approval')).toBe('true')
    expect(parsed.searchParams.get('entity_id')).toBe('e1')
  })

  it('A02-2: the envelope survives whole -- pagination and every row pass through', async () => {
    const rows = [approvableRow('inv-1'), notApprovableRow('inv-2')]
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: rows, pagination: { limit: 50, offset: 10, total: 137 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listAwaitingApproval(af, base, {})

    expect(result.pagination).toEqual({ limit: 50, offset: 10, total: 137 })
    expect(result.invoices.length).toBeGreaterThan(0)
    expect(result.invoices.map((r) => r.id)).toEqual(['inv-1', 'inv-2'])
  })
})

describe('isApprovableRow (A02-3, fail-closed against contradictory sibling facts)', () => {
  it('A02-3: can_approve alone decides it, even when run_state disagrees either way', () => {
    // can_approve:true but run_state says closed -- a second predicate would refuse
    // this; the single-clause predicate must not.
    const trueDespiteClosedRun = { ...baseRow, can_approve: true, approval: { ...OPEN_APPROVAL, run_state: 'approved' } }
    expect(isApprovableRow(trueDespiteClosedRun)).toBe(true)

    // can_approve:false but run_state says open -- a second predicate might let this
    // through; the single-clause predicate must still refuse (fail-closed).
    const falseDespiteOpenRun = { ...baseRow, can_approve: false, approval: { ...OPEN_APPROVAL, run_state: 'open' } }
    expect(isApprovableRow(falseDespiteOpenRun)).toBe(false)

    // Absent/undefined can_approve is not `=== true`.
    const absent = { ...baseRow, can_approve: undefined as unknown as boolean }
    expect(isApprovableRow(absent)).toBe(false)

    // Positive control: nothing here is vacuously false.
    expect(isApprovableRow({ ...baseRow, can_approve: true })).toBe(true)
  })
})

describe('approveInvoices (A02-4..A02-9, the fan-out contract)', () => {
  it('A02-4: one POST per id, right route and body, in request order', async () => {
    const fetchMock = stubFetch(() => okResponse())
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await approveInvoices(af, base, ['inv-a', 'inv-b'])

    expect(fetchMock).toHaveBeenCalledTimes(2)
    const calls = fetchMock.mock.calls as [string, RequestInit][]
    expect(calls.map(([url]) => url)).toEqual([
      'https://gw/api/invoice/v1/invoices/inv-a/approvals',
      'https://gw/api/invoice/v1/invoices/inv-b/approvals',
    ])
    for (const [, init] of calls) {
      expect(init.method).toBe('POST')
      expect(JSON.parse(init.body as string)).toEqual({ decision: 'approved' })
    }
  })

  it('A02-5: concurrency is exactly 1 -- request n+1 is not issued until n settles', async () => {
    const resolvers = new Map<string, (v: MockResponse) => void>()
    const fetchMock = vi.fn((url: string) => {
      const id = idFromUrl(url)
      return new Promise<MockResponse>((resolve) => resolvers.set(id, resolve))
    })
    vi.stubGlobal('fetch', fetchMock)
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const resultPromise = approveInvoices(af, base, ['a', 'b', 'c'])
    // Today's stub rejects synchronously and nothing below ever awaits it if an
    // earlier assertion fails first -- a harmless extra handler keeps that rejection
    // from surfacing as vitest's global "unhandled rejection" noise.
    resultPromise.catch(() => {})

    await waitUntil(() => resolvers.has('a'))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(resolvers.has('b'), 'b must not be requested before a settles').toBe(false)

    resolvers.get('a')!(okResponse())
    await waitUntil(() => resolvers.has('b'))
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(resolvers.has('c'), 'c must not be requested before b settles').toBe(false)

    resolvers.get('b')!(okResponse())
    await waitUntil(() => resolvers.has('c'))
    expect(fetchMock).toHaveBeenCalledTimes(3)

    resolvers.get('c')!(okResponse())
    const results = await resultPromise
    expect(results).toHaveLength(3)
  })

  it('A02-6: a per-item failure -- 401 included -- does not abort the run', async () => {
    const fetchMock = stubFetch((url) =>
      idFromUrl(url) === 'a' ? errorResponse(401, 'unauthorized') : okResponse(),
    )
    const onUnauthorized = vi.fn()
    const af = createAuthedFetch(() => 'tok', onUnauthorized)

    const results = await approveInvoices(af, base, ['a', 'b'])

    // b's request was actually issued despite a's 401 -- the trap this spec exists to
    // catch is a loop that lets authedFetch's rethrow abort the whole call.
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(results).toHaveLength(2)
    expect(results[0]).toEqual({ id: 'a', ok: false, message: 'unauthorized' })
    expect(results[1]).toEqual({ id: 'b', ok: true })
    // authedFetch's own 401 side effect still fires -- it is caught, not suppressed.
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('A02-7: results are per item, never a count -- one array entry per id, in order', async () => {
    const fetchMock = stubFetch((url) => (idFromUrl(url) === 'b' ? errorResponse(409, 'closed') : okResponse()))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const results = await approveInvoices(af, base, ['a', 'b', 'c'])

    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(Array.isArray(results)).toBe(true)
    expect(results.length).toBeGreaterThan(0)
    expect(results).toHaveLength(3)
    expect(results.map((r) => r.id)).toEqual(['a', 'b', 'c'])
    expect(results.map((r) => r.ok)).toEqual([true, false, true])
  })

  it('A02-8: the server\'s reason rides through byte-identically, no SPA rewording', async () => {
    // Verbatim reason #5 from internal/invoice/handlers.go:394.
    const reason =
      "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role."
    stubFetch(() => errorResponse(403, reason))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const results = await approveInvoices(af, base, ['a'])

    expect(results).toHaveLength(1)
    const [result] = results as [ApproveResult]
    expect(result.ok).toBe(false)
    expect(result.ok === false && result.message).toBe(reason)
  })

  it('A02-9: the empty case issues zero fetches and resolves []', async () => {
    const fetchMock = stubFetch(() => okResponse())
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const results = await approveInvoices(af, base, [])

    expect(results).toEqual([])
    expect(fetchMock).toHaveBeenCalledTimes(0)
  })
})

describe('approvalsBarView (A02-10..A02-14)', () => {
  it('A02-10: one shared gate -- submitting and page-loading both close it, the open state opens it', () => {
    const rows = [approvableRow('a'), approvableRow('b')]

    expect(approvalsBarView(['a', 'b'], rows, 'submitting', false).canApprove).toBe(false)
    expect(approvalsBarView(['a', 'b'], rows, 'armed', true).canApprove).toBe(false)
    expect(approvalsBarView(['a', 'b'], rows, 'armed', false).canApprove).toBe(true)
  })

  it('A02-11: the scope is in the string -- countLabel names the page, not the tenant', () => {
    const rows = [approvableRow('a')]

    const view = approvalsBarView(['a'], rows, 'armed', false)

    expect(view.countLabel).toContain('this page')
    expect(view.countLabel).toContain('1')
  })

  it('A02-12: singular at one -- confirmPrompt never reads "1 invoices"', () => {
    const rows = [approvableRow('a')]

    const view = approvalsBarView(['a'], rows, 'armed', false)

    expect(view.confirmPrompt).not.toMatch(/1 invoices\b/i)
    expect(view.confirmPrompt).toMatch(/\b1 invoice\b/i)
  })

  it('A02-13: the bar promises the action, not the outcome', () => {
    const rows = [approvableRow('a'), approvableRow('b'), approvableRow('c')]

    const view = approvalsBarView(['a', 'b', 'c'], rows, 'armed', false)
    const text = `${view.confirmPrompt} ${view.confirmDetail}`

    // "approved" (outcome, past participle) never appears -- another approver can
    // decide a row between this fetch and the confirm click.
    expect(text).not.toMatch(/\bapproved\b/i)
    // Positive control: the action itself is still named somewhere in the bar's copy.
    expect(text).toMatch(/\bapprove\b/i)
  })

  it('A02-14: the note is page-scoped', () => {
    const mixedRows = [approvableRow('a'), notApprovableRow('b')]
    const mixed = approvalsBarView(['a'], mixedRows, 'armed', false)
    expect(mixed.note).not.toBeNull()
    expect(mixed.note).toContain('this page')

    const allReadyRows = [approvableRow('a')]
    const allReady = approvalsBarView(['a'], allReadyRows, 'armed', false)
    expect(allReady.note).toBeNull()
  })
})

describe('approvalRowView (A02-15, A02-16, A02-18)', () => {
  it('A02-15: the em-dash is the ONLY fallback -- approve_blocked_reason rides through verbatim or stays null', () => {
    const withTitle = approvalRowView({ ...baseRow, approval: { ...OPEN_APPROVAL, pending_role_title: 'Reviewer' } })
    expect(withTitle.roleLabel).toBe('Reviewer')

    const withoutTitle = approvalRowView({ ...baseRow, approval: { ...OPEN_APPROVAL, pending_role_title: null } })
    expect(withoutTitle.roleLabel).toBe('—')

    // Verbatim reason #2 from internal/invoice/handlers.go:382 -- no SPA rewording.
    const reason = 'Only a validated invoice can be approved or rejected.'
    const blocked = approvalRowView({ ...baseRow, can_approve: false, approve_blocked_reason: reason })
    expect(blocked.blockedReason).toBe(reason)

    const open = approvalRowView({ ...baseRow, can_approve: true, approve_blocked_reason: null })
    expect(open.blockedReason).toBeNull()
  })

  it('A02-16: overdue is the wire\'s own answer, never re-derived from due_at', () => {
    const pastDueNotOverdue = approvalRowView({
      ...baseRow,
      approval: { ...OPEN_APPROVAL, due_at: '2020-01-01T00:00:00Z', overdue: false },
    })
    expect(pastDueNotOverdue.overdue).toBe(false)

    const futureDueOverdue = approvalRowView({
      ...baseRow,
      approval: { ...OPEN_APPROVAL, due_at: '2099-01-01T00:00:00Z', overdue: true },
    })
    expect(futureDueOverdue.overdue).toBe(true)
  })

  it('A02-18: pending_holder_warn is a warning, not a gate -- the row stays selectable AND the warning still renders', () => {
    const row = { ...baseRow, can_approve: true, approval: { ...OPEN_APPROVAL, pending_holder_warn: true } }

    expect(isApprovableRow(row)).toBe(true)
    expect(approvalRowView(row).pendingHolderWarn).toBe(true)
  })
})

describe('pruneApprovalSelection (A02-17)', () => {
  it('A02-17: drops exactly the stale ids -- gone-from-rows and no-longer-approvable alike, keeps the rest, in order', () => {
    const rows = [approvableRow('a'), notApprovableRow('b'), approvableRow('c')]

    // 'b' is present but no longer approvable; 'd' is absent from rows entirely --
    // both are stale and must be dropped, and only them.
    const result = pruneApprovalSelection(['a', 'b', 'd'], rows)
    expect(result.length).toBeGreaterThan(0)
    expect(result).toEqual(['a'])

    // Nothing stale: every id survives, in the same order (content only -- G1: this
    // helper owes no instance-identity guarantee, that lives in subtask 04's effect).
    const unchanged = pruneApprovalSelection(['a', 'c'], rows)
    expect(unchanged).toEqual(['a', 'c'])
  })
})
