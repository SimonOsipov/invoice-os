// Buyer directory client: GET/POST/PATCH /api/invoice/v1/customers (internal/customer).
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
  { limit, offset, status, tin }: { limit: number; offset: number; status?: Customer['status']; tin?: string },
): Promise<CustomerListResponse> {
  let url = `${base}/api/invoice/v1/customers?limit=${limit}&offset=${offset}`
  if (status) url += `&status=${status}`
  if (tin) url += `&tin=${encodeURIComponent(tin)}`
  return authedFetch<CustomerListResponse>(url)
}

export type CustomerCreateInput = CustomerUpdateInput & { tin: string }

export function createCustomer(authedFetch: AuthedFetch, base: string, input: CustomerCreateInput): Promise<Customer> {
  return authedFetch<Customer>(`${base}/api/invoice/v1/customers`, { method: 'POST', body: input })
}

// The directory's filing TIN pattern: 12 digits with a hyphen after the 8th, or RN-<digits>.
export const FILEABLE_TIN = /^([0-9]{8}-[0-9]{4}|RN-[0-9]+)$/

// The active record for an exact TIN, or null when none matches.
export async function findCustomerByTin(authedFetch: AuthedFetch, base: string, tin: string): Promise<Customer | null> {
  const res = await authedFetch<CustomerListResponse>(`${base}/api/invoice/v1/customers?tin=${encodeURIComponent(tin)}&status=active&limit=1`)
  return res.customers[0] ?? null
}

// Named records for the distinct fileable TINs; an empty `tin` would list the whole directory.
export async function recordNamesByTin(authedFetch: AuthedFetch, base: string, tins: string[]): Promise<Map<string, string>> {
  const fileable = [...new Set(tins)].filter((t) => FILEABLE_TIN.test(t))
  const found = await Promise.all(fileable.map((t) => findCustomerByTin(authedFetch, base, t).catch(() => null)))
  const names = new Map<string, string>()
  found.forEach((c, i) => {
    if (c?.name != null) names.set(fileable[i], c.name)
  })
  return names
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

export function emptyBuyerForm(): BuyerFormState {
  const form = {} as BuyerFormState
  for (const [formKey] of FIELDS) form[formKey] = ''
  return form
}

// The trimmed TIN plus each non-blank trimmed field.
export function toCustomerCreateInput(tin: string, state: BuyerFormState): CustomerCreateInput {
  const input: CustomerCreateInput = { tin: tin.trim() }
  for (const [formKey, wireKey] of FIELDS) {
    const v = state[formKey].trim()
    if (v !== '') input[wireKey] = v
  }
  return input
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

export const GENERIC_ERROR = 'Something went wrong. Please try again.'
export const TIN_REQUIRED = 'TIN is required'
export const TIN_SHAPE_ERROR = 'Enter the TIN as 12345678-0001, or RN- followed by digits.'
export const CUSTOMER_TIN_HINT = '12-digit FIRS TIN as 12345678-0001, or RN- followed by digits.'
export const DUPLICATE_TIN_ERROR = 'A customer with this TIN already exists'

// null: a 401 shows nothing, the authedFetch seam is already signing out.
export function mapCorrectionError(err: unknown): string | null {
  if (!(err instanceof ApiError)) return GENERIC_ERROR
  if (err.status === 401) return null
  if (err.status === 400) return err.message.trim() || GENERIC_ERROR
  return GENERIC_ERROR
}

export interface CreateError {
  field?: 'tin' | 'buyerType'
  message: string
  duplicate?: true
}

// The server names a field only by the first word of its 400 message. null: a 401 shows nothing.
export function mapCreateError(err: unknown): CreateError | null {
  if (!(err instanceof ApiError)) return { message: GENERIC_ERROR }
  if (err.status === 401) return null
  if (err.status === 409) return { field: 'tin', message: DUPLICATE_TIN_ERROR, duplicate: true }
  if (err.status === 400) {
    const message = err.message.trim() || GENERIC_ERROR
    if (message.startsWith('tin')) return { field: 'tin', message }
    if (message.startsWith('buyer_type')) return { field: 'buyerType', message }
    return { message }
  }
  return { message: GENERIC_ERROR }
}
