// RED specs (M4-20-01) — pin the chart/format/billing math layer (rng, series,
// lineChart, formatters, spend/outcome/rejection/uptime generators, billing/quota
// math) before the executor implements the bodies in charts.ts. Transcribed verbatim
// from the Obsidian M4-20 story's Test Specs table (23 rows), amended per task-138's
// validation addenda: GAP-2 (buildRejectionReasons takes {label,count}[]), GAP-3
// (SpendBar.proj flag), GAP-5 (added series clamping case), GAP-6 (lineChart y-bounds
// are inclusive), plus the 0.021 outcome-sum tolerance and Math.floor-not-round /
// clamped-widthPct / byte-exact-detail quota notes.
//
// Every spec below currently fails because charts.ts's stub bodies throw `new
// Error('not implemented')` before returning anything — that IS the correct RED reason
// (assertion / not-implemented), not an import/compile/setup error. These are pure (no
// React, no fetch) functions — no vi.stubGlobal/mocking needed here.
import { describe, expect, it } from 'vitest'

import {
  buildOutcomeColumns,
  buildRejectionReasons,
  buildSpendBars,
  computeBillLine,
  computeQuota,
  fmt,
  lineChart,
  naira,
  nairaC,
  rng,
  SCALE_PLAN,
  series,
  upStrip,
  type RejectionInput,
} from './charts'

// Extracts every y coordinate from an `M{x} {y} L{x} {y} ...` SVG path string produced
// by lineChart — coordinate pairs alternate x, y, so odd token indices are y values.
function extractYs(path: string): number[] {
  return path
    .split(' ')
    .filter((_, i) => i % 2 === 1)
    .map(Number)
}

// buildOutcomeColumns/buildSpendBars emit percentage strings (e.g. '96.08%', '78.3%');
// parseFloat stops at the trailing '%' and returns the numeric magnitude.
function pct(s: string): number {
  return parseFloat(s)
}

describe('rng', () => {
  it('rng_is_a_deterministic_lcg: seed 1 draws exactly the LCG formula three times in a row', () => {
    const next = rng(1)
    let s = 1
    const step = () => {
      s = (s * 1664525 + 1013904223) >>> 0
      return s / 4294967296
    }

    expect(next()).toBe(step())
    expect(next()).toBe(step())
    expect(next()).toBe(step())
  })

  it('rng_same_seed_same_sequence: two independent generators from seed 413 draw the same 24-value sequence', () => {
    const a = rng(413)
    const b = rng(413)
    const seqA = Array.from({ length: 24 }, () => a())
    const seqB = Array.from({ length: 24 }, () => b())

    expect(seqA).toEqual(seqB)
  })

  it('rng_different_seeds_diverge: seeds 71 and 88 draw different 10-value sequences', () => {
    const a = rng(71)
    const b = rng(88)
    const seqA = Array.from({ length: 10 }, () => a())
    const seqB = Array.from({ length: 10 }, () => b())

    expect(seqA).not.toEqual(seqB)
  })
})

describe('series', () => {
  it('series_length_and_floor: series(30, 1.55, 0.5, 0.012, 88) has length 30 and every value is >= 1', () => {
    const s = series(30, 1.55, 0.5, 0.012, 88)

    expect(s.length).toBe(30)
    expect(s.every((v) => v >= 1)).toBe(true)
  })

  it('series_is_deterministic: series(7, 1500, 640, 5, 71) called twice returns deeply equal arrays', () => {
    expect(series(7, 1500, 640, 5, 71)).toEqual(series(7, 1500, 640, 5, 71))
  })

  // GAP-5 — series_length_and_floor never exercises the Math.max(1, …) floor (its raw
  // minimum never dips below 1). This case's raw minimum is -0.7946, so 13 of its 20
  // values clamp to exactly 1.
  it('series_floor_actually_engages: series(20, 1, 4, 0, 5) clamps thirteen of twenty values to exactly the floor of 1 (GAP-5)', () => {
    const s = series(20, 1, 4, 0, 5)
    const atFloor = s.filter((v) => v === 1)

    expect(atFloor.length).toBe(13)
    expect(Math.min(...s)).toBe(1)
    expect(s.some((v) => v === 1)).toBe(true)
  })
})

