// The validation RULES contract over the wire, at the raw level (rawFetch) so status and
// envelope are observable. PATCH /v1/rules/{key} refuses every authenticated caller: 403
// {error: "rules are managed by ASComply"}, whatever the body. The staff routes
// (/v1/staff/rules) admit only a rules-role staff token.
import { randomUUID } from 'node:crypto'
import { test, expect } from '@playwright/test'
import {
  createCustomRule,
  createEntity,
  createInvoice,
  deleteCustomRule,
  getAuditLog,
  listCustomRules,
  login,
  provisionStaffAccount,
  rawFetch,
  signInSession,
  updateCustomRule,
  validateInvoice,
  PERSONAS,
  type CustomRule,
  type CustomRuleInput,
  type Entity,
} from './client'
import { freshTin } from './fixtures'
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

const VERSIONS = '/api/validation/v1/staff/rule-versions'
const DRAFT = `${VERSIONS}/draft`
const SYNCS = '/api/validation/v1/staff/code-list-syncs'
// The published version stays scheduled, so no invoice is judged by it; sealed versions cannot be deleted.
const FAR_FUTURE = '3001-01-01'

// The invoice of internal/validation/sample_invoice.json.
const SAMPLE_INVOICE = {
  invoice_number: 'INV-2026-000123',
  issue_date: '2026-07-11',
  currency: 'NGN',
  supplier: { tin: '12345678-0001', name: 'Acme Nigeria Ltd' },
  buyer: { tin: '87654321-0002', name: 'Buyer Ltd' },
  subtotal: 1000.0,
  vat: 75.0,
  total: 1075.0,
  line_items: [{ id: '1', description: 'Widget', quantity: 10.0, unit_price: 100.0, line_total: 1000.0 }],
}

type DeskVersion = { version: number; state: string; rule_count: number }

