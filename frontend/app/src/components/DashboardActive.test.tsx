// @vitest-environment jsdom
// QA Mode B adversarial coverage (task-332, BUG-01-06, AC-5) -- nothing rendered this
// component before, so the Overview chip's relabel (DashboardActive.tsx:259) was
// untested in both directions. Mirrors InvoiceDetail.test.tsx's fetch-mock + ctx-cast
// idiom (single-endpoint mock: DashboardActive fires only getRollup, unlike
// InvoiceDetail's two concurrent effects).
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { useLayoutEffect } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AuditEvent, AuditResponse } from '../lib/audit'
import { createAuthedFetch } from '../lib/authedFetch'
import { sparkPath } from '../lib/chartPath'
import type { Counts, HistoryRow, Metrics, Rollup } from '../lib/dashboard'
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

const ok = (body: unknown) => Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(body) })

function auditResponse(events: AuditEvent[] = []): AuditResponse {
  return {
    events,
    page: { limit: 5, has_more: false, next_cursor: null },
    total: events.length,
    log_is_empty: events.length === 0,
    facets: { event: [], actor: [], company: [] },
  }
}

const NOW_MS = Date.now()
function auditEvent(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 'ev-1',
    created_at: new Date(NOW_MS - 300_000).toISOString(),
    event: 'invoice.created',
    actor: 'sub-1',
    actor_name: 'Ada Obi',
    actor_kind: 'person',
    entity_id: 'ent-1',
    company_name: 'Acme Ltd',
    company_scope: 'company',
    payload: null,
    ...over,
  }
}
const EVENTS = ['invoice.created', 'invoice.validated', 'invoice.approval_approved', 'invoice.updated', 'invoice.approval_armed']
const LABELS = ['Invoice created', 'Invoice validated', 'Approved', 'Invoice updated', 'Approval requested']
const fiveEvents = (): AuditEvent[] => EVENTS.map((event, i) => auditEvent({ id: `ev-${i}`, event }))

type AuditReply = AuditResponse | ((url: string) => Promise<MockResponse>)
const auditPending: AuditReply = () => new Promise<MockResponse>(() => {})
const auditFails: AuditReply = () => Promise.resolve<MockResponse>({ ok: false, status: 500, json: () => Promise.resolve({}) })

type HistoryReply = unknown | (() => Promise<MockResponse>)

