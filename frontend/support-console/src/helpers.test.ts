// Specs for the pure presentation helpers: token NAMES only, never literals.
import { describe, expect, it } from 'vitest'

import { DASHBOARDS } from './metricsApi'
import { alignSeries, CHART_H, healthTone, readoutPlacement, readoutRows, sentryServiceUrl, sentryTenantUrl, SPARK_HEIGHT, SPARK_WIDTH, severityStyle, sparkline, tokenName, utcClock, yTop } from './helpers'
import type { Severity } from './types'

const ALL_SEVERITIES: Severity[] = ['error', 'warn', 'info']

describe('severityStyle', () => {
  it('HLP-5: covers every severity and emits only token references', () => {
    for (const s of ALL_SEVERITIES) {
      const st = severityStyle(s)
      expect(st.label).toBe(s.toUpperCase())
      for (const v of [st.bg, st.border, st.text]) {
        expect(v, `severity ${s}`).toMatch(/^var\(--[a-z0-9-]+\)$/)
      }
    }
  })
})

describe('sparkline', () => {
  it('draws nothing under two points', () => {
    expect(sparkline([])).toEqual({ line: '', area: '' })
    expect(sparkline([5])).toEqual({ line: '', area: '' })
    const { line, area } = sparkline([1, 2, 3])
    expect(line).toMatch(/^M[\d.]+ [\d.]+ L[\d.]+ [\d.]+ L[\d.]+ [\d.]+$/)
    const nums = [...line.matchAll(/[ML]([\d.]+) ([\d.]+)/g)].map((m) => [Number(m[1]), Number(m[2])])
    expect(nums).toHaveLength(3)
    for (const [x, y] of nums) {
      expect(x).toBeGreaterThanOrEqual(0)
      expect(x).toBeLessThanOrEqual(SPARK_WIDTH)
      expect(y).toBeGreaterThanOrEqual(0)
      expect(y).toBeLessThanOrEqual(SPARK_HEIGHT)
    }
    expect(area).toBe(`${line} L${SPARK_WIDTH} ${SPARK_HEIGHT} L0 ${SPARK_HEIGHT} Z`)
  })
})

describe('sparkline edges', () => {
  const coords = (line: string) => [...line.matchAll(/[ML]([\d.]+) ([\d.]+)/g)].map((m) => [Number(m[1]), Number(m[2])])

  it('draws a flat series without NaN', () => {
    const { line } = sparkline([3, 3, 3])
    expect(line).not.toMatch(/NaN|Infinity/)
    const ys = coords(line).map(([, y]) => y)
    expect(ys).toHaveLength(3)
    expect(new Set(ys).size).toBe(1)
    expect(sparkline([0, 0]).line).not.toMatch(/NaN/)
  })

  it('spans the full width with two points and puts the larger value higher', () => {
    const c = coords(sparkline([1, 9]).line)
    expect(c).toHaveLength(2)
    expect(c[0][0]).toBe(0)
    expect(c[1][0]).toBe(220)
    expect(sparkline([1, 9]).area.endsWith('L220 44 L0 44 Z')).toBe(true)
    expect(c[1][1]).toBeLessThan(c[0][1])
  })
})

describe('healthTone', () => {
  it('maps three tones', () => {
    expect(healthTone('green')).toEqual({ dot: 'var(--status-green-text)', stroke: 'var(--action)', fill: 'var(--action-tint)' })
    expect(healthTone('amber')).toEqual({ dot: 'var(--status-amber-text)', stroke: 'var(--status-amber-text)', fill: 'var(--status-amber-bg)' })
    expect(healthTone('red')).toEqual({ dot: 'var(--status-red-text)', stroke: 'var(--status-red-text)', fill: 'var(--status-red-bg)' })
  })

  it('healthTone gives the muted tone from status tokens', () => {
    expect(healthTone('muted')).toEqual({ dot: 'var(--status-muted-text)', stroke: 'var(--status-muted-text)', fill: 'var(--status-muted-bg)' })
  })
})