describe('lineChart', () => {
  it('line_chart_path_shape: lineChart([1,2,3,4],1000,240,12,6) is the exact four-point path with three L commands, x from 0.0 to 1000.0', () => {
    const { line } = lineChart([1, 2, 3, 4], 1000, 240, 12, 6)

    expect(line).toBe('M0.0 234.0 L333.3 160.0 L666.7 86.0 L1000.0 12.0')
  })

  // GAP-6 — the measured y range is exactly [8, 84] and both endpoints are attained
  // (series min/max map to the padding edges by construction), so the bounds check
  // MUST be inclusive.
  it('line_chart_y_within_padding: every y of lineChart(series(30,1.55,0.5,0.012,88),400,90,8,6) is within the inclusive range [8, 84] and never NaN (GAP-6)', () => {
    const { line } = lineChart(series(30, 1.55, 0.5, 0.012, 88), 400, 90, 8, 6)
    const ys = extractYs(line)

    expect(ys.length).toBeGreaterThan(0)
    for (const y of ys) {
      expect(Number.isNaN(y)).toBe(false)
      expect(y).toBeGreaterThanOrEqual(8)
      expect(y).toBeLessThanOrEqual(84)
    }
  })

  it('line_chart_flat_series_no_div_by_zero: lineChart([5,5,5],100,50,5,5) has no NaN because the (mx-mn)||1 range guard holds', () => {
    const { line, area } = lineChart([5, 5, 5], 100, 50, 5, 5)

    expect(line).not.toContain('NaN')
    expect(area).not.toContain('NaN')
  })

  it('line_chart_area_closes_the_path: lineChart([1,2],1000,240,12,6) closes area as line + " L1000 240 L0 240 Z"', () => {
    const { line, area } = lineChart([1, 2], 1000, 240, 12, 6)

    expect(area).toBe(`${line} L1000 240 L0 240 Z`)
    expect(area).toBe('M0.0 234.0 L1000.0 12.0 L1000 240 L0 240 Z')
  })
})

describe('fmt', () => {
  it('fmt_rounds_then_groups: fmt(48214.6) is "48,215"; fmt(412) is "412"', () => {
    expect(fmt(48214.6)).toBe('48,215')
    expect(fmt(412)).toBe('412')
  })
})

describe('nairaC', () => {
  it('naira_c_magnitude_thresholds: nairaC picks the right compact magnitude suffix at each threshold', () => {
    expect(nairaC(18.7e9)).toBe('₦18.7B')
    expect(nairaC(3184200)).toBe('₦3.18M')
    expect(nairaC(48214)).toBe('₦48K')
    expect(nairaC(940)).toBe('₦940')
  })
})

describe('naira', () => {
  it('naira_full_grouping: naira(4120000) is "₦4,120,000"', () => {
    expect(naira(4120000)).toBe('₦4,120,000')
  })
})

describe('buildSpendBars', () => {
  // No export surfaces the raw 22-day sum or the projected total directly, so this
  // pins the projection formula against the exact `series(22, 148000, 52000, 1500,
  // 213)` call buildSpendBars is documented to use for its 22 actual bars — both the
  // deterministic golden sum/projection (independently re-derived) and the directional
  // invariant the formula must preserve.
  it('spend_projection_extrapolates_22_days_to_30: the 22-day actual series projects to day 30 via (sum/22)*30, exceeding the 22-day sum', () => {
    const actual = series(22, 148000, 52000, 1500, 213)
    const sum = actual.reduce((a, b) => a + b, 0)
    const proj = (sum / 22) * 30

    expect(sum).toBeCloseTo(3722091.704105027, 6)
    expect(proj).toBeCloseTo(5075579.5965068545, 6)
    expect(proj).toBeGreaterThan(sum)
  })

  // GAP-3 — SpendBar exposes a `proj` flag distinguishing the 22 actual bars from the
  // 8 projected bars (30 = 22 + 8).
  it('spend_bars_split_actual_and_projected: buildSpendBars returns 30 bars, 0-21 actual and 22-29 projected, every height in (0%, 100%] (GAP-3)', () => {
    const bars = buildSpendBars()

    expect(bars.length).toBe(30)
    for (let i = 0; i < 22; i++) {
      expect(bars[i]!.proj).toBe(false)
    }
    for (let i = 22; i < 30; i++) {
      expect(bars[i]!.proj).toBe(true)
    }
    for (const bar of bars) {
      const h = pct(bar.h)
      expect(h).toBeGreaterThan(0)
      expect(h).toBeLessThanOrEqual(100)
    }
  })
})

