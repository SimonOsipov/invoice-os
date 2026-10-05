// @vitest-environment jsdom
// QA Mode B adversarial coverage (task-332, BUG-01-06, AC-5) -- nothing rendered this
// component before, so the Overview chip's relabel (DashboardActive.tsx:259) was
// untested in both directions. Mirrors InvoiceDetail.test.tsx's fetch-mock + ctx-cast
// idiom (single-endpoint mock: DashboardActive fires only getRollup, unlike
// InvoiceDetail's two concurrent effects).
import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { Counts, Metrics, Rollup } from '../lib/dashboard'
import type { PlatformCtx } from '../types'
import { DashboardActive } from './DashboardActive'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

const ZERO_COUNTS: Counts = { draft: 0, validated: 0, queued: 0, submitted: 0, accepted: 0, rejected: 0, failed: 0 }

function rollup(
  needsAttention: number,
  countsOver: Partial<Counts> = {},
  metricsOver: Metrics = {},
  awaitingApproval = 0,
): Rollup {
  const counts: Counts = { ...ZERO_COUNTS, ...countsOver }
  return {
    totals: { counts, needs_attention: needsAttention, awaiting_approval: awaitingApproval, metrics: metricsOver, top_violations: [] },
    clients: [],
    top_violations: [],
  }
}

// mode: 'inhouse' resolves scopedBucket straight to rollup.totals (dashboard.ts) --
// sidesteps needing a matching `clients` row, irrelevant to the chip label/count.
function dashCtx(): PlatformCtx {
  const ctx = {
    mode: 'inhouse',
    active: {},
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    nav: () => {},
  }
  return ctx as unknown as PlatformCtx
}

function mockRollupFetch(data: Rollup) {
  const fetchMock = vi.fn(() => Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(data) }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('DashboardActive needs-attention chip (task-332, BUG-01-06, [overview-chip-relabelled-here])', () => {
  it('AC-5: needs_attention > 0 reads the relabelled REJECTED / FAILED / BLOCKED / SENT BACK chip', async () => {
    mockRollupFetch(rollup(3, { rejected: 1, failed: 1 }))

    render(<DashboardActive ctx={dashCtx()} />)

    expect(await screen.findByText('REJECTED / FAILED / BLOCKED / SENT BACK')).toBeDefined()
    expect(screen.queryByText('REJECTED / FAILED')).toBeNull()
    // The un-widened string must be gone, not merely a prefix of the widened one.
    expect(screen.queryByText('REJECTED / FAILED / BLOCKED')).toBeNull()
  })

  it('needs_attention === 0 still reads ALL CLEAR -- the relabel only touches the non-zero string', async () => {
    mockRollupFetch(rollup(0))

    render(<DashboardActive ctx={dashCtx()} />)

    expect(await screen.findByText('ALL CLEAR')).toBeDefined()
    expect(screen.queryByText(/REJECTED/)).toBeNull()
    expect(screen.queryByText(/SENT BACK/)).toBeNull()
  })

  it('the explanatory sentence names all four causes', async () => {
    mockRollupFetch(rollup(1, { rejected: 1 }))

    render(<DashboardActive ctx={dashCtx()} />)

    expect(
      await screen.findByText(
        'Invoices rejected, failed, blocked by an error-severity validation issue, or sent back by an approver.',
      ),
    ).toBeDefined()
  })

  // The single highest-risk case in this subtask: the story's Core AC says
  // needs_attention's COUNT is correct today and must not change. bucket.needs_attention
  // (3) here intentionally disagrees with BOTH counts.rejected + counts.failed (0 + 0 =
  // 0) and the total/awaiting KPIs (7, from draft alone) -- the shape a blocked-draft
  // contribution takes (rejected/failed stay 0, only the needs_attention overlay carries
  // the 3). A regression that re-derives the number from rejected+failed would render 0;
  // one that reused total/awaiting would render 7. getAllByText pins the count to
  // EXACTLY the two legitimate render sites that read bucket.needs_attention verbatim
  // (the big panel number and the "Exceptions" KPI tile) -- see DashboardActive.tsx
  // kpiValues, which threads the same `needsAttention` param into both.
  // awaiting_approval is 5 so the fourth tile's delta ("5 awaiting approval") can neither
  // equal the asserted 3 nor collide with any other bare number on the page.
  it('AC-5: the displayed count is bucket.needs_attention passed through untransformed, not re-derived from counts', async () => {
    mockRollupFetch(rollup(3, { rejected: 0, failed: 0, draft: 7 }, {}, 5))

    render(<DashboardActive ctx={dashCtx()} />)

    // Settle on a static tile title -- the pill copy is itself under test in this story.
    await screen.findByText('Readiness score')
    // Exactly the two legitimate render sites read bucket.needs_attention verbatim; a
    // regression deriving from rejected+failed (0) or total/awaiting (7) would break
    // this count, not just add a stray "3" -- '0' and '7' both appear elsewhere on the
    // page (donut zero-segments, other KPI tiles) so they're not usable as negative
    // assertions here.
    expect(screen.getAllByText('3')).toHaveLength(2)
  })
})

// mode: 'firm' with a selected client -- scopedBucket resolves to that client's `clients`
// row instead of rollup.totals, so live panels must read bucket.*, never data.* directly.
function firmCtx(entityId: string, name: string): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId, name },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    nav: () => {},
  }
  return ctx as unknown as PlatformCtx
}

