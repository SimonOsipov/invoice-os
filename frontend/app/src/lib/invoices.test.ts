// RED specs (M4-09-03, task-184, I1-I14) — pin the invoice list/detail data-access
// helpers, the invoiceStatusStyle pill mapper, verdictStatus, and the
// shouldFetchInvoices/invoicesViewState render-decision helpers before the executor
// implements the bodies in invoices.ts.
//
// I6-I14 mirror portfolio.test.ts's / validationApi.test.ts's `vi.stubGlobal('fetch',
// ...)` pattern: `fetch` is stubbed, but `createAuthedFetch`/`apiFetch` are the REAL
// @invoice-os/api-client + src/lib/authedFetch.ts exports, so a stubbed 200/401
// produces a genuine ApiError{kind, ...} — proof at the integration level, not a
// re-implementation of apiFetch's own contract (already covered by client.test.ts).
//
// Every spec below currently fails because listInvoices/getInvoice/getInvoiceHistory/
// editInvoice/revalidateInvoice/invoiceStatusStyle/verdictStatus/
// shouldFetchInvoices/invoicesViewState's stub bodies throw `new Error('not
// implemented')` before ever calling the real authedFetch/fetch (or, for the pure
// helpers, before returning anything) — that IS the correct RED reason (assertion /
// not-implemented), not an import/compile/setup error.
// Scoped to this file only (not the shared tsconfig.json): INV-06-T11b is this file's
// first-ever node:fs usage, and @types/node's ambient ("node:"-prefixed) module
// declarations aren't picked up automatically under this project's tsconfig without it.
/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, asyncReducer, initialState, type AsyncState } from '@invoice-os/api-client'

import { createAuthedFetch } from './authedFetch'
import type { AuthedFetch } from './portfolio'
import {
  canResolveOutside,
  clampFilterText,
  computedLineSum,
  createInvoice,
  DETAIL_SUBMIT_COPY,
  diffEditInput,
  diffLineItems,
  editInvoice,
  FAILURE_EXPLANATION_FALLBACK,
  FAILURE_KINDS,
  failureExplanation,
  formFromInvoice,
  gateByActiveEntity,
  getInvoice,
  getInvoiceHistory,
  getInvoiceUbl,
  invoiceStatusStyle,
  invoicesViewState,
  isBuyerTinMissing,
  isInFlight,
  isRowSelectable,
  keepInvoiceAsIs,
  keptAsIs,
  LIVE_POLL_MS,
  listInvoices,
  mbsPathToEditField,
  newIdempotencyKey,
  normaliseInvoiceRow,
  pruneSelection,
  reasonFieldFlags,
  rejectionProvenance,
  resolveInvoiceOutside,
  resolvedOutside,
  revalidateInvoice,
  selectableIds,
  selectAllState,
  selectBlockedReason,
  shouldFetchInvoices,
  shouldPollInvoice,
  shouldPollList,
  shouldRefreshHistory,
  shouldShowFiscalRecord,
  shouldShowRejectionCard,
  singleSubmitOutcome,
  skipReasonLabel,
  submitInvoices,
  toggleSelection,
  unresolveInvoiceOutside,
  verdictStatus,
  violationSummary,
  type BatchSubmitResultItem,
  type EditFieldKey,
  type EditFormState,
  type InvoiceApproval,
  type InvoiceCreateInput,
  type InvoiceDetailRecord,
  type InvoiceEditInput,
  type InvoiceLineItem,
  type InvoiceListResponse,
  type InvoiceRecord,
  type InvoiceStatus,
  type ListInvoicesOptions,
  type RejectionReason,
  type StatusChange,
} from './invoices'
// Namespace import, ADDITIONAL to the named import above -- INV-06-T11a below asserts
// over this whole object with the `in` operator, so it never imports the [isfixable-deleted]
// export by name and can't fail to compile either before or after that export is removed.
import * as invoicesModule from './invoices'
// This test file is a leaf (nothing imports it back), so importing reviewBatch.ts here
// alongside invoices.ts is cycle-safe even though reviewBatch.ts itself imports FROM
// invoices.ts (task-329, BUG-01-03).
import { BATCH_SUBMIT_MAX_IDS } from './reviewBatch'

interface MockResponse {
  ok: boolean
  status: number
  statusText?: string
  json: () => Promise<unknown>
  // Optional so every existing {ok,status,json} call site keeps compiling.
  text?: () => Promise<string>
}

function mockFetchOnce(response: MockResponse) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// Calls a (currently throwing) helper and returns the caught error, tolerating both a
// synchronous throw (today's stub) and an eventual async rejection — mirrors
// portfolio.test.ts's / validationApi.test.ts's captureRejection helper.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected the call to reject, but it resolved')
}

// Calls a (currently throwing) helper and swallows the failure, so assertions on the
// fetch mock below still execute pre-implementation -- client.test.ts's tryCall.
async function tryCall(thunk: () => unknown): Promise<void> {
  try {
    await thunk()
  } catch {
    // ignored — the resolve/reject contract is pinned by the rows beside this one.
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

const base = 'https://gw'

const draftInvoice: InvoiceRecord = {
  id: 'inv-1',
  entity_id: 'e1',
  import_batch_id: null,
  invoice_number: 'INV-001',
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
}

// Approval fixtures (APPR-08-09). `run_state` is the only field the predicate reads;
// the rest mirror approval.RowFacts so these stay assignable to InvoiceApproval.
const OPEN_RUN: InvoiceApproval = {
  run_state: 'open',
  pending_ord: 1,
  pending_role_title: 'Reviewer',
  pending_holder_warn: false,
  due_at: null,
  overdue: false,
}

const APPROVED_RUN: InvoiceApproval = {
  run_state: 'approved',
  pending_ord: null,
  pending_role_title: null,
  pending_holder_warn: false,
  due_at: null,
  overdue: false,
}

describe('invoiceStatusStyle', () => {
  it('I1: each of the 7 canonical states maps to a distinct, well-formed StatusStyle with an uppercased label', () => {
    const statuses: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']

    const styles = statuses.map((status) => invoiceStatusStyle(status))

    for (const style of styles) {
      expect(style.bg).toBeTruthy()
      expect(style.border).toBeTruthy()
      expect(style.text).toBeTruthy()
      expect(style.label).toBeTruthy()
      expect(style.label).toBe(style.label.toUpperCase())
    }

    const distinct = new Set(styles.map((style) => JSON.stringify(style)))
    expect(distinct.size).toBe(statuses.length)
  })

  it('I2: an unrecognized status falls back to the muted style without throwing', () => {
    expect(() => invoiceStatusStyle('bogus' as InvoiceStatus)).not.toThrow()

    const style = invoiceStatusStyle('bogus' as InvoiceStatus)

    expect(style.bg).toBe('var(--status-muted-bg)')
    expect(style.border).toBe('var(--status-muted-border)')
    expect(style.text).toBe('var(--status-muted-text)')
    expect(style.label).toBeTruthy()
  })
})

// I3/I4 (the deleted edit-availability predicate's own block) are removed with the export
// itself (INVED-01-06, [gates-on-the-wire]): the status set they pinned now lives ONLY in
// the backend, where TestCanEdit_AllStatuses (internal/invoice/transition_test.go) pins
// the same seven states against legalTransitions. INV-06-T11a/T11b below are what replace
// them here -- they assert the export is gone rather than assert over its behaviour.

describe('verdictStatus', () => {
  // Updated (M5-09-03, task-253, Core AC #5): verdictStatus gains a second parameter,
  // the invoice itself, and a new on-load derivation for the demoted-draft case
  // (task-188 item 4) -- split I5 into four cases (a-d) rather than one, since the new
  // rule has four independently meaningful branches.
  it('I5a: session staleness still wins', () => {
    const validated: InvoiceRecord = { ...draftInvoice, status: 'validated', rule_set_version_id: 'rsv-1', rule_set_version: 2 }

    expect(verdictStatus(true, validated)).toBe('stale')
  })

  it('I5b: a demoted draft is stale on load', () => {
    const demoted: InvoiceRecord = { ...draftInvoice, status: 'draft', rule_set_version_id: 'rsv-1', violations: [] }

    expect(verdictStatus(false, demoted)).toBe('stale')
  })

  it('I5c: a never-validated draft is current', () => {
    const fresh: InvoiceRecord = { ...draftInvoice, status: 'draft', rule_set_version_id: null }

    expect(verdictStatus(false, fresh)).toBe('current')
  })

  it('I5d: a draft holding an error violation is current', () => {
    const blocked: InvoiceRecord = {
      ...draftInvoice,
      status: 'draft',
      rule_set_version_id: 'rsv-1',
      violations: [{ rule_key: 'supplier-tin-required', severity: 'error', message: 'Supplier TIN is required' }],
    }

    expect(verdictStatus(false, blocked)).toBe('current')
  })
})

describe('listInvoices', () => {
  it('I6: {needsAttention:true} builds .../invoices?needs_attention=true and unwraps .invoices', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () =>
        Promise.resolve({
          invoices: [draftInvoice],
          pagination: { limit: 50, offset: 0, total: 1 },
        }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listInvoices(af, base, { needsAttention: true })

    expect(result.invoices).toEqual([draftInvoice])
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices?needs_attention=true')
    expect(init.method).toBe('GET')
  })

  it('I7: no filter omits the query string entirely, still unwraps .invoices', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listInvoices(af, base, {})

    expect(result.invoices).toEqual([])
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices')
    expect(url).not.toContain('?')
  })

  // [entity-id-restored] (persona-handoff-fix regression fix): entity_id moved from a
  // browser-side re-filter (gateByActiveEntity, née filterByActiveEntity) to a real
  // server query param, so listInvoices' own query-string construction is now the
  // load-bearing proof that a company switch actually narrows the request, not just
  // the in-browser view of it.
  it('{entityId} builds .../invoices?entity_id=... and unwraps .invoices', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [draftInvoice], pagination: { limit: 50, offset: 0, total: 1 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listInvoices(af, base, { entityId: 'entity-1' })

    expect(result.invoices).toEqual([draftInvoice])
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices?entity_id=entity-1')
  })

  it('{needsAttention:true, entityId} combines both params in one query string', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { needsAttention: true, entityId: 'entity-1' })

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    const parsed = new URL(url)
    expect(parsed.searchParams.get('needs_attention')).toBe('true')
    expect(parsed.searchParams.get('entity_id')).toBe('entity-1')
  })

  it('entityId omitted (undefined) omits ?entity_id entirely', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { entityId: undefined })

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('entity_id')
  })
})

// --- Stage 2.5 (Mode A, task-284) RED specs for the envelope + widened
// ListInvoicesOptions (AC-1). I6/I7/the entityId spec above got a one-token value-half
// edit (result -> result.invoices) forced by the envelope change; their URL-half
// assertions are untouched. `as ListInvoicesOptions` casts below are necessary and
// deliberate -- the type is not widened with the 7 new fields until Stage 3, so passing
// them through the real (unwidened) signature needs an explicit cast, not a silent `any`.
describe('listInvoices: the envelope + widened options (AC-1, Stage 2.5)', () => {
  it('LIST-1a (green-before -- already covered by I7, restated for the option table): absent options ({}) emit no query params', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, {})

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('?')
  })

  it('LIST-1b (the discriminating leg): explicitly-empty/false options (needsAttention:false, needsFix:false, importBatchIds:[\'\'], ruleKey:"", q:"") still emit no query params', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, {
      needsAttention: false,
      needsFix: false,
      importBatchIds: [''],
      ruleKey: '',
      q: '',
    } as ListInvoicesOptions)

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('?')
  })

  it('LIST-2: the envelope is returned whole -- pagination.total is reachable, not discarded', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [draftInvoice], pagination: { limit: 50, offset: 0, total: 500 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listInvoices(af, base, {})

    expect(result.pagination).toEqual({ limit: 50, offset: 0, total: 500 })
  })

  it('LIST-3: needsFix is emitted iff strictly true, mirroring the shipped needsAttention (I17) precedent', async () => {
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const falseMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { needsFix: false } as ListInvoicesOptions)
    const [falseUrl] = falseMock.mock.calls[0] as [string, RequestInit]
    expect(falseUrl).not.toContain('needs_fix')

    const trueMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { needsFix: true } as ListInvoicesOptions)
    const [trueUrl] = trueMock.mock.calls[0] as [string, RequestInit]
    expect(trueUrl).toContain('needs_fix=true')
  })

  it('LIST-KEEP-1 (INVCR-01-15, D6, task-291): keptAsIs is emitted iff strictly true, mirroring LIST-3\'s needsFix precedent', async () => {
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const falseMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { keptAsIs: false } as ListInvoicesOptions)
    const [falseUrl] = falseMock.mock.calls[0] as [string, RequestInit]
    expect(falseUrl).not.toContain('kept_as_is')

    const trueMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { keptAsIs: true } as ListInvoicesOptions)
    const [trueUrl] = trueMock.mock.calls[0] as [string, RequestInit]
    expect(trueUrl).toContain('kept_as_is=true')
  })

  it('LIST-4: offset:0 is emitted, not dropped -- the classic falsy-zero bug (`!= null`, never truthiness)', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { limit: 50, offset: 0 } as ListInvoicesOptions)

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    const parsed = new URL(url)
    expect(parsed.searchParams.get('limit')).toBe('50')
    expect(parsed.searchParams.get('offset')).toBe('0')
  })

  it('LIST-5: no client-side default -- an explicit limit/offset passes through untouched, but {} never synthesizes ?limit=50 (I7\'s guard against `opts.limit ?? 50`)', async () => {
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const explicitMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 25, offset: 10, total: 0 } }),
    })
    await listInvoices(af, base, { limit: 25, offset: 10 } as ListInvoicesOptions)
    const [explicitUrl] = explicitMock.mock.calls[0] as [string, RequestInit]
    expect(explicitUrl).toContain('limit=25')
    expect(explicitUrl).toContain('offset=10')

    const absentMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, {})
    const [absentUrl] = absentMock.mock.calls[0] as [string, RequestInit]
    expect(absentUrl).not.toContain('limit')
    expect(absentUrl).not.toContain('offset')
  })

  it('LIST-6 (BULK-02-14, AC-1): importBatchIds emits one import_batch_id param PER non-empty id, via append -- never a single overwritten value', async () => {
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const twoIdsMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { importBatchIds: ['a', 'b'] })
    const [twoIdsUrl] = twoIdsMock.mock.calls[0] as [string, RequestInit]
    const parsed = new URL(twoIdsUrl)
    expect(parsed.searchParams.getAll('import_batch_id')).toEqual(['a', 'b'])

    const emptyStringMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { importBatchIds: [''] })
    const [emptyStringUrl] = emptyStringMock.mock.calls[0] as [string, RequestInit]
    expect(emptyStringUrl).not.toContain('import_batch_id')

    const emptyArrayMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { importBatchIds: [] })
    const [emptyArrayUrl] = emptyArrayMock.mock.calls[0] as [string, RequestInit]
    expect(emptyArrayUrl).not.toContain('import_batch_id')
  })

  it('LIST-FK-1 (BUG-06-04 QA gap-fill): failure_kind surfaces per row from a real multi-row wire payload, no cross-row bleed', async () => {
    const rowWithKind = { ...draftInvoice, id: 'inv-a', status: 'failed', failure_kind: 'payload_not_built' }
    const rowWithoutKind = { ...draftInvoice, id: 'inv-b', status: 'failed', failure_kind: null }
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () =>
        Promise.resolve({ invoices: [rowWithKind, rowWithoutKind], pagination: { limit: 50, offset: 0, total: 2 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await listInvoices(af, base, {})

    expect(result.invoices[0].failure_kind).toBe('payload_not_built')
    expect(result.invoices[1].failure_kind).toBeNull()
  })
})

// --- APPR-12-01 (task-525): awaitingApproval reaches the wire client --------
describe('APPR-12-01: awaitingApproval reaches the wire client', () => {
  it('A01-1: {awaitingApproval: true} emits awaiting_approval=true on the URL', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { awaitingApproval: true })

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(new URL(url).searchParams.get('awaiting_approval')).toBe('true')
  })

  it('A01-2: {awaitingApproval: false} emits nothing -- not even the literal "awaiting_approval=false" -- paired against a true leg so the absence is not a vacuous pass', async () => {
    const af = createAuthedFetch(() => 'tok', vi.fn())

    // Positive leg first, combined with a filter that already works today: proves this
    // test's harness DOES observe the param when it reaches the wire, so the false leg's
    // absence below can't pass merely because nothing here is ever emitted.
    const trueMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { awaitingApproval: true, entityId: 'e1' })
    const [trueUrl] = trueMock.mock.calls[0] as [string, RequestInit]
    expect(trueUrl).toContain('entity_id=e1')
    expect(trueUrl).toContain('awaiting_approval=true')

    const falseMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { awaitingApproval: false, entityId: 'e1' })
    const [falseUrl] = falseMock.mock.calls[0] as [string, RequestInit]
    expect(falseUrl).toContain('entity_id=e1')
    expect(falseUrl).not.toContain('awaiting_approval')
    expect(falseUrl).not.toContain('awaiting_approval=false')
  })

  it('A01-3 (green-before guard): listInvoices(af, base, {}) still emits no ? at all', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, {})

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('?')
  })

  it('A01-4: {awaitingApproval:true, entityId, limit:25, offset:0} composes all four -- offset:0 included (the LIST-4 falsy trap)', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 25, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, {
      awaitingApproval: true,
      entityId: 'e1',
      limit: 25,
      offset: 0,
    })

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    const params = new URL(url).searchParams
    expect(params.get('awaiting_approval')).toBe('true')
    expect(params.get('entity_id')).toBe('e1')
    expect(params.get('limit')).toBe('25')
    expect(params.get('offset')).toBe('0')
  })
})

// A01-5 (task-525, architect validation addendum Gap 2): the only oracle for a doc
// comment is the source text itself -- TS interfaces are erased at runtime. Walks up/
// out from live anchors (mirrors dashboard.test.ts's rollupBucketDoc) rather than fixed
// line numbers, so edits elsewhere in the file can't silently move the scan off target.
const NUMBER_WORDS: Record<string, number> = { nine: 9, ten: 10, eleven: 11, twelve: 12 }

function numberWordsIn(text: string): number[] {
  const matches = text.toLowerCase().match(/\b(nine|ten|eleven|twelve)\b/g) ?? []
  return matches.map((w) => NUMBER_WORDS[w])
}

function readInvoicesSource(): string {
  return readFileSync(fileURLToPath(new URL('./invoices.ts', import.meta.url)), 'utf8')
}

// Live field count of ListInvoicesOptions: walks from the interface declaration to its
// closing brace, counting non-comment field-declaration lines.
function listInvoicesOptionsFieldCount(): number {
  const lines = readInvoicesSource().split('\n')
  const start = lines.findIndex((l) => l.startsWith('export interface ListInvoicesOptions {'))
  if (start < 0) return -1
  let count = 0
  for (let i = start + 1; i < lines.length; i++) {
    const line = lines[i]
    if (line.trimStart().startsWith('}')) break
    if (line.trimStart().startsWith('//')) continue
    if (/^\s+\w+\??:/.test(line)) count++
  }
  return count
}

// The doc paragraph directly above the interface (:310-311 today) -- walks up from the
// declaration while lines stay comments, same technique as dashboard.test.ts's
// rollupBucketDoc.
function listInvoicesOptionsDoc(): string {
  const lines = readInvoicesSource().split('\n')
  const anchor = lines.findIndex((l) => l.startsWith('export interface ListInvoicesOptions {'))
  if (anchor < 0) return ''
  const block: string[] = []
  for (let i = anchor - 1; i >= 0 && lines[i].trimStart().startsWith('//'); i--) block.unshift(lines[i])
  return block.join('\n')
}

// The file-header bullet for listInvoices (:21-28 today) -- a separate comment block
// from the paragraph above the interface. Starts at the bullet's own line, stops at the
// next `// - ` bullet or the first non-comment line.
function listInvoicesHeaderBulletDoc(): string {
  const lines = readInvoicesSource().split('\n')
  const start = lines.findIndex((l) => l.includes('- listInvoices:'))
  if (start < 0) return ''
  const block: string[] = [lines[start]]
  for (let i = start + 1; i < lines.length; i++) {
    const trimmed = lines[i].trimStart()
    if (!trimmed.startsWith('//')) break
    if (/^\/\/\s*-\s/.test(trimmed)) break
    block.push(lines[i])
  }
  return block.join('\n')
}

describe('A01-5 control: the anchors locate the interface and both comment blocks', () => {
  // Non-vacuity control (mirrors dashboard.test.ts's LIB-DOC control). Green today: if
  // the real assertion below ever passes because a scan read the wrong region -- or
  // nothing -- this control fails first and says so.
  it('finds the interface, the header bullet, and the doc paragraph, each with a number-word present', () => {
    expect(listInvoicesOptionsFieldCount(), 'lost anchor on the interface declaration').toBeGreaterThan(0)

    const headerDoc = listInvoicesHeaderBulletDoc()
    expect(headerDoc, 'lost anchor on the listInvoices header bullet').toContain('listInvoices')
    expect(numberWordsIn(headerDoc).length, 'control needle: a number-word must be present today').toBeGreaterThanOrEqual(1)

    const paraDoc = listInvoicesOptionsDoc()
    expect(paraDoc, 'lost anchor on the doc paragraph above the interface').toContain('filters')
    expect(
      numberWordsIn(paraDoc).length,
      'control needle: BOTH "nine filters" and "All nine" occurrences must be present today',
    ).toBeGreaterThanOrEqual(2)
  })
})

describe('A01-5: both comment blocks name the live ListInvoicesOptions field count, not a stale one', () => {
  it('the header bullet (:21) and every number-word in the doc paragraph (:310-311) equal the live field count', () => {
    const liveCount = listInvoicesOptionsFieldCount()

    const headerWords = numberWordsIn(listInvoicesHeaderBulletDoc())
    expect(headerWords.length, 'the header bullet must still name a number-word').toBeGreaterThanOrEqual(1)
    for (const n of headerWords) {
      expect(n, 'the file-header bullet (:21) must name the live field count').toBe(liveCount)
    }

    const paraWords = numberWordsIn(listInvoicesOptionsDoc())
    // Two occurrences today: "nine filters" (:310) and "All nine" (:311) -- both must be
    // caught, not just the first one a naive find-and-replace would touch (architect
    // validation addendum, Gap 1).
    expect(paraWords.length, 'both "nine filters" and "All nine" occurrences must be present').toBeGreaterThanOrEqual(2)
    for (const n of paraWords) {
      expect(n, 'the doc paragraph above the interface (:310-311) must name the live field count').toBe(liveCount)
    }
  })
})

// --- APPR-12-01 (task-525) QA adversarial coverage --------------------------
describe('APPR-12-01 adversarial (QA): awaitingApproval edge cases', () => {
  it('QA-1: {awaitingApproval: undefined} explicitly passed emits nothing, same as absent', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { awaitingApproval: undefined })

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).not.toContain('awaiting_approval')
  })

  it('QA-2: awaitingApproval + needsAttention + keptAsIs all emit independently -- none suppresses another', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { awaitingApproval: true, needsAttention: true, keptAsIs: true })

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    const params = new URL(url).searchParams
    expect(params.get('awaiting_approval')).toBe('true')
    expect(params.get('needs_attention')).toBe('true')
    expect(params.get('kept_as_is')).toBe('true')
  })

  it('QA-3: composes with status/needsFix and is unaffected by the options object\'s key declaration order', async () => {
    const mockA = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())
    await listInvoices(af, base, { awaitingApproval: true, status: 'rejected', needsFix: true })
    const [urlA] = mockA.mock.calls[0] as [string, RequestInit]

    const mockB = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    // Same options, keys declared in the opposite order -- object key order must not
    // change which params are emitted.
    await listInvoices(af, base, { needsFix: true, status: 'rejected', awaitingApproval: true })
    const [urlB] = mockB.mock.calls[0] as [string, RequestInit]

    const paramsA = new URL(urlA).searchParams
    const paramsB = new URL(urlB).searchParams
    for (const key of ['awaiting_approval', 'status', 'needs_fix']) {
      expect(paramsB.get(key), `${key} must match regardless of declaration order`).toBe(paramsA.get(key))
    }
    expect(paramsA.get('awaiting_approval')).toBe('true')
    expect(paramsA.get('status')).toBe('rejected')
    expect(paramsA.get('needs_fix')).toBe('true')
  })
})

// --- APPR-08-08 (task-499): the per-row `approval` envelope -----------------
//
// ROW-1..5 drive `normaliseInvoiceRow` directly; ROW-6 goes through `listInvoices`,
// which is the only oracle that it maps over EVERY row rather than one.

// wireRow builds an UNANNOTATED wire object and casts once, at the boundary. The
// APPROVE-1/2/3/5 specs get the same effect for free by going through the fetch mock,
// which TypeScript never checks; normaliseInvoiceRow is called directly, so the cast
// has to be explicit -- and the malformed values are the whole point of the specs.
const wireRow = (over: Record<string, unknown>): InvoiceRecord =>
  ({ ...draftInvoice, ...over }) as unknown as InvoiceRecord

// rowWithoutApproval is the OLDER-SERVER wire: the key is absent, not null.
const rowWithoutApproval = (): InvoiceRecord => {
  const clone: Record<string, unknown> = { ...draftInvoice }
  delete clone.approval
  return clone as unknown as InvoiceRecord
}

// fullApproval mirrors approval.RowFacts' six keys, all well-formed.
const fullApproval: InvoiceApproval = {
  run_state: 'open',
  pending_ord: 0,
  pending_role_title: 'Finance Lead',
  pending_holder_warn: false,
  due_at: '2026-08-20T09:00:00Z',
  overdue: false,
}

