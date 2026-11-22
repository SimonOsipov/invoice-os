// The fake ERP's invoices arrive as drafts over the deployed gateway: the preview runs the fake connector.
// Every step acts on a client this run created; assertions are containment, never a tenant total.
import { test, expect } from '@playwright/test'
import { login, createEntity, createInvoice, listInvoices, getAuditLog, rawFetch, apiBase, PERSONAS } from './client'
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

// The one connect step in this file: create the pending fake connection, then complete its consent
// through the gateway's /erp/callback with the company code, as the consent page does.
async function connectFake(token: string, entityId: string, key: string): Promise<string> {
  const res = await rawFetch(`/api/portfolio/v1/entities/${entityId}/erp-connections`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
    body: { system: FAKE },
  })
  expect(res.status).toBe(201)
  const conn = res.body as { id: string; consent_url: string }
  const state = new URL(conn.consent_url).searchParams.get('state') ?? ''
  const done = await fetch(`${apiBase()}/erp/callback?${new URLSearchParams({ state, code: key })}`, { redirect: 'manual' })
  expect(done.status).toBe(200)
  return conn.id
}

test.describe.serial('ERP ingest (API E2E, over the deployed gateway)', () => {
  let admin: string
  let entityId: string
  let connId: string
  let ownId: string

  const inClient = async () => (await listInvoices(admin, { entity_id: entityId, limit: 100 })).invoices

  test.beforeAll(async () => {
    test.setTimeout(240_000) // a hook has its own timeout; the pull sweep ticks once a minute
    admin = await login(PERSONAS.A)
    const tin = freshTin()
    entityId = (await createEntity(admin, { name: `ERP ingest ${tin}`, tin })).id
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
    // fixture check: pins fakeSeed fake-co-1 and fakeLine (internal/erp/fake.go)
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
    expect(same.map((i) => i.id)).toContain(ownId)
    expect(same.every((i) => i.id === ownId)).toBe(true)
    expect(Number(same.find((i) => i.id === ownId)?.total)).toBe(OWN_TOTAL)
  })

  test('the pull is audited: two pulled and one linked duplicate', async () => {
    const log = await getAuditLog(admin, { event: [PULLED, SKIPPED], limit: 200 })
    const mine = log.events
      .map((e) => ({ event: e.event, p: e.payload as Payload }))
      .filter((e) => e.p.connection_id === connId)
    const pulled = mine.filter((e) => e.event === PULLED).map((e) => e.p.invoice_number)
    expect(pulled).toEqual(expect.arrayContaining([NUMBERS[0], NUMBERS[2]]))
    const dup = mine.find((e) => e.event === SKIPPED && e.p.invoice_number === NUMBERS[1])
    expect(dup).toBeDefined()
    expect(dup?.p.linked).toBe(true)
    expect(dup?.p.differs).toBe(true)
  })
})