describe('DashboardActive live panels (task-429, METR-01-05)', () => {
  it('(a) the Readiness tile drops its SAMPLE chip; trend and activity panels keep theirs', async () => {
    mockRollupFetch(rollup(0))

    render(<DashboardActive ctx={dashCtx()} />)

    const readinessHead = (await screen.findByText('Readiness score')).parentElement
    expect(within(readinessHead!).queryByText('SAMPLE')).toBeNull()
    expect(screen.getByText('12 WEEKS · SAMPLE')).toBeDefined()
    const activityHead = screen.getByText('Recent activity').parentElement
    expect(within(activityHead!).getByText('SAMPLE')).toBeDefined()
  })

  it('(b) Top validation failures drops its FIRM-WIDE chip', async () => {
    mockRollupFetch(rollup(0))

    render(<DashboardActive ctx={dashCtx()} />)

    const failuresHead = (await screen.findByText('Top validation failures')).parentElement
    expect(within(failuresHead!).queryByText('FIRM-WIDE')).toBeNull()
  })

  it('(c) the live score renders from metrics.readiness', async () => {
    mockRollupFetch(rollup(0, {}, { readiness: { num: 85, den: 100 } }))

    render(<DashboardActive ctx={dashCtx()} />)

    expect(await screen.findByText('85')).toBeDefined()
  })

  it('(d) a client with zero invoices renders the em-dash and "No invoices yet", never 0%', async () => {
    mockRollupFetch(rollup(0)) // metrics: {} -- the empty-client signal, not a zero score

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Readiness score') // wait for the ready state to settle
    // Scoped to the Readiness tile itself: the trend tile shows the same "No invoices
    // yet" copy under a null live score, and donutSegments legitimately renders '0%' for
    // all seven canonical states when the invoice-status donut has no invoices, so
    // document-wide queries would be false positives here, not real assertions.
    const readinessTile = screen.getByText('Readiness score').parentElement!.parentElement!
    expect(within(readinessTile).getByText('No invoices yet')).toBeDefined()
    expect(within(readinessTile).getAllByText('—').length).toBeGreaterThan(0)
    expect(within(readinessTile).queryByText('0%')).toBeNull()
  })

  it('(e) AC-7: a firm-mode client renders its OWN top_violations, not the tenant-wide list', async () => {
    const data: Rollup = {
      totals: { counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] },
      clients: [
        {
          entity_id: 'ent-1',
          entity_name: 'Dangote Cement PLC',
          counts: ZERO_COUNTS,
          needs_attention: 0,
          awaiting_approval: 0,
          metrics: {},
          top_violations: [{ rule_key: 'client-only-rule', invoices: 4 }],
        },
      ],
      top_violations: [{ rule_key: 'tenant-wide-rule', invoices: 10 }],
    }
    mockRollupFetch(data)

    render(<DashboardActive ctx={firmCtx('ent-1', 'Dangote Cement PLC')} />)

    expect(await screen.findByText('client-only-rule')).toBeDefined()
    expect(screen.queryByText('tenant-wide-rule')).toBeNull()
  })
})

// QA Mode B adversarial coverage. A mutation pass (bucket.metrics -> data.totals.metrics
// on the ring/note/bars/VAT call sites) left every test above green, because they're all
// inhouse-mode where scopedBucket resolves straight to rollup.totals -- same trap AC-7
// already called out for top_violations, but the executor's tests didn't extend it to
// metrics. These fixtures make the two buckets diverge to close that gap.
describe('DashboardActive live panels — adversarial (QA task-429)', () => {
  it('(f) firm-mode client renders its OWN readiness score, note, bar and VAT figure, never the tenant rollup totals', async () => {
    const data: Rollup = {
      totals: {
        counts: ZERO_COUNTS,
        needs_attention: 0,
        awaiting_approval: 0,
        metrics: {
          readiness: { num: 40, den: 100 },
          blocked_by_rules: { num: 9, den: 0 },
          bar_field_completeness: { num: 20, den: 100 },
          vat_tracked: { num: 50000000, den: 0 },
        },
        top_violations: [],
      },
      clients: [
        {
          entity_id: 'ent-1',
          entity_name: 'Dangote Cement PLC',
          counts: ZERO_COUNTS,
          needs_attention: 0,
          awaiting_approval: 0,
          metrics: {
            readiness: { num: 92, den: 100 },
            bar_field_completeness: { num: 88, den: 100 },
            vat_tracked: { num: 12340000, den: 0 },
          },
          top_violations: [],
        },
      ],
      top_violations: [],
    }
    mockRollupFetch(data)

    render(<DashboardActive ctx={firmCtx('ent-1', 'Dangote Cement PLC')} />)

    expect(await screen.findByText('92')).toBeDefined()
    expect(screen.getByText('88%')).toBeDefined()
    expect(screen.getByText('₦123k')).toBeDefined()
    expect(screen.getByText('All invoices checked and clear of blocking rules.')).toBeDefined()
    expect(screen.queryByText('40')).toBeNull()
    expect(screen.queryByText('20%')).toBeNull()
    expect(screen.queryByText('₦500k')).toBeNull()
    expect(screen.queryByText(/9 blocked by rules/)).toBeNull()
  })

  it('(g) readiness ring colours >=85 as var(--action) at the exact boundary', async () => {
    mockRollupFetch(rollup(0, {}, { readiness: { num: 85, den: 100 } }))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('85')
    const tile = screen.getByText('Readiness score').parentElement!.parentElement!
    const circles = tile.querySelectorAll('circle')
    expect(circles[1].getAttribute('stroke')).toBe('var(--action)')
  })

  it('(h) readiness ring colours the 70-84 band as var(--status-amber-text) at the exact boundary', async () => {
    mockRollupFetch(rollup(0, {}, { readiness: { num: 70, den: 100 } }))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('70')
    const tile = screen.getByText('Readiness score').parentElement!.parentElement!
    const circles = tile.querySelectorAll('circle')
    expect(circles[1].getAttribute('stroke')).toBe('var(--status-amber-text)')
  })

  it('(i) firm-mode client with zero metrics still resolves via the matched clients row, not EMPTY_BUCKET', async () => {
    const data: Rollup = {
      totals: { counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] },
      clients: [
        { entity_id: 'ent-2', entity_name: 'Zenith Traders', counts: { ...ZERO_COUNTS, draft: 5, validated: 3, submitted: 2 }, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] },
      ],
      top_violations: [],
    }
    mockRollupFetch(data)

    render(<DashboardActive ctx={firmCtx('ent-2', 'Zenith Traders')} />)

    // metrics: {} on the matched row reads the same empty-state copy as EMPTY_BUCKET, so
    // the counts (only present on a real clients row, never on EMPTY_BUCKET) are the proof
    // scopedBucket found the row rather than falling through. The invoice-status donut
    // legitimately repeats the same total, and the trend tile shows the same "No invoices
    // yet" copy under a null live score, so scope each query to its own tile.
    await screen.findByText('Readiness score') // wait for the ready state to settle
    const readinessTile = screen.getByText('Readiness score').parentElement!.parentElement!
    expect(within(readinessTile).getByText('No invoices yet')).toBeDefined()
    const invoicesTile = screen.getByText('Invoices').parentElement!.parentElement!
    expect(within(invoicesTile).getByText('10')).toBeDefined() // 5 draft + 3 validated + 2 submitted
    const awaitingTile = screen.getByText('Not yet submitted').parentElement!.parentElement!
    expect(within(awaitingTile).getByText('8')).toBeDefined() // draft + validated, both non-zero and distinct
  })

  it('(j) firm-mode entityId with no matching clients row falls through to EMPTY_BUCKET and renders the empty state, not a crash', async () => {
    const data: Rollup = {
      totals: {
        counts: { ...ZERO_COUNTS, draft: 99 },
        needs_attention: 0,
        awaiting_approval: 0,
        metrics: { readiness: { num: 40, den: 100 } },
        top_violations: [{ rule_key: 'tenant-rule', invoices: 5 }],
      },
      clients: [],
      top_violations: [{ rule_key: 'tenant-rule', invoices: 5 }],
    }
    mockRollupFetch(data)

    render(<DashboardActive ctx={firmCtx('ghost-entity', 'Ghost Co')} />)

    await screen.findByText('Readiness score') // wait for the ready state to settle
    // Scoped to the Readiness tile: the trend tile shows the same "No invoices yet"
    // copy under a null live score.
    const readinessTile = screen.getByText('Readiness score').parentElement!.parentElement!
    expect(within(readinessTile).getByText('No invoices yet')).toBeDefined()
    expect(screen.getByText('No invoices validated yet')).toBeDefined() // EMPTY_BUCKET has zero invoices
    expect(screen.queryByText('99')).toBeNull()
    expect(screen.queryByText('tenant-rule')).toBeNull()
  })

  it('(k) firm-mode client with empty top_violations renders "No open failures", not the tenant-wide list', async () => {
    const data: Rollup = {
      totals: { counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [{ rule_key: 'tenant-wide-rule', invoices: 10 }] },
      clients: [
        { entity_id: 'ent-3', entity_name: 'Clean Client Ltd', counts: { ...ZERO_COUNTS, validated: 4 }, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] },
      ],
      top_violations: [{ rule_key: 'tenant-wide-rule', invoices: 10 }],
    }
    mockRollupFetch(data)

    render(<DashboardActive ctx={firmCtx('ent-3', 'Clean Client Ltd')} />)

    expect(await screen.findByText('No open failures')).toBeDefined()
    expect(screen.queryByText('tenant-wide-rule')).toBeNull()
  })
})