test.describe('staff rules desk (API E2E, over the deployed gateway)', () => {
  let customer: string
  let noRole: string
  let rulesRole: string

  test.beforeAll(async () => {
    customer = await login(PERSONAS.A)
    const plain = await provisionStaffAccount('desk-no-role')
    noRole = (await signInSession(plain.email, plain.password)).access_token
    const granted = await provisionStaffAccount('desk-rules', undefined, { rulesRole: true })
    rulesRole = (await signInSession(granted.email, granted.password)).access_token
  })

  const auth = (token: string) => ({ Authorization: `Bearer ${token}` })
  const get = (token: string, path: string) => rawFetch(path, { headers: auth(token) })
  const send = (token: string, method: string, path: string, body?: unknown) => rawFetch(path, { method, headers: auth(token), body })

  async function versions(): Promise<DeskVersion[]> {
    const res = await get(rulesRole, VERSIONS)
    expect(res.status, 'versions list').toBe(200)
    return (res.body as { versions: DeskVersion[] }).versions
  }
  const inForce = async (): Promise<number> => {
    const res = await get(rulesRole, STAFF_RULES)
    expect(res.status, 'rules in force').toBe(200)
    return (res.body as { version: number }).version
  }
  // A topology run or a retry may have left a draft; open one only when none exists.
  async function draftNumber(): Promise<number> {
    const found = (await versions()).find((v) => v.state === 'draft')
    if (found) return found.version
    const opened = await send(rulesRole, 'POST', DRAFT)
    expect(opened.status, 'open a draft').toBe(201)
    return (opened.body as { version: number }).version
  }

  const probeKey = `e2e-desk-${randomUUID().slice(0, 8)}`
  const probeBody = (key: string) => ({
    key,
    type: 'required',
    target: probeKey.replace(/-/g, '_'),
    params: {},
    severity: 'info',
    when: null,
    message: 'e2e desk probe',
    enabled: true,
  })
  const probePut = { ...probeBody(probeKey), key: undefined }

  const refusals: Array<{ name: string; token: () => string }> = [
    { name: 'a customer token', token: () => customer },
    { name: 'a staff token without the rules role', token: () => noRole },
  ]

  for (const who of refusals) {
    test(`staff rules desk routes refuse ${who.name}`, async () => {
      const t = who.token()
      const routes: Array<[string, string, unknown?]> = [
        ['GET', VERSIONS],
        ['GET', `${STAFF_RULES}?version=4`],
        ['GET', SYNCS],
        ['POST', DRAFT],
        ['POST', `${DRAFT}/rules`, probeBody(probeKey)],
        ['PUT', `${DRAFT}/rules/${probeKey}`, probePut],
        ['DELETE', `${DRAFT}/rules/${probeKey}`],
        ['POST', `${DRAFT}/publish`, { effective_from: FAR_FUTURE }],
        ['POST', `${DRAFT}/test`, { invoice: {} }],
      ]
      for (const [method, path, body] of routes) {
        assertErrorEnvelope(await send(t, method, path, body), 403, `${method} ${path} with ${who.name}`)
      }
    })
  }

  test('the versions list has one version in force', async () => {
    const list = await versions()
    const forced = list.filter((v) => v.state === 'in_force')
    expect(forced, 'exactly one version in force').toHaveLength(1)
    expect(forced[0].version, 'the list agrees with GET rules').toBe(await inForce())

    const other = list.find((v) => v.state !== 'in_force' && v.rule_count > 0)
    expect(other, 'another listed version with rules').toBeDefined()
    const res = await get(rulesRole, `${STAFF_RULES}?version=${other!.version}`)
    expect(res.status, 'rules of another version').toBe(200)
    expect(res.body).toMatchObject({ version: other!.version })
    expect((res.body as { rules: StaffRule[] }).rules, 'the list rule_count matches the rules read').toHaveLength(other!.rule_count)
  })

  test('a rules-role staff member drafts and tests a rule', async () => {
    const draft = await draftNumber()
    const rules = `${DRAFT}/rules`

    const added = await send(rulesRole, 'POST', rules, probeBody(probeKey))
    expect(added.status, 'add the probe').toBe(201)
    expect(added.body).toMatchObject({ key: probeKey, rule_set_version: draft })
    assertErrorEnvelope(await send(rulesRole, 'POST', rules, probeBody(probeKey)), 409, 'add the probe twice')

    const read = await get(rulesRole, `${STAFF_RULES}?version=${draft}`)
    expect(read.status, 'rules of the draft').toBe(200)
    expect((read.body as { rules: StaffRule[] }).rules.map((r) => r.key), 'the draft holds the probe').toContain(probeKey)

    assertErrorEnvelope(await send(rulesRole, 'POST', DRAFT), 409, 'open a second draft')

    const sample = SAMPLE_INVOICE
    const test = await send(rulesRole, 'POST', `${DRAFT}/test`, { invoice: sample })
    expect(test.status, 'test the draft').toBe(200)
    const sides = test.body as { draft: { error: string | null; violations: Array<{ rule_key: string }> }; in_force: { violations: Array<{ rule_key: string }> } }
    expect(sides.draft.error, 'the draft evaluates').toBeNull()
    expect(sides.draft.violations.map((v) => v.rule_key), 'the probe fires in the draft').toContain(probeKey)
    expect(sides.in_force.violations.map((v) => v.rule_key), 'the probe is absent from the version in force').not.toContain(probeKey)

    const removed = await send(rulesRole, 'DELETE', `${rules}/${probeKey}`)
    expect(removed.status, 'remove the probe').toBe(200)
    expect(removed.body).toMatchObject({ key: probeKey })
    expect((await send(rulesRole, 'POST', rules, probeBody(probeKey))).status, 're-add the probe').toBe(201)
  })

  test('publishing seals the draft with a future start date', async () => {
    const draft = await draftNumber()
    const forceBefore = await inForce()
    const yesterday = new Date(Date.now() - 86_400_000).toISOString().slice(0, 10)
    const publish = (effective_from: string) => send(rulesRole, 'POST', `${DRAFT}/publish`, { effective_from })

    const refused = await publish(yesterday)
    assertErrorEnvelope(refused, 400, 'publish with yesterday')
    expect(refused.body, 'refused for the date').toEqual({ error: 'start date must be today or later' })
    expect((await versions()).find((v) => v.version === draft)?.state, 'a refused publish leaves the draft').toBe('draft')

    // Publishing seals whatever the draft holds, so no rule list is asserted.
    const published = await publish(FAR_FUTURE)
    expect(published.status, 'publish').toBe(200)
    const sealed = (published.body as { version: number }).version
    expect(published.body).toMatchObject({ version: draft, effective_from: FAR_FUTURE })

    const list = await versions()
    expect(list.find((v) => v.version === sealed)?.state, 'the published version').toBe('scheduled')
    expect(list.filter((v) => v.state === 'in_force'), 'one version in force').toHaveLength(1)
    expect(await inForce(), 'the version in force is unchanged').toBe(forceBefore)

    const put = await send(rulesRole, 'PUT', `${DRAFT}/rules/${probeKey}`, probePut)
    assertErrorEnvelope(put, 404, 'edit with no draft')
    expect(put.body).toEqual({ error: 'no draft' })
  })

  test('the sync log is readable by rules staff', async () => {
    const summary = await get(rulesRole, SYNCS)
    expect(summary.status, 'sync summary').toBe(200)
    const lists = (summary.body as { lists: Array<{ list: string; synced_at: string }> }).lists
    expect(Array.isArray(lists), 'lists is an array').toBe(true)

    // The detail route answers for a list with no sync: 200 and an empty array, whatever the log holds.
    const none = await get(rulesRole, `${SYNCS}?list=e2e-never-synced`)
    expect(none.status, 'detail of an unsynced list').toBe(200)
    expect(none.body).toEqual({ list: 'e2e-never-synced', syncs: [] })

    for (const { list, synced_at } of lists) {
      const detail = await get(rulesRole, `${SYNCS}?list=${encodeURIComponent(list)}`)
      expect(detail.status, `detail of ${list}`).toBe(200)
      const syncs = (detail.body as { syncs: Array<{ synced_at: string; added: unknown; removed: unknown; changed: unknown; added_count: unknown; removed_count: unknown; changed_count: unknown }> }).syncs
      expect(Array.isArray(syncs), `${list} syncs is an array`).toBe(true)
      expect(syncs.length, `${list} has a sync, since the summary lists it`).toBeGreaterThan(0)
      expect(syncs[0].synced_at, `${list}: the newest sync is the summary's`).toBe(synced_at)
      for (const s of syncs) {
        expect([Array.isArray(s.added), Array.isArray(s.removed), Array.isArray(s.changed)], `${list} code arrays`).toEqual([true, true, true])
        expect([typeof s.added_count, typeof s.removed_count, typeof s.changed_count], `${list} code counts`).toEqual(['number', 'number', 'number'])
      }
    }
  })
})

