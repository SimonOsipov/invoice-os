// The fake ERP's invoices arrive as drafts over the deployed gateway: the preview runs the fake connector.
// Every step acts on a client this run created; assertions are containment, never a tenant total.
import { test, expect } from '@playwright/test'
import { login, createEntity, createInvoice, listInvoices, getAuditLog, rawFetch, PERSONAS } from './client'
import { freshTin } from './fixtures'

const FAKE = 'fake' // fakeSystem in internal/erp/fake.go
const FAKE_KEY = 'fake-co-1' // fakeSeed key in internal/erp/fake_seed.go
const NUMBERS = ['FK1-0001', 'FK1-0002', 'FK1-0003'] // fakeSeed fake-co-1 invoice numbers
const FAKE_TOTAL = 107500 // fake line 100000.00 at 7.5% (fakeLine in internal/erp/fake.go)
const OWN_TOTAL = 5000
const PULLED = 'erp.document.pulled' // recordPulled in internal/erp/ingest.go
const SKIPPED = 'erp.document.skipped_duplicate' // recordSkipped in internal/erp/ingest.go

interface Payload {
  connection_id: string
  invoice_number: string
  linked?: boolean
  differs?: boolean
}

// The one connect step in this file: POST the fake connection for a client.
async function connectFake(token: string, entityId: string, key: string): Promise<string> {
  const res = await rawFetch(`/api/portfolio/v1/entities/${entityId}/erp-connections`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
    body: { system: FAKE, external_key: key },
  })
  expect(res.status).toBe(201)
  return (res.body as { id: string }).id
}

test.describe.serial('ERP ingest (API E2E, over the deployed gateway)', () => {
  test.setTimeout(240_000)
  let admin: string
  let entityId: string
  let connId: string
  let ownId: string

  const inClient = async () => (await listInvoices(admin, { entity_id: entityId, limit: 100 })).invoices

  test.beforeAll(async () => {
    admin = await login(PERSONAS.A)
    const tin = freshTin()
    entityId = (await createEntity(admin, { name: `ERPF-03 ${tin}`, tin })).id
    const own = await createInvoice(admin, {
      entity_id: entityId,
      invoice_number: NUMBERS[1],
      line_items: [{ description: 'Own line', quantity: '1', unit_price: '5000.00', line_total: '5000.00' }],
      subtotal: '5000.00',
      total: '5000.00',
    })
    ownId = own.id
    connId = await connectFake(admin, entityId, FAKE_KEY)
    await expect
      .poll(async () => (await inClient()).map((i) => i.invoice_number), { timeout: 180_000, intervals: [5_000] })
      .toEqual(expect.arrayContaining([NUMBERS[0], NUMBERS[2]]))
  })

  test("the fake ERP's invoices arrive in the client as drafts", async () => {
    const mine = await inClient()
    for (const n of [NUMBERS[0], NUMBERS[2]]) {
      const inv = mine.find((i) => i.invoice_number === n)
      expect(inv, n).toBeDefined()
      expect(['draft', 'validated']).toContain(inv?.status)
      expect(Number(inv?.total)).toBe(FAKE_TOTAL)
    }
  })

  test('an invoice number that already exists links and is never overwritten', async () => {
    const same = (await inClient()).filter((i) => i.invoice_number === NUMBERS[1])
    expect(same).toHaveLength(1)
    expect(same[0].id).toBe(ownId)
    expect(Number(same[0].total)).toBe(OWN_TOTAL)
  })

  test('the pull is audited: two pulled and one linked duplicate', async () => {
    const log = await getAuditLog(admin, { event: [PULLED, SKIPPED], limit: 200 })
    const mine = log.events
      .map((e) => ({ event: e.event, p: e.payload as Payload }))
      .filter((e) => e.p.connection_id === connId)
    const pulled = mine.filter((e) => e.event === PULLED).map((e) => e.p.invoice_number)
    expect(pulled).toEqual(expect.arrayContaining([NUMBERS[0], NUMBERS[2]]))
    const skipped = mine.filter((e) => e.event === SKIPPED)
    expect(skipped).toHaveLength(1)
    expect(skipped[0].p.invoice_number).toBe(NUMBERS[1])
    expect(skipped[0].p.linked).toBe(true)
    expect(skipped[0].p.differs).toBe(true)
  })
})
