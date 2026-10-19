// M3-14-02 (Core AC 2): the cross-tenant isolation proof, over the wire — through the
// SAME typed seam (api/client.ts) every api/ spec shares, rather than Playwright's
// untyped `request` fixture (that's ../topology/isolation.spec.ts, the sibling this
// suite deliberately overlaps with — story Decision "positioned alongside topology,
// not consolidated"). Three properties are proven against the DEPLOYED gateway:
//   AC1 identity    — each token's /me resolves EXACTLY its own seeded tenant + domain
//                      role, never the other's.
//   AC2 membership  — each token's /memberships lists EXACTLY its own tenant's seeded members
//                      (plus its e2e role accounts), never the other tenant's subject.
//   AC3 mutation    — firm A cannot read/update/offboard an entity firm B owns (and
//                      symmetrically), and the failure mode is 404 (RLS row-invisibility),
//                      NOT 403 (auth/authz) — Decision A9.
// AC1/AC2 alone only prove read isolation on tenancy's own tables; AC3 additionally
// proves RLS holds on a DIFFERENT service (portfolio), reached through the same
// gateway + JWT path, across create/read/update/mutate — not just a single SELECT.
// The last describe adds invoice writes: B's five writes to A's invoice answer an
// unknown id's 404 and change nothing.
import { test, expect } from '@playwright/test'
import {
  apiBase,
  login,
  me,
  memberships,
  createEntity,
  getEntity,
  listEntities,
  listInvoices,
  listCustomers,
  createCustomer,
  getAuditLog,
  rollup,
  updateEntity,
  offboardEntity,
  createInvoice,
  validateInvoice,
  getInvoice,
  getInvoiceHistory,
  getInvoiceApproval,
  rawFetch,
  PERSONAS,
  ApiError,
  type Invoice,
} from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope, ensureFirmPolicyActive } from './contract-helpers'
import { TENANTS } from '../topology/targets'
import { isE2eMemberEmail, isSeededMember } from '../realAccounts'

// captureRejection(): mirrors packages/api-client/src/client.test.ts's helper — wraps a
// thunk so a call that resolves (the wrong outcome here) fails loudly with a clear
// message, rather than silently passing before the ApiError assertions below ever run.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected the cross-tenant call to reject, but it resolved with a 2xx')
}

// assertNotFoundAcrossTenant(): the AC3 core assertion, applied to each of
// get/update/offboard — cross-tenant access must fail as ApiError{kind:'http',status:404}
// (A9), never resolve 200 with the other tenant's row.
async function assertNotFoundAcrossTenant(thunk: () => unknown, label: string): Promise<void> {
  const err = await captureRejection(thunk)
  expect(err, `${label}: expected an ApiError`).toBeInstanceOf(ApiError)
  const apiErr = err as ApiError
  expect(apiErr.kind, `${label}: expected ApiError.kind 'http'`).toBe('http')
  expect(apiErr.status, `${label}: expected 404 (RLS row-invisibility, not 403 — A9)`).toBe(404)
}

