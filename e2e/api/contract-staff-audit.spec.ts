// The staff audit and evidence contract over the wire: the staff search matches a tenant's own log, the staff bundle
// is the tenant admin's bundle, a staff read writes no audit row, and a customer token is refused on all five paths.
// Acts only on rows the spec creates: a fresh entity and two invoices in PERSONAS.A's tenant, a fresh entity in PERSONAS.B's.
import { randomUUID } from 'node:crypto'
import { test, expect } from '@playwright/test'
import { unzipSync } from 'fflate'
import { apiBase, createEntity, createInvoice, getAuditLog, login, PERSONAS, provisionStaffAccount, rawFetch, signInSession, type AuditEvent, type StaffAccount } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope } from './contract-helpers'

const STAFF_AUDIT = '/api/invoice/v1/staff/audit-log'
const STAFF_CHANGES = '/api/invoice/v1/staff/staff-audit-log'
const tenantPath = (id: string): string => `/api/invoice/v1/staff/tenants/${id}`

// audit.SupportActor, internal/audit/audit.go.
const SUPPORT_ACTOR = 'ASComply support'
// staffManifestActor, internal/archive/staff.go.
const STAFF_MANIFEST_ACTOR = { name: 'ASComply support', kind: 'raw' }
// parseStaffFilter, internal/audit/staff_read.go.
const INVALID_TENANT = 'invalid tenant'
const LIMIT_MESSAGE = 'limit must be >= 1'
const RANGE_MESSAGE = 'from must not be after to'
const CURSOR_MESSAGE = 'cursor is malformed'
// statusForErr, internal/archive/handlers.go.
const TENANT_NOT_FOUND = 'tenant not found'
const ENTITY_NOT_FOUND = 'not found'
// maxLimit, internal/audit/handlers.go.
const MAX_LIMIT = 100

// ceiling: assumes the runner and gateway clocks agree within 5 s.
const START = new Date(Date.now() - 5_000).toISOString()
// A wide period: the entity id already scopes the bundle to this spec's rows.
const PERIOD = { from: '2020-01-01T00:00:00Z', to: '2035-01-01T00:00:00Z' }

type StaffRow = { id: string; tenant_id: string; created_at: string; event: string; actor: string }
type StaffPage = { events: StaffRow[]; page: { limit: number; has_more: boolean; next_cursor: string | null } }
type Preview = { entity: { id: string; name: string; tin: string | null }; period: unknown; filename: string; counts: Record<string, number>; over_limit: boolean }
type Manifest = Record<string, unknown> & { generated_by: unknown }

const bearer = (token: string): Record<string, string> => ({ Authorization: `Bearer ${token}` })

