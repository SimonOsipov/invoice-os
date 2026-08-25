// RED specs (APPR-12-02, task-527, A02-1..A02-18) -- pin the Approvals screen's pure
// core + fan-out client before the executor fills in approvals.ts's stub bodies.
// Every spec below fails today because the stub throws `not implemented` before doing
// any real work -- that IS the correct red reason (assertion / not-implemented), not an
// import/compile/setup error. Mirrors invoices.test.ts's own `vi.stubGlobal('fetch', ...)`
// idiom: `fetch` is stubbed, but `createAuthedFetch`/`apiFetch` are the real
// @invoice-os/api-client + src/lib/authedFetch.ts exports, so a stubbed 200/401/403
// produces a genuine ApiError -- proof at the integration level.

/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type ApiFetchOptions } from '@invoice-os/api-client'

import { createAuthedFetch } from './authedFetch'
import {
  APPROVAL_CARD_COPY,
  APPROVALS_COPY,
  approvalOutcome,
  approvalProgressLabel,
  approvalRowView,
  approvalRunStateView,
  approvalSelectAllState,
  approvalsBarView,
  approvalStateView,
  approveInvoices,
  canRejectReason,
  decideInvoice,
  decisionBlockedReasons,
  getInvoiceApprovalRun,
  isApprovableRow,
  listAwaitingApproval,
  pendingApprovalStep,
  pruneApprovalSelection,
  type ApprovalRun,
  type ApprovalRunStep,
  type ApproveResult,
} from './approvals'
import { fmtDate } from './format'
import type { InvoiceApproval, InvoiceRecord, ListInvoicesOptions } from './invoices'
import type { AuthedFetch } from './portfolio'

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

  // QA (Stage 4, Mode B): the four cases above never distinguish `=== true` from a
  // truthy check -- `undefined` is falsy either way. A wire value that is truthy but not
  // strictly `true` closes that gap; normaliseInvoiceRow already coerces with
  // `=== true` (invoices.ts:557) before this ever runs, but isApprovableRow is exported
  // and callable directly, so its own strictness must not depend on that upstream step.
  it('A02-3b: a truthy-but-not-true can_approve is still refused (strict equality, not coercion)', () => {
    expect(isApprovableRow({ ...baseRow, can_approve: 1 as unknown as boolean })).toBe(false)
    expect(isApprovableRow({ ...baseRow, can_approve: 'true' as unknown as boolean })).toBe(false)
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

// G4 (task-528, APPR-12-03, Mode A). approvalRowView has no `stepLabel` field yet --
// `view.stepLabel` below type-checks as `undefined` via the widened intersection (never a
// tsc error) and fails on the VALUE, the correct red reason. pending_ord is 0-BASED on
// the wire (gate_test.go:1086 pins Ord 0 as a legitimate pending step, not "no step"), and
// null on a row with no run at all (store.go:691-694's vacuous NOT EXISTS).
describe('approvalRowView: step label is 1-based and null-safe (G4, new for APPR-12-03)', () => {
  it('pending_ord 0 renders "Step 1", not "Step 0"', () => {
    const view = approvalRowView({ ...baseRow, approval: { ...OPEN_APPROVAL, pending_ord: 0 } }) as ReturnType<typeof approvalRowView> & { stepLabel?: string }

    expect(view.pendingOrd, 'the raw wire ordinal must still pass through unchanged').toBe(0)
    expect(view.stepLabel, "a 0-based wire ordinal must render 1-based for humans -- 'Step 0' is wrong on its face").toBe('Step 1')
  })

  it('pending_ord null (no run at all) renders an em dash', () => {
    const view = approvalRowView({ ...baseRow, approval: null }) as ReturnType<typeof approvalRowView> & { stepLabel?: string }

    expect(view.pendingOrd).toBeNull()
    expect(view.stepLabel, 'no run at all must render the em dash, never "Step null" or a blank').toBe('—')
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

// --- Adversarial / edge coverage added at QA (Stage 4, Mode B), on top of the
// Stage-2.5 AC specs above (A02-1..A02-18, left untouched). approvalOutcome and
// APPROVALS_COPY had no numbered spec in the architect's Test Specs table -- Mode A
// declared their signatures for AC-1 but wrote no red for them. ---

describe('approvalOutcome (no A02 spec -- QA-added)', () => {
  it('builds one row per result, in result order, for a mixed pass/fail batch', () => {
    const results: ApproveResult[] = [
      { id: 'a', ok: true },
      { id: 'b', ok: false, message: 'this approval run is already closed' },
      { id: 'c', ok: true },
    ]
    const numbersById = new Map([
      ['a', 'INV-001'],
      ['b', 'INV-002'],
      ['c', 'INV-003'],
    ])

    const rows = approvalOutcome(results, numbersById)

    expect(rows.length).toBeGreaterThan(0)
    expect(rows).toEqual([
      { invoiceNumber: 'INV-001', ok: true, label: 'Approved', message: null },
      {
        invoiceNumber: 'INV-002',
        ok: false,
        label: 'Not approved',
        message: 'this approval run is already closed',
      },
      { invoiceNumber: 'INV-003', ok: true, label: 'Approved', message: null },
    ])
  })

  it('falls back to the raw id when numbersById has no entry for it', () => {
    const results: ApproveResult[] = [{ id: 'inv-unknown', ok: true }]

    const rows = approvalOutcome(results, new Map())

    expect(rows).toEqual([{ invoiceNumber: 'inv-unknown', ok: true, label: 'Approved', message: null }])
  })

  it('the label differs between the ok and non-ok cases, and is never empty', () => {
    const results: ApproveResult[] = [
      { id: 'a', ok: true },
      { id: 'b', ok: false, message: 'this approval run is already closed' },
    ]

    const rows = approvalOutcome(results, new Map())

    for (const row of rows) {
      expect(typeof row.label).toBe('string')
      expect(row.label.length).toBeGreaterThan(0)
    }
    expect(rows[0].label).not.toEqual(rows[1].label)
  })

  it('is derived from the results array, never from numbersById\'s own size', () => {
    // numbersById carries five entries (as if built off a larger `selected`); results
    // carries only two. The output must track results, not the map.
    const numbersById = new Map([
      ['a', 'INV-001'],
      ['b', 'INV-002'],
      ['c', 'INV-003'],
      ['d', 'INV-004'],
      ['e', 'INV-005'],
    ])
    const results: ApproveResult[] = [
      { id: 'a', ok: true },
      { id: 'b', ok: true },
    ]

    const rows = approvalOutcome(results, numbersById)

    expect(rows).toHaveLength(2)
    expect(rows.map((r) => r.invoiceNumber)).toEqual(['INV-001', 'INV-002'])
  })
})

describe('APPROVALS_COPY (no A02 spec -- QA-added)', () => {
  it('declares every key as a non-empty string', () => {
    const keys = ['clear', 'cancel', 'sending', 'resultInvoice', 'resultOutcome'] as const
    expect(keys.length).toBeGreaterThan(0)
    for (const key of keys) {
      expect(typeof APPROVALS_COPY[key]).toBe('string')
      expect(APPROVALS_COPY[key].length).toBeGreaterThan(0)
    }
  })
})

describe('adversarial / edge coverage (QA Stage 4, Mode B)', () => {
  it('a fan-out where the LAST item fails still returns every entry, in order', async () => {
    const fetchMock = stubFetch((url) => (idFromUrl(url) === 'c' ? errorResponse(409, 'closed') : okResponse()))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const results = await approveInvoices(af, base, ['a', 'b', 'c'])

    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(results).toHaveLength(3)
    expect(results.map((r) => r.ok)).toEqual([true, true, false])
    expect(results[2]).toEqual({ id: 'c', ok: false, message: 'closed' })
  })

  it('a fan-out where EVERY item fails still returns one entry per id, never throws', async () => {
    const fetchMock = stubFetch(() => errorResponse(409, 'this approval run is already closed'))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const results = await approveInvoices(af, base, ['a', 'b', 'c'])

    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(results).toHaveLength(3)
    expect(results.every((r) => r.ok === false)).toBe(true)
  })

  it('duplicate ids in the input each issue their own request and their own result entry', async () => {
    const fetchMock = stubFetch(() => okResponse())
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const results = await approveInvoices(af, base, ['a', 'a'])

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(results).toHaveLength(2)
    expect(results.map((r) => r.id)).toEqual(['a', 'a'])
  })

  it('approvalsBarView with nothing selected is not visible and cannot be confirmed, even while open', () => {
    const rows = [approvableRow('a'), approvableRow('b')]

    const view = approvalsBarView([], rows, 'armed', false)

    expect(view.visible).toBe(false)
    expect(view.canApprove).toBe(false)
    expect(view.eligible).toEqual([])
  })

  it('approvalSelectAllState never reports "all" on a page with zero approvable rows (vacuous-every guard)', () => {
    // The empty-rows case and the all-blocked case both hit the `approvable.length === 0`
    // guard; an unguarded `.every()` over an empty array is vacuously true and would
    // report 'all' here, which is the trap this test exists to catch.
    expect(approvalSelectAllState([], [])).toBe('none')

    const allBlocked = [notApprovableRow('a'), notApprovableRow('b')]
    expect(approvalSelectAllState([], allBlocked)).toBe('none')
    expect(approvalSelectAllState(['a', 'b'], allBlocked)).toBe('none')
  })

  it('a contradictory wire row (approve_blocked_reason set while can_approve is true) -- can_approve wins, the reason still rides through', () => {
    const row = { ...baseRow, can_approve: true, approve_blocked_reason: 'This invoice has no approval run to decide on.' }

    // Selectability reads can_approve alone (U5) -- the stale/contradictory reason does
    // not override it.
    expect(isApprovableRow(row)).toBe(true)
    // The reason is passed through unconditionally, never blanked out because can_approve
    // happened to be true -- approvalRowView authors no "can_approve wins so hide it" logic.
    expect(approvalRowView(row).blockedReason).toBe('This invoice has no approval run to decide on.')
  })
})

// --- task-529 (APPR-12-04, Mode A) -- RED specs for bulk approve's plan-validation gaps
// that live in this file. approvalProgressLabel throws `not implemented` today (its own
// stub comment), so both specs below fail on that throw -- the correct red reason. ---

describe('approvalProgressLabel (G-04-A, new for APPR-12-04)', () => {
  it('names both the done count and the total, and is never empty', () => {
    const label = approvalProgressLabel(1, 3)

    expect(typeof label).toBe('string')
    expect(label.length).toBeGreaterThan(0)
    expect(label).toContain('1')
    expect(label).toContain('3')
  })

  it('the label changes as done advances toward total -- not a static string', () => {
    expect(approvalProgressLabel(0, 5)).not.toBe(approvalProgressLabel(5, 5))
  })
})

// --- A04-11: LIB-SCAN-A (G-04-F) -- the repo's FIRST inline-literal scanner.
//
// Every shipped [bulk-copy-lives-in-the-lib] guard (LIB-SCAN-1 reviewBatch.test.ts:1358,
// TAB-7b, ROW-7b, BATCH-7b) scans for three FORBIDDEN WORDS, not inline literals, and the
// canonical [bulk-copy-lives-in-the-lib] component keeps its own checkbox aria-label
// inline (ReviewInvoicesTab.tsx:661) -- there is no precedent regex to copy, and an
// unscoped scan false-positives on every data-testid/className/CSS value in the file.
//
// THE RULE: a literal counts as inline copy if it is (1) a JSX text node -- bare text
// between tags, outside any {} expression, containing a real word (2+ letters); or (2) a
// quoted string literal on a title=/aria-label=/placeholder= attribute; or (3) a quoted
// string literal used as the fallback operand of a `??`/`||` expression sitting directly
// in JSX child position -- it renders exactly like a text node once the primary is
// absent. Category 3 is what catches ApprovalsView.tsx:81's own
// `ctx.user.tenantName ?? 'Your workspace'` -- the orchestrator ruled that fallback MOVES
// into APPROVALS_COPY rather than being allowlisted, so this scan must red on it today.
// Middle dots and whitespace-only text nodes (the ` · ` separator between two copy
// expressions) are not flagged: the word-character requirement is what tells them apart
// from real copy.
function stripJsxBraces(src: string): string {
  let out = ''
  let depth = 0
  for (const ch of src) {
    if (ch === '{') {
      depth++
      continue
    }
    if (ch === '}') {
      depth = Math.max(0, depth - 1)
      continue
    }
    out += depth === 0 ? ch : ' '
  }
  return out
}

function scanInlineLiterals(fullSource: string): string[] {
  const start = fullSource.indexOf('return (')
  const jsx = start >= 0 ? fullSource.slice(start) : fullSource
  const violations: string[] = []

  for (const m of jsx.matchAll(/\b(?:title|aria-label|placeholder)="([^"]*)"/g)) {
    if (/[a-zA-Z]{2,}/.test(m[1])) violations.push(m[0])
  }

  // JSX comments ({/* ... */}) are excluded before the fallback scan, not just the text-
  // node scan below -- a future comment mentioning `?? '...'` syntax must not false-positive.
  const withoutJsxComments = jsx.replace(/\{\s*\/\*[\s\S]*?\*\/\s*\}/g, '')
  for (const m of withoutJsxComments.matchAll(/(?:\?\?|\|\|)\s*'([^']*)'/g)) {
    if (/[a-zA-Z]{2,}/.test(m[1])) violations.push(m[0])
  }

  for (const m of stripJsxBraces(jsx).matchAll(/>([^<>{}]*)</g)) {
    if (/[a-zA-Z]{2,}/.test(m[1])) violations.push(m[1].trim())
  }

  return violations
}

describe('ApprovalsView.tsx source: no inline copy -- everything routes through lib/approvals.ts (A04-11, LIB-SCAN-A)', () => {
  it('non-vacuity control: the scan flags a synthetic violation of each of the three kinds, and flags nothing on a clean synthetic snippet', () => {
    const dirty = `
      function C() {
        return (
          <div>
            <input aria-label="Pick all the rows" />
            <span>Nothing selected yet</span>
            <p>{ctx.user.tenantName ?? 'Your workspace'} · {APPROVALS_COPY.subtitle}</p>
          </div>
        )
      }
    `
    const violations = scanInlineLiterals(dirty)
    expect(violations.length, 'the synthetic dirty snippet must trip at least one violation of each kind').toBeGreaterThanOrEqual(3)
    expect(violations.some((v) => v.includes('Pick all the rows'))).toBe(true)
    expect(violations.some((v) => v.includes('Nothing selected yet'))).toBe(true)
    expect(violations.some((v) => v.includes('Your workspace'))).toBe(true)

    const clean = `
      function C() {
        return (
          <div>
            <input aria-label={APPROVALS_COPY.selectAllLabel} />
            <span>{APPROVALS_COPY.emptyTitle}</span>
            <p>{ctx.user.tenantName ?? undefined} · {APPROVALS_COPY.subtitle}</p>
          </div>
        )
      }
    `
    expect(scanInlineLiterals(clean), 'a clean snippet routing everything through expressions must trip nothing').toEqual([])
  })

  it('A04-11: ApprovalsView.tsx carries no inline copy -- today it carries exactly the ?? \'Your workspace\' fallback', () => {
    const srcPath = fileURLToPath(new URL('../components/ApprovalsView.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    const violations = scanInlineLiterals(source)

    expect(violations, 'ApprovalsView.tsx must author no inline copy; the ruled-on exception moves into APPROVALS_COPY, it is not allowlisted').toEqual([])
  })
})

// --- APPR-16-04 (task-536, Mode A) -- RED specs for approveInvoices' optional 5th
// AbortSignal param (AC-1..AC-4). The row-boundary check must sit at the TOP of each
// loop iteration, before the request fires -- never mid-request. A16-4b is the one spec
// that would catch a signal wrongly forwarded into the inner authedFetch call, not just
// the absence of a boundary check.
// Calls approveInvoices with a 5th arg through a permissive cast, not a direct call --
// today's 4-param signature makes a direct 5-arg call a tsc error (TS2554), and Mode A
// prefers a RED that fails on the VALUE, not the type (G4's precedent, this same file).
// Same function reference at runtime either way.
type ApproveInvoicesWithSignal = (
  authedFetch: ReturnType<typeof createAuthedFetch>,
  base: string,
  ids: string[],
  onProgress: ((result: ApproveResult, index: number) => void) | undefined,
  signal: AbortSignal | undefined,
) => Promise<ApproveResult[]>
const approveInvoicesWithSignal = approveInvoices as unknown as ApproveInvoicesWithSignal

describe('approveInvoices: optional AbortSignal, checked at the row boundary only (APPR-16-04, task-536)', () => {
  it('A16-4a: aborting after row 2 of 5 leaves 2 sent and issues no third request', async () => {
    const fetchMock = stubFetch(() => okResponse())
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const controller = new AbortController()

    const results = await approveInvoicesWithSignal(
      af,
      base,
      ['a', 'b', 'c', 'd', 'e'],
      (_result, index) => {
        if (index === 1) controller.abort()
      },
      controller.signal,
    )

    expect(fetchMock, 'row 3 must never be requested once the row boundary sees the abort').toHaveBeenCalledTimes(2)
    expect(results).toHaveLength(2)
    expect(results.map((r) => r.id)).toEqual(['a', 'b'])
  })

  it('A16-4b: abort never cancels an in-flight request -- it settles, then row 4 is never sent', async () => {
    const resolvers = new Map<string, (v: MockResponse) => void>()
    // Mirrors what a REAL fetch does with a forwarded AbortSignal (rejects the pending
    // call on 'abort') -- this is what makes the test catch a signal wrongly plumbed
    // into authedFetch, not merely the absence of a row-boundary check.
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      const id = idFromUrl(url)
      return new Promise<MockResponse>((resolve, reject) => {
        resolvers.set(id, resolve)
        init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))
      })
    })
    vi.stubGlobal('fetch', fetchMock)
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const controller = new AbortController()

    const resultPromise = approveInvoicesWithSignal(af, base, ['a', 'b', 'c', 'd'], undefined, controller.signal)
    resultPromise.catch(() => {})

    await waitUntil(() => resolvers.has('a'))
    resolvers.get('a')!(okResponse())
    await waitUntil(() => resolvers.has('b'))
    resolvers.get('b')!(okResponse())
    await waitUntil(() => resolvers.has('c'))

    controller.abort() // row c is in flight right now -- must not be cancelled mid-wire
    expect(fetchMock, 'aborting must not touch the in-flight fetch call').toHaveBeenCalledTimes(3)
    resolvers.get('c')!(okResponse())

    // Bounded flush, never an indefinite wait: a correct implementation settles
    // resultPromise within a few microtask hops without row d ever being requested. A
    // buggy one that keeps looping WOULD request row d -- and nothing in this test ever
    // resolves it -- so this assertion (not an unconditional `await resultPromise`) is
    // what has to catch that, or the buggy path hangs instead of failing cleanly.
    for (let i = 0; i < 30 && fetchMock.mock.calls.length < 4; i++) await Promise.resolve()
    expect(fetchMock, 'row 4 must never be requested once the boundary sees the abort').toHaveBeenCalledTimes(3)

    const results = await resultPromise
    expect(results, "row c's own response is still recorded, not discarded mid-wire").toHaveLength(3)
    expect(results.map((r) => r.id)).toEqual(['a', 'b', 'c'])
    expect(results[2]).toEqual({ id: 'c', ok: true })
  })

  it('A16-4c: an already-aborted signal issues zero requests', async () => {
    const fetchMock = stubFetch(() => okResponse())
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const controller = new AbortController()
    controller.abort()

    const results = await approveInvoicesWithSignal(af, base, ['a', 'b'], undefined, controller.signal)

    expect(fetchMock).toHaveBeenCalledTimes(0)
    expect(results).toEqual([])
  })

  it('A16-4d: no signal behaves exactly as today -- 5 requests, 5 results, a per-item failure does not abort the run', async () => {
    // Backward compatibility for a caller that omits the signal is mechanical once it's
    // optional and 5th (Stage 1 architecture note) -- this arity check is the one part
    // of "no behaviour change for existing callers" that can actually go red before the
    // param exists; the behaviour itself is provably unchanged either way.
    expect(approveInvoices.length, 'approveInvoices must gain the optional 5th signal param').toBe(5)

    const fetchMock = stubFetch((url) => (idFromUrl(url) === 'c' ? errorResponse(409, 'closed') : okResponse()))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const results = await approveInvoices(af, base, ['a', 'b', 'c', 'd', 'e'])

    expect(fetchMock).toHaveBeenCalledTimes(5)
    expect(results).toHaveLength(5)
    expect(results.map((r) => r.ok)).toEqual([true, true, false, true, true])
  })
})

// --- APPR-13-01 (task-550, Mode A) -- RED specs for the approval-run wire mirror:
// getInvoiceApprovalRun/decideInvoice (AC-3/AC-4) and the two pure reason helpers
// (AC-2/AC-3), plus the three-way wire-mirror guard (AC-5/AC-6). The four wire types in
// lib/approvals.ts are real and complete (a type carries no behaviour); the five
// functions throw `new Error('not implemented')`. The wire-mirror rows below pin
// already-complete static text (the types, and internal/approval/read_model.go +
// e2e/api/client.ts, both untouched by this subtask) and so are expected to pass now --
// only the function-behaviour rows (getInvoiceApprovalRun/decideInvoice/canRejectReason/
// decisionBlockedReasons) are red on the stub throw.

const GO_READ_MODEL_PATH = fileURLToPath(new URL('../../../../internal/approval/read_model.go', import.meta.url))
const E2E_CLIENT_PATH = fileURLToPath(new URL('../../../../e2e/api/client.ts', import.meta.url))
const LIB_APPROVALS_PATH = fileURLToPath(new URL('./approvals.ts', import.meta.url))

// Struct-scoped extraction (D-46/AC-6): a file-wide tag regex would fold RunStep's own
// json tags into Resolved's count and silently corrupt every floor -- this delimits each
// struct body first, brace-balanced, then extracts keys only from within it.
function goStructKeys(source: string, structName: string): string[] {
  const body = new RegExp(`type\\s+${structName}\\s+struct\\s*\\{([^{}]*)\\}`).exec(source)?.[1] ?? ''
  const keys: string[] = []
  for (const m of body.matchAll(/`json:"([^"]+)"`/g)) {
    const key = m[1].split(',')[0]
    if (key !== '-') keys.push(key)
  }
  return keys
}

// Same brace-balanced trick for a TS interface body, works for both the multi-line style
// (e2e/api/client.ts) and a hypothetical single-line style without two code paths.
function tsInterfaceKeys(source: string, interfaceName: string): string[] {
  const body = new RegExp(`export interface\\s+${interfaceName}\\s*\\{([^{}]*)\\}`).exec(source)?.[1] ?? ''
  const keys: string[] = []
  for (const rawSeg of body.split(/[\n;]/)) {
    const seg = rawSeg.trim()
    if (!seg || seg.startsWith('//')) continue
    const m = /^([A-Za-z_][A-Za-z0-9_]*)\??\s*:/.exec(seg)
    if (m) keys.push(m[1])
  }
  return keys
}

// The comparator itself: symmetric-difference key names, [] means the two sets agree.
// Exercised for real by the equality row below and proven non-trivial by the
// planted-positive row -- it must be able to report a real mismatch, not just agree.
function keySetDiff(a: string[], b: string[]): string[] {
  const setA = new Set(a)
  const setB = new Set(b)
  const diff = new Set<string>()
  for (const k of a) if (!setB.has(k)) diff.add(k)
  for (const k of b) if (!setA.has(k)) diff.add(k)
  return [...diff]
}

const WIRE_STRUCTS = [
  { go: 'Resolved', ts: 'ApprovalResolved', floor: 2 },
  { go: 'RunStep', ts: 'ApprovalRunStep', floor: 13 },
  { go: 'RunDecision', ts: 'ApprovalRunDecision', floor: 6 },
  { go: 'Run', ts: 'ApprovalRun', floor: 7 },
] as const

describe('wire mirror: Go read_model.go <-> lib/approvals.ts <-> e2e/api/client.ts (AC-5, AC-6)', () => {
  // Needle and floor rows run BEFORE the equality row (D-46): a broken json-tag or
  // interface-key regex yields {} for every source, and equality would otherwise pass
  // vacuously on {} === {} === {}.
  it('control needle: all three source files were actually read, not stubbed or empty', () => {
    const goSource = readFileSync(GO_READ_MODEL_PATH, 'utf8')
    const libSource = readFileSync(LIB_APPROVALS_PATH, 'utf8')
    const e2eSource = readFileSync(E2E_CLIENT_PATH, 'utf8')

    expect(goSource.length).toBeGreaterThan(0)
    expect(goSource, 'lost anchor on read_model.go').toContain('func (r Run) MarshalJSON')

    expect(libSource.length).toBeGreaterThan(0)
    expect(libSource, 'lost anchor on lib/approvals.ts').toContain('export interface ApprovalRun')

    expect(e2eSource.length).toBeGreaterThan(0)
    expect(e2eSource, 'lost anchor on e2e/api/client.ts').toContain('getInvoiceApproval')
  })

  it('population FLOOR: the struct-scoped extractor produced a non-empty key set per struct per source', () => {
    const goSource = readFileSync(GO_READ_MODEL_PATH, 'utf8')
    const libSource = readFileSync(LIB_APPROVALS_PATH, 'utf8')
    const e2eSource = readFileSync(E2E_CLIENT_PATH, 'utf8')

    for (const { go, ts, floor } of WIRE_STRUCTS) {
      expect(goStructKeys(goSource, go).length, `Go ${go} must clear its floor of ${floor}`).toBeGreaterThanOrEqual(
        floor,
      )
      expect(
        tsInterfaceKeys(libSource, ts).length,
        `lib/approvals.ts ${ts} must clear its floor of ${floor}`,
      ).toBeGreaterThanOrEqual(floor)
      expect(
        tsInterfaceKeys(e2eSource, ts).length,
        `e2e/api/client.ts ${ts} must clear its floor of ${floor}`,
      ).toBeGreaterThanOrEqual(floor)
    }
  })

  it('three-way equality: every struct/interface key set agrees across all three sources (runs AFTER the floor row above, which is what makes this equality meaningful)', () => {
    const goSource = readFileSync(GO_READ_MODEL_PATH, 'utf8')
    const libSource = readFileSync(LIB_APPROVALS_PATH, 'utf8')
    const e2eSource = readFileSync(E2E_CLIENT_PATH, 'utf8')

    for (const { go, ts } of WIRE_STRUCTS) {
      const goKeys = goStructKeys(goSource, go)
      const libKeys = tsInterfaceKeys(libSource, ts)
      const e2eKeys = tsInterfaceKeys(e2eSource, ts)

      expect(keySetDiff(goKeys, libKeys), `${ts}: Go ${go} vs lib/approvals.ts`).toEqual([])
      expect(keySetDiff(goKeys, e2eKeys), `${ts}: Go ${go} vs e2e/api/client.ts`).toEqual([])
      expect(keySetDiff(libKeys, e2eKeys), `${ts}: lib/approvals.ts vs e2e/api/client.ts`).toEqual([])
    }
  })

  it('planted-positive: the comparator can detect a real mismatch, not just agree', () => {
    // Synthetic, in-memory only -- not read from disk. One field ('b') is present on the
    // Go side and deliberately missing on the TS side.
    const goFixture = 'type Fixture struct {\n\tA string `json:"a"`\n\tB string `json:"b"`\n}'
    const tsFixtureMissingB = 'export interface Fixture {\n  a: string\n}'

    const goKeys = goStructKeys(goFixture, 'Fixture')
    const tsKeys = tsInterfaceKeys(tsFixtureMissingB, 'Fixture')
    expect(goKeys).toEqual(['a', 'b'])
    expect(tsKeys).toEqual(['a'])

    const diff = keySetDiff(goKeys, tsKeys)
    expect(diff.length, 'the comparator must not agree on a genuinely mismatched pair').toBeGreaterThan(0)
    expect(diff, 'the missing key must surface by name, not just a boolean flag').toContain('b')
  })
})

describe('getInvoiceApprovalRun (AC-3: 404 -> null, every other error rethrows including 401)', () => {
  it('a 404 resolves null, not an error', async () => {
    stubFetch(() => errorResponse(404, 'no approval run for this invoice'))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await expect(getInvoiceApprovalRun(af, base, 'inv-1')).resolves.toBeNull()
  })

  it('a 401 still rethrows the exact caught error (not a re-wrap), and the sign-out seam still fires', async () => {
    stubFetch(() => errorResponse(401, 'unauthorized'))
    const onUnauthorized = vi.fn()
    const realAf = createAuthedFetch(() => 'tok', onUnauthorized)
    // Wraps the real authedFetch to capture the exact promise it hands back for the one
    // call getInvoiceApprovalRun makes -- proves the catch does `throw e`, not a re-wrap.
    let inner: Promise<unknown> | undefined
    const af = ((url: string, opts?: ApiFetchOptions) => {
      const p = realAf(url, opts)
      inner = p
      return p
    }) as unknown as AuthedFetch

    const outer = getInvoiceApprovalRun(af, base, 'inv-1')
    await expect(outer).rejects.toBeInstanceOf(ApiError)

    expect(inner, 'getInvoiceApprovalRun must call authedFetch, not short-circuit').toBeDefined()
    const [outerCaught, innerCaught] = await Promise.all([
      outer.catch((e: unknown) => e),
      inner!.catch((e: unknown) => e),
    ])
    expect(outerCaught, 'must rethrow the exact caught error, not a re-wrap').toBe(innerCaught)
    expect(onUnauthorized, "authedFetch's own 401 seam must still fire").toHaveBeenCalledTimes(1)
  })

  it('a 500 still rethrows as an ApiError carrying the same status', async () => {
    stubFetch(() => errorResponse(500, 'internal error'))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const caught = await getInvoiceApprovalRun(af, base, 'inv-1').catch((e: unknown) => e)

    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).status).toBe(500)
  })
})