test.describe('cross-tenant isolation (API E2E, over the deployed gateway)', () => {
  test("AC1: /me resolves exactly the caller's own tenant + domain role, never the other's", async () => {
    const tokenA = await login(PERSONAS.A)
    const tokenB = await login(PERSONAS.B)

    const meA = await me(tokenA)
    const meB = await me(tokenB)

    expect(meA.tenant.id).toBe(PERSONAS.A.tenantId)
    expect(meA.tenant.name).toBe('Okafor & Partners')
    expect(meA.tenant.kind).toBe('firm')
    expect(meA.user.role).toBe('admin')

    expect(meB.tenant.id).toBe(PERSONAS.B.tenantId)
    expect(meB.tenant.name).toBe('Honeywell Group')
    expect(meB.tenant.kind).toBe('in_house')
    expect(meB.user.role).toBe('admin')

    // The isolation proof: both rows exist in the same tenants table, yet neither token
    // ever resolves the other's id — RLS, not a missing row, is what scopes the SELECT.
    expect(meA.tenant.id).not.toBe(meB.tenant.id)
  })

  test("AC2: /memberships lists exactly the caller's own tenant's members, never the other tenant's subject", async () => {
    const tokenA = await login(PERSONAS.A)
    const tokenB = await login(PERSONAS.B)

    const { memberships: membersA } = await memberships(tokenA)
    const { memberships: membersB } = await memberships(tokenB)

    const userIdsA = membersA.map((m) => m.user_id).sort()
    const userIdsB = membersB.map((m) => m.user_id).sort()

    // Positive: the seeded subset is exactly the tenant's seeded members (A: 7, B: 7); any other
    // member must be one of the tenant's e2e role accounts (realAccounts.ts).
    expect(userIdsA.filter(isSeededMember)).toEqual([...TENANTS.a.members].sort())
    expect(userIdsB.filter(isSeededMember)).toEqual([...TENANTS.b.members].sort())
    for (const m of membersA.filter((m) => !isSeededMember(m.user_id))) expect(isE2eMemberEmail(TENANTS.a.id, m.email ?? ''), `a non-seeded member of A: ${m.email}`).toBe(true)
    for (const m of membersB.filter((m) => !isSeededMember(m.user_id))) expect(isE2eMemberEmail(TENANTS.b.id, m.email ?? ''), `a non-seeded member of B: ${m.email}`).toBe(true)

    // Negative: neither list leaks the other tenant's subject.
    expect(userIdsA).not.toContain(PERSONAS.B.subject)
    expect(userIdsB).not.toContain(PERSONAS.A.subject)
  })

  test('AC3: firm A cannot read/update/offboard an entity firm B owns — 404, not 403 (A9)', async () => {
    const tokenA = await login(PERSONAS.A)
    const tokenB = await login(PERSONAS.B)

    const tinB = freshTin()
    const entityB = await createEntity(tokenB, { name: `M3-14-02 isolation B ${tinB}`, tin: tinB })
    expect(entityB.status).toBe('active')

    // Positive control: B (the owner) can still read its own just-created entity — this
    // guards against a "deny everything" false pass, where the negative asserts below
    // would still hold even if RLS 404'd unconditionally rather than scoping by tenant.
    const ownRead = await getEntity(tokenB, entityB.id)
    expect(ownRead.id).toBe(entityB.id)

    await assertNotFoundAcrossTenant(() => getEntity(tokenA, entityB.id), 'A reads B entity')
    await assertNotFoundAcrossTenant(
      () => updateEntity(tokenA, entityB.id, { name: 'cross-tenant-should-fail' }),
      'A updates B entity',
    )
    await assertNotFoundAcrossTenant(() => offboardEntity(tokenA, entityB.id), 'A offboards B entity')

    // Zero-side-effect check: a 404 response could in principle mask a write that went
    // through anyway. Re-read as the owner (B) and confirm the blocked update/offboard
    // attempts left the row completely untouched.
    const afterAttack = await getEntity(tokenB, entityB.id)
    expect(afterAttack.name).toBe(entityB.name)
    expect(afterAttack.status).toBe('active')
  })

  test('AC3 (symmetric): firm B cannot read/update/offboard an entity firm A owns — 404, not 403 (A9)', async () => {
    const tokenA = await login(PERSONAS.A)
    const tokenB = await login(PERSONAS.B)

    const tinA = freshTin()
    const entityA = await createEntity(tokenA, { name: `M3-14-02 isolation A ${tinA}`, tin: tinA })
    expect(entityA.status).toBe('active')

    // Positive control: A (the owner) can still read its own just-created entity — see
    // the rationale in the test above.
    const ownRead = await getEntity(tokenA, entityA.id)
    expect(ownRead.id).toBe(entityA.id)

    await assertNotFoundAcrossTenant(() => getEntity(tokenB, entityA.id), 'B reads A entity')
    await assertNotFoundAcrossTenant(
      () => updateEntity(tokenB, entityA.id, { name: 'cross-tenant-should-fail' }),
      'B updates A entity',
    )
    await assertNotFoundAcrossTenant(() => offboardEntity(tokenB, entityA.id), 'B offboards A entity')

    // Zero-side-effect check: see the rationale in the test above.
    const afterAttack = await getEntity(tokenA, entityA.id)
    expect(afterAttack.name).toBe(entityA.name)
    expect(afterAttack.status).toBe('active')
  })
})

// cleanInvoiceFields(): own copy of persona-inhouse.spec.ts's fixture (no cross-suite
// imports). Fires zero violations, so validate promotes it and arms the firm run.
function cleanInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

