import { ApiError } from '@invoice-os/api-client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import {
  colourMap,
  fetchStaffMetrics,
  fleetSeries,
  formatValue,
  judge,
  latestOf,
  type MetricPanel,
  type PanelKey,
  type Point,
  type StaffMetrics,
} from './metricsApi'

const GW = 'https://gw.test'
const ROUTE = `${GW}/api/submission/v1/staff/metrics`

let fetchMock: ReturnType<typeof vi.fn>
beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', GW + '/')
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => (k === SESSION_KEY ? JSON.stringify({ v: 2, token: 'T', refresh_token: 'R' }) : null),
  })
  fetchMock = vi.fn(async () => new Response(JSON.stringify({ range: '1h' }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const panel = (key: PanelKey, series: Record<string, Point[]>, available = true): MetricPanel => ({
  key,
  unit: '',
  available,
  series: Object.entries(series).map(([name, points]) => ({ name, points })),
})
const body = (...panels: MetricPanel[]): StaffMetrics => ({
  range: '1h',
  service: '',
  start: '2026-10-24T00:00:00Z',
  end: '2026-10-24T01:00:00Z',
  step_seconds: 60,
  connection_budget: 80,
  panels,
  limits: { available: true, services: [] },
  deploys: { available: true, revisions: [] },
})

describe('fetchStaffMetrics', () => {
  it('fetchStaffMetrics reads the staff metrics route for a range', async () => {
    const out = await fetchStaffMetrics('1h', '')
    expect(fetchMock.mock.calls[0][0]).toBe(`${ROUTE}?range=1h`)
    expect(fetchMock.mock.calls[0][1].headers.Authorization ?? fetchMock.mock.calls[0][1].headers.get?.('Authorization')).toBe('Bearer T')
    expect(out).toEqual({ range: '1h' })
  })

  it('fetchStaffMetrics adds the service only when one is picked', async () => {
    await fetchStaffMetrics('6h', 'submission')
    await fetchStaffMetrics('6h', '')
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual([`${ROUTE}?range=6h&service=submission`, `${ROUTE}?range=6h`])
  })

  it('fetchStaffMetrics rejects with the HTTP status', async () => {
    for (const status of [401, 403, 500]) {
      fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: 'x' }), { status, headers: { 'Content-Type': 'application/json' } }))
      const err = await fetchStaffMetrics('1h', '').catch((e) => e)
      expect(err).toBeInstanceOf(ApiError)
      expect((err as ApiError).status).toBe(status)
    }
  })
})

describe('fleetSeries', () => {
  it('fleetSeries sums requests across services per step', () => {
    const m = body(panel('req_per_min', { app: [[1, 2], [2, 3]], gateway: [[1, 8], [2, 1]] }))
    expect(fleetSeries(m).requests).toEqual([[1, 10], [2, 4]])
  })

  it('fleetSeries weights 5xx by requests', () => {
    const m = body(panel('req_per_min', { a: [[1, 10]], b: [[1, 90]] }), panel('error_pct', { a: [[1, 10]], b: [[1, 0]] }))
    expect(fleetSeries(m).error).toEqual([[1, 1]])
  })

  it('fleetSeries reads 0 % 5xx at a step with no requests', () => {
    const m = body(panel('req_per_min', { a: [[1, 0]], b: [[1, 0]] }), panel('error_pct', { a: [[1, 0]], b: [[1, 0]] }))
    expect(fleetSeries(m).error).toEqual([[1, 0]])
  })

  it('fleetSeries skips a service that misses a point at a step', () => {
    const m = body(panel('req_per_min', { a: [[1, 10]], b: [] }), panel('error_pct', { a: [[1, 2]], b: [[1, 50]] }))
    expect(fleetSeries(m).error).toEqual([[1, 2]])
  })

  it('fleetSeries takes the worst service for p95', () => {
    const m = body(panel('latency_p95_ms', { a: [[1, 300]], b: [[1, 1200]] }))
    expect(fleetSeries(m).p95).toEqual([[1, 1200]])
  })

  it('fleetSeries reads the database series as connections', () => {
    const m = body(panel('sql_connections', { database: [[1, 23], [2, 25]] }))
    expect(fleetSeries(m).connections).toEqual([[1, 23], [2, 25]])
  })

  it('fleetSeries is empty for an unavailable or empty panel', () => {
    const m = body(panel('req_per_min', { a: [[1, 5]] }, false), panel('error_pct', {}))
    const f = fleetSeries(m)
    expect(f.requests).toEqual([])
    expect(f.error).toEqual([])
    expect(latestOf(f.requests)).toBeNull()
    expect(latestOf(f.error)).toBeNull()
  })
})

