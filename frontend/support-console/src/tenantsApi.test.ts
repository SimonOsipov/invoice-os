import { ApiError } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { fetchStaffTenant, fetchStaffTenants, filterTenants, kindLabel, memberName, type StaffTenant } from './tenantsApi'

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
    fetchMock.mockResolvedValue(json(200, detail))
    expect(await fetchStaffTenant('a/b')).toEqual(detail)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe(`${ROUTE}/a%2Fb`)
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
  })

  it('kindLabel names both kinds', () => {
    expect(kindLabel('firm')).toBe('FIRM')
    expect(kindLabel('in_house')).toBe('IN-HOUSE')
    expect(kindLabel('other')).toBe('OTHER')
  })
})