describe('sentryTenantUrl', () => {
  it('sentryTenantUrl builds the issues query for one tenant', () => {
    expect(sentryTenantUrl('3f2c1a9e-0b7d-4c55-9a1e-6d2f8b7a4c10')).toBe(
      'https://as-comply.sentry.io/issues/?query=tenant_id:3f2c1a9e-0b7d-4c55-9a1e-6d2f8b7a4c10&project=4512190351474768&statsPeriod=14d',
    )
  })

  it('sentryTenantUrl encodes an id that holds URL syntax', () => {
    const url = sentryTenantUrl('x&project=1')
    expect(url).toContain('query=tenant_id:x%26project%3D1&')
    expect(url.match(/project=4512190351474768/g)).toHaveLength(1)
    expect(url.match(/project=/g)).toHaveLength(1)
  })
})

describe('sentryServiceUrl', () => {
  it('sentryServiceUrl builds the issues query for one service', () => {
    expect(sentryServiceUrl('gateway')).toBe('https://as-comply.sentry.io/issues/?query=server_name:gateway&project=4512190351474768&statsPeriod=14d')
  })

  it('sentryServiceUrl encodes a name that holds URL syntax', () => {
    const url = sentryServiceUrl('x&project=1')
    expect(url).toContain('query=server_name:x%26project%3D1&')
    expect(url.match(/project=4512190351474768/g)).toHaveLength(1)
    expect(url.match(/project=/g)).toHaveLength(1)
  })
})

describe('DASHBOARDS', () => {
  it('DASHBOARDS are the two Terraform output URLs', () => {
    expect(DASHBOARDS.services).toBe('https://console.cloud.google.com/monitoring/dashboards/builder/27155b37-f324-478d-9e9d-c91ede1837f9?project=ascomply-prod')
    expect(DASHBOARDS.database_edge).toBe('https://console.cloud.google.com/monitoring/dashboards/builder/3c070f78-6025-432a-b178-5cde3fe08514?project=ascomply-prod')
  })
})

describe('chart geometry', () => {
  it('CHART_H is the plot height the layout reads', () => {
    expect(CHART_H).toBe(160)
  })
  it('the SVG chart helpers are gone', async () => {
    const exported = Object.keys(await import('./helpers'))
    expect(exported).toContain('CHART_H')
    for (const gone of ['CHART_W', 'niceCeil', 'chartX', 'chartY', 'chartPath']) expect(exported, gone).not.toContain(gone)
  })
})

