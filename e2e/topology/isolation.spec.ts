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
