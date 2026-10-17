import { ApiError } from '@invoice-os/api-client'
import { describe, expect, it, vi } from 'vitest'

import { buyerFormFrom, findCustomerByTin, listCustomers, mapCorrectionError, recordNamesByTin, toCustomerUpdateInput, updateCustomer, type Customer } from './buyerDirectory'

const BASE = 'https://gateway.test'

const CUSTOMER: Customer = {
  id: 'c1',
  tin: '20184412-0001',
  name: 'Acme Ltd',
  contact_person: null,
  buyer_type: 'B2B',
  email: 'a@x.ng',
  telephone: null,
  street: null,
  city: 'Ikeja',
  postal_zone: null,
  country: null,
  state: null,
  lga: null,
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
}

describe('buyerDirectory', () => {
  it('listCustomers requests the invoice service with limit and offset', async () => {
    const af = vi.fn().mockResolvedValue({ customers: [], pagination: { limit: 50, offset: 50, total: 0 } })
    await listCustomers(af, BASE, { limit: 50, offset: 50 })
    expect(af).toHaveBeenCalledTimes(1)
    expect(af).toHaveBeenCalledWith(`${BASE}/api/invoice/v1/customers?limit=50&offset=50`)
  })

  it('findCustomerByTin asks only for active records', async () => {
    const af = vi.fn().mockResolvedValue({ customers: [CUSTOMER], pagination: { limit: 1, offset: 0, total: 1 } })
    await findCustomerByTin(af, BASE, '12345678-0001')
    expect(af).toHaveBeenCalledWith(`${BASE}/api/invoice/v1/customers?tin=12345678-0001&status=active&limit=1`)
  })

  it('updateCustomer PATCHes the customer by id', async () => {
    const af = vi.fn().mockResolvedValue(CUSTOMER)
    await updateCustomer(af, BASE, 'c1', { city: '' })
    expect(af).toHaveBeenCalledWith(`${BASE}/api/invoice/v1/customers/c1`, { method: 'PATCH', body: { city: '' } })
  })

  it('toCustomerUpdateInput sends only changed trimmed fields', () => {
    const form = { ...buyerFormFrom(CUSTOMER), email: ' b@x.ng ', city: '' }
    expect(toCustomerUpdateInput(CUSTOMER, form)).toEqual({ email: 'b@x.ng', city: '' })
  })

  it('an untouched buyer form yields an empty diff', () => {
    expect(toCustomerUpdateInput(CUSTOMER, buyerFormFrom(CUSTOMER))).toEqual({})
  })

  it('a whitespace-only entry on a null field is not a change', () => {
    expect(toCustomerUpdateInput(CUSTOMER, { ...buyerFormFrom(CUSTOMER), telephone: '   ' })).toEqual({})
  })

  it('mapCorrectionError maps 400, 401, 404 and a network error', () => {
    const generic = 'Something went wrong. Please try again.'
    expect(mapCorrectionError(new ApiError('http', 'buyer_type must be B2B, B2G or B2C', 400))).toBe('buyer_type must be B2B, B2G or B2C')
    expect(mapCorrectionError(new ApiError('http', 'unauthorized', 401))).toBeNull()
    expect(mapCorrectionError(new ApiError('http', 'not found', 404))).toBe(generic)
    expect(mapCorrectionError(new TypeError('Failed to fetch'))).toBe(generic)
  })
})

describe('recordNamesByTin', () => {
  it('recordNamesByTin looks up only distinct fileable TINs', async () => {
    const af = vi.fn().mockResolvedValue({ customers: [], pagination: { limit: 1, offset: 0, total: 0 } })
    await recordNamesByTin(af, BASE, ['20000000-0001', '20000000-0001', '', 'BAD', 'RN-9'])
    expect(af.mock.calls.map((c) => c[0])).toEqual([
      `${BASE}/api/invoice/v1/customers?tin=20000000-0001&status=active&limit=1`,
      `${BASE}/api/invoice/v1/customers?tin=RN-9&status=active&limit=1`,
    ])
  })

  it('recordNamesByTin maps named records and skips a missing or unnamed one', async () => {
    const byTin: Record<string, Customer[]> = {
      'RN-1': [{ ...CUSTOMER, tin: 'RN-1', name: 'Rec A' }],
      'RN-2': [],
      'RN-3': [{ ...CUSTOMER, tin: 'RN-3', name: null }],
    }
    const af = vi.fn().mockImplementation((url: string) => Promise.resolve({ customers: byTin[new URL(url).searchParams.get('tin') ?? ''], pagination: { limit: 1, offset: 0, total: 1 } }))
    const names = await recordNamesByTin(af, BASE, ['RN-1', 'RN-2', 'RN-3'])
    expect(Object.fromEntries(names)).toEqual({ 'RN-1': 'Rec A' })
  })

  it('recordNamesByTin keeps the other names when one lookup rejects', async () => {
    const af = vi.fn().mockImplementation((url: string) =>
      new URL(url).searchParams.get('tin') === 'RN-1'
        ? Promise.reject(new Error('down'))
        : Promise.resolve({ customers: [{ ...CUSTOMER, tin: 'RN-2', name: 'Rec B' }], pagination: { limit: 1, offset: 0, total: 1 } }),
    )
    const names = await recordNamesByTin(af, BASE, ['RN-1', 'RN-2'])
    expect(Object.fromEntries(names)).toEqual({ 'RN-2': 'Rec B' })
  })
})