describe('normaliseInvoiceRow (APPR-08-08): the list row fail-closed approval pass', () => {
  it('ROW-1: a missing or non-object `approval` normalises to null, never undefined', () => {
    const missing = normaliseInvoiceRow(rowWithoutApproval())
    expect(missing.approval).toBeNull()
    expect(missing.approval).not.toBeUndefined()
    expect('approval' in missing).toBe(true)

    // A non-object is the same "the server did not say" case: a string/number/array
    // would otherwise be handed to a consumer that reads `.run_state` off it.
    for (const bogus of ['open', 7, [], true]) {
      expect(normaliseInvoiceRow(wireRow({ approval: bogus })).approval, `approval=${JSON.stringify(bogus)}`).toBeNull()
    }
  })

  it('ROW-2: pending_holder_warn/overdue are `=== true`, never truthy -- and a genuine true still survives', () => {
    // The G2 idiom: `1` survives `?? false` AND plain truthiness, and the STRING
    // "false" is truthy too. These two booleans drive a WARNING badge on the register,
    // so anything that is not literally true must read false.
    const hostile = normaliseInvoiceRow(
      wireRow({ approval: { ...fullApproval, pending_holder_warn: 'true', overdue: 1 } }),
    )
    expect(hostile.approval?.pending_holder_warn).toBe(false)
    expect(hostile.approval?.overdue).toBe(false)

    // The permissive control -- without it a hardcoded `false` passes the two above.
    const genuine = normaliseInvoiceRow(
      wireRow({ approval: { ...fullApproval, pending_holder_warn: true, overdue: true } }),
    )
    expect(genuine.approval?.pending_holder_warn).toBe(true)
    expect(genuine.approval?.overdue).toBe(true)
  })

  it('ROW-3: pending_role_title passes through byte-identically -- no SPA-authored fallback', () => {
    // internal/approval's roleTitle answers "Deleted role" for a role that no longer
    // exists, and read_model.go's holder copy uses an em dash (U+2014). That copy is
    // the backend's ([gates-on-the-wire]); a default authored here is exactly the
    // drift that decision forbids.
    const title = 'Finance Lead — Lagos'
    const got = normaliseInvoiceRow(wireRow({ approval: { ...fullApproval, pending_role_title: title } }))
    expect(got.approval).not.toBeNull()
    expect(got.approval?.pending_role_title).toBe(title)
  })

  it('ROW-4: the three nullable approval fields normalise to null when absent, never undefined', () => {
    const got = normaliseInvoiceRow(wireRow({ approval: { run_state: 'open' } }))
    expect(got.approval).not.toBeNull()
    expect(got.approval?.run_state).toBe('open')
    for (const key of ['pending_ord', 'pending_role_title', 'due_at'] as const) {
      expect(got.approval?.[key], key).toBeNull()
      expect(got.approval?.[key], key).not.toBeUndefined()
    }
  })

  it('ROW-5: every key the normaliser does not own is left byte-identical', () => {
    const wire = wireRow({ approval: { ...fullApproval, overdue: 1 } })
    const got = normaliseInvoiceRow(wire)

    expect(Object.keys(got).sort()).toEqual(Object.keys(wire).sort())
    // The three the normaliser DOES own are skipped, not asserted byte-identical:
    // A09-11 (APPR-12-09) owns the approve pair, ROW-1..4 own `approval`. Leaving them
    // in this loop would make its claim silently false the moment either is touched.
    for (const key of Object.keys(wire)) {
      if (key === 'approval' || key === 'can_approve' || key === 'approve_blocked_reason') continue
      expect(got[key as keyof InvoiceRecord], key).toEqual(wire[key as keyof InvoiceRecord])
    }
  })

  it('ROW-6: listInvoices normalises EVERY row, not just the first', () => {
    // A single-row shortcut (`invoices[0]`) or a normaliser applied only to `.find(...)`
    // would pass every case above and still ship raw wire on rows 2..n.
    const envelope = {
      invoices: [
        { ...draftInvoice, id: 'inv-a', approval: { ...fullApproval, overdue: 1 } },
        { ...draftInvoice, id: 'inv-b', approval: undefined },
        { ...draftInvoice, id: 'inv-c', approval: { run_state: 'open' } },
      ],
      pagination: { limit: 50, offset: 0, total: 3 },
    }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(envelope) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    return listInvoices(af, base, {}).then((result) => {
      expect(result.invoices).toHaveLength(3)
      expect(result.invoices[0].approval?.overdue).toBe(false)
      expect(result.invoices[1].approval).toBeNull()
      expect(result.invoices[2].approval?.pending_ord).toBeNull()
      expect(result.invoices[2].approval?.pending_role_title).toBeNull()
      expect(result.invoices[2].approval?.due_at).toBeNull()
    })
  })

  // ROW-7 pins a DECISION, not an accident (QA, task-499). `run_state` is the one
  // approval field with no fallback: the five others are enumerated in the plan's
  // fail-closed list and this one is not, so the shipped behaviour had to be either
  // chosen or corrected. It is chosen.
  //
  // A `?? ''` here would be an SPA-authored default [gates-on-the-wire] forbids, and it
  // buys nothing: '' is as un-matchable as undefined against any real state, so a
  // consumer branching on `run_state === 'open'` reads false either way -- the fallback
  // would only disguise a malformed wire as a well-formed one. Nulling the whole object
  // instead would silently reclassify a malformed row as "no approval run", a positive
  // claim no spec pins.
  //
  // What makes the pass-through sound is that the Go side structurally cannot omit the
  // key: RowFacts.RunState is a plain string with no omitempty, asserted through this
  // very handler by TestListHandler_NonNullApprovalAlwaysCarriesRunState. Change either
  // side and both tests must move together.
  it('ROW-7: run_state passes through untouched -- no `?? \'\'`, and a malformed row is not re-classified', () => {
    // Any state the backend sends survives byte-identical, including one this SPA has
    // never heard of -- there is no allow-list here, deliberately.
    for (const state of ['open', 'approved', 'rejected', 'cancelled', 'some_future_state']) {
      const got = normaliseInvoiceRow(wireRow({ approval: { ...fullApproval, run_state: state } }))
      expect(got.approval?.run_state, state).toBe(state)
    }

    // The pinned decision: a wire object with no `run_state` keeps its object shape and
    // reads undefined. The Go wire cannot produce this; the test exists so that changing
    // the answer is a deliberate edit rather than a silent one.
    const malformed = normaliseInvoiceRow(wireRow({ approval: { pending_ord: 1 } }))
    expect(malformed.approval, 'a malformed approval must NOT be nulled -- that would claim "no approval run"').not.toBeNull()
    expect(malformed.approval?.run_state).toBeUndefined()
    expect(malformed.approval?.run_state, 'an SPA-authored \'\' would disguise a malformed wire as a well-formed one').not.toBe('')
    // The other five fields still normalise, so the pass-through is scoped to this key.
    expect(malformed.approval?.pending_ord).toBe(1)
    expect(malformed.approval?.pending_role_title).toBeNull()
    expect(malformed.approval?.pending_holder_warn).toBe(false)
    expect(malformed.approval?.due_at).toBeNull()
    expect(malformed.approval?.overdue).toBe(false)
  })
})

// --- APPR-12-09 (task-526): can_approve / approve_blocked_reason on the LIST row ---
//
// A09-11 is the RUNTIME oracle for the two new normalisation lines; A09-12 is the tsc +
// source oracle for the type having MOVED to InvoiceRecord rather than being copied.
//
// The hazard is stated verbatim at the APPROVE-1/2/3/5 block above: normaliseInvoiceRow
// returns `{ ...raw, approval }` and `raw` is already typed InvoiceRecord, so OMITTING
// either new line COMPILES and tsc reports nothing. There is no list-side equivalent of
// APPROVE-1/2/3/5 today -- ROW-2 hardcodes pending_holder_warn/overdue and ROW-4 loops
// three approval sub-keys, so neither reaches these two. This block is that equivalent,
// and DELETING either normalisation line reds it.

// approveFlagsOn reads the two keys off a row without depending on InvoiceRecord already
// declaring them: A09-12 owns the type move, this block owns the runtime behaviour, and
// keeping them separate means a tsc failure in one cannot mask the other.
const approveFlagsOn = (row: InvoiceRecord) =>
  row as unknown as { can_approve: unknown; approve_blocked_reason: unknown }

// The OLDER-SERVER wire: both keys absent, not null. Written as a delete rather than an
// omission so it stays honest once draftInvoice itself carries them.
const rowWithoutApproveKeys = (): InvoiceRecord => {
  const clone: Record<string, unknown> = { ...draftInvoice }
  delete clone.can_approve
  delete clone.approve_blocked_reason
  return clone as unknown as InvoiceRecord
}

describe('normaliseInvoiceRow (APPR-12-09): the list row fails CLOSED on can_approve', () => {
  it('A09-11a: can_approve is `=== true`, never truthy -- and a genuine true survives', () => {
    // `1` survives `?? false` AND plain truthiness; the STRING "false" is truthy too.
    // This flag gates the queue's Approve button, so anything not literally true denies.
    for (const hostile of [undefined, null, 'true', 'false', 1, 0, {}, []]) {
      const got = approveFlagsOn(normaliseInvoiceRow(wireRow({ can_approve: hostile })))
      expect(got.can_approve, `can_approve=${JSON.stringify(hostile)}`).toBe(false)
    }

    // An ABSENT key must normalise to false and be PRESENT afterwards -- undefined is the
    // fail-open shape [gates-on-the-wire] exists to remove.
    const older = normaliseInvoiceRow(rowWithoutApproveKeys())
    expect(approveFlagsOn(older).can_approve).toBe(false)
    expect(approveFlagsOn(older).can_approve).not.toBeUndefined()
    expect('can_approve' in older).toBe(true)

    // The permissive control -- without it a hardcoded `false` passes everything above.
    expect(approveFlagsOn(normaliseInvoiceRow(wireRow({ can_approve: true }))).can_approve).toBe(true)
  })

  it('A09-11b: approve_blocked_reason passes through byte-identically, null when absent', () => {
    // approvalGate's rung 5 (internal/invoice/handlers.go), verbatim -- em dash U+2014.
    // The SPA has no authority over this copy; a fallback authored here is the drift
    // [gates-on-the-wire] forbids.
    const reasonText =
      "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role."
    const got = approveFlagsOn(
      normaliseInvoiceRow(wireRow({ can_approve: false, approve_blocked_reason: reasonText })),
    )
    expect(got.approve_blocked_reason).toBe(reasonText)

    for (const absent of [undefined, null]) {
      const normalised = approveFlagsOn(normaliseInvoiceRow(wireRow({ approve_blocked_reason: absent })))
      expect(normalised.approve_blocked_reason, `approve_blocked_reason=${JSON.stringify(absent)}`).toBeNull()
      expect(normalised.approve_blocked_reason).not.toBeUndefined()
    }

    const older = normaliseInvoiceRow(rowWithoutApproveKeys())
    expect(approveFlagsOn(older).approve_blocked_reason).toBeNull()
    expect('approve_blocked_reason' in older).toBe(true)
  })

  it('A09-11c: each key reads its OWN wire key, never a neighbour', () => {
    // A normalisation line copied from its neighbour and half-edited is the likeliest way
    // these two go wrong, and tsc cannot see it (APPROVE-7's argument, list side).
    const got = approveFlagsOn(
      normaliseInvoiceRow(
        wireRow({ can_approve: true, approve_blocked_reason: 'a stale reason the server still sent' }),
      ),
    )
    expect(got.can_approve).toBe(true)
    expect(got.approve_blocked_reason).toBe('a stale reason the server still sent')
  })
})

// interfaceLines returns one interface's declaration lines from invoices.ts, comments and
// blanks stripped. Anchored on the `export interface` line and the closing brace at column
// zero, so edits elsewhere in the file cannot move the scan off target (A01-5's idiom).
function interfaceLines(name: string): string[] {
  const lines = readInvoicesSource().split('\n')
  const start = lines.findIndex((l) => l.startsWith(`export interface ${name} `) || l.startsWith(`export interface ${name}{`))
  if (start < 0) return []
  const out: string[] = []
  for (let i = start + 1; i < lines.length; i++) {
    if (lines[i].startsWith('}')) break
    const trimmed = lines[i].trim()
    if (!trimmed || trimmed.startsWith('//')) continue
    out.push(trimmed)
  }
  return out
}

const fieldNamesOf = (name: string): string[] =>
  interfaceLines(name).map((l) => l.split(':')[0].replace('?', '').trim())

describe('APPR-12-09 A09-12: the approve pair MOVED to InvoiceRecord, it was not duplicated', () => {
  it('control: both interfaces are located and still carry their own anchors', () => {
    // Non-vacuity needle (A01-5's control idiom): if either scan below reads the wrong
    // region -- or nothing -- this fails first and says so.
    expect(fieldNamesOf('InvoiceRecord'), 'lost anchor on InvoiceRecord').toContain('approval')
    expect(fieldNamesOf('InvoiceDetailRecord'), 'lost anchor on InvoiceDetailRecord').toContain('qr_png_base64')
    // U5a: the REJECT pair stays detail-only, so it is also the control proving the
    // "absent from InvoiceDetailRecord" assertions below are about the approve pair
    // specifically and not about an empty scan.
    expect(fieldNamesOf('InvoiceDetailRecord')).toContain('can_reject')
    expect(fieldNamesOf('InvoiceDetailRecord')).toContain('reject_blocked_reason')
  })

  it('both keys are declared on InvoiceRecord, REQUIRED and nullable-typed', () => {
    const lines = interfaceLines('InvoiceRecord')
    // REQUIRED, never `?`: an optional key lets a consumer read undefined and treat "the
    // server did not say" as an open question (invoices.ts's own rule for every action key).
    expect(lines, 'can_approve must be declared on InvoiceRecord, without `?`').toContain('can_approve: boolean')
    expect(lines, 'approve_blocked_reason must be declared on InvoiceRecord, without `?`').toContain(
      'approve_blocked_reason: string | null',
    )
  })

  it('InvoiceDetailRecord INHERITS them and no longer redeclares them', () => {
    const detail = fieldNamesOf('InvoiceDetailRecord')
    expect(detail, 'can_approve must be inherited via Omit<InvoiceRecord,\'approval\'>, not redeclared').not.toContain(
      'can_approve',
    )
    expect(detail, 'approve_blocked_reason must be inherited, not redeclared').not.toContain('approve_blocked_reason')
  })

  it('tsc: the two keys are reachable off InvoiceRecord and off InvoiceDetailRecord alike', () => {
    // Pick<> over a key the interface does not declare is a COMPILE error, so this row is
    // the typecheck half of the move -- red under `pnpm typecheck` until the keys land.
    const onBase: Pick<InvoiceRecord, 'can_approve' | 'approve_blocked_reason'> = {
      can_approve: false,
      approve_blocked_reason: null,
    }
    const onDetail: Pick<InvoiceDetailRecord, 'can_approve' | 'approve_blocked_reason'> = {
      can_approve: true,
      approve_blocked_reason: null,
    }
    expect(onBase.can_approve).toBe(false)
    expect(onBase.approve_blocked_reason).toBeNull()
    expect(onDetail.can_approve).toBe(true)
  })
})

describe('violationSummary (AC-2, Stage 2.5)', () => {
  it('SUMMARY-1: unwraps .rules and preserves the server order verbatim, never re-sorted', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () =>
        Promise.resolve({
          rules: [
            { rule_key: 'zzz-rule', invoices: 5 },
            { rule_key: 'aaa-rule', invoices: 5 },
          ],
        }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await violationSummary(af, base, ['batch-1'])

    expect(result).toEqual([
      { rule_key: 'zzz-rule', invoices: 5 },
      { rule_key: 'aaa-rule', invoices: 5 },
    ])
  })

  it('ERR-1b: a 500 from violationSummary rejects ApiError{status:500} unchanged, not wrapped or swallowed', async () => {
    mockFetchOnce({
      ok: false,
      status: 500,
      statusText: 'Internal Server Error',
      json: () => Promise.reject(new Error('no body')),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => violationSummary(af, base, ['batch-1']))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(500)
  })
})

describe('getInvoice', () => {
  it('I8: rule_set_version:null AND the key omitted both normalize to null', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ ...draftInvoice, rule_set_version: null }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const withExplicitNull = await getInvoice(af, base, 'inv-1')
    expect(withExplicitNull.rule_set_version).toBeNull()

    const { rule_set_version: _omitted, ...withoutKey } = draftInvoice
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(withoutKey) })

    const withMissingKey = await getInvoice(af, base, 'inv-1')
    expect(withMissingKey.rule_set_version).toBeNull()
  })

  it('I9: rule_set_version:2 passes through unchanged; GET .../invoices/{id}', async () => {
    const validatedInvoice: InvoiceRecord = { ...draftInvoice, status: 'validated', rule_set_version: 2 }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(validatedInvoice) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.rule_set_version).toBe(2)
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/inv-1')
  })

  it('I-fiscal-1: getInvoice surfaces irn/csid/qr_payload/rejection_reasons verbatim', async () => {
    const reasons: RejectionReason[] = [{ code: 'invalid_tin', message: 'TIN failed validation', path: 'buyer.tin' }]
    const fiscalInvoice: InvoiceDetailRecord = {
      ...draftInvoice,
      status: 'accepted',
      irn: 'IRN-001',
      csid: 'CSID-001',
      qr_payload: 'payload-string',
      rejection_reasons: reasons,
      rule_set_version: 2,
      qr_png_base64: 'aGVsbG8=',
      // accepted: canEdit false, canRevalidate false -> the blocked reason is null (it is
      // non-null EXACTLY when canEdit && !canRevalidate, i.e. validated/rejected).
      // Submit's rule is weaker and does NOT mirror that: submit_blocked_reason != null
      // implies !can_submit, never the converse. This is the approver's shape; a preparer
      // gets the role sentence here with can_edit still false.
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
    }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(fiscalInvoice) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.irn).toBe('IRN-001')
    expect(result.csid).toBe('CSID-001')
    expect(result.qr_payload).toBe('payload-string')
    expect(result.rejection_reasons).toEqual(reasons)
  })

  it('I-fiscal-2: getInvoice normalizes a missing qr_png_base64 to null', async () => {
    // draftInvoice is status 'draft': canEdit and canRevalidate both true, reason null.
    // The can_submit/submit_blocked_reason pair below is synthetic filler for this
    // qr_png_base64 test -- no real wire pairs can_submit:false with a null reason on draft.
    const detailInvoice: InvoiceDetailRecord = {
      ...draftInvoice,
      rule_set_version: 2,
      qr_png_base64: 'aGVsbG8=',
      can_edit: true,
      can_revalidate: true,
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
    }
    const { qr_png_base64: _omittedQr, ...withoutQrKey } = detailInvoice
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(withoutQrKey) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.qr_png_base64).toBeNull()
  })

  it('getInvoice: can_submit true passes through', async () => {
    const detailInvoice: InvoiceDetailRecord = {
      ...draftInvoice,
      status: 'validated',
      rule_set_version: 2,
      qr_png_base64: null,
      can_edit: true,
      can_revalidate: false,
      revalidate_blocked_reason: 'Validated invoices cannot be re-validated.',
      can_submit: true,
      submit_blocked_reason: null,
      can_view_ubl: true,
      ubl_blocked_reason: null,
      can_resolve_outside: false,
      resolve_outside_blocked_reason: null,
      can_approve: false,
      approve_blocked_reason: null,
      can_reject: false,
      reject_blocked_reason: null,
    }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(detailInvoice) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_submit).toBe(true)
    expect(result.can_edit).toBe(true)
    expect(result.can_revalidate).toBe(false)
  })

  it('getInvoice: a stringly-typed "false" can_submit is denied', async () => {
    const detailInvoice = {
      ...draftInvoice,
      status: 'validated',
      rule_set_version: 2,
      qr_png_base64: null,
      can_edit: true,
      can_revalidate: false,
      revalidate_blocked_reason: null,
      can_submit: 'false', // non-boolean truthy: mutation oracle for `=== true` over `?? false`
    }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(detailInvoice) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_submit).toBe(false)
    expect(result.can_edit).toBe(true)
  })

  it('getInvoice: a wire missing can_submit fails closed', async () => {
    const detailInvoice: InvoiceDetailRecord = {
      ...draftInvoice,
      status: 'validated',
      rule_set_version: 2,
      qr_png_base64: null,
      can_edit: true,
      can_revalidate: false,
      revalidate_blocked_reason: null,
      can_submit: true,
      submit_blocked_reason: null,
      can_view_ubl: true,
      ubl_blocked_reason: null,
      can_resolve_outside: false,
      resolve_outside_blocked_reason: null,
      can_approve: false,
      approve_blocked_reason: null,
      can_reject: false,
      reject_blocked_reason: null,
    }
    const { can_submit: _omittedCanSubmit, ...withoutCanSubmit } = detailInvoice
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(withoutCanSubmit) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_submit).toBe(false)
  })

  it('getInvoice: submit_blocked_reason passes through byte-identically', async () => {
    const reasonText = 'Only validated invoices can be submitted — re-validate this invoice first.'
    const wire = { ...draftInvoice, can_edit: true, can_submit: false, submit_blocked_reason: reasonText }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.submit_blocked_reason).toBe(reasonText)
  })

  // The ROLE refusal, not a status one: notApproverTransmitReason (handlers.go) is the
  // only blocked reason a preparer ever sees, and the em dash below is U+2014 copied from
  // that const -- a hyphen here would make the byte-identity claim vacuous.
  it('getInvoice: the role sentence passes through byte-identically', async () => {
    const roleReason = 'Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team.'
    const wire = { ...draftInvoice, status: 'validated', can_edit: true, can_submit: false, submit_blocked_reason: roleReason }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.submit_blocked_reason).toBe(roleReason)
    expect(result.can_submit).toBe(false)
  })

  // submitGate's role arm fires on EVERY status, including ones where can_edit is false --
  // so the wire carries the sentence on queued/accepted too, and getInvoice must not drop
  // it just because the SPA will have no actions bar to render it in.
  it('getInvoice: the role sentence survives a status where can_edit is false', async () => {
    const roleReason = 'Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team.'
    const wire = { ...draftInvoice, status: 'queued', can_edit: false, can_submit: false, submit_blocked_reason: roleReason }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.submit_blocked_reason).toBe(roleReason)
    expect(result.can_edit).toBe(false)
  })

  // APPR-08-05: submit_blocked_reason gains a THIRD source -- awaitingApprovalReason
  // (handlers.go), emitted when an approver views a validated invoice whose run is still
  // open. Em dash is U+2014, copied from that const.
  it('getInvoice: the awaiting-approval sentence passes through byte-identically', async () => {
    const approvalReason = 'This invoice is waiting on approval — it can be submitted once an approver approves it.'
    const wire = { ...draftInvoice, status: 'validated', can_edit: true, can_submit: false, submit_blocked_reason: approvalReason }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.submit_blocked_reason).toBe(approvalReason)
    expect(result.can_submit).toBe(false)
  })

  // The complement to the stringly-typed 'false' case above: `?? false` would let a
  // stringly-typed "true" through as truthy, and an approval-blocked wire is exactly where
  // that matters -- it would re-enable a Submit button the server just refused.
  it('getInvoice: a stringly-typed "true" can_submit is denied', async () => {
    const wire = { ...draftInvoice, status: 'validated', can_edit: true, can_submit: 'true', submit_blocked_reason: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_submit).toBe(false)
  })

  // MUTATION ORACLE (QA): every other reason fixture in this file is already tidy, so a
  // `?.trim()` / `.normalize()` / whitespace-collapse slipped into getInvoice's normalizer
  // survives the whole suite. Pass-through means the bytes, not the visible words.
  it('getInvoice: a submit reason keeps its padding, NBSP and doubled spaces byte-for-byte', async () => {
    // Escapes, not pasted characters: a literal NBSP/tab is invisible to the next reader.
    const untidy = '  Only an admin or a reviewer can submit\u00a0 an invoice to NRS/MBS \u2014 ask an approver on your team.\t'
    const wire = { ...draftInvoice, status: 'validated', can_edit: true, can_submit: false, submit_blocked_reason: untidy }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.submit_blocked_reason).toBe(untidy)
  })

  it('getInvoice: a wire missing submit_blocked_reason normalizes to null', async () => {
    const wire = { ...draftInvoice, can_edit: true, can_submit: false }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.submit_blocked_reason).toBeNull()
    expect(result.submit_blocked_reason).not.toBeUndefined()
  })

  // The companion to the missing-key case above: an approver on a non-editable status gets
  // can_submit:false with an explicit null reason. Nothing may fill that hole -- the SPA has
  // no authority to author a sentence the server declined to send.
  it('getInvoice: an explicit null submit_blocked_reason stays null, never a substitute sentence', async () => {
    const wire = { ...draftInvoice, status: 'queued', can_edit: false, can_submit: false, submit_blocked_reason: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.submit_blocked_reason).toBeNull()
    // Positive companion: the call really did return a parsed record, so the null above
    // is the wire's null and not an empty/failed result.
    expect(result.status).toBe('queued')
  })

  it('getInvoice: failure_kind (BUG-06-04 QA gap-fill) surfaces a stored kind from the real wire payload, not just a compiled fixture', async () => {
    const wire = { ...draftInvoice, status: 'failed', failure_kind: 'acknowledged_no_verdict' }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.failure_kind).toBe('acknowledged_no_verdict')
  })

  it('getInvoice: a non-failed invoice carries failure_kind:null, never a stale leftover value', async () => {
    const wire = { ...draftInvoice, status: 'validated', failure_kind: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.failure_kind).toBeNull()
  })

  // RED specs (task-400, BUG-04-04, Mode A) -- can_view_ubl / ubl_blocked_reason join the
  // existing fail-closed normalization. getInvoice spreads the wire today, so G2/G3/G5
  // (the coercion + missing-key rows) fail; G1/G4/G6 pin the passthrough the added lines
  // must not break -- G6 is the oracle for `||` written where `??` belongs.
  it('G1: can_view_ubl passes through when literally true', async () => {
    const wire = { ...draftInvoice, can_view_ubl: true, ubl_blocked_reason: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_view_ubl).toBe(true)
    expect(result.ubl_blocked_reason).toBeNull()
  })

  it('G2: a stringly-typed can_view_ubl is denied', async () => {
    // Unannotated literal (the can_submit idiom above): a non-boolean truthy the wire
    // might carry. Mutation oracle for `=== true` over `?? false` / plain truthiness.
    const wire = { ...draftInvoice, can_view_ubl: 'true', ubl_blocked_reason: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_view_ubl).toBe(false)
  })

  it('G3: a wire missing can_view_ubl fails closed', async () => {
    const wire: InvoiceDetailRecord = {
      ...draftInvoice,
      rule_set_version: 2,
      qr_png_base64: null,
      can_edit: true,
      can_revalidate: true,
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
    }
    const { can_view_ubl: _omittedCanViewUbl, ...withoutCanViewUbl } = wire
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(withoutCanViewUbl) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_view_ubl).toBe(false)
    expect(result.can_view_ubl).not.toBeUndefined()
  })

  it('G4: ubl_blocked_reason passes through byte-identically', async () => {
    // internal/invoice/ubl.go's ublBlockedPrefix + "at least one line item." -- em dash
    // U+2014. The SPA has no authority over this copy.
    const reasonText = 'This invoice cannot be rendered as a UBL document — it is missing at least one line item.'
    const wire = { ...draftInvoice, can_view_ubl: false, ubl_blocked_reason: reasonText }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.ubl_blocked_reason).toBe(reasonText)
  })

  it('G5: a missing ubl_blocked_reason normalizes to null', async () => {
    const wire = { ...draftInvoice, can_view_ubl: true }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.ubl_blocked_reason).toBeNull()
    expect(result.ubl_blocked_reason).not.toBeUndefined()
  })

  it('G6: an empty-string reason is preserved, not coerced', async () => {
    // Unreachable from the real server (ublBlockedReason returns nil, never ""), and
    // exactly the mutation oracle for `||` written in place of `??`.
    const wire = { ...draftInvoice, can_view_ubl: false, ubl_blocked_reason: '' }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.ubl_blocked_reason).toBe('')
  })

  // QA pass (task-400, Mode B). G1-G6 pin each field alone; G7 pins the COMBINATION the
  // server never emits but a dropped key makes representable -- blocked with no reason.
  // The contract is that the two fields normalize INDEPENDENTLY: the record reads
  // {false, null}, and no SPA-authored reason is invented to fill the gap.
  it('G7: blocked with a missing reason stays blocked, and no reason is invented', async () => {
    const wire = { ...draftInvoice, can_view_ubl: false }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_view_ubl).toBe(false)
    expect(result.ubl_blocked_reason).toBeNull()
  })

  // The other half of the same combination: allowed WITH a reason. `can_view_ubl` must not
  // be derived from the reason's presence (nor the reason cleared because the gate is open)
  // -- each key is passed through as sent.
  it('G8: an allowed gate carrying a reason keeps both values as sent', async () => {
    const wire = { ...draftInvoice, can_view_ubl: true, ubl_blocked_reason: 'stale reason' }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_view_ubl).toBe(true)
    expect(result.ubl_blocked_reason).toBe('stale reason')
  })

  // --- APPR-08-06 (task-504): can_approve / can_reject fail-closed normalization ---
  //
  // These specs exist because `getInvoice` returns `{ ...res, ... }` and `res` is already
  // typed `InvoiceDetailRecord`: OMITTING the four explicit normalization lines COMPILES,
  // and tsc reports nothing. Without them the four keys bypass the fail-closed convention
  // entirely and arrive as whatever the wire carried. Only a runtime spec catches that,
  // which is what each `it` below is.

  it('APPROVE-1: a non-boolean truthy can_approve is denied', async () => {
    // Unannotated literal (the G2 idiom): the mutation oracle for `=== true` over
    // `?? false` / plain truthiness. `1` survives both of those.
    const wire = { ...draftInvoice, can_approve: 1, approve_blocked_reason: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_approve).toBe(false)
  })

  it('APPROVE-2: a stringly-typed can_reject is denied', async () => {
    // The nastiest wire value there is: the STRING "true" is truthy, and even the
    // string "false" would be. Approve/reject are the two most destructive buttons on
    // the screen, so anything that is not literally `true` must deny.
    const wire = { ...draftInvoice, can_reject: 'true', reject_blocked_reason: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_reject).toBe(false)
  })

  it('APPROVE-3: a wire missing either boolean fails closed, never undefined', async () => {
    const af = createAuthedFetch(() => 'tok', vi.fn())

    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ ...draftInvoice, can_reject: true }) })
    const missingApprove = await getInvoice(af, base, 'inv-1')
    expect(missingApprove.can_approve).toBe(false)
    expect(missingApprove.can_approve).not.toBeUndefined()

    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ ...draftInvoice, can_approve: true }) })
    const missingReject = await getInvoice(af, base, 'inv-1')
    expect(missingReject.can_reject).toBe(false)
    expect(missingReject.can_reject).not.toBeUndefined()

    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ ...draftInvoice, can_approve: undefined, can_reject: undefined }) })
    const undef = await getInvoice(af, base, 'inv-1')
    expect(undef.can_approve).toBe(false)
    expect(undef.can_reject).toBe(false)
  })

  it('APPROVE-4: a genuine true survives -- the normalization is not a hardcoded false', async () => {
    const wire = { ...draftInvoice, can_approve: true, approve_blocked_reason: null, can_reject: true, reject_blocked_reason: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_approve).toBe(true)
    expect(result.can_reject).toBe(true)
  })

  it('APPROVE-5: both reasons normalize to null when absent, never undefined', async () => {
    const wire = { ...draftInvoice, can_approve: false, can_reject: false }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.approve_blocked_reason).toBeNull()
    expect(result.approve_blocked_reason).not.toBeUndefined()
    expect(result.reject_blocked_reason).toBeNull()
    expect(result.reject_blocked_reason).not.toBeUndefined()
  })

  it('APPROVE-6: both reasons pass through byte-identically', async () => {
    // internal/invoice/handlers.go's approvalGate rung 5, verbatim -- em dash U+2014.
    // The SPA has no authority over this copy ([gates-on-the-wire]); a fallback string
    // authored here is exactly the drift that decision forbids.
    const reasonText =
      "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role."
    const wire = {
      ...draftInvoice,
      can_approve: false,
      approve_blocked_reason: reasonText,
      can_reject: false,
      reject_blocked_reason: reasonText,
    }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.approve_blocked_reason).toBe(reasonText)
    expect(result.reject_blocked_reason).toBe(reasonText)
  })

  // APPR-08-06 QA (Mode B): APPROVE-6 gives both reasons the SAME string, so a
  // normalizer that reads one wire key into the other key's slot survives it. Every
  // other key here is distinct too -- a normalizer line copied from its neighbour and
  // half-edited is the likeliest way these four go wrong, and tsc cannot see it.
  it('APPROVE-7: each key reads its OWN wire key, never a neighbour', async () => {
    const wire = {
      ...draftInvoice,
      can_approve: true,
      approve_blocked_reason: 'approve reason',
      can_reject: false,
      reject_blocked_reason: 'reject reason',
      can_edit: false,
      can_submit: false,
      submit_blocked_reason: 'submit reason',
      can_view_ubl: false,
      ubl_blocked_reason: 'ubl reason',
      can_resolve_outside: false,
      resolve_outside_blocked_reason: 'resolve reason',
      can_revalidate: false,
      revalidate_blocked_reason: 'revalidate reason',
    }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_approve).toBe(true)
    expect(result.can_reject).toBe(false)
    expect(result.approve_blocked_reason).toBe('approve reason')
    expect(result.reject_blocked_reason).toBe('reject reason')
    // The neighbours the four could plausibly be cross-wired to, pinned in the same
    // response so a swap anywhere in the block shows up here.
    expect(result.submit_blocked_reason).toBe('submit reason')
    expect(result.ubl_blocked_reason).toBe('ubl reason')
    expect(result.resolve_outside_blocked_reason).toBe('resolve reason')
    expect(result.revalidate_blocked_reason).toBe('revalidate reason')
  })

  // The mirror of APPROVE-7 with the two booleans flipped: a cross-wire that happens
  // to agree on one fixture cannot agree on both.
  it('APPROVE-8: the two booleans are independent, in both directions', async () => {
    const af = createAuthedFetch(() => 'tok', vi.fn())

    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ ...draftInvoice, can_approve: false, can_reject: true }),
    })
    const rejectOnly = await getInvoice(af, base, 'inv-1')
    expect(rejectOnly.can_approve).toBe(false)
    expect(rejectOnly.can_reject).toBe(true)

    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ ...draftInvoice, can_approve: true, can_reject: false }),
    })
    const approveOnly = await getInvoice(af, base, 'inv-1')
    expect(approveOnly.can_approve).toBe(true)
    expect(approveOnly.can_reject).toBe(false)
  })
})

