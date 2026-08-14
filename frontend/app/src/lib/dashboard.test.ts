// RED specs (M4-10-01, task-189, DASH-T1-T23) — pin the dashboard rollup fetch wrapper
// and its pure viewmodel helpers before the executor implements the bodies in
// dashboard.ts. Transcribed verbatim from the architect's Test Specs table (story
// "[M4-10-01]").
//
// getRollup is tested via a directly-injected vi.fn() authedFetch stub, NOT the
// createAuthedFetch + global-fetch route that invoices.test.ts/portfolio.test.ts use —
// that heavier route exists there to exercise the onUnauthorized seam, which getRollup
// does not own (it only ever calls the authedFetch it's handed).
//
// Every spec below currently fails because getRollup/donutSegments/deslug/topFailures/
// resolveCtaLabel/isEmptyRollup/dashboardViewState/entityHealth's stub bodies throw `new
// Error('not implemented')` before ever calling the injected authedFetch (or, for the
// pure helpers, before returning anything) — that IS the correct RED reason (assertion /
// not-implemented), not an import/compile/setup error.
import { describe, expect, it } from 'vitest'
import { vi } from 'vitest'

import { ApiError, type AsyncState } from '@invoice-os/api-client'

import type { AuthedFetch } from './portfolio'
import {
  dashboardViewState,
  deslug,
  EMPTY_BUCKET,
  entityHealth,
  formatMetric,
  getRollup,
  isEmptyRollup,
  metricRatio,
  readinessBars,
  readinessNote,
  readinessRing,
  resolveCtaLabel,
  scopedBucket,
  topFailures,
  donutSegments,
  type Counts,
  type Rollup,
  type RollupClient,
  type Metrics,
  type RuleCount,
} from './dashboard'

// Calls a (currently throwing) helper and returns the caught error, tolerating both a
// synchronous throw (today's stub) and an eventual async rejection — mirrors
// invoices.test.ts's / portfolio.test.ts's captureRejection helper.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected the call to reject, but it resolved')
}

const base = 'https://gw'

function counts(overrides: Partial<Counts> = {}): Counts {
  return {
    draft: 0,
    validated: 0,
    queued: 0,
    submitted: 0,
    accepted: 0,
    rejected: 0,
    failed: 0,
    ...overrides,
  }
}

const rollupFixture: Rollup = {
  totals: { counts: counts({ draft: 1 }), needs_attention: 1, metrics: {}, top_violations: [] },
  clients: [
    { entity_id: 'e1', entity_name: 'Okafor & Partners', counts: counts({ draft: 1 }), needs_attention: 1, metrics: {}, top_violations: [] },
  ],
  top_violations: [{ rule_key: 'supplier-tin-format', invoices: 1 }],
}

const CANONICAL_LABELS = ['Draft', 'Validated', 'Queued', 'Submitted', 'Accepted', 'Rejected', 'Failed']

describe('getRollup', () => {
  it('DASH-T1: GETs .../api/dashboard/v1/rollup once via the injected authedFetch and resolves the body verbatim', async () => {
    const fetchMock = vi.fn().mockResolvedValue(rollupFixture)
    const af = fetchMock as unknown as AuthedFetch

    const result = await getRollup(af, base)

    expect(result).toEqual(rollupFixture)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledWith('https://gw/api/dashboard/v1/rollup')
  })

  it('DASH-T2: an ApiError rejection from authedFetch propagates as the SAME instance, not reshaped', async () => {
    const apiErr = new ApiError('http', 'boom', 500)
    const fetchMock = vi.fn().mockRejectedValue(apiErr)
    const af = fetchMock as unknown as AuthedFetch

    const err = await captureRejection(() => getRollup(af, base))

    expect(err).toBe(apiErr)
  })

  it('DASH-T3: a plain (non-ApiError) rejection from authedFetch propagates as that same Error instance, not wrapped', async () => {
    const genericErr = new Error('boom')
    const fetchMock = vi.fn().mockRejectedValue(genericErr)
    const af = fetchMock as unknown as AuthedFetch

    const err = await captureRejection(() => getRollup(af, base))

    expect(err).toBe(genericErr)
  })
})

