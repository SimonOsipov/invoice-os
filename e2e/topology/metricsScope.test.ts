import { describe, expect, test } from 'vitest'
import { scopeStrays, type ScopedMetrics } from './metricsScope'

const FLEET = ['gateway', 'app', 'worker']
const m = (over: Partial<ScopedMetrics> & { req?: string[] } = {}): ScopedMetrics => ({
  panels: [
    { key: 'req_per_min', series: (over.req ?? ['gateway']).map((name) => ({ name })) },
    { key: 'sql_cpu_pct', series: [{ name: 'database' }] },
  ],
  limits: { services: [{ name: 'gateway' }] },
  deploys: { revisions: [{ service: 'gateway' }] },
  ...over,
})

describe('scopeStrays', () => {
  test('a fleet-only answer has no strays, and the Cloud SQL "database" series is ignored', () => {
    expect(scopeStrays(m(), FLEET)).toEqual([])
  })
  test('another PR series and an untrimmed own series are strays', () => {
    expect(scopeStrays(m({ req: ['gateway', 'gateway-pr-546', 'gateway-pr-545'] }), FLEET)).toEqual(['gateway-pr-546', 'gateway-pr-545'])
  })
  test('a stray limits name and a stray deploy service are strays', () => {
    const bad = m({ limits: { services: [{ name: 'gateway-pr-546' }] }, deploys: { revisions: [{ service: 'mockissuer' }] } })
    expect(scopeStrays(bad, FLEET)).toEqual(['gateway-pr-546', 'mockissuer'])
  })
  test('a narrowed answer allows only the picked name', () => {
    expect(scopeStrays(m({ req: ['gateway', 'app'] }), ['gateway'])).toEqual(['app'])
  })
})