describe('decideInvoice (AC-4: reason omitted on approve, carried verbatim on reject)', () => {
  it('approve sends no reason', async () => {
    const fetchMock = stubFetch(() => okResponse())
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await decideInvoice(af, base, 'inv-1', 'approved')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/inv-1/approvals')
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body as string)).toEqual({ decision: 'approved' })
  })

  it('reject sends the reason verbatim, untrimmed -- the server does the trimming', async () => {
    const fetchMock = stubFetch(() => okResponse())
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const reason = '  not our supplier  '

    await decideInvoice(af, base, 'inv-1', 'rejected', reason)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(init.body as string)).toEqual({ decision: 'rejected', reason })
  })
})

describe('canRejectReason (AC-3 story-level: the trim guard)', () => {
  it('rejects whitespace-only or empty strings; accepts anything with a non-whitespace character', () => {
    expect(canRejectReason('   ')).toBe(false)
    expect(canRejectReason('')).toBe(false)
    expect(canRejectReason('\t\n')).toBe(false)
    expect(canRejectReason(' x ')).toBe(true)
  })
})

// The five approvalGate rungs (internal/invoice/handlers.go:378-394), verbatim -- the
// same reasons the wire actually sends as approve_blocked_reason/reject_blocked_reason.
const APPROVAL_GATE_SENTENCES = [
  'Only an admin or a reviewer can approve or reject an invoice — ask an approver on your team.',
  'Only a validated invoice can be approved or rejected.',
  'This invoice has no approval run to decide on.',
  "This invoice's approval run is already closed.",
  "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role.",
]

