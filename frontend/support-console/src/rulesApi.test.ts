import { ApiError } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { fetchRules, fetchRulesInForce, fetchVersions, reasonValid, stateLabel, switchRule, toRule, versionMeta, type VersionState } from './rulesApi'

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
