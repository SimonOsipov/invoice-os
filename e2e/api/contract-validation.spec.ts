// The validation RULES contract over the wire, at the raw level (rawFetch) so status and
// envelope are observable. PATCH /v1/rules/{key} refuses every authenticated caller: 403
// {error: "rules are managed by ASComply"}, whatever the body.
import { test, expect } from '@playwright/test'
import { login, rawFetch, PERSONAS } from './client'
import { assertErrorEnvelope } from './contract-helpers'

// rulesManagedMessage in internal/validation/handlers.go.
const RULES_MANAGED_MESSAGE = 'rules are managed by ASComply'

test.describe('validation contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  const cases: Array<{ name: string; key: string; body: unknown }> = [
    { name: 'absent enabled', key: 'vat-standard-rate', body: {} },
    { name: 'unknown key', key: 'no-such-rule', body: { enabled: true } },
    { name: 'valid body', key: 'vat-standard-rate', body: { enabled: true } },
  ]

  for (const c of cases) {
    test(`toggle with ${c.name} -> 403 managed-rules envelope`, async () => {
      const res = await rawFetch(`/api/validation/v1/rules/${c.key}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: c.body,
      })
      assertErrorEnvelope(res, 403, `toggle ${c.name}`)
      expect(res.body).toEqual({ error: RULES_MANAGED_MESSAGE })
    })
  }
})
