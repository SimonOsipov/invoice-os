import { ApiError } from '@invoice-os/api-client'
import { describe, expect, it, vi } from 'vitest'

import { buyerFormFrom, listCustomers, mapCorrectionError, toCustomerUpdateInput, updateCustomer, type Customer } from './buyerDirectory'

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
  created_at: '2026-01-01T00:00:00Z',
}

describe('buyerDirectory', () => {
  it('listCustomers requests the invoice service with limit and offset', async () => {
    const af = vi.fn().mockResolvedValue({ customers: [], pagination: { limit: 50, offset: 50, total: 0 } })
    await listCustomers(af, BASE, { limit: 50, offset: 50 })
    expect(af).toHaveBeenCalledTimes(1)
    expect(af).toHaveBeenCalledWith(`${BASE}/api/invoice/v1/customers?limit=50&offset=50`)
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