// RED specs (task-400, BUG-04-04, Mode A) -- getInvoiceUbl is a throwing stub, so every
// row below fails on the throw or on the assertion it guards, not on a missing import.
describe('getInvoiceUbl (task-400, BUG-04-04)', () => {
  const UBL_XML =
    '<?xml version="1.0" encoding="UTF-8"?>\n<Invoice><cbc:ID>INV &amp; Co</cbc:ID><cbc:Note>Ångström</cbc:Note></Invoice>'
  const REASON = 'This invoice cannot be rendered as a UBL document — it is missing at least one line item.'
  const notJSON = () => Promise.reject(new SyntaxError('not JSON'))

  it('U1: requests the ubl route with the id percent-encoded', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, text: () => Promise.resolve(UBL_XML), json: notJSON })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await tryCall(() => getInvoiceUbl(af, base, 'a/b'))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/a%2Fb/ubl')
  })

  it('U2: resolves the server bytes unmodified', async () => {
    mockFetchOnce({ ok: true, status: 200, text: () => Promise.resolve(UBL_XML), json: notJSON })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoiceUbl(af, base, 'inv-1')

    expect(result).toBe(UBL_XML)
  })

  it('U3: sends the bearer token and takes the text path', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, text: () => Promise.resolve(UBL_XML), json: notJSON })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await tryCall(() => getInvoiceUbl(af, base, 'inv-1'))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    const headers = new Headers(init.headers)
    expect(headers.get('Authorization')).toBe('Bearer tok')
  })

  it('U4: a 409 rejects with ApiError{status:409} carrying the reason byte-identically', async () => {
    mockFetchOnce({
      ok: false,
      status: 409,
      statusText: 'Conflict',
      json: () => Promise.resolve({ error: REASON }),
      text: () => Promise.resolve(JSON.stringify({ error: REASON })),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => getInvoiceUbl(af, base, 'inv-1'))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.status).toBe(409)
    expect(apiErr.message).toBe(REASON)
  })

  it('U5: a 404 is distinguishable from a 409 and carries no reason copy', async () => {
    mockFetchOnce({
      ok: false,
      status: 404,
      statusText: 'Not Found',
      json: () => Promise.resolve({ error: 'not found' }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => getInvoiceUbl(af, base, 'inv-1'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(404)
    expect((err as ApiError).message).toBe('not found')
  })

  it('U6: keeps the 401 sign-out seam', async () => {
    // The one row a bare-fetch implementation fails ([ubl-fetch-through-authedfetch]).
    mockFetchOnce({
      ok: false,
      status: 401,
      statusText: 'Unauthorized',
      json: () => Promise.resolve({ error: 'unauthorized' }),
    })
    const onUnauthorized = vi.fn()
    const af = createAuthedFetch(() => 'tok', onUnauthorized)

    const err = await captureRejection(() => getInvoiceUbl(af, base, 'inv-1'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('U7: a 500 rejects and yields no document', async () => {
    mockFetchOnce({
      ok: false,
      status: 500,
      statusText: 'Internal Server Error',
      json: () => Promise.resolve({ error: 'internal server error' }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => getInvoiceUbl(af, base, 'inv-1'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(500)
  })

  // QA pass (task-400, Mode B) -- U8-U11 project the client-layer text-path edges onto the
  // helper the viewer actually calls.
  it("U8: a 2xx whose body stream fails rejects with ApiError('malformed'), not a raw TypeError", async () => {
    // json resolves, so a helper that never took the text path would return an object here
    // instead of rejecting -- the row is red on a missing text branch too.
    mockFetchOnce({
      ok: true,
      status: 200,
      text: () => Promise.reject(new TypeError('stream aborted')),
      json: () => Promise.resolve({ a: 1 }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => getInvoiceUbl(af, base, 'inv-1'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('malformed')
    expect((err as ApiError).status).toBe(200)
  })

  it('U9: an empty document resolves the empty string rather than null', async () => {
    // Unreachable from ubl.go, but BUG-04-06's viewer must not be handed null for it.
    mockFetchOnce({ ok: true, status: 200, text: () => Promise.resolve(''), json: notJSON })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoiceUbl(af, base, 'inv-1')

    expect(result).toBe('')
    expect(result).not.toBeNull()
  })

  it('U10: non-ASCII document bytes survive the helper verbatim', async () => {
    // Naira sign, CJK and an explicitly DECOMPOSED e + U+0301 -- no normalize, no re-encode.
    const doc = '<Invoice><cbc:Note>\u20a61,000 \u767c\u7968 e\u0301</cbc:Note></Invoice>'
    mockFetchOnce({ ok: true, status: 200, text: () => Promise.resolve(doc), json: notJSON })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoiceUbl(af, base, 'inv-1')

    expect(result).toBe(doc)
    expect(doc.normalize('NFC')).not.toBe(doc)
  })

  it('U11: sends no request body and no Accept header', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, text: () => Promise.resolve(UBL_XML), json: notJSON })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await getInvoiceUbl(af, base, 'inv-1')

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.method).toBe('GET')
    expect(init.body).toBeUndefined()
    expect(new Headers(init.headers).has('Accept')).toBe(false)
  })
})

describe('getInvoiceHistory', () => {
  it('I10: the bare StatusChange[] array passes through unchanged; GET .../invoices/{id}/history', async () => {
    const history: StatusChange[] = [
      { from_status: null, to_status: 'draft', actor: 'system', actor_name: 'System', actor_kind: 'system', changed_at: '2026-07-01T00:00:00Z' },
      { from_status: 'draft', to_status: 'validated', actor: 'user:u1', actor_name: 'user:u1', actor_kind: 'raw', changed_at: '2026-07-02T00:00:00Z' },
    ]
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(history) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoiceHistory(af, base, 'inv-1')

    expect(result).toEqual(history)
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/inv-1/history')
  })
})

describe('editInvoice', () => {
  it('I11: PATCH .../invoices/{id} with only the changed field(s) in the body', async () => {
    const updated: InvoiceRecord = { ...draftInvoice, supplier_tin: 'x' }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(updated) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await editInvoice(af, base, 'inv-1', { supplier_tin: 'x' })

    expect(result).toEqual(updated)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/inv-1')
    expect(init.method).toBe('PATCH')
    expect(init.body).toBe(JSON.stringify({ supplier_tin: 'x' }))
  })
})

describe('keepInvoiceAsIs (INVCR-01-15, D6, task-291)', () => {
  it('KEEP-INV-1: POST .../invoices/{id}/keep-as-is with {reason} as the whole body', async () => {
    const kept: InvoiceRecord = {
      ...draftInvoice,
      kept_as_is_at: '2026-07-31T00:00:00Z',
      kept_as_is_by: 'user-1',
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(kept) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await keepInvoiceAsIs(af, base, 'inv-1', 'Buyer confirmed the discrepancy is intentional.')

    expect(result).toEqual(kept)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/inv-1/keep-as-is')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(JSON.stringify({ reason: 'Buyer confirmed the discrepancy is intentional.' }))
  })

  it('KEEP-INV-2: a 409 (clean invoice / not draft) rejects with the ApiError untouched', async () => {
    mockFetchOnce({
      ok: false,
      status: 409,
      statusText: 'Conflict',
      json: () => Promise.resolve({ error: 'invoice must be a draft with a blocking violation to be kept as-is' }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => keepInvoiceAsIs(af, base, 'inv-1', 'some reason'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(409)
  })
})

describe('resolveInvoiceOutside / unresolveInvoiceOutside / canResolveOutside / resolvedOutside', () => {
  it('RESOLVE-INV-1: POST body is {reason} only', async () => {
    const resolved: InvoiceRecord = {
      ...draftInvoice,
      status: 'failed',
      kept_as_is_at: '2026-08-01T00:00:00Z',
      kept_as_is_by: 'user-1',
      kept_as_is_reason: 'Filed manually through the NRS portal.',
    }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(resolved) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await resolveInvoiceOutside(af, base, 'inv-1', 'Filed manually through the NRS portal.')

    expect(result).toEqual(resolved)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/inv-1/resolved-outside')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(JSON.stringify({ reason: 'Filed manually through the NRS portal.' }))
  })

  it('RESOLVE-INV-2: DELETE sends no body', async () => {
    const unresolved: InvoiceRecord = {
      ...draftInvoice,
      status: 'failed',
      kept_as_is_at: null,
      kept_as_is_by: null,
      kept_as_is_reason: null,
    }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(unresolved) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await unresolveInvoiceOutside(af, base, 'inv-1')

    expect(result).toEqual(unresolved)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/inv-1/resolved-outside')
    expect(init.method).toBe('DELETE')
    expect(init.body).toBeUndefined()
  })

  it('RESOLVE-INV-3: normalizer fails closed', async () => {
    const wire: InvoiceDetailRecord = {
      ...draftInvoice,
      status: 'failed',
      rule_set_version: null,
      qr_png_base64: null,
      can_edit: false,
      can_revalidate: false,
      revalidate_blocked_reason: null,
      can_submit: false,
      submit_blocked_reason: null,
      can_view_ubl: false,
      ubl_blocked_reason: null,
      can_resolve_outside: true,
      resolve_outside_blocked_reason: null,
      can_approve: false,
      approve_blocked_reason: null,
      can_reject: false,
      reject_blocked_reason: null,
    }
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const { can_resolve_outside: _omitted, ...withoutKey } = wire
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(withoutKey) })
    const absent = await getInvoice(af, base, 'inv-1')
    expect(absent.can_resolve_outside).toBe(false)

    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ ...wire, can_resolve_outside: undefined }) })
    const undef = await getInvoice(af, base, 'inv-1')
    expect(undef.can_resolve_outside).toBe(false)

    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ ...wire, can_resolve_outside: 'true' }) })
    const stringly = await getInvoice(af, base, 'inv-1')
    expect(stringly.can_resolve_outside).toBe(false)
  })

  it('RESOLVE-INV-4: reason passes through verbatim', async () => {
    const reasonText = 'Filed via the NRS portal — receipt #4471.  '
    const wire = { ...draftInvoice, status: 'failed', can_resolve_outside: false, resolve_outside_blocked_reason: reasonText }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.resolve_outside_blocked_reason).toBe(reasonText)
  })

  it('RESOLVE-INV-5: canResolveOutside requires a reason', () => {
    const cases: Array<[string, boolean]> = [
      ['', false],
      ['   ', false],
      ['\t\n', false],
      ['a real reason', true],
      ['  padded  ', true],
    ]

    const results = cases.map(([reason]) => canResolveOutside(reason))

    expect(results).toEqual(cases.map(([, expected]) => expected))
  })

  it('RESOLVE-INV-6: resolvedOutside is status-gated', () => {
    const failedMarked = resolvedOutside({
      status: 'failed',
      kept_as_is_at: '2026-08-01T00:00:00Z',
      kept_as_is_by: 'user-1',
      kept_as_is_reason: 'Filed manually through the NRS portal.',
    })
    expect(failedMarked).not.toBeNull()

    const draftMarked = resolvedOutside({
      status: 'draft',
      kept_as_is_at: '2026-08-01T00:00:00Z',
      kept_as_is_by: 'user-1',
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    })
    expect(draftMarked).toBeNull()

    const failedUnmarked = resolvedOutside({
      status: 'failed',
      kept_as_is_at: null,
      kept_as_is_by: null,
      kept_as_is_reason: null,
    })
    expect(failedUnmarked).toBeNull()
  })

  it('RESOLVE-INV-7: a non-2xx response rejects with the ApiError untouched', async () => {
    mockFetchOnce({
      ok: false,
      status: 409,
      statusText: 'Conflict',
      json: () => Promise.resolve({ error: 'invoice is not in a failed state' }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => resolveInvoiceOutside(af, base, 'inv-1', 'some reason'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(409)
  })

  it('RESOLVE-INV-8: unresolveInvoiceOutside on a non-2xx response rejects with the ApiError untouched', async () => {
    mockFetchOnce({
      ok: false,
      status: 404,
      statusText: 'Not Found',
      json: () => Promise.resolve({ error: 'invoice not found' }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => unresolveInvoiceOutside(af, base, 'inv-1'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(404)
  })

  it('RESOLVE-INV-9: resolvedOutside is null for every non-failed status, not just draft', () => {
    const nonFailed: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected']

    for (const status of nonFailed) {
      expect(
        resolvedOutside({
          status,
          kept_as_is_at: '2026-08-01T00:00:00Z',
          kept_as_is_by: 'user-1',
          kept_as_is_reason: 'Filed manually through the NRS portal.',
        }),
      ).toBeNull()
    }
  })

  it('RESOLVE-INV-10: resolvedOutside accepts both a list-row (InvoiceRecord) and a detail-row (InvoiceDetailRecord) call shape', () => {
    // A future list-marker caller passes a bare listInvoices() row; a future detail-banner
    // caller passes a full getInvoice() row. Pick<InvoiceRecord, ...> must satisfy both --
    // this pins that it does, not just that the narrow fields do.
    const listRow: InvoiceRecord = {
      ...draftInvoice,
      status: 'failed',
      kept_as_is_at: '2026-08-01T00:00:00Z',
      kept_as_is_by: 'user-1',
      kept_as_is_reason: 'Filed manually through the NRS portal.',
    }
    const detailRow: InvoiceDetailRecord = {
      ...listRow,
      rule_set_version: null,
      qr_png_base64: null,
      can_edit: false,
      can_revalidate: false,
      revalidate_blocked_reason: null,
      can_submit: false,
      submit_blocked_reason: null,
      can_view_ubl: false,
      ubl_blocked_reason: null,
      can_resolve_outside: false,
      resolve_outside_blocked_reason: null,
      can_approve: false,
      approve_blocked_reason: null,
      can_reject: false,
      reject_blocked_reason: null,
    }

    const fromList = resolvedOutside(listRow)
    const fromDetail = resolvedOutside(detailRow)

    expect(fromList).toEqual({ at: '2026-08-01T00:00:00Z', by: 'user-1', reason: 'Filed manually through the NRS portal.' })
    expect(fromDetail).toEqual(fromList)
  })
})

// RED specs (task-392, BUG-03-03, Mode A) -- keptAsIs is a throwing stub today, so every
// case below fails on the throw, not on a missing import.
describe('keptAsIs (task-392, BUG-03-03)', () => {
  it("returns null on an un-kept invoice (kept_as_is_at/_by/_reason all null, matching detailRecord()'s defaults)", () => {
    expect(keptAsIs({ kept_as_is_at: null, kept_as_is_by: null, kept_as_is_reason: null })).toBeNull()
  })

  it('surfaces the persisted at/by/reason verbatim', () => {
    const result = keptAsIs({
      kept_as_is_at: '2026-07-31T00:00:00Z',
      kept_as_is_by: 'c0000000-0000-0000-0000-000000000001',
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    })

    expect(result).toEqual({
      at: '2026-07-31T00:00:00Z',
      by: 'c0000000-0000-0000-0000-000000000001',
      reason: 'Buyer confirmed the discrepancy is intentional.',
    })
  })

  // No fabricated reason: `by`/`reason` null pass through as null, never a placeholder
  // string, even though `at` alone means "kept" -- the all-or-nothing CHECK constraint is
  // server-side only, this helper must not paper over a row that somehow violates it.
  it('never fabricates a reason when at is set but by/reason are null', () => {
    const result = keptAsIs({ kept_as_is_at: '2026-07-31T00:00:00Z', kept_as_is_by: null, kept_as_is_reason: null })

    expect(result).toEqual({ at: '2026-07-31T00:00:00Z', by: null, reason: null })
  })
})

describe('createInvoice', () => {
  it('CREATE-1 posts to the invoice create route with the body verbatim', async () => {
    const body: InvoiceCreateInput = {
      entity_id: 'e1',
      invoice_number: 'INV-2026-00482',
      issue_date: '2026-06-16T00:00:00Z',
      supplier_tin: '12345678-0001',
      supplier_name: 'Lagos Freight Ltd',
      buyer_tin: '00000000002',
      buyer_name: 'Beta Ltd',
      currency: 'NGN',
      subtotal: '3300.47',
      vat: '247.54',
      total: '3548.01',
      line_items: [
        { description: 'Logistics consulting', quantity: '2', unit_price: '1500.25', line_total: '3000.50', line_tax: null },
        { description: 'Warehousing', quantity: '3', unit_price: '99.99', line_total: '299.97', line_tax: null },
      ],
    }
    const created: InvoiceRecord = {
      ...draftInvoice,
      id: 'inv-new',
      entity_id: 'e1',
      invoice_number: body.invoice_number,
      supplier_tin: body.supplier_tin,
      supplier_name: body.supplier_name,
    }
    const fetchMock = mockFetchOnce({ ok: true, status: 201, json: () => Promise.resolve(created) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await createInvoice(af, base, body)

    expect(result).toEqual(created)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(JSON.stringify(body))
    // I18-style fidelity: exactly the passed keys, no undefined/extra keys injected.
    const sentBody: unknown = JSON.parse(init.body as string)
    expect(Object.keys(sentBody as object).sort()).toEqual(Object.keys(body).sort())
  })

  it('CREATE-2 a 400 rejects with the ApiError untouched', async () => {
    mockFetchOnce({
      ok: false,
      status: 400,
      statusText: 'Bad Request',
      json: () => Promise.resolve({ error: 'entity_id is required' }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const body: InvoiceCreateInput = {
      entity_id: '',
      invoice_number: 'INV-2026-00482',
      issue_date: null,
      supplier_tin: null,
      supplier_name: null,
      buyer_tin: null,
      buyer_name: null,
      currency: 'NGN',
      subtotal: null,
      vat: null,
      total: null,
      line_items: [],
    }

    const err = await captureRejection(() => createInvoice(af, base, body))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(400)
    expect((err as ApiError).message).toBe('entity_id is required')
  })
})

describe('revalidateInvoice', () => {
  it('I12: POST .../invoices/{id}/validate with no body', async () => {
    const validated: InvoiceRecord = { ...draftInvoice, status: 'validated', rule_set_version: 3 }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(validated) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await revalidateInvoice(af, base, 'inv-1')

    expect(result).toEqual(validated)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/inv-1/validate')
    expect(init.method).toBe('POST')
    expect(init.body).toBeUndefined()
  })
})

describe('submitInvoices', () => {
  it('I-submit-1: submitInvoices posts ids + key and unwraps results', async () => {
    const results: BatchSubmitResultItem[] = [
      { invoice_id: 'a', enqueued: true, status: 'queued' },
      { invoice_id: 'b', enqueued: false, status: 'validated', reason: 'not_validated' },
    ]
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ results }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await submitInvoices(af, base, ['a', 'b'], 'k')

    expect(result).toEqual(results)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/submissions')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(JSON.stringify({ invoice_ids: ['a', 'b'], idempotency_key: 'k' }))

    // BatchSubmitResultItem's keys, pinned (addendum A3): `reason` is `omitempty` on the
    // Go side -- genuinely ABSENT (undefined) on an enqueued item, never "".
    const enqueuedItem = result[0]
    expect(Object.keys(enqueuedItem).sort()).toEqual(['enqueued', 'invoice_id', 'status'])
    expect(enqueuedItem.invoice_id).toBe('a')
    expect(enqueuedItem.enqueued).toBe(true)
    expect(enqueuedItem.status).toBe('queued')
    expect(enqueuedItem.reason).toBeUndefined()
  })

  it('I-submit-2: submitInvoices propagates ApiError unchanged', async () => {
    mockFetchOnce({
      ok: false,
      status: 400,
      statusText: 'Bad Request',
      json: () => Promise.resolve({ error: 'invoice_ids is required' }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => submitInvoices(af, base, [], 'k'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(400)
  })
})

describe('newIdempotencyKey', () => {
  it('I-key-1: newIdempotencyKey returns a fresh 36-char uuid', () => {
    const a = newIdempotencyKey()
    const b = newIdempotencyKey()

    expect(a).toHaveLength(36)
    expect(b).toHaveLength(36)
    expect(a.length).toBeLessThanOrEqual(218)
    expect(a).not.toBe(b)
  })
})

describe('mbsPathToEditField', () => {
  it('I-path-1: the nine MBS paths map to their edit fields', () => {
    const table: Array<[string, EditFieldKey]> = [
      ['issue_date', 'issue_date'],
      ['currency', 'currency'],
      ['subtotal', 'subtotal'],
      ['vat', 'vat'],
      ['total', 'total'],
      ['supplier.tin', 'supplier_tin'],
      ['supplier.name', 'supplier_name'],
      ['buyer.tin', 'buyer_tin'],
      ['buyer.name', 'buyer_name'],
    ]

    for (const [path, field] of table) {
      expect(mbsPathToEditField(path), `path=${path}`).toBe(field)
    }
  })

  it('I-path-2: unmapped and absent paths return null, never swallow the reason', () => {
    expect(mbsPathToEditField('invoice_number')).toBeNull()
    expect(mbsPathToEditField('line_items[0].unit_price')).toBeNull()
    expect(mbsPathToEditField('')).toBeNull()
    expect(mbsPathToEditField(undefined)).toBeNull()
    // The APP's own vocabulary (synthesized error body only) must never appear in the
    // mapping table.
    expect(mbsPathToEditField('customer.taxIdentifier')).toBeNull()
  })
})

describe('skipReasonLabel', () => {
  it('I-skip-1: skipReasonLabel maps all three reachable reasons and passes others through', () => {
    expect(skipReasonLabel('not_validated')).toBe('Not validated — validate it first')
    expect(skipReasonLabel('duplicate_request')).toBe('Already submitted with this request')
    expect(skipReasonLabel('awaiting_approval')).toBe(
      'This invoice is waiting on approval — it can be submitted once an approver approves it.',
    )
    expect(skipReasonLabel('wat')).toBe('wat')

    // Three tokens, three distinct non-empty sentences: a map collapsed onto one shared
    // string would still satisfy the three assertions above once they all name it.
    const labels = ['not_validated', 'duplicate_request', 'awaiting_approval'].map(skipReasonLabel)
    expect(labels).toHaveLength(3)
    expect(labels.filter((l) => l.length > 0)).toHaveLength(3)
    expect(new Set(labels).size).toBe(3)
  })

  // The server owns this sentence: awaitingApprovalReason, internal/invoice/handlers.go. Its
  // Go half (TestAwaitingApprovalReason_MatchesTheSPASkipLabel) never runs on a frontend-only
  // PR -- CI's `go` path filter excludes frontend/**.
  it('the awaiting-approval label is the server sentence, byte for byte', () => {
    expect(skipReasonLabel('awaiting_approval')).toBe(
      'This invoice is waiting on approval — it can be submitted once an approver approves it.',
    )
    expect(skipReasonLabel('not_validated')).not.toBe(skipReasonLabel('awaiting_approval'))
  })
})

describe('singleSubmitOutcome', () => {
  it('singleSubmitOutcome: an enqueued item for this invoice is queued', () => {
    const items: BatchSubmitResultItem[] = [{ invoice_id: 'inv-1', enqueued: true, status: 'queued' }]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result).toEqual({ kind: 'queued' })
  })

  it('singleSubmitOutcome: not_validated is skipped, not queued', () => {
    const items: BatchSubmitResultItem[] = [
      { invoice_id: 'inv-1', enqueued: false, status: 'validated', reason: 'not_validated' },
    ]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result).toEqual({ kind: 'skipped', message: skipReasonLabel('not_validated') })
  })

  it('singleSubmitOutcome: duplicate_request is skipped even though the item reports status queued', () => {
    // status:'queued' here is the trap -- enqueued is what decides, never status.
    const items: BatchSubmitResultItem[] = [
      { invoice_id: 'inv-1', enqueued: false, status: 'queued', reason: 'duplicate_request' },
    ]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result.kind).not.toBe('queued')
    expect(result).toEqual({ kind: 'skipped', message: skipReasonLabel('duplicate_request') })
  })

  // The two cases above use skipReasonLabel as their own oracle, so neither can catch the
  // label moving. This one spells the server sentence.
  it('singleSubmitOutcome: an awaiting_approval item carries the server sentence verbatim', () => {
    const items: BatchSubmitResultItem[] = [
      { invoice_id: 'inv-1', enqueued: false, status: 'validated', reason: 'awaiting_approval' },
    ]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result).toEqual({
      kind: 'skipped',
      message: 'This invoice is waiting on approval — it can be submitted once an approver approves it.',
    })
  })

  it('singleSubmitOutcome: a non-boolean truthy enqueued is not queued', () => {
    const items: BatchSubmitResultItem[] = [
      { invoice_id: 'inv-1', enqueued: 'false', status: 'validated' } as unknown as BatchSubmitResultItem,
    ]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result.kind).toBe('skipped')
    if (result.kind === 'skipped') {
      expect(result.message).toBe(DETAIL_SUBMIT_COPY.notQueued)
    }
  })

  it('singleSubmitOutcome: an undefined results array is unresolved', () => {
    const result = singleSubmitOutcome('inv-1', undefined)

    expect(result.kind).toBe('unresolved')
    if (result.kind === 'unresolved') {
      expect(result.message).not.toBe('undefined')
    }
  })

  it('singleSubmitOutcome: a result naming a different invoice is unresolved', () => {
    const items: BatchSubmitResultItem[] = [{ invoice_id: 'other', enqueued: true, status: 'queued' }]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result.kind).toBe('unresolved')
    expect(result.kind).not.toBe('queued')
  })

  it('singleSubmitOutcome: a skipped item with no reason gets the neutral label', () => {
    const items: BatchSubmitResultItem[] = [{ invoice_id: 'inv-1', enqueued: false, status: 'validated' }]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result.kind).toBe('skipped')
    if (result.kind === 'skipped') {
      expect(result.message).toBe(DETAIL_SUBMIT_COPY.notQueued)
      expect(result.message).not.toBe('undefined')
    }
  })
})

describe('singleSubmitOutcome: adversarial coverage', () => {
  it('picks the item naming this invoice, ignoring an enqueued item for another invoice', () => {
    const items: BatchSubmitResultItem[] = [
      { invoice_id: 'other-1', enqueued: true, status: 'queued' },
      { invoice_id: 'inv-1', enqueued: false, status: 'validated', reason: 'not_validated' },
      { invoice_id: 'other-2', enqueued: false, status: 'validated', reason: 'not_validated' },
    ]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result).toEqual({ kind: 'skipped', message: skipReasonLabel('not_validated') })
  })

  it('a malformed response repeating this invoice id resolves the first match (skip then enqueue)', () => {
    const items: BatchSubmitResultItem[] = [
      { invoice_id: 'inv-1', enqueued: false, status: 'validated', reason: 'not_validated' },
      { invoice_id: 'inv-1', enqueued: true, status: 'queued' },
    ]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result).toEqual({ kind: 'skipped', message: skipReasonLabel('not_validated') })
  })

  it('a malformed response repeating this invoice id resolves the first match (enqueue then skip)', () => {
    const items: BatchSubmitResultItem[] = [
      { invoice_id: 'inv-1', enqueued: true, status: 'queued' },
      { invoice_id: 'inv-1', enqueued: false, status: 'validated', reason: 'not_validated' },
    ]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result).toEqual({ kind: 'queued' })
  })

  it('an empty results array is unresolved, distinct from an undefined results array', () => {
    const result = singleSubmitOutcome('inv-1', [])

    expect(result.kind).toBe('unresolved')
    if (result.kind === 'unresolved') {
      expect(result.message).toBe(DETAIL_SUBMIT_COPY.unresolved)
    }
  })

  it('enqueued: null is not queued', () => {
    const items = [
      { invoice_id: 'inv-1', enqueued: null, status: 'validated' },
    ] as unknown as BatchSubmitResultItem[]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result.kind).toBe('skipped')
    if (result.kind === 'skipped') {
      expect(result.message).toBe(DETAIL_SUBMIT_COPY.notQueued)
      expect(result.message).not.toBe('undefined')
    }
  })

  it('a matching item with enqueued absent entirely is not queued', () => {
    const items = [{ invoice_id: 'inv-1', status: 'validated' }] as unknown as BatchSubmitResultItem[]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result.kind).toBe('skipped')
    if (result.kind === 'skipped') {
      expect(result.message).toBe(DETAIL_SUBMIT_COPY.notQueued)
    }
  })

  it('an unknown reason code surfaces the reason itself, never undefined or the neutral label', () => {
    const items: BatchSubmitResultItem[] = [
      { invoice_id: 'inv-1', enqueued: false, status: 'validated', reason: 'app_unreachable' },
    ]

    const result = singleSubmitOutcome('inv-1', items)

    expect(result.kind).toBe('skipped')
    if (result.kind === 'skipped') {
      expect(result.message).toBe('app_unreachable')
      expect(result.message).not.toBe('undefined')
      expect(result.message).not.toBe(DETAIL_SUBMIT_COPY.notQueued)
    }
  })
})

describe('DETAIL_SUBMIT_COPY', () => {
  it('DETAIL_SUBMIT_COPY matches the acceptance criterion verbatim', () => {
    expect(`${DETAIL_SUBMIT_COPY.prompt} ${DETAIL_SUBMIT_COPY.detail}`).toBe(
      'Send this invoice for transmission? Nothing here can pull it back.',
    )
  })
})

describe('selection helpers', () => {
  it('I-sel-1: only validated rows with no open approval run are selectable', () => {
    const statuses: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']
    const rows: InvoiceRecord[] = statuses.map((status, i) => ({ ...draftInvoice, id: `inv-${i}`, status }))

    // draftInvoice carries approval: null, so these rows keep their original meaning.
    expect(selectableIds(rows)).toEqual(['inv-1'])
    // EXTENDED (APPR-08-09): the status dimension crossed with the approval fact.
    // 7 x 3 = 21 cases, of which exactly two are true (validated+null, validated+approved).
    for (const row of rows) {
      for (const approval of [null, OPEN_RUN, APPROVED_RUN]) {
        const expected = row.status === 'validated' && approval?.run_state !== 'open'
        expect(
          isRowSelectable({ ...row, approval }),
          `status=${row.status} run_state=${approval?.run_state ?? 'null'}`,
        ).toBe(expected)
      }
    }
  })

  it('I-sel-2: toggleSelection adds then removes', () => {
    const afterAdd = toggleSelection([], 'a')
    expect(afterAdd).toEqual(['a'])

    const afterRemove = toggleSelection(afterAdd, 'a')
    expect(afterRemove).toEqual([])
  })

  it('I-sel-3: pruneSelection drops departed and no-longer-validated ids', () => {
    const rows: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated' },
      { ...draftInvoice, id: 'b', status: 'queued' },
    ]

    expect(pruneSelection(['a', 'b', 'c'], rows)).toEqual(['a'])
  })
})

describe('selectAllState', () => {
  it('S-1: no selectable rows renders none, never a vacuously-checked all', () => {
    // LOAD-BEARING EDGE (addendum A9a): Array.prototype.every() over an empty array is
    // vacuously true, so a naive selectAllState would render a CHECKED select-all on a
    // page with zero selectable rows.
    const rows: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'draft' },
      { ...draftInvoice, id: 'b', status: 'queued' },
    ]

    expect(selectAllState([], rows)).toBe('none')
  })

  it('S-2: every selectable id selected is all', () => {
    const rows: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated' },
      { ...draftInvoice, id: 'b', status: 'validated' },
    ]

    expect(selectAllState(['a', 'b'], rows)).toBe('all')
  })

  it('S-3: a strict non-empty subset is some', () => {
    const rows: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated' },
      { ...draftInvoice, id: 'b', status: 'validated' },
    ]

    expect(selectAllState(['a'], rows)).toBe('some')
  })

  it('S-4: a selection holding only a stale id is never all, even when lengths coincidentally match', () => {
    const rows: InvoiceRecord[] = [{ ...draftInvoice, id: 'a', status: 'validated' }]

    expect(selectAllState(['stale-id'], rows)).not.toBe('all')
  })
})

// RED specs (APPR-08-09, task-500, Stage 2.5/Mode A) — isRowSelectable must read the
// row's approval fact, not just its status, so an awaiting-approval invoice cannot be
// batch-selected into a submit the server would only skip. isRowSelectable's body is
// still `row.status === 'validated'` (its `// stub` marker), so every spec below that
// involves an open run fails on its assertion, never on an import or compile error.
describe('isRowSelectable reads the approval fact (APPR-08-09)', () => {
  it('A-sel-1: a validated row with an open run is NOT selectable', () => {
    expect(isRowSelectable({ ...draftInvoice, status: 'validated', approval: OPEN_RUN })).toBe(false)
  })

  it('A-sel-2: a validated row with an approved run IS selectable', () => {
    expect(isRowSelectable({ ...draftInvoice, status: 'validated', approval: APPROVED_RUN })).toBe(true)
  })

  it('A-sel-3: a validated row with no approval run at all IS selectable (AC #5, an unarmed tenant is unchanged)', () => {
    expect(isRowSelectable({ ...draftInvoice, status: 'validated', approval: null })).toBe(true)
  })

  it('A-sel-4: selectableIds excludes awaiting-approval rows from a mixed page', () => {
    const rows: InvoiceRecord[] = [
      { ...draftInvoice, id: 'clear', status: 'validated', approval: null },
      { ...draftInvoice, id: 'awaiting', status: 'validated', approval: OPEN_RUN },
      { ...draftInvoice, id: 'approved', status: 'validated', approval: APPROVED_RUN },
      { ...draftInvoice, id: 'draft', status: 'draft', approval: OPEN_RUN },
    ]

    expect(selectableIds(rows)).toEqual(['clear', 'approved'])
  })

  it('A-sel-5: pruneSelection drops an id whose row came back from the server with an open run', () => {
    const before: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated', approval: null },
      { ...draftInvoice, id: 'b', status: 'validated', approval: null },
    ]
    // The same selection survives the pre-fetch rows -- so the drop below is the approval
    // fact biting, not an id that merely left the page.
    expect(pruneSelection(['a', 'b'], before)).toEqual(['a', 'b'])

    const afterRefetch: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated', approval: OPEN_RUN },
      { ...draftInvoice, id: 'b', status: 'validated', approval: null },
    ]

    expect(pruneSelection(['a', 'b'], afterRefetch)).toEqual(['b'])
  })

  it('A-sel-6: selectAllState reports none on a page where every row is awaiting approval', () => {
    const rows: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated', approval: OPEN_RUN },
      { ...draftInvoice, id: 'b', status: 'validated', approval: OPEN_RUN },
    ]

    // The selectableIds leg is load-bearing (QA Stage 4, task-500): `selectAllState([], …)`
    // alone returns 'none' via `matched === 0` for ANY page, so it passed under the
    // status-only stub too. A-sel-7 is the other discriminator.
    expect(selectableIds(rows)).toEqual([])
    expect(selectAllState([], rows)).toBe('none')
  })

  it("A-sel-7: selectAllState is none even when every open-run id sits in the selection -- a stale selection can't inflate past the empty guard", () => {
    const rows: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated', approval: OPEN_RUN },
      { ...draftInvoice, id: 'b', status: 'validated', approval: OPEN_RUN },
    ]

    expect(selectAllState(['a', 'b'], rows)).toBe('none')
  })
})

