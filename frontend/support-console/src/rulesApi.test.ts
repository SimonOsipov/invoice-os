import { ApiError } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { readFileSync } from 'node:fs'
import {
  addDraftRule,
  capCodes,
  diffRules,
  draftRuleErrors,
  editDraftRule,
  EMPTY_FORM,
  fetchCodeListSummary,
  fetchCodeListSyncs,
  fetchRules,
  fetchRulesInForce,
  fetchVersions,
  formFromRule,
  openDraft,
  publishDraft,
  publishWarnings,
  RULE_TYPES,
  reasonValid,
  removeDraftRule,
  ruleBody,
  SAMPLE_INVOICE,
  stateLabel,
  switchRule,
  testDraft,
  toRule,
  versionMeta,
  type RuleForm,
  type RuleVersion,
  type VersionState,
} from './rulesApi'
import type { Rule } from './types'

const GW = 'https://gw.test'
const RULES = `${GW}/api/validation/v1/staff/rules`
const wire = { key: 'k', type: 'cel', target: 'invoice.total', params: { min: 0 }, severity: 'warning' as const, when: null, scope: 'document', message: 'm', enabled: false }

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const store = (token: string | null) =>
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => (k === SESSION_KEY && token ? JSON.stringify({ v: 2, token, refresh_token: 'R' }) : null),
  })

let fetchMock: ReturnType<typeof vi.fn>
beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', GW + '/')
  store('T')
  fetchMock = vi.fn(async () => json(200, { version: 4, rules: [wire] }))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('rulesApi', () => {
  it('toRule maps a wire rule onto the console Rule', () => {
    expect(toRule(wire)).toEqual({ key: 'k', type: 'cel', field: 'invoice.total', severity: 'warn', scope: 'global', enabled: false, message: 'm', params: { min: 0 }, when: null })
    expect(toRule({ ...wire, severity: 'error' }).severity).toBe('error')
    expect(toRule({ ...wire, severity: 'info' }).severity).toBe('info')
  })

  it('toRule carries params and when', () => {
    const r = toRule({ ...wire, params: { a: [1, 2], b: 'x' }, when: 'has(invoice.x)' })
    expect(r.params).toEqual({ a: [1, 2], b: 'x' })
    expect(r.when).toBe('has(invoice.x)')
    expect(toRule(wire).when).toBeNull()
  })

  it('stateLabel and versionMeta', () => {
    const labels: Record<VersionState, string> = { draft: 'DRAFT', in_force: 'IN FORCE', scheduled: 'SCHEDULED', superseded: 'SUPERSEDED', retired: 'RETIRED' }
    for (const [state, label] of Object.entries(labels)) expect(stateLabel(state as VersionState)).toBe(label)
    expect(versionMeta({ effective_from: '2026-08-06', rule_count: 20 })).toBe('eff. 2026-08-06 · 20 rules')
    expect(versionMeta({ state: 'draft', effective_from: null, rule_count: 21 })).toBe('editing · 21 rules')
    expect(versionMeta({ state: 'retired', effective_from: null, rule_count: 3 })).toBe('never in force · 3 rules')
  })

  it('fetchVersions and fetchRules build the URLs', async () => {
    fetchMock.mockResolvedValueOnce(json(200, { today: '2026-10-11', versions: [] }))
    expect(await fetchVersions()).toEqual({ today: '2026-10-11', versions: [] })
    const [vUrl, vInit] = fetchMock.mock.calls[0]
    expect(vUrl).toBe(`${GW}/api/validation/v1/staff/rule-versions`)
    expect(vInit.method).toBe('GET')
    expect(new Headers(vInit.headers).get('Authorization')).toBe('Bearer T')

    await fetchRules(3)
    expect(fetchMock.mock.calls[1][0]).toBe(`${RULES}?version=3`)
    expect(new Headers(fetchMock.mock.calls[1][1].headers).get('Authorization')).toBe('Bearer T')
    await fetchRules()
    expect(fetchMock.mock.calls[2][0]).toBe(RULES)
  })

  it('fetchRulesInForce builds the gateway URL and sends the stored token', async () => {
    const got = await fetchRulesInForce()
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(RULES)
    expect(init.method).toBe('GET')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer T')
    expect(got).toEqual({ version: 4, rules: [toRule(wire)] })
  })

  it('switchRule sends PATCH with enabled and reason and encodes the key', async () => {
    fetchMock.mockResolvedValueOnce(json(200, { key: 'a/b c', enabled: false }))
    await switchRule('a/b c', false, 'r')
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(`${RULES}/a%2Fb%20c`)
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body)).toEqual({ enabled: false, reason: 'r' })
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer T')
  })

  it('reasonValid', () => {
    expect(reasonValid('')).toBe(false)
    expect(reasonValid('   ')).toBe(false)
    expect(reasonValid('x')).toBe(true)
    expect(reasonValid('x'.repeat(500))).toBe(true)
    expect(reasonValid('x'.repeat(501))).toBe(false)
    expect(reasonValid('😀'.repeat(500))).toBe(true)
    expect(reasonValid('😀'.repeat(501))).toBe(false)
  })

  it('switchRule and fetchRulesInForce map failures to ApiError', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    await expect(fetchRulesInForce()).rejects.toBeInstanceOf(ApiError)
    await expect(switchRule('k', true, 'r')).rejects.toBeInstanceOf(ApiError)
    vi.stubEnv('VITE_GATEWAY_URL', GW)

    store(null)
    await expect(fetchRulesInForce()).rejects.toBeInstanceOf(ApiError)
    await expect(switchRule('k', true, 'r')).rejects.toBeInstanceOf(ApiError)
    expect(fetchMock).not.toHaveBeenCalled()
    store('T')

    fetchMock.mockResolvedValueOnce(json(403, { error: 'forbidden' }))
    await expect(fetchRulesInForce()).rejects.toMatchObject({ status: 403 })
    fetchMock.mockResolvedValueOnce(json(409, { error: 'rule is already disabled' }))
    await expect(switchRule('k', false, 'r')).rejects.toMatchObject({ status: 409, message: 'rule is already disabled' })
  })
})

