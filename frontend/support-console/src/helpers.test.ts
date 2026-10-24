// Specs for the pure presentation helpers: token NAMES only, never literals.
import { describe, expect, it } from 'vitest'

import { DASHBOARDS } from './metricsApi'
import { CHART_H, CHART_W, chartPath, chartX, healthTone, niceCeil, sentryServiceUrl, sentryTenantUrl, SPARK_HEIGHT, SPARK_WIDTH, severityStyle, sparkline } from './helpers'
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
  const o = { start: 1000, end: 1300, step: 60, yMax: 10 }
  it('chartPath spans the box from start to end', () => {
    expect(chartPath([[1000, 0], [1300, 10]], { ...o, step: 300 })).toBe('M0.0 160.0 L600.0 0.0')
  })
  it('chartPath breaks the line at a missing step', () => {
    const d = chartPath([[1000, 1], [1060, 1], [1240, 1], [1300, 1]], o)
    expect(d.match(/M/g)).toHaveLength(2)
  })
  it('chartPath keeps one line across a step of exactly 1.5 steps', () => {
    expect(chartPath([[1000, 1], [1090, 1]], o).match(/M/g)).toHaveLength(1)
  })
  it('chartPath draws nothing for no points and a lone M for one point', () => {
    expect(chartPath([], o)).toBe('')
    expect(chartPath([[1150, 10]], o)).toBe('M300.0 0.0')
  })
  it('chartPath breaks the line just over 1.5 steps', () => {
    expect(chartPath([[1000, 1], [1091, 1]], o).match(/M/g)).toHaveLength(2)
  })
  it('chartX clamps a deploy outside the range to the edge', () => {
    expect(chartX(o.end + 120, o.start, o.end)).toBe(CHART_W)
    expect(chartX(o.start - 1, o.start, o.end)).toBe(0)
    expect(CHART_H).toBe(160)
  })
  it('niceCeil rounds up to 1, 2 or 5 times a power of ten', () => {
    expect([0, -3, 0.7, 1, 1.2, 3, 7, 80, 1000.1].map(niceCeil)).toEqual([1, 1, 1, 1, 2, 5, 10, 100, 2000])
    expect([1.8, 4.5, 5, 0.03].map(niceCeil)).toEqual([2, 5, 5, 0.05])
  })
})