describe('DashboardActive trend re-anchor (task-430, METR-01-06)', () => {
  it('the readiness ring and the trend headline show the same live score', async () => {
    // 67 sits below buildMockPanels' fabricated range (72-95) for any seed, so a
    // not-yet-anchored trend is guaranteed to show a different number here.
    mockRollupFetch(rollup(0, {}, { readiness: { num: 67, den: 100 } }))

    render(<DashboardActive ctx={dashCtx()} />)

    const ringTile = (await screen.findByText('Readiness score')).parentElement!.parentElement!
    expect(within(ringTile).getByText('67')).toBeDefined()
    const trendTile = screen.getByText('Readiness trend').parentElement!.parentElement!
    expect(within(trendTile).getByText('67%')).toBeDefined()
  })

  it('null live score renders the trend empty state, no fabricated curve, chip stays', async () => {
    mockRollupFetch(rollup(0)) // metrics: {} -- no invoices, no live score

    render(<DashboardActive ctx={dashCtx()} />)

    const trendTile = (await screen.findByText('Readiness trend')).parentElement!.parentElement!
    expect(trendTile.querySelectorAll('svg').length).toBe(0)
    expect(within(trendTile).getByText('—')).toBeDefined()
    expect(within(trendTile).getByText('No invoices yet')).toBeDefined()
    expect(within(trendTile).getByText('12 WEEKS · SAMPLE')).toBeDefined()
  })
})

// QA Mode B adversarial coverage (task-430, METR-01-06). MTR-01..06 in dashboardMock.test.ts
// prove buildMockPanels is correct in isolation; these prove the RENDERED page never lets
// the ring and the trend headline drift apart, at the boundary scores and across a client
// switch, which is the actual product guarantee the story cares about.
describe('DashboardActive trend re-anchor — adversarial (QA task-430)', () => {
  it.each([0, 45, 100])('the ring and the trend headline show the same live value at score %i', async (score) => {
    mockRollupFetch(rollup(0, {}, { readiness: { num: score, den: 100 } }))

    render(<DashboardActive ctx={dashCtx()} />)

    const ringTile = (await screen.findByText('Readiness score')).parentElement!.parentElement!
    expect(within(ringTile).getByText(String(score))).toBeDefined()
    const trendTile = screen.getByText('Readiness trend').parentElement!.parentElement!
    expect(within(trendTile).getByText(`${score}%`)).toBeDefined()
    // The SAMPLE chip stays even once the headline goes live -- only the endpoint is
    // real, the 12-week series behind it is still fabricated.
    expect(within(trendTile).getByText('12 WEEKS · SAMPLE')).toBeDefined()
  })

  // chartScore's last point is pinned to finalScore verbatim (dashboardMock.ts) and its
  // y-coordinate is `168 - 1.5 * finalScore` (ch=176, top pad 8, plot height 150) --
  // asserting the RENDERED <path d> ending, not just chart.now, catches a regression that
  // anchors the headline number but leaves the curve itself still ending on the old
  // fabricated score.
  it('score 100 draws a curve whose rendered SVG path actually ends at the live score', async () => {
    mockRollupFetch(rollup(0, {}, { readiness: { num: 100, den: 100 } }))

    render(<DashboardActive ctx={dashCtx()} />)

    const trendTile = (await screen.findByText('Readiness trend')).parentElement!.parentElement!
    const linePath = trendTile.querySelector('path[stroke="var(--action)"]')
    expect(linePath?.getAttribute('d')?.endsWith(' L 680.0 18.0')).toBe(true)
  })

  it('score 0 draws a curve whose rendered SVG path actually ends at the live score -- 0 is a score, not an absence', async () => {
    mockRollupFetch(rollup(0, {}, { readiness: { num: 0, den: 100 } }))

    render(<DashboardActive ctx={dashCtx()} />)

    const trendTile = (await screen.findByText('Readiness trend')).parentElement!.parentElement!
    const linePath = trendTile.querySelector('path[stroke="var(--action)"]')
    expect(linePath?.getAttribute('d')?.endsWith(' L 680.0 168.0')).toBe(true)
  })

  it('switching the selected client updates BOTH the ring and the trend together, with no stale value left behind', async () => {
    const data: Rollup = {
      totals: { counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] },
      clients: [
        { entity_id: 'ent-a', entity_name: 'Client A', counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: { readiness: { num: 30, den: 100 } }, top_violations: [] },
        { entity_id: 'ent-b', entity_name: 'Client B', counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: { readiness: { num: 95, den: 100 } }, top_violations: [] },
      ],
      top_violations: [],
    }
    mockRollupFetch(data)

    const { rerender } = render(<DashboardActive ctx={firmCtx('ent-a', 'Client A')} />)

    const ringTileA = (await screen.findByText('Readiness score')).parentElement!.parentElement!
    expect(within(ringTileA).getByText('30')).toBeDefined()
    const trendTileA = screen.getByText('Readiness trend').parentElement!.parentElement!
    expect(within(trendTileA).getByText('30%')).toBeDefined()

    rerender(<DashboardActive ctx={firmCtx('ent-b', 'Client B')} />)

    const ringTileB = screen.getByText('Readiness score').parentElement!.parentElement!
    expect(within(ringTileB).getByText('95')).toBeDefined()
    expect(within(ringTileB).queryByText('30')).toBeNull()
    const trendTileB = screen.getByText('Readiness trend').parentElement!.parentElement!
    expect(within(trendTileB).getByText('95%')).toBeDefined()
    expect(within(trendTileB).queryByText('30%')).toBeNull()
  })
})

