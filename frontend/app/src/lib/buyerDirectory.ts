// Buyer directory client: GET/PATCH /api/invoice/v1/customers (internal/customer).
import { ApiError } from '@invoice-os/api-client'

import type { AuthedFetch } from './portfolio'

export const BUYER_TYPES = ['B2B', 'B2G', 'B2C'] as const

// Keys mirror internal/customer Customer's JSON tags (wireMirrors.test.ts).
export interface Customer {
  id: string
  tin: string
  name: string | null
  contact_person: string | null
  buyer_type: string | null
  email: string | null
  telephone: string | null
  street: string | null
  city: string | null
  postal_zone: string | null
  country: string | null
  state: string | null
  lga: string | null
  status: 'active' | 'archived'
  created_at: string
}

export interface CustomerListResponse {
  customers: Customer[]
  pagination: { limit: number; offset: number; total: number }
}

export type CustomerUpdateInput = Partial<Record<EditableKey, string>>

type EditableKey = Exclude<keyof Customer, 'id' | 'tin' | 'status' | 'created_at'>

export function listCustomers(
  authedFetch: AuthedFetch,
  base: string,
  { limit, offset }: { limit: number; offset: number },
): Promise<CustomerListResponse> {
  return authedFetch<CustomerListResponse>(`${base}/api/invoice/v1/customers?limit=${limit}&offset=${offset}`)
}

// The directory's filing TIN pattern: 12 digits with a hyphen after the 8th, or RN-<digits>.
export const FILEABLE_TIN = /^([0-9]{8}-[0-9]{4}|RN-[0-9]+)$/

// The active record for an exact TIN, or null when none matches.
export async function findCustomerByTin(authedFetch: AuthedFetch, base: string, tin: string): Promise<Customer | null> {
  const res = await authedFetch<CustomerListResponse>(`${base}/api/invoice/v1/customers?tin=${encodeURIComponent(tin)}&status=active&limit=1`)
  return res.customers[0] ?? null
}

export function updateCustomer(authedFetch: AuthedFetch, base: string, id: string, input: CustomerUpdateInput): Promise<Customer> {
  return authedFetch<Customer>(`${base}/api/invoice/v1/customers/${id}`, { method: 'PATCH', body: input })
}

// Form key -> wire key.
const FIELDS = [
  ['name', 'name'],
  ['contactPerson', 'contact_person'],
  ['buyerType', 'buyer_type'],
  ['email', 'email'],
  ['telephone', 'telephone'],
  ['street', 'street'],
  ['city', 'city'],
  ['postalZone', 'postal_zone'],
  ['country', 'country'],
  ['state', 'state'],
  ['lga', 'lga'],
] as const

export interface BuyerFormState {
  name: string
  contactPerson: string
  buyerType: string
  email: string
  telephone: string
  street: string
  city: string
  postalZone: string
  country: string
  state: string
  lga: string
}

export function buyerFormFrom(c: Customer): BuyerFormState {
  const form = {} as BuyerFormState
  for (const [formKey, wireKey] of FIELDS) form[formKey] = c[wireKey] ?? ''
  return form
}

// A changed field is sent trimmed; a cleared one is '' so the server clears it.
export function toCustomerUpdateInput(original: Customer, state: BuyerFormState): CustomerUpdateInput {
  const diff: CustomerUpdateInput = {}
  for (const [formKey, wireKey] of FIELDS) {
    const next = state[formKey].trim()
    if (next !== (original[wireKey] ?? '')) diff[wireKey] = next
  }
  return diff
}

const GENERIC_ERROR = 'Something went wrong. Please try again.'

// null: a 401 shows nothing, the authedFetch seam is already signing out.
export function mapCorrectionError(err: unknown): string | null {
  if (!(err instanceof ApiError)) return GENERIC_ERROR
  if (err.status === 401) return null
  if (err.status === 400) return err.message.trim() || GENERIC_ERROR
  return GENERIC_ERROR
}
