import { test, expect } from '@playwright/test'
import { rawFetch, me, headerOf, PERSONAS } from './client'

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
})