describe('donutSegments', () => {
  it('DASH-T4: all-zero counts still yield all 7 segments in canonical order, zeros shown as "0" / "0%"', () => {
    const segs = donutSegments(counts())

    expect(segs).toHaveLength(7)
    expect(segs.map((s) => s.label)).toEqual(CANONICAL_LABELS)
    for (const seg of segs) {
      expect(seg.count).toBe('0')
      expect(seg.pct).toBe('0%')
    }
  })

  it('DASH-T5: mixed counts (draft:3, validated:1) compute the correct count/pct per segment; order+length unchanged', () => {
    const segs = donutSegments(counts({ draft: 3, validated: 1 }))

    expect(segs).toHaveLength(7)
    expect(segs.map((s) => s.label)).toEqual(CANONICAL_LABELS)
    const byLabel = Object.fromEntries(segs.map((s) => [s.label, s]))
    expect(byLabel.Draft.count).toBe('3')
    expect(byLabel.Draft.pct).toBe('75%')
    expect(byLabel.Validated.count).toBe('1')
    expect(byLabel.Validated.pct).toBe('25%')
    for (const label of ['Queued', 'Submitted', 'Accepted', 'Rejected', 'Failed']) {
      expect(byLabel[label].count).toBe('0')
      expect(byLabel[label].pct).toBe('0%')
    }
  })

  it('DASH-T6: a single non-zero state (accepted:5) is 100%, every other state is 0%, order unchanged', () => {
    const segs = donutSegments(counts({ accepted: 5 }))

    const byLabel = Object.fromEntries(segs.map((s) => [s.label, s]))
    expect(byLabel.Accepted.pct).toBe('100%')
    for (const label of ['Draft', 'Validated', 'Queued', 'Submitted', 'Rejected', 'Failed']) {
      expect(byLabel[label].pct).toBe('0%')
    }
    expect(segs.map((s) => s.label)).toEqual(CANONICAL_LABELS)
  })

  it('DASH-T7: needs_attention is never a donut input, so no returned segment label ever names it', () => {
    const segs = donutSegments(counts({ draft: 2, rejected: 1, failed: 1 }))

    const labels = segs.map((s) => s.label.toLowerCase())
    expect(labels).not.toContain('needs attention')
    expect(labels).not.toContain('needs_attention')
    expect(segs).toHaveLength(7)
  })
})

describe('deslug', () => {
  it('DASH-T8: hyphens become spaces, sentence case, and TIN stays an acronym', () => {
    expect(deslug('supplier-tin-format')).toBe('Supplier TIN format')
  })

  it('DASH-T9: underscores become spaces, sentence case, and VAT stays an acronym', () => {
    expect(deslug('vat_standard_rate')).toBe('VAT standard rate')
  })

  it('DASH-T10: an already-spaced key is normalised to sentence case', () => {
    expect(deslug('Already Clean')).toBe('Already clean')
  })

  it('DASH-T11: a single lowercase word capitalises; an empty string stays empty', () => {
    expect(deslug('single')).toBe('Single')
    expect(deslug('')).toBe('')
  })

  it('DASH-T11b: a non-leading acronym is uppercased wherever it appears', () => {
    expect(deslug('buyer-tin-required')).toBe('Buyer TIN required')
    expect(deslug('mbs-transmission-failed')).toBe('MBS transmission failed')
  })
})

describe('topFailures', () => {
  it('DASH-T12: an empty violations list yields an empty failures list', () => {
    expect(topFailures([])).toEqual([])
  })

  it('DASH-T13: de-slugs each rule_key into label, keeps raw ruleKey/count, computes bar relative to the max, preserves server order', () => {
    const result = topFailures([
      { rule_key: 'supplier-tin-format', invoices: 3 },
      { rule_key: 'vat-standard-rate', invoices: 1 },
    ])

    expect(result).toEqual([
      { label: 'Supplier TIN format', ruleKey: 'supplier-tin-format', count: 3, bar: '100%' },
      { label: 'VAT standard rate', ruleKey: 'vat-standard-rate', count: 1, bar: '33%' },
    ])
  })
})