const VERSIONS = `${GW}/api/validation/v1/staff/rule-versions`
const body = (i: number) => JSON.parse(fetchMock.mock.calls[i][1].body)

describe('draft requests', () => {
  it('addDraftRule, editDraftRule and removeDraftRule build requests', async () => {
    fetchMock.mockImplementation(async () => json(200, {}))
    const rule = { type: 'cel', target: '', params: {}, severity: 'error' as const, when: null, message: 'm', enabled: true }
    await addDraftRule({ key: 'a-b', ...rule })
    await editDraftRule('a-b', rule)
    await removeDraftRule('a-b')
    const [add, edit, rm] = fetchMock.mock.calls
    expect([add[0], add[1].method]).toEqual([`${VERSIONS}/draft/rules`, 'POST'])
    expect(body(0)).toEqual({ key: 'a-b', ...rule })
    expect([edit[0], edit[1].method]).toEqual([`${VERSIONS}/draft/rules/a-b`, 'PUT'])
    expect(body(1)).toEqual(rule)
    expect(body(1)).not.toHaveProperty('key')
    expect([rm[0], rm[1].method]).toEqual([`${VERSIONS}/draft/rules/a-b`, 'DELETE'])
    expect(rm[1].body).toBeUndefined()
  })

  it('encodes the key of an edit and a removal', async () => {
    fetchMock.mockImplementation(async () => json(200, {}))
    await removeDraftRule('a/b c')
    expect(fetchMock.mock.calls[0][0]).toBe(`${VERSIONS}/draft/rules/a%2Fb%20c`)
  })

  it('openDraft, testDraft and publishDraft build requests', async () => {
    fetchMock.mockImplementation(async () => json(200, {}))
    await openDraft()
    await testDraft({ total: 1 })
    await publishDraft('2026-12-01')
    expect(fetchMock.mock.calls.map((c) => [c[0], c[1].method])).toEqual([
      [`${VERSIONS}/draft`, 'POST'],
      [`${VERSIONS}/draft/test`, 'POST'],
      [`${VERSIONS}/draft/publish`, 'POST'],
    ])
    expect(body(1)).toEqual({ invoice: { total: 1 } })
    expect(body(2)).toEqual({ effective_from: '2026-12-01' })
    expect(new Headers(fetchMock.mock.calls[0][1].headers).get('Authorization')).toBe('Bearer T')
  })

  it('a server refusal reaches the caller with its text', async () => {
    fetchMock.mockResolvedValueOnce(json(409, { error: 'rule already in the draft' }))
    await expect(addDraftRule({ key: 'a', type: 'cel', target: '', params: {}, severity: 'error', when: null, message: 'm', enabled: true })).rejects.toMatchObject({ status: 409, message: 'rule already in the draft' })
  })
})

