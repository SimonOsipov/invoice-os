// Shared targets + fixtures for the M2-14 topology E2E (task-23.4). Unlike the smoke
// suite (which only needs the SPA URLs), these tests drive the live gateway too: the
// browser round trip and the cross-tenant isolation check both go through it. Each PR now
// deploys to its own ephemeral Railway environment (M4-21), so these URLs are REQUIRED —
// resolveTarget throws rather than falling back to a hardcoded dev deployment (Decision
// [fail-loud-targets]).

import { ACTIVE_RULE_SET_VERSION } from '../rule-set'
import { resolveTarget } from '../targets'

// The public gateway (mock issuer + /api/*) and the app SPA on this run's environment.
export const GATEWAY_URL = resolveTarget('GATEWAY_URL')
export const APP_URL = resolveTarget('APP_URL')

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

// The seeded, ACTIVE MBS rule-set that the live gateway evaluates (v2 since M4-04-01 --
// migrations/20260716185106_rule_set_v2.sql). The "has-violations" preset
// (invoicePayload.ts PRESETS) fires a subset of these — a robust sample rather than all
// 19 — plus the rule-set version the engine tags every violation row with, which
// topology.spec.ts asserts against a live rendered table cell.
//
// The version comes from the shared ../rule-set module, NOT a literal here: it is the one
// place the e2e package names it ([e2e-active-version]).
export const VALIDATION_EXPECTED = {
  ruleSetVersion: ACTIVE_RULE_SET_VERSION,
  sampleRuleKeys: ['supplier-name-required', 'vat-standard-rate', 'currency-allowed'],
} as const