// FAIL-OPEN, pinned as a recorded decision rather than an accident (APPR-08-09, B5).
// `InvoiceApproval.run_state` is a bare `string` with no union, and normaliseInvoiceRow
// passes it through untouched ([gates-on-the-wire]), so anything that is not exactly
// 'open' -- a typo, a case variant, an absent key -- reads as SELECTABLE. Accepted: the
// column is CHECK-constrained, and the SERVER gate is authoritative (a wrongly-selectable
// row is skipped with awaiting_approval), so this is a display inconsistency, never a
// bypass. If a future change makes it fail CLOSED, these three specs are what must be
// re-decided rather than silently flipped.
describe('isRowSelectable fails OPEN on an unrecognised run_state (APPR-08-09, decision B5)', () => {
  it("A-sel-8: an unknown run_state ('opened', 'OPEN') on a non-null approval reads as selectable", () => {
    for (const run_state of ['opened', 'OPEN', 'Open', 'approved_pending']) {
      expect(
        isRowSelectable({ ...draftInvoice, status: 'validated', approval: { ...OPEN_RUN, run_state } }),
        `run_state=${run_state}`,
      ).toBe(true)
    }
  })

  it('A-sel-9: an empty-string run_state on a non-null approval reads as selectable', () => {
    expect(isRowSelectable({ ...draftInvoice, status: 'validated', approval: { ...OPEN_RUN, run_state: '' } })).toBe(true)
  })

  it('A-sel-10: an absent run_state key on a non-null approval reads as selectable', () => {
    // The wire cannot produce this today (RowFacts has no omitempty), and the type forbids
    // it -- the cast is what makes the undefined branch reachable at all.
    const noRunState = { ...OPEN_RUN, run_state: undefined } as unknown as InvoiceApproval

    expect(isRowSelectable({ ...draftInvoice, status: 'validated', approval: noRunState })).toBe(true)
  })

  it('A-sel-11: fail-open never rescues a non-validated status', () => {
    expect(isRowSelectable({ ...draftInvoice, status: 'draft', approval: { ...OPEN_RUN, run_state: 'opened' } })).toBe(false)
  })

  it('A-sel-12: a non-object approval fails open too, and never throws', () => {
    // normaliseInvoiceRow coerces a string/number/array/absent `approval` to null before a
    // row reaches here (invoices.ts:501-517), so this pins the PREDICATE's own behaviour
    // for the day someone builds a row without it -- `?.` only guards null/undefined.
    const junk = ['open', 42, [], true, undefined]

    for (const approval of junk) {
      const row = { ...draftInvoice, status: 'validated' as InvoiceStatus, approval } as unknown as InvoiceRecord
      expect(() => isRowSelectable(row), `approval=${JSON.stringify(approval) ?? 'undefined'}`).not.toThrow()
      expect(isRowSelectable(row), `approval=${JSON.stringify(approval) ?? 'undefined'}`).toBe(true)
    }
  })
})

// QA Stage 4 (task-500) — the whole (status x run_state) surface, with LITERAL expected
// values rather than I-sel-1's oracle, which recomputes the implementation's own
// expression and so cannot catch a rule that is wrong in both places at once.
//
// `run_state` has five wire values: null (no run) plus the four the CHECK allows
// (migrations/20260809232011_approval_runs.sql:40). Only 'open' blocks; the other four
// read as selectable. That is EQUIVALENT to the server gate
// (TransmitClear = !policyActive || approvedRun, gate.go:39) on every reachable state,
// but only because of four invariants living outside the SPA: ApplyValidation always arms
// (store.go:1938), a rejection demotes to draft in the same tx (decision.go:319),
// every walk back to draft cancels the live run (engine.go:262), and 'approved' is EXISTS
// over all runs while `run_state` is the newest one (gate.go:104). If any of those change,
// this table is the SPA-side thing that must be re-decided.
describe('isRowSelectable over the whole status x run_state surface (APPR-08-09, QA Stage 4)', () => {
  const RUN_STATES = [null, 'open', 'approved', 'rejected', 'cancelled'] as const

  it('A-sel-13: exactly four of the 35 combinations are selectable, and all four are validated', () => {
    const statuses: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']
    const selectable: string[] = []

    for (const status of statuses) {
      for (const run_state of RUN_STATES) {
        const approval = run_state === null ? null : { ...OPEN_RUN, run_state }
        if (isRowSelectable({ ...draftInvoice, status, approval })) selectable.push(`${status}/${run_state ?? 'no-run'}`)
      }
    }

    expect(selectable).toEqual([
      'validated/no-run',
      'validated/approved',
      'validated/rejected',
      'validated/cancelled',
    ])
  })

  it('A-sel-14: an open run on every non-validated status is still non-selectable -- the status half dominates', () => {
    const nonValidated: InvoiceStatus[] = ['draft', 'queued', 'submitted', 'accepted', 'rejected', 'failed']

    for (const status of nonValidated) {
      expect(isRowSelectable({ ...draftInvoice, status, approval: OPEN_RUN }), `status=${status}`).toBe(false)
      expect(isRowSelectable({ ...draftInvoice, status, approval: null }), `status=${status}`).toBe(false)
    }
  })

  it('A-sel-15: a page mixing all five run states selects the four non-open ids, in row order', () => {
    const rows: InvoiceRecord[] = RUN_STATES.map((run_state) => ({
      ...draftInvoice,
      id: run_state ?? 'no-run',
      status: 'validated' as InvoiceStatus,
      approval: run_state === null ? null : { ...OPEN_RUN, run_state },
    }))

    expect(selectableIds(rows)).toEqual(['no-run', 'approved', 'rejected', 'cancelled'])
    // 'some', not 'all': the open-run id is in the selection but not in `selectable`, so it
    // can neither be counted nor inflate the comparison.
    expect(selectAllState(['no-run', 'open'], rows)).toBe('some')
    expect(selectAllState(['no-run', 'approved', 'rejected', 'cancelled', 'open'], rows)).toBe('all')
  })

  it('A-sel-16: when a run CLOSES between fetches the row becomes selectable again, but pruneSelection does not resurrect a dropped id', () => {
    const open: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated', approval: OPEN_RUN },
      { ...draftInvoice, id: 'b', status: 'validated', approval: null },
    ]
    const closed: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated', approval: APPROVED_RUN },
      { ...draftInvoice, id: 'b', status: 'validated', approval: null },
    ]

    // Fetch 1: the approver has not decided, so 'a' is dropped from the selection.
    const afterOpen = pruneSelection(['a', 'b'], open)
    expect(afterOpen).toEqual(['b'])

    // Fetch 2: the run closed. 'a' is selectable again -- but prune is a filter, not a
    // memory, so it cannot re-add what the previous tick removed. The user must re-tick.
    expect(selectableIds(closed)).toEqual(['a', 'b'])
    expect(pruneSelection(afterOpen, closed)).toEqual(['b'])
    expect(pruneSelection(['a', 'b'], closed)).toEqual(['a', 'b'])
  })
})