// A KPI tile's root is the TileHead's grandparent; its delta is the only .mono inside,
// because KPI heads carry no meta.
function kpiTile(label: string): HTMLElement {
  return screen.getByText(label).parentElement!.parentElement!
}
function kpiDeltaEl(label: string): HTMLElement {
  return kpiTile(label).querySelector('span.mono') as HTMLElement
}
function kpiDelta(label: string): string {
  return kpiDeltaEl(label).textContent!
}

describe('DashboardActive KPI tiles say what they count', () => {
  it('the not-yet-submitted tile is draft + validated, with both addends non-zero and distinct', async () => {
    mockRollupFetch(rollup(0, { draft: 5, validated: 3, submitted: 2 }))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Not yet submitted')
    expect(within(kpiTile('Not yet submitted')).getByText('8')).toBeDefined()
  })

  it('the not-yet-submitted delta names the awaiting-approval subset', async () => {
    mockRollupFetch(rollup(0, { draft: 5, validated: 3 }, {}, 3))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Not yet submitted')
    expect(kpiDelta('Not yet submitted')).toBe('3 awaiting approval')

    cleanup()
    mockRollupFetch(rollup(0))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Not yet submitted')
    expect(kpiDelta('Not yet submitted')).toBe('none waiting')
  })

  it('the two value-derived deltas keep both legs and the two constant deltas are untouched', async () => {
    mockRollupFetch(rollup(0))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Exceptions')
    expect(kpiDelta('Exceptions')).toBe('all clear')
    expect(kpiDelta('Not yet submitted')).toBe('none waiting')
    expect(kpiDelta('Invoices')).toMatch(/^\d+ transmitted$/)
    expect(kpiDelta('VAT tracked')).toBe('output VAT')

    cleanup()
    mockRollupFetch(rollup(2, { draft: 4, validated: 1, submitted: 6, accepted: 2 }, {}, 3))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Exceptions')
    expect(kpiDelta('Exceptions')).toBe('to resolve')
    expect(kpiDelta('Not yet submitted')).toBe('3 awaiting approval')
    expect(kpiDelta('Invoices')).toMatch(/^\d+ transmitted$/)
    expect(kpiDelta('VAT tracked')).toBe('output VAT')
  })

  it('the KPI row still renders exactly four tiles', async () => {
    mockRollupFetch(rollup(0))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Readiness score')
    expect(document.querySelector('.pf-dash-row-a .pf-grid-2')!.children).toHaveLength(4)
  })

  // The tile sums Object.values(bucket.counts); donutSegments sums CANONICAL_STATES.
  // Nothing else ties those two -- this is the tripwire for the next count key added.
  it('the donut total, the ring centre and the sum of the seven legend counts all agree', async () => {
    // Every state distinct and non-zero, so no coincidental collision can hide a
    // mis-summed key. 1+2+3+4+5+6+7 = 28.
    const counts: Counts = { draft: 1, validated: 2, queued: 3, submitted: 4, accepted: 5, rejected: 6, failed: 7 }
    mockRollupFetch(rollup(0, counts))

    render(<DashboardActive ctx={dashCtx()} />)

    const donut = (await screen.findByText('Invoice status')).parentElement!.parentElement!
    const head = Number(screen.getByText('Invoice status').nextElementSibling!.textContent!.replace(' TOTAL', ''))
    const centre = Number(within(donut).getByText('DOCS').previousElementSibling!.textContent)
    const legend = ['Draft', 'Validated', 'Queued', 'Submitted', 'Accepted', 'Rejected', 'Failed'].reduce(
      (sum, label) => sum + Number(within(donut).getByText(label).parentElement!.lastElementChild!.textContent),
      0,
    )

    expect(Object.values(counts).reduce((a, b) => a + b, 0)).toBe(28)
    expect(head).toBe(28)
    expect(centre).toBe(28)
    expect(legend).toBe(28)
  })
})

