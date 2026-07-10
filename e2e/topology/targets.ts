// Shared targets + fixtures for the M2-14 topology E2E (task-23.4). Unlike the smoke
// suite (which only needs the SPA URLs), these tests drive the live gateway too: the
// browser round trip and the cross-tenant isolation check both go through it. URLs
// default to the live dev deployments and are overridable via env, matching the smoke
// pattern so the same suite runs against a PR preview or any other deploy.

const resolve = (envVar: string, fallback: string): string => process.env[envVar]?.trim() || fallback

// The public gateway (mock issuer + /api/*) and the app SPA on the dev environment.
export const GATEWAY_URL = resolve('GATEWAY_URL', 'https://gateway-development-997b.up.railway.app')
export const APP_URL = resolve('APP_URL', 'https://app-development-3b4b.up.railway.app')

// The seeded isolation pair (db/seed.dev.sql), M3-02: the two real persona tenants, each
// with an admin membership for its persona subject. Both rows exist in the tenants
// table, so RLS — not a WHERE clause — is what limits each token to its own row. Using
// the real persona tenants (rather than the throwaway aaaa…/bbbb… fixtures) is required
// now that /me is membership-gated (a non-member subject would 403, not 200) and doubles
// as the live proof that the firm and in-house personas resolve their own tenant + role.
export const TENANTS = {
  a: {
    id: '11111111-1111-1111-1111-111111111111',
    name: 'Okafor & Partners',
    kind: 'firm',
    subject: 'c0000000-0000-0000-0000-000000000001',
    role: 'admin',
    // All three seeded members of this tenant (db/seed.dev.sql) — the live
    // membership-list proof (isolation.spec.ts) asserts GET /v1/memberships
    // returns exactly these user_ids and none of tenant b's.
    members: [
      'c0000000-0000-0000-0000-000000000001',
      'c0000000-0000-0000-0000-000000000003',
      'c0000000-0000-0000-0000-000000000004',
    ],
  },
  b: {
    id: '22222222-2222-2222-2222-222222222222',
    name: 'Honeywell Group',
    kind: 'in_house',
    subject: 'c0000000-0000-0000-0000-000000000002',
    role: 'admin',
    members: ['c0000000-0000-0000-0000-000000000002'],
  },
} as const

// The firm persona (frontend/app/src/auth.ts) resolves to seeded tenant 1111. Its
// uppercased backend name is what the verified sidebar renders after the round trip.
export const FIRM_PERSONA = {
  buttonName: 'Chinedu Okafor',
  tenantName: 'Okafor & Partners',
} as const