describe('resolveCtaLabel', () => {
  it('DASH-T14: zero issues renders all-clear copy — no "Resolve"', () => {
    const label = resolveCtaLabel(0)

    expect(label).not.toContain('Resolve')
  })

  it('DASH-T15: exactly one issue is singular: "Resolve 1 issue →"', () => {
    expect(resolveCtaLabel(1)).toBe('Resolve 1 issue →')
  })

  it('DASH-T16: more than one issue is plural: "Resolve 5 issues →"', () => {
    expect(resolveCtaLabel(5)).toBe('Resolve 5 issues →')
  })
})

describe('isEmptyRollup', () => {
  it('DASH-T17: all-zero totals.counts is empty', () => {
    const r: Rollup = { totals: { counts: counts(), needs_attention: 0, metrics: {}, top_violations: [] }, clients: [], top_violations: [] }

    expect(isEmptyRollup(r)).toBe(true)
  })

  it('DASH-T18: any non-zero total count is not empty', () => {
    const r: Rollup = {
      totals: { counts: counts({ draft: 1 }), needs_attention: 0, metrics: {}, top_violations: [] },
      clients: [],
      top_violations: [],
    }

    expect(isEmptyRollup(r)).toBe(false)
  })
})

describe('dashboardViewState', () => {
  it('DASH-T19: base==null is "idle" regardless of async status (no-gateway zero-network short-circuit wins)', () => {
    const s: AsyncState<Rollup> = { status: 'ready', data: rollupFixture, error: null }

    expect(dashboardViewState(null, s)).toBe('idle')
  })

  it('DASH-T20: base present mirrors async.status exactly, for loading/empty/error/ready', () => {
    const cases: Array<AsyncState<Rollup>> = [
      { status: 'loading', data: null, error: null },
      { status: 'empty', data: null, error: null },
      { status: 'error', data: null, error: new ApiError('network', 'boom') },
      { status: 'ready', data: rollupFixture, error: null },
    ]

    for (const s of cases) {
      expect(dashboardViewState(base, s)).toBe(s.status)
    }
  })
})

describe('entityHealth', () => {
  const clientA: RollupClient = { entity_id: 'A', entity_name: 'Acme', counts: counts(), needs_attention: 0, metrics: {}, top_violations: [] }

  it('DASH-T21: an entity absent from clients reads no-invoices', () => {
    expect(entityHealth([clientA], 'Z')).toEqual({ kind: 'no-invoices' })
  })

  it('DASH-T22: an entity present with needs_attention:2 reads needs-attention with that count', () => {
    const clientZ: RollupClient = { entity_id: 'Z', entity_name: 'Zeta', counts: counts(), needs_attention: 2, metrics: {}, top_violations: [] }

    expect(entityHealth([clientA, clientZ], 'Z')).toEqual({ kind: 'needs-attention', count: 2 })
  })

  it('DASH-T23: an entity present with needs_attention:0 reads clear', () => {
    const clientZ: RollupClient = { entity_id: 'Z', entity_name: 'Zeta', counts: counts(), needs_attention: 0, metrics: {}, top_violations: [] }

    expect(entityHealth([clientA, clientZ], 'Z')).toEqual({ kind: 'clear' })
  })
})

