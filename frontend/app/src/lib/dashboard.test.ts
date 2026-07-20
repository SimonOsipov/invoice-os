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
  entityHealth,
  getRollup,
  isEmptyRollup,
  resolveCtaLabel,
  topFailures,
  donutSegments,
  type Counts,
  type Rollup,
  type RollupClient,
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
  totals: { counts: counts({ draft: 1 }), needs_attention: 1 },
  clients: [{ entity_id: 'e1', entity_name: 'Okafor & Partners', counts: counts({ draft: 1 }), needs_attention: 1 }],
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
  it('DASH-T8: hyphens become spaces, each word Title-Cased', () => {
    expect(deslug('supplier-tin-format')).toBe('Supplier Tin Format')
  })

  it('DASH-T9: underscores become spaces, each word Title-Cased', () => {
    expect(deslug('vat_standard_rate')).toBe('Vat Standard Rate')
  })

  it('DASH-T10: an already-spaced/capitalized key passes through unchanged', () => {
    expect(deslug('Already Clean')).toBe('Already Clean')
  })

  it('DASH-T11: a single lowercase word Title-Cases; an empty string stays empty', () => {
    expect(deslug('single')).toBe('Single')
    expect(deslug('')).toBe('')
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
      { label: 'Supplier Tin Format', ruleKey: 'supplier-tin-format', count: 3, bar: '100%' },
      { label: 'Vat Standard Rate', ruleKey: 'vat-standard-rate', count: 1, bar: '33%' },
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
    const r: Rollup = { totals: { counts: counts(), needs_attention: 0 }, clients: [], top_violations: [] }

    expect(isEmptyRollup(r)).toBe(true)
  })

  it('DASH-T18: any non-zero total count is not empty', () => {
    const r: Rollup = { totals: { counts: counts({ draft: 1 }), needs_attention: 0 }, clients: [], top_violations: [] }

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
  const clientA: RollupClient = { entity_id: 'A', entity_name: 'Acme', counts: counts(), needs_attention: 0 }

  it('DASH-T21: an entity absent from clients reads no-invoices', () => {
    expect(entityHealth([clientA], 'Z')).toEqual({ kind: 'no-invoices' })
  })

  it('DASH-T22: an entity present with needs_attention:2 reads needs-attention with that count', () => {
    const clientZ: RollupClient = { entity_id: 'Z', entity_name: 'Zeta', counts: counts(), needs_attention: 2 }

    expect(entityHealth([clientA, clientZ], 'Z')).toEqual({ kind: 'needs-attention', count: 2 })
  })

  it('DASH-T23: an entity present with needs_attention:0 reads clear', () => {
    const clientZ: RollupClient = { entity_id: 'Z', entity_name: 'Zeta', counts: counts(), needs_attention: 0 }

    expect(entityHealth([clientA, clientZ], 'Z')).toEqual({ kind: 'clear' })
  })
})
