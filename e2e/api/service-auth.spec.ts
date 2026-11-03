import { test, expect } from '@playwright/test'
import { login, rawFetch, fleetHeaders, PERSONAS } from './client'
import { serviceUrl, googleIdToken } from './preview'

// One test per service in routedServices (cmd/gateway/main.go); keep the list in step.
// Each body is that service's own GET /v1/ping reply in cmd/<service>/main.go.
const SERVICES = ['tenancy', 'portfolio', 'invoice', 'validation', 'submission', 'dashboard', 'notifications']

// An emptied list would generate zero tests and pass; fail the file load instead.
if (SERVICES.length !== 7) throw new Error(`service-auth: expected 7 routed services, got ${SERVICES.length}`)

test.describe('the deployed gateway reaches each guarded service with its token', () => {
  for (const service of SERVICES) {
    test(service, async () => {
      const token = await login(PERSONAS.A)
      const res = await rawFetch(`/api/${service}/v1/ping`, { headers: { Authorization: `Bearer ${token}` } })
      expect(res.status).toBe(200)
      expect(res.body).toEqual({ service, status: 'ok' })
    })
  }
})

// Guard reply: X-Gateway-Guard 'refused' and {"error":"unauthorized"} (internal/platform/peer.go).
test.describe('a direct call to each guarded service', () => {
  for (const service of SERVICES) {
    const call = async (withToken: boolean) => {
      const audience = serviceUrl(service)
      const res = await fetch(`${audience}/v1/ping`, {
        headers: {
          'X-Serverless-Authorization': `Bearer ${await googleIdToken(audience)}`,
          'X-Tenant-ID': PERSONAS.B.tenantId,
          'X-User-ID': PERSONAS.B.subject,
          'X-User-Role': PERSONAS.B.role,
          ...(withToken ? fleetHeaders() : {}),
        },
      })
      return { res, body: await res.json() }
    }

    test(`direct call: ${service} refuses a call without the gateway token`, async () => {
      const { res, body } = await call(false)
      expect(res.status).toBe(401)
      expect(res.headers.get('x-gateway-guard')).toBe('refused')
      expect(body).toEqual({ error: 'unauthorized' })
    })

    test(`direct call: ${service} admits the gateway token`, async () => {
      const { res, body } = await call(true)
      expect(res.status).toBe(200)
      expect(body).toEqual({ service, status: 'ok' })
    })
  }
})
