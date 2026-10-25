import { ApiError } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { fetchStaffHealth, fleetSummary, jobSeries, jobsOf, series, shortBuild, type FleetHealth, type JobView, type StaffHealth, type TrendPoint } from './healthApi'

const GW = 'https://gw.test'
const ROUTE = `${GW}/api/submission/v1/staff/health`

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const point = (services_down: number | null, queue_depth: number, dead_lettered = 0, river_jobs: JobView | null = null): TrendPoint => ({ taken_at: '2026-10-14T10:00:00Z', services_down, queue_depth, dead_lettered, river_jobs })

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

describe('healthApi', () => {
  it('fetchStaffHealth reads the staff health route', async () => {
    const body: StaffHealth = { checked_at: '2026-10-14T10:05:00Z', fleet: { status: 'ok', services: [] }, jobs: { queued: 1 }, queue_depth: 1, river_jobs: { total: { queue_depth: 1, dead_lettered: 0 }, services: {} }, trend: [point(0, 1)] }
    fetchMock.mockResolvedValue(json(200, body))
    expect(await fetchStaffHealth()).toEqual(body)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe(ROUTE)
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer T')
  })

  it('fetchStaffHealth rejects with the HTTP status', async () => {
    for (const status of [401, 403, 500]) {
      fetchMock.mockResolvedValue(json(status, { error: 'x' }))
      const err = await fetchStaffHealth().catch((e: unknown) => e)
      expect(err).toBeInstanceOf(ApiError)
      expect((err as ApiError).status).toBe(status)
    }
  })

  it('fleetSummary counts up and names the down services', () => {
    const fleet: FleetHealth = {
      status: 'degraded',
      services: [
        { name: 'gateway', status: 'up' },
        { name: 'docling', status: 'down' },
        { name: 'auth', status: 'down' },
      ],
    }
    expect(fleetSummary(fleet)).toEqual({ up: 1, total: 3, down: ['docling', 'auth'] })
    expect(fleetSummary({ status: 'unknown', services: [] })).toEqual({ up: 0, total: 0, down: [] })
  })

  const jv = (q: number, d: number, services: JobView['services'] = {}): JobView => ({ total: { queue_depth: q, dead_lettered: d }, services })

  it('jobsOf reads the total for All services', () => {
    expect(jobsOf(jv(5, 1), '')).toEqual({ queue_depth: 5, dead_lettered: 1 })
  })

  it("jobsOf reads a service's totals and null for a service without kinds", () => {
    const v = jv(9, 3, { submission: { queue_depth: 2, dead_lettered: 0 } })
    expect(jobsOf(v, 'submission')).toEqual({ queue_depth: 2, dead_lettered: 0 })
    expect(jobsOf(v, 'app')).toBeNull()
  })

  it('jobSeries skips samples without kind counts', () => {
    const t = [point(0, 7, 7, null), point(0, 1, 0, jv(1, 0)), point(0, 1, 0, jv(3, 0))]
    expect(jobSeries(t, '', 'queue_depth')).toEqual([1, 3])
  })

  it('jobSeries reads one service per sample', () => {
    const at = (d: number) => point(0, 0, 0, jv(9, 9, { notifications: { queue_depth: 0, dead_lettered: d } }))
    const t = [at(0), at(2)]
    expect(jobSeries(t, 'notifications', 'dead_lettered')).toEqual([0, 2])
    expect(jobSeries(t, 'app', 'dead_lettered')).toEqual([])
  })

  it('series keeps order and drops nulls', () => {
    const trend = [point(1, 5), point(null, 7), point(0, 9)]
    expect(series(trend, 'services_down')).toEqual([1, 0])
    expect(series(trend, 'queue_depth')).toEqual([5, 7, 9])
  })

  it('shortBuild cuts to seven characters', () => {
    expect(shortBuild('541ac755c7a1b2c3')).toBe('541ac75')
    expect(shortBuild('')).toBe('—')
    expect(shortBuild(undefined)).toBe('—')
  })

  it('fleetSummary edge fleets: empty, all down, unknown with services', () => {
    expect(fleetSummary({ status: 'ok', services: [] })).toEqual({ up: 0, total: 0, down: [] })
    const allDown: FleetHealth = { status: 'degraded', services: [{ name: 'a', status: 'down' }, { name: 'b', status: 'down' }] }
    expect(fleetSummary(allDown)).toEqual({ up: 0, total: 2, down: ['a', 'b'] })
    expect(fleetSummary({ status: 'unknown', services: [{ name: 'a', status: 'up' }] })).toEqual({ up: 0, total: 0, down: [] })
  })

  it('series edge inputs: empty, all null, single, zero kept', () => {
    expect(series([], 'queue_depth')).toEqual([])
    expect(series([point(null, 1), point(null, 2)], 'services_down')).toEqual([])
    expect(series([point(0, 4, 3)], 'dead_lettered')).toEqual([3])
    expect(series([point(0, 0)], 'services_down')).toEqual([0])
  })

  it('shortBuild leaves a build shorter than seven characters whole', () => {
    expect(shortBuild('abc')).toBe('abc')
    expect(shortBuild('1234567')).toBe('1234567')
    expect(shortBuild('12345678')).toBe('1234567')
  })

  it('fetchStaffHealth forwards the abort signal and rejects when signed out', async () => {
    const ac = new AbortController()
    await fetchStaffHealth(ac.signal)
    expect(fetchMock.mock.calls[0][1].signal).toBeInstanceOf(AbortSignal)
    vi.stubGlobal('localStorage', { getItem: () => null })
    fetchMock.mockClear()
    const err = await fetchStaffHealth().catch((e: unknown) => e)
    expect((err as ApiError).status).toBe(401)
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
