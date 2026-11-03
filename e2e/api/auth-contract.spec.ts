// M3-15-02 (Core AC 2 + AC 3): the auth-header contract, over the wire —
// through the SAME api/ seam (api/client.ts) this suite shares, but at the
// RAW level (M3-15-01's rawFetch) rather than the typed apiFetch wrapper:
// this file needs byte-level control over the Authorization header itself
// (absent / wrong-scheme / well-formed-but-unverifiable), which apiFetch's
// `token` option can't express directly. Two properties are proven against
// the DEPLOYED gateway:
//   AC1     — three distinct auth-failure shapes (absent header, malformed
//             scheme, invalid Bearer) each reject with HTTP 401 and the
//             shared flat envelope { error: <string> } (exactly one key,
//             string value).
//   AC2/AC3 — the SAME invalid-Bearer request, fired at each of the three
//             surface prefixes (tenancy/portfolio/validation), rejects with
//             an IDENTICAL 401 + envelope — proving 401 is enforced ONCE at
//             the gateway edge, pre-routing (internal/gateway/gateway.go:44-59;
//             internal/platform/auth/middleware.go:14-47), rather than being
//             independently re-implemented (and potentially drifting) per
//             backend service.
// Per the story's verified contract facts, the reject body is a flat
// {"error":<string>} everywhere (no RFC-7807, no nested {error:{code,...}}).
// This file asserts that SHAPE, not the literal string — matching this
// suite's existing convention of asserting ApiError.kind/status rather than
// pinning response body text (isolation.spec.ts).
// Cross-surface identity (AC2/AC3) is instead proven by directly comparing
// the three response bodies to each other, so "identical" is demonstrated,
// not assumed from a hard-coded string.
// Read-only: no login, no token minting, no writes, no rules-table mutation.
import { createPrivateKey, generateKeyPairSync, randomUUID, sign, type KeyObject } from 'node:crypto'
import { test, expect } from '@playwright/test'
import { claimsOf, headerOf, login, me, PERSONAS, rawFetch } from './client'
import { assertUnauthorizedEnvelope } from './contract-helpers'
import { previewSecret } from './preview'

test.describe('auth-header contract (API E2E, over the deployed gateway)', () => {
  test.describe('AC1: three distinct auth-failure variants each reject 401 with the shared envelope', () => {
    test('absent Authorization header -> 401 { error: string }', async () => {
      // No headers at all — rawFetch applies init.headers verbatim, so
      // omitting it entirely sends no Authorization header.
      const res = await rawFetch('/api/tenancy/v1/me')
      assertUnauthorizedEnvelope(res, 'absent Authorization header')
    })

    test('malformed scheme (Basic, not Bearer) -> 401 { error: string }', async () => {
      // Present but wrong scheme: BearerToken() only recognizes "Bearer ",
      // so any non-Bearer scheme still fails the scheme check. The value
      // is intentionally not a real credential (not base64).
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: 'Basic not-a-credential' },
      })
      assertUnauthorizedEnvelope(res, 'malformed scheme (Basic)')
    })

    test('invalid Bearer (well-formed scheme, unverifiable token) -> 401 { error: string }', async () => {
      // Correct scheme, garbage token: passes the scheme/non-empty check in
      // BearerToken() but fails signature Verify() downstream.
      const res = await rawFetch('/api/tenancy/v1/me', {
        headers: { Authorization: 'Bearer not-a-jwt' },
      })
      assertUnauthorizedEnvelope(res, 'invalid Bearer')
    })
  })

  test('AC2/AC3: invalid Bearer yields an identical 401 + envelope across all three surface prefixes (pre-routing enforcement)', async () => {
    const invalidBearer = { Authorization: 'Bearer not-a-jwt' }

    const tenancy = await rawFetch('/api/tenancy/v1/me', { headers: invalidBearer })
    const portfolio = await rawFetch('/api/portfolio/v1/entities', { headers: invalidBearer })
    // The validation surface's probe is a PATCH at the rules route: a 401 is raised
    // pre-routing (internal/gateway/gateway.go), so the request never reaches a handler and
    // nothing is toggled.
    const validation = await rawFetch('/api/validation/v1/rules/vat-standard-rate', { method: 'PATCH', headers: invalidBearer })

    assertUnauthorizedEnvelope(tenancy, 'tenancy surface (/api/tenancy/v1/me)')
    assertUnauthorizedEnvelope(portfolio, 'portfolio surface (/api/portfolio/v1/entities)')
    assertUnauthorizedEnvelope(validation, 'validation surface (/api/validation/v1/rules/{key})')

    // Not just three independently-shaped envelopes — the SAME body, proving
    // a single pre-routing reject() path produced all three rather than each
    // service independently formatting its own 401.
    expect(portfolio.body, 'portfolio body should equal tenancy body').toEqual(tenancy.body)
    expect(validation.body, 'validation body should equal tenancy body').toEqual(tenancy.body)
  })
})

function signEs256(header: object, claims: object, key: KeyObject): string {
  const b64 = (o: object): string => Buffer.from(JSON.stringify(o)).toString('base64url')
  const data = `${b64(header)}.${b64(claims)}`
  return `${data}.${sign('sha256', Buffer.from(data), { key, dsaEncoding: 'ieee-p1363' }).toString('base64url')}`
}

test.describe('forged tokens (F-204)', () => {
  let real: string
  let header: Record<string, unknown>
  let claims: Record<string, unknown>

  test.beforeAll(async () => {
    real = await login(PERSONAS.A)
    header = headerOf(real)
    claims = claimsOf(real)
  })

  const getMe = (token: string) => rawFetch('/api/tenancy/v1/me', { headers: { Authorization: `Bearer ${token}` } })
  // Parsed lazily: a failed secret read must not fail the tests that do not need it.
  const previewKey = (): KeyObject => createPrivateKey({ key: JSON.parse(previewSecret('mock-issuer-key'))[0], format: 'jwk' })
  const now = (): number => Math.floor(Date.now() / 1000)

  test("forged tokens: the mock login's own token is accepted", async () => {
    expect((await me(real)).tenant.id).toBe(PERSONAS.A.tenantId)
  })

  test('forged tokens: a fresh key under a random kid is refused', async () => {
    const fresh = generateKeyPairSync('ec', { namedCurve: 'P-256' }).privateKey
    assertUnauthorizedEnvelope(await getMe(signEs256({ ...header, kid: randomUUID() }, claims, fresh)), 'fresh key, random kid')
  })

  test('forged tokens: a fresh key under the published kid is refused', async () => {
    const fresh = generateKeyPairSync('ec', { namedCurve: 'P-256' }).privateKey
    assertUnauthorizedEnvelope(await getMe(signEs256(header, claims, fresh)), 'fresh key, published kid')
  })

  test('forged tokens: the preview key signs an accepted unexpired token', async () => {
    const token = signEs256(header, { ...claims, iat: now(), exp: now() + 300 }, previewKey())
    expect((await me(token)).tenant.id).toBe(PERSONAS.A.tenantId)
  })

  test('forged tokens: an expired token under the published key is refused', async () => {
    const token = signEs256(header, { ...claims, iat: now() - 7200, exp: now() - 3600 }, previewKey())
    assertUnauthorizedEnvelope(await getMe(token), 'preview key, expired')
  })
})