test.describe('staff audit and evidence (API E2E, over the deployed gateway)', () => {
  let customer: string
  let customerB: string
  let staff: string
  let account: StaffAccount
  let entityA: { id: string; name: string; tin: string }
  let entityB: string
  const invoiceIds: string[] = []

  const staffGet = (path: string) => rawFetch(path, { headers: bearer(staff) })

  async function staffSearch(query: Record<string, string>): Promise<StaffPage> {
    const res = await staffGet(`${STAFF_AUDIT}?${new URLSearchParams(query)}`)
    expect(res.status, `staff search ${JSON.stringify(query)}`).toBe(200)
    return res.body as StaffPage
  }

  const bundleQuery = (entityId: string): string => new URLSearchParams({ entity_id: entityId, ...PERIOD }).toString()

  async function download(path: string, token: string): Promise<Record<string, Uint8Array>> {
    const res = await fetch(`${apiBase()}${path}`, { headers: bearer(token) })
    expect(res.status, `download ${path}`).toBe(200)
    expect(res.headers.get('Content-Type')).toBe('application/zip')
    return unzipSync(new Uint8Array(await res.arrayBuffer()))
  }

  const manifestOf = (zip: Record<string, Uint8Array>): Manifest => JSON.parse(new TextDecoder().decode(zip['manifest.json'])) as Manifest

  test.beforeAll(async () => {
    customer = await login(PERSONAS.A)
    customerB = await login(PERSONAS.B)
    account = await provisionStaffAccount('staff-audit')
    staff = (await signInSession(account.email, account.password)).access_token

    const tin = freshTin()
    const name = `Zz SUPP-07 ${tin}`
    // portfolio stores the TIN without its hyphen (internal/portfolio/tin.go)
    entityA = { id: (await createEntity(customer, { name, tin })).id, name, tin: tin.replace('-', '') }
    entityB = (await createEntity(customerB, { name: `Zz SUPP-07 ${freshTin()}`, tin: freshTin() })).id
    for (const n of ['1', '2']) {
      const invoice = await createInvoice(customer, {
        entity_id: entityA.id,
        invoice_number: `INV-SUPP-07-${freshTin()}-${n}`,
        buyer_state: 'NG-LA',
        line_items: [{ description: 'Widget', quantity: '1', unit_price: '100', line_total: '100', tax_category: 'STANDARD_VAT', price_unit: 'KGM' }],
      })
      invoiceIds.push(invoice.id)
    }
  })

  test('staff audit: the tenant filter matches the tenant\'s own log; paging holds; refusals', async () => {
    const own = await getAuditLog(customer, { actor: [PERSONAS.A.subject], from: START, limit: MAX_LIMIT })
    const base = { tenant: PERSONAS.A.tenantId, actor: PERSONAS.A.subject, from: START, limit: String(MAX_LIMIT) }
    const found = await staffSearch(base)
    const ownIds = own.events.map((e) => e.id)
    expect(ownIds.length, 'the tenant log holds the spec writes').toBeGreaterThan(0)
    expect(found.events.map((e) => e.id), 'staff search returns the tenant log ids in the same order').toEqual(ownIds)
    for (const e of found.events) expect(e.tenant_id, 'the tenant filter holds').toBe(PERSONAS.A.tenantId)

    const created = found.events.filter((e) => e.event === 'invoice.created')
    expect(created.length, 'both spec invoices are in the staff search').toBeGreaterThanOrEqual(2)
    for (const id of invoiceIds) {
      const mine = (await getAuditLog(customer, { invoice_id: id, event: ['invoice.created'] })).events.map((e: AuditEvent) => e.id)
      expect(mine.length, `invoice ${id} has a created row`).toBeGreaterThan(0)
      for (const row of mine) expect(found.events.map((e) => e.id), `the staff search holds row ${row}`).toContain(row)
    }

    expect(found.events.length, 'two pages need two rows').toBeGreaterThanOrEqual(2)
    const one = await staffSearch({ ...base, limit: '1' })
    expect(one.page.has_more, 'page 1 has more').toBe(true)
    expect(one.page.next_cursor, 'page 1 names a cursor').toBeTruthy()
    const two = await staffSearch({ ...base, limit: '1', cursor: one.page.next_cursor! })
    expect(one.events, 'page 1 holds one row').toHaveLength(1)
    expect(two.events, 'page 2 holds one row').toHaveLength(1)
    expect([one.events[0].id, two.events[0].id], 'the two pages are the first two rows: no overlap, no skip').toEqual(ownIds.slice(0, 2))
    expect(Date.parse(two.events[0].created_at), 'created_at does not increase across pages').toBeLessThanOrEqual(Date.parse(one.events[0].created_at))

    const refuse = async (query: string, status: number, message: string) => {
      const res = await staffGet(`${STAFF_AUDIT}?${query}`)
      assertErrorEnvelope(res, status, query)
      expect((res.body as { error: string }).error, query).toBe(message)
    }
    await refuse('tenant=nope', 400, INVALID_TENANT)
    await refuse('limit=0', 400, LIMIT_MESSAGE)
    await refuse(`from=${encodeURIComponent('2030-01-02T00:00:00Z')}&to=${encodeURIComponent('2030-01-01T00:00:00Z')}`, 400, RANGE_MESSAGE)
    await refuse('cursor=not-a-cursor', 400, CURSOR_MESSAGE)
    // A malformed escape never reaches the handler: the gateway proxy drops it (httputil cleanQueryParams). Its 400 is a Go test.
  })

  test('staff evidence: the staff bundle is the tenant admin\'s bundle', async () => {
    const tenantZip = await download(`/api/invoice/v1/evidence-bundle?${bundleQuery(entityA.id)}`, customer)
    const staffZip = await download(`${tenantPath(PERSONAS.A.tenantId)}/evidence-bundle?${bundleQuery(entityA.id)}`, staff)

    expect(Object.keys(staffZip).sort(), 'the same entry names').toEqual(Object.keys(tenantZip).sort())
    for (const name of Object.keys(tenantZip).filter((n) => n !== 'manifest.json')) {
      expect(Buffer.from(staffZip[name]).equals(Buffer.from(tenantZip[name])), `${name} is byte-equal`).toBe(true)
    }
    const strip = ({ generated_at: _at, generated_by: _by, ...rest }: Manifest) => rest
    const staffManifest = manifestOf(staffZip)
    expect(strip(staffManifest), 'the manifests agree but for generated_at and generated_by').toEqual(strip(manifestOf(tenantZip)))
    expect(staffManifest.generated_by, 'the staff manifest names support').toEqual(STAFF_MANIFEST_ACTOR)

    const staffPreview = await staffGet(`${tenantPath(PERSONAS.A.tenantId)}/evidence-bundle/preview?${bundleQuery(entityA.id)}`)
    const tenantPreview = await rawFetch(`/api/invoice/v1/evidence-bundle/preview?${bundleQuery(entityA.id)}`, { headers: bearer(customer) })
    expect(staffPreview.status, 'staff preview').toBe(200)
    expect(staffPreview.body, 'the staff preview equals the tenant preview').toEqual(tenantPreview.body)
    // invoiceIds is what the spec created, not read from either preview.
    expect((staffPreview.body as Preview).counts.invoices, 'the preview counts the spec invoices').toBe(invoiceIds.length)

    const entities = await staffGet(`${tenantPath(PERSONAS.A.tenantId)}/entities`)
    expect(entities.status, 'staff entities').toBe(200)
    const listed = (entities.body as { entities: { id: string; name: string; tin: string | null }[] }).entities.find((e) => e.id === entityA.id)
    expect(listed, 'the company list holds the spec entity').toBeDefined()
    expect(listed, 'the listed company matches the spec entity').toEqual({ id: entityA.id, name: entityA.name, tin: entityA.tin })

    const other = async (path: string, message: string) => {
      const res = await staffGet(path)
      assertErrorEnvelope(res, 404, path)
      expect((res.body as { error: string }).error, path).toBe(message)
    }
    for (const suffix of ['evidence-bundle/preview', 'evidence-bundle']) {
      await other(`${tenantPath(PERSONAS.A.tenantId)}/${suffix}?${bundleQuery(entityB)}`, ENTITY_NOT_FOUND)
      await other(`${tenantPath(randomUUID())}/${suffix}?${bundleQuery(entityA.id)}`, TENANT_NOT_FOUND)
    }
    await other(`${tenantPath(randomUUID())}/entities`, TENANT_NOT_FOUND)
  })

  test('staff reads write no audit row', async () => {
    await staffSearch({ tenant: PERSONAS.A.tenantId, from: START })
    await staffGet(`${tenantPath(PERSONAS.A.tenantId)}/entities`)
    expect((await staffGet(`${tenantPath(PERSONAS.A.tenantId)}/evidence-bundle/preview?${bundleQuery(entityA.id)}`)).status).toBe(200)
    await download(`${tenantPath(PERSONAS.A.tenantId)}/evidence-bundle?${bundleQuery(entityA.id)}`, staff)

    const changes = await staffGet(STAFF_CHANGES)
    expect(changes.status, 'staff changes').toBe(200)
    const mine = (changes.body as { changes: { actor: string }[] }).changes.filter((c) => c.actor === account.userId)
    expect(mine, 'no staff_audit_log row names this staff account').toEqual([])

    const since = await getAuditLog(customer, { from: START })
    expect(since.events.length, 'the tenant log read returns the spec writes, so an empty support read means something').toBeGreaterThan(0)
    const support = await getAuditLog(customer, { actor: [SUPPORT_ACTOR], from: START })
    expect(support.events, "no support row in the tenant's log since the spec start").toEqual([])
  })

  test('a customer token is refused on every staff audit path', async () => {
    const paths = [STAFF_AUDIT, STAFF_CHANGES, `${tenantPath(PERSONAS.A.tenantId)}/entities`, `${tenantPath(PERSONAS.A.tenantId)}/evidence-bundle/preview?${bundleQuery(entityA.id)}`, `${tenantPath(PERSONAS.A.tenantId)}/evidence-bundle?${bundleQuery(entityA.id)}`]
    for (const path of paths) {
      const res = await rawFetch(path, { headers: bearer(customer) })
      assertErrorEnvelope(res, 403, `customer ${path}`)
      expect(res.body, `customer ${path}`).toEqual({ error: 'forbidden' })
    }
  })
})
