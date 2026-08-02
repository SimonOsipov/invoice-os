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
import {
  clampFilterText,
  computedLineSum,
  createInvoice,
  diffLineItems,
  editInvoice,
  gateByActiveEntity,
  getInvoice,
  getInvoiceHistory,
  invoiceStatusStyle,
  invoicesViewState,
  isInFlight,
  isRowSelectable,
  keepInvoiceAsIs,
  LIVE_POLL_MS,
  listInvoices,
  mbsPathToEditField,
  newIdempotencyKey,
  pruneSelection,
  reasonFieldFlags,
  rejectionProvenance,
  revalidateInvoice,
  selectableIds,
  selectAllState,
  shouldFetchInvoices,
  shouldPollInvoice,
  shouldPollList,
  shouldRefreshHistory,
  shouldShowFiscalRecord,
  shouldShowRejectionCard,
  skipReasonLabel,
  submitInvoices,
  toggleSelection,
  verdictStatus,
  violationSummary,
  type BatchSubmitResultItem,
  type EditFieldKey,
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
  rule_set_version: null,
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
      can_edit: false,
      can_revalidate: false,
      revalidate_blocked_reason: null,
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
    const detailInvoice: InvoiceDetailRecord = {
      ...draftInvoice,
      rule_set_version: 2,
      qr_png_base64: 'aGVsbG8=',
      can_edit: true,
      can_revalidate: true,
      revalidate_blocked_reason: null,
    }
    const { qr_png_base64: _omittedQr, ...withoutQrKey } = detailInvoice
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(withoutQrKey) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    const result = await getInvoice(af, base, 'inv-1')

    expect(result.qr_png_base64).toBeNull()
  })
})

describe('getInvoiceHistory', () => {
  it('I10: the bare StatusChange[] array passes through unchanged; GET .../invoices/{id}/history', async () => {
    const history: StatusChange[] = [
      { from_status: null, to_status: 'draft', actor: 'system', changed_at: '2026-07-01T00:00:00Z' },
      { from_status: 'draft', to_status: 'validated', actor: 'user:u1', changed_at: '2026-07-02T00:00:00Z' },
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
  it('I-skip-1: skipReasonLabel maps both reachable reasons and passes others through', () => {
    expect(skipReasonLabel('not_validated')).toBe('Not validated — validate it first')
    expect(skipReasonLabel('duplicate_request')).toBe('Already submitted with this request')
    expect(skipReasonLabel('wat')).toBe('wat')
  })
})

describe('selection helpers', () => {
  it('I-sel-1: only validated rows are selectable', () => {
    const statuses: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']
    const rows: InvoiceRecord[] = statuses.map((status, i) => ({ ...draftInvoice, id: `inv-${i}`, status }))

    expect(selectableIds(rows)).toEqual(['inv-1'])
    for (const row of rows) {
      expect(isRowSelectable(row.status), `status=${row.status}`).toBe(row.status === 'validated')
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
  it('SYNC-1: the fixture (typed InvoiceRecord) carries exactly the 25 keys mirrored from invoice.go, no more, no fewer', () => {
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
      'rule_set_version',
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

