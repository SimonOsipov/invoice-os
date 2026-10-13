import { ApiError } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { detailStatus, fetchStaffTenant, fetchStaffTenants, filterTenants, initials, kindLabel, memberName, type StaffTenant } from './tenantsApi'

const GW = 'https://gw.test'
const ROUTE = `${GW}/api/tenancy/v1/staff/tenants`

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const tenant = (name: string, over: Partial<StaffTenant> = {}): StaffTenant => ({
  id: name, name, kind: 'firm', created_at: '2026-01-01T00:00:00Z', demo: false, active_members: 1, invoice_count: 0, last_activity_at: null, ...over,
})

let fetchMock: ReturnType<typeof vi.fn>
beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', GW + '/')
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => (k === SESSION_KEY ? JSON.stringify({ v: 2, token: 'T', refresh_token: 'R' }) : null),
  })
  fetchMock = vi.fn(async () => json(200, {}))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('tenantsApi', () => {
  it('fetchStaffTenants reads the staff tenants route', async () => {
    const t1 = tenant('Okafor')
    fetchMock.mockResolvedValue(json(200, { tenants: [t1] }))
    expect(await fetchStaffTenants()).toEqual([t1])
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(ROUTE)
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer T')
  })

  it('fetchStaffTenant encodes the id and returns the detail', async () => {
    const detail = { tenant: tenant('a/b'), members: [], invoices: [] }
    fetchMock.mockImplementation(async () => json(200, detail))
    expect(await fetchStaffTenant('a/b')).toEqual(detail)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe(`${ROUTE}/a%2Fb`)
    await fetchStaffTenant('x?y#z')
    expect(fetchMock.mock.calls[1][0]).toBe(`${ROUTE}/x%3Fy%23z`)
    const ac = new AbortController()
    await fetchStaffTenant('t', ac.signal)
    expect(fetchMock.mock.calls[2][1].signal).toBe(ac.signal)
  })

  it('fetchStaffTenants keeps an empty list and a null last_activity_at', async () => {
    fetchMock.mockResolvedValue(json(200, { tenants: [] }))
    expect(await fetchStaffTenants()).toEqual([])
    const quiet = tenant('Quiet', { last_activity_at: null })
    fetchMock.mockResolvedValue(json(200, { tenants: [quiet] }))
    const got = await fetchStaffTenants()
    expect(got).toHaveLength(1)
    expect(got[0].last_activity_at).toBeNull()
  })

  it('fetchStaffTenants throws the server message and sends the abort signal', async () => {
    fetchMock.mockResolvedValue(json(403, { error: 'forbidden' }))
    const ac = new AbortController()
    const err = await fetchStaffTenants(ac.signal).catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.message).toBe('forbidden')
    expect(err.status).toBe(403)
    expect(fetchMock.mock.calls[0][1].signal).toBe(ac.signal)
  })

  it('fetchStaffTenants rejects 401 without a request when signed out', async () => {
    vi.stubGlobal('localStorage', { getItem: () => null })
    const err = await fetchStaffTenants().catch((e) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(401)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('fetchStaffTenant rejects with the HTTP status', async () => {
    for (const status of [404, 403, 401]) {
      fetchMock.mockResolvedValue(json(status, { error: 'x' }))
      const err = await fetchStaffTenant('t').catch((e) => e)
      expect(err).toBeInstanceOf(ApiError)
      expect(err.status).toBe(status)
    }
  })

  it('filterTenants matches the name case-insensitively and keeps order', () => {
    const list = [tenant('Okafor'), tenant('Honeywell'), tenant('ASComply (dev)')]
    expect(filterTenants(list, '  hon ').map((t) => t.name)).toEqual(['Honeywell'])
    expect(filterTenants(list, 'O').map((t) => t.name)).toEqual(['Okafor', 'Honeywell', 'ASComply (dev)'])
    expect(filterTenants(list, 'OKA').map((t) => t.name)).toEqual(['Okafor'])
    expect(filterTenants(list, '')).toBe(list)
    expect(filterTenants(list, '   ')).toBe(list)
    expect(filterTenants(list, 'hOnEy')).toHaveLength(1)
    expect(filterTenants(list, '(DEV)').map((t) => t.name)).toEqual(['ASComply (dev)'])
    expect(filterTenants([], 'x')).toEqual([])
    expect(filterTenants(list, 'zzz')).toEqual([])
  })

  it('filterTenants ignores kind and id', () => {
    const list = [tenant('Okafor', { kind: 'in_house', id: 'abc-123' })]
    expect(filterTenants(list, 'in_house')).toEqual([])
    expect(filterTenants(list, 'abc')).toEqual([])
  })

  it('memberName climbs display name, email, user id', () => {
    const base = { user_id: 'u1', display_name: null, email: null }
    expect(memberName({ ...base, display_name: 'Ada' })).toBe('Ada')
    expect(memberName({ ...base, display_name: '', email: 'a@x' })).toBe('a@x')
    expect(memberName(base)).toBe('u1')
    expect(memberName({ ...base, display_name: '', email: '' })).toBe('u1')
    expect(memberName({ ...base, display_name: null, email: 'e@x' })).toBe('e@x')
    expect(memberName({ ...base, display_name: 'Ada', email: 'a@x' })).toBe('Ada')
  })

  it('kindLabel names both kinds', () => {
    expect(kindLabel('firm')).toBe('FIRM')
    expect(kindLabel('in_house')).toBe('IN-HOUSE')
    expect(kindLabel('other')).toBe('OTHER')
  })

  it('initials takes the first letter of the first two words', () => {
    expect(initials('Okafor & Partners')).toBe('O&')
    expect(initials('  honeywell  ')).toBe('H')
    expect(initials('ASComply (dev) Ltd')).toBe('A(')
    expect(initials('')).toBe('?')
    expect(initials('   ')).toBe('?')
  })

  it('detailStatus shows no previous tenant', () => {
    const detail = { tenant: tenant('X', { id: 'X' }), members: [], invoices: [] }
    const ready = { status: 'ready', data: detail, error: null }
    expect(detailStatus(ready, 'Y')).toBe('loading')
    expect(detailStatus(ready, 'X')).toBe('ready')
    expect(detailStatus(ready, null)).toBe('none')
    expect(detailStatus({ status: 'idle', data: null, error: null }, 'X')).toBe('loading')
    expect(detailStatus({ status: 'loading', data: null, error: null }, 'X')).toBe('loading')
    const failed = (status: number | null) => ({ status: 'error', data: null, error: { status } })
    expect(detailStatus(failed(404), 'X')).toBe('not-found')
    expect(detailStatus(failed(403), 'X')).toBe('forbidden')
    expect(detailStatus(failed(500), 'X')).toBe('error')
    expect(detailStatus(failed(null), 'X')).toBe('error')
  })
})
