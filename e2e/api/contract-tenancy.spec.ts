// M3-15-05 (Order 5 of 5, FINAL): the tenancy contract spec, over the wire —
// through the SAME typed seam (api/client.ts) every api/ spec shares.
// Read-only: mints tokens via login() and GETs /me + /memberships only — no
// writes, no schema/fixture change. Assertions go through rawFetch
// (M3-15-01), so the exact HTTP status + envelope shape is directly
// observable — unlike apiFetch, which normalizes a non-2xx into a thrown
// ApiError. Mirrors contract-portfolio.spec.ts's assertErrorEnvelope
// convention (its own local copy, not imported — matches this suite's
// existing per-file duplication of the shape helper).
//
// Three properties are proven against the DEPLOYED gateway:
//   - Happy-path status + shape (persona A, Core AC 1): /me -> 200 +
//     {tenant:{id,name,kind}, user:{id,role}}; /memberships -> 200 +
//     {memberships:[{user_id,role},...]}.
//   - Error-path status + envelope on GET /me: both cases mint their token
//     via the EXISTING login() helper — no new fixtures. login() only reads
//     persona.subject + persona.tenantId (client.ts), so a plain object
//     spread of PERSONAS.A with one field overridden is enough to express a
//     custom (subject, tenant_id) pair; no change to login()/client.ts was
//     needed.
//     - 403 (no membership): tenant_id = seeded tenant A, subject = persona
//       B (a real seeded UUID, member of tenant B only). Gateway
//       authorize() passes (tenant_id non-empty) -> tenancy resolves tenant
//       A under RLS -> the membership lookup for subject-B-in-tenant-A finds
//       no row -> ErrNoMembership -> 403.
//     - 404 (unknown tenant): tenant_id = crypto.randomUUID() (syntactically
//       valid, nonexistent). The tenant lookup runs first under RLS and
//       returns 0 rows -> ErrTenantNotFound -> 404, before membership is
//       ever queried.
//   - List-shape boundary (Core AC 4): /memberships is a flat, non-paginated
//     list — the body has NO `pagination` key (the paginated list is
//     portfolio's, covered in M3-15-03).
//
// Deliberately NOT covered here:
//   - 500: legitimately unreachable without breaking the server (a real
//     DB/infra failure) — not something this suite can trigger on demand.
//   - 401 (missing/malformed/invalid-Bearer auth): the shared pre-routing
//     auth-failure envelope is already proven for the tenancy surface (and
//     cross-surface) by auth-contract.spec.ts (M3-15-02).
import { test, expect } from '@playwright/test'
import { login, rawFetch, PERSONAS } from './client'

type RawResult = { status: number; body: unknown }

// assertErrorEnvelope(): the shared error-path assertion — a rejected
// request must carry the EXPECTED status and a body that is EXACTLY the
// shared envelope shape {error: <string>}: a plain object with one key,
// `error`, whose value is a string. Mirrors contract-portfolio.spec.ts's
// helper of the same name.
function assertErrorEnvelope(result: RawResult, expectedStatus: number, label: string): void {
  expect(result.status, `${label}: expected HTTP ${expectedStatus}`).toBe(expectedStatus)
  expect(result.body, `${label}: expected a parsed JSON object body`).toBeInstanceOf(Object)
  const body = result.body as Record<string, unknown>
  expect(Object.keys(body), `${label}: expected exactly one key, 'error'`).toEqual(['error'])
  expect(typeof body.error, `${label}: expected body.error to be a string`).toBe('string')
}

test.describe('tenancy contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test.describe('happy-path status + shape (persona A)', () => {
    test('/me -> 200 + {tenant:{id,name,kind}, user:{id,role}}', async () => {
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, '/me should return 200').toBe(200)

      const body = res.body as Record<string, unknown>
      expect(Object.keys(body).sort(), 'expected exactly the tenant/user top-level keys').toEqual(['tenant', 'user'])

      const tenant = body.tenant as Record<string, unknown>
      expect(Object.keys(tenant).sort(), 'expected exactly the tenant.{id,name,kind} keys').toEqual([
        'id',
        'kind',
        'name',
      ])
      // Known seeded values (db/seed.dev.sql) — same convention as
      // isolation.spec.ts's AC1: a real, resolvable identity, not just a
      // well-typed one.
      expect(tenant.id).toBe(PERSONAS.A.tenantId)
      expect(tenant.name).toBe('Okafor & Partners')
      expect(tenant.kind).toBe('firm')

      const user = body.user as Record<string, unknown>
      expect(Object.keys(user).sort(), 'expected exactly the user.{id,role} keys').toEqual(['id', 'role'])
      expect(user.id).toBe(PERSONAS.A.subject)
      expect(user.role).toBe('admin')
    })

    test('/memberships -> 200 + {memberships:[{user_id,role},...]}, no pagination key', async () => {
      const res = await rawFetch('/api/tenancy/v1/memberships', {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, '/memberships should return 200').toBe(200)

      const body = res.body as Record<string, unknown>
      expect(Array.isArray(body.memberships), 'body.memberships should be an array').toBe(true)
      const members = body.memberships as Array<Record<string, unknown>>
      expect(members.length, 'tenant A has seeded members').toBeGreaterThan(0)
      for (const m of members) {
        expect(typeof m.user_id, 'each membership.user_id should be a string').toBe('string')
        expect(typeof m.role, 'each membership.role should be a string').toBe('string')
      }

      // List-shape boundary (Core AC 4): memberships is a flat,
      // non-paginated list — unlike portfolio's list (M3-15-03), it must
      // NOT carry a pagination envelope.
      expect(body, 'memberships should have no pagination key').not.toHaveProperty('pagination')
    })
  })

  test.describe('error paths on GET /me', () => {
    test('403 (no membership): tenant A token, persona B subject -> {error: string}', async () => {
      const noMembershipToken = await login({ ...PERSONAS.A, subject: PERSONAS.B.subject })
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: `Bearer ${noMembershipToken}` },
      })
      assertErrorEnvelope(res, 403, 'tenant A token + persona B subject (non-member of A)')
    })

    test('404 (unknown tenant): random-UUID tenant_id -> {error: string}', async () => {
      const unknownTenantToken = await login({ ...PERSONAS.A, tenantId: crypto.randomUUID() })
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: `Bearer ${unknownTenantToken}` },
      })
      assertErrorEnvelope(res, 404, 'unknown tenant (random UUID tenant_id)')
    })
  })
})