// QA Mode B adversarial coverage. A mutation pass over the re-labelled tiles left three
// holes the tests above do not reach: bucket.awaiting_approval could be read off
// data.totals with every assertion still green (the same firm-mode scope trap the
// adversarial metrics block already calls out), and both colour branches AC-2/AC-4
// pin as unchanged had no oracle at all.
describe('DashboardActive KPI tiles — adversarial (QA)', () => {
  // Inside the donut tile, span.money is [ring centre, ...legend counts]; span.mono is
  // [head meta, DOCS, ...legend pcts]. Reading them structurally rather than by a fixed
  // label list means an eighth CANONICAL state is summed too, not silently skipped.
  function donutTile(): HTMLElement {
    return screen.getByText('Invoice status').parentElement!.parentElement!
  }
  function donutNumbers(): { head: string; centre: number; legend: number[]; pcts: string[] } {
    const tile = donutTile()
    const money = Array.from(tile.querySelectorAll('span.money')).map((e) => Number(e.textContent))
    const mono = Array.from(tile.querySelectorAll('span.mono')).map((e) => e.textContent!)
    return { head: mono[0], centre: money[0], legend: money.slice(1), pcts: mono.slice(2) }
  }

  it('an all-zero rollup reads every zero leg, and the donut shows 0 rather than dividing by it', async () => {
    mockRollupFetch(rollup(0))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Exceptions')
    expect(within(kpiTile('Exceptions')).getByText('0')).toBeDefined()
    expect(kpiDelta('Exceptions')).toBe('all clear')
    expect(within(kpiTile('Not yet submitted')).getByText('0')).toBeDefined()
    expect(kpiDelta('Not yet submitted')).toBe('none waiting')
    expect(screen.getByText('ALL CLEAR')).toBeDefined()

    // donutSegments' `|| 1` denominator must not let the head and the percentages
    // disagree: every share is a true 0%, and no cell renders NaN.
    const { head, centre, legend, pcts } = donutNumbers()
    expect(head).toBe('0 TOTAL')
    expect(centre).toBe(0)
    expect(legend).toEqual([0, 0, 0, 0, 0, 0, 0])
    expect(pcts).toEqual(['0%', '0%', '0%', '0%', '0%', '0%', '0%'])
    expect(donutTile().textContent).not.toMatch(/NaN|Infinity/)
  })

  it('firm mode scopes the awaiting-approval delta and the exceptions tile to the client row, never the tenant totals', async () => {
    const data: Rollup = {
      totals: {
        counts: { ...ZERO_COUNTS, draft: 40, validated: 20 },
        needs_attention: 42,
        awaiting_approval: 99,
        metrics: {},
        top_violations: [],
      },
      clients: [
        {
          entity_id: 'ent-9',
          entity_name: 'Scoped Ltd',
          counts: { ...ZERO_COUNTS, draft: 2, validated: 1 },
          needs_attention: 4,
          awaiting_approval: 1,
          metrics: {},
          top_violations: [],
        },
      ],
      top_violations: [],
    }
    mockRollupFetch(data)

    render(<DashboardActive ctx={firmCtx('ent-9', 'Scoped Ltd')} />)

    await screen.findByText('Not yet submitted')
    expect(within(kpiTile('Not yet submitted')).getByText('3')).toBeDefined()
    expect(kpiDelta('Not yet submitted')).toBe('1 awaiting approval')
    expect(within(kpiTile('Exceptions')).getByText('4')).toBeDefined()
    expect(screen.queryByText('99 awaiting approval')).toBeNull()
    expect(screen.queryByText('60')).toBeNull()
    expect(screen.queryByText('42')).toBeNull()
  })

  it('awaiting_approval equal to the tile value reads the whole population as waiting', async () => {
    // The ceiling case, and the only one: the rollup counts awaiting_approval with
    // FILTER (WHERE i.status = 'validated' AND ...) over the same group as `validated`
    // (internal/dashboard/store.go), so it is bounded by validated, itself bounded by
    // draft + validated. A delta larger than its own tile's value cannot reach the wire.
    mockRollupFetch(rollup(0, { validated: 4 }, {}, 4))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Not yet submitted')
    expect(within(kpiTile('Not yet submitted')).getByText('4')).toBeDefined()
    expect(kpiDelta('Not yet submitted')).toBe('4 awaiting approval')
  })

  it('a non-zero tile with nothing awaiting still names the subset rather than falling back to the zero leg', async () => {
    mockRollupFetch(rollup(0, { draft: 5 }, {}, 0))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Not yet submitted')
    expect(kpiDelta('Not yet submitted')).toBe('0 awaiting approval')
  })

  it('the two re-labelled tiles and the pill keep their original colour branches', async () => {
    mockRollupFetch(rollup(0))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Exceptions')
    expect(kpiDeltaEl('Exceptions').style.color).toBe('var(--status-green-text)')
    expect(kpiDeltaEl('Not yet submitted').style.color).toBe('var(--fg-3)')
    const clearPill = screen.getByText('ALL CLEAR')
    expect(clearPill.style.color).toBe('var(--status-green-text)')
    expect(clearPill.getAttribute('style')).toContain('background: var(--status-green-bg)')
    expect(clearPill.getAttribute('style')).toContain('border: 1px solid var(--status-green-border)')

    cleanup()
    mockRollupFetch(rollup(2, { draft: 4, validated: 1 }, {}, 1))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Exceptions')
    expect(kpiDeltaEl('Exceptions').style.color).toBe('var(--status-red-text)')
    expect(kpiDeltaEl('Not yet submitted').style.color).toBe('var(--status-amber-text)')
    const alertPill = screen.getByText('REJECTED / FAILED / BLOCKED / SENT BACK')
    expect(alertPill.style.color).toBe('var(--status-red-text)')
    expect(alertPill.getAttribute('style')).toContain('background: var(--status-red-bg)')
    expect(alertPill.getAttribute('style')).toContain('border: 1px solid var(--status-red-border)')
  })

  it('the retired tile labels and deltas render nowhere', async () => {
    mockRollupFetch(rollup(2, { draft: 4, validated: 1 }, {}, 1))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Exceptions')
    expect(screen.queryByText('Failing invoices')).toBeNull()
    expect(screen.queryByText('Awaiting submission')).toBeNull()
    expect(screen.queryByText('needs fixing')).toBeNull()
    expect(screen.queryByText('not yet sent')).toBeNull()
  })

  it('the donut tripwire sums every rendered legend row, so an added canonical state is counted too', async () => {
    const spread: Counts = { draft: 1, validated: 2, queued: 3, submitted: 4, accepted: 5, rejected: 6, failed: 7 }
    mockRollupFetch(rollup(0, spread))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Invoice status')
    const { head, centre, legend } = donutNumbers()
    // One row per canonical state, so an empty legend can never sum to a vacuous pass.
    expect(legend).toHaveLength(7)
    expect(head).toBe('28 TOTAL')
    expect(centre).toBe(28)
    expect(legend.reduce((a, b) => a + b, 0)).toBe(28)
  })
})