// RED specs (APPR-12-06, task-531, Stage 2.5/Mode A) — selectBlockedReason is the pure
// helper the register/review checkboxes read to name WHY a row can't be selected for
// submit. Built on skipReasonLabel(batchSubmitReason*) (GAP-3), never a fresh literal, so
// the pre-click reason stays byte-identical to the post-click skip panel. Every spec below
// fails on the stub's thrown `not implemented`, the correct RED reason.
describe('selectBlockedReason (APPR-12-06)', () => {
  it('A06-1: a selectable row (validated, no open run) has a null reason', () => {
    expect(selectBlockedReason({ ...draftInvoice, status: 'validated', approval: null })).toBeNull()
  })

  it("A06-2: an open-run row names the approval cause, byte-identical to skipReasonLabel('awaiting_approval')", () => {
    const row = { ...draftInvoice, status: 'validated' as InvoiceStatus, approval: OPEN_RUN }
    expect(selectBlockedReason(row)).toBe(skipReasonLabel('awaiting_approval'))
  })

  it("A06-3: a draft row -- the only pre-submission status that isn't validated -- names the status cause, byte-identical to skipReasonLabel('not_validated')", () => {
    const row = { ...draftInvoice, status: 'draft' as InvoiceStatus, approval: null }
    expect(selectBlockedReason(row)).toBe(skipReasonLabel('not_validated'))
  })

  // A06-4 asserted TOTALITY only (`not.toBeNull()`), which is why it stayed green while
  // ten deployed rows read "Not validated — validate it first" beside an ACCEPTED pill.
  // It now asserts TRUTHFULNESS: the EXACT expected string, or null, for all 35 cells.
  it('A06-4: truthfulness — every cell of the status x run_state matrix returns the exact reason it can honestly name, or null', () => {
    const AWAITING = skipReasonLabel('awaiting_approval')
    const NOT_VALIDATED = skipReasonLabel('not_validated')
    const runStates = ['none', 'open', 'approved', 'rejected', 'cancelled'] as const
    type RunKey = (typeof runStates)[number]

    // Written out cell by cell ON PURPOSE. An expectation derived from a predicate would
    // just re-run the implementation and pass on whatever it does. Only the two
    // pre-submission statuses may carry a sentence; the five later ones say nothing,
    // because the status pill beside them is already the answer.
    const EXPECTED: Record<InvoiceStatus, Record<RunKey, string | null>> = {
      draft: { none: NOT_VALIDATED, open: AWAITING, approved: NOT_VALIDATED, rejected: NOT_VALIDATED, cancelled: NOT_VALIDATED },
      validated: { none: null, open: AWAITING, approved: null, rejected: null, cancelled: null },
      queued: { none: null, open: null, approved: null, rejected: null, cancelled: null },
      submitted: { none: null, open: null, approved: null, rejected: null, cancelled: null },
      accepted: { none: null, open: null, approved: null, rejected: null, cancelled: null },
      rejected: { none: null, open: null, approved: null, rejected: null, cancelled: null },
      failed: { none: null, open: null, approved: null, rejected: null, cancelled: null },
    }

    const statuses = Object.keys(EXPECTED) as InvoiceStatus[]
    let cells = 0
    for (const status of statuses) {
      for (const run of runStates) {
        cells++
        const candidate = { ...draftInvoice, status, approval: run === 'none' ? null : { ...OPEN_RUN, run_state: run } }
        expect(selectBlockedReason(candidate), `status=${status} run_state=${run}`).toBe(EXPECTED[status][run])
      }
    }

    // The table must cover the whole union, not a subset someone trimmed: 7 statuses x 5
    // run states. A shrunken EXPECTED would otherwise pass by simply asserting less.
    expect(statuses).toHaveLength(7)
    expect(cells).toBe(35)
  })

  // The defect stated as its own spec, so the guard survives a future rewrite of A06-4's
  // table: no post-submission row may be told to validate itself.
  it('A06-4b: no post-submission status ever returns the not-validated sentence, at any run_state', () => {
    const postSubmission: InvoiceStatus[] = ['queued', 'submitted', 'accepted', 'rejected', 'failed']

    for (const status of postSubmission) {
      for (const approval of [null, OPEN_RUN, APPROVED_RUN]) {
        const candidate = { ...draftInvoice, status, approval }
        expect(
          selectBlockedReason(candidate),
          `status=${status} run_state=${approval?.run_state ?? 'null'} -- a row already past submission cannot be "validated first"`,
        ).toBeNull()
      }
    }
  })
})

describe('selectBlockedReason does not read can_approve (APPR-12-06, AC #7)', () => {
  it('A06-12: a validated row with no open run stays SELECTABLE with a NULL reason even when blocked from approving (can_approve:false + a non-null approve_blocked_reason) — guards against harmonising the submit and approve gates', () => {
    const candidate: InvoiceRecord = {
      ...draftInvoice,
      status: 'validated',
      approval: null,
      can_approve: false,
      approve_blocked_reason: 'Waiting on the Finance Lead seat',
    }

    expect(isRowSelectable(candidate), 'the submit gate must stay independent of the approve gate').toBe(true)
    expect(selectBlockedReason(candidate)).toBeNull()
  })
})

describe('live-refresh predicates', () => {
  it('I-poll-1: isInFlight is true for exactly queued and submitted', () => {
    const statuses: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']

    for (const status of statuses) {
      expect(isInFlight(status), `status=${status}`).toBe(status === 'queued' || status === 'submitted')
    }
  })

  it('I-poll-2: shouldPollInvoice requires in-flight AND visible', () => {
    expect(shouldPollInvoice('queued', true)).toBe(true)
    expect(shouldPollInvoice('queued', false)).toBe(false)
    expect(shouldPollInvoice('accepted', true)).toBe(false)
  })

  it('I-poll-3: shouldPollList polls when any row is in flight', () => {
    const terminalRows: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'accepted' },
      { ...draftInvoice, id: 'b', status: 'rejected' },
    ]
    expect(shouldPollList(terminalRows, true)).toBe(false)

    const withQueued: InvoiceRecord[] = [...terminalRows, { ...draftInvoice, id: 'c', status: 'queued' }]
    expect(shouldPollList(withQueued, true)).toBe(true)
    expect(shouldPollList(withQueued, false)).toBe(false)
  })

  it('LIVE_POLL_MS is 2000', () => {
    expect(LIVE_POLL_MS).toBe(2000)
  })
})

describe('shouldRefreshHistory', () => {
  it('I-hist-1: shouldRefreshHistory fires only on a real change', () => {
    expect(shouldRefreshHistory('queued', 'accepted')).toBe(true)
    expect(shouldRefreshHistory('queued', 'queued')).toBe(false)
  })

  it('I-hist-2: shouldRefreshHistory is false on first observation', () => {
    // The initial history.run() on mount already covers the first load -- firing here
    // too would double-fetch.
    expect(shouldRefreshHistory(null, 'queued')).toBe(false)
  })
})

describe('shouldShowRejectionCard', () => {
  it('R-1: empty reasons never shows the card', () => {
    expect(shouldShowRejectionCard({ status: 'rejected', rejection_reasons: [] })).toBe(false)
  })

  it('R-2: non-empty reasons on a rejected invoice shows the card', () => {
    expect(shouldShowRejectionCard({ status: 'rejected', rejection_reasons: [{ code: 'invalid_tin', message: 'bad tin' }] })).toBe(true)
  })

  it('R-3: non-empty reasons on a demoted draft still shows the card', () => {
    expect(shouldShowRejectionCard({ status: 'draft', rejection_reasons: [{ code: 'invalid_tin', message: 'bad tin' }] })).toBe(true)
  })

  it('R-4: non-empty reasons on an accepted invoice never shows the card (server-bug backstop)', () => {
    // task-251 AC #7: an accepted invoice should never carry rejection_reasons, but the
    // card must not show one if it somehow does -- an untested backstop is not a
    // backstop.
    expect(shouldShowRejectionCard({ status: 'accepted', rejection_reasons: [{ code: 'invalid_tin', message: 'bad tin' }] })).toBe(false)
  })
})

describe('rejectionProvenance', () => {
  it('P-1: rejectionProvenance is total and correct over all seven statuses', () => {
    const statuses: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']

    for (const status of statuses) {
      expect(rejectionProvenance(status), `status=${status}`).toBe(status === 'rejected' ? 'current' : 'historical')
    }
  })
})

describe('shouldShowFiscalRecord (optional helper)', () => {
  it('F-fiscal-1: accepted with an irn shows the fiscal record', () => {
    expect(shouldShowFiscalRecord({ status: 'accepted', irn: 'IRN-1' })).toBe(true)
  })

  it('F-fiscal-2: accepted without an irn does not', () => {
    expect(shouldShowFiscalRecord({ status: 'accepted', irn: null })).toBe(false)
  })

  it('F-fiscal-3: non-accepted status does not, even with an irn present', () => {
    expect(shouldShowFiscalRecord({ status: 'draft', irn: 'IRN-1' })).toBe(false)
  })
})

describe('shouldFetchInvoices / invoicesViewState', () => {
  it('I13: base==null short-circuits shouldFetchInvoices to false and invoicesViewState to "idle" regardless of async status; base set mirrors async.status', () => {
    expect(shouldFetchInvoices(null)).toBe(false)
    expect(shouldFetchInvoices(base)).toBe(true)

    const readyState: AsyncState<InvoiceRecord[]> = { status: 'ready', data: [draftInvoice], error: null }
    expect(invoicesViewState(null, readyState)).toBe('idle')

    const cases: Array<AsyncState<InvoiceRecord[]>> = [
      { status: 'idle', data: null, error: null },
      { status: 'loading', data: null, error: null },
      { status: 'error', data: null, error: new ApiError('network', 'boom') },
      { status: 'empty', data: null, error: null },
      { status: 'ready', data: [draftInvoice], error: null },
    ]
    for (const asyncState of cases) {
      expect(invoicesViewState(base, asyncState)).toBe(asyncState.status)
    }
  })
})

// gateByActiveEntity ([dashboard-scope-per-client], persona-handoff-fix step 2; RENAMED
// from filterByActiveEntity by the step-6 regression fix, [entity-id-restored]) — added
// alongside the client-scoped Invoices list this story ships, not part of the original
// I1-I14 architect table. The AUTHORITATIVE row-level entity_id filtering now happens
// server-side (listInvoices' own `entity_id` param, covered by the `listInvoices`
// describe block above); this function's row-level check stays too, now as a
// render-time invariant against a stale-entity flash rather than the primary filter —
// see its own doc comment (lib/invoices.ts) for why dropping it would be wrong
// (product-advisor review caught this pre-commit).
describe('gateByActiveEntity', () => {
  const other: InvoiceRecord = { ...draftInvoice, id: 'inv-2', entity_id: 'e2', invoice_number: 'INV-002' }

  it('firm mode (isInhouse:false) keeps only rows whose entity_id matches the active entity', () => {
    expect(gateByActiveEntity([draftInvoice, other], false, 'e1')).toEqual([draftInvoice])
  })

  it('firm mode with entityId===null (no client resolved yet) returns [], never every row', () => {
    expect(gateByActiveEntity([draftInvoice, other], false, null)).toEqual([])
  })

  it('in-house (isInhouse:true) bypasses the gate entirely and returns every row unchanged', () => {
    expect(gateByActiveEntity([draftInvoice, other], true, null)).toEqual([draftInvoice, other])
    expect(gateByActiveEntity([draftInvoice, other], true, 'e1')).toEqual([draftInvoice, other])
  })

  it('an empty input list stays empty regardless of scope', () => {
    expect(gateByActiveEntity([], false, 'e1')).toEqual([])
    expect(gateByActiveEntity([], true, null)).toEqual([])
  })
})

describe('invoices data layer: 401 propagation', () => {
  it('I14: a 401 from getInvoice rejects ApiError{status:401} AND fires the authedFetch seam\'s onUnauthorized once', async () => {
    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'token expired' }) })
    const onUnauthorized = vi.fn()
    const af = createAuthedFetch(() => 'tok', onUnauthorized)

    const err = await captureRejection(() => getInvoice(af, base, 'inv-1'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    // Not this helper's job to call onUnauthorized (that's the seam's, M3-07-02) —
    // asserted here only to prove getInvoice didn't intercept/swallow the error before
    // it reached the seam.
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })
})

// --- Adversarial / edge coverage added at QA (Stage 4), on top of the Stage-2.5 AC
// specs above (I1-I14, left untouched). ---

describe('invoiceStatusStyle: totality (adversarial)', () => {
  it('I15: every one of the 7 InvoiceStatus values resolves to a well-formed style (exhaustive, not spot-checked)', () => {
    const allStatuses: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']

    for (const status of allStatuses) {
      const style = invoiceStatusStyle(status)
      expect(style, `status=${status}`).toBeDefined()
      expect(typeof style.bg, `status=${status}`).toBe('string')
      expect(typeof style.border, `status=${status}`).toBe('string')
      expect(typeof style.text, `status=${status}`).toBe('string')
      expect(style.bg.length, `status=${status}`).toBeGreaterThan(0)
      expect(style.border.length, `status=${status}`).toBeGreaterThan(0)
      expect(style.text.length, `status=${status}`).toBeGreaterThan(0)
      expect(style.label, `status=${status}`).toBe(status.toUpperCase())
    }
  })

  it('I16: multiple unrecognized status strings all fall back to the exact muted style without throwing', () => {
    const bogusValues = ['', 'DRAFT', 'pending', 'unknown-status', 'null']

    for (const bogus of bogusValues) {
      expect(() => invoiceStatusStyle(bogus as InvoiceStatus), `bogus=${JSON.stringify(bogus)}`).not.toThrow()
      const style = invoiceStatusStyle(bogus as InvoiceStatus)
      expect(style, `bogus=${JSON.stringify(bogus)}`).toEqual({
        bg: 'var(--status-muted-bg)',
        border: 'var(--status-muted-border)',
        text: 'var(--status-muted-text)',
        label: 'UNKNOWN',
      })
    }
  })
})

describe('listInvoices: needsAttention explicit-false (adversarial)', () => {
  it('I17: {needsAttention:false} omits ?needs_attention entirely — only ===true appends it', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { needsAttention: false })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices')
    expect(url).not.toContain('needs_attention')
    expect(url).not.toContain('?')
  })
})

describe('editInvoice: multi-field body fidelity (adversarial)', () => {
  it('I18: a multi-field patch serializes exactly the passed keys — no undefined/extra keys injected — via PATCH', async () => {
    const patch: InvoiceEditInput = { supplier_tin: 'x', buyer_name: 'New Buyer', total: '999.00' }
    // `updated` is the server's PATCH response, an InvoiceRecord. InvoiceEditInput's
    // `line_items` (LineItemEditInput[], no id/line_no) is a DIFFERENT type from the
    // stored InvoiceRecord['line_items'], so the key is dropped before spreading -- this
    // patch is header-only either way.
    const { line_items: _unusedLines, ...headerPatch } = patch
    const updated: InvoiceRecord = { ...draftInvoice, ...headerPatch }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(updated) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await editInvoice(af, base, 'inv-1', patch)

    expect(result).toEqual(updated)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/inv-1')
    expect(init.method).toBe('PATCH')
    const sentBody: unknown = JSON.parse(init.body as string)
    expect(sentBody).toEqual(patch)
    expect(Object.keys(sentBody as object).sort()).toEqual(Object.keys(patch).sort())
  })
})

