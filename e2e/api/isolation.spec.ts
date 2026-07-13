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
  PERSONAS,
  ApiError,
} from './client'
import { freshTin } from './fixtures'
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

    // Positive: each list is exactly its tenant's seeded members (A: 3, B: 1).
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
