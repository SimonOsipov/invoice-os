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
    expect(screen.getByText('No open failures')).toBeDefined()
    expect(screen.queryByText('99')).toBeNull()
    expect(screen.queryByText('tenant-rule')).toBeNull()
  })

  it('(k) firm-mode client with empty top_violations renders "No open failures", not the tenant-wide list', async () => {
    const data: Rollup = {
      totals: { counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [{ rule_key: 'tenant-wide-rule', invoices: 10 }] },
      clients: [
        { entity_id: 'ent-3', entity_name: 'Clean Client Ltd', counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] },
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