const form = (over: Partial<RuleForm> = {}): RuleForm => ({ ...EMPTY_FORM, key: 'ok-key', type: 'cel', message: 'msg', ...over })
const v4Rule: Rule = { key: 'no-duplicate-line-items', type: 'format/regex', field: 'supplier.tin', severity: 'warn', scope: 'global', enabled: true, message: 'TIN shape', params: { pattern: '^\\d+$' }, when: 'has(invoice.x)' }

describe('draftRuleErrors', () => {
  it.each([
    ['a bad key', { key: 'Bad' }, 'lowercase'],
    ['a 65-character key', { key: 'a'.repeat(65) }, 'at most 64'],
    ['a blank key', { key: '' }, 'Key is required'],
    ['a missing type', { type: '' }, 'Type is required'],
    ['params that are an array', { params: '[1]' }, 'JSON object'],
    ['params that are not JSON', { params: 'not json' }, 'not valid JSON'],
    ['params null', { params: 'null' }, 'JSON object'],
    ['a blank message', { message: '   ' }, 'Message is required'],
    ['a 501-character message', { message: '😀'.repeat(501) }, 'at most 500'],
  ] as [string, Partial<RuleForm>, string][])('%s gives one error', (_, over, text) => {
    const errs = draftRuleErrors(form(over), [])
    expect(errs).toHaveLength(1)
    expect(errs[0]).toContain(text)
  })

  it('a new key already in the draft gives the draft line', () => {
    expect(draftRuleErrors(form({ key: 'dup' }), ['dup'])).toEqual(['Key already in the draft.'])
    expect(draftRuleErrors(form({ key: 'dup' }), [])).toEqual([])
  })

  it('a v4-shaped rule, a 64-character key and a 500-point message pass', () => {
    expect(draftRuleErrors(formFromRule(v4Rule), [])).toEqual([])
    expect(draftRuleErrors(form({ key: 'a'.repeat(64), message: '😀'.repeat(500) }), [])).toEqual([])
  })
})

describe('form and body', () => {
  it('formFromRule maps warn to warning and ruleBody maps back', () => {
    const f = formFromRule(v4Rule)
    expect(f.severity).toBe('warning')
    expect(ruleBody(f)).toEqual({ type: 'format/regex', target: 'supplier.tin', params: { pattern: '^\\d+$' }, severity: 'warning', when: 'has(invoice.x)', message: 'TIN shape', enabled: true })
  })

  it('a blank when is null, blank params are {} and the message is trimmed', () => {
    expect(ruleBody(form({ when: '  ', params: ' ', message: ' hi ' }))).toMatchObject({ when: null, params: {}, message: 'hi' })
  })
})

const r = (key: string, over: Partial<Rule> = {}): Rule => ({ key, type: 'cel', field: 'invoice.total', severity: 'error', scope: 'global', enabled: true, message: 'm', params: { expr: 'true' }, when: null, ...over })

describe('diffRules', () => {
  it('lists added, changed and removed by key and leaves the same rule out', () => {
    const draft = [r('y', { params: { expr: 'false' } }), r('x'), r('w')]
    const inForce = [r('z'), r('y'), r('w')]
    expect(diffRules(draft, inForce)).toEqual([
      { sign: '+', key: 'x', detail: 'cel · error · invoice.total', tag: 'ADDED', kind: 'added' },
      { sign: '~', key: 'y', detail: 'params changed', tag: 'CHANGED', kind: 'changed' },
      { sign: '−', key: 'z', detail: 'cel · error · invoice.total', tag: 'REMOVED', kind: 'removed' },
    ])
    expect(diffRules(inForce, inForce)).toEqual([])
    expect(diffRules([], [])).toEqual([])
  })

  it.each([
    ['type', { type: 'enum' }],
    ['target', { field: 'invoice.vat' }],
    ['severity', { severity: 'info' as const }],
    ['when', { when: 'true' }],
    ['message', { message: 'other' }],
    ['enabled', { enabled: false }],
  ] as [string, Partial<Rule>][])('a %s change is a changed row', (name, over) => {
    expect(diffRules([r('k', over)], [r('k')])).toEqual([expect.objectContaining({ key: 'k', kind: 'changed', detail: `${name} changed` })])
  })

  it('sorts by key whatever the input order', () => {
    expect(diffRules([r('b'), r('a')], [r('c')]).map((d) => d.key)).toEqual(['a', 'b', 'c'])
  })
})

