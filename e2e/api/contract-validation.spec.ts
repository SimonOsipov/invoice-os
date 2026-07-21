// M3-15-04 (Order 4 of 5): the validation contract spec, over the wire —
// through the SAME typed seam (api/client.ts) every api/ spec shares, at the
// RAW level (M3-15-01's rawFetch) so the exact HTTP status + envelope shape
// is directly observable — unlike apiFetch, which normalizes a non-2xx into
// a thrown ApiError. Mirrors contract-portfolio.spec.ts's (M3-15-03) shape,
// scoped to the validation surface (POST /v1/validate, PATCH /v1/rules/{key}).
//
// Two properties are proven against the DEPLOYED gateway:
//   - Happy-path status + shape: a valid invoice -> 200 + a well-formed
//     {rule_set_version: number, violations: array} body. Shape only — no
//     assertion on violation CONTENTS or keys, so this test is
//     order-independent of both M3-14-03's kill-switch spec (validation.spec.ts,
//     which mutates the global rules table mid-suite) and of whatever state
//     the seeded v1 rule set happens to be in when this file runs.
//   - Error-path status + envelope: three malformed-request shapes each
//     reject with the status internal/validation/handlers.go's statusForErr
//     predicts, and every error body is the shared flat {error: <string>}
//     envelope (exactly one key, string value) — same shape
//     auth-contract.spec.ts and contract-portfolio.spec.ts prove for their
//     surfaces.
//
// READ-ONLY (Core AC 3): this file never sends {enabled: false} and never
// otherwise mutates the global, un-tenanted `rules` table (Decision A3 — one
// `rules` row per key, not per-tenant, shared by every api/ spec and every
// other engineer/CI run hitting this dev fleet). The two toggle cases below
// (absent-enabled, unknown-key) are both REJECTED before any write reaches
// the store (handlers.go:109-117 for absent-enabled; the store's ErrNotFound
// path for an unknown key never issues an UPDATE) — so exercising them stays
// consistent with the read-only property. The 409 ErrRedundantTransition
// toggle path (PATCH enabled:true on an already-enabled rule) is
// deliberately NOT re-driven here: it's already covered by M3-14-03's
// kill-switch spec (validation.spec.ts), and re-driving it here would
// require toggling a real rule's enabled bit, breaking this file's
// read-only guarantee for no additional coverage.
import { test, expect } from '@playwright/test'
import { login, rawFetch, PERSONAS } from './client'
import { validInvoice } from './fixtures'
import { assertErrorEnvelope } from './contract-helpers'

test.describe('validation contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test.describe('happy-path status + shape', () => {
    test('validate valid invoice -> 200 + well-formed {rule_set_version, violations} shape', async () => {
      const res = await rawFetch('/api/validation/v1/validate', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: validInvoice,
      })
      expect(res.status, 'validate should return 200').toBe(200)
      // Shape only, deliberately: no assertion on violations' CONTENTS or
      // keys (see file header) — this stays a shape contract, not a re-run
      // of validation.spec.ts's collect-all coverage.
      const body = res.body as Record<string, unknown>
      expect(typeof body.rule_set_version, 'rule_set_version should be numeric').toBe('number')
      expect(Array.isArray(body.violations), 'violations should be an array').toBe(true)
    })
  })

  test.describe('error-path status + envelope', () => {
    test('validate with no request body -> 400 {error: string} (io.EOF decode-error branch)', async () => {
      // Omit `body` entirely — rawFetch only JSON-stringifies a body that is
      // PRESENT (client.ts), so this sends a genuinely empty request body.
      // A raw non-JSON string like "not json" would instead be serialized to
      // the valid JSON string "\"not json\"" and still reject 400, but via a
      // map type-mismatch branch — NOT the decode-error path this test
      // targets. An empty body makes json.NewDecoder(r.Body).Decode return
      // io.EOF, the genuine decode-error branch (handlers.go:66-70). Still
      // needs a valid persona-A token: the handler checks identity -> 401
      // BEFORE it ever reaches Decode (handlers.go:61-70), so an
      // unauthenticated empty-body request would 401, not isolate this
      // branch.
      const res = await rawFetch('/api/validation/v1/validate', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'validate with no body')
    })

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