describe('getInvoiceHistory: empty array (adversarial)', () => {
  it('I19: an empty history response resolves to [] — not null/undefined', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve([]) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoiceHistory(af, base, 'inv-1')

    expect(result).toEqual([])
    expect(result).not.toBeNull()
    expect(result).not.toBeUndefined()
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe('invoices data layer: non-401 ApiError propagation (adversarial)', () => {
  it('I20: a 404 from getInvoice rejects ApiError{status:404} unchanged', async () => {
    mockFetchOnce({
      ok: false,
      status: 404,
      statusText: 'Not Found',
      json: () => Promise.resolve({ error: 'invoice not found' }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => getInvoice(af, base, 'missing'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(404)
  })

  it('I21: a 500 from listInvoices rejects ApiError{status:500} unchanged, even with no parseable body', async () => {
    mockFetchOnce({
      ok: false,
      status: 500,
      statusText: 'Internal Server Error',
      json: () => Promise.reject(new Error('no body')),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => listInvoices(af, base, {}))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(500)
  })
})

// --- QA Mode B (task-253): adversarial coverage on top of the Stage-2.5/Stage-4 specs
// above. Nothing above is weakened, skipped, or deleted. ---

describe('pruneSelection: real churn (adversarial)', () => {
  it('P-sel-1: a poll tick that simultaneously advances, removes and adds rows keeps only the still-present still-validated ids', () => {
    // Selection was computed against an earlier page: a (validated, stays), b (validated,
    // advances to queued by the poll), c (validated, scrolled/filtered off the page), plus
    // a stale id 'z' the selection never should have contained. The polled rows also
    // introduce a brand-new validated row 'd' that was never selected.
    const priorSelection = ['a', 'b', 'c', 'z']
    const polledRows: InvoiceRecord[] = [
      { ...draftInvoice, id: 'a', status: 'validated' },
      { ...draftInvoice, id: 'b', status: 'queued' },
      { ...draftInvoice, id: 'd', status: 'validated' },
    ]

    const result = pruneSelection(priorSelection, polledRows)

    // Exactly the still-present, still-validated ids -- 'b' dropped (advanced past
    // validated), 'c'/'z' dropped (no longer present), 'd' NOT added (pruneSelection only
    // narrows an existing selection, it never grows one).
    expect(result).toEqual(['a'])
  })

  it('P-sel-2: a selection that becomes fully invalid across one churn collapses to empty, not an error', () => {
    const rows: InvoiceRecord[] = [{ ...draftInvoice, id: 'a', status: 'accepted' }]

    expect(pruneSelection(['a', 'b'], rows)).toEqual([])
  })
})

describe('submitInvoices: failure modes (adversarial)', () => {
  it('SUB-1: a non-2xx with an unparseable body still rejects ApiError{kind:"http"} with the real status', async () => {
    mockFetchOnce({
      ok: false,
      status: 502,
      statusText: 'Bad Gateway',
      json: () => Promise.reject(new Error('not json')),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => submitInvoices(af, base, ['a'], 'k'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(502)
  })

  it('SUB-2: a network-level throw rejects ApiError{kind:"network", status:null}, never swallowed', async () => {
    const fetchMock = vi.fn().mockRejectedValue(new TypeError('Failed to fetch'))
    vi.stubGlobal('fetch', fetchMock)
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => submitInvoices(af, base, ['a'], 'k'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('network')
    expect((err as ApiError).status).toBeNull()
  })

  it('SUB-3: a 2xx body with no "results" key resolves to undefined, pinned as-is (mirrors listInvoices\' unguarded .invoices unwrap; the backend contract guarantees the key, per addendum A10)', async () => {
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await submitInvoices(af, base, ['a'], 'k')

    // Pinned, not endorsed: submitInvoices does no defensive `?? []` the way getInvoice
    // does for rule_set_version/qr_png_base64. If a caller (M5-09-06) ever does
    // `result.length` or `result.filter(...)` on this without a guard, it throws a
    // TypeError at the call site rather than surfacing an ApiError -- flagged for QA
    // report, not fixed here (out of this subtask's scope to change the contract).
    expect(result).toBeUndefined()
  })

  it('SUB-4: a 400 with a structured {error} envelope surfaces that message on the ApiError, unchanged', async () => {
    mockFetchOnce({
      ok: false,
      status: 400,
      statusText: 'Bad Request',
      json: () => Promise.resolve({ error: 'idempotency_key exceeds the 218-char bound' }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => submitInvoices(af, base, ['a'], 'x'.repeat(219)))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(400)
    expect((err as ApiError).message).toBe('idempotency_key exceeds the 218-char bound')
  })
})

describe('newIdempotencyKey: under load (adversarial)', () => {
  it('KEY-1: 500 calls are all distinct, all exactly 36 chars, all within the 218-char backend bound', () => {
    const keys = Array.from({ length: 500 }, () => newIdempotencyKey())

    expect(new Set(keys).size).toBe(500)
    for (const key of keys) {
      expect(key).toHaveLength(36)
      expect(key.length).toBeLessThanOrEqual(218)
    }
  })

  it('KEY-2: the derived per-invoice key ("<request key>:<invoice id>", batch_submit.go deriveBatchSubmitKey) stays comfortably within its own 255-char CHECK bound', () => {
    // 36 (uuid request key) + 1 (":") + 36 (uuid invoice id) = 73, far under the shared
    // idempotency_keys.key CHECK char_length<=255 (migrations/20260707193000_river_and_
    // idempotency.sql:394) and under the handler's own 218-char pre-derivation cap
    // (handlers.go:543-545).
    const requestKey = newIdempotencyKey()
    const invoiceId = '11111111-1111-1111-1111-111111111111'
    const derived = `${requestKey}:${invoiceId}`

    expect(derived).toHaveLength(73)
    expect(derived.length).toBeLessThanOrEqual(255)
  })
})

describe('mbsPathToEditField: hostile input (adversarial)', () => {
  it('PATH-3: indexed line-item paths never map and never throw', () => {
    expect(mbsPathToEditField('line_items[0].unit_price')).toBeNull()
    expect(mbsPathToEditField('line_items[3].description')).toBeNull()
    expect(() => mbsPathToEditField('line_items[0].unit_price')).not.toThrow()
  })

  it('PATH-4: casing variants of a real path do not fuzzy-match (the table is case-sensitive)', () => {
    expect(mbsPathToEditField('Supplier.Tin')).toBeNull()
    expect(mbsPathToEditField('SUPPLIER.TIN')).toBeNull()
    expect(mbsPathToEditField('Issue_Date')).toBeNull()
  })

  it('PATH-5: whitespace and a trailing dot are never trimmed/normalized away', () => {
    expect(mbsPathToEditField(' supplier.tin')).toBeNull()
    expect(mbsPathToEditField('supplier.tin ')).toBeNull()
    expect(mbsPathToEditField('supplier.tin.')).toBeNull()
    expect(mbsPathToEditField('.supplier.tin')).toBeNull()
  })

  it('PATH-6: every hostile input above resolves without throwing (a throw here would blank the M5-09-05 detail render)', () => {
    const hostile: Array<string | undefined> = [
      'line_items[0].unit_price',
      'Supplier.Tin',
      ' supplier.tin',
      'supplier.tin.',
      '',
      undefined,
      'customer.taxIdentifier',
    ]
    for (const path of hostile) {
      expect(() => mbsPathToEditField(path), `path=${JSON.stringify(path)}`).not.toThrow()
    }
  })
})

describe('InvoiceRecord: field-by-field sync with invoice.go (adversarial, regression guard)', () => {
  it('SYNC-1: the fixture (typed InvoiceRecord) carries exactly the 29 keys mirrored from invoice.go, no more, no fewer', () => {
    // Independently transcribed from internal/invoice/invoice.go:83-105 (Invoice struct
    // json tags). `expectedKeys` is a plain untyped string[] with no `keyof InvoiceRecord`
    // constraint tying it to the interface (invoices.ts:127-151), so nothing here would
    // fail to compile if the two ever diverged -- the runtime assertion below is the ONLY
    // guard, both against this list drifting from the Go struct and against the fixture
    // silently dropping a field the interface still declares.
    const expectedKeys = [
      'id',
      'entity_id',
      'import_batch_id',
      'invoice_number',
      'status',
      'issue_date',
      'supplier_tin',
      'supplier_name',
      'buyer_tin',
      'buyer_name',
      'currency',
      'subtotal',
      'vat',
      'total',
      'violations',
      'rule_set_version_id',
      'created_at',
      'irn',
      'csid',
      'qr_payload',
      'rejection_reasons',
      // INVCR-01-15 (D6, task-291): +3 -- kept_as_is_at/by/reason join Invoice as
      // direct top-level siblings, no omitempty, same flattened shape as every
      // field above.
      'kept_as_is_at',
      'kept_as_is_by',
      'kept_as_is_reason',
      // BUG-06-04 (task-386): +1 -- failure_kind joins Invoice the same way.
      'failure_kind',
      'rule_set_version',
      // APPR-08-08 (task-499): +1 -- `approval` is a listItem SIBLING, not an Invoice
      // json tag, exactly like `rule_set_version` above. The title said 25 while this
      // list already held 26 (rule_set_version was added without updating it); the
      // count is 27 now and the title says so.
      'approval',
      // APPR-12-09 (task-526): +2 -- the approve pair is a listItem sibling too, and the
      // ONE action pair both wires carry. The reject pair stays detail-only (U5a), so it
      // must NOT appear here.
      'can_approve',
      'approve_blocked_reason',
      // line_items is optional (LineItems omitempty on List; the fixture omits it, as a
      // list-shaped record legitimately would).
    ]

    expect(Object.keys(draftInvoice).sort()).toEqual([...expectedKeys].sort())
  })

  it('SYNC-2: import_batch_id round-trips as a plain string when present, matching the *string/no-omitempty Go tag', () => {
    const withBatch: InvoiceRecord = { ...draftInvoice, import_batch_id: 'batch-123' }
    expect(withBatch.import_batch_id).toBe('batch-123')
  })
})

// --- QA follow-up (task-251): reasonFieldFlags was extracted from InvoiceDetail.tsx's
// InvoiceEditForm (commit 5968178) specifically because its first-reason-wins collision
// rule had zero test oracle inline. Nothing above is weakened, skipped, or deleted. ---

function reason(path: string | undefined, code: string, message = 'm'): RejectionReason {
  return { path, code, message }
}

describe('reasonFieldFlags', () => {
  it('FLAG-1: each of the nine mapped MBS paths produces its edit-field key with that reason\'s code', () => {
    const table: Array<[string, EditFieldKey]> = [
      ['issue_date', 'issue_date'],
      ['currency', 'currency'],
      ['subtotal', 'subtotal'],
      ['vat', 'vat'],
      ['total', 'total'],
      ['supplier.tin', 'supplier_tin'],
      ['supplier.name', 'supplier_name'],
      ['buyer.tin', 'buyer_tin'],
      ['buyer.name', 'buyer_name'],
    ]
    const reasons = table.map(([path], i) => reason(path, `code-${i}`))

    const flags = reasonFieldFlags(reasons)

    expect(flags.size).toBe(9)
    for (const [path, field] of table) {
      const i = table.findIndex(([p]) => p === path)
      expect(flags.get(field), `path=${path}`).toBe(`code-${i}`)
    }
  })

  // LOAD-BEARING: this is the decision that had no oracle inline (QA finding 2 on
  // task-251) -- when two reasons map to the SAME field, the FIRST one (in `reasons`
  // order) must win, deterministically. Mutation-tested: flipping the implementation's
  // `!flags.has(field)` guard to let the LAST reason win instead turns this red (see
  // task-251 implementation notes for the verbatim failure).
  it('FLAG-2: two reasons mapping to the same field keep the FIRST reason\'s code, not the last', () => {
    const first = reason('supplier.tin', 'first_code', 'first message')
    const second = reason('supplier.tin', 'second_code', 'second message')

    const flags = reasonFieldFlags([first, second])

    expect(flags.size).toBe(1)
    expect(flags.get('supplier_tin')).toBe('first_code')
    expect(flags.get('supplier_tin')).not.toBe('second_code')
  })

  it('FLAG-3: unmapped, empty, or undefined paths contribute no map entry, and the input reasons are never mutated', () => {
    const reasons: RejectionReason[] = [
      reason('invoice_number', 'unmapped_field'),
      reason('', 'empty_path'),
      reason(undefined, 'no_path'),
    ]
    const reasonsSnapshot = JSON.parse(JSON.stringify(reasons)) as RejectionReason[]

    const flags = reasonFieldFlags(reasons)

    expect(flags.size).toBe(0)
    // The helper only ever narrows to a field->code Map; it must not reach back into the
    // caller's reason objects/array (the full reason list is still rendered in full by
    // the rejection card above the field flags, per the source comment at invoices.ts).
    expect(reasons).toEqual(reasonsSnapshot)
    expect(reasons).toHaveLength(3)
  })

  it('FLAG-4: an empty reasons array yields an empty Map, not undefined or a throw', () => {
    expect(() => reasonFieldFlags([])).not.toThrow()

    const flags = reasonFieldFlags([])

    expect(flags).toBeInstanceOf(Map)
    expect(flags.size).toBe(0)
    expect(flags).not.toBeUndefined()
  })

  it('FLAG-5: a mixed realistic set (some mapped, some not, one duplicate field) produces exactly the expected map', () => {
    const reasons: RejectionReason[] = [
      reason('supplier.tin', 'invalid_tin', 'Supplier TIN failed checksum'), // mapped
      reason('invoice_number', 'unmapped_invoice_number'), // unmapped -> no entry
      reason('buyer.name', 'missing_buyer_name'), // mapped
      reason('supplier.tin', 'duplicate_supplier_tin_reason'), // same field as #1 -> loses
      reason(undefined, 'no_path_reason'), // undefined -> no entry
      reason('total', 'total_mismatch'), // mapped
    ]

    const flags = reasonFieldFlags(reasons)

    expect(flags.size).toBe(3)
    expect(flags.get('supplier_tin')).toBe('invalid_tin')
    expect(flags.get('buyer_name')).toBe('missing_buyer_name')
    expect(flags.get('total')).toBe('total_mismatch')
    expect(flags.has('issue_date')).toBe(false)
  })
})

describe('reasonFieldFlags: hostile input (adversarial)', () => {
  it('FLAG-6: the APP\'s own foreign vocabulary, indexed line-item paths, whitespace, and a trailing dot all contribute no entry and never throw', () => {
    const reasons: RejectionReason[] = [
      reason('customer.taxIdentifier', 'app_only_vocab'),
      reason('line_items[0].unit_price', 'indexed_path'),
      reason('line_items[3].description', 'indexed_path_2'),
      reason(' supplier.tin', 'leading_whitespace'),
      reason('supplier.tin ', 'trailing_whitespace'),
      reason('supplier.tin.', 'trailing_dot'),
      reason('', 'empty_string'),
    ]

    expect(() => reasonFieldFlags(reasons)).not.toThrow()

    const flags = reasonFieldFlags(reasons)

    expect(flags.size).toBe(0)
  })

  it('FLAG-7: a large batch of exclusively-unmapped/hostile reasons still resolves to an empty map without throwing (guards a render-blanking regression)', () => {
    const hostilePaths: Array<string | undefined> = [
      'customer.taxIdentifier',
      'line_items[0].unit_price',
      undefined,
      '',
      'Supplier.Tin',
      '.supplier.tin',
    ]
    const reasons = hostilePaths.map((path, i) => reason(path, `code-${i}`))

    expect(() => reasonFieldFlags(reasons)).not.toThrow()
    expect(reasonFieldFlags(reasons).size).toBe(0)
  })
})

// --- RED specs (Stage 2.5, Mode A): INVED-01-06 (task-267) -- action flags, line
// payload, and computedLineSum. Transcribed from the architect's Test Specs table
// (INV-06-T1..T13); nothing above is weakened, skipped, or deleted.
//
// computedLineSum/diffLineItems are R0 stubs that `throw new Error('not implemented')` --
// most specs below fail on that throw (the intended not-implemented RED signal, per this
// file's established convention). INV-06-T10/T10c fail on a real assertion against
// getInvoice, which does not yet normalize the three action flags. INV-06-T11a/T11b fail
// on a real assertion against the [isfixable-deleted] export, which has not yet been
// removed.

// The five MBS content fields diffLineItems compares -- mirrors the unexported
// `LineContent` type in invoices.ts (id/line_no deliberately excluded,
// [fingerprint-excludes-line-ids]); this file can't import that type since it isn't
// exported, so it's redeclared here structurally over the exported InvoiceLineItem.
type LineFields = Pick<InvoiceLineItem, 'description' | 'quantity' | 'unit_price' | 'line_total' | 'line_tax'>

function lineFields(overrides: Partial<LineFields> = {}): LineFields {
  return {
    description: 'Widget',
    quantity: '2',
    unit_price: '100.00',
    line_total: '200.00',
    line_tax: '15.00',
    ...overrides,
  }
}

describe('computedLineSum (INVED-01-06)', () => {
  it('INV-06-T1: sums unit_price * quantity across lines -- plain and DB-shaped (trailing-zero) decimal strings alike', () => {
    const plain: Array<Pick<InvoiceLineItem, 'quantity' | 'unit_price'>> = [
      { quantity: '2', unit_price: '100' },
      { quantity: '1', unit_price: '50' },
    ]
    expect(computedLineSum(plain)).toBe('250.00')

    const dbShaped: Array<Pick<InvoiceLineItem, 'quantity' | 'unit_price'>> = [
      { quantity: '2.000', unit_price: '100.00' },
      { quantity: '1.000', unit_price: '50.00' },
    ]
    expect(computedLineSum(dbShaped)).toBe('250.00')
  })

  it("INV-06-T2: an absent (null) quantity weights the line at 1, mirroring lineSumEval's implicit-weight-1 rule", () => {
    expect(computedLineSum([{ quantity: null, unit_price: '100' }])).toBe('100.00')
  })

  it('INV-06-T3: any line missing unit_price nulls the whole sum -- never a partial total the rule would refuse', () => {
    expect(
      computedLineSum([
        { quantity: '1', unit_price: null },
        { quantity: '2', unit_price: '50' },
      ]),
    ).toBeNull()
  })

  it('INV-06-T4: a non-numeric unit_price nulls the sum', () => {
    expect(computedLineSum([{ quantity: '1', unit_price: 'abc' }])).toBeNull()
  })

  it('INV-06-T5: an empty line set is null, never "0.00" -- nothing to show', () => {
    expect(computedLineSum([])).toBeNull()
  })

  it("INV-06-T6: sub-kobo precision survives exactly -- the mirrored rule's own tolerance is 0.005", () => {
    expect(computedLineSum([{ quantity: '3', unit_price: '0.005' }])).toBe('0.015')
  })

  it('INV-06-T6b: exact-decimal arithmetic only -- a float-accumulation mutant turns every case below red', () => {
    // Mutation oracle: swapping this helper's exact scaled-integer arithmetic for a naive
    // Number(u)*Number(q) accumulation reproduces float artifacts verified live in node:
    // 8.20+0.10 -> 8.299999999999999; 100.10*3+0.30 -> 300.59999999999997;
    // 0.07*3 -> 0.21000000000000002; 0.10+0.20 -> 0.30000000000000004. Any of those next to
    // a Subtotal is a visible defect on the firm's #1 failing rule.
    expect(
      computedLineSum([
        { quantity: '1', unit_price: '8.20' },
        { quantity: '1', unit_price: '0.10' },
      ]),
    ).toBe('8.30')

    expect(
      computedLineSum([
        { quantity: '3', unit_price: '100.10' },
        { quantity: '1', unit_price: '0.30' },
      ]),
    ).toBe('300.60')

    expect(computedLineSum([{ quantity: '3', unit_price: '0.07' }])).toBe('0.21')

    expect(
      computedLineSum([
        { quantity: '1', unit_price: '0.10' },
        { quantity: '1', unit_price: '0.20' },
      ]),
    ).toBe('0.30')
  })

  it('INV-06-T6c: a present-but-non-numeric quantity also nulls the sum, not only a bad amount', () => {
    expect(computedLineSum([{ quantity: 'abc', unit_price: '100' }])).toBeNull()
  })

  it("INV-06-T6d: an empty-string unit_price nulls the sum -- guards the Number('')===0 trap", () => {
    expect(computedLineSum([{ quantity: '1', unit_price: '' }])).toBeNull()
  })

  it("INV-06-T6e: a leading-zero unit_price fails the backend's own numeric grammar and nulls the sum", () => {
    expect(computedLineSum([{ quantity: '1', unit_price: '007' }])).toBeNull()
  })

  it('INV-06-T13: computedLineSum never derives/rewrites subtotal, vat or total, and never mutates its input (AC #6)', () => {
    const lines: InvoiceLineItem[] = [
      { id: 'l1', line_no: 1, description: 'Widget', quantity: '2', unit_price: '100.00', line_total: '200.00', line_tax: '15.00' },
      { id: 'l2', line_no: 2, description: 'Gadget', quantity: '1', unit_price: '50.00', line_total: '50.00', line_tax: '3.75' },
    ]
    const inv: InvoiceRecord = {
      ...draftInvoice,
      // Deliberately NOT reconciled with the lines below -- proves the hint doesn't touch
      // these fields even when they disagree with its own computed sum.
      subtotal: '999.99',
      vat: '11.25',
      total: '1011.24',
      line_items: lines,
    }
    const linesSnapshot = JSON.parse(JSON.stringify(lines)) as InvoiceLineItem[]

    const sum = computedLineSum(inv.line_items ?? [])

    expect(sum).toBe('250.00')
    expect(inv.subtotal).toBe('999.99')
    expect(inv.vat).toBe('11.25')
    expect(inv.total).toBe('1011.24')
    expect(inv.line_items).toEqual(linesSnapshot)
  })
})

describe('diffLineItems (INVED-01-06)', () => {
  it('INV-06-T7: a content-identical copy returns undefined -- an untouched editor sends no line_items key', () => {
    const original: LineFields[] = [lineFields(), lineFields({ description: 'Gadget', unit_price: '50.00', line_total: '50.00', line_tax: '3.75', quantity: '1' })]
    const untouchedCopy: LineFields[] = original.map((l) => ({ ...l }))

    expect(diffLineItems(original, untouchedCopy)).toBeUndefined()
  })

  it("INV-06-T7b: a stored NULL canonicalizes the same as the '' a React input yields -- highest-value spec here", () => {
    const original: LineFields[] = [lineFields({ description: null, line_tax: null })]
    const edited: LineFields[] = [lineFields({ description: '', line_tax: '' })]

    // Without the ''->null canonicalization on both sides this returns an array: replace-all
    // fires, line ids churn, and the fingerprint genuinely moves (NULL -> '' is real
    // content) -- falsely demoting a validated invoice on a save that changed nothing, and
    // (for a numeric column) raising 22P02 -> ErrValidation -> 400 on a no-op save.
    expect(diffLineItems(original, edited)).toBeUndefined()
  })

  it("INV-06-T7d: a stored '' on the ORIGINAL side canonicalizes the same as the edited side's '' -- closes the gap T7b left (both sides there start out null, so a mutant that drops canonicalization ONLY on `original` still passes T7b)", () => {
    // A blank description cell imports as a stored '' , never NULL (line_description is
    // absent from importer/service.go's numericFields, so fieldValue's blank-guard never
    // fires for it -- unlike subtotal/vat/total/line_quantity/line_unit_price). Round-tripped
    // through the editor, an untouched line must still diff as unchanged.
    const original: LineFields[] = [lineFields({ description: '', line_tax: '' })]
    const edited: LineFields[] = [lineFields({ description: '', line_tax: '' })]

    expect(diffLineItems(original, edited)).toBeUndefined()
  })

  it('INV-06-T7c: diffLineItems never mutates either input array, and still returns the correct diff', () => {
    const original: LineFields[] = [lineFields()]
    const edited: LineFields[] = [lineFields({ description: 'Changed' })]
    Object.freeze(original[0])
    Object.freeze(original)
    Object.freeze(edited[0])
    Object.freeze(edited)
    const originalSnapshot = JSON.parse(JSON.stringify(original)) as LineFields[]
    const editedSnapshot = JSON.parse(JSON.stringify(edited)) as LineFields[]

    const result = diffLineItems(original, edited)

    expect(result).toEqual([lineFields({ description: 'Changed' })])
    expect(original).toEqual(originalSnapshot)
    expect(edited).toEqual(editedSnapshot)
  })

  it('INV-06-T8: a single changed field returns the full canonicalized new set, in order', () => {
    const line2 = lineFields({ description: 'Gadget', unit_price: '50.00', line_total: '50.00', line_tax: '3.75', quantity: '1' })
    const original: LineFields[] = [lineFields(), line2]
    const edited: LineFields[] = [lineFields({ description: 'New Description' }), { ...line2 }]

    expect(diffLineItems(original, edited)).toEqual([lineFields({ description: 'New Description' }), line2])
  })

  it('INV-06-T9: removing the middle line of three returns the remaining two, in order', () => {
    const line1 = lineFields({ description: 'First' })
    const line2 = lineFields({ description: 'Second' })
    const line3 = lineFields({ description: 'Third' })
    const original: LineFields[] = [line1, line2, line3]
    const edited: LineFields[] = [{ ...line1 }, { ...line3 }]

    expect(diffLineItems(original, edited)).toEqual([line1, line3])
  })

  it('INV-06-T9b: emptying a 2-line invoice returns [] -- not undefined, the delete-all state must be reachable', () => {
    const original: LineFields[] = [lineFields(), lineFields({ description: 'Second' })]

    const result = diffLineItems(original, [])

    expect(result).toEqual([])
    expect(result).not.toBeUndefined()
  })

  it('INV-06-T9c: a line-less invoice stays line-less -- no spurious line_items key', () => {
    expect(diffLineItems([], [])).toBeUndefined()
  })

  it("INV-06-T9d: reordering two otherwise-identical lines returns the NEW order -- line_no is positional and IS hashed", () => {
    const a = lineFields({ description: 'A' })
    const b = lineFields({ description: 'B' })
    const original: LineFields[] = [a, b]
    const edited: LineFields[] = [{ ...b }, { ...a }]

    expect(diffLineItems(original, edited)).toEqual([b, a])
  })

  it('INV-06-T9e: emitted objects carry exactly the five MBS fields, even when edited rows are built by spreading InvoiceLineItem', () => {
    const original: LineFields[] = [lineFields({ description: 'Old' })]
    // InvoiceLineItem-typed VARIABLE (not an inline literal) so `id`/`line_no` don't trip
    // excess-property checking against diffLineItems' Pick<InvoiceLineItem, ...> parameter.
    const editedWithIds: InvoiceLineItem[] = [
      { id: 'line-1', line_no: 1, description: 'New', quantity: '2', unit_price: '100.00', line_total: '200.00', line_tax: '15.00' },
    ]

    const result = diffLineItems(original, editedWithIds)

    expect(result).toBeDefined()
    for (const row of result ?? []) {
      expect(Object.keys(row).sort()).toEqual(['description', 'line_tax', 'line_total', 'quantity', 'unit_price'])
    }
  })
})

describe('editInvoice: line_items absent/undefined/[] (INV-06-T12)', () => {
  it('INV-06-T12: line_items is omitted when absent or undefined, and sent as an explicit [] when cleared', async () => {
    const updated: InvoiceRecord = { ...draftInvoice, supplier_tin: 'x' }
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const fetchMock1 = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(updated) })
    await editInvoice(af, base, 'inv-1', { supplier_tin: 'x' })
    const [, init1] = fetchMock1.mock.calls[0] as [string, RequestInit]
    expect(init1.body).not.toContain('line_items')

    const fetchMock2 = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(updated) })
    await editInvoice(af, base, 'inv-1', { supplier_tin: 'x', line_items: undefined })
    const [, init2] = fetchMock2.mock.calls[0] as [string, RequestInit]
    expect(init2.body).not.toContain('line_items')

    const fetchMock3 = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(updated) })
    await editInvoice(af, base, 'inv-1', { line_items: [] })
    const [, init3] = fetchMock3.mock.calls[0] as [string, RequestInit]
    expect(init3.body).toBe(JSON.stringify({ line_items: [] }))
    expect(init3.body).toContain('"line_items":[]')
  })
})

describe('getInvoice: action-flag normalization (INVED-01-06)', () => {
  it('INV-06-T10: a GET payload omitting all three action keys normalizes fail-closed', async () => {
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(draftInvoice) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_edit).toBe(false)
    expect(result.can_revalidate).toBe(false)
    expect(result.revalidate_blocked_reason).toBeNull()
  })

  it("INV-06-T10b: well-formed action flags -- including the backend's verbatim em-dash copy -- pass through byte-identical, never SPA-authored", async () => {
    const reasonText = 'Only draft invoices can be re-validated — edit this invoice to return it to draft.'
    const wire = { ...draftInvoice, can_edit: true, can_revalidate: false, revalidate_blocked_reason: reasonText }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_edit).toBe(true)
    expect(result.can_revalidate).toBe(false)
    expect(result.revalidate_blocked_reason).toBe(reasonText)
  })

  it('INV-06-T10c: a non-boolean truthy can_edit (a stringly-typed "false") is denied, never passed through -- the mutation oracle for ===true over ??false', async () => {
    const wire = { ...draftInvoice, can_edit: 'false', can_revalidate: true, revalidate_blocked_reason: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_edit).toBe(false)
    // Positive companion: a genuinely-true, differently-shaped flag on the SAME payload
    // still passes through, proving the denial above is about can_edit's own value, not a
    // side effect that zeroes out every flag.
    expect(result.can_revalidate).toBe(true)
  })
})

// The [isfixable-deleted] export's own doc-comment header, body, and vitest block are
// named at exact line numbers in this subtask's architect plan for removal at GREEN; the
// two specs below are the RED gate proving that removal actually happened, both as a
// symbol (T11a) and as a source-wide identifier scan (T11b).
describe('the [isfixable-deleted] export is gone (INVED-01-06)', () => {
  it('INV-06-T11a: the export no longer exists on the module namespace', () => {
    // A positive companion, so a broken/empty namespace import can't make the negative
    // assertion below pass for the wrong reason.
    expect('getInvoice' in invoicesModule).toBe(true)

    expect(('isFix' + 'able') in invoicesModule).toBe(false)
  })

  it('INV-06-T11b: no file under frontend/app/src contains the identifier as a literal substring', () => {
    // Root resolved from THIS file's own location, never process.cwd() -- vitest may be
    // invoked from the monorepo root or from frontend/app, and cwd-relative traversal
    // would silently scan the wrong subtree depending on which.
    const srcRoot = fileURLToPath(new URL('..', import.meta.url))
    // Built at runtime, never spelled out literally -- a literal needle would appear in
    // THIS file's own source text and make the scan match itself, so it could never pass
    // even once the identifier is genuinely gone everywhere else.
    const needle = 'isFix' + 'able'

    // Sanity/positive companion first: prove the walker mechanism itself actually visits
    // and reads files, by finding a needle that unquestionably exists (computedLineSum,
    // declared earlier in this very file). A walker that silently visited nothing (wrong
    // root, swallowed error, empty dir) would otherwise make the real scan below pass
    // vacuously.
    expect(scanForIdentifier(srcRoot, 'computedLineSum').length).toBeGreaterThan(0)

    expect(scanForIdentifier(srcRoot, needle)).toEqual([])
  })
})

// [gates-on-the-wire]: the blocked-reason copy is the backend's, and the three wire mirrors
// only carry it. A literal in a production source file is how that decision gets quietly
// reversed -- the SPA starts answering "why is this disabled" itself and drifts from Go.
describe('no production source under src/ authors a submit-blocked sentence (APPR-01 AC-4)', () => {
  const srcRoot = fileURLToPath(new URL('..', import.meta.url))
  // Both spec files carry these sentences as FIXTURES, which is the point of them -- so the
  // scan is production-only. Dropping this filter turns the specs below red on their own
  // fixtures, which is also what proves the walker is reaching real files.
  const productionOnly = (needle: string): string[] => scanForIdentifier(srcRoot, needle).filter((p) => !/\.test\.tsx?$/.test(p))

  it('the role refusal sentence appears in no production file', () => {
    // Split so this file's own text never spells the needle, belt-and-braces with the
    // .test.ts(x) filter above (data.test.ts's idiom).
    const needle = 'ask an appro' + 'ver on your team'

    // Non-vacuity: the same walker, same filter, finds a symbol that unquestionably lives in
    // a production file. A walker that visited nothing would pass the negatives for free.
    expect(productionOnly('DETAIL_SUBMIT_COPY').length).toBeGreaterThan(0)
    expect(productionOnly(needle)).toEqual([])
  })

  it('the status-fork submit sentences appear in no production file either', () => {
    // submitBlockedReason's three arms (handlers.go) share this stem; the SPA must not
    // reconstruct any of them, nor the role sentence's verb phrase.
    expect(productionOnly('Only validated invoices can be sub' + 'mitted')).toEqual([])
    expect(productionOnly('can sub' + 'mit an invoice to NRS/MBS')).toEqual([])
  })

  it('the awaiting-approval sentence appears in exactly one production file -- the sanctioned mirror', () => {
    // awaitingApprovalReason (handlers.go). Split on either side of the em dash, never
    // through it. SKIP_REASON_LABELS.awaiting_approval now carries those bytes verbatim under
    // a two-sided guard, so lib/invoices.ts is the one file allowed to spell them; every other
    // production file is still forbidden to author the server's copy. toEqual, not a filter:
    // deleting the mirror empties the array and reds this too.
    const MIRROR = ['lib/invoices.ts']
    expect(productionOnly('This invoice is waiting on appro' + 'val')).toEqual(MIRROR)
    expect(productionOnly('it can be sub' + 'mitted once an approver approves it')).toEqual(MIRROR)
  })
})

// --- Adversarial / edge coverage added at QA (Stage 4, Mode B), on top of the
// Stage-2.5 AC specs above (INV-06-T1..T13, left untouched). ---

describe('computedLineSum (adversarial, QA)', () => {
  it('QA-CLS-1: a very large line set (1000 lines) sums correctly, not just small fixtures', () => {
    const lines: Array<Pick<InvoiceLineItem, 'quantity' | 'unit_price'>> = Array.from({ length: 1000 }, () => ({
      quantity: '1',
      unit_price: '1.00',
    }))

    expect(computedLineSum(lines)).toBe('1000.00')
  })

  it('QA-CLS-2: negative unit_price (a credit/reversal line) sums correctly, including an all-negative set', () => {
    expect(computedLineSum([{ quantity: '2', unit_price: '-50.00' }])).toBe('-100.00')

    expect(
      computedLineSum([
        { quantity: '1', unit_price: '100.00' },
        { quantity: '1', unit_price: '-40.00' },
      ]),
    ).toBe('60.00')
  })

  it("QA-CLS-3: a quantity of '0' contributes zero -- distinct from an ABSENT quantity (weights 1) and a non-numeric one (violates)", () => {
    expect(computedLineSum([{ quantity: '0', unit_price: '100.00' }])).toBe('0.00')
  })

  it('QA-CLS-4: extreme decimal scale (10 fractional digits) survives exactly -- no rounding or truncation', () => {
    expect(computedLineSum([{ quantity: '1', unit_price: '0.0000000001' }])).toBe('0.0000000001')
  })

  it('QA-CLS-5: undefined and null line arrays normalize identically through the `?? []` call-site idiom', () => {
    const maybeUndefined: InvoiceLineItem[] | undefined = undefined
    const maybeNull: InvoiceLineItem[] | null = null
    const fromUndefined = computedLineSum(maybeUndefined ?? [])
    const fromNull = computedLineSum(maybeNull ?? [])

    expect(fromUndefined).toBeNull()
    expect(fromNull).toBeNull()
    expect(fromUndefined).toBe(computedLineSum([]))
  })
})

describe('diffLineItems (adversarial, QA)', () => {
  it('QA-DLI-1: a line whose every one of the five fields is null on the original side and \'\' on the edited side is still unchanged -- generalizes T7b beyond description/line_tax to all five fields', () => {
    const original: LineFields[] = [{ description: null, quantity: null, unit_price: null, line_total: null, line_tax: null }]
    const edited: LineFields[] = [{ description: '', quantity: '', unit_price: '', line_total: '', line_tax: '' }]

    expect(diffLineItems(original, edited)).toBeUndefined()
  })

  it('QA-DLI-2: a large line set (300 lines) -- untouched copy is undefined; a single deep change returns the full 300-line array with only that field changed', () => {
    const original: LineFields[] = Array.from({ length: 300 }, (_, i) => lineFields({ description: `Item ${i}` }))
    const untouchedCopy: LineFields[] = original.map((l) => ({ ...l }))
    expect(diffLineItems(original, untouchedCopy)).toBeUndefined()

    const edited: LineFields[] = original.map((l) => ({ ...l }))
    edited[150] = { ...edited[150], description: 'Changed deep in the set' }

    const result = diffLineItems(original, edited)
    expect(result).toHaveLength(300)
    expect(result?.[150].description).toBe('Changed deep in the set')
    expect(result?.[149].description).toBe('Item 149')
    expect(result?.[151].description).toBe('Item 151')
  })

  it('QA-DLI-3: round-trip property -- diffLineItems(x, x)-equivalent is undefined across a range of shapes, including negative/decimal/imported-blank content', () => {
    const shapes: LineFields[][] = [
      [lineFields()],
      [{ description: null, quantity: null, unit_price: null, line_total: null, line_tax: null }],
      [{ description: '', quantity: '', unit_price: '', line_total: '', line_tax: '' }],
      [lineFields({ unit_price: '-50.00', line_total: '-100.00' })],
      [lineFields({ unit_price: '0.0000000001' })],
      [lineFields({ quantity: '0' })],
      [lineFields(), lineFields({ description: 'Second', unit_price: '-1.00' })],
      [],
    ]

    for (const shape of shapes) {
      // A fresh, content-identical copy on the edited side -- never the same array
      // reference -- so this is a genuine content comparison, not a reference check.
      const copy: LineFields[] = shape.map((l) => ({ ...l }))
      expect(diffLineItems(shape, copy), JSON.stringify(shape)).toBeUndefined()
    }
  })
})

// --- INVCR-01-14 (task-290, Stage 2.5/Mode A) — RED specs for the row-expansion fix
// editor's two data-access integration points: a per-invoice re-validate that never
// touches the import, and the editInvoice+diffLineItems composition the row editor
// uses. FIX-7 closes a genuine gap: INV-06-T10 (above) already proves an OMITTED
// can_revalidate normalizes to false, but `?? false` and `=== true` behave IDENTICALLY
// on `undefined` -- omission alone cannot discriminate the two implementations. Only a
// non-boolean TRUTHY value (T10c's own technique, there applied to can_edit only) does.

describe('revalidateInvoice: a per-invoice re-validate never touches the import (FIX-5, INVCR-01-14)', () => {
  it("FIX-5: re-validating 'u-9' fires exactly one POST .../invoices/u-9/validate and no import-endpoint call — fixing a row never restarts the import", async () => {
    const validated: InvoiceRecord = { ...draftInvoice, id: 'u-9', status: 'validated', rule_set_version: 3 }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(validated) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await revalidateInvoice(af, base, 'u-9')

    expect(result).toEqual(validated)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw/api/invoice/v1/invoices/u-9/validate')
    expect(url).not.toContain('/imports')
    expect(init.method).toBe('POST')
  })
})

describe('getInvoice: can_revalidate specifically fails closed on a non-boolean truthy value (FIX-7, INVCR-01-14)', () => {
  it('FIX-7: a stringly-typed "false" can_revalidate normalizes to false — the mutation oracle for === true over ?? false on THIS flag (INV-06-T10c only covers can_edit)', async () => {
    const wire = { ...draftInvoice, can_edit: true, can_revalidate: 'false', revalidate_blocked_reason: null }
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(wire) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.can_revalidate).toBe(false)
    // Positive companion, mirroring T10c's own shape: can_edit's genuine `true` on the
    // SAME payload still passes through, proving the denial is about can_revalidate's
    // own value, not a side effect that zeroes every flag.
    expect(result.can_edit).toBe(true)
  })
})

describe('editInvoice + diffLineItems together: the row-expansion editor\'s own composition (FIX-8, INVCR-01-14)', () => {
  it('FIX-8: changing only vat, with line items run through diffLineItems unchanged, produces a wire body carrying vat and NO line_items key', async () => {
    const lines: LineFields[] = [lineFields()]
    const updated: InvoiceRecord = { ...draftInvoice, vat: '999.00' }
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(updated) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    // The row-expansion editor never touches line items — it always diffs the SAME
    // content it loaded against itself, which diffLineItems (INV-06-T7) already pins as
    // undefined. Asserted again HERE as the exact composition the fix editor performs:
    // build the header patch, conditionally attach diffLineItems' result, then call
    // editInvoice once.
    const patch: InvoiceEditInput = { vat: '999.00' }
    const diffed = diffLineItems(lines, lines)
    if (diffed !== undefined) patch.line_items = diffed

    await editInvoice(af, base, 'inv-1', patch)

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.body).toBe(JSON.stringify({ vat: '999.00' }))
    expect(init.body).not.toContain('line_items')
  })
})

// RED specs (task-329, BUG-01-03, Mode A) -- the register's own pagination, envelope
// emptiness, and page-scoped selection, ahead of the InvoicesList.tsx fix. REGISTER_PAGE_SIZE
// and invoiceListIsEmpty don't exist yet -- read off the module namespace through an index
// signature (mirrors glyphs.test.ts's own idiom) so a missing export fails as an `undefined`
// ASSERTION, never an import/compile error.
const invoicesNS = invoicesModule as unknown as Record<string, unknown>

describe('listInvoices sends limit and offset for the register page', () => {
  it('a register-sized page ({limit:50, offset:100}) puts both on the wire', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 100, total: 259 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { limit: 50, offset: 100 } as ListInvoicesOptions)

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    const parsed = new URL(url)
    expect(parsed.searchParams.get('limit')).toBe('50')
    expect(parsed.searchParams.get('offset')).toBe('100')
  })

  // Regression guard, not a red bug: listInvoices already emits offset via `!= null`
  // (lib/invoices.ts:425-426, LIST-4 above already pins this) -- restated here against the
  // register's own page-1 request shape so a future truthiness regression on THIS call
  // shape fails here too, not only on LIST-4's generic one.
  it('offset:0 (page 1) is still emitted, not dropped by a falsy-zero guard', async () => {
    const fetchMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 259 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { limit: 50, offset: 0 } as ListInvoicesOptions)

    const [url] = fetchMock.mock.calls[0] as [string, RequestInit]
    const parsed = new URL(url)
    expect(parsed.searchParams.get('limit')).toBe('50')
    expect(parsed.searchParams.get('offset')).toBe('0')
  })
})

describe('REGISTER_PAGE_SIZE is 50 and stays under BATCH_SUBMIT_MAX_IDS', () => {
  it('REGISTER_PAGE_SIZE === 50, and a full page can never breach the batch-submit id cap', () => {
    const registerPageSize = invoicesNS.REGISTER_PAGE_SIZE
    expect(registerPageSize, 'REGISTER_PAGE_SIZE is not exported by invoices.ts yet').toBeDefined()
    expect(registerPageSize).toBe(50)
    expect(registerPageSize as number).toBeLessThan(BATCH_SUBMIT_MAX_IDS)
  })
})

describe('invoiceListIsEmpty resolves empty from the set, not the page', () => {
  it('mid-set empty page is false, genuine zero-total is true, a populated page is false', () => {
    const invoiceListIsEmpty = invoicesNS.invoiceListIsEmpty as ((r: unknown) => boolean) | undefined
    // Guard first, and the ONLY unguarded call below reads through this same checked
    // reference -- a missing export fails right here (an assertion), never as a
    // "not a function" crash further down.
    expect(invoiceListIsEmpty, 'invoiceListIsEmpty is not exported by invoices.ts yet').toBeDefined()

    // [empty-is-total-zero]'s own trap: total>0 but this page's own slice is [].
    const midSetEmptyPage = { invoices: [], pagination: { limit: 50, offset: 100, total: 259 } }
    expect(invoiceListIsEmpty!(midSetEmptyPage)).toBe(false)

    const genuineEmpty = { invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }
    expect(invoiceListIsEmpty!(genuineEmpty)).toBe(true)

    const populatedPage = { invoices: [draftInvoice], pagination: { limit: 50, offset: 0, total: 1 } }
    expect(invoiceListIsEmpty!(populatedPage)).toBe(false)
  })
})

// QA adversarial (task-329 review of InvoicesList.tsx's `state === 'ready' && list.data
// != null && rows.length === 0` mid-set-empty-page gate): the spec'd condition was
// `total > 0 && rows.length === 0`. Formally, through the SAME reducer + isEmpty wiring
// InvoicesList.tsx actually uses (asyncReducer + invoiceListIsEmpty as the `isEmpty`
// option), status can only ever reach 'ready' when total>0, and 'ready' is the only
// status that keeps `data` non-null -- so the two conditions coincide for every envelope
// this app can produce. If a future isEmpty swap breaks that coincidence, this fails here
// rather than silently reintroducing the mid-set-empty-page bug under a different name.
describe('claim: state==="ready" implies pagination.total>0, given invoiceListIsEmpty as isEmpty', () => {
  it('total>0 resolves ready with the envelope intact (data stays the SAME reference)', () => {
    const envelope: InvoiceListResponse = { invoices: [], pagination: { limit: 50, offset: 100, total: 259 } }
    const next = asyncReducer(initialState<InvoiceListResponse>(true), { type: 'success', data: envelope, isEmpty: invoicesModule.invoiceListIsEmpty })

    expect(next.status).toBe('ready')
    expect(next.data).toBe(envelope)
  })

  it('total===0 can only resolve empty, never ready -- the ready-gated rung is unreachable for it', () => {
    const envelope: InvoiceListResponse = { invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }
    const next = asyncReducer(initialState<InvoiceListResponse>(true), { type: 'success', data: envelope, isEmpty: invoicesModule.invoiceListIsEmpty })

    expect(next.status).toBe('empty')
    expect(next.data).toBeNull()
  })
})

describe('selectAllState and selectableIds never span beyond the given rows', () => {
  it('a 50-row page with 12 selectable rows selects exactly those 12', () => {
    const rows: InvoiceRecord[] = Array.from({ length: 50 }, (_, i) => ({
      ...draftInvoice,
      id: `inv-${i}`,
      status: i < 12 ? 'validated' : 'draft',
    }))

    const ids = selectableIds(rows)
    expect(ids).toHaveLength(12)
    const rowIds = new Set(rows.map((r) => r.id))
    for (const id of ids) expect(rowIds.has(id), `${id} must be one of the given rows`).toBe(true)

    expect(selectAllState(ids, rows)).toBe('all')
  })
})

// RED specs (task-331, BUG-01-05, Mode A): clampFilterText doesn't exist yet -- read off
// the module namespace (mirrors REGISTER_PAGE_SIZE/invoiceListIsEmpty's own idiom above)
// so a missing export fails as an assertion, never an import/compile error.
describe('clampFilterText (BUG-01-05)', () => {
  function clamp(s: string): string {
    const fn = invoicesNS.clampFilterText as ((s: string) => string) | undefined
    expect(fn, 'clampFilterText is not exported by invoices.ts yet').toBeDefined()
    return fn!(s)
  }

  it('ASCII table cases: 250 chars clamps to 200, 200 chars is unchanged, empty stays empty', () => {
    expect(clamp('x'.repeat(250))).toHaveLength(200)
    expect(clamp('x'.repeat(200))).toBe('x'.repeat(200))
    expect(clamp('')).toBe('')
  })

  // The server's cap is 200 BYTES, not runes (handlers.go:305-316/488-492, `len(q)` on a
  // Go string counts UTF-8 bytes). A naive value.slice(0, 200) counts UTF-16 code units
  // instead, so multi-byte content passes a char-count check and still 400s server-side.
  it('a 100-char CJK string (3 bytes/char, 300 bytes) clamps to whole characters within 200 bytes', () => {
    const cjk = '文'.repeat(100)

    const result = clamp(cjk)

    expect(new TextEncoder().encode(result).length).toBeLessThanOrEqual(200)
    expect(result).not.toContain('�')
    // 66 whole chars = 198 bytes; a 67th would push it to 201, over the cap.
    expect(result).toBe('文'.repeat(66))
  })

  it('a 60-emoji string (4 bytes/char, 2 UTF-16 units/char, 240 bytes) clamps to whole characters within 200 bytes', () => {
    const emoji = '😀'.repeat(60)

    const result = clamp(emoji)

    expect(new TextEncoder().encode(result).length).toBeLessThanOrEqual(200)
    expect(result).not.toContain('�')
    // 50 whole emoji = 200 bytes exactly; a 51st would push it to 204.
    expect(result).toBe('😀'.repeat(50))
  })

  // The byte cutoff (200) falls mid-character here: 199 ASCII bytes leave a 1-byte budget,
  // not enough for the trailing CJK char's 3-byte sequence. A byte-slice-then-decode
  // implementation would split that sequence and surface a replacement char (U+FFFD) on
  // decode; the correct clamp drops the whole character instead of a partial one.
  it('a byte cutoff that falls mid-character drops the whole character, never a partial one', () => {
    const mixed = 'a'.repeat(199) + '文'

    const result = clamp(mixed)

    expect(new TextEncoder().encode(result).length).toBeLessThanOrEqual(200)
    expect(result).not.toContain('�')
    expect(result).toBe('a'.repeat(199))
  })
})

// QA adversarial (task-331, BUG-01-05): the byte-cutoff boundary itself, exhaustively --
// the RED specs above hit "mid-character" and one exact multiple-of-char-width boundary
// (the 60-emoji case), but never the two single-byte-decision edges the while loop's
// condition actually turns on, nor every offset a 4-byte sequence can straddle.
describe('clampFilterText: exhaustive boundary decisions (QA adversarial)', () => {
  const emoji = '😀' // F0 9F 98 80 -- 4 bytes, 2 UTF-16 code units
  const clamp = clampFilterText

  // bytes[200] itself is the LEAD byte of the next character (11110xxx, &0xc0 === 0xc0):
  // the while loop's condition is false on the first check, so `end` never decrements.
  // Correct, because .subarray(0, 200) already excludes byte 200 -- the character
  // starting there contributes zero bytes to the output, so there is nothing to split.
  it('byte 200 is a lead byte: the loop does not decrement, and the character is cleanly excluded whole', () => {
    const s = 'a'.repeat(200) + emoji
    const result = clamp(s)

    expect(new TextEncoder().encode(result).length).toBe(200)
    expect(result).not.toContain('�')
    expect(result).toBe('a'.repeat(200))
  })

  // bytes[200] is a plain ASCII byte (0xxxxxxx, &0xc0 === 0x00): same non-decrementing
  // path as the lead-byte case, for the opposite reason (nothing multi-byte anywhere
  // near the cut). Restates the existing 250-char ASCII case as an explicit boundary
  // check rather than an incidental one.
  it('byte 200 is a plain ASCII byte: the loop does not decrement', () => {
    const s = 'a'.repeat(210)
    const result = clamp(s)

    expect(new TextEncoder().encode(result).length).toBe(200)
    expect(result).toBe('a'.repeat(200))
  })

  // Every offset a 4-byte character can straddle the cut at: 0 bytes of it included (the
  // lead-byte case above, restated inline for the loop below), 1/2/3 bytes included (the
  // cut lands on one of its three continuation bytes, and the loop must back off through
  // ALL of them to reach the lead byte, not stop at the first continuation byte it sees),
  // and all 4 bytes included (the character fits and the cut is clean ASCII beyond it).
  // Never partial: the emoji is either wholly present or wholly absent, and the result is
  // never a stray lone surrogate (`result.length` would report an odd-looking value) or a
  // U+FFFD replacement char.
  it.each([0, 1, 2, 3, 4])('a 4-byte emoji with %i of its bytes before the 200-byte cut is never split', (bytesIncluded) => {
    const prefixLen = 200 - bytesIncluded
    const s = 'a'.repeat(prefixLen) + emoji + 'a'.repeat(10)

    const result = clamp(s)
    const byteLen = new TextEncoder().encode(result).length

    expect(byteLen, `bytesIncluded=${bytesIncluded}`).toBeLessThanOrEqual(200)
    expect(result, `bytesIncluded=${bytesIncluded}`).not.toContain('�')

    if (bytesIncluded === 4) {
      // The whole emoji fits inside the 200-byte budget -- included whole.
      expect(result, `bytesIncluded=${bytesIncluded}`).toBe('a'.repeat(prefixLen) + emoji)
      expect(byteLen, `bytesIncluded=${bytesIncluded}`).toBe(200)
    } else {
      // 0/1/2/3 bytes of the emoji before the cut -- the loop backs off past every one
      // of them, dropping the whole character rather than emitting a partial sequence.
      expect(result, `bytesIncluded=${bytesIncluded}`).toBe('a'.repeat(prefixLen))
    }
  })

  // `end === 0` (a clamp with nothing left) would require a single Unicode code point to
  // encode past 200 bytes -- impossible, since UTF-8 caps every code point (up to
  // U+10FFFF) at 4 bytes. The tightest real case is many consecutive 4-byte characters
  // near the cut: the loop can back off at most 3 bytes (the longest run of continuation
  // bytes any single character has) before it must hit a lead byte, so `end` can never
  // fall below (200 - 3) when bytes.length > 200. Asserted here over a run of ten
  // consecutive emoji straddling the cut, rather than argued only in a comment.
  it('a run of consecutive 4-byte characters at the cut still resolves to a valid, non-empty clamp', () => {
    const s = 'a'.repeat(197) + emoji.repeat(10)

    const result = clamp(s)
    const byteLen = new TextEncoder().encode(result).length

    expect(byteLen).toBeGreaterThan(0)
    expect(byteLen).toBeLessThanOrEqual(200)
    expect(result).not.toContain('�')
    // 197 'a' bytes + 0 whole emoji (the first emoji's lead byte lands at 197, well
    // inside the 3-byte decrement budget from 200) -- confirms the loop lands exactly
    // where the arithmetic above predicts, not merely "somewhere valid".
    expect(result).toBe('a'.repeat(197))
  })

  // TextDecoder() defaults to fatal:false, which SILENTLY substitutes U+FFFD for a
  // malformed sequence rather than throwing -- clampFilterText relies on its input
  // always being a valid boundary (never fatal, by construction), so this documents
  // that reliance is actually load-bearing: decoding a deliberately-broken sequence
  // (with the same non-fatal decoder clampFilterText uses) DOES produce a replacement
  // char, proving the earlier "no U+FFFD" assertions are a real signal, not vacuously
  // true because TextDecoder can't ever produce one.
  it('control: a genuinely split multi-byte sequence DOES decode to U+FFFD without {fatal:true} -- proving the no-\\uFFFD assertions above are meaningful', () => {
    const bytes = new TextEncoder().encode('a'.repeat(199) + '文')
    const brokenSlice = bytes.subarray(0, 200) // splits the 3-byte CJK char after 1 byte
    const decoded = new TextDecoder().decode(brokenSlice)

    expect(decoded).toContain('�')
  })
})

describe('listInvoices emits q only when non-empty (BUG-01-05)', () => {
  // Green-before: listInvoices' own q handling shipped with BUG-01-04 (LIST-1b/I17's
  // precedent for the other string filters). Restated here as the header-search story's
  // own AC row, over the exact values it uses.
  it('{q: "INV-9001"} builds a URL containing q=INV-9001; {q: ""} omits the param entirely', async () => {
    const withQueryMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await listInvoices(af, base, { q: 'INV-9001' })
    const [withQueryUrl] = withQueryMock.mock.calls[0] as [string, RequestInit]
    expect(new URL(withQueryUrl).searchParams.get('q')).toBe('INV-9001')

    const emptyQueryMock = mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [], pagination: { limit: 50, offset: 0, total: 0 } }),
    })
    await listInvoices(af, base, { q: '' })
    const [emptyQueryUrl] = emptyQueryMock.mock.calls[0] as [string, RequestInit]
    expect(emptyQueryUrl).not.toContain('q=')
  })
})