const ver = (version: number, state: VersionState, effective_from: string | null, opened_at: string | null = null): RuleVersion => ({ rule_set_version_id: `id${version}`, version, state, effective_from, opened_at, rule_count: 1 })

describe('publishWarnings', () => {
  const draft = ver(5, 'draft', null, '2026-10-01T09:30:00Z')
  it('names an in-force version that took effect after the draft was opened', () => {
    expect(publishWarnings([draft, ver(4, 'in_force', '2026-10-05')])).toEqual(['v4 took effect after this draft was opened. The diff shows what publishing changes.'])
  })
  it('stays quiet for an earlier or equal effective date', () => {
    expect(publishWarnings([draft, ver(4, 'in_force', '2026-09-01')])).toEqual([])
    expect(publishWarnings([draft, ver(4, 'in_force', '2026-10-01')])).toEqual([])
  })
  it('names each scheduled version the draft does not carry', () => {
    expect(publishWarnings([draft, ver(6, 'scheduled', '2027-01-01'), ver(4, 'in_force', '2026-09-01')])).toEqual([
      'v6 is scheduled from 2027-01-01. This draft does not carry its rules. A start date on or after 2027-01-01 replaces v6.',
    ])
  })
  it('says nothing without a draft', () => {
    expect(publishWarnings([ver(6, 'scheduled', '2027-01-01'), ver(4, 'in_force', '2026-10-05')])).toEqual([])
  })
})

describe('SAMPLE_INVOICE', () => {
  it('is the invoice of internal/validation/sample_invoice.json', () => {
    const file = JSON.parse(readFileSync(new URL('../../../internal/validation/sample_invoice.json', import.meta.url), 'utf8'))
    expect(JSON.parse(SAMPLE_INVOICE)).toEqual(file.invoice)
  })
})

describe('RULE_TYPES', () => {
  it('lists the RuleType constants of internal/validation/rule.go, no more and no fewer', () => {
    const src = readFileSync(new URL('../../../internal/validation/rule.go', import.meta.url), 'utf8')
    const code = src
      .split('\n')
      .map((l) => l.replace(/\/\/.*$/, ''))
      .join('\n')
    const types = [...code.matchAll(/^\s*Type\w+\s+RuleType\s*=\s*"([^"]+)"/gm)].map((m) => m[1])
    expect(types.length, 'constants found').toBeGreaterThan(5)
    expect([...RULE_TYPES].sort()).toEqual([...types].sort())
  })

  it('fetchCodeListSummary and fetchCodeListSyncs build the URLs', async () => {
    const url = `${GW}/api/validation/v1/staff/code-list-syncs`
    const lists = [{ list: 'hs-codes', synced_at: '2026-10-09T08:15:00Z', entry_count: 9, added_count: 3, removed_count: 1, changed_count: 0, status: 'held' }]
    fetchMock.mockResolvedValueOnce(json(200, { lists }))
    expect(await fetchCodeListSummary()).toEqual(lists)
    expect(fetchMock.mock.calls[0][0]).toBe(url)
    expect(new Headers(fetchMock.mock.calls[0][1].headers).get('Authorization')).toBe('Bearer T')
    const syncs = [{ status: 'applied', synced_at: '2026-10-09T08:15:00Z', entry_count: 9, added: ['A'], removed: [], changed: [] }]
    fetchMock.mockResolvedValueOnce(json(200, { list: 'hs-codes', syncs }))
    expect(await fetchCodeListSyncs('hs-codes')).toEqual(syncs)
    expect(fetchMock.mock.calls[1][0]).toBe(`${url}?list=hs-codes`)
  })

  it('fetchCodeListSyncs encodes the list name in the query', async () => {
    fetchMock.mockResolvedValueOnce(json(200, { list: 'a b&c', syncs: [] }))
    expect(await fetchCodeListSyncs('a b&c')).toEqual([])
    expect(fetchMock.mock.calls[0][0]).toBe(`${GW}/api/validation/v1/staff/code-list-syncs?list=a%20b%26c`)
  })

  it('capCodes', () => {
    const n = (k: number) => Array.from({ length: k }, (_, i) => `c${i}`)
    expect(capCodes(n(199), 200)).toEqual({ shown: n(199), more: 0 })
    expect(capCodes(n(200), 200)).toEqual({ shown: n(200), more: 0 })
    expect(capCodes(n(201), 200)).toEqual({ shown: n(200), more: 1 })
    expect(capCodes([], 200)).toEqual({ shown: [], more: 0 })
  })
})
