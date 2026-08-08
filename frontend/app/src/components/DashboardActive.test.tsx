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

function rollup(needsAttention: number, countsOver: Partial<Counts> = {}, metricsOver: Metrics = {}): Rollup {
  const counts: Counts = { ...ZERO_COUNTS, ...countsOver }
  return {
    totals: { counts, needs_attention: needsAttention, metrics: metricsOver, top_violations: [] },
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
  it('AC-5: needs_attention > 0 reads the relabelled REJECTED / FAILED / BLOCKED chip', async () => {
    mockRollupFetch(rollup(3, { rejected: 1, failed: 1 }))

    render(<DashboardActive ctx={dashCtx()} />)

    expect(await screen.findByText('REJECTED / FAILED / BLOCKED')).toBeDefined()
    expect(screen.queryByText('REJECTED / FAILED')).toBeNull()
  })

  it('needs_attention === 0 still reads ALL CLEAR -- the relabel only touches the non-zero string', async () => {
    mockRollupFetch(rollup(0))

    render(<DashboardActive ctx={dashCtx()} />)

    expect(await screen.findByText('ALL CLEAR')).toBeDefined()
    expect(screen.queryByText(/REJECTED/)).toBeNull()
  })

  // The single highest-risk case in this subtask: the story's Core AC says
  // needs_attention's COUNT is correct today and must not change. bucket.needs_attention
  // (3) here intentionally disagrees with BOTH counts.rejected + counts.failed (0 + 0 =
  // 0) and the total/awaiting KPIs (7, from draft alone) -- the shape a blocked-draft
  // contribution takes (rejected/failed stay 0, only the needs_attention overlay carries
  // the 3). A regression that re-derives the number from rejected+failed would render 0;
  // one that reused total/awaiting would render 7. getAllByText pins the count to
  // EXACTLY the two legitimate render sites that read bucket.needs_attention verbatim
  // (the big panel number and the "Failing invoices" KPI tile) -- see DashboardActive.tsx
  // kpiValues, which threads the same `needsAttention` param into both.
  it('AC-5: the displayed count is bucket.needs_attention passed through untransformed, not re-derived from counts', async () => {
    mockRollupFetch(rollup(3, { rejected: 0, failed: 0, draft: 7 }))

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('REJECTED / FAILED / BLOCKED') // wait for the ready state to settle
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

    expect(await screen.findByText('No invoices yet')).toBeDefined()
    // Scoped to the Readiness tile itself: donutSegments legitimately renders '0%' for
    // all seven canonical states when the invoice-status donut has no invoices, so a
    // document-wide queryByText('0%') is a false positive there, not a real assertion.
    const readinessTile = screen.getByText('Readiness score').parentElement!.parentElement!
    expect(within(readinessTile).getAllByText('—').length).toBeGreaterThan(0)
    expect(within(readinessTile).queryByText('0%')).toBeNull()
  })

  it('(e) AC-7: a firm-mode client renders its OWN top_violations, not the tenant-wide list', async () => {
    const data: Rollup = {
      totals: { counts: ZERO_COUNTS, needs_attention: 0, metrics: {}, top_violations: [] },
      clients: [
        {
          entity_id: 'ent-1',
          entity_name: 'Dangote Cement PLC',
          counts: ZERO_COUNTS,
          needs_attention: 0,
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