// Recursively walks `rootDir`, reading every .ts/.tsx file, and returns the relative paths
// of every file whose text contains `needle` as a literal substring.
function scanForIdentifier(rootDir: string, needle: string): string[] {
  const hits: string[] = []
  function walk(dir: string): void {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name)
      if (entry.isDirectory()) {
        walk(full)
      } else if (/\.(ts|tsx)$/.test(entry.name) && readFileSync(full, 'utf8').includes(needle)) {
        hits.push(path.relative(rootDir, full))
      }
    }
  }
  walk(rootDir)
  return hits
}

// RED specs (task-332, BUG-01-06, Mode A) -- hasBlockingViolation doesn't exist yet.
// Read off invoicesNS (defined above, same index-signature idiom as glyphs.test.ts) so a
// missing export fails as an undefined ASSERTION (undefined !== the expected boolean),
// never an import/compile error.
type HasBlockingViolationFn = (inv: Pick<InvoiceRecord, 'violations'>) => boolean | undefined
function hasBlockingViolationUnderTest(): HasBlockingViolationFn {
  const fn = invoicesNS.hasBlockingViolation as HasBlockingViolationFn | undefined
  return (inv) => fn?.(inv)
}

describe('hasBlockingViolation (task-332, BUG-01-06)', () => {
  it('is true only for an error-severity violation', () => {
    const call = hasBlockingViolationUnderTest()
    const cases: Array<{ label: string; violations: InvoiceRecord['violations']; expected: boolean }> = [
      { label: 'empty', violations: [], expected: false },
      { label: 'warning only', violations: [{ rule_key: 'r1', severity: 'warning', message: 'm' }], expected: false },
      { label: 'info only', violations: [{ rule_key: 'r1', severity: 'info', message: 'm' }], expected: false },
      { label: 'error only', violations: [{ rule_key: 'r1', severity: 'error', message: 'm' }], expected: true },
      {
        label: 'mixed, one error',
        violations: [
          { rule_key: 'r1', severity: 'warning', message: 'm' },
          { rule_key: 'r2', severity: 'error', message: 'm' },
        ],
        expected: true,
      },
    ]
    for (const { label, violations, expected } of cases) {
      expect(call({ violations }), label).toBe(expected)
    }
  })

  // The attention set includes `rejected` on status ALONE (server-side needs_attention) --
  // this helper must never re-derive that from an empty violations array, or the two
  // predicates would silently drift out of sync with each other (Out of Scope fence).
  it('does not restate needs_attention -- a rejected invoice with violations:[] is still false', () => {
    const call = hasBlockingViolationUnderTest()
    const rejectedNoViolations: InvoiceRecord = { ...draftInvoice, status: 'rejected', violations: [] }
    expect(call(rejectedNoViolations)).toBe(false)
  })
})

// Regression guard (task-332, BUG-01-06), not a red bug: shouldShowRejectionCard already
// behaves this way (R-1/R-2 above). Restated over a `failed` invoice specifically, since
// that's the status this story's honest-line addition touches.
describe('shouldShowRejectionCard over a failed invoice (regression guard, BUG-01-06)', () => {
  it('reasons present shows the card; [] does not', () => {
    expect(shouldShowRejectionCard({ status: 'failed', rejection_reasons: [{ code: 'X', message: 'y' }] })).toBe(true)
    expect(shouldShowRejectionCard({ status: 'failed', rejection_reasons: [] })).toBe(false)
  })
})

// RED specs (task-333, BUG-01-07, Mode A) -- AGGREGATE_PAGE_SIZE/AGGREGATE_MAX_PAGES/
// remainingPageOffsets/AllInvoices/allInvoicesIsEmpty/fetchAllInvoices don't exist yet.
// Read off invoicesNS (defined above, same index-signature idiom as glyphs.test.ts /
// hasBlockingViolationUnderTest above) so a missing export fails as an ASSERTION, never
// an import/compile error. AllInvoices is a TYPE, not a runtime value -- mirrored locally
// as AllInvoicesShape rather than imported, same reason clampFilterText's own RED section
// never imports a not-yet-existing symbol by name.
interface AllInvoicesShape {
  invoices: InvoiceRecord[]
  total: number
  fetched: number
  truncated: boolean
}

type FetchAllInvoicesOpts = Omit<ListInvoicesOptions, 'limit' | 'offset'>

type RemainingPageOffsetsFn = (total: number, limit: number, maxPages: number) => { offsets: number[]; truncated: boolean }
type FetchAllInvoicesFn = (authedFetch: AuthedFetch, base: string, opts: FetchAllInvoicesOpts) => Promise<AllInvoicesShape>
type AllInvoicesIsEmptyFn = (r: AllInvoicesShape) => boolean

function remainingPageOffsetsUnderTest(total: number, limit: number, maxPages: number): { offsets: number[]; truncated: boolean } {
  const fn = invoicesNS.remainingPageOffsets as RemainingPageOffsetsFn | undefined
  expect(fn, 'remainingPageOffsets is not exported by invoices.ts yet').toBeDefined()
  return fn!(total, limit, maxPages)
}

function fetchAllInvoicesUnderTest(
  authedFetch: AuthedFetch,
  base: string,
  opts: FetchAllInvoicesOpts = {},
): Promise<AllInvoicesShape> {
  const fn = invoicesNS.fetchAllInvoices as FetchAllInvoicesFn | undefined
  expect(fn, 'fetchAllInvoices is not exported by invoices.ts yet').toBeDefined()
  return fn!(authedFetch, base, opts)
}

function allInvoicesIsEmptyUnderTest(r: AllInvoicesShape): boolean {
  const fn = invoicesNS.allInvoicesIsEmpty as AllInvoicesIsEmptyFn | undefined
  expect(fn, 'allInvoicesIsEmpty is not exported by invoices.ts yet').toBeDefined()
  return fn!(r)
}

function invoiceRow(id: string): InvoiceRecord {
  return { ...draftInvoice, id }
}

function requestedOffset(url: string): number {
  return Number(new URL(url).searchParams.get('offset') ?? '0')
}

function requestedOffsets(fetchMock: { mock: { calls: unknown[][] } }): number[] {
  return (fetchMock.mock.calls as [string, RequestInit][]).map(([url]) => requestedOffset(url))
}

// A full page of `count` rows echoing `{limit, offset, total}` -- the shape every
// fetchAllInvoices page request resolves to.
function pageResponse(limit: number, offset: number, total: number, count: number): MockResponse {
  return {
    ok: true,
    status: 200,
    json: () =>
      Promise.resolve({
        invoices: Array.from({ length: count }, (_, i) => invoiceRow(`inv-${offset}-${i}`)),
        pagination: { limit, offset, total },
      }),
  }
}

function errorResponse(status: number): MockResponse {
  return { ok: false, status, statusText: 'boom', json: () => Promise.resolve({ error: 'boom' }) }
}