// scopedBucket ([dashboard-scope-per-client], persona-handoff-fix step 2) — added
// alongside the client-scoped Overview/Sidebar this story ships, not part of the
// architect's original DASH-T1-T23 table.
describe('scopedBucket', () => {
  const rollup: Rollup = {
    totals: { counts: counts({ draft: 5, accepted: 2 }), needs_attention: 3, metrics: {}, top_violations: [] },
    clients: [
      { entity_id: 'e1', entity_name: 'Acme', counts: counts({ validated: 4 }), needs_attention: 1, metrics: {}, top_violations: [] },
    ],
    top_violations: [],
  }

  it('in-house (isInhouse:true) always resolves rollup.totals, regardless of entityId', () => {
    expect(scopedBucket(true, null, rollup)).toEqual(rollup.totals)
    expect(scopedBucket(true, 'e1', rollup)).toEqual(rollup.totals)
  })

  it('firm mode with a real entityId present in `clients` resolves that entity\'s own bucket, not totals', () => {
    expect(scopedBucket(false, 'e1', rollup)).toEqual({
      counts: counts({ validated: 4 }),
      needs_attention: 1,
      metrics: {},
      top_violations: [],
    })
  })

  it('firm mode with entityId===null (no client resolved yet) resolves EMPTY_BUCKET, never totals', () => {
    expect(scopedBucket(false, null, rollup)).toEqual(EMPTY_BUCKET)
  })

  it('firm mode with an entityId absent from `clients` (zero invoices, INNER JOIN) resolves EMPTY_BUCKET, never totals', () => {
    expect(scopedBucket(false, 'e-no-invoices', rollup)).toEqual(EMPTY_BUCKET)
  })

  // Both legs in one test on purpose: the in-house leg returns rollup.totals by
  // reference and carries any new field for free, so only the firm leg's
  // field-by-field rebuild can silently drop one.
  it('scopedBucket carries awaiting_approval on both legs', () => {
    const r: Rollup = {
      totals: { counts: counts({ validated: 9 }), needs_attention: 3, awaiting_approval: 7, metrics: {}, top_violations: [] },
      clients: [
        { entity_id: 'e1', entity_name: 'Acme', counts: counts({ validated: 4 }), needs_attention: 1, awaiting_approval: 2, metrics: {}, top_violations: [] },
      ],
      top_violations: [],
    }

    expect(scopedBucket(true, null, r).awaiting_approval).toBe(7)
    expect(scopedBucket(false, 'e1', r).awaiting_approval).toBe(2)
    expect(scopedBucket(false, 'e-no-invoices', r).awaiting_approval).toBe(0)
    expect(EMPTY_BUCKET.awaiting_approval).toBe(0)
  })
})

// QA adversarial coverage (Mode B, task-189) — appended post-implementation. These are NOT
// from the architect's Test Specs table (DASH-T1..T23 above); they target gaps the
// happy-path table doesn't reach. Every test here is mutation-verified to fail if the
// corresponding behavior regresses (verified manually during QA, not committed).

describe('deslug — QA adversarial', () => {
  it('QA-D1: consecutive separators (double hyphen or double underscore) collapse to a single space', () => {
    // deslug splits on a run of separators (/[-_\s]+/) and drops empty tokens, so a
    // doubled separator does not leak a second space into the rendered label.
    expect(deslug('a--b')).toBe('A b')
    expect(deslug('a__b')).toBe('A b')
  })

  it('QA-D2: mixed hyphen/underscore in the same key deslugs to single-spaced sentence case, same as either alone', () => {
    expect(deslug('a-b_c')).toBe('A b c')
  })

  it('QA-D3: a leading or trailing separator is trimmed away (no edge space)', () => {
    // the empty leading/trailing tokens produced by the split are dropped by filter(Boolean).
    expect(deslug('-abc-')).toBe('Abc')
  })

  it('QA-D4: a numeric segment is left untouched (no crash on a digit-only "word")', () => {
    expect(deslug('rule-2-check')).toBe('Rule 2 check')
  })
})

describe('donutSegments — QA adversarial', () => {
  it('QA-DS1: with 3 nonzero states, each offset equals the negative running sum of PRIOR segments\' own arc lengths (not just per-segment pct); order and 7-segment presence hold', () => {
    const segs = donutSegments(counts({ draft: 1, accepted: 3, failed: 2 }))

    expect(segs).toHaveLength(7)
    expect(segs.map((s) => s.label)).toEqual(CANONICAL_LABELS)

    const dashLen = (seg: (typeof segs)[number]) => parseFloat(seg.dash.split(' ')[0])
    let expectedOffset = 0
    for (const seg of segs) {
      expect(parseFloat(seg.offset)).toBeCloseTo(-expectedOffset, 0)
      expectedOffset += dashLen(seg)
    }
  })
})

