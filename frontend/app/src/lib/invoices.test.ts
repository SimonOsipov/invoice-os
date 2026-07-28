// RED specs (M4-09-03, task-184, I1-I14) — pin the invoice list/detail data-access
// helpers, the invoiceStatusStyle pill mapper, isFixable/verdictStatus, and the
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
// editInvoice/revalidateInvoice/invoiceStatusStyle/isFixable/verdictStatus/
// shouldFetchInvoices/invoicesViewState's stub bodies throw `new Error('not
// implemented')` before ever calling the real authedFetch/fetch (or, for the pure
// helpers, before returning anything) — that IS the correct RED reason (assertion /
// not-implemented), not an import/compile/setup error.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncState } from '@invoice-os/api-client'

import { createAuthedFetch } from './authedFetch'
import {
  editInvoice,
  gateByActiveEntity,
  getInvoice,
  getInvoiceHistory,
  invoiceStatusStyle,
  invoicesViewState,
  isFixable,
  isInFlight,
  isRowSelectable,
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
  type BatchSubmitResultItem,
  type EditFieldKey,
  type InvoiceDetailRecord,
  type InvoiceEditInput,
  type InvoiceRecord,
  type InvoiceStatus,
  type RejectionReason,
  type StatusChange,
} from './invoices'

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

describe('isFixable', () => {
  // Updated (M5-09-03, task-253, Core AC #4): M5-05-01 (task-237) widened
  // Store.Edit's own precondition to accept `rejected` (the reject/fix/resubmit rework
  // path) -- this mirror re-syncs to match, closing [spa-untouched]. Deliberately
  // updated, not deleted: 'rejected' moves OUT of the not-fixable set below and INTO
  // this one.
  it('I3: draft, validated and rejected are fixable', () => {
    expect(isFixable('draft')).toBe(true)
    expect(isFixable('validated')).toBe(true)
    expect(isFixable('rejected')).toBe(true)
  })

  it('I4: queued/submitted/accepted/failed are not fixable', () => {
    const nonFixable: InvoiceStatus[] = ['queued', 'submitted', 'accepted', 'failed']

    for (const status of nonFixable) {
      expect(isFixable(status)).toBe(false)
    }
  })
})

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

    expect(result).toEqual([draftInvoice])
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

    expect(result).toEqual([])
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

    expect(result).toEqual([draftInvoice])
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
    const detailInvoice: InvoiceDetailRecord = { ...draftInvoice, rule_set_version: 2, qr_png_base64: 'aGVsbG8=' }
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
    const updated: InvoiceRecord = { ...draftInvoice, ...patch }
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
  it('SYNC-1: the fixture (typed InvoiceRecord) carries exactly the 22 keys mirrored from invoice.go:83-105, no more, no fewer', () => {
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