describe('latestOf', () => {
  it('latestOf takes the last step', () => {
    expect(latestOf([[1, 1], [3, 5], [2, 9]])).toBe(5)
  })
})

describe('judge', () => {
  it('judge holds at the threshold and turns high above it', () => {
    expect([judge('error', 1, 1, 80), judge('error', 1.01, 1.01, 80)]).toEqual(['ok', 'high'])
    expect([judge('p95', 1000, 1000, 80), judge('p95', 1010, 1010, 80)]).toEqual(['ok', 'high'])
    expect([judge('connections', 64, 64, 80), judge('connections', 65, 65, 80)]).toEqual(['ok', 'high'])
    expect([judge('cpu', 80, 80, 80), judge('cpu', 80.1, 80.1, 80), judge('ram', 80, 80, 80), judge('ram', 80.1, 80.1, 80)]).toEqual(['ok', 'high', 'ok', 'high'])
  })

  it('judge reads peaked when only the range peak is over', () => {
    expect(judge('error', 0.4, 2.5, 80)).toBe('peaked')
    expect(judge('p95', 300, 1200, 80)).toBe('peaked')
  })

  it('the word agrees with the displayed value', () => {
    const word = (stat: 'error' | 'p95', unit: string, raw: number) => {
      const f = formatValue(unit, raw)
      return [`${f.value}${f.unit === 's' ? ' s' : ''}`, judge(stat, f.n, f.n, 80)]
    }
    expect(word('error', '%', 1.004)).toEqual(['1.00', 'ok'])
    expect(word('p95', 'ms', 1004)).toEqual(['1.00 s', 'ok'])
    expect(word('error', '%', 1.006)).toEqual(['1.01', 'high'])
  })
})

describe('colourMap', () => {
  const names = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i']

  it('colourMap colours the seven busiest services in name order', () => {
    // peaks: i, h busiest-last; a and b are the two quietest
    const peaks: Record<string, number> = { a: 1, b: 2, c: 30, d: 40, e: 50, f: 60, g: 70, h: 80, i: 90 }
    const m = body(panel('req_per_min', Object.fromEntries(names.map((n) => [n, [[1, peaks[n]]] as Point[]]))))
    expect(colourMap(m)).toEqual({
      c: 'var(--series-1)',
      d: 'var(--series-2)',
      e: 'var(--series-3)',
      f: 'var(--series-4)',
      g: 'var(--series-5)',
      h: 'var(--series-6)',
      i: 'var(--series-7)',
    })
  })

  it('colourMap falls back to names when requests have no data', () => {
    const m = body(panel('req_per_min', {}, false), panel('cpu_pct', Object.fromEntries(names.slice().reverse().map((n) => [n, [[1, 5]] as Point[]]))))
    expect(Object.keys(colourMap(m))).toEqual(['a', 'b', 'c', 'd', 'e', 'f', 'g'])
    expect(colourMap(m).a).toBe('var(--series-1)')
  })
})

describe('formatValue', () => {
  it('formatValue formats each unit', () => {
    const cases: [string, number, string, string, number][] = [
      ['req/min', 4.24, '4.2', 'req/min', 4.2],
      ['req/min', 318.6, '319', 'req/min', 319],
      ['req/min', 1420, '1.4k', 'req/min', 1400],
      ['%', 0.4249, '0.42', '%', 0.42],
      ['% of limit', 63.54, '63.5', '% of limit', 63.5],
      ['ms', 640.2, '640', 'ms', 640],
      ['ms', 1249, '1.25', 's', 1250],
      ['ms', 999.6, '1.00', 's', 1000],
      ['count', 23, '23', '', 23],
    ]
    for (const [unit, v, value, outUnit, n] of cases) {
      expect(formatValue(unit, v), `${unit} ${v}`).toEqual({ value, unit: outUnit, n })
    }
  })
})
