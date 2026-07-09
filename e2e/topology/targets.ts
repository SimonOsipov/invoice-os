// Shared targets + fixtures for the M2-14 topology E2E (task-23.4). Unlike the smoke
// suite (which only needs the SPA URLs), these tests drive the live gateway too: the
// browser round trip and the cross-tenant isolation check both go through it. URLs
// default to the live dev deployments and are overridable via env, matching the smoke
// pattern so the same suite runs against a PR preview or any other deploy.

const resolve = (envVar: string, fallback: string): string => process.env[envVar]?.trim() || fallback

// The public gateway (mock issuer + /api/*) and the app SPA on the dev environment.
export const GATEWAY_URL = resolve('GATEWAY_URL', 'https://gateway-development-997b.up.railway.app')
export const APP_URL = resolve('APP_URL', 'https://app-development-3b4b.up.railway.app')

// The seeded isolation pair (db/seed.dev.sql). Both rows exist in the tenants table, so
// RLS — not a WHERE clause — is what limits each token to its own row. Subjects are
// arbitrary but fixed uuids (the mock issuer stamps them as the JWT `sub`).
export const TENANTS = {
  a: {
    id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
    name: 'Tenant A (dev)',
    subject: 'a0000000-0000-0000-0000-0000000000a1',
  },
  b: {
    id: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
    name: 'Tenant B (dev)',
    subject: 'b0000000-0000-0000-0000-0000000000b1',
  },
} as const

// The firm persona (frontend/app/src/auth.ts) resolves to seeded tenant 1111. Its
// uppercased backend name is what the verified sidebar renders after the round trip.
export const FIRM_PERSONA = {
  buttonName: 'Chinedu Okafor',
  tenantName: 'Okafor & Partners',
} as const