// Routes by URL: /audit-log answers `audit` (default: an empty log), /rollup/history answers
// `history` (default: no rows), anything else the rollup.
function mockRollupFetch(data: Rollup, audit: AuditReply = auditResponse(), history: HistoryReply = { history: [] }) {
  const fetchMock = vi.fn((url: string) => {
    if (String(url).includes('/rollup/history')) return typeof history === 'function' ? (history as () => Promise<MockResponse>)() : ok(history)
    if (!String(url).includes('/audit-log')) return ok(data)
    return typeof audit === 'function' ? audit(String(url)) : ok(audit)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}
const auditUrls = (m: ReturnType<typeof mockRollupFetch>) =>
  m.mock.calls.map((c) => new URL(String(c[0]))).filter((u) => u.pathname.includes('/audit-log'))

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
  it.each([
    ['no invoices', rollup(0)],
    ['readiness 67', rollup(0, {}, { readiness: { num: 67, den: 100 } })],
  ])('no SAMPLE text on the Overview, trend meta reads 12 WEEKS (%s)', async (_, data) => {
    mockRollupFetch(data)

    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByText('No activity yet') // the audit tile settled
    expect(document.body.textContent).not.toMatch(/SAMPLE/)
    expect(screen.getByText('Readiness trend').parentElement!.textContent).toBe('Readiness trend12 WEEKS')
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

  it('dashboardActive_customKeyReadsCustomRule', async () => {
    const key = 'custom:3f2a0000-0000-0000-0000-000000000001'
    const data = rollup(0)
    data.totals.top_violations = [{ rule_key: key, invoices: 4 }, { rule_key: 'vat-standard-rate', invoices: 2 }]
    mockRollupFetch(data)

    const { container } = render(<DashboardActive ctx={dashCtx()} />)
    await screen.findByText('Top validation failures')

    const custom = await waitFor(() => {
      const el = container.querySelector(`[title="${key}"]`)
      expect(el).not.toBeNull()
      return el as Element
    })
    expect(custom.textContent).toBe('Custom rule')
    expect(container.querySelector('[title="vat-standard-rate"]')?.textContent).toBe('vat-standard-rate')
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

const trendTile = () => cardOf('Readiness trend')
const trendBody = () => headOf('Readiness trend').nextElementSibling as HTMLElement
const NO_HISTORY = 'No history yetThe trend starts on the first day this workspace records.'

describe('DashboardActive readiness trend draws no curve', () => {
  it.each([0, 45, 100])('a live score draws the empty state in the trend tile (score %i)', async (score) => {
    mockRollupFetch(rollup(0, {}, { readiness: { num: score, den: 100 } }))

    render(<DashboardActive ctx={dashCtx()} />)

    const ringTile = (await screen.findByText('Readiness score')).parentElement!.parentElement!
    expect(within(ringTile).getByText(String(score))).toBeDefined()
    expect(trendBody().textContent).toBe(NO_HISTORY)
    expect(trendTile().querySelector('svg')).toBeNull()
    expect(within(trendTile()).queryByText(`${score}%`)).toBeNull()
  })

  it('the trend empty state uses the sibling empty-state markup', async () => {
    mockRollupFetch(rollup(0, {}, { readiness: { num: 67, den: 100 } }))

    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    const body = trendBody()
    expect(body.style.padding).toBe('40px 20px')
    expect(body.children[0].classList.contains('card-title')).toBe(true)
    const msg = body.children[1] as HTMLElement
    expect(msg.style.fontSize).toBe('13px')
    expect(msg.style.color).toBe('var(--fg-3)')
  })

  it('null live score keeps the em-dash and "No invoices yet"', async () => {
    mockRollupFetch(rollup(0)) // metrics: {} -- no invoices, no live score

    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    expect(trendTile().querySelector('svg')).toBeNull()
    expect(within(trendTile()).getByText('—')).toBeDefined()
    expect(within(trendTile()).getByText('No invoices yet')).toBeDefined()
    expect(within(trendTile()).getByText('12 WEEKS')).toBeDefined()
    expect(within(trendTile()).queryByText('No history yet')).toBeNull()
  })

  it('switching the selected client moves the ring; the trend stays the empty state', async () => {
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

    await settle()
    expect(within(cardOf('Readiness score')).getByText('30')).toBeDefined()
    expect(within(trendTile()).getByText('No history yet')).toBeDefined()

    rerender(<DashboardActive ctx={firmCtx('ent-b', 'Client B')} />)

    expect(within(cardOf('Readiness score')).getByText('95')).toBeDefined()
    expect(within(cardOf('Readiness score')).queryByText('30')).toBeNull()
    expect(within(trendTile()).getByText('No history yet')).toBeDefined()
  })

  it('switching from a live-score client to a no-invoices client flips the trend branch, and back', async () => {
    const data: Rollup = {
      totals: { counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] },
      clients: [
        { entity_id: 'ent-a', entity_name: 'Client A', counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: { readiness: { num: 30, den: 100 } }, top_violations: [] },
        { entity_id: 'ent-b', entity_name: 'Client B', counts: ZERO_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] },
      ],
      top_violations: [],
    }
    mockRollupFetch(data)

    const { rerender } = render(<DashboardActive ctx={firmCtx('ent-a', 'Client A')} />)

    await settle()
    expect(trendBody().textContent).toBe(NO_HISTORY)

    rerender(<DashboardActive ctx={firmCtx('ent-b', 'Client B')} />)
    expect(trendBody().textContent).toBe('—No invoices yet')
    expect(within(trendTile()).queryByText('No history yet')).toBeNull()

    rerender(<DashboardActive ctx={firmCtx('ent-a', 'Client A')} />)
    expect(trendBody().textContent).toBe(NO_HISTORY)
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

describe('DashboardActive Recent activity (OVER-01)', () => {
  const activityTile = () => screen.getByText('Recent activity').parentElement!.parentElement as HTMLElement
  const settleTile = async () => {
    await screen.findByText('Readiness score')
  }
  const firmWith = (id: string) => firmCtx(id, `Client ${id}`)

  it('in-house requests five rows and no company', async () => {
    const m = mockRollupFetch(rollup(0), auditResponse(fiveEvents()))
    render(<DashboardActive ctx={{ ...dashCtx(), active: { entityId: 'ent-1' } } as unknown as PlatformCtx} />)
    await screen.findByText('Invoice created')

    const urls = auditUrls(m)
    expect(urls).toHaveLength(1)
    expect(urls[0].pathname).toBe('/api/invoice/v1/audit-log')
    expect(urls[0].searchParams.get('limit')).toBe('5')
    expect(urls[0].searchParams.has('company')).toBe(false)
  })

  it('firm mode sends the active entity', async () => {
    const m = mockRollupFetch(rollup(0), auditResponse(fiveEvents()))
    render(<DashboardActive ctx={firmWith('ent-1')} />)
    await screen.findByText('Invoice created')

    const u = auditUrls(m)[0]
    expect(u.searchParams.get('company')).toBe('ent-1')
    expect(u.searchParams.get('limit')).toBe('5')
  })

  it('the request carries no client filter beyond limit and company', async () => {
    const m = mockRollupFetch(rollup(0), auditResponse(fiveEvents()))
    render(<DashboardActive ctx={firmWith('ent-1')} />)
    await screen.findByText('Invoice created')

    expect([...auditUrls(m)[0].searchParams.keys()].sort()).toEqual(['company', 'limit'])
  })

  const byCompany = (replies: Record<string, AuditResponse>): AuditReply => (url) =>
    ok(replies[new URL(url).searchParams.get('company') ?? ''])

  it('switching client refetches, shows Loading between, and never shows the old rows', async () => {
    const one = auditResponse([auditEvent({ id: 'a', actor_name: 'Old Actor' })])
    const m = mockRollupFetch(rollup(0), byCompany({ 'ent-1': one, 'ent-2': auditResponse([auditEvent({ id: 'b', actor_name: 'New Actor' })]) }))
    const { rerender } = render(<DashboardActive ctx={firmWith('ent-1')} />)
    await screen.findByText('Old Actor')

    rerender(<DashboardActive ctx={firmWith('ent-2')} />)
    expect(screen.queryByText('Old Actor')).toBeNull()
    expect(within(activityTile()).getByText('Loading activity…')).toBeDefined()
    await screen.findByText('New Actor')
    expect(auditUrls(m).map((u) => u.searchParams.get('company'))).toEqual(['ent-1', 'ent-2'])
  })

  it('no committed frame after a client switch shows the old rows', async () => {
    mockRollupFetch(rollup(0), byCompany({
      'ent-1': auditResponse([auditEvent({ id: 'a', actor_name: 'Old Actor' })]),
      'ent-2': auditResponse([auditEvent({ id: 'b', actor_name: 'New Actor' })]),
    }))
    const frames: string[] = []
    // Sibling layout effects run in the commit, after the DOM is mutated and before any paint.
    const Probe = () => {
      useLayoutEffect(() => { frames.push(document.body.textContent ?? '') })
      return null
    }
    const ui = (id: string) => <><DashboardActive ctx={firmWith(id)} /><Probe /></>
    const { rerender } = render(ui('ent-1'))
    await screen.findByText('Old Actor')

    frames.length = 0
    rerender(ui('ent-2'))
    await screen.findByText('New Actor')

    expect(frames.length).toBeGreaterThan(0)
    expect(frames[0]).not.toContain('Old Actor')
  })

  it('no committed frame after an in-house to firm switch shows the old rows', async () => {
    mockRollupFetch(rollup(0), (url) =>
      ok(auditResponse([auditEvent({ id: 'x', actor_name: new URL(url).searchParams.has('company') ? 'Firm Actor' : 'House Actor' })])),
    )
    const frames: string[] = []
    const Probe = () => {
      useLayoutEffect(() => { frames.push(document.body.textContent ?? '') })
      return null
    }
    const inhouse = { ...dashCtx(), active: { entityId: 'ent-1' } } as unknown as PlatformCtx
    const { rerender } = render(<><DashboardActive ctx={inhouse} /><Probe /></>)
    await screen.findByText('House Actor')

    frames.length = 0
    rerender(<><DashboardActive ctx={firmWith('ent-1')} /><Probe /></>)
    await screen.findByText('Firm Actor')

    expect(frames.length).toBeGreaterThan(0)
    expect(frames[0]).not.toContain('House Actor')
  })

  it('a late response for the old client is dropped', async () => {
    const resolvers: Record<string, (v: MockResponse) => void> = {}
    mockRollupFetch(rollup(0), (url) => new Promise<MockResponse>((res) => { resolvers[new URL(url).searchParams.get('company')!] = res }))
    const reply = (name: string) => ({ ok: true, status: 200, json: () => Promise.resolve(auditResponse([auditEvent({ id: name, actor_name: name })])) })

    const { rerender } = render(<DashboardActive ctx={firmWith('ent-1')} />)
    await screen.findByText('Readiness score')
    await waitFor(() => expect(resolvers['ent-1']).toBeDefined())
    rerender(<DashboardActive ctx={firmWith('ent-2')} />)
    await waitFor(() => expect(resolvers['ent-2']).toBeDefined())

    resolvers['ent-2'](reply('Actor Two'))
    await screen.findByText('Actor Two')
    resolvers['ent-1'](reply('Actor One'))
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByText('Actor One')).toBeNull()
    expect(screen.getByText('Actor Two')).toBeDefined()
  })

  it('a client going to null clears the rows and makes no further call', async () => {
    const m = mockRollupFetch(rollup(0), auditResponse(fiveEvents()))
    const { rerender } = render(<DashboardActive ctx={firmWith('ent-1')} />)
    await screen.findByText('Invoice created')

    rerender(<DashboardActive ctx={{ ...firmWith('ent-1'), active: { entityId: null, name: 'x' } } as unknown as PlatformCtx} />)
    expect(screen.queryByText('Invoice created')).toBeNull()
    expect(within(activityTile()).getByText('No activity yet')).toBeDefined()
    expect(auditUrls(m)).toHaveLength(1)
  })

  it('firm with no resolved client makes no call', async () => {
    const m = mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={{ ...firmWith('x'), active: { entityId: null, name: 'x' } } as unknown as PlatformCtx} />)
    await settleTile()

    expect(auditUrls(m)).toHaveLength(0)
    expect(within(activityTile()).getByText('No activity yet')).toBeDefined()
  })

  it('in-house: entity id landing after the rollup keeps the rows and makes no second call', async () => {
    const m = mockRollupFetch(rollup(0), auditResponse(fiveEvents()))
    const inhouse = (entityId: string | null) => ({ ...dashCtx(), active: { entityId, name: 'x' } }) as unknown as PlatformCtx
    const { rerender } = render(<DashboardActive ctx={inhouse(null)} />)
    await screen.findByText('Invoice created')

    rerender(<DashboardActive ctx={inhouse('ent-1')} />)
    expect(screen.getByText('Invoice created')).toBeDefined()
    expect(within(activityTile()).queryByText('Loading activity…')).toBeNull()
    await settleTile()
    expect(auditUrls(m)).toHaveLength(1)
  })

  it('a raw subject with no actor name renders in the mono font', async () => {
    mockRollupFetch(rollup(0), auditResponse([auditEvent({ actor_name: '', actor: 'sys-x' })]))
    render(<DashboardActive ctx={dashCtx()} />)
    const who = await screen.findByText('sys-x')

    expect(who.classList.contains('mono')).toBe(true)
  })

  it('a row reads actor, label, company, time', async () => {
    mockRollupFetch(rollup(0), auditResponse([auditEvent()]))
    render(<DashboardActive ctx={dashCtx()} />)
    await screen.findByText('Ada Obi')

    const tile = within(activityTile())
    expect(tile.getByText('Invoice created')).toBeDefined()
    expect(tile.getByText('Acme Ltd')).toBeDefined()
    expect(tile.getByText('5m ago')).toBeDefined()
  })

  it('five rows keep the response order', async () => {
    mockRollupFetch(rollup(0), auditResponse(fiveEvents()))
    render(<DashboardActive ctx={dashCtx()} />)
    await screen.findByText('Invoice created')

    const text = activityTile().textContent!
    const at = LABELS.map((l) => text.indexOf(l))
    expect(at.every((i) => i >= 0)).toBe(true)
    expect(at).toEqual([...at].sort((x, y) => x - y))
  })

  it('a workspace-level row omits the company', async () => {
    mockRollupFetch(rollup(0), auditResponse([auditEvent({ company_scope: 'workspace', company_name: null })]))
    render(<DashboardActive ctx={dashCtx()} />)
    await screen.findByText('Ada Obi')

    const row = screen.getByText('Ada Obi').parentElement as HTMLElement
    expect(row.textContent).toBe('Ada Obi Invoice created')
  })

  it('empty log shows the empty state and no mock row', async () => {
    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={dashCtx()} />)
    await settleTile()

    const tile = within(activityTile())
    await tile.findByText('No activity yet')
    expect(tile.getByText('Edits, validations, approvals and submissions appear here as they happen.')).toBeDefined()
    expect(screen.queryByText('No activity to show')).toBeNull()
    expect(screen.queryByText('INV-2026-00481')).toBeNull()
  })

  it('the empty tile holds only title and empty copy', async () => {
    mockRollupFetch(rollup(0, { validated: 5 }))
    render(<DashboardActive ctx={dashCtx()} />)
    await settleTile()
    await within(activityTile()).findByText('No activity yet')

    expect(activityTile().textContent).toBe(
      'Recent activityNo activity yetEdits, validations, approvals and submissions appear here as they happen.',
    )
  })

  it('a failed call shows the error, not the empty state', async () => {
    mockRollupFetch(rollup(0), auditFails)
    render(<DashboardActive ctx={dashCtx()} />)
    await settleTile()

    const tile = within(activityTile())
    expect(await tile.findByText(/Something went wrong/)).toBeDefined()
    expect(tile.queryByText('No activity yet')).toBeNull()
  })

  it('Retry refetches', async () => {
    let n = 0
    mockRollupFetch(rollup(0), () => (n++ === 0 ? auditFails('') : ok(auditResponse(fiveEvents()))))
    render(<DashboardActive ctx={dashCtx()} />)
    await settleTile()

    fireEvent.click(await within(activityTile()).findByRole('button', { name: 'Retry' }))
    await screen.findByText('Invoice created')
    expect(within(activityTile()).queryByText(/Something went wrong/)).toBeNull()
  })

  it('a pending call shows Loading', async () => {
    mockRollupFetch(rollup(0), auditPending)
    render(<DashboardActive ctx={dashCtx()} />)
    await settleTile()

    expect(within(activityTile()).getByText('Loading activity…')).toBeDefined()
  })

  it.each([true, false])('the head carries no SAMPLE and no mock row (handoff %s)', async (handoff) => {
    mockRollupFetch(rollup(0))
    render(<DashboardActive ctx={{ ...dashCtx(), handoff } as unknown as PlatformCtx} />)
    await settleTile()

    const head = screen.getByText('Recent activity').parentElement as HTMLElement
    expect(within(head).queryByText('SAMPLE')).toBeNull()
    expect(screen.queryByText('INV-2026-00481')).toBeNull()
  })

  it('a failed rollup still shows no tile', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve<MockResponse>({ ok: false, status: 500, json: () => Promise.resolve({}) })))
    render(<DashboardActive ctx={dashCtx()} />)

    await screen.findByRole('button', { name: 'Retry' })
    expect(screen.queryByText('Recent activity')).toBeNull()
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

  it('every activity dot is a 50% circle at 8x8', async () => {
    mockRollupFetch(rollup(0), auditResponse(fiveEvents()))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    await screen.findByText('Invoice created')

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

  it('the trend body pads 20px 20px 22px with no score, and 40px 20px with a live score', async () => {
    mockRollupFetch(rollup(0, { validated: 1 }, { readiness: { num: 85, den: 100 } }))
    const { unmount } = render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    const live = headOf('Readiness trend').nextElementSibling as HTMLElement
    expect(live.querySelector('svg')).toBeNull()
    expect(live.style.padding).toBe('40px 20px')
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

  it('the four KPI tiles draw no svg and keep value and delta', async () => {
    mockRollupFetch(rollup(0, { validated: 3, draft: 2 }))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    const expected: Record<string, [string, string]> = {
      Invoices: ['5', '0 transmitted'],
      'VAT tracked': ['—', 'output VAT'],
      Exceptions: ['0', 'all clear'],
      'Not yet submitted': ['5', '0 awaiting approval'],
    }
    for (const [label, [value, delta]] of Object.entries(expected)) {
      const tile = kpiTile(label)
      expect(tile.querySelector('svg'), label).toBeNull()
      expect(within(tile).getByText(value), label).toBeDefined()
      expect(kpiDelta(label), label).toBe(delta)
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

// KPI sparklines and the Readiness trend draw recorded history.
describe('DashboardActive draws recorded history', () => {
  const TODAY = '2026-10-29'
  const urls = (m: ReturnType<typeof mockRollupFetch>, part: string) =>
    m.mock.calls.filter((c) => String(c[0]).includes(part))
  const day = (n: number) => `2026-10-${String(n).padStart(2, '0')}`
  const row = (d: string, entity = 'ent-a', total = 10, num = 5): HistoryRow => ({
    day: d, entity_id: entity, total, readiness_num: num, needs_attention: total % 7, not_submitted: total % 5, vat_kobo: total * 100,
  })
  // 21..29 Oct, rising totals, readiness 50%.
  const nineDays = (entity = 'ent-a') => Array.from({ length: 9 }, (_, i) => row(day(21 + i), entity, 10 + i))
  const KPI = ['Invoices', 'VAT tracked', 'Exceptions', 'Not yet submitted']
  const STROKES = ['var(--action)', 'var(--action)', 'var(--status-red-text)', 'var(--status-amber-text)']
  const live = () => rollup(0, { accepted: 100 }, { readiness: { num: 67, den: 100 } })
  const firmLive = (): Rollup => {
    const c = (id: string) => ({ entity_id: id, entity_name: id, counts: { ...ZERO_COUNTS, accepted: 100 }, needs_attention: 0, awaiting_approval: 0, metrics: { readiness: { num: 67, den: 100 } }, top_violations: [] })
    return { ...live(), clients: [c('ent-a'), c('ent-b')] }
  }
  const kpiRow = (l: string) => kpiTile(l).lastElementChild!.lastElementChild as HTMLElement
  const noKpiSvg = () =>
    KPI.forEach((l) => {
      const tile = kpiTile(l)
      expect(tile.querySelector('svg'), l).toBeNull()
      expect(tile.textContent, l).not.toContain('SAMPLE')
      const row = kpiRow(l)
      expect(row.children.length, l).toBe(1)
      expect(row.firstElementChild, l).toBe(kpiDeltaEl(l))
    })

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(`${TODAY}T12:00:00Z`))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('reads history once per mount, not blocking the tiles', async () => {
    const m = mockRollupFetch(live(), auditResponse(), () => new Promise<MockResponse>(() => {}))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    expect(within(kpiTile('Invoices')).getByText('100')).toBeDefined()
    expect(within(kpiTile('Exceptions')).getByText('0')).toBeDefined()
    expect(urls(m, '/rollup/history')).toHaveLength(1)
    expect(urls(m, '/dashboard/v1/rollup').filter((c) => !String(c[0]).includes('/history'))).toHaveLength(1)
  })

  it('KPI tiles draw a sparkline from two recorded days', async () => {
    const rows = [row(day(27), 'ent-a', 10), row(day(28), 'ent-a', 20), row(day(29), 'ent-a', 30)]
    mockRollupFetch(live(), auditResponse(), { history: rows })
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    await waitFor(() => expect(kpiTile('Invoices').querySelector('svg')).not.toBeNull())
    // today's row is replaced by the live value (total 100).
    const totals = [10, 20, 100]
    const fields = [
      totals,
      [1000, 2000, 0],
      [3, 6, 0],
      [0, 0, 0],
    ]
    KPI.forEach((l, i) => {
      const paths = kpiTile(l).querySelectorAll('svg path')
      expect(paths, l).toHaveLength(1)
      expect(paths[0].getAttribute('d'), l).toBe(sparkPath(fields[i]))
      expect(paths[0].getAttribute('stroke'), l).toBe(STROKES[i])
    })
  })

  it('KPI tiles draw nothing from one recorded day', async () => {
    mockRollupFetch(live(), auditResponse(), { history: [row(day(29))] })
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    await Promise.resolve()

    noKpiSvg()
  })

  it('firm mode draws only the active client\'s rows', async () => {
    const rows = [row(day(27), 'ent-a', 10), row(day(28), 'ent-a', 20), row(day(27), 'ent-b', 500), row(day(28), 'ent-b', 900)]
    mockRollupFetch(firmLive(), auditResponse(), { history: rows })
    render(<DashboardActive ctx={firmCtx('ent-a', 'Client A')} />)
    await settle()

    await waitFor(() => expect(kpiTile('Invoices').querySelector('svg')).not.toBeNull())
    expect(kpiTile('Invoices').querySelector('path')!.getAttribute('d')).toBe(sparkPath([10, 20, 100]))
  })

  it('trend draws headline, delta, chart, months and span meta', async () => {
    mockRollupFetch(live(), auditResponse(), { history: nineDays() })
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    await waitFor(() => expect(trendTile().querySelector('svg')).not.toBeNull())
    const t = trendTile()
    expect(within(t).getByText('67%')).toBeDefined()
    expect(within(t).getByText('▲ +17 pts since 21 Oct')).toBeDefined()
    expect(within(t).getByText('SINCE 21 OCT · 9 DAYS')).toBeDefined()
    expect(within(t).getByText('OCT')).toBeDefined()
    expect(t.querySelectorAll('svg')).toHaveLength(1)
    expect(within(t).queryByText('No history yet')).toBeNull()
    expect(document.body.textContent).not.toContain('SAMPLE')
  })

  it('trend meta reads 12 WEEKS at an 84-day span', async () => {
    // 84 days ending today: 2026-08-07 .. 2026-10-29.
    const start = Date.UTC(2026, 7, 7)
    const rows = Array.from({ length: 84 }, (_, i) => row(new Date(start + i * 86400000).toISOString().slice(0, 10)))
    mockRollupFetch(live(), auditResponse(), { history: rows })
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    await waitFor(() => expect(trendTile().querySelector('svg')).not.toBeNull())
    expect(within(trendTile()).getByText('12 WEEKS')).toBeDefined()
  })

  it('trend keeps the empty state on one recorded day', async () => {
    mockRollupFetch(live(), auditResponse(), { history: [row(day(29))] })
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    await Promise.resolve()

    expect(trendBody().textContent).toBe(NO_HISTORY)
    expect(within(trendTile()).getByText('12 WEEKS')).toBeDefined()
    expect(trendTile().querySelector('svg')).toBeNull()
  })

  it('a null live score wins over history', async () => {
    mockRollupFetch(rollup(0), auditResponse(), { history: nineDays().slice(0, 5) })
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    await Promise.resolve()

    expect(trendBody().textContent).toBe('—No invoices yet')
    expect(trendTile().querySelector('svg')).toBeNull()
  })

  it('a failed history call leaves the OVER-02 states and no banner', async () => {
    mockRollupFetch(live(), auditResponse(), () => Promise.resolve<MockResponse>({ ok: false, status: 500, json: () => Promise.resolve({}) }))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    await Promise.resolve()

    expect(within(kpiTile('Invoices')).getByText('100')).toBeDefined()
    noKpiSvg()
    expect(within(trendTile()).getByText('No history yet')).toBeDefined()
    expect(screen.queryByText('Retry')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('a history body without a history array behaves as no history', async () => {
    mockRollupFetch(live(), auditResponse(), live())
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    await Promise.resolve()

    noKpiSvg()
    expect(within(trendTile()).getByText('No history yet')).toBeDefined()
  })

  it('late history draws the curves and fetches nothing again', async () => {
    let release!: (v: MockResponse) => void
    const pending = new Promise<MockResponse>((r) => (release = r))
    const m = mockRollupFetch(live(), auditResponse(), () => pending)
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    noKpiSvg()
    const held = kpiRow('Invoices')
    const delta = kpiDelta('Invoices')
    const heldTrend = trendBody()

    release({ ok: true, status: 200, json: () => Promise.resolve({ history: nineDays() }) })
    await waitFor(() => expect(kpiTile('Invoices').querySelector('svg')).not.toBeNull())
    await waitFor(() => expect(trendTile().querySelector('svg')).not.toBeNull())
    expect(trendBody()).toBe(heldTrend)

    expect(kpiRow('Invoices')).toBe(held)
    expect(Array.from(held.children)).toEqual([kpiDeltaEl('Invoices'), kpiTile('Invoices').querySelector('svg')])
    expect(kpiDelta('Invoices')).toBe(delta)

    expect(within(kpiTile('Invoices')).getByText('100')).toBeDefined()
    expect(urls(m, '/dashboard/v1/rollup').filter((c) => !String(c[0]).includes('/history'))).toHaveLength(1)
  })

  it('Retry after both calls failed re-reads the history and draws the curves', async () => {
    let failing = true
    const fail = () => Promise.resolve<MockResponse>({ ok: false, status: 503, json: () => Promise.resolve({}) })
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (failing) return fail()
      if (String(url).includes('/rollup/history')) return ok({ history: nineDays() })
      if (String(url).includes('/audit-log')) return ok(auditResponse())
      return ok(live())
    }))
    render(<DashboardActive ctx={dashCtx()} />)
    const retry = await screen.findByRole('button', { name: 'Retry' })
    failing = false
    fireEvent.click(retry)
    await waitFor(() => expect(kpiTile('Invoices').querySelector('svg')).not.toBeNull())
  })

  it('a client switch follows the new client without a refetch', async () => {
    const rows = [row(day(29), 'ent-a'), ...Array.from({ length: 40 }, (_, i) => row(new Date(Date.UTC(2026, 9, 29) - i * 86400000).toISOString().slice(0, 10), 'ent-b'))]
    const m = mockRollupFetch(firmLive(), auditResponse(), { history: rows })
    const { rerender } = render(<DashboardActive ctx={firmCtx('ent-a', 'Client A')} />)
    await settle()
    await Promise.resolve()
    noKpiSvg()

    rerender(<DashboardActive ctx={firmCtx('ent-b', 'Client B')} />)
    await waitFor(() => expect(kpiTile('Invoices').querySelector('svg')).not.toBeNull())
    expect(urls(m, '/rollup/history')).toHaveLength(1)
  })

  it('one client\'s long history does not draw on a client with one recorded day', async () => {
    const rows = [row(day(29), 'ent-a'), ...Array.from({ length: 40 }, (_, i) => row(new Date(Date.UTC(2026, 9, 29) - i * 86400000).toISOString().slice(0, 10), 'ent-b'))]
    mockRollupFetch(firmLive(), auditResponse(), { history: rows })
    render(<DashboardActive ctx={firmCtx('ent-a', 'Client A')} />)
    await settle()
    await Promise.resolve()

    noKpiSvg()
    expect(within(trendTile()).getByText('No history yet')).toBeDefined()
  })

  it('trend delta reads a fall and no change', async () => {
    const days = (num: number) => Array.from({ length: 9 }, (_, i) => row(day(21 + i), 'ent-a', 100, num))
    mockRollupFetch(live(), auditResponse(), { history: days(90) })
    const first = render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    await waitFor(() => expect(within(trendTile()).getByText('▼ -23 pts since 21 Oct')).toBeDefined())
    first.unmount()

    mockRollupFetch(live(), auditResponse(), { history: days(67) })
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    await waitFor(() => expect(within(trendTile()).getByText('no change since 21 Oct')).toBeDefined())
  })

  it('trend meta is a span below 84 days', async () => {
    const start = Date.UTC(2026, 7, 8)
    const rows = Array.from({ length: 83 }, (_, i) => row(new Date(start + i * 86400000).toISOString().slice(0, 10)))
    mockRollupFetch(live(), auditResponse(), { history: rows })
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()

    await waitFor(() => expect(within(trendTile()).getByText('SINCE 8 AUG · 83 DAYS')).toBeDefined())
  })

  it('late history after a client switch draws the new client only', async () => {
    let release!: (v: MockResponse) => void
    const pending = new Promise<MockResponse>((r) => (release = r))
    const b = Array.from({ length: 5 }, (_, i) => row(day(25 + i), 'ent-b', 10 + i * 10))
    const rows = [...b, row(day(28), 'ent-a', 999), row(day(29), 'ent-a', 5)]
    mockRollupFetch(firmLive(), auditResponse(), () => pending)
    const { rerender } = render(<DashboardActive ctx={firmCtx('ent-a', 'Client A')} />)
    await settle()
    rerender(<DashboardActive ctx={firmCtx('ent-b', 'Client B')} />)
    release({ ok: true, status: 200, json: () => Promise.resolve({ history: rows }) })

    await waitFor(() => expect(kpiTile('Invoices').querySelector('svg')).not.toBeNull())
    expect(kpiTile('Invoices').querySelector('path')!.getAttribute('d')).toBe(sparkPath([10, 20, 30, 40, 100]))
  })

  it('a history rejection after the rollup rendered leaves no trace', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    mockRollupFetch(live(), auditResponse(), () => Promise.reject(new Error('boom')))
    render(<DashboardActive ctx={dashCtx()} />)
    await settle()
    await Promise.resolve()

    noKpiSvg()
    expect(screen.queryByText(/boom/)).toBeNull()
    expect(err).not.toHaveBeenCalled()
    err.mockRestore()
  })
})