function stubFetch(impl: (url: string) => MockResponse | Promise<MockResponse>) {
  const fetchMock = vi.fn((url: string, _init?: RequestInit) => Promise.resolve(impl(url)))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// Cheap bounded microtask wait -- fetchAllInvoices' page-1 await needs a few ticks to
// unwind (listInvoices -> authedFetch -> apiFetch -> res.json()) before the fan-out's
// remaining fetch() calls happen; bounded so a broken stub fails fast, not hangs.
async function waitUntil(predicate: () => boolean, maxTicks = 50): Promise<void> {
  for (let i = 0; i < maxTicks && !predicate(); i++) await Promise.resolve()
}

describe('AGGREGATE_PAGE_SIZE / AGGREGATE_MAX_PAGES (task-333, BUG-01-07)', () => {
  it('mirror the server ceiling (200) and the 2000-invoice cap (10 pages)', () => {
    const pageSize = invoicesNS.AGGREGATE_PAGE_SIZE
    const maxPages = invoicesNS.AGGREGATE_MAX_PAGES
    expect(pageSize, 'AGGREGATE_PAGE_SIZE is not exported by invoices.ts yet').toBeDefined()
    expect(maxPages, 'AGGREGATE_MAX_PAGES is not exported by invoices.ts yet').toBeDefined()
    expect(pageSize).toBe(200)
    expect(maxPages).toBe(10)
  })
})

describe('remainingPageOffsets (task-333, BUG-01-07)', () => {
  it('remainingPageOffsets returns nothing when the set fits one page', () => {
    expect(remainingPageOffsetsUnderTest(200, 200, 10)).toEqual({ offsets: [], truncated: false })
    expect(remainingPageOffsetsUnderTest(0, 200, 10)).toEqual({ offsets: [], truncated: false })
    expect(remainingPageOffsetsUnderTest(1, 200, 10)).toEqual({ offsets: [], truncated: false })
  })

  it('remainingPageOffsets covers the set', () => {
    expect(remainingPageOffsetsUnderTest(259, 200, 10)).toEqual({ offsets: [200], truncated: false })
    expect(remainingPageOffsetsUnderTest(600, 200, 10)).toEqual({ offsets: [200, 400], truncated: false })
  })

  it('remainingPageOffsets caps and reports truncation', () => {
    const result = remainingPageOffsetsUnderTest(5000, 200, 10)
    expect(result.offsets).toEqual([200, 400, 600, 800, 1000, 1200, 1400, 1600, 1800])
    expect(result.truncated).toBe(true)
  })

  it('remainingPageOffsets guards a nonsensical limit', () => {
    const result = remainingPageOffsetsUnderTest(500, 0, 10)
    expect(result).toEqual({ offsets: [], truncated: false })
    expect(Number.isFinite(result.offsets.length)).toBe(true)
  })

  // Implementation Notes #2 (exact-cap boundary): total === maxPages*limit exactly (2000
  // with 10x200) means the cap was reached with nothing left over -- truncated is false.
  // The Test Specs table only covers clearly-over (5000) and clearly-under.
  it('is not truncated when total lands exactly on the cap boundary', () => {
    const result = remainingPageOffsetsUnderTest(2000, 200, 10)
    expect(result.offsets).toHaveLength(9)
    expect(result.truncated).toBe(false)
  })
})

describe('fetchAllInvoices (task-333, BUG-01-07)', () => {
  it('fetchAllInvoices concatenates every page of the set', async () => {
    const fetchMock = stubFetch((url) => {
      const offset = requestedOffset(url)
      if (offset === 0) return pageResponse(200, 0, 259, 200)
      if (offset === 200) return pageResponse(200, 200, 259, 59)
      throw new Error(`unexpected offset requested: ${offset}`)
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await fetchAllInvoicesUnderTest(af, base, {})

    expect(result.invoices).toHaveLength(259)
    expect(result.total).toBe(259)
    expect(result.fetched).toBe(259)
    expect(result.truncated).toBe(false)
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(requestedOffsets(fetchMock).sort((a, b) => a - b)).toEqual([0, 200])
    // Page 1 is requested at AGGREGATE_PAGE_SIZE -- the client doesn't know what the
    // server will echo back until it answers.
    const [firstUrl] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(new URL(firstUrl).searchParams.get('limit')).toBe('200')
  })

  it('fetchAllInvoices paginates on the echoed limit, not the constant', async () => {
    const fetchMock = stubFetch((url) => {
      const offset = requestedOffset(url)
      if (offset === 0) return pageResponse(50, 0, 130, 50)
      if (offset === 50) return pageResponse(50, 50, 130, 50)
      if (offset === 100) return pageResponse(50, 100, 130, 30)
      throw new Error(`unexpected offset requested: ${offset}`)
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await fetchAllInvoicesUnderTest(af, base, {})

    // An implementation that trusts AGGREGATE_PAGE_SIZE (200) instead of the echoed
    // limit (50) would see total:130 <= 200 and request only offset 0 -- this is the
    // one assertion that catches it.
    expect(requestedOffsets(fetchMock).sort((a, b) => a - b)).toEqual([0, 50, 100])
    expect(result.invoices).toHaveLength(130)
    expect(result.total).toBe(130)
  })

  it('fetchAllInvoices reports truncation at the cap', async () => {
    const fetchMock = stubFetch((url) => pageResponse(200, requestedOffset(url), 5000, 200))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await fetchAllInvoicesUnderTest(af, base, {})

    expect(result.truncated).toBe(true)
    expect(result.fetched).toBe(2000)
    expect(result.invoices).toHaveLength(2000)
    expect(fetchMock).toHaveBeenCalledTimes(10)
    expect(requestedOffsets(fetchMock).sort((a, b) => a - b)).toEqual([0, 200, 400, 600, 800, 1000, 1200, 1400, 1600, 1800])
  })

  it('fetchAllInvoices forwards entityId to every page', async () => {
    const fetchMock = stubFetch((url) => pageResponse(200, requestedOffset(url), 400, 200))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await fetchAllInvoicesUnderTest(af, base, { entityId: 'E' })

    expect(fetchMock).toHaveBeenCalledTimes(2)
    for (const [url] of fetchMock.mock.calls as [string, RequestInit][]) {
      expect(new URL(url).searchParams.get('entity_id')).toBe('E')
    }
  })

  // Build the stub so a SERIAL implementation deadlocks: each remaining-page request
  // hangs until every remaining offset (200/400/600/800, from a total:1000 set) has been
  // seen. .map()-then-Promise.all issues all four synchronously, so the last one observed
  // unblocks all four at once; an await-in-a-loop implementation would never even issue
  // the 2nd..4th request (it awaits the 1st's promise first, which never resolves), so
  // this test times out against it instead of passing.
  it('fetchAllInvoices issues the pages after the first concurrently', async () => {
    const wantOffsets = [200, 400, 600, 800]
    const requested = new Set<number>()
    const pending: Array<() => void> = []
    const fetchMock = stubFetch((url) => {
      const offset = requestedOffset(url)
      if (offset === 0) return pageResponse(200, 0, 1000, 200)
      requested.add(offset)
      return new Promise<MockResponse>((resolve) => {
        pending.push(() => resolve(pageResponse(200, offset, 1000, 200)))
        if (wantOffsets.every((o) => requested.has(o))) for (const release of pending.splice(0)) release()
      })
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await fetchAllInvoicesUnderTest(af, base, {})

    expect(result.invoices).toHaveLength(1000)
    expect(requested.size).toBe(4)
    expect(fetchMock).toHaveBeenCalledTimes(5)
  })

  it('fetchAllInvoices concatenates in ascending-offset order whatever the resolution order', async () => {
    const resolvers = new Map<number, (v: MockResponse) => void>()
    const rowFor = (offset: number): MockResponse => ({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ invoices: [invoiceRow(`row-${offset}`)], pagination: { limit: 200, offset, total: 600 } }),
    })
    stubFetch((url) => {
      const offset = requestedOffset(url)
      if (offset === 0) return rowFor(0)
      return new Promise<MockResponse>((resolve) => resolvers.set(offset, resolve))
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const resultPromise = fetchAllInvoicesUnderTest(af, base, {})
    await waitUntil(() => resolvers.size === 2)
    expect(resolvers.has(200), 'offset 200 was not requested').toBe(true)
    expect(resolvers.has(400), 'offset 400 was not requested').toBe(true)

    // Resolve 400 BEFORE 200, deliberately out of offset order.
    resolvers.get(400)!(rowFor(400))
    resolvers.get(200)!(rowFor(200))

    const result = await resultPromise
    expect(result.invoices.map((inv) => inv.id)).toEqual(['row-0', 'row-200', 'row-400'])
  })
})

// Implementation Notes #1 (partial failure): Promise.all's default behaviour -- one
// rejected page rejects the WHOLE call, no partial-results-plus-flag degradation. Matches
// listInvoices' own non-2xx-rejects-unchanged contract (invoices.ts:55-56).
describe('fetchAllInvoices partial failure (task-333, BUG-01-07)', () => {
  it('rejects the whole call, carrying the underlying error unchanged, when one page of the fan-out rejects', async () => {
    stubFetch((url) => {
      const offset = requestedOffset(url)
      return offset === 200 ? errorResponse(500) : pageResponse(200, offset, 600, 200)
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => fetchAllInvoicesUnderTest(af, base, {}))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('http')
    expect((err as ApiError).status).toBe(500)
  })
})

describe('allInvoicesIsEmpty (task-333, BUG-01-07)', () => {
  it('allInvoicesIsEmpty is true only when the set is empty', () => {
    expect(allInvoicesIsEmptyUnderTest({ invoices: [], total: 0, fetched: 0, truncated: false })).toBe(true)
    expect(allInvoicesIsEmptyUnderTest({ invoices: [draftInvoice], total: 259, fetched: 259, truncated: false })).toBe(false)
  })
})

// QA adversarial coverage (task-333, BUG-01-07) -- gaps the RED Test Specs table didn't
// require: remainingPageOffsets under malformed/extreme inputs, fetched's true semantics,
// every filter (not just entityId) forwarded, and no unhandled rejection leaking from the
// sibling in-flight requests on partial failure.
describe('remainingPageOffsets under extreme/malformed inputs (QA adversarial)', () => {
  it.each([
    ['negative total', -100, 200, 10],
    ['negative limit', 500, -5, 10],
    ['NaN total', NaN, 200, 10],
    ['NaN limit', 500, NaN, 10],
    ['Infinity limit', 500, Infinity, 10],
    ['maxPages 0', 500, 200, 0],
    ['maxPages 1', 500, 200, 1],
    ['total 0', 0, 200, 10],
  ] as const)('%s never emits NaN/Infinity/negative offsets, bounded by maxPages', (_label, total, limit, maxPages) => {
    const { offsets, truncated } = remainingPageOffsetsUnderTest(total, limit, maxPages)
    for (const offset of offsets) {
      expect(Number.isFinite(offset), `offset ${offset} is not finite`).toBe(true)
      expect(offset, `offset ${offset} is negative`).toBeGreaterThanOrEqual(0)
    }
    // Pages 2..maxPages, so at most maxPages-1 offsets -- clamped at 0 since maxPages
    // itself may be <=1 here (a length can never be negative).
    expect(offsets.length).toBeLessThanOrEqual(Math.max(maxPages - 1, 0))
    expect(typeof truncated).toBe('boolean')
  })

  // The one case where "more than the cap" is unambiguous even with a non-finite total --
  // exact values, not just bounds, to prove the fill-to-cap arithmetic is right.
  it('total: Infinity fills to the cap with finite offsets and reports truncated', () => {
    const { offsets, truncated } = remainingPageOffsetsUnderTest(Infinity, 200, 10)
    expect(offsets).toEqual([200, 400, 600, 800, 1000, 1200, 1400, 1600, 1800])
    expect(offsets.every(Number.isFinite)).toBe(true)
    expect(truncated).toBe(true)
  })

  // KNOWN GAP (report-only, not fixed here): the only real caller passes the server-echoed
  // pagination.limit, always a JSON integer, so this is unreachable in production -- but
  // the pure function does not itself guard a fractional limit >= 1, and emits fractional
  // offsets rather than rejecting or rounding. Pinned so this doesn't silently regress
  // further (e.g. a future caller passing a computed, non-integer limit).
  it('a fractional limit >= 1 produces fractional (non-integer) offsets', () => {
    const { offsets } = remainingPageOffsetsUnderTest(500, 1.5, 10)
    expect(offsets.some((o) => !Number.isInteger(o))).toBe(true)
  })
})

describe('fetchAllInvoices "fetched" reflects actually retrieved rows, not total (QA adversarial)', () => {
  it('fetched equals invoices.length even when a later page returns fewer rows than total implies', async () => {
    stubFetch((url) => {
      const offset = requestedOffset(url)
      if (offset === 0) return pageResponse(200, 0, 300, 200)
      // total said 100 more rows exist at offset 200, but only 40 come back -- e.g. rows
      // were deleted between page 1 landing and this request.
      if (offset === 200) return pageResponse(200, 200, 300, 40)
      throw new Error(`unexpected offset requested: ${offset}`)
    })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await fetchAllInvoicesUnderTest(af, base, {})

    expect(result.invoices).toHaveLength(240)
    expect(result.fetched).toBe(240)
    expect(result.fetched).not.toBe(result.total)
    expect(result.total).toBe(300)
  })
})

describe('fetchAllInvoices forwards every filter, not just entityId (QA adversarial, AC #5)', () => {
  it('forwards needsAttention/status/needsFix/ruleKey/q/importBatchIds/keptAsIs to every page', async () => {
    const fetchMock = stubFetch((url) => pageResponse(200, requestedOffset(url), 400, 200))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await fetchAllInvoicesUnderTest(af, base, {
      entityId: 'E',
      needsAttention: true,
      status: 'rejected',
      needsFix: true,
      ruleKey: 'RULE-1',
      q: 'acme',
      importBatchIds: ['batch-1', 'batch-2'],
      keptAsIs: true,
      awaitingApproval: true,
    })

    expect(fetchMock).toHaveBeenCalledTimes(2)
    for (const [url] of fetchMock.mock.calls as [string, RequestInit][]) {
      const params = new URL(url).searchParams
      expect(params.get('entity_id')).toBe('E')
      expect(params.get('needs_attention')).toBe('true')
      expect(params.get('status')).toBe('rejected')
      expect(params.get('needs_fix')).toBe('true')
      expect(params.get('rule_key')).toBe('RULE-1')
      expect(params.get('q')).toBe('acme')
      expect(params.getAll('import_batch_id')).toEqual(['batch-1', 'batch-2'])
      expect(params.get('kept_as_is')).toBe('true')
      expect(params.get('awaiting_approval')).toBe('true')
    }
  })
})

describe('fetchAllInvoices partial failure (QA adversarial)', () => {
  it('carries the underlying message and body unchanged, not just kind/status', async () => {
    stubFetch((url) => (requestedOffset(url) === 200 ? errorResponse(500) : pageResponse(200, requestedOffset(url), 600, 200)))
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const err = await captureRejection(() => fetchAllInvoicesUnderTest(af, base, {}))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).message).toBe('boom')
    expect((err as ApiError).body).toEqual({ error: 'boom' })
  })

  // Promise.all attaches a handler to EVERY input promise (not just the first to settle),
  // so a sibling page rejecting alongside the one that wins the race must not surface as a
  // separate unhandled rejection -- that can fail an unrelated CI run under Node/Vitest's
  // unhandledRejection reporting.
  it('produces no unhandled promise rejection from sibling in-flight pages when more than one fails', async () => {
    const unhandled: unknown[] = []
    const onUnhandled = (reason: unknown) => unhandled.push(reason)
    process.on('unhandledRejection', onUnhandled)
    try {
      stubFetch((url) => {
        const offset = requestedOffset(url)
        if (offset === 200 || offset === 600) return errorResponse(500)
        return pageResponse(200, offset, 1000, 200)
      })
      const af = createAuthedFetch(() => 'tok', vi.fn())

      const err = await captureRejection(() => fetchAllInvoicesUnderTest(af, base, {}))
      expect(err).toBeInstanceOf(ApiError)

      // Let any dangling promise rejection surface before asserting none did.
      await new Promise((resolve) => setTimeout(resolve, 0))
      await Promise.resolve()
    } finally {
      process.off('unhandledRejection', onUnhandled)
    }
    expect(unhandled).toEqual([])
  })
})

// RED specs (BUG-06-05, task-387) -- pin failureExplanation()'s copy mapping before the
// executor implements it. Every spec below currently fails on the import of
// FAILURE_EXPLANATION_FALLBACK/FAILURE_KINDS/failureExplanation (none exist yet in
// invoices.ts), matching this file's own RED convention (see file header).
describe('failureExplanation', () => {
  it('FK-1: each of the three kinds returns its own headline and detail', () => {
    const results = FAILURE_KINDS.map((k) => failureExplanation(k))
    const headlines = results.map((r) => r.headline)
    const details = results.map((r) => r.detail)

    expect(new Set(headlines).size).toBe(headlines.length)
    expect(new Set(details).size).toBe(details.length)
    for (const r of results) {
      expect(r.headline.trim().length).toBeGreaterThan(0)
      expect(r.detail.trim().length).toBeGreaterThan(0)
    }
  })

  it('FK-2: no kind silently degrades to the fallback', () => {
    for (const k of FAILURE_KINDS) {
      const r = failureExplanation(k)
      expect(r, `kind=${k}`).not.toEqual(FAILURE_EXPLANATION_FALLBACK)
      expect(r.headline.trim().length, `kind=${k} headline`).toBeGreaterThan(0)
      expect(r.detail.trim().length, `kind=${k} detail`).toBeGreaterThan(0)
      expect(r.nextStep.trim().length, `kind=${k} nextStep`).toBeGreaterThan(0)
    }
  })

  it('FK-3: every branch returns a non-empty nextStep', () => {
    const inputs: Array<string | null> = [...FAILURE_KINDS, null, 'wat']
    for (const kind of inputs) {
      const r = failureExplanation(kind)
      expect(r.nextStep.trim().length, `kind=${String(kind)}`).toBeGreaterThan(0)
    }
  })

  it('FK-4: null degrades to the recorded-reason fallback', () => {
    let r: ReturnType<typeof failureExplanation> | undefined
    expect(() => {
      r = failureExplanation(null)
    }).not.toThrow()
    expect(r!.headline).toBeDefined()
    expect(r!.detail).toBeDefined()
    expect(r!.nextStep).toBeDefined()
    expect(r!.detail).toMatch(/not recorded/i)
  })

  it('FK-5: an unrecognised kind takes the same branch as null', () => {
    expect(failureExplanation('app_rejected')).toEqual(failureExplanation(null))
  })

  it('FK-6: no branch offers a retry, re-validate or edit', () => {
    const all: Array<string | null> = [...FAILURE_KINDS, null, 'wat']
    for (const kind of all) {
      const r = failureExplanation(kind)
      const joined = `${r.headline} ${r.detail} ${r.nextStep}`
      expect(joined, `kind=${String(kind)}`).not.toMatch(
        /\bedit|re-?validate|re-?submit|send it again|try again|retry|enter it again|enter this invoice again/i,
      )
    }
  })

  it('FK-7: no branch claims anyone has been notified', () => {
    const all: Array<string | null> = [...FAILURE_KINDS, null, 'wat']
    for (const kind of all) {
      const r = failureExplanation(kind)
      const joined = `${r.headline} ${r.detail} ${r.nextStep}`
      expect(joined, `kind=${String(kind)}`).not.toMatch(/notified|alerted|our team|we've been|looking into|monitor/i)
    }
  })

  it('FK-8: acknowledged_no_verdict warns that entering it again could file it twice', () => {
    const r = failureExplanation('acknowledged_no_verdict')
    expect(r.detail).toMatch(/twice|double/i)
  })

  it('FK-9: nothing from the wire is interpolated', () => {
    const hostile = 'https://app.internal:8443/x?token=sk_live_abc'
    expect(failureExplanation(hostile)).toEqual(failureExplanation(null))
  })

  it('FK-10: no branch uses internal vocabulary', () => {
    const all: Array<string | null> = [...FAILURE_KINDS, null, 'wat']
    for (const kind of all) {
      const r = failureExplanation(kind)
      const joined = `${r.headline} ${r.detail} ${r.nextStep}`
      expect(joined, `kind=${String(kind)}`).not.toMatch(
        /payload|transform|adapter|dead[- ]letter|worker|poll|\bjob\b|wire|attempt|enqueue|idempoten/i,
      )
    }
  })

  it('FK-11: FAILURE_KINDS matches the Go vocabulary', () => {
    // Independently transcribed from internal/submission/failure.go:11-14, not read
    // from FAILURE_KINDS itself.
    const expected = ['payload_not_built', 'never_acknowledged', 'acknowledged_no_verdict']
    expect([...FAILURE_KINDS].sort()).toEqual(expected.sort())
  })

  // QA gap-fill (task-387): FK-1 only pins headline/detail distinctness -- an operator
  // scanning just the action line needs the three nextSteps to differ too.
  it('FK-12: the three kinds nextSteps are mutually distinct', () => {
    const steps = FAILURE_KINDS.map((k) => failureExplanation(k).nextStep)
    expect(new Set(steps).size).toBe(steps.length)
  })

  it('FK-13: failureExplanation is total and pure over FAILURE_KINDS -- no kind falls through, repeat calls agree', () => {
    for (const k of FAILURE_KINDS) {
      const r = failureExplanation(k)
      expect(r, `kind=${k}`).toBeDefined()
      expect(typeof r.headline, `kind=${k} headline`).toBe('string')
      expect(typeof r.detail, `kind=${k} detail`).toBe('string')
      expect(typeof r.nextStep, `kind=${k} nextStep`).toBe('string')
      expect(failureExplanation(k), `kind=${k} repeat call`).toEqual(r)
    }
  })

  it('FK-14: unusual inputs (empty, whitespace-only, wrong case, very long) all take the fallback branch unchanged', () => {
    const veryLong = 'x'.repeat(10_000)
    const inputs = ['', '  ', 'PAYLOAD_NOT_BUILT', veryLong]
    for (const input of inputs) {
      expect(failureExplanation(input), `input=${JSON.stringify(input.slice(0, 24))}…`).toEqual(FAILURE_EXPLANATION_FALLBACK)
    }
  })
})

// RED spec (task-391, BUG-03-02, Mode A) -- formFromInvoice still seeds the raw RFC3339
// value today, so this fails on the actual seed, not an import/compile error.
describe('formFromInvoice: issue_date seed format', () => {
  it("seeds issue_date in the input's stated format (YYYY-MM-DD), not the raw RFC3339 timestamp", () => {
    const form = formFromInvoice(draftInvoice)

    expect(form.issue_date).toBe('2026-07-01')
  })
})

// Composition tests for diffEditInput(original, form). These are GREEN today: the
// current (unfixed) code seeds and compares the SAME raw representation on both sides of
// the generic skip, so it round-trips correctly by coincidence. Their job is to catch the
// trap if formFromInvoice's seed is normalized without moving/re-targeting diffEditInput's
// issue_date comparison in lockstep -- they must stay green after that fix too.
describe('diffEditInput: composition with formFromInvoice', () => {
  it('an untouched form produces an empty patch', () => {
    const form = formFromInvoice(draftInvoice)

    expect(diffEditInput(draftInvoice, form)).toEqual({})
  })

  it('an untouched form with a null issue_date produces an empty patch', () => {
    const inv: InvoiceRecord = { ...draftInvoice, issue_date: null }
    const form = formFromInvoice(inv)

    expect(diffEditInput(inv, form)).toEqual({})
  })

  it('a changed date is sent as midnight UTC', () => {
    const form: EditFormState = { ...formFromInvoice(draftInvoice), issue_date: '2026-02-05' }

    expect(diffEditInput(draftInvoice, form)).toEqual({ issue_date: '2026-02-05T00:00:00Z' })
  })

  it('a cleared date is dropped, never sent as an empty string', () => {
    const form: EditFormState = { ...formFromInvoice(draftInvoice), issue_date: '' }

    const patch = diffEditInput(draftInvoice, form)

    expect('issue_date' in patch).toBe(false)
  })

  it('a full RFC3339 timestamp typed by hand passes through unchanged', () => {
    const form: EditFormState = { ...formFromInvoice(draftInvoice), issue_date: '2026-02-05T09:30:00Z' }

    expect(diffEditInput(draftInvoice, form)).toEqual({ issue_date: '2026-02-05T09:30:00Z' })
  })

  it('changing VAT alone patches VAT alone, never issue_date -- fails in the half-fixed intermediate state', () => {
    const form: EditFormState = { ...formFromInvoice(draftInvoice), vat: '999.00' }

    expect(diffEditInput(draftInvoice, form)).toEqual({ vat: '999.00' })
  })
})

// QA Mode B (task-391, BUG-03-02): adversarial coverage on top of the composition tests
// above, which are untouched. draftInvoice's own issue_date is already midnight, so none
// of those exercise a seed the input's truncation actually loses precision on.
describe('diffEditInput: issue_date adversarial coverage (task-391, BUG-03-02)', () => {
  it('an untouched save on a non-midnight timestamp produces an empty patch -- the lost time is never rewritten to midnight', () => {
    // Reverting the hoist (issue_date branch back below the generic skip, comparing
    // against the raw original) makes this produce { issue_date: '...T00:00:00Z' } instead
    // -- verified by replaying the pre-fix (058479c) branch order against this fixture.
    const inv: InvoiceRecord = { ...draftInvoice, issue_date: '2026-07-01T14:30:00Z' }
    const form = formFromInvoice(inv)

    expect(form.issue_date).toBe('2026-07-01')
    expect(diffEditInput(inv, form)).toEqual({})
  })

  it('changing VAT alone on a non-midnight-time invoice patches VAT alone -- issue_date is neither sent nor collapsed to midnight', () => {
    const inv: InvoiceRecord = { ...draftInvoice, issue_date: '2026-07-01T14:30:00Z' }
    const form: EditFormState = { ...formFromInvoice(inv), vat: '999.00' }

    expect(diffEditInput(inv, form)).toEqual({ vat: '999.00' })
  })

  it('an already-garbage stored issue_date round-trips untouched to an empty patch', () => {
    const inv: InvoiceRecord = { ...draftInvoice, issue_date: 'garbage-stored' }
    const form = formFromInvoice(inv)

    expect(form.issue_date).toBe('garbage-stored')
    expect(diffEditInput(inv, form)).toEqual({})
  })

  it('a garbage value pasted over a real date is sent verbatim, never blanked', () => {
    const form: EditFormState = { ...formFromInvoice(draftInvoice), issue_date: 'garbage-paste' }

    expect(diffEditInput(draftInvoice, form)).toEqual({ issue_date: 'garbage-paste' })
  })

  it('a garbage stored issue_date with trailing whitespace round-trips untouched to an empty patch', () => {
    // toDateInputValue passes non-date input through unchanged, so the form seeds with the
    // trailing space intact; form.issue_date is then trimmed before compare but the original
    // side wasn't -- an untouched save must still no-op (CodeRabbit, PR #138).
    const inv: InvoiceRecord = { ...draftInvoice, issue_date: 'garbage-stored ' }
    const form = formFromInvoice(inv)

    expect(form.issue_date).toBe('garbage-stored ')
    expect(diffEditInput(inv, form)).toEqual({})
  })
})

// RED specs (task-413, BUG-05-04, Mode A) -- isBuyerTinMissing's stub always returns
// false, so every "missing" case below fails on the boolean, not on the import.
describe('isBuyerTinMissing (task-413, BUG-05-04)', () => {
  it('AC-1: null, undefined, empty and whitespace-only are all missing', () => {
    const missing: Array<string | null | undefined> = [null, undefined, '', '   ']
    for (const tin of missing) {
      expect(isBuyerTinMissing(tin), `tin=${JSON.stringify(tin)}`).toBe(true)
    }
  })

  // Already green under today's constant-false stub -- any nullary boolean stub
  // trivially satisfies one direction. Load-bearing once the real predicate lands.
  it('AC-1: a present value is not missing, even malformed', () => {
    expect(isBuyerTinMissing('87654321-0002')).toBe(false)
    expect(isBuyerTinMissing('BADTIN')).toBe(false)
  })
})

