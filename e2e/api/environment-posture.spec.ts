import { test, expect } from '@playwright/test'
import { rawFetch, me, headerOf, mockIssuerHeaders, PERSONAS } from './client'
import { nightlyTarget } from '../targets'

// The mock routes the nightly gate covers (cmd/gateway/main.go).
const MOCK_ROUTES = ['/auth/login', '/auth/mock/member', '/auth/mock/invitation-token', '/auth/mock/deletion-link-token', '/auth/mock/readiness-confirm-token']

function noToken(body: unknown): boolean {
  return !(body as { access_token?: unknown } | undefined)?.access_token
}

test.describe('the preview environment posture (F-242)', () => {
  test('the preview gateway reports the mock issuer on', async () => {
    const res = await rawFetch('/health')
    expect(res.status).toBe(200)
    // mock_issuer "on": platform.MockIssuer in cmd/gateway/main.go
    expect((res.body as { mock_issuer?: string }).mock_issuer).toBe('on')
  })

  test('the mock login mints a token the gateway accepts', async () => {
    const res = await rawFetch('/auth/login', {
      method: 'POST',
      headers: mockIssuerHeaders(),
      body: { subject: PERSONAS.A.subject, role: 'authenticated', tenant_id: PERSONAS.A.tenantId },
    })
    expect(res.status).toBe(200)
    const { token_type, access_token } = res.body as { token_type: string; access_token: string }
    // token_type "bearer": internal/gateway/gateway.go
    expect(token_type).toBe('bearer')
    expect(access_token.split('.')).toHaveLength(3)
    const header = headerOf(access_token)
    expect(header.alg).toBe('ES256')

    const jwks = await rawFetch('/.well-known/jwks.json')
    expect(jwks.status).toBe(200)
    const kids = (jwks.body as { keys: { kid: string }[] }).keys.map((k) => k.kid)
    expect(kids).toContain(header.kid)

    expect((await me(access_token)).tenant.id).toBe(PERSONAS.A.tenantId)
  })

  test.describe('the nightly gates the mock routes', () => {
    test.skip(!nightlyTarget(), 'only the nightly gates the mock routes; SECAUD-47 owns PR previews')

    for (const route of MOCK_ROUTES) {
      // 401 {"error":"unauthorized"} is platform.RequireToken's refusal.
      test(`${route} refuses a request without the mock issuer token`, async () => {
        const res = await rawFetch(route, { method: 'POST', body: {} })
        expect(res.status).toBe(401)
        expect(res.body).toEqual({ error: 'unauthorized' })
        expect(noToken(res.body)).toBe(true)
      })

      test(`${route} refuses a wrong mock issuer token`, async () => {
        const [name, value] = Object.entries(mockIssuerHeaders())[0]
        const res = await rawFetch(route, { method: 'POST', headers: { [name]: `${value}x` }, body: {} })
        expect(res.status).toBe(401)
        expect(noToken(res.body)).toBe(true)
      })
    }

    test('the mock login refuses a preflight without an Origin', async () => {
      const res = await rawFetch('/auth/login', { method: 'OPTIONS' })
      expect(res.status).toBe(401)
      expect(noToken(res.body)).toBe(true)
    })
  })
})
