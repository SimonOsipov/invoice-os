import { test, expect } from '@playwright/test'
import { login, rawFetch, PERSONAS } from './client'

// One test per service in routedServices (cmd/gateway/main.go); keep the list in step.
// Each body is that service's own GET /v1/ping reply in cmd/<service>/main.go.
const SERVICES = ['tenancy', 'portfolio', 'invoice', 'validation', 'submission', 'dashboard', 'notifications']

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