describe('decisionBlockedReasons (AC-2: dedup rule)', () => {
  it('identical reasons collapse to one, for each of the five approvalGate sentences', () => {
    expect(APPROVAL_GATE_SENTENCES.length).toBe(5)
    for (const s of APPROVAL_GATE_SENTENCES) {
      expect(decisionBlockedReasons(s, s)).toEqual([s])
    }
  })

  it('divergent reasons both survive; a null side drops out; both null is empty', () => {
    expect(decisionBlockedReasons('a', 'b')).toEqual(['a', 'b'])
    expect(decisionBlockedReasons(null, 'b')).toEqual(['b'])
    expect(decisionBlockedReasons('a', null)).toEqual(['a'])
    expect(decisionBlockedReasons(null, null)).toEqual([])
  })
})

// --- APPR-13-01 (task-550) adversarial / edge coverage added at QA (Stage 4, Mode B),
// on top of the Stage-2.5 AC specs above (the 12 wire-mirror/run/decide/reason rows,
// left untouched). ---

describe('wire mirror: WIRE_STRUCTS table non-vacuity guard (QA-added)', () => {
  it('the struct table is non-empty -- an accidentally-cleared table would let the floor and equality loops above pass on zero iterations', () => {
    expect(WIRE_STRUCTS.length).toBeGreaterThan(0)
    expect(WIRE_STRUCTS.map((w) => w.ts)).toEqual([
      'ApprovalResolved',
      'ApprovalRunStep',
      'ApprovalRunDecision',
      'ApprovalRun',
    ])
  })
})

