import { ApiError } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { fetchRulesInForce, reasonValid, switchRule, toRule } from './rulesApi'

const GW = 'https://gw.test'
const RULES = `${GW}/api/validation/v1/staff/rules`
const wire = { key: 'k', type: 'cel', target: 'invoice.total', severity: 'warning' as const, scope: 'document', message: 'm', enabled: false }

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
    expect(toRule(wire)).toEqual({ key: 'k', type: 'cel', field: 'invoice.total', severity: 'warn', scope: 'global', enabled: false, message: 'm' })
    expect(toRule({ ...wire, severity: 'error' }).severity).toBe('error')
    expect(toRule({ ...wire, severity: 'info' }).severity).toBe('info')
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
