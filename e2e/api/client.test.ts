// task-498 (APPR-08-07, Mode B): the emission rule for listInvoices' query params.
//
// AC #9 had no enforcement at all. Deleting the awaiting_approval line from listInvoices
// leaves `tsc --noEmit` green (the interface field still typechecks) and the whole vitest
// suite green, because nothing calls listInvoices under vitest and the first Playwright
// caller lands in APPR-08-10. A dropped emit line would therefore reach main silently, and
// APPR-08-10 would look like it had broken the filter.
//
// The @invoice-os/api-client seam is mocked so this asserts the URL listInvoices BUILDS,
// with no network. topology/targets.ts calls resolveTarget at module scope, so the env
// vars must be set before client.ts is imported -- hence the dynamic import.
import { beforeAll, describe, expect, it, vi } from 'vitest'

const requested: string[] = []

vi.mock('@invoice-os/api-client/client', () => ({
  apiFetch: (url: string) => {
    requested.push(url)
    return Promise.resolve({ invoices: [], pagination: {} })
  },
  ApiError: class ApiError extends Error {},
}))

let listInvoices: (typeof import('./client'))['listInvoices']

beforeAll(async () => {
  process.env.GATEWAY_URL = 'https://gateway.test'
  process.env.APP_URL = 'https://app.test'
  ;({ listInvoices } = await import('./client'))
})

// query returns the query string listInvoices issued for the given argument.
async function query(arg?: Parameters<typeof listInvoices>[1]): Promise<string> {
  requested.length = 0
  await listInvoices('token', arg)
  expect(requested).toHaveLength(1)
  return requested[0].slice(requested[0].indexOf('/api/'))
}

describe('listInvoices awaiting_approval', () => {
  it('emits nothing when the field is omitted', async () => {
    expect(await query()).toBe('/api/invoice/v1/invoices')
    expect(await query({ limit: 5 })).toBe('/api/invoice/v1/invoices?limit=5')
  })

  it('emits awaiting_approval=true', async () => {
    expect(await query({ awaiting_approval: true })).toBe('/api/invoice/v1/invoices?awaiting_approval=true')
  })

  // The `!== undefined` rule, not the SPA client's `=== true` rule: explicit false is a
  // real query string here. strconv.ParseBool accepts "false", and ListFilter's zero value
  // applies no predicate, so the server answers it exactly as it answers an absent param.
  it('emits awaiting_approval=false rather than dropping it', async () => {
    expect(await query({ awaiting_approval: false })).toBe('/api/invoice/v1/invoices?awaiting_approval=false')
  })

  it('composes with the other params', async () => {
    expect(await query({ limit: 2, offset: 4, q: 'acme', entity_id: 'e-1', awaiting_approval: true })).toBe(
      '/api/invoice/v1/invoices?limit=2&offset=4&q=acme&entity_id=e-1&awaiting_approval=true',
    )
  })
})
