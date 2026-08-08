import { describe, expect, it } from 'vitest'

import { buildMockPanels } from './dashboardMock'

describe('buildMockPanels (task-430, METR-01-06)', () => {
  it('MTR-01: chart.now anchors to the live score and the curve ends there', () => {
    const mock = buildMockPanels('Acme Co', 42)
    expect(mock.chart?.now).toBe(42)
    // vals[11] is pinned to finalScore and x is fixed at cw=680 -- the final
    // path point is deterministic regardless of the seed's rnd() sequence.
    expect(mock.chart?.line.endsWith(' L 680.0 105.0')).toBe(true)
  })

  it('MTR-02: no live score returns chart: null, not a fabricated curve', () => {
    const mock = buildMockPanels('Acme Co', null)
    expect(mock.chart).toBeNull()
  })

  it('MTR-03: the fields this story orphaned are gone from the returned object', () => {
    const mock = buildMockPanels('Acme Co', 42)
    expect('score' in mock).toBe(false)
    expect('ring' in mock).toBe(false)
    expect('readinessNote' in mock).toBe(false)
    expect('readinessMetrics' in mock).toBe(false)
    expect('vatLabel' in mock).toBe(false)
  })

  it('MTR-04: same seed + same liveScorePct is byte-identical across calls', () => {
    const a = buildMockPanels('Acme Co', 55)
    const b = buildMockPanels('Acme Co', 55)
    expect(JSON.stringify(a)).toBe(JSON.stringify(b))
  })

  it('MTR-05: deltaLabel flips to a down arrow when the live score sits below the fabricated start', () => {
    const mock = buildMockPanels('Acme Co', 10)
    expect(mock.chart?.deltaLabel.startsWith('▼')).toBe(true)
  })

  it('MTR-06: liveScorePct 0 still renders a chart -- 0 is a score, not an absence', () => {
    const mock = buildMockPanels('Acme Co', 0)
    expect(mock.chart).not.toBeNull()
    expect(mock.chart?.now).toBe(0)
  })
})