describe('getInvoiceApprovalRun: adversarial error shapes (QA-added)', () => {
  it('a 404 whose body is not valid JSON still resolves null -- isNoApprovalRun reads only the status', async () => {
    const fetchMock = vi.fn(() =>
      Promise.resolve({
        ok: false,
        status: 404,
        statusText: 'Not Found',
        json: () => Promise.reject(new SyntaxError('Unexpected end of JSON input')),
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await expect(getInvoiceApprovalRun(af, base, 'inv-1')).resolves.toBeNull()
  })

  it('an ApiError with status === null (network/malformed kind) rethrows, never resolves null', async () => {
    const networkError = new ApiError('network', 'fetch failed', null)
    const af = (() => Promise.reject(networkError)) as unknown as AuthedFetch

    await expect(getInvoiceApprovalRun(af, base, 'inv-1')).rejects.toBe(networkError)
  })

  it('a non-ApiError throw (a raw TypeError from the network layer) rethrows unwrapped, not swallowed', async () => {
    const rawError = new TypeError('Failed to fetch')
    const af = (() => Promise.reject(rawError)) as unknown as AuthedFetch

    await expect(getInvoiceApprovalRun(af, base, 'inv-1')).rejects.toBe(rawError)
  })
})

describe('decideInvoice: adversarial reason payloads (QA-added)', () => {
  it('a reason with a newline, a quote, and a multi-byte character survives byte-identical', async () => {
    const fetchMock = stubFetch(() => okResponse())
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const reason = 'Not our supplier.\nSee "invoice #42" — 日本語 ok?'

    await decideInvoice(af, base, 'inv-1', 'rejected', reason)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(JSON.parse(init.body as string)).toEqual({ decision: 'rejected', reason })
  })

  // The trim guard is canRejectReason's job (row 10), not decideInvoice's -- the caller
  // decides whether to send at all. This pins what actually goes over the wire once sent.
  it('approve genuinely OMITS the reason key from the body object -- not `reason: undefined` -- checked before any JSON serialization', async () => {
    let capturedBody: unknown
    const af = (async (_url: string, opts?: ApiFetchOptions) => {
      capturedBody = opts?.body
      return { run_id: 'r1', state: 'open', steps: [], decisions: [] }
    }) as unknown as AuthedFetch

    await decideInvoice(af, base, 'inv-1', 'approved')

    expect(capturedBody).toBeDefined()
    expect(
      Object.prototype.hasOwnProperty.call(capturedBody as object, 'reason'),
      'the body object must carry no reason key at all on approve, not an own key holding undefined',
    ).toBe(false)
    expect(Object.keys(capturedBody as object)).toEqual(['decision'])
  })
})

describe('canRejectReason: adversarial whitespace (QA-added)', () => {
  it('a non-breaking space, a tab-only string, and a newline-only string are all rejected', () => {
    expect(canRejectReason(' ')).toBe(false)
    expect(canRejectReason('\t')).toBe(false)
    expect(canRejectReason('\n')).toBe(false)
  })
})

describe('decisionBlockedReasons: adversarial dedup edges (QA-added)', () => {
  it('two sentences differing only by trailing whitespace do NOT collapse -- strict equality, not a normalized compare', () => {
    const withTrailingSpace = APPROVAL_GATE_SENTENCES[1] + ' '
    const result = decisionBlockedReasons(APPROVAL_GATE_SENTENCES[1], withTrailingSpace)

    expect(result.length, 'the pinned shipped answer is 2 -- reject === approve is strict string equality').toBe(2)
    expect(result).toEqual([APPROVAL_GATE_SENTENCES[1], withTrailingSpace])
  })

  it('one null, one present, in each order, using the real approvalGate sentences', () => {
    expect(APPROVAL_GATE_SENTENCES.length).toBe(5)
    const [first, second] = APPROVAL_GATE_SENTENCES

    expect(decisionBlockedReasons(first, null)).toEqual([first])
    expect(decisionBlockedReasons(null, second)).toEqual([second])
  })
})

// --- AUDIT-09-06: the approval state projection. approvalRunStateView survives the
// trail projection's deletion because invoiceStrip.ts's node 3 reads it.

function baseStep(overrides: Partial<ApprovalRunStep> = {}): ApprovalRunStep {
  return {
    ord: 0,
    kind: 'approval',
    state: 'pending',
    workflow_role_key: 'finance',
    workflow_role_title: 'Finance',
    holder: null,
    sla_hours: null,
    due_at: null,
    overdue: false,
    satisfied_at: null,
    satisfied_by: null,
    notify_target: null,
    notify_channel: null,
    ...overrides,
  }
}

function stateRun(steps: ApprovalRunStep[], state = 'open'): ApprovalRun {
  return {
    run_id: 'run-1',
    state,
    opened_at: '2026-08-01T00:00:00Z',
    closed_at: null,
    closed_by: null,
    steps,
    decisions: [],
  }
}

describe("pendingApprovalStep: the server's own predicate (P-14)", () => {
  it('the lowest ord wins regardless of array order', () => {
    const later = baseStep({ ord: 2, workflow_role_title: 'Tax Reviewer' })
    const earlier = baseStep({ ord: 1, workflow_role_title: 'Engagement Manager' })

    // Supplied out of ord order: a .find() that trusts array position returns `later`.
    expect(pendingApprovalStep(stateRun([later, earlier]))).toBe(earlier)
  })

  it('null on every run state but open -- a voided run keeps its pending steps (P-13)', () => {
    const steps = [baseStep({ ord: 0 })]

    // Positive control first: the identical steps DO yield the step on an open run, so the
    // nulls below are the state gate and not a predicate that never matches.
    expect(pendingApprovalStep(stateRun(steps, 'open'))).toBe(steps[0])
    for (const state of ['approved', 'rejected', 'cancelled', '', 'weird']) {
      expect(pendingApprovalStep(stateRun(steps, state)), state).toBeNull()
    }
  })

  it('kind must be approval -- a pending notify/condition/autoapprove step is not a holder', () => {
    for (const kind of ['notify', 'condition', 'autoapprove']) {
      expect(pendingApprovalStep(stateRun([baseStep({ kind })])), kind).toBeNull()
    }
    // Positive control on the same fixture axis.
    expect(pendingApprovalStep(stateRun([baseStep({ kind: 'approval' })]))).not.toBeNull()
  })

  it('a non-pending approval step is not a holder', () => {
    for (const state of ['satisfied', 'skipped', 'rejected']) {
      expect(pendingApprovalStep(stateRun([baseStep({ state })])), state).toBeNull()
    }
  })
})

describe("approvalStateView: the rail card's pure core", () => {
  it('overdue wins over a formatted due date', () => {
    const dueAt = '2026-07-01T00:00:00Z'

    const view = approvalStateView(stateRun([baseStep({ overdue: true, due_at: dueAt })]))

    expect(view.pending?.dueLabel).toBe(APPROVAL_CARD_COPY.overdue)
    expect(view.pending?.dueLabel).not.toBe(fmtDate(dueAt))
    // Positive control: the same due_at DOES format once the server stops calling it overdue.
    expect(approvalStateView(stateRun([baseStep({ overdue: false, due_at: dueAt })])).pending?.dueLabel).toBe(fmtDate(dueAt))
  })

  it('a null role title falls back to the em dash, never the string "null"', () => {
    const view = approvalStateView(stateRun([baseStep({ workflow_role_key: null, workflow_role_title: null })]))

    expect(view.pending?.roleTitle).toBe('—')
    expect(view.pending?.holderText).toBeNull()
  })

  it('holder is a passthrough, never re-derived through roles.ts (D-34)', () => {
    // withExtra's "+N" form (internal/approval/read_model.go) survives byte for byte.
    const view = approvalStateView(stateRun([baseStep({ holder: { text: 'Ada Obi +2', warn: false } })]))

    expect(view.pending?.holderText).toBe('Ada Obi +2')
    expect(view.pending?.holderWarn).toBe(false)
    expect(approvalStateView(stateRun([baseStep({ holder: { text: 'Nobody assigned', warn: true } })])).pending?.holderWarn).toBe(true)
  })

  it('voided is exactly run.state === "cancelled"', () => {
    const steps = [baseStep({ ord: 0 })]

    expect(approvalStateView(stateRun(steps, 'cancelled')).voided).toBe(true)
    for (const state of ['open', 'approved', 'rejected', 'weird']) {
      expect(approvalStateView(stateRun(steps, state)).voided, state).toBe(false)
    }
  })

  it('the state label and tone come from approvalRunStateView, and pending is null off an open run', () => {
    const steps = [baseStep({ ord: 0, workflow_role_title: 'Finance lead' })]

    const open = approvalStateView(stateRun(steps, 'open'))
    expect(open.stateLabel).toBe(APPROVAL_CARD_COPY.stateOpen)
    expect(open.stateTone).toBe('amber')
    expect(open.pending?.roleTitle).toBe('Finance lead')

    const cancelled = approvalStateView(stateRun(steps, 'cancelled'))
    expect(cancelled.stateLabel).toBe(APPROVAL_CARD_COPY.stateCancelled)
    expect(cancelled.pending).toBeNull()
  })
})

// Three assertions the deleted approvals.test.ts describes carried that arch 4.5's ledger
// does not name, re-expressed against the new projection. Each survives a shape the wire
// can still produce; none is reached by the specs above.
describe('approvalStateView: edges the retired trail projection covered (AUDIT-09-06 QA)', () => {
  it('an OPEN run with no steps at all yields no pending step, and does not throw', () => {
    // A policy whose ladder is all conditions/notifies arms an open run with no approval
    // step. Retired twin: `an empty steps array projects to an empty array, does not throw`.
    // Positive control on the same axis, first: one pending approval step DOES yield a holder.
    expect(approvalStateView(stateRun([baseStep({ ord: 0 })], 'open')).pending).not.toBeNull()

    const view = approvalStateView(stateRun([], 'open'))
    expect(view.pending, 'an empty ladder is nobody waiting, not a throw').toBeNull()
    expect(view.stateLabel).toBe(APPROVAL_CARD_COPY.stateOpen)
    expect(view.voided).toBe(false)
    expect(pendingApprovalStep(stateRun([], 'open'))).toBeNull()
  })

  it('a malformed due_at falls through to fmtDate\'s em-dash guard, never "Invalid Date"', () => {
    // approvalStateView calls fmtDate(step.due_at) unguarded; fmtDate answers the em dash
    // on a NaN date (lib/format.ts). Nothing has pinned that path since the trail
    // projection died. Positive control on the same field: a well-formed due_at formats.
    expect(approvalStateView(stateRun([baseStep({ due_at: '2026-07-01T00:00:00Z' })])).pending?.dueLabel).toBe(
      fmtDate('2026-07-01T00:00:00Z'),
    )

    const label = approvalStateView(stateRun([baseStep({ due_at: 'not-a-date' })])).pending?.dueLabel
    expect(label).toBe('\u2014')
    expect(label).not.toMatch(/Invalid/i)
  })

  it('an empty-string holder text is not the same as an absent one', () => {
    // `?? null` keeps '' as ''; the card's `holderText != null` guard then renders an empty
    // holder-name line instead of omitting it. Conflating the two is a one-character change
    // (`??` -> `||`). Retired twin of the same name.
    expect(approvalStateView(stateRun([baseStep({ holder: { text: '', warn: true } })])).pending?.holderText).toBe('')
    expect(approvalStateView(stateRun([baseStep({ holder: null })])).pending?.holderText).toBeNull()
  })
})

describe('approvalRunStateView: the four run states, plus an unknown fallback (AC-5)', () => {
  it('the four run states map to label and tone', () => {
    expect(approvalRunStateView('open')).toEqual({ label: APPROVAL_CARD_COPY.stateOpen, tone: 'amber' })
    expect(approvalRunStateView('approved')).toEqual({ label: APPROVAL_CARD_COPY.stateApproved, tone: 'green' })
    expect(approvalRunStateView('rejected')).toEqual({ label: APPROVAL_CARD_COPY.stateRejected, tone: 'red' })
    expect(approvalRunStateView('cancelled')).toEqual({ label: APPROVAL_CARD_COPY.stateCancelled, tone: 'muted' })
  })

  it('an unknown run state is muted and self-labelled', () => {
    expect(approvalRunStateView('frozen')).toEqual({ label: 'frozen', tone: 'muted' })
  })
})

// D-34: holderText is a passthrough, so the projection must never reach roles.ts for a
// Role[]/Member[] it does not receive. Reuses the LIB_APPROVALS_PATH/readFileSync pair above.
describe('lib/approvals.ts source: no import from roles.ts (AC-7, D-34)', () => {
  it('the projection does not import roles.ts', () => {
    const libSource = readFileSync(LIB_APPROVALS_PATH, 'utf8')

    expect(libSource, 'lost anchor on lib/approvals.ts').toContain('export function approvalStateView')
    expect(libSource.length).toBeGreaterThan(15000)
    expect(libSource).not.toContain("from './roles'")
  })
})

describe('approvalRunStateView: adversarial edges (QA Stage 4, Mode B)', () => {
  it('an empty string is its own unknown-state label, not conflated with an absent state', () => {
    expect(approvalRunStateView('')).toEqual({ label: '', tone: 'muted' })
  })

  it('a case-differing state is exact-match only, never case-insensitive', () => {
    expect(approvalRunStateView('Open')).toEqual({ label: 'Open', tone: 'muted' })
    expect(approvalRunStateView('Open')).not.toEqual({ label: APPROVAL_CARD_COPY.stateOpen, tone: 'amber' })
  })
})

// CodeRabbit PR #167 fix: approvalRunStateView's `known` map is a plain object literal, so
// an unguarded `map[key] ?? fallback` resolves an inherited Object.prototype member
// ('constructor', 'toString') as a hit instead of falling through. Object.hasOwn guards it.
describe('prototype-pollution guard: inherited Object.prototype keys never resolve as labels (CodeRabbit PR #167)', () => {
  it('approvalRunStateView("constructor") falls through to the raw string, not Object', () => {
    expect(approvalRunStateView('constructor')).toEqual({ label: 'constructor', tone: 'muted' })
  })

  it('approvalRunStateView("toString") falls through to the raw string', () => {
    expect(approvalRunStateView('toString')).toEqual({ label: 'toString', tone: 'muted' })
  })
})