describe('chart model', () => {
  const a: [number, number][] = [[0, 1], [60, 2], [120, 3], [180, 4]]
  const b: [number, number][] = [[60, 5]]

  it('alignSeries puts every line on one time grid', () => {
    expect(alignSeries([{ points: a }, { points: b }], 0, 180, 60)).toEqual([[0, 60, 120, 180], [1, 2, 3, 4], [null, 5, null, null]])
  })
  it('alignSeries leaves a null at a step every line misses', () => {
    const [x, v] = alignSeries([{ points: [[0, 7], [60, 8], [180, 9]] }], 0, 180, 60)
    expect(x).toContain(120)
    expect(v).toEqual([7, 8, null, 9])
  })
  it('alignSeries keeps a point that is off the grid or after the end', () => {
    const [x, v] = alignSeries([{ points: [[90, 1], [240, 2]] }], 0, 180, 60)
    expect(x).toEqual([0, 60, 90, 120, 180, 240])
    expect(v).toEqual([null, null, 1, null, null, 2])
  })
  it('alignSeries with no lines returns the grid alone', () => {
    expect(alignSeries([], 0, 180, 60)).toEqual([[0, 60, 120, 180]])
  })
  it('yTop covers the highest point and every guide', () => {
    expect(yTop([{ points: [[0, 5]] }], [{ value: 80 }])).toBe(80)
    expect(yTop([{ points: [[0, 1200]] }], [{ value: 1000 }])).toBe(1200)
    expect(yTop([{ points: [[0, 0]] }], [])).toBe(0)
  })
  it('utcClock prints clock time for short ranges and the date for long ones', () => {
    const tz = process.env.TZ
    process.env.TZ = 'Africa/Lagos'
    try {
      const t = Date.UTC(2026, 9, 14, 23, 5) / 1000
      expect(utcClock(t, false)).toBe('23:05 UTC')
      expect(utcClock(t, true)).toBe('10-14 23:05')
    } finally {
      if (tz === undefined) delete process.env.TZ
      else process.env.TZ = tz
    }
  })

  const lines = [
    { name: 'a', color: 'c1', unit: '%' },
    { name: 'g', color: 'c2', unit: '%', folded: true },
    { name: 'b', color: 'c3', unit: 'ms' },
  ]
  const grid: Parameters<typeof readoutRows>[1] = [[0, 60], [0.4249, 1], [3, 1], [1249, null]]
  it('readoutRows lists coloured lines, then folded lines, with legend values', () => {
    const rows = readoutRows(lines, grid, 0, null)
    expect(rows.map((r) => r.name)).toEqual(['a', 'b', 'g'])
    expect(rows.map((r) => r.value)).toEqual(['0.42 %', '1.25 s', '3.00 %'])
  })
  it('readoutRows prints a dash for a missing point', () => {
    expect(readoutRows(lines, grid, 1, null).find((r) => r.name === 'b')?.value).toBe('—')
  })
  it('readoutRows lists only the isolated line', () => {
    expect(readoutRows(lines, grid, 0, 'b').map((r) => r.name)).toEqual(['b'])
  })
  it('readoutRows prints dashes outside the grid', () => {
    for (const idx of [-1, 2]) expect(readoutRows(lines, grid, idx, null).every((r) => r.value === '—')).toBe(true)
  })

  const plot = { left: 100, top: 40, width: 400 }
  const box = { width: 160, height: 90 }
  const view = { width: 1280, height: 800 }
  it('readoutPlacement sits right of the cursor in the left half', () => {
    expect(readoutPlacement({ x: 150, y: 70 }, plot, box, view)).toEqual({ left: 162, top: 40 })
  })
  it('readoutPlacement flips left past the middle', () => {
    expect(readoutPlacement({ x: 450, y: 70 }, plot, box, view).left).toBe(450 - 12 - 160)
  })
  it('readoutPlacement stays inside the view', () => {
    const r = readoutPlacement({ x: 20, y: 70 }, { left: 0, top: 760, width: 30 }, box, view)
    expect(r.top).toBe(800 - 90 - 8)
    expect(r.left).toBeGreaterThanOrEqual(8)
  })

  it('alignSeries sorts unsorted points and keeps the last of a duplicate time', () => {
    const [x, v] = alignSeries([{ points: [[120, 3], [0, 1], [60, 2], [60, 9]] }], 0, 120, 60)
    expect(x).toEqual([0, 60, 120])
    expect(v).toEqual([1, 9, 3])
  })
  it('alignSeries stops the grid at the last whole step when the step does not divide the range', () => {
    expect(alignSeries([], 0, 100, 60)).toEqual([[0, 60]])
  })
  it('alignSeries keeps a real zero value apart from a missing point', () => {
    expect(alignSeries([{ points: [[0, 0]] }], 0, 60, 60)[1]).toEqual([0, null])
  })
  it('readoutPlacement clamps the right edge inside the view', () => {
    const r = readoutPlacement({ x: 120, y: 70 }, { left: 100, top: 40, width: 400 }, box, { width: 200, height: 800 })
    expect(r.left).toBe(200 - 160 - 8)
  })
  it('readoutPlacement does not flip at the exact middle of the plot', () => {
    expect(readoutPlacement({ x: 300, y: 70 }, plot, box, view).left).toBe(312)
  })
  it('readoutPlacement keeps the top margin when the plot top is above it', () => {
    expect(readoutPlacement({ x: 150, y: 0 }, { ...plot, top: 2 }, box, view).top).toBe(8)
  })
  it('tokenName rejects an empty or non-var value', () => {
    expect(tokenName('')).toBeNull()
    expect(tokenName('var()')).toBeNull()
    expect(tokenName('rgb(1,2,3)')).toBeNull()
  })

  it('tokenName reads the custom property of a var()', () => {
    expect(tokenName('var(--series-1)')).toBe('--series-1')
    expect(tokenName('var( --fg-4 )')).toBe('--fg-4')
    expect(tokenName('#2a78d6')).toBeNull()
    expect(tokenName('var(--a) var(--b)')).toBeNull()
  })
})