describe('DashboardActive failures panel before any invoice is validated (AUTH-10-04)', () => {
  const failuresPanel = (title: string) => screen.getByText(title).parentElement as HTMLElement

  it('zero invoices is not "every invoice passed"', async () => {
    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Readiness score') // settle before asserting the panel
    expect(screen.getByText('No invoices validated yet')).toBeDefined()
    expect(screen.getByText('Failures appear here once invoices are validated.')).toBeDefined()
    expect(screen.queryByText('Every invoice passed validation.')).toBeNull()
    expect(screen.queryByText('No open failures')).toBeNull()
    expect(failuresPanel('No invoices validated yet').querySelector('svg'), 'no tick at zero invoices').toBeNull()
  })

  // Regression guard: green before and after the change.
  it('invoices with no failures still say No open failures', async () => {
    mockRollupFetch(rollup(0, { validated: 3 }))
    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Readiness score')
    expect(screen.getByText('No open failures')).toBeDefined()
    expect(screen.getByText('Every invoice passed validation.')).toBeDefined()
    expect(screen.queryByText('No invoices validated yet')).toBeNull()
    expect(failuresPanel('No open failures').querySelector('svg'), 'the tick stays when an invoice is validated').not.toBeNull()
  })

  it('drafts only: nothing validated yet, no tick', async () => {
    mockRollupFetch(rollup(0, { draft: 3 }))
    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Readiness score')
    expect(screen.getByText('No invoices validated yet')).toBeDefined()
    expect(screen.getByText('Failures appear here once invoices are validated.')).toBeDefined()
    expect(screen.queryByText('Every invoice passed validation.')).toBeNull()
    expect(screen.queryByText('No open failures')).toBeNull()
    expect(failuresPanel('No invoices validated yet').querySelector('svg'), 'no tick for drafts').toBeNull()
  })

  // One validated invoice is the boundary: `> 1` would flip it back to the empty-state copy.
  it('one validated invoice and no failures says No open failures, with its tick', async () => {
    mockRollupFetch(rollup(0, { validated: 1 }))
    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Readiness score')
    expect(screen.getByText('No open failures')).toBeDefined()
    expect(screen.queryByText('No invoices validated yet')).toBeNull()
    expect(failuresPanel('No open failures').querySelector('svg')).not.toBeNull()
  })

  it('drafts plus one validated invoice says No open failures, with its tick', async () => {
    mockRollupFetch(rollup(0, { draft: 3, validated: 1 }))
    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Readiness score')
    expect(screen.getByText('No open failures')).toBeDefined()
    expect(screen.getByText('Every invoice passed validation.')).toBeDefined()
    expect(screen.queryByText('No invoices validated yet')).toBeNull()
    expect(failuresPanel('No open failures').querySelector('svg')).not.toBeNull()
  })

  it('firm mode reads the selected client counts: drafts-only client under a validated tenant', async () => {
    const data = rollup(0, { validated: 9 })
    data.clients = [
      {
        entity_id: 'ent-1',
        entity_name: 'Dangote Cement PLC',
        counts: { ...ZERO_COUNTS, draft: 2 },
        needs_attention: 0,
        awaiting_approval: 0,
        metrics: {},
        top_violations: [],
      },
    ]
    mockRollupFetch(data)
    render(<DashboardActive ctx={firmCtx('ent-1', 'Dangote Cement PLC')} />)

    await screen.findByText('Readiness score')
    expect(screen.getByText('No invoices validated yet')).toBeDefined()
    expect(screen.queryByText('No open failures')).toBeNull()
  })

  it.each([
    ['drafts only', { draft: 3 }],
    ['drafts and one rejected', { draft: 3, rejected: 1 }],
    ['drafts and one failed', { draft: 3, failed: 1 }],
    ['no invoices', {}],
    ['validated only', { validated: 5 }],
  ] as [string, Partial<Counts>][])('with failures listed (%s) neither empty-state copy renders', async (_name, countsOver) => {
    const data = rollup(0, countsOver)
    data.totals.top_violations = [{ rule_key: 'tin-checksum', invoices: 2 }]
    mockRollupFetch(data)
    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('Readiness score')
    expect(screen.getByText('tin-checksum')).toBeDefined()
    expect(screen.queryByText('No open failures')).toBeNull()
    expect(screen.queryByText('No invoices validated yet')).toBeNull()
    expect(screen.queryByText('Failures appear here once invoices are validated.')).toBeNull()
  })
})

describe('DashboardActive Recent activity (AUTH-10-07, Core AC-7)', () => {
  const handoffCtx = () => ({ ...dashCtx(), handoff: true }) as unknown as PlatformCtx
  const personaCtx = () => ({ ...dashCtx(), handoff: false }) as unknown as PlatformCtx

  it('hand-off: Recent activity is empty, not a sample', async () => {
    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={handoffCtx()} />)

    await screen.findByText('Readiness score')
    const head = screen.getByText('Recent activity').parentElement as HTMLElement
    expect(screen.getByText('No activity to show')).toBeDefined()
    expect(within(head).queryByText('SAMPLE'), 'no SAMPLE badge on the activity head').toBeNull()
    expect(screen.queryByText('INV-2026-00481')).toBeNull()
  })

  it('hand-off: Recent activity stays empty with invoices present, and the tile holds only its title and the empty line', async () => {
    mockRollupFetch(rollup(0, { validated: 5 }))
    render(<DashboardActive ctx={handoffCtx()} />)

    await screen.findByText('Readiness score')
    const tile = screen.getByText('Recent activity').parentElement!.parentElement as HTMLElement
    expect(within(tile).getByText('No activity to show')).toBeDefined()
    expect(tile.textContent).toBe('Recent activityNo activity to show')
  })

  // Control: green before and after.
  it('persona: Recent activity keeps the SAMPLE feed', async () => {
    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={personaCtx()} />)

    await screen.findByText('Readiness score')
    const head = screen.getByText('Recent activity').parentElement as HTMLElement
    expect(within(head).getByText('SAMPLE')).toBeDefined()
    expect(screen.getByText('INV-2026-00481')).toBeDefined()
    expect(screen.queryByText('No activity to show')).toBeNull()
  })
})

// inline-token reads only; jsdom has no cascade, so resolved values are OV-01..OV-04's.
const cardOf = (title: string) => screen.getByText(title).parentElement!.parentElement as HTMLElement
const headOf = (title: string) => screen.getByText(title).parentElement as HTMLElement
const settle = () => screen.findByText('Readiness score')
const TILE_TITLES = [
  'Readiness score',
  'Invoices',
  'VAT tracked',
  'Exceptions',
  'Not yet submitted',
  'Needs attention',
  'Invoice status',
  'Readiness trend',
  'Top validation failures',
  'Recent activity',
]
const PILL_GONE = /radius-pill|border-radius:\s*(99|999)px/

