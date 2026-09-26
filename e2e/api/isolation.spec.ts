// M3-14-02 (Core AC 2): the cross-tenant isolation proof, over the wire — through the
// SAME typed seam (api/client.ts) every api/ spec shares, rather than Playwright's
// untyped `request` fixture (that's ../topology/isolation.spec.ts, the sibling this
// suite deliberately overlaps with — story Decision "positioned alongside topology,
// not consolidated"). Three properties are proven against the DEPLOYED gateway:
//   AC1 identity    — each token's /me resolves EXACTLY its own seeded tenant + domain
//                      role, never the other's.
//   AC2 membership  — each token's /memberships lists EXACTLY its own tenant's members,
//                      never the other tenant's subject.
//   AC3 mutation    — firm A cannot read/update/offboard an entity firm B owns (and
//                      symmetrically), and the failure mode is 404 (RLS row-invisibility),
//                      NOT 403 (auth/authz) — Decision A9.
// AC1/AC2 alone only prove read isolation on tenancy's own tables; AC3 additionally
// proves RLS holds on a DIFFERENT service (portfolio), reached through the same
// gateway + JWT path, across create/read/update/mutate — not just a single SELECT.
import { test, expect } from '@playwright/test'
import {
  login,
  me,
  memberships,
  createEntity,
  getEntity,
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

    // Positive: each list is exactly its tenant's seeded members (A: 6, B: 7).
    expect(userIdsA).toEqual([...TENANTS.a.members].sort())
    expect(userIdsB).toEqual([...TENANTS.b.members].sort())

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
  test("B's edit, validate, transition, submit and approve of A's invoice each answer a random id's 404 and change nothing", async () => {
    test.setTimeout(120_000)

    const tin = freshTin()
    const entity = await createEntity(tokenA, { name: `Zz TEST-04 cross-tenant ${tin}`, tin })
    const created = await createInvoice(tokenA, { entity_id: entity.id, ...cleanInvoiceFields(`INV-TEST-04-X-${freshTin()}`) })
    const validated = await validateInvoice(tokenA, created.id)
    expect(validated.status, 'the clean fixture must promote draft -> validated').toBe('validated')

    const ownRead = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${tokenA}` } })
    expect(ownRead.status, "positive control: A's own GET of its invoice").toBe(200)

    const before = await snapshotInvoice(tokenA, created.id)
    expect(before.approval.state, 'validate must arm an open run, so approve has a real target').toBe('open')

    // Well-formed bodies, so tenancy is the only reason left to refuse.
    const headers = { Authorization: `Bearer ${tokenB}` }
    const verbs: Record<string, (id: string) => ReturnType<typeof rawFetch>> = {
      edit: (id) => rawFetch(`/api/invoice/v1/invoices/${id}`, { method: 'PATCH', headers, body: { buyer_name: 'cross-tenant write' } }),
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
    expect(after.invoice, "A's invoice (Invoice keys) after B's five writes").toEqual(before.invoice)
    expect(after.history, "A's status history after B's five writes").toEqual(before.history)
    expect(after.approval, "A's approval run after B's five writes").toEqual(before.approval)
  })
})
