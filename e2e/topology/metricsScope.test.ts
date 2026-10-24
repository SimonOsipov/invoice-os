import { describe, expect, it } from 'vitest'
import { scopeStrays, type ScopedMetrics } from './metricsScope'

const panel = (key: string, ...names: string[]) => ({ key, series: names.map((name) => ({ name })) })

const body = (over: Record<string, string[]> = {}, limits: string[] = ['app'], deploys: string[] = ['app']): ScopedMetrics => ({
  panels: [
    panel('req_per_min', ...(over.req_per_min ?? ['app'])),
    panel('error_pct', ...(over.error_pct ?? ['app'])),
    panel('sql_cpu_pct', 'database'),
    panel('latency_p95_fleet_ms', 'fleet'),
  ],
  limits: { services: limits.map((name) => ({ name })) },
  deploys: { revisions: deploys.map((service) => ({ service })) },
})

describe('scopeStrays', () => {
  it('ignores the fleet series', () => {
    expect(scopeStrays(body(), ['app'])).toEqual([])
  })

  it('still reports a stray under another panel', () => {
    expect(scopeStrays(body({ req_per_min: ['fleet'], error_pct: ['other'] }, ['ghost']), ['app'])).toEqual(['fleet', 'other', 'ghost'])
  })
})
