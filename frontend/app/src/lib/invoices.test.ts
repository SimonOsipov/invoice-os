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
  getInvoice,
  getInvoiceHistory,
  invoiceStatusStyle,
  invoicesViewState,
  isFixable,
  listInvoices,
  revalidateInvoice,
  shouldFetchInvoices,
  verdictStatus,
  type InvoiceEditInput,
  type InvoiceRecord,
  type InvoiceStatus,
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
  it('I3: draft and validated are fixable', () => {
    expect(isFixable('draft')).toBe(true)
    expect(isFixable('validated')).toBe(true)
  })

  it('I4: queued/submitted/accepted/rejected/failed are not fixable', () => {
    const nonFixable: InvoiceStatus[] = ['queued', 'submitted', 'accepted', 'rejected', 'failed']

    for (const status of nonFixable) {
      expect(isFixable(status)).toBe(false)
    }
  })
})

describe('verdictStatus', () => {
  it('I5: true -> "stale", false -> "current"', () => {
    expect(verdictStatus(true)).toBe('stale')
    expect(verdictStatus(false)).toBe('current')
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
