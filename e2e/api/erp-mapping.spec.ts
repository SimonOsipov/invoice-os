// The client's mapping and a TIN lookup reach the fake ERP's drafts over the deployed gateway.
// Every step acts on a client this run created; assertions never rely on a tenant total.
import { test, expect } from '@playwright/test'
import { login, createEntity, listInvoices, getInvoice, getAuditLog, rawFetch, apiBase, PERSONAS } from './client'
import { freshTin } from './fixtures'
import { connectFake } from './erpFake'
import { assertErrorEnvelope } from './contract-helpers'
import { IMPORT_KEYS } from '../importFixtures'

const FAKE = 'fake' // fakeSystem in internal/erp/fake.go
const FAKE_KEY = 'fake-co-1' // fakeSeed key in internal/erp/fake_seed.go
const NUMBERS = ['FK1-0001', 'FK1-0002', 'FK1-0003'] // fakeSeed fake-co-1 invoice numbers
const CUST1 = 'fake-co-1-cust-1' // fakeSeed customer 1: invoices 1 and 3
const CUST2_TIN = '2345678904' // fakeSeed customer 2's TIN, no lookup
const FAKE_ITEM = 'fake-item-consulting' // ItemID in fakeInvoiceBody (internal/erp/fake.go)
const ERP_TARGETS = ['erp_customer_id', 'erp_tax_code', 'erp_item_id'] // TargetCustomerID, TargetTaxCode, TargetItemID in internal/erp/fieldmap.go
const ITEM_PATH = 'lines[].item_id'
const BAD_STATE = 'NG-XX-NOPE'
const BAD_STATE_MSG = 'customer state: not on the NRS states list' // invalidLookup in internal/erp/mapping_store.go
const CHANGED = 'erp.mapping.changed' // SaveMapping and SaveLookups in internal/erp/mapping_store.go
const PREPARER = 'c0000000-0000-0000-0000-000000000003' // seeded firm preparer, all_clients

interface Field {
  target: string
  source_path: string | null
  origin: string
}

