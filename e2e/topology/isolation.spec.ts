import { test, expect, type APIRequestContext } from '@playwright/test'
import { GATEWAY_URL, TENANTS } from './targets'

// M2-14 deliverable (3): live cross-tenant isolation over the full edge path. For a
// seeded tenant, mint a token via the gateway's mock issuer and read GET
// /api/tenancy/v1/me — the response is whatever the gateway (JWT verify → inject
// X-Tenant-ID) and tenancy (SET LOCAL app.current_tenant → RLS-scoped SELECT) resolve.
//
// M3-02 (A5): repointed from the throwaway aaaa…/bbbb… fixtures to the two real seeded
// persona tenants (firm 1111…, in-house 2222…). /me is now membership-gated (M3-02-01),
// so a non-member subject would 403 rather than 200 — the persona subjects are seeded
// admins of their own tenant (db/seed.dev.sql), which also makes this a more meaningful
// live proof: real firm vs. in-house identities, not arbitrary fixture rows.
interface Me {
  tenant: { id: string; name: string; kind: string }
  user: { id: string; role: string }
}

async function resolveTenant(request: APIRequestContext, t: { id: string; subject: string }): Promise<Me> {
  const login = await request.post(`${GATEWAY_URL}/auth/login`, {
    data: { subject: t.subject, role: 'authenticated', tenant_id: t.id },
  })
  expect(login.ok(), `auth/login for ${t.id} returned HTTP ${login.status()}`).toBeTruthy()
  const { access_token } = (await login.json()) as { access_token: string }

  const me = await request.get(`${GATEWAY_URL}/api/tenancy/v1/me`, {
    headers: { Authorization: `Bearer ${access_token}` },
  })
  expect(me.ok(), `/v1/me for ${t.id} returned HTTP ${me.status()}`).toBeTruthy()
  return (await me.json()) as Me
}

test('cross-tenant isolation: each token reads exactly its own tenant + domain role through the live gateway', async ({
  request,
}) => {
  const a = await resolveTenant(request, TENANTS.a)
  const b = await resolveTenant(request, TENANTS.b)

  // Each token resolves EXACTLY its own seeded tenant — the bare SELECT under RLS returns
  // only the row whose id equals app.current_tenant — including the kind discriminator
  // (firm vs. in_house) and the caller's domain role resolved from `memberships`.
  expect(a.tenant.id).toBe(TENANTS.a.id)
  expect(a.tenant.name).toBe(TENANTS.a.name)
  expect(a.tenant.kind).toBe(TENANTS.a.kind)
  expect(a.user.role).toBe(TENANTS.a.role)
  expect(b.tenant.id).toBe(TENANTS.b.id)
  expect(b.tenant.name).toBe(TENANTS.b.name)
  expect(b.tenant.kind).toBe(TENANTS.b.kind)
  expect(b.user.role).toBe(TENANTS.b.role)

  // The isolation proof: tenant B's row exists in the same table, yet tenant A never
  // resolves it (and vice versa). Both rows being present is what makes RLS — not a
  // missing row — demonstrably the filter that keeps A from reading B.
  expect(a.tenant.id).not.toBe(b.tenant.id)
  expect(a.tenant.id).not.toBe(TENANTS.b.id)
  expect(b.tenant.id).not.toBe(TENANTS.a.id)
})

// M3-02 (AC #6, live): the core claim isn't just "each token resolves its own tenant" —
// it's that firm A's session cannot READ firm B's MEMBERSHIPS. /me alone doesn't probe
// that (it returns one row, the caller's own tenant); GET /v1/memberships returns the
// whole member LIST for the caller's tenant, so this is the end-to-end deployed proof
// that RLS — not application code — is what scopes the list, exactly like the
// service-layer proof (internal/tenancy/tenancy_test.go TestStoreListMemberships_*).
async function fetchMemberships(
  request: APIRequestContext,
  t: { id: string; subject: string },
): Promise<{ memberships: { user_id: string; role: string }[] }> {
  const login = await request.post(`${GATEWAY_URL}/auth/login`, {
    data: { subject: t.subject, role: 'authenticated', tenant_id: t.id },
  })
  expect(login.ok(), `auth/login for ${t.id} returned HTTP ${login.status()}`).toBeTruthy()
  const { access_token } = (await login.json()) as { access_token: string }

  const res = await request.get(`${GATEWAY_URL}/api/tenancy/v1/memberships`, {
    headers: { Authorization: `Bearer ${access_token}` },
  })
  expect(res.ok(), `/v1/memberships for ${t.id} returned HTTP ${res.status()}`).toBeTruthy()
  return (await res.json()) as { memberships: { user_id: string; role: string }[] }
}

test('cross-tenant isolation: each tenant token lists exactly its own members through the live gateway', async ({
  request,
}) => {
  const a = await fetchMemberships(request, TENANTS.a)
  const b = await fetchMemberships(request, TENANTS.b)

  const aUserIds = a.memberships.map((m) => m.user_id).sort()
  const bUserIds = b.memberships.map((m) => m.user_id).sort()

  // Positive: the firm token's list is exactly its 3 seeded members (admin/preparer/
  // reviewer); the in-house token's list is exactly its 1 seeded member.
  expect(aUserIds).toEqual([...TENANTS.a.members].sort())
  expect(bUserIds).toEqual([...TENANTS.b.members].sort())

  // Negative: neither list leaks the other tenant's subject — the in-house persona
  // never appears in the firm list, and vice versa, even though both are real,
  // membership-holding subjects in the same memberships table.
  expect(aUserIds).not.toContain(TENANTS.b.subject)
  expect(bUserIds).not.toContain(TENANTS.a.subject)
})
