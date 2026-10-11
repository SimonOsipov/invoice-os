// The validation RULES contract over the wire, at the raw level (rawFetch) so status and
// envelope are observable. PATCH /v1/rules/{key} refuses every authenticated caller: 403
// {error: "rules are managed by ASComply"}, whatever the body. The staff routes
// (/v1/staff/rules) admit only a rules-role staff token.
import { test, expect } from '@playwright/test'
import { login, provisionStaffAccount, rawFetch, signInSession, PERSONAS } from './client'
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

// Constants of internal/validation/staff_rules.go: StaffSwitchRuleHandler and staffRulesError.
const NOT_FOUND_MESSAGE = 'no such rule in the version in force'
const ALREADY_DISABLED_MESSAGE = 'rule is already disabled'
const ALREADY_ENABLED_MESSAGE = 'rule is already enabled'

// The probe is a global rule no other spec names; every flip restores in a finally.
const PROBE_RULE = 'no-duplicate-line-items'
const STAFF_RULES = '/api/validation/v1/staff/rules'

type StaffRule = { key: string; enabled: boolean }

test.describe('staff rules (API E2E, over the deployed gateway)', () => {
  let customer: string
  let noRole: string
  let rulesRole: string

  test.beforeAll(async () => {
    customer = await login(PERSONAS.A)
    const plain = await provisionStaffAccount('kill-no-role')
    noRole = (await signInSession(plain.email, plain.password)).access_token
    const granted = await provisionStaffAccount('kill-rules', undefined, { rulesRole: true })
    rulesRole = (await signInSession(granted.email, granted.password)).access_token
  })

  const auth = (token: string) => ({ Authorization: `Bearer ${token}` })
  const listRules = async (token: string) => rawFetch(STAFF_RULES, { headers: auth(token) })
  const patchRule = (token: string, key: string, body: unknown) => rawFetch(`${STAFF_RULES}/${key}`, { method: 'PATCH', headers: auth(token), body })

  // Reads the list as the rules-role staff and returns the probe's state; fails clearly when the key is absent.
  async function probeEnabled(): Promise<boolean> {
    const res = await listRules(rulesRole)
    expect(res.status, 'staff rules list').toBe(200)
    const rules = (res.body as { rules: StaffRule[] }).rules
    const probe = rules.find((r) => r.key === PROBE_RULE)
    if (!probe) throw new Error(`probe rule ${PROBE_RULE} is not in the version in force; keys: ${rules.map((r) => r.key).join(', ')}`)
    return probe.enabled
  }

  const refusals: Array<{ name: string; token: () => string }> = [
    { name: 'a customer token', token: () => customer },
    { name: 'a staff token without the rules role', token: () => noRole },
  ]

  for (const who of refusals) {
    test(`staff rules routes refuse ${who.name}`, async () => {
      assertErrorEnvelope(await listRules(who.token()), 403, `list with ${who.name}`)
      assertErrorEnvelope(await patchRule(who.token(), PROBE_RULE, { enabled: false, reason: 'e2e refusal probe' }), 403, `switch with ${who.name}`)
      expect(await probeEnabled(), 'the refused switch left the probe enabled').toBe(true)
    })
  }

  test('a rules-role staff member switches a rule off and on', async () => {
    expect(await probeEnabled(), 'probe starts enabled').toBe(true)
    try {
      const off = await patchRule(rulesRole, PROBE_RULE, { enabled: false, reason: 'e2e kill switch' })
      expect(off.status, 'disable').toBe(200)
      expect(off.body).toMatchObject({ key: PROBE_RULE, enabled: false })
      expect(await probeEnabled(), 'list after disable').toBe(false)

      const again = await patchRule(rulesRole, PROBE_RULE, { enabled: false, reason: 'e2e kill switch' })
      assertErrorEnvelope(again, 409, 'repeat disable')
      expect(again.body).toEqual({ error: ALREADY_DISABLED_MESSAGE })

      const on = await patchRule(rulesRole, PROBE_RULE, { enabled: true, reason: 'e2e restore' })
      expect(on.status, 'enable').toBe(200)
      expect(on.body).toMatchObject({ key: PROBE_RULE, enabled: true })
      expect(await probeEnabled(), 'list after enable').toBe(true)
      const repeatOn = await patchRule(rulesRole, PROBE_RULE, { enabled: true, reason: 'e2e repeat' })
      assertErrorEnvelope(repeatOn, 409, 'repeat enable')
      expect(repeatOn.body).toEqual({ error: ALREADY_ENABLED_MESSAGE })
    } finally {
      if (!(await probeEnabled())) await patchRule(rulesRole, PROBE_RULE, { enabled: true, reason: 'e2e restore' })
    }
  })

  test('staff switch rejects a missing reason and an unknown key', async () => {
    const before = await probeEnabled()
    try {
      assertErrorEnvelope(await patchRule(rulesRole, PROBE_RULE, { enabled: false }), 400, 'missing reason')
      assertErrorEnvelope(await patchRule(rulesRole, PROBE_RULE, { enabled: false, reason: '   ' }), 400, 'blank reason')
      const unknown = await patchRule(rulesRole, 'no-such-rule', { enabled: false, reason: 'e2e unknown key' })
      assertErrorEnvelope(unknown, 404, 'unknown key')
      expect(unknown.body).toEqual({ error: NOT_FOUND_MESSAGE })
      expect(await probeEnabled(), 'rejected requests changed nothing').toBe(before)
    } finally {
      if (!(await probeEnabled())) await patchRule(rulesRole, PROBE_RULE, { enabled: true, reason: 'e2e restore' })
    }
  })

  test('the legacy toggle route refuses a rules-role staff token', async () => {
    const res = await rawFetch(`/api/validation/v1/rules/${PROBE_RULE}`, { method: 'PATCH', headers: auth(rulesRole), body: { enabled: false } })
    assertErrorEnvelope(res, 403, 'legacy toggle with a rules-role staff token')
    expect(await probeEnabled(), 'probe still enabled').toBe(true)
  })
})
