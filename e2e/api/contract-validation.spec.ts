// The validation RULES contract spec, over the wire -- through the SAME typed seam
// (api/client.ts) every api/ spec shares, at the RAW level (M3-15-01's rawFetch) so the
// exact HTTP status + envelope shape is directly observable; apiFetch normalizes a non-2xx
// into a thrown ApiError. Mirrors contract-portfolio.spec.ts's shape, scoped to the one
// client-facing route on this surface: PATCH /v1/rules/{key}.
//
// Error-path status + envelope: two malformed-request shapes each reject with the status
// internal/validation/handlers.go's statusForErr predicts, and every error body is the
// shared flat {error: <string>} envelope (exactly one key, string value) -- the same shape
// auth-contract.spec.ts and contract-portfolio.spec.ts prove for their surfaces.
//
// READ-ONLY (Core AC 3): this file never sends {enabled: false} and never otherwise mutates
// the global, un-tenanted `rules` table (Decision A3 -- one `rules` row per key, not
// per-tenant, shared by every api/ spec and every other engineer/CI run hitting this dev
// fleet). Both cases below are REJECTED before any write reaches the store
// (handlers.go:109-117 for absent-enabled; the store's ErrNotFound path for an unknown key
// never issues an UPDATE). The 409 ErrRedundantTransition path is deliberately NOT
// re-driven here: validation.spec.ts's kill-switch spec already covers it, and re-driving
// it would require toggling a real rule's enabled bit for no additional coverage.
import { test } from '@playwright/test'
import { login, rawFetch, PERSONAS } from './client'
import { assertErrorEnvelope } from './contract-helpers'

test.describe('validation contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test.describe('error-path status + envelope', () => {
    test('toggle vat-standard-rate with absent enabled -> 400 {error: string} (rejected before any write)', async () => {
      // {} has no "enabled" key at all — toggleRequest.Enabled decodes to
      // nil and is rejected by the nil check (handlers.go:114-117) BEFORE
      // toggle() is ever called, so this performs no write to the rule.
      // Targets vat-standard-rate (an existing, real rule key) purely to
      // prove the absent-enabled check fires ahead of any lookup/write —
      // this file never sends {enabled: false} and never changes this rule's
      // enabled state (see file header).
      const res = await rawFetch('/api/validation/v1/rules/vat-standard-rate', {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: {},
      })
      assertErrorEnvelope(res, 400, 'toggle absent enabled')
    })

    test('toggle unknown rule key -> 404 {error: string} (ErrNotFound, no write)', async () => {
      // "no-such-rule" doesn't exist in the seeded v1 rule set, so the store
      // returns ErrNotFound without ever reaching an UPDATE — statusForErr
      // maps that to 404 (handlers.go:147-148).
      const res = await rawFetch('/api/validation/v1/rules/no-such-rule', {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { enabled: true },
      })
      assertErrorEnvelope(res, 404, 'toggle unknown key')
    })
  })
})