// internal/customrule's ErrNotPermitted answer (fail in handlers.go).
const CUSTOM_RULE_NOT_ADMIN_MESSAGE = 'only a workspace admin can change custom rules'
// Seeded active PREPARER of tenant a (db/seed.dev.sql): a member, not an admin.
const CUSTOM_RULE_PREPARER = 'c0000000-0000-0000-0000-000000000003'
// internal/customrule.Key: "custom:" + rule id.
const CUSTOM_KEY_PREFIX = 'custom:'

// Every rule is scoped to a fresh client entity and only that entity's invoices are validated (never
// queued: a saved error rule stays pending until the reconciliation tick), so no other spec is judged by it.
test.describe('custom rules (API E2E, over the deployed gateway)', () => {
  let admin: string
  let preparer: string
  let entity: Entity
  const created: string[] = []

  const input = (severity: 'error' | 'warning', message: string): CustomRuleInput => ({
    entity_id: entity.id,
    type: 'format/regex', // validation.TypeFormat
    target: 'invoice_number',
    params: { pattern: '^NEVER-MATCHES$' },
    severity,
    message,
    enabled: true,
  })

  async function saveRule(severity: 'error' | 'warning', message: string): Promise<CustomRule> {
    const rule = await createCustomRule(admin, input(severity, message))
    created.push(rule.id)
    return rule
  }

  async function freshInvoice(): Promise<string> {
    const inv = await createInvoice(admin, {
      entity_id: entity.id,
      invoice_number: `INV-CR-${freshTin()}-${randomUUID().slice(0, 8)}`,
      issue_date: '2026-01-01T00:00:00Z',
      supplier_tin: freshTin(),
      supplier_name: 'Acme Nigeria Ltd',
      buyer_tin: '87654321-0002',
      buyer_name: 'Buyer Ltd',
      currency: 'NGN',
      subtotal: '1000',
      vat: '75',
      total: '1075',
      line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
    })
    return inv.id
  }

  test.beforeAll(async () => {
    admin = await login(PERSONAS.A)
    preparer = await login({ ...PERSONAS.A, subject: CUSTOM_RULE_PREPARER })
    const tin = freshTin()
    entity = await createEntity(admin, { name: `ENGI-14 custom rules ${tin}`, tin })
  })

  // Each test starts with no live rule on the entity, so one test's rule never judges another's invoice.
  async function clearRules() {
    for (const id of created.splice(0)) await deleteCustomRule(admin, id).catch(() => undefined)
  }
  test.afterEach(clearRules)
  test.afterAll(clearRules)

  test('a blocking client rule keeps the invoice in draft', async () => {
    const rule = await saveRule('error', 'number must match NEVER-MATCHES')
    expect(rule.key).toBe(`${CUSTOM_KEY_PREFIX}${rule.id}`)
    const result = await validateInvoice(admin, await freshInvoice())
    expect(result.status).toBe('draft')
    expect(result.violations).toEqual(expect.arrayContaining([expect.objectContaining({ rule_key: rule.key, severity: 'error' })]))
  })

  test('the same rule as a warning lets the invoice validate', async () => {
    const rule = await saveRule('error', 'warn me instead')
    const id = await freshInvoice()
    expect((await validateInvoice(admin, id)).status).toBe('draft')

    const updated = await updateCustomRule(admin, rule.id, input('warning', 'warn me instead'))
    expect(updated.revision).toBe(2)
    const result = await validateInvoice(admin, id)
    expect(result.status).toBe('validated')
    expect(result.violations).toEqual(expect.arrayContaining([expect.objectContaining({ rule_key: rule.key, severity: 'warning' })]))
  })

  test('a preparer reads custom rules but cannot write them', async () => {
    const rule = await saveRule('warning', 'preparer can read this')
    const res = await rawFetch('/api/invoice/v1/custom-rules', {
      method: 'POST',
      headers: { Authorization: `Bearer ${preparer}` },
      body: input('warning', 'preparer write'),
    })
    assertErrorEnvelope(res, 403, 'preparer create')
    expect(res.body).toEqual({ error: CUSTOM_RULE_NOT_ADMIN_MESSAGE })

    const listed = await listCustomRules(preparer)
    expect(listed.map((r) => r.id)).toContain(rule.id)
  })

  test('an invalid expression is refused', async () => {
    const before = (await listCustomRules(admin)).length
    const res = await rawFetch('/api/invoice/v1/custom-rules', {
      method: 'POST',
      headers: { Authorization: `Bearer ${admin}` },
      body: { ...input('error', 'bad expression'), type: 'cel', target: 'invoice_number', params: { expr: 'invoice.total >' } },
    })
    assertErrorEnvelope(res, 400, 'invalid cel')
    expect((res.body as { error: string }).error).toContain('params.expr')
    expect((await listCustomRules(admin)).length).toBe(before)
  })

  test('an admin deletes the rule and the invoice validates', async () => {
    const rule = await saveRule('error', 'to be deleted')
    const id = await freshInvoice()
    expect((await validateInvoice(admin, id)).status).toBe('draft')

    expect(await deleteCustomRule(admin, rule.id)).toBe(204)
    expect((await listCustomRules(admin)).map((r) => r.id)).not.toContain(rule.id)
    expect((await validateInvoice(admin, id)).status).toBe('validated')
  })

  test('the save is audited against the client', async () => {
    const rule = await saveRule('warning', 'audited')
    const log = await getAuditLog(admin, { event: ['custom_rule.created'], company: entity.id })
    const row = log.events.find((e) => (e.payload as { rule_id?: string } | null)?.rule_id === rule.id)
    expect(row, 'custom_rule.created row for the new rule').toBeDefined()
    expect(row?.entity_id).toBe(entity.id)
  })
})
