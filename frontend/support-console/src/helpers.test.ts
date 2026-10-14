// Specs for the pure presentation helpers: token NAMES only, never literals.
import { describe, expect, it } from 'vitest'

import { healthTone, SPARK_HEIGHT, SPARK_WIDTH, severityStyle, sparkline } from './helpers'
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
})