// satisfies makes the list exhaustive both ways: a key added to or dropped from Invoice
// fails typecheck here.
const INVOICE_KEYS = Object.keys({
  id: true,
  entity_id: true,
  import_batch_id: true,
  invoice_number: true,
  status: true,
  issue_date: true,
  supplier_tin: true,
  supplier_name: true,
  buyer_tin: true,
  buyer_name: true,
  currency: true,
  subtotal: true,
  vat: true,
  total: true,
  violations: true,
  rule_set_version_id: true,
  created_at: true,
  irn: true,
  csid: true,
  qr_payload: true,
  rejection_reasons: true,
  kept_as_is_at: true,
  kept_as_is_by: true,
  kept_as_is_reason: true,
  invoice_kind: true,
  tax_currency_code: true,
  due_date: true,
  issue_time: true,
  tax_point_date: true,
  payment_status: true,
  supplier_email: true,
  supplier_telephone: true,
  supplier_street: true,
  supplier_city: true,
  supplier_postal_zone: true,
  supplier_country: true,
  supplier_state: true,
  supplier_lga: true,
  buyer_email: true,
  buyer_telephone: true,
  buyer_street: true,
  buyer_city: true,
  buyer_postal_zone: true,
  buyer_country: true,
  buyer_state: true,
  buyer_lga: true,
  line_items: true,
} satisfies Record<keyof Invoice, true>) as (keyof Invoice)[]

// snapshotInvoice(): what a cross-tenant write could change. overdue is computed from
// time.Now() on every read, so it and its input due_at are dropped from each step.
async function snapshotInvoice(token: string, id: string) {
  const inv = await getInvoice(token, id)
  const run = await getInvoiceApproval(token, id)
  return {
    invoice: Object.fromEntries(INVOICE_KEYS.map((k) => [k, inv[k]])),
    history: await getInvoiceHistory(token, id),
    approval: {
      run_id: run.run_id,
      state: run.state,
      steps: run.steps.map(({ due_at, overdue, ...rest }) => rest),
    },
  }
}

// Each B refusal is compared with B's refusal for a random id, so a route that 404s
// everyone cannot pass and no body works as an existence oracle.
test.describe('cross-tenant invoice writes (API E2E)', () => {
  let tokenA: string
  let tokenB: string

  test.beforeAll(async () => {
    tokenA = await login(PERSONAS.A)
    tokenB = await login(PERSONAS.B)
    await ensureFirmPolicyActive(tokenA)
  })

  // One test, so a retry re-arranges a fresh invoice and the after-read shares the writes' retry unit.
  test("B's edit, line edit, validate, transition, submit and approve of A's invoice each answer a random id's 404 and change nothing", async () => {
    test.setTimeout(120_000)

    const tin = freshTin()
    const entity = await createEntity(tokenA, { name: `Zz TEST-04 cross-tenant ${tin}`, tin })
    const created = await createInvoice(tokenA, { entity_id: entity.id, ...cleanInvoiceFields(`INV-TEST-04-X-${freshTin()}`) })
    const validated = await validateInvoice(tokenA, created.id)
    expect(validated.status, 'the clean fixture must promote draft -> validated').toBe('validated')

    const ownRead = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${tokenA}` } })
    expect(ownRead.status, "positive control: A's own GET of its invoice").toBe(200)

    const ownLineId = (await getInvoice(tokenA, created.id)).line_items?.[0]?.id
    expect(ownLineId, "A's own line id, which B's line edit presents").toBeTruthy()

    const before = await snapshotInvoice(tokenA, created.id)
    expect(before.approval.state, 'validate must arm an open run, so approve has a real target').toBe('open')
    expect(before.approval.steps.length, 'the open run must carry steps, or its deep-equal is vacuous').toBeGreaterThan(0)
    expect(before.history.length, "A's history must hold the validate transition").toBeGreaterThan(0)

    // Well-formed bodies, so tenancy is the only reason left to refuse.
    const headers = { Authorization: `Bearer ${tokenB}` }
    const verbs: Record<string, (id: string) => ReturnType<typeof rawFetch>> = {
      edit: (id) =>
        rawFetch(`/api/invoice/v1/invoices/${id}`, {
          method: 'PATCH',
          headers,
          body: { buyer_name: 'cross-tenant write', buyer_state: 'NG-LA', issue_time: '10:00:00' },
        }),
      editLine: (id) =>
        rawFetch(`/api/invoice/v1/invoices/${id}`, {
          method: 'PATCH',
          headers,
          body: { line_items: [{ id: ownLineId, description: 'cross-tenant write', tax_category: 'EXEMPTED' }] },
        }),
      validate: (id) => rawFetch(`/api/invoice/v1/invoices/${id}/validate`, { method: 'POST', headers }),
      transition: (id) => rawFetch(`/api/invoice/v1/invoices/${id}/transitions`, { method: 'POST', headers, body: { target: 'queued' } }),
      submit: (id) =>
        rawFetch('/api/invoice/v1/invoices/submissions', {
          method: 'POST',
          headers,
          body: { invoice_ids: [id], idempotency_key: crypto.randomUUID() },
        }),
      approve: (id) => rawFetch(`/api/invoice/v1/invoices/${id}/approvals`, { method: 'POST', headers, body: { decision: 'approved' } }),
    }

    for (const [verb, send] of Object.entries(verbs)) {
      const onA = await send(created.id)
      const onRandom = await send(crypto.randomUUID())
      assertErrorEnvelope(onA, 404, `${verb}: B on A's invoice`)
      assertErrorEnvelope(onRandom, 404, `${verb}: B on a random id`)
      expect(onA.body, `${verb}: B's refusal for A's invoice must equal its refusal for a random id`).toEqual(onRandom.body)
    }

    const after = await snapshotInvoice(tokenA, created.id)
    expect(after.invoice, "A's invoice (Invoice keys) after B's six writes").toEqual(before.invoice)
    expect(after.history, "A's status history after B's six writes").toEqual(before.history)
    expect(after.approval, "A's approval run after B's six writes").toEqual(before.approval)
  })
})