describe('topFailures — QA adversarial', () => {
  it('QA-TF1: two rules tied on invoices both bar at 100% and keep server (input) order', () => {
    const result = topFailures([
      { rule_key: 'rule-a', invoices: 2 },
      { rule_key: 'rule-b', invoices: 2 },
    ])

    expect(result).toEqual([
      { label: 'Rule a', ruleKey: 'rule-a', count: 2, bar: '100%' },
      { label: 'Rule b', ruleKey: 'rule-b', count: 2, bar: '100%' },
    ])
  })

  it('QA-TF2: a single-element list bars its only rule at 100%', () => {
    const result = topFailures([{ rule_key: 'only-rule', invoices: 7 }])

    expect(result).toEqual([{ label: 'Only rule', ruleKey: 'only-rule', count: 7, bar: '100%' }])
  })
})

describe('resolveCtaLabel — QA adversarial', () => {
  it('QA-RC1: n=2 is the plural boundary just past singular: "Resolve 2 issues →"', () => {
    expect(resolveCtaLabel(2)).toBe('Resolve 2 issues →')
  })
})

describe('entityHealth — QA adversarial', () => {
  it('QA-EH1: an empty clients array reads no-invoices for any entityId', () => {
    expect(entityHealth([], 'Z')).toEqual({ kind: 'no-invoices' })
  })

  it('QA-EH2: a present client with a large needs_attention count round-trips that exact count, uncapped/untruncated', () => {
    const clientBig: RollupClient = { entity_id: 'BIG', entity_name: 'Big Co', counts: counts(), needs_attention: 137, metrics: {}, top_violations: [] }

    expect(entityHealth([clientBig], 'BIG')).toEqual({ kind: 'needs-attention', count: 137 })
  })
})

describe('isEmptyRollup — QA adversarial: exactly one of the 7 states nonzero', () => {
  const stateKeys: (keyof Counts)[] = [
    'draft',
    'validated',
    'queued',
    'submitted',
    'accepted',
    'rejected',
    'failed',
  ]

  for (const key of stateKeys) {
    it(`QA-IE-${key}: only "${key}" nonzero is not empty (guards a helper checking a subset of the 7 keys)`, () => {
      const r: Rollup = {
        totals: { counts: counts({ [key]: 1 } as Partial<Counts>), needs_attention: 0, metrics: {}, top_violations: [] },
        clients: [],
        top_violations: [],
      }

      expect(isEmptyRollup(r)).toBe(false)
    })
  }
})

// Metric registry & derived helpers (Mode A RED, task-428) — transcribed from the
// architect's Test Specs table (FMR-01..FMR-13). dashboard.ts does not yet export
// Metrics/metricRatio/formatMetric/readinessRing/readinessBars/readinessNote, and
// RollupBucket/RollupClient don't yet carry metrics/top_violations — every spec below
// is expected to fail on that missing surface, not a typo.

const CIRC = 2 * Math.PI * 50

describe('metricRatio', () => {
  it('FMR-01: rounds num/den to a 0..100 percentage', () => {
    const m: Metrics = { readiness: { num: 2, den: 262 } }
    expect(metricRatio(m, 'readiness')).toBe(1)
  })

  it('FMR-02: an absent key and a zero denominator both read null, never 0', () => {
    const m: Metrics = { readiness: { num: 5, den: 0 } }
    expect(metricRatio({}, 'readiness')).toBeNull()
    expect(metricRatio(m, 'readiness')).toBeNull()
  })
})

describe('formatMetric', () => {
  it('FMR-03: a null underlying value renders the em dash for every kind (ratio/count/amount)', () => {
    expect(formatMetric({}, 'readiness')).toBe('—')
    expect(formatMetric({}, 'blocked_by_rules')).toBe('—')
    expect(formatMetric({}, 'vat_tracked')).toBe('—')
  })

  it('FMR-04: vat_tracked converts kobo to naira exactly once, formatted via fmtShort', () => {
    const m: Metrics = { vat_tracked: { num: 240_000_000, den: 2 } }
    expect(formatMetric(m, 'vat_tracked')).toBe('₦2.4M')
  })

  it('FMR-05: a count metric renders the plain integer, not a percentage', () => {
    const m: Metrics = { blocked_by_rules: { num: 12, den: 259 } }
    expect(formatMetric(m, 'blocked_by_rules')).toBe('12')
  })

  it('FMR-06: an unregistered key renders the em dash and does not throw', () => {
    const m: Metrics = { made_up_key: { num: 1, den: 1 } }
    expect(() => formatMetric(m, 'made_up_key')).not.toThrow()
    expect(formatMetric(m, 'made_up_key')).toBe('—')
  })
})

