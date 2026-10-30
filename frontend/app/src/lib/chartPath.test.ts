import { describe, expect, it } from 'vitest'

import { sparkPath, trendPath } from './chartPath'

describe('sparkPath', () => {
  it('draws [0, 10] corner to corner in the 88x30 box with 3px padding', () => {
    expect(sparkPath([0, 10])).toBe('M3.0 27.0 L85.0 3.0')
  })

  it('sparkPath_drawsANegativeSeriesInsideItsBox', () => {
    const ys = sparkPath([-100, 0, 100])
      .split(' ')
      .map((t) => t.replace(/^[ML]/, ''))
      .filter((_, i) => i % 2 === 1)
      .map(Number)
    expect(ys).toHaveLength(3)
    for (const y of ys) {
      expect(y).toBeGreaterThanOrEqual(3)
      expect(y).toBeLessThanOrEqual(27)
    }
    expect(ys[0]).toBe(27)
    expect(ys[2]).toBe(3)
  })

  it('draws a flat series as a flat line, not NaN', () => {
    const d = sparkPath([5, 5, 5])
    expect(d).not.toContain('NaN')
    expect(d).toBe('M3.0 27.0 L44.0 27.0 L85.0 27.0')
  })
})

describe('trendPath', () => {
  const series = (last: number) => [...Array.from({ length: 11 }, () => 50), last]

  it('ends on the last value at x=680, and the area closes along the baseline', () => {
    const { line, area } = trendPath(series(42))
    expect(line.endsWith(' L 680.0 105.0')).toBe(true)
    expect(area.endsWith(' L 680 176 L 0 176 Z')).toBe(true)
  })

  it('clamps the series to 0-100', () => {
    expect(trendPath(series(150)).line.endsWith(' L 680.0 18.0')).toBe(true)
    expect(trendPath(series(-5)).line.endsWith(' L 680.0 168.0')).toBe(true)
  })

  it('is deterministic', () => {
    expect(trendPath(series(42))).toEqual(trendPath(series(42)))
  })
})