describe('DashboardActive DA-LOOK (D-29)', () => {
  it('each of the 7 legend swatches is a 2px square at 10x10', async () => {
    mockRollupFetch(rollup(0, { draft: 1, validated: 2 }))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    const rows = ['Draft', 'Validated', 'Queued', 'Submitted', 'Accepted', 'Rejected', 'Failed'].map((l) => screen.getByText(l).parentElement!)
    expect(rows).toHaveLength(7)
    for (const row of rows) {
      const swatch = row.firstElementChild as HTMLElement
      expect(swatch.style.borderRadius).toBe('2px')
      expect(swatch.style.width).toBe('10px')
      expect(swatch.style.height).toBe('10px')
    }
  })

  it('the all-clear tick is a 40px circle, and drafts-only draws none', async () => {
    mockRollupFetch(rollup(0, { validated: 3 }))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    const tick = screen.getByText('No open failures').previousElementSibling as HTMLElement
    expect(tick.querySelector('svg')).not.toBeNull()
    expect(tick.style.borderRadius).toBe('50%')
    expect(tick.style.width).toBe('40px')
    expect(tick.style.height).toBe('40px')
  })

  it('every Recent activity dot is a 50% circle at 8x8', async () => {
    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    const tile = cardOf('Recent activity')
    const dots = [...tile.querySelectorAll<HTMLElement>('span')].filter((s) => s.style.width === '8px' && s.style.height === '8px')
    expect(dots).toHaveLength(5)
    for (const dot of dots) expect(dot.style.borderRadius).toBe('50%')
  })

  it('all ten TileHeads hold padding 14px 20px and gap 10px', async () => {
    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    expect(TILE_TITLES).toHaveLength(10)
    for (const title of TILE_TITLES) {
      const head = headOf(title)
      expect(head.style.padding, title).toBe('14px 20px')
      expect(head.style.gap, title).toBe('10px')
      expect(head.style.justifyContent, title).toBe('space-between')
    }
  })

  it('no rendered element carries a pill radius, and the circles that replaced it are present', async () => {
    for (const data of [rollup(0, { validated: 3 }), rollup(2, { rejected: 1, draft: 2 })]) {
      mockRollupFetch(data)
      const { container, unmount } = render(<DashboardActive ctx={dashCtx()} />)
      await settle()

      const styled = [...container.querySelectorAll<HTMLElement>('[style*="border-radius"]')]
      expect(styled.length).toBeGreaterThan(20)
      expect(styled.some((el) => el.style.borderRadius === '50%')).toBe(true)
      for (const el of styled) expect(el.getAttribute('style')).not.toMatch(PILL_GONE)
      unmount()
    }
  })
})

describe('DashboardActive Overview header (D-24)', () => {
  it('the h1 is 28px / -0.03em and sets no inline weight, in loading and ready alike', async () => {
    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={dashCtx()} />)

    const loadingH1 = screen.getByRole('heading', { level: 1 })
    expect(loadingH1.textContent).toBe('Acme Co')
    await settle()
    const h1 = screen.getByRole('heading', { level: 1 })
    for (const el of [loadingH1, h1]) {
      expect(el.style.fontSize).toBe('28px')
      expect(el.style.letterSpacing).toBe('-0.03em')
      expect(el.style.margin).toBe('0px 0px 5px')
      expect(el.style.fontWeight).toBe('')
    }
  })
})

describe('DashboardActive Needs-attention block flow (D-8)', () => {
  const NA = (title = 'Needs attention') => {
    const head = headOf(title)
    return { card: head.parentElement!, body: head.nextElementSibling as HTMLElement }
  }

  it('the body holds the figure row, the sentence and the button, with no inner wrapper', async () => {
    mockRollupFetch(rollup(3, { rejected: 1, failed: 1 }))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    const { card, body } = NA()
    expect([...card.children].map((c) => c.tagName)).toEqual(['DIV', 'DIV'])
    expect([...body.children].map((c) => c.tagName)).toEqual(['DIV', 'P', 'BUTTON'])
    const row = body.children[0] as HTMLElement
    expect([...row.children].map((c) => c.tagName)).toEqual(['SPAN', 'SPAN'])
    expect(row.children[0].textContent).toBe('3')
    expect(row.children[1].textContent).toBe('REJECTED / FAILED / BLOCKED / SENT BACK')
    expect(body.children[1].textContent).toMatch(/^Invoices rejected, failed, blocked/)
    expect(body.style.display).toBe('')
    expect(body.style.flexDirection).toBe('')
  })

  it('the figure row, figure and sentence hold the prototype spacing', async () => {
    mockRollupFetch(rollup(3, { rejected: 1, failed: 1 }))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    const { body } = NA()
    const row = body.children[0] as HTMLElement
    expect(row.style.display).toBe('flex')
    expect(row.style.alignItems).toBe('center')
    expect(row.style.gap).toBe('16px')
    expect(row.style.marginBottom).toBe('14px')
    expect(row.style.flexWrap).toBe('wrap')
    const figure = row.children[0] as HTMLElement
    expect(figure.className).toContain('money')
    expect(figure.style.fontSize).toBe('56px')
    expect(figure.style.fontWeight).toBe('700')
    expect(figure.style.lineHeight).toBe('1')
    const sentence = body.children[1] as HTMLElement
    expect(sentence.style.fontSize).toBe('13px')
    expect(sentence.style.lineHeight).toBe('1.55')
    expect(sentence.style.margin).toBe('0px 0px 20px')
    expect(sentence.style.maxWidth).toBe('520px')
  })

  it('the button is a 38px ghost sized by its label: no stretch, no extra top margin', async () => {
    mockRollupFetch(rollup(1, { rejected: 1 }))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    const btn = NA().body.children[2] as HTMLElement
    expect(btn.textContent).toBe('Resolve 1 issue →')
    expect([...btn.classList]).toEqual(['v2-btn', 'v2-btn-ghost', 'pf-btn'])
    expect(btn.style.height).toBe('38px')
    expect(btn.style.fontSize).toBe('13px')
    expect(btn.style.width).toBe('')
    expect(btn.style.justifyContent).toBe('')
    expect(btn.style.marginTop).toBe('')
    expect(btn.style.alignSelf).toBe('')
  })

  it('the pill is 4px (radius-sm) and 0.04em in both states, with its colour branches', async () => {
    for (const [data, label] of [
      [rollup(2, { rejected: 2 }), 'REJECTED / FAILED / BLOCKED / SENT BACK'],
      [rollup(0), 'ALL CLEAR'],
    ] as [Rollup, string][]) {
      mockRollupFetch(data)
      const { unmount } = render(<DashboardActive ctx={dashCtx()} />)
      await settle()

      const pill = screen.getByText(label)
      expect(pill.getAttribute('style')).toContain('border-radius: var(--radius-sm)')
      expect(pill.style.letterSpacing).toBe('0.04em')
      expect(pill.style.padding).toBe('3px 9px')
      expect(pill.style.fontSize).toBe('10px')
      unmount()
    }
  })
})