test.describe.serial('ERP mapping (API E2E, over the deployed gateway)', () => {
  test.setTimeout(240_000) // the pull sweep ticks once a minute

  const runStart = new Date().toISOString()
  const savedTin = freshTin()
  let admin: string
  let preparer: string
  let entityId: string

  const auth = (t: string) => ({ Authorization: `Bearer ${t}` })
  const mappingPath = () => `/api/portfolio/v1/entities/${entityId}/erp-mappings/${FAKE}`
  const lookupsPath = (key = '') => `/api/portfolio/v1/entities/${entityId}/erp-lookups/${FAKE}${key ? `?external_key=${key}` : ''}`
  const inClient = async () => (await listInvoices(admin, { entity_id: entityId, limit: 100 })).invoices
  const fieldsOf = (body: unknown) => (body as { fields: Field[] }).fields

  test.beforeAll(async () => {
    admin = await login(PERSONAS.A)
    preparer = await login({ ...PERSONAS.A, subject: PREPARER })
    const tin = freshTin()
    entityId = (await createEntity(admin, { name: `ERP mapping ${tin}`, tin })).id
  })

  test("the fake's default mapping is readable", async () => {
    const res = await rawFetch(mappingPath(), { headers: auth(admin) })
    expect(res.status).toBe(200)
    const fields = fieldsOf(res.body)
    expect(new Set(fields.map((f) => f.target))).toEqual(new Set([...IMPORT_KEYS, ...ERP_TARGETS]))
    expect(fields).toHaveLength(IMPORT_KEYS.length + 3)
    expect(fields.every((f) => f.origin === 'default')).toBe(true)
    expect(fields.find((f) => f.target === 'invoice_number')?.source_path).toBe('number')
  })

  test('a preparer reads but cannot save the mapping or the lookups', async () => {
    expect((await rawFetch(mappingPath(), { headers: auth(preparer) })).status).toBe(200)

    const notJson = (path: string) =>
      fetch(`${apiBase()}${path}`, { method: 'PUT', headers: { ...auth(preparer), 'Content-Type': 'application/json' }, body: 'not json' }).then(
        async (r) => ({ status: r.status, body: (await r.json().catch(() => undefined)) as unknown }),
      )
    const refused = [
      await rawFetch(mappingPath(), { method: 'PUT', headers: auth(preparer), body: { fields: [{ target: 'line_description', source_path: ITEM_PATH }] } }),
      await rawFetch(lookupsPath(FAKE_KEY), { method: 'PUT', headers: auth(preparer), body: { lookups: [{ kind: 'customer', erp_key: CUST1, tin: savedTin }] } }),
      await notJson(mappingPath()),
      await notJson(lookupsPath(FAKE_KEY)),
    ]
    for (const [i, res] of refused.entries()) assertErrorEnvelope(res, 403, `preparer save #${i}`)
  })

  test('an admin saves an override and a TIN lookup; an unknown state is refused', async () => {
    const saved = await rawFetch(mappingPath(), {
      method: 'PUT',
      headers: auth(admin),
      body: { fields: [{ target: 'line_description', source_path: ITEM_PATH }] },
    })
    expect(saved.status).toBe(200)
    const line = fieldsOf(saved.body).find((f) => f.target === 'line_description')
    expect(line).toMatchObject({ source_path: ITEM_PATH, origin: 'client' })

    const tin = await rawFetch(lookupsPath(FAKE_KEY), {
      method: 'PUT',
      headers: auth(admin),
      body: { lookups: [{ kind: 'customer', erp_key: CUST1, tin: savedTin }] },
    })
    expect(tin.status).toBe(200)
    expect((tin.body as { external_key: string }).external_key).toBe(FAKE_KEY)

    const bad = await rawFetch(lookupsPath(FAKE_KEY), {
      method: 'PUT',
      headers: auth(admin),
      body: { lookups: [{ kind: 'customer', erp_key: CUST1, state: BAD_STATE }] },
    })
    assertErrorEnvelope(bad, 400, 'off-list state')
    expect((bad.body as { error: string }).error).toBe(BAD_STATE_MSG)
  })

  test('the fake ERP connects and its invoices arrive', async () => {
    await connectFake(admin, entityId, FAKE_KEY)
    await expect
      .poll(async () => (await inClient()).map((i) => i.invoice_number), { timeout: 180_000, intervals: [5_000] })
      .toEqual(expect.arrayContaining(NUMBERS))
  })

  test("customer 1's invoices carry the saved TIN and the override", async () => {
    const mine = await inClient()
    for (const n of [NUMBERS[0], NUMBERS[2]]) {
      const inv = await getInvoice(admin, mine.find((i) => i.invoice_number === n)?.id ?? '')
      expect(inv.buyer_tin, n).toBe(savedTin)
      expect(inv.line_items?.map((l) => l.description), n).toEqual([FAKE_ITEM])
      expect(inv.violations.map((v) => v.rule_key), n).not.toContain('buyer-tin-format')
    }
  })

  test("customer 2's invoice keeps the ERP's partial TIN and fails validation", async () => {
    const mine = await inClient()
    const inv = await getInvoice(admin, mine.find((i) => i.invoice_number === NUMBERS[1])?.id ?? '')
    expect(inv.buyer_tin).toBe(CUST2_TIN)
    expect(inv.status).toBe('draft')
    expect(inv.violations.map((v) => v.rule_key)).toContain('buyer-tin-format')
  })

  test('each save is audited without values', async () => {
    const log = await getAuditLog(admin, { event: [CHANGED], from: runStart, limit: 200 })
    const mine = log.events.filter((e) => e.actor === PERSONAS.A.subject)
    const payloads = mine.map((e) => e.payload as Record<string, unknown>)
    expect(payloads.map((p) => p.part)).toEqual(expect.arrayContaining(['fields', 'lookups']))
    for (const p of payloads) expect(Object.keys(p).sort()).toEqual(['part', 'system'])
    expect(JSON.stringify(mine)).not.toContain(savedTin)
  })
})