describe('buildOutcomeColumns', () => {
  it('outcome_columns_sum_to_one_hundred: all 24 columns sum to 100 within 0.021, and every one of the 96 segments is >= 0', () => {
    const cols = buildOutcomeColumns()

    expect(cols.length).toBe(24)
    for (const col of cols) {
      const values = [pct(col.acc), pct(col.rej), pct(col.fail), pct(col.pend)]
      const sum = values.reduce((a, b) => a + b, 0)

      expect(Math.abs(sum - 100)).toBeLessThanOrEqual(0.021)
      for (const v of values) {
        expect(v).toBeGreaterThanOrEqual(0)
      }
    }
  })

  it('outcome_failed_and_pending_are_equal: fail === pend in all 24 columns (same halved remainder after acc/rej)', () => {
    const cols = buildOutcomeColumns()

    for (const col of cols) {
      expect(col.fail).toBe(col.pend)
    }
  })

  it('outcome_columns_are_golden_pinned: column 0 and column 23 are byte-exact (fixed seed 413)', () => {
    const cols = buildOutcomeColumns()

    expect(cols[0]).toEqual({ acc: '96.08%', rej: '2.83%', fail: '0.54%', pend: '0.54%' })
    expect(cols[23]).toEqual({ acc: '94.71%', rej: '4.01%', fail: '0.64%', pend: '0.64%' })
  })
})

describe('buildRejectionReasons', () => {
  // GAP-2 — the prototype's input (lines 916-922) is {label, count}[], not bare
  // numbers. Label text is not itself pinned by the story; only the width/count
  // arithmetic derived from `count` is under test here.
  it('rejection_bar_widths_are_relative_to_max: widths are pct-of-max, strictly descending, byte-exact at the third bar (GAP-2)', () => {
    const raw: RejectionInput[] = [
      { label: 'Invalid TIN', count: 412 },
      { label: 'Duplicate IRN', count: 287 },
      { label: 'Schema mismatch', count: 176 },
      { label: 'Timeout', count: 98 },
      { label: 'Other', count: 44 },
    ]

    const bars = buildRejectionReasons(raw)

    expect(bars[0]!.width).toBe('100.0%')
    for (let i = 1; i < bars.length; i++) {
      expect(pct(bars[i]!.width)).toBeLessThan(pct(bars[i - 1]!.width))
    }
    expect(bars[2]!.width).toBe('42.7%')
  })
})

describe('upStrip', () => {
  it('up_strip_marks_bad_indices: 90 cells; indices 86-89 share one fill token distinct from index 0', () => {
    const cells = upStrip(4, [86, 87, 88, 89])

    expect(cells.length).toBe(90)
    const badFill = cells[86]!.fill
    for (const idx of [86, 87, 88, 89]) {
      expect(cells[idx]!.fill).toBe(badFill)
    }
    expect(cells[0]!.fill).not.toBe(badFill)
  })

  it('up_strip_is_deterministic: upStrip(4, [86,87,88,89]) called twice returns deeply equal arrays', () => {
    expect(upStrip(4, [86, 87, 88, 89])).toEqual(upStrip(4, [86, 87, 88, 89]))
  })
})

describe('computeBillLine', () => {
  it('bill_line_amounts_match_seeded_totals: computeBillLine reproduces the prototype billing table totals using SCALE_PLAN rates', () => {
    expect(SCALE_PLAN.clearedRate).toBe(40)
    expect(SCALE_PLAN.overageRate).toBe(42)
    expect(SCALE_PLAN.includedRequests).toBe(40000)
    expect(computeBillLine(46820, SCALE_PLAN.clearedRate)).toBe(1_872_800)
    expect(computeBillLine(8214, SCALE_PLAN.overageRate)).toBe(344_988)
  })
})

describe('computeQuota', () => {
  it('quota_pct_floors_it_does_not_round: pct is Math.floor(used/included*100) — 120 not 121; 39999/40000 floors to 99, never 100', () => {
    expect(computeQuota(48214, 40000).pct).toBe(120)
    expect(computeQuota(39999, 40000).pct).toBe(99)
  })

  it('quota_width_clamps_and_detail_is_byte_exact: over, clamped widthPct, and the middle-dot detail string are exact', () => {
    const q = computeQuota(48214, 40000)

    expect(q.over).toBe(8214)
    expect(q.widthPct).toBe(100)
    expect(q.detail).toBe('48.2K / 40K included · 8.2K over')
  })
})