describe('readinessRing', () => {
  it('FMR-07a: 79% is amber-banded, offset matches the shipped ring geometry', () => {
    const ring = readinessRing({ readiness: { num: 79, den: 100 } })
    expect(ring.score).toBe(79)
    expect(ring.color).toBe('var(--status-amber-text)')
    expect(ring.circ).toBe(CIRC.toFixed(1))
    expect(ring.offset).toBe((CIRC * (1 - 79 / 100)).toFixed(1))
  })

  it('FMR-07b: 90% crosses into the --action band', () => {
    const ring = readinessRing({ readiness: { num: 90, den: 100 } })
    expect(ring.color).toBe('var(--action)')
  })

  it('FMR-07c: 60% falls into the red band', () => {
    const ring = readinessRing({ readiness: { num: 60, den: 100 } })
    expect(ring.color).toBe('var(--status-red-text)')
  })

  it('FMR-07d: an absent readiness metric yields a null score and a fully-offset (empty) ring', () => {
    const ring = readinessRing({})
    expect(ring.score).toBeNull()
    expect(ring.offset).toBe(CIRC.toFixed(1))
  })
})

describe('readinessBars', () => {
  it('FMR-08: exactly three bars, fixed order, pinned labels', () => {
    const m: Metrics = {
      bar_field_completeness: { num: 90, den: 100 },
      bar_tax_accuracy: { num: 70, den: 100 },
      bar_identifiers_format: { num: 50, den: 100 },
    }
    const bars = readinessBars(m)
    expect(bars).toHaveLength(3)
    expect(bars.map((b) => b.label)).toEqual([
      'Field completeness',
      'Tax accuracy · VAT / WHT',
      'Identifiers & format',
    ])
  })

  it('FMR-09: an absent bar metric reads pct:null and the em-dash label', () => {
    const bars = readinessBars({})
    expect(bars).toHaveLength(3)
    for (const bar of bars) {
      expect(bar.pct).toBeNull()
      expect(bar.pctLabel).toBe('—')
    }
  })
})

describe('readinessNote', () => {
  it('FMR-10: non-zero clauses join in fixed order: blocked, failed, unchecked', () => {
    const m: Metrics = {
      blocked_by_rules: { num: 12, den: 300 },
      failed_in_transmission: { num: 3, den: 300 },
      never_validated: { num: 259, den: 300 },
    }
    expect(readinessNote(m)).toBe('12 blocked by rules · 3 failed in transmission · 259 not yet checked')
  })

  // Closes a vacuous-pass gap FMR-10/11 alone would leave open: a join that never
  // actually omits a zero clause would still satisfy both of those.
  it('FMR-10b: a zero clause is omitted from the join, not rendered as "0 ..."', () => {
    const m: Metrics = {
      blocked_by_rules: { num: 0, den: 300 },
      failed_in_transmission: { num: 4, den: 300 },
      never_validated: { num: 0, den: 300 },
    }
    expect(readinessNote(m)).toBe('4 failed in transmission')
  })

  it('FMR-11: all three counts zero renders the all-clear sentence', () => {
    const m: Metrics = {
      blocked_by_rules: { num: 0, den: 300 },
      failed_in_transmission: { num: 0, den: 300 },
      never_validated: { num: 0, den: 300 },
    }
    expect(readinessNote(m)).toBe('All invoices checked and clear of blocking rules.')
  })

  it('FMR-12: no metrics at all reads "No invoices yet"', () => {
    expect(readinessNote({})).toBe('No invoices yet')
  })
})

