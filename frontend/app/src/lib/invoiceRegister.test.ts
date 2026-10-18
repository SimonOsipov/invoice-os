import { ApiError } from '@invoice-os/api-client'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { fetchInvoiceRegister, invoiceRegisterUrl, previousMonthPeriod, registerErrorLine, registerPeriodValid } from './invoiceRegister'
import { SessionEndedError } from './renewal'

afterEach(() => vi.unstubAllGlobals())

describe('previousMonthPeriod', () => {
  it('returns the previous calendar month', () => {
    const cases: [Date, string, string][] = [
      [new Date(2026, 9, 18), '2026-09-01', '2026-09-30'],
      [new Date(2027, 0, 15), '2026-12-01', '2026-12-31'],
      [new Date(2028, 2, 1), '2028-02-01', '2028-02-29'],
      [new Date(2026, 2, 31), '2026-02-01', '2026-02-28'],
    ]
    for (const [now, from, to] of cases) expect(previousMonthPeriod(now)).toEqual({ from, to })
  })
})

describe('registerPeriodValid', () => {
  it('accepts equal dates, rejects from after to and an empty side', () => {
    expect(registerPeriodValid({ from: '2026-09-01', to: '2026-09-01' })).toBe(true)
    expect(registerPeriodValid({ from: '2026-09-02', to: '2026-09-01' })).toBe(false)
    expect(registerPeriodValid({ from: '', to: '2026-09-01' })).toBe(false)
    expect(registerPeriodValid({ from: '2026-09-01', to: '' })).toBe(false)
  })
})

describe('invoiceRegisterUrl', () => {
  it('encodes the triple', () => {
    expect(invoiceRegisterUrl('https://gw', { entityId: 'ent 1', from: '2026-09-01', to: '2026-09-30' })).toBe(
      'https://gw/api/invoice/v1/invoice-register?entity_id=ent+1&from=2026-09-01&to=2026-09-30',
    )
  })
})

describe('fetchInvoiceRegister', () => {
  const r = { entityId: 'ent-1', from: '2026-09-01', to: '2026-09-30' }

  it('rejects with the server sentence', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 404, statusText: 'Not Found', json: () => Promise.resolve({ error: 'not found' }) })))
    const err = await fetchInvoiceRegister(() => 'tok', 'https://gw', r).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(404)
    expect(err.message).toBe('not found')
  })

  it('falls back to the register filename without a Content-Disposition', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.resolve({ ok: true, headers: { get: () => null }, blob: () => Promise.resolve(new Blob(['x'])) })),
    )
    const res = await fetchInvoiceRegister(() => 'tok', 'https://gw', r)
    expect(res.filename).toBe('ASComply_invoice-register.xlsx')
  })
})

describe('registerErrorLine', () => {
  it('maps each failure to its line', () => {
    expect(registerErrorLine(new ApiError('http', 'x', 400, { error: 'the sentence' }))).toBe('the sentence')
    expect(registerErrorLine(new ApiError('http', 'Bad Gateway', 502, undefined))).toBe('The register could not be built (HTTP 502). Try again.')
    expect(registerErrorLine(new TypeError('Failed to fetch'))).toBe('Could not reach the server. Check your connection and try again.')
    expect(registerErrorLine(new SessionEndedError())).toBeNull()
  })
})