// db/seed.dev.sql: Amaka Eze (…0014, a reviewer on 1111) holds the first two; the third is a
// client of the same firm she does not hold.
const LIMITED_REVIEWER = 'c0000000-0000-0000-0000-000000000014'
const OWN_CLIENT = 'Adeyemi & Sons Trading Ltd'
const OTHER_OWN_CLIENT = 'Chukwu Global Ventures Ltd'
const NOT_HELD_CLIENT = 'Okonkwo Textiles Nigeria Ltd'
const EVIDENCE_PERIOD = { from: '2020-01-01T00:00:00Z', to: '2035-01-01T00:00:00Z' }
const REGISTER_PERIOD = { from: '2026-01-01', to: '2026-12-31' }

test.describe('a limited member of one firm reads only its clients (FIRMS-03)', () => {
  let limited: string
  let admin: string
  let ownEntityId: string
  let otherOwnEntityId: string
  let notHeldEntityId: string
  let ownInvoiceId: string
  let notHeldInvoiceId: string
  let freshEntityId: string

  test.beforeAll(async () => {
    limited = await login({ ...PERSONAS.A, subject: LIMITED_REVIEWER })
    admin = await login(PERSONAS.A)
    const idOf = async (name: string) => {
      const { entities } = await listEntities(admin, { q: name, limit: 200 })
      const found = entities.find((e) => e.name === name)
      expect(found, `${name} is a seeded client of 1111`).toBeTruthy()
      return found!.id
    }
    ownEntityId = await idOf(OWN_CLIENT)
    otherOwnEntityId = await idOf(OTHER_OWN_CLIENT)
    notHeldEntityId = await idOf(NOT_HELD_CLIENT)
    const firstInvoice = async (entityId: string) => {
      const { invoices } = await listInvoices(admin, { entity_id: entityId, limit: 1 })
      expect(invoices.length, `the seed gives client ${entityId} an invoice`).toBeGreaterThan(0)
      return invoices[0].id
    }
    ownInvoiceId = await firstInvoice(ownEntityId)
    // The seed arms no approval run, so validate a fresh invoice of a client she does not hold.
    const fresh = await createEntity(admin, { name: `Zz FIRMS-03 not held ${freshTin()}`, tin: freshTin() })
    freshEntityId = fresh.id
    const created = await createInvoice(admin, { entity_id: fresh.id, ...cleanInvoiceFields(`INV-FIRMS-03-${freshTin()}`) })
    expect((await validateInvoice(admin, created.id)).status, 'the clean fixture must promote draft -> validated').toBe('validated')
    notHeldInvoiceId = created.id
    expect((await getInvoiceApproval(admin, notHeldInvoiceId)).steps.length, 'positive control: the admin reads its approval run').toBeGreaterThan(0)
  })

  test('FIRMS-03: the limited reviewer lists exactly its two clients', async () => {
    const { entities, pagination } = await listEntities(limited, { limit: 200 })
    expect(entities.map((e) => e.name).sort()).toEqual([OWN_CLIENT, OTHER_OWN_CLIENT].sort())
    expect(pagination.total).toBe(2)
  })

  test('FIRMS-03: the limited reviewer reads its client and gets 404 for the other client', async () => {
    const before = await snapshotInvoice(admin, notHeldInvoiceId)

    expect((await getEntity(limited, ownEntityId)).id, 'positive control: its own client').toBe(ownEntityId)
    expect((await getInvoice(limited, ownInvoiceId)).id, 'positive control: its own invoice').toBe(ownInvoiceId)

    await assertNotFoundAcrossTenant(() => getEntity(limited, notHeldEntityId), 'entity of a client it does not hold')
    await assertNotFoundAcrossTenant(() => getInvoice(limited, notHeldInvoiceId), 'invoice of a client it does not hold')
    await assertNotFoundAcrossTenant(() => getInvoiceHistory(limited, notHeldInvoiceId), 'history of that invoice')
    await assertNotFoundAcrossTenant(() => getInvoiceApproval(limited, notHeldInvoiceId), 'approval run of that invoice')
    await assertNotFoundAcrossTenant(() => listInvoices(limited, { entity_id: notHeldEntityId }), 'invoice list filtered to that client')
    await assertNotFoundAcrossTenant(() => getEntity(limited, freshEntityId), 'entity of the client holding that invoice')

    const preview = new URLSearchParams({ entity_id: notHeldEntityId, ...EVIDENCE_PERIOD })
    const control = await rawFetch(`/api/invoice/v1/evidence-bundle/preview?${preview}`, { headers: { Authorization: `Bearer ${admin}` } })
    expect(control.status, 'positive control: the admin reads the same preview').toBe(200)
    assertErrorEnvelope(
      await rawFetch(`/api/invoice/v1/evidence-bundle/preview?${preview}`, { headers: { Authorization: `Bearer ${limited}` } }),
      404,
      'bundle preview of that client',
    )

    expect(await snapshotInvoice(admin, notHeldInvoiceId), "the other client's invoice is unchanged").toEqual(before)
  })

  test("FIRMS-03: the limited reviewer's register of the other client is 404", async () => {
    const params = new URLSearchParams({ entity_id: notHeldEntityId, ...REGISTER_PERIOD })
    const control = await fetch(`${apiBase()}/api/invoice/v1/invoice-register?${params}`, { headers: { Authorization: `Bearer ${admin}` } })
    expect(control.status, 'positive control: the admin downloads the same register').toBe(200)
    const res = await rawFetch(`/api/invoice/v1/invoice-register?${params}`, { headers: { Authorization: `Bearer ${limited}` } })
    assertErrorEnvelope(res, 404, 'register of a client it does not hold')
  })

  test("FIRMS-03: the unfiltered invoice list holds only the limited reviewer's clients", async () => {
    const { invoices } = await listInvoices(limited, {})
    expect(invoices.length, 'its two clients carry invoices').toBeGreaterThan(0)
    const held = new Set([ownEntityId, otherOwnEntityId])
    for (const inv of invoices) expect(held.has(inv.entity_id), `invoice ${inv.id} belongs to one of its clients`).toBe(true)
  })
})