describe('scopedBucket / EMPTY_BUCKET — metrics and top_violations passthrough', () => {
  const totalsMetrics: Metrics = { readiness: { num: 80, den: 100 } }
  const totalsViolations: RuleCount[] = [{ rule_key: 'supplier-tin-required', invoices: 2 }]
  const clientMetrics: Metrics = { readiness: { num: 60, den: 50 } }
  const clientViolations: RuleCount[] = [{ rule_key: 'buyer-tin-required', invoices: 1 }]

  const rollup: Rollup = {
    totals: {
      counts: counts({ draft: 5 }),
      needs_attention: 3,
      metrics: totalsMetrics,
      top_violations: totalsViolations,
    },
    clients: [
      {
        entity_id: 'e1',
        entity_name: 'Acme',
        counts: counts({ validated: 4 }),
        needs_attention: 1,
        metrics: clientMetrics,
        top_violations: clientViolations,
      },
    ],
    top_violations: [],
  }

  it('FMR-13a: in-house mode carries metrics and top_violations through (rollup.totals)', () => {
    const bucket = scopedBucket(true, null, rollup)
    expect(bucket.metrics).toEqual(totalsMetrics)
    expect(bucket.top_violations).toEqual(totalsViolations)
  })

  it('FMR-13b: firm mode carries metrics and top_violations through from the selected client, not totals', () => {
    const bucket = scopedBucket(false, 'e1', rollup)
    expect(bucket.metrics).toEqual(clientMetrics)
    expect(bucket.top_violations).toEqual(clientViolations)
  })

  it('FMR-13c: EMPTY_BUCKET carries metrics: {} and top_violations: []', () => {
    expect(EMPTY_BUCKET.metrics).toEqual({})
    expect(EMPTY_BUCKET.top_violations).toEqual([])
  })
})

// QA adversarial coverage (Mode B, task-428) — appended post-implementation, same
// convention as the DASH-T block above: not from the architect's FMR table, targets
// gaps the happy-path table doesn't reach.

describe('metricRatio — QA adversarial', () => {
  it('QA-MR1: num > den (cannot happen server-side) is not clamped to 100 — documents the gap, not a passing contract', () => {
    expect(metricRatio({ x: { num: 150, den: 100 } }, 'x')).toBe(150)
  })

  it('QA-MR2: a negative num is not clamped to 0 either — same unclamped-math gap as QA-MR1', () => {
    expect(metricRatio({ x: { num: -5, den: 100 } }, 'x')).toBe(-5)
  })

  it('QA-MR3: den:1 boundary — num:0 reads 0%, num:1 reads 100%, neither is mistaken for the null/absent case', () => {
    expect(metricRatio({ x: { num: 0, den: 1 } }, 'x')).toBe(0)
    expect(metricRatio({ x: { num: 1, den: 1 } }, 'x')).toBe(100)
  })
})

describe('formatMetric — QA adversarial', () => {
  it('QA-FM1: den:1 boundary renders "0%" and "100%", not the em dash', () => {
    expect(formatMetric({ readiness: { num: 0, den: 1 } }, 'readiness')).toBe('0%')
    expect(formatMetric({ readiness: { num: 1, den: 1 } }, 'readiness')).toBe('100%')
  })

  it('QA-FM2: a very large kobo amount (billions of naira) still routes through fmtShort — no separate "B" unit, so it renders as a 4-digit "M" figure, not an em dash or a throw', () => {
    const m: Metrics = { vat_tracked: { num: 350_000_000_000, den: 1 } } // 3.5B naira after /100
    expect(formatMetric(m, 'vat_tracked')).toBe('₦3500.0M')
  })
})

describe('readinessBars — QA adversarial', () => {
  it('QA-RB1: only one of the three bar metrics present — that one computes normally, the other two read null/em-dash, all three still returned in fixed order', () => {
    const m: Metrics = { bar_tax_accuracy: { num: 90, den: 100 } }
    const bars = readinessBars(m)

    expect(bars).toHaveLength(3)
    expect(bars[0].pct).toBeNull()
    expect(bars[0].pctLabel).toBe('—')
    expect(bars[1].pct).toBe(90)
    expect(bars[1].pctLabel).toBe('90%')
    expect(bars[2].pct).toBeNull()
    expect(bars[2].pctLabel).toBe('—')
  })
})
