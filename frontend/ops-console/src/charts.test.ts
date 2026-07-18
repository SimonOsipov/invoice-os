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
  buildEvidenceBundles,
  buildOutcomeColumns,
  buildRejectionReasons,
  buildSpendBars,
  compactCount,
  computeBillLine,
  computeQuota,
  fmt,
  lineChart,
  naira,
  nairaC,
  rng,
  SCALE_PLAN,
  series,
  showDeadLetterCallout,
  spendTotals,
  upStrip,
  vatSplit,
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

  // Adversarial (QA Mode B): the implementation guards `(seed >>> 0) || 1`, so seed 0
  // must fall back to seed 1's sequence rather than dividing-by/propagating a degenerate
  // LCG state of 0.
  it('rng_seed_zero_falls_back_to_seed_one: rng(0) draws the exact same sequence as rng(1)', () => {
    const zero = rng(0)
    const one = rng(1)
    const seqZero = Array.from({ length: 5 }, () => zero())
    const seqOne = Array.from({ length: 5 }, () => one())

    expect(seqZero).toEqual(seqOne)
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

  // Adversarial (QA Mode B): series() delegates to rng() internally, so the same
  // same-seed-same-sequence / different-seed-divergence contract rng carries must
  // survive the base/amp/trend transform layered on top of it.
  it('series_different_seeds_diverge: series(5, 1500, 640, 5, 71) and series(5, 1500, 640, 5, 88) are not deeply equal', () => {
    const a = series(5, 1500, 640, 5, 71)
    const b = series(5, 1500, 640, 5, 88)

    expect(a).not.toEqual(b)
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

  // Adversarial (QA Mode B): a single-point series makes `st = W / (n - 1)` divide by
  // zero, so `i * st` is `0 * Infinity = NaN` for the (only) point's x coordinate. This
  // is a genuine, verified property of the verbatim-ported algorithm (Node re-derivation,
  // not a guess) — pinned here as a known consumer hazard: any caller that can pass a
  // 1-point series (e.g. a dynamic time-range selector) will render a broken path. Not a
  // defect in this subtask (AC-1 requires a verbatim port, and computeQuota/SCALE_PLAN's
  // GAP-4 precedent already establishes new edge behavior is pinned, not silently
  // "fixed"), but real enough to lock in rather than leave undocumented.
  it('line_chart_single_point_x_is_nan: lineChart([5],100,50,5,5) has NaN in the x coordinate (n-1 divide-by-zero) but a finite y', () => {
    const { line } = lineChart([5], 100, 50, 5, 5)

    expect(line).toBe('MNaN 45.0')
    const y = Number(line.split(' ')[1])
    expect(Number.isNaN(y)).toBe(false)
  })

  it('line_chart_empty_array_no_nan: lineChart([],100,50,5,5) has an empty line and no NaN in either path', () => {
    const { line, area } = lineChart([], 100, 50, 5, 5)

    expect(line).toBe('')
    expect(line).not.toContain('NaN')
    expect(area).not.toContain('NaN')
  })

  it('line_chart_two_identical_points_is_byte_exact: lineChart([7,7],100,50,5,5) is exact under the (mx-mn)||1 flat guard', () => {
    const { line } = lineChart([7, 7], 100, 50, 5, 5)

    expect(line).toBe('M0.0 45.0 L100.0 45.0')
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

// Adversarial (QA Mode B): compactCount (GAP-1) is only exercised indirectly today, via
// computeQuota's `detail` string at the single pinned value 48214/40000/8214. Direct
// boundary coverage below closes that gap — every value below independently re-derived
// in Node against the shipped implementation, not guessed.
describe('compactCount', () => {
  it('compact_count_below_threshold_stays_bare: compactCount(999) is "999" (no K suffix below 1000)', () => {
    expect(compactCount(999)).toBe('999')
  })

  it('compact_count_exact_thousand_strips_trailing_zero: compactCount(1000) is "1K", not "1.0K"', () => {
    expect(compactCount(1000)).toBe('1K')
  })

  it('compact_count_rounds_half_up_at_the_tenth_boundary: compactCount(1049) is "1K" (rounds down to 1.0, stripped) and compactCount(1050) is "1.1K" (rounds up)', () => {
    expect(compactCount(1049)).toBe('1K')
    expect(compactCount(1050)).toBe('1.1K')
  })

  it('compact_count_exact_multiple_of_thousand: compactCount(40000) is "40K"', () => {
    expect(compactCount(40000)).toBe('40K')
  })

  it('compact_count_zero: compactCount(0) is "0"', () => {
    expect(compactCount(0)).toBe('0')
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

  // Adversarial (QA Mode B): buildSpendBars() feeds the spend-bars screenshot directly.
  // Non-determinism here (e.g. an accidental Math.random or Date.now creeping in later)
  // would make the visual QA gate flaky without any test catching it — full-array
  // (not just per-field) equality across two calls is the guard.
  it('spend_bars_are_fully_deterministic: buildSpendBars() called twice returns a deeply equal 30-element array', () => {
    expect(buildSpendBars()).toEqual(buildSpendBars())
  })
})

// QA Mode B (M4-20-03) — spendTotals() is the one new export this subtask adds to
// charts.ts (D1): buildSpendBars() only returns bar geometry, so the Spend MTD KPI card
// and the Spend-over-time card's two headline figures need their own export. Both specs
// below were re-derived independently in Node against the shipped implementation, not
// guessed.
describe('spendTotals', () => {
  it('spend_totals_seeded_values: spendTotals() returns the seeded 22-day sum/projection and nairaC compacts both to the pinned KPI strings', () => {
    const { mtd, proj } = spendTotals()

    expect(mtd).toBeCloseTo(3722091.704105027, 6)
    expect(proj).toBeCloseTo(5075579.5965068545, 6)
    expect(nairaC(mtd)).toBe('₦3.72M')
    expect(nairaC(proj)).toBe('₦5.08M')
  })

  // Product-advisor recommendation: spendTotals() and buildSpendBars() currently share
  // a private `spendSeries()` helper (structural coupling) — nothing stops a future edit
  // from giving one of them its own seed and silently drifting the two apart. A first
  // draft of this spec tried to recover buildSpendBars()'s internal scale (spMax) from
  // spendTotals().mtd and reconstruct the total from bar heights; mutation-testing that
  // draft (drift spendTotals() to a different seed) proved it a tautology — because
  // buildSpendBars() defines its projected-bar height as the *mean* of its own actual
  // bars by construction, feeding any mtd back through that ratio always reconstructs
  // the same mtd, independent of whether the two functions actually share a seed. That
  // reconstruction is scale-bound to whatever `mtd` you feed it, so it can never fail.
  //
  // This version is genuinely independent: it regenerates the documented ground-truth
  // series via the *exported*, pure `series()` (same literal params buildSpendBars is
  // documented to use: n=22, base=148000, amp=52000, trend=1500, seed=213) — not the
  // module-private `spendSeries()` helper the two functions actually share — and checks
  // that buildSpendBars()'s 22 actual-bar heights are *proportional* to that ground
  // truth (ratio-to-first-bar, scale-free, so it isn't vulnerable to the same
  // tautology). A same-seed run's ratios track the ground truth within ~0.001; any
  // other seed's ratios diverge by ~0.09-0.12 on average (re-derived in Node against
  // seeds 71/88/44) — nearly two orders of magnitude apart, so 0.02 is a safe cutoff.
  it('spend_totals_matches_bar_series: buildSpendBars() actual-bar heights are proportional to the same seed-213 series spendTotals().mtd sums (shared-seed invariant)', () => {
    const groundTruth = series(22, 148000, 52000, 1500, 213)
    const { mtd } = spendTotals()
    expect(mtd).toBeCloseTo(
      groundTruth.reduce((a, b) => a + b, 0),
      6,
    )

    const bars = buildSpendBars()
    const actualHeights = bars.slice(0, 22).map((b) => pct(b.h))
    let totalRatioDiff = 0
    for (let i = 1; i < 22; i++) {
      const barRatio = actualHeights[i]! / actualHeights[0]!
      const seriesRatio = groundTruth[i]! / groundTruth[0]!
      totalRatioDiff += Math.abs(barRatio - seriesRatio)
    }
    expect(totalRatioDiff / 21).toBeLessThan(0.02)
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

  // Adversarial (QA Mode B): buildOutcomeColumns() feeds the outcomes-strip screenshot.
  // The golden pin above only locks columns 0 and 23; this locks the full 24-column
  // array across two calls, so a regression in any of the other 22 columns (e.g. RNG
  // state leaking or a Date-seeded fallback) is still caught, not just visually.
  it('outcome_columns_are_fully_deterministic: buildOutcomeColumns() called twice returns a deeply equal 24-element array', () => {
    expect(buildOutcomeColumns()).toEqual(buildOutcomeColumns())
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

  // Adversarial (QA Mode B): boundary re-derived in Node against the shipped
  // implementation. At used === included exactly, pct and widthPct must both land on
  // 100 (not 99 from float slack, not 101), and there must be no overage.
  it('quota_used_equals_included_is_exactly_full: computeQuota(40000, 40000) is pct 100, widthPct 100, over 0', () => {
    const q = computeQuota(40000, 40000)

    expect(q.pct).toBe(100)
    expect(q.widthPct).toBe(100)
    expect(q.over).toBe(0)
  })

  // Adversarial (QA Mode B): `Math.max(0, used - included)` must clamp `over` at 0 when
  // usage is under quota — a naive `used - included` would go negative and corrupt the
  // billing display (a negative "over" line item).
  it('quota_used_below_included_never_goes_negative: computeQuota(30000, 40000) has over === 0, not -10000', () => {
    const q = computeQuota(30000, 40000)

    expect(q.pct).toBe(75)
    expect(q.widthPct).toBe(75)
    expect(q.over).toBe(0)
  })

  // Adversarial (QA Mode B): included === 0 is a division by zero. The implementation
  // has no explicit guard for this case, so the actual behavior below was independently
  // re-derived in Node against the shipped code (not assumed): used/0*100 is +Infinity,
  // Math.floor(Infinity) stays Infinity, and Math.min(100, Infinity) clamps widthPct to
  // 100 — so the bar geometry stays safe even though pct itself is not a finite number.
  // Pinning this locks in the chosen (implicit) behavior per the task's instruction to
  // assert it rather than leave it undefined; SCALE_PLAN.includedRequests is a hardcoded
  // 40000 so this path is not reachable via the real plan today, but computeQuota is a
  // general pure function and this is a real input for any future caller.
  it('quota_included_zero_pct_is_infinite_but_width_stays_clamped: computeQuota(100, 0) has pct === Infinity, widthPct === 100, over === 100', () => {
    const q = computeQuota(100, 0)

    expect(q.pct).toBe(Infinity)
    expect(q.widthPct).toBe(100)
    expect(q.over).toBe(100)
    expect(q.detail).toBe('100 / 0 included · 100 over')
  })

  // Adversarial (QA Mode B): the doubly-degenerate 0/0 case is NaN, not Infinity — a
  // distinct failure mode from the included=0-with-nonzero-used case above, and one a
  // naive `pct > 0` or `pct === 100` check downstream would silently mishandle (NaN
  // fails every numeric comparison). widthPct inherits the NaN via Math.min; over stays
  // a clean 0 because Math.max(0, 0 - 0) does not involve division.
  it('quota_used_and_included_both_zero_is_nan_not_zero: computeQuota(0, 0) has NaN pct and widthPct, but over is still exactly 0', () => {
    const q = computeQuota(0, 0)

    expect(Number.isNaN(q.pct)).toBe(true)
    expect(Number.isNaN(q.widthPct)).toBe(true)
    expect(q.over).toBe(0)
  })
})

// RED (M4-20-04) — showDeadLetterCallout is a DOM-free pure predicate extracted per QA
// finding F7 / decision [dead-letter-predicate-extracted]: the dead-letter callout's
// visibility is a three-term conjunction whose default-state rendering is identical
// whether or not the two guards (filter, query) were ported, so a screenshot gate
// cannot falsify a dropped guard. Target contract (prototype :1104 + :949):
// dlCount > 0 && filter === 'all' && !query. Currently fails on `not implemented`
// (the stub body throws) — that IS the correct RED reason, not a compile/import error.
describe('showDeadLetterCallout', () => {
  it('dl_callout_shows_when_all_three_conditions_hold: dlCount 3, filter "all", empty query is true', () => {
    expect(showDeadLetterCallout(3, 'all', '')).toBe(true)
  })

  it('dl_callout_hidden_when_no_dead_letters: dlCount 0, filter "all", empty query is false', () => {
    expect(showDeadLetterCallout(0, 'all', '')).toBe(false)
  })

  it('dl_callout_hidden_when_filter_guard_dropped: dlCount 3, filter "rejected", empty query is false', () => {
    expect(showDeadLetterCallout(3, 'rejected', '')).toBe(false)
  })

  it('dl_callout_hidden_when_query_guard_dropped: dlCount 3, filter "all", query "ZP-INV" is false', () => {
    expect(showDeadLetterCallout(3, 'all', 'ZP-INV')).toBe(false)
  })

  it('dl_callout_hidden_when_both_guards_dropped: dlCount 3, filter "accepted", query "ZP-INV" is false', () => {
    expect(showDeadLetterCallout(3, 'accepted', 'ZP-INV')).toBe(false)
  })

  it('dl_callout_hidden_when_filter_is_dead_letter_itself: dlCount 3, filter "dead-letter", empty query is false', () => {
    expect(showDeadLetterCallout(3, 'dead-letter', '')).toBe(false)
  })
})

// RED (M4-20-05, task-142 E2) — vatSplit is the dedup target for helpers.ts:35-36's
// inline reqJSON net/vat math, and is also what the evidence response embeds. Currently
// fails on `not implemented` (the stub body throws) — that IS the correct RED reason.
describe('vatSplit', () => {
  it('vat_split_matches_the_pinned_seed_value: vatSplit(4120000) is exactly { net: 3832558, vat: 287442 }', () => {
    expect(vatSplit(4120000)).toEqual({ net: 3832558, vat: 287442 })
  })

  it('vat_split_formula_holds_with_no_rounding_leak: net === Math.round(raw / 1.075), vat === raw - net, and net + vat === raw across several magnitudes', () => {
    for (const raw of [4120000, 412700, 305000, 22140000, 1, 100, 1000, 7, 999999]) {
      const { net, vat } = vatSplit(raw)
      expect(net).toBe(Math.round(raw / 1.075))
      expect(vat).toBe(raw - net)
      expect(net + vat).toBe(raw)
    }
  })
})

// RED (M4-20-05, task-142 E2) — buildEvidenceBundles ports evidenceData() (proto
// 1203-1236). Every golden value below was independently re-derived against the
// prototype's literal base rows + the `91 - i` / four-slice-length formulas, not
// trusted from prose.
describe('buildEvidenceBundles', () => {
  it('evidence_bundles_returns_eight_rows: buildEvidenceBundles() returns exactly 8 rows', () => {
    expect(buildEvidenceBundles().length).toBe(8)
  })

  it('evidence_bundle_row_zero_is_golden_pinned: row 0 (i=0) pins id/irn/csid/hash/prevHash exactly, catching the -6/-5/-4/-3 slice lengths and the 91-i base case', () => {
    const row = buildEvidenceBundles()[0]!

    expect(row.id).toBe('ev_088412')
    expect(row.irn).toBe('IRN-NG-88412-A91')
    expect(row.csid).toBe('MBS-CSID:9f2a8412e1b7c4d009f8e2c5a1f0b6d3e7c9a4b')
    expect(row.hash).toBe('sha256:9f8412a3e1b7c4d09f08e2c5a1f0b6d3e7c9a4d21b8')
    expect(row.prevHash).toBe('sha256:8e412c20')
  })

  it('evidence_bundle_row_seven_is_golden_pinned: row 7 (i=7) pins id/irn/csid/hash/prevHash exactly, catching the 91-i arithmetic and slice lengths at the far end of the array', () => {
    const row = buildEvidenceBundles()[7]!

    expect(row.id).toBe('ev_088210')
    expect(row.irn).toBe('IRN-NG-88210-A84')
    expect(row.csid).toBe('MBS-CSID:9f2a8210e1b7c4d079f8e2c5a1f0b6d3e7c9a4b')
    expect(row.hash).toBe('sha256:9f8210a3e1b7c4d09f78e2c5a1f0b6d3e7c9a4d21b8')
    expect(row.prevHash).toBe('sha256:8e210c27')
  })

  it('evidence_bundle_irn_descends_a91_to_a84_in_order: the 8 rows’ irn suffixes are exactly A91 down to A84, i.e. 91 - i for i = 0..7', () => {
    const suffixes = buildEvidenceBundles().map((r) => r.irn.slice(r.irn.lastIndexOf('-A') + 2))

    expect(suffixes).toEqual(['91', '90', '89', '88', '87', '86', '85', '84'])
  })

  it('evidence_bundle_ids_are_unique: all 8 ids are distinct (no invoice-suffix collision)', () => {
    const ids = buildEvidenceBundles().map((r) => r.id)

    expect(new Set(ids).size).toBe(8)
  })

  // The prototype's copy claims "HASH-CHAINED" (proto:320 pill text), but
  // evidenceData() does NOT actually chain prevHash to the previous row's hash:
  // prevHash is an independent template keyed only on the row's own invoice + index
  // ('sha256:8e' + invoice.slice(-3) + 'c2' + i), never derived from bundles[i-1].hash.
  // Port as-is (task-142 E2 verdict: "not a real chain... do not fix it into one").
  // This spec locks in the real (non-chained) behavior so a future "fix" that wires
  // prevHash to the prior row's actual hash is a deliberate, visible change, not a
  // silent one.
  it('evidence_bundle_prev_hash_is_independently_derived_not_chained_to_the_prior_row: row i prevHash never equals, and is never a substring of, row i-1 hash', () => {
    const bundles = buildEvidenceBundles()

    for (let i = 1; i < bundles.length; i++) {
      expect(bundles[i]!.prevHash).not.toBe(bundles[i - 1]!.hash)
      expect(bundles[i - 1]!.hash.includes(bundles[i]!.prevHash)).toBe(false)
    }
  })

  it('evidence_bundle_response_is_byte_exact_and_embeds_the_vat_split: row 0 response JSON pins irn/cleared_at reformatting and vatSplit(4120000)’s net/vat', () => {
    const row = buildEvidenceBundles()[0]!

    expect(row.response).toBe(
      '{\n  "status": "CLEARED",\n  "irn": "IRN-NG-88412-A91",\n  "csid": "MBS.9f2a…c7",\n  "cleared_at": "2026-07-18T09:14:00Z",\n  "net": 3832558,\n  "vat": 287442\n}',
    )
  })
})