describe('DashboardActive small clearances (D-24)', () => {
  it('the donut legend is a 9px column and every count reserves 18px', async () => {
    mockRollupFetch(rollup(0, { draft: 1, validated: 2 }))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    const legend = screen.getByText('Draft').parentElement!.parentElement as HTMLElement
    expect(legend.children).toHaveLength(7)
    expect(legend.style.display).toBe('flex')
    expect(legend.style.flexDirection).toBe('column')
    expect(legend.style.gap).toBe('9px')
    for (const row of legend.children) {
      const count = row.lastElementChild as HTMLElement
      expect(count.style.minWidth).toBe('18px')
      expect(count.style.fontWeight).toBe('600')
      expect((row as HTMLElement).style.gridTemplateColumns).toBe('10px minmax(0, 1fr) auto auto')
    }
  })

  it('the trend body pads 20px 20px 22px with a live score, and with none', async () => {
    mockRollupFetch(rollup(0, { validated: 1 }, { readiness: { num: 85, den: 100 } }))
    const { unmount } = render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    const live = headOf('Readiness trend').nextElementSibling as HTMLElement
    expect(live.querySelector('svg[viewBox="0 0 680 176"]')).not.toBeNull()
    expect(live.style.padding).toBe('20px 20px 22px')
    unmount()

    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    const none = headOf('Readiness trend').nextElementSibling as HTMLElement
    expect(none.querySelector('svg')).toBeNull()
    expect(none.style.padding).toBe('20px 20px 22px')
    expect((none.firstElementChild as HTMLElement).style.gap).toBe('10px')
    expect(none.textContent).toBe('—No invoices yet')
  })

  it('each failure row gives the rule key 150px on one line and the count a 40px minimum, and keeps four cells', async () => {
    const data = rollup(0, { rejected: 6 })
    data.totals.top_violations = [
      { rule_key: 'buyer-tin-format', invoices: 4 },
      { rule_key: 'tin-checksum', invoices: 2 },
    ]
    mockRollupFetch(data)
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    for (const key of ['buyer-tin-format', 'tin-checksum']) {
      const row = screen.getByText(key).parentElement as HTMLElement
      expect(row.children).toHaveLength(4)
      const keyCell = row.children[2] as HTMLElement
      expect(keyCell.textContent).toBe(key)
      expect(keyCell.style.width).toBe('150px')
      expect(keyCell.style.flex).toBe('0 0 auto')
      expect(keyCell.style.whiteSpace).toBe('nowrap')
      expect(keyCell.style.overflow).toBe('hidden')
      expect(keyCell.style.textOverflow).toBe('ellipsis')
      expect(keyCell.title).toBe(key)
      const count = row.children[3] as HTMLElement
      expect(count.style.width).toBe('')
      expect(count.style.minWidth).toBe('40px')
      expect(count.style.whiteSpace).toBe('nowrap')
      expect(count.style.flex).toBe('0 0 auto')
      expect(count.style.textAlign).toBe('right')
    }
  })

  it('the four KPI tiles keep their 138px floor and padded body (D-7)', async () => {
    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    for (const label of ['Invoices', 'VAT tracked', 'Exceptions', 'Not yet submitted']) {
      const tile = kpiTile(label)
      expect(tile.style.minHeight, label).toBe('138px')
      expect((tile.lastElementChild as HTMLElement).style.padding, label).toBe('18px 20px 20px')
      expect((tile.lastElementChild!.firstElementChild as HTMLElement).style.fontWeight, label).toBe('700')
    }
  })
})

describe('DashboardActive states sit inside the page wrapper (D-37)', () => {
  const wrapperOf = () => screen.getByRole('heading', { level: 1 }).parentElement!.parentElement as HTMLElement

  it('loading renders under the header, inside the padded wrapper', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    const { container } = render(<DashboardActive ctx={dashCtx()} />)

    const wrapper = wrapperOf()
    expect(container.firstElementChild).toBe(wrapper)
    expect(wrapper.style.padding).toBe('30px 36px 56px')
    const loading = screen.getByText('Loading dashboard…')
    expect(wrapper.contains(loading)).toBe(true)
    expect(screen.getByRole('heading', { level: 1 }).compareDocumentPosition(loading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('error renders its message and Retry inside the wrapper', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve<MockResponse>({ ok: false, status: 503, json: () => Promise.resolve({}) })))
    render(<DashboardActive ctx={dashCtx()} />)

    const retry = await screen.findByRole('button', { name: 'Retry' })
    expect(wrapperOf().contains(retry)).toBe(true)
    expect(wrapperOf().style.padding).toBe('30px 36px 56px')
  })

  it('idle (no gateway) renders the zero-state inside the wrapper', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    render(<DashboardActive ctx={dashCtx()} />)

    const title = screen.getByText('No invoice activity yet')
    expect(wrapperOf().contains(title)).toBe(true)
    expect(screen.getByText('Counts appear once invoices are created.')).toBeDefined()
    expect(wrapperOf().style.padding).toBe('30px 36px 56px')
  })

  it('ES-02 the dashboard idle card is dense', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    render(<DashboardActive ctx={dashCtx()} />)

    const title = screen.getByText('No invoice activity yet')
    const card = title.parentElement as HTMLElement
    expect(card.style.padding).toBe('48px')
    expect(card.style.background).toBe('transparent')
    expect(title.style.fontSize).toBe('15px')
    expect(card.querySelector('button'), 'control: the idle card has no action').toBeNull()
  })
})
