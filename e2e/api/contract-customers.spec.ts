// The buyer directory over the deployed gateway: an invoice save files the buyer, a member's
// correction is audited and survives the next save, and the error matrix. Rows are keyed by
// random per-run TINs and the spec acts only on those.
import { randomUUID } from 'node:crypto'
import { test, expect } from '@playwright/test'
import { createEntity, createInvoice, getAuditLog, listCustomers, login, rawFetch, updateCustomer, PERSONAS, type Customer, type Entity } from './client'
import { freshBuyerTin, freshTin } from './fixtures'
import { assertErrorEnvelope } from './contract-helpers'

// internal/customer/customer.go's errNoFields and errBuyerType, and the list handler's limit message.
const NO_FIELDS = 'no fields to update'
const BUYER_TYPE = 'buyer_type must be B2B, B2G or B2C'
const LIMIT_MIN = 'limit must be >= 1'

test.describe('buyer directory contract (API E2E, over the deployed gateway)', () => {
  let token: string
  let entity: Entity

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
    const tin = freshTin()
    entity = await createEntity(token, { name: `ENGI-05 buyers ${tin}`, tin })
  })

  // Saves an invoice for `tin`; returns the buyer record it filled.
  async function fileBuyer(tin: string, buyer: Record<string, string> = {}): Promise<Customer> {
    await createInvoice(token, {
      entity_id: entity.id,
      invoice_number: `INV-ENGI05-${randomUUID().slice(0, 8)}`,
      buyer_tin: tin,
      buyer_name: 'Buyer Ltd',
      invoice_kind: 'B2B',
      ...buyer,
    })
    const { customers } = await listCustomers(token, { tin })
    expect(customers, 'the invoice save must file exactly one record for the TIN').toHaveLength(1)
    return customers[0]
  }

  test('saving an invoice creates the buyer record', async () => {
    const tin = freshBuyerTin()
    const email = `buyer-${tin}@example.com`
    const record = await fileBuyer(tin, { buyer_name: `Filled ${tin}`, buyer_email: email })
    expect(record).toMatchObject({ tin, name: `Filled ${tin}`, email, buyer_type: 'B2B' })
  })

  test('a correction is audited and survives a later invoice', async () => {
    const tin = freshBuyerTin()
    const record = await fileBuyer(tin)
    const fixed = `fixed-${tin}@example.com`
    const patched = await updateCustomer(token, record.id, { email: fixed })
    expect(patched.email).toBe(fixed)

    const audit = await getAuditLog(token, { event: ['customers.customer.updated'], limit: 200 })
    const row = audit.events.find((e) => (e.payload as { tin?: string } | null)?.tin === tin)
    expect(row, `no customers.customer.updated audit row carries tin ${tin}`).toBeDefined()

    await fileBuyer(tin, { buyer_email: `new-${tin}@example.com`, buyer_city: 'Abuja' })
    const [after] = (await listCustomers(token, { tin })).customers
    expect(after.email, 'a corrected field keeps the correction').toBe(fixed)
    expect(after.city, 'an uncorrected field takes the later invoice').toBe('Abuja')
  })

  test('the error matrix', async () => {
    const tin = freshBuyerTin()
    const record = await fileBuyer(tin)
    const auth = { Authorization: `Bearer ${token}` }
    const patch = (id: string, body: unknown) => rawFetch(`/api/invoice/v1/customers/${id}`, { method: 'PATCH', headers: auth, body })

    const empty = await patch(record.id, {})
    assertErrorEnvelope(empty, 400, 'PATCH {}')
    expect((empty.body as { error: string }).error).toBe(NO_FIELDS)

    const badType = await patch(record.id, { buyer_type: 'x' })
    assertErrorEnvelope(badType, 400, 'PATCH buyer_type x')
    expect((badType.body as { error: string }).error).toBe(BUYER_TYPE)

    const missing = await patch(randomUUID(), { name: 'Nobody' })
    assertErrorEnvelope(missing, 404, 'PATCH a random uuid')
    expect((missing.body as { error: string }).error).toBe('not found')

    const limit = await rawFetch('/api/invoice/v1/customers?limit=0', { headers: auth })
    assertErrorEnvelope(limit, 400, 'GET limit=0')
    expect((limit.body as { error: string }).error).toBe(LIMIT_MIN)
  })

  test("another tenant's record is 404", async () => {
    const tin = freshBuyerTin()
    const record = await fileBuyer(tin)
    const tokenB = await login(PERSONAS.B)
    const res = await rawFetch(`/api/invoice/v1/customers/${record.id}`, {
      method: 'PATCH',
      headers: { Authorization: `Bearer ${tokenB}` },
      body: { name: 'Hijacked' },
    })
    assertErrorEnvelope(res, 404, "another tenant's record")
    const [after] = (await listCustomers(token, { tin })).customers
    expect(after.name, "tenant 2222 must not change tenant 1111's record").toBe(record.name)
  })
})