// db/seed.dev.sql: Folake Adesina (...0003, a preparer on 1111) holds "All clients".
const ALL_CLIENTS_PREPARER = 'c0000000-0000-0000-0000-000000000003'

test.describe("screens that span clients stop at a limited member's clients (FIRMS-04)", () => {
  let limited: string
  let admin: string
  let allClients: string
  let held: string[]
  let ownInvoiceId: string
  let notHeldInvoiceId: string
  let tin: string
  let customerId: string

  const COUNT_KEYS = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed'] as const

  test.beforeAll(async () => {
    limited = await login({ ...PERSONAS.A, subject: LIMITED_REVIEWER })
    admin = await login(PERSONAS.A)
    allClients = await login({ ...PERSONAS.A, subject: ALL_CLIENTS_PREPARER })
    const idOf = async (name: string) => {
      const { entities } = await listEntities(admin, { q: name, limit: 200 })
      const found = entities.find((e) => e.name === name)
      expect(found, `${name} is a seeded client of 1111`).toBeTruthy()
      return found!.id
    }
    const ownEntityId = await idOf(OWN_CLIENT)
    const otherOwnEntityId = await idOf(OTHER_OWN_CLIENT)
    held = [ownEntityId, otherOwnEntityId]
    for (const entityId of held) {
      const { invoices } = await listInvoices(admin, { entity_id: entityId, limit: 1 })
      expect(invoices.length, `the seed gives client ${entityId} an invoice`).toBeGreaterThan(0)
    }
    const validated = async (entityId: string, label: string) => {
      const created = await createInvoice(admin, { entity_id: entityId, ...cleanInvoiceFields(`INV-FIRMS-04-${label}-${freshTin()}`) })
      expect((await validateInvoice(admin, created.id)).status, 'the clean fixture must promote draft -> validated').toBe('validated')
      return created.id
    }
    const fresh = await createEntity(admin, { name: `Zz FIRMS-04 not held ${freshTin()}`, tin: freshTin() })
    notHeldInvoiceId = await validated(fresh.id, 'NH')
    ownInvoiceId = await validated(ownEntityId, 'OWN')
    tin = freshTin()
    customerId = (await createCustomer(admin, { tin, name: `FIRMS-04 buyer ${tin}` })).id
  })

  test("FIRMS-04: the limited reviewer's rollup counts only its clients", async () => {
    const [mine, all] = await Promise.all([rollup(limited), rollup(admin)])
    expect(mine.clients.map((c) => c.entity_id).sort()).toEqual([...held].sort())
    expect(all.clients.map((c) => c.entity_id), 'positive control: the admin rollup holds the not-held client').toContain(
      (await getInvoice(admin, notHeldInvoiceId)).entity_id,
    )
    for (const c of mine.clients) {
      const same = all.clients.find((a) => a.entity_id === c.entity_id)
      expect(same, `the admin rollup holds ${c.entity_id}`).toBeTruthy()
      expect(c.counts, `counts of ${c.entity_id}`).toEqual(same!.counts)
    }
    for (const k of COUNT_KEYS) {
      expect(mine.totals.counts[k], `totals.counts.${k}`).toBe(mine.clients.reduce((n, c) => n + c.counts[k], 0))
    }
  })

  test("FIRMS-04: the limited reviewer's audit log holds no other client's and no workspace row", async () => {
    expect((await getAuditLog(admin, { invoice_id: notHeldInvoiceId })).events.length, 'positive control: the admin reads the not-held invoice rows').toBeGreaterThan(0)
    expect((await getAuditLog(limited, { invoice_id: notHeldInvoiceId })).events).toEqual([])
    expect((await getAuditLog(limited, { invoice_id: ownInvoiceId })).events.length, 'its own invoice rows').toBeGreaterThan(0)

    expect((await getAuditLog(limited, { company: 'workspace' })).events).toEqual([])

    const adminTin = await getAuditLog(admin, { q: tin })
    expect(
      adminTin.events.find((e) => e.event === 'customers.customer.created' && e.entity_id === null),
      'positive control: the admin reads the customer row',
    ).toBeTruthy()
    expect((await getAuditLog(limited, { q: tin })).events).toEqual([])

    const page = await getAuditLog(limited, { limit: 100 })
    expect(page.events.length, 'its first page is not empty').toBeGreaterThan(0)
    for (const e of page.events) expect(held, `event ${e.id} belongs to one of its clients`).toContain(e.entity_id)
  })

  test('FIRMS-04: an All clients member keeps workspace rows', async () => {
    const byTin = await getAuditLog(allClients, { q: tin })
    expect(byTin.events.find((e) => e.event === 'customers.customer.created' && e.entity_id === null), 'the customer row').toBeTruthy()

    const workspace = await getAuditLog(allClients, { company: 'workspace' })
    expect(workspace.events.length, 'workspace rows').toBeGreaterThan(0)
    for (const e of workspace.events) expect(e.entity_id, `workspace event ${e.id}`).toBeNull()

    expect((await getAuditLog(allClients, { invoice_id: notHeldInvoiceId })).events.length, 'the not-held invoice rows').toBeGreaterThan(0)
  })

  test("FIRMS-04: the limited reviewer's Approvals queue holds only its clients", async () => {
    const mine = await listInvoices(limited, { awaiting_approval: true, limit: 200 })
    const ids = mine.invoices.map((i) => i.id)
    expect(ids, 'its own validated invoice').toContain(ownInvoiceId)
    for (const i of mine.invoices) expect(held, `invoice ${i.id} belongs to one of its clients`).toContain(i.entity_id)
    expect(ids).not.toContain(notHeldInvoiceId)

    const all = await listInvoices(admin, { awaiting_approval: true, limit: 200 })
    expect(all.invoices.map((i) => i.id), 'positive control: the admin queue holds the not-held invoice').toContain(notHeldInvoiceId)
  })

  test('FIRMS-04: the buyer directory stays shared', async () => {
    const { customers } = await listCustomers(limited, { tin })
    expect(customers.map((c) => c.id)).toContain(customerId)
  })
})
