// @vitest-environment jsdom
// Pins the All/Active/Archived control (defaulting to All, nothing hidden), a
// server-side status request per position, the filter-aware header count, a
// filter-specific empty state, and ctx.entities staying untouched by the filter. Mirrors
// CustomersView.test.tsx/ReportsView.test.tsx's own mock-fetch-by-pathname idiom, since
// ClientsView also fires an independent rollup fetch.
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { Rollup } from '../lib/dashboard'
import type { Entity } from '../lib/portfolio'
import type { PlatformCtx } from '../types'
import { ClientsView } from './ClientsView'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function isRollupUrl(url: string): boolean {
  return new URL(url).pathname.endsWith('/rollup')
}

// Annotated `: Rollup` like ReportsView.test.tsx's sibling fixture, which is what makes an
// omitted RollupBucket field a typecheck error rather than a silent gap. The annotation is
// INERT here today: the sibling ClientsView.test.ts shadows this file out of tsc's program
// (TypeScript keeps only the highest-priority extension per basename, .ts over .tsx), so
// nothing typechecks it. Keep the fixture exhaustive by hand until that name collision goes.
const ZERO_ROLLUP: Rollup = {
  totals: {
    counts: { draft: 0, validated: 0, queued: 0, submitted: 0, accepted: 0, rejected: 0, failed: 0 },
    needs_attention: 0,
    awaiting_approval: 0,
    metrics: {},
    top_violations: [],
  },
  clients: [],
  top_violations: [],
}

function rollupResponse(): MockResponse {
  return { ok: true, status: 200, json: () => Promise.resolve(ZERO_ROLLUP) }
}

function entity(over: Partial<Entity> = {}): Entity {
  return {
    id: 'e1',
    name: 'Okafor & Partners',
    tin: '00000000001',
    registration: 'RC123456',
    sector: 'logistics',
    address: '12 Marina Rd, Lagos',
    status: 'active',
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

const ACTIVE_ROWS: Entity[] = [
  entity({ id: 'a1', name: 'Okafor & Partners', status: 'active' }),
  entity({ id: 'a2', name: 'Beta Traders', status: 'active' }),
]
const ARCHIVED_ROWS: Entity[] = [entity({ id: 'r1', name: 'Honeywell Group', status: 'archived' })]
const ALL_ROWS: Entity[] = [...ACTIVE_ROWS, ...ARCHIVED_ROWS]

// Models the real server contract (portfolio.go:215-222): empty/absent status -> both
// statuses; 'active'/'archived' -> that subset only.
function rowsForStatus(status: string | null): Entity[] {
  if (status === 'active') return ACTIVE_ROWS
  if (status === 'archived') return ARCHIVED_ROWS
  return ALL_ROWS
}

function entitiesResponse(rows: Entity[]): MockResponse {
  return {
    ok: true,
    status: 200,
    json: () => Promise.resolve({ entities: rows, pagination: { limit: 200, offset: 0, total: rows.length } }),
  }
}

// Routes by pathname/query rather than a FIFO queue -- the rollup fetch and the entities
// fetch can land in either order, and the entities fetch itself re-fires on every filter
// click, so a queue would attach the wrong body to the wrong request.
function mockFetch() {
  const fetchMock = vi.fn((url: string) => {
    if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
    const status = new URL(url).searchParams.get('status')
    return Promise.resolve(entitiesResponse(rowsForStatus(status)))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function entitiesCallUrls(fetchMock: ReturnType<typeof mockFetch>): string[] {
  return fetchMock.mock.calls.map((c) => c[0] as string).filter((url) => !isRollupUrl(url))
}

function clientsCtx(entities: Entity[]): PlatformCtx {
  const ctx = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    user: { name: 'Firm User', initials: 'FU', tenantName: 'Acme Co', verified: true },
    entities,
    entitiesState: 'ready',
    entitiesError: null,
    refetchEntities: () => {},
  }
  return ctx as unknown as PlatformCtx
}

// The count paragraph sits directly under the "Client portfolio" h1 (ClientsView.tsx's
// header block) -- anchored to that stable heading rather than a fixed full-string match,
// since the surrounding copy (org segment / "· partner program" suffix) isn't pinned by
// this story.
function headerCountText(): string {
  const heading = screen.getByText('Client portfolio')
  return heading.parentElement?.querySelector('p')?.textContent ?? ''
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('ClientsView: status filter defaults to All -- nothing hidden on first load', () => {
  it('offers All/Active/Archived and renders every row (active and archived) on mount', async () => {
    const fetchMock = mockFetch()
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)

    await screen.findByText('Okafor & Partners')
    expect(screen.getByText('Beta Traders')).toBeDefined()
    expect(screen.getByText('Honeywell Group'), 'the default position must not hide the archived row').toBeDefined()

    screen.getByRole('button', { name: 'All' })
    screen.getByRole('button', { name: 'Active' })
    screen.getByRole('button', { name: 'Archived' })

    const urls = entitiesCallUrls(fetchMock)
    expect(urls, 'mount must fire exactly one entities request').toHaveLength(1)
    expect(new URL(urls[0]).searchParams.has('status'), 'the default All position must send no status param').toBe(false)
  })
})

describe('ClientsView: each position requests its own status param and renders only those rows', () => {
  it('clicking Active issues status=active and hides the archived row', async () => {
    const fetchMock = mockFetch()
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    fireEvent.click(screen.getByRole('button', { name: 'Active' }))

    await waitFor(() => expect(screen.queryByText('Honeywell Group'), 'archived row must be gone under Active').toBeNull())
    expect(screen.getByText('Okafor & Partners')).toBeDefined()
    expect(screen.getByText('Beta Traders')).toBeDefined()

    const urls = entitiesCallUrls(fetchMock)
    const lastUrl = urls[urls.length - 1]
    // Asserted on the REQUEST, not just the rendered rows -- a client-side filter over the
    // already-fetched roster would pass the rows-only assertions above unchanged.
    expect(new URL(lastUrl).searchParams.get('status'), 'Active must issue a status=active request').toBe('active')
  })

  it('clicking Archived issues status=archived and hides active rows; All restores both and sends no status', async () => {
    const fetchMock = mockFetch()
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    fireEvent.click(screen.getByRole('button', { name: 'Archived' }))
    await screen.findByText('Honeywell Group')
    expect(screen.queryByText('Okafor & Partners'), 'active rows must be gone under Archived').toBeNull()
    expect(screen.queryByText('Beta Traders')).toBeNull()
    expect(new URL(entitiesCallUrls(fetchMock).slice(-1)[0]).searchParams.get('status')).toBe('archived')

    fireEvent.click(screen.getByRole('button', { name: 'All' }))
    await screen.findByText('Okafor & Partners')
    await screen.findByText('Honeywell Group')
    expect(screen.getByText('Beta Traders')).toBeDefined()
    expect(new URL(entitiesCallUrls(fetchMock).slice(-1)[0]).searchParams.has('status'), 'returning to All must send no status param').toBe(false)
  })
})

describe('ClientsView: the header count follows the filter', () => {
  it('count reflects the full roster on All, then each narrowed count as the filter changes', async () => {
    mockFetch()
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await screen.findByText('Okafor & Partners')
    expect(headerCountText(), 'the default All position must show the full roster count').toContain('3 companies')

    fireEvent.click(screen.getByRole('button', { name: 'Active' }))
    await waitFor(() => expect(headerCountText()).toContain('2 companies'))

    fireEvent.click(screen.getByRole('button', { name: 'Archived' }))
    await waitFor(() => expect(headerCountText()).toContain('1 companies'))
  })
})

// QA (task-336, BUG-01-10, Mode B): every fixture above sets pagination.total ===
// rows.length, so a shown/total argument swap at the ClientsView call site
// (portfolioCountLabel(total, shown) instead of (shown, total)) is invisible to them --
// mutation-verified. This pins a genuine shown<total (server-truncated) response so the
// call-site argument order is actually exercised, not just portfolioCountLabel's own
// pure-function tests in portfolio.test.ts.
describe('ClientsView: the header count keeps shown/total in order when the server reports more than was returned', () => {
  it('renders "<rendered rows> of <server total> companies", not the reverse', async () => {
    const fetchMock = vi.fn((url: string) => {
      if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
      return Promise.resolve(entitiesResponse(ACTIVE_ROWS)).then((r) => ({
        ...r,
        json: () => Promise.resolve({ entities: ACTIVE_ROWS, pagination: { limit: 2, offset: 0, total: 5 } }),
      }))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    expect(headerCountText(), 'shown (2 rendered rows) must come first, server total (5) second').toContain('2 of 5 companies')
    expect(headerCountText()).not.toContain('5 of 2')
  })
})

describe('ClientsView: a narrowing position with zero rows renders a filter-specific empty state', () => {
  it('Archived with no archived clients shows a filter-specific message, not "No entities yet"', async () => {
    const activeOnly = [entity({ id: 'x1', name: 'Solo Client', status: 'active' })]
    const fetchMock = vi.fn((url: string) => {
      if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
      const status = new URL(url).searchParams.get('status')
      const rows = status === 'archived' ? [] : activeOnly
      return Promise.resolve(entitiesResponse(rows))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ClientsView ctx={clientsCtx(activeOnly)} />)
    await screen.findByText('Solo Client')

    fireEvent.click(screen.getByRole('button', { name: 'Archived' }))

    // Copy chosen here (not pinned by the story) -- see the RED-run report.
    await screen.findByText('No clients match this filter')
    expect(screen.queryByText('No entities yet'), 'the generic empty state is wrong while active clients exist').toBeNull()
  })
})

// QA (task-336, BUG-01-10, Mode B): EntityFormModal's onSuccess must fire BOTH
// refetchEntities() (the switcher's shared roster) and filtered.run() (this view's own
// rows) -- ClientsView no longer reads ctx.entities for its rows at all, so a create that
// only refreshed ctx.entities would leave the new client invisible here until a manual
// reload. Drives a real Add-client round trip through the modal, not a direct prop call.
describe('ClientsView: creating a client refetches BOTH the switcher roster and this view\'s filtered rows', () => {
  it('after Add client succeeds, refetchEntities fires once and the new client appears without a manual reload', async () => {
    const newEntity = entity({ id: 'new1', name: 'Fresh Co', status: 'active' })
    let created = false
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
      if (init?.method === 'POST') {
        created = true
        return Promise.resolve({ ok: true, status: 201, json: () => Promise.resolve(newEntity) })
      }
      const status = new URL(url).searchParams.get('status')
      const rows = created ? [...rowsForStatus(status), newEntity] : rowsForStatus(status)
      return Promise.resolve(entitiesResponse(rows))
    })
    vi.stubGlobal('fetch', fetchMock)

    const ctx = clientsCtx(ALL_ROWS)
    const refetchEntities = vi.fn()
    ctx.refetchEntities = refetchEntities

    render(<ClientsView ctx={ctx} />)
    await screen.findByText('Okafor & Partners')

    fireEvent.click(screen.getByRole('button', { name: 'Add client' }))
    const dialog = await screen.findByRole('dialog')

    const nameInput = dialog.querySelectorAll('.pf-input')[0] as HTMLInputElement
    const tinInput = within(dialog).getByPlaceholderText('########-####')
    fireEvent.change(nameInput, { target: { value: 'Fresh Co' } })
    fireEvent.change(tinInput, { target: { value: '00000000099' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add client' }))

    await waitFor(() => expect(created, 'the create POST never fired').toBe(true))

    // Proves filtered.run() actually refired: the row list is driven solely by
    // `filtered.data`, never ctx.entities, so this can only appear via a refetch.
    await screen.findByText('Fresh Co')
    expect(refetchEntities, 'onSuccess must also call ctx.refetchEntities for the switcher').toHaveBeenCalledTimes(1)
  })
})

describe("ClientsView: ctx.entities (the switcher's roster) is unchanged by the filter", () => {
  it('the roster reference/content passed in survives a filter change untouched', async () => {
    mockFetch()
    const ctx = clientsCtx(ALL_ROWS)
    const rosterBefore = ctx.entities

    render(<ClientsView ctx={ctx} />)
    await screen.findByText('Okafor & Partners')

    fireEvent.click(screen.getByRole('button', { name: 'Active' }))
    await waitFor(() => expect(screen.queryByText('Honeywell Group')).toBeNull())

    expect(ctx.entities, 'ClientsView must never mutate/replace the shared roster it was given').toBe(rosterBefore)
    expect(ctx.entities).toEqual(ALL_ROWS)
  })
})

// --- Archive/restore row action (BUG-01-12) -----------------------------------------
// Routes a POST to .../offboard|onboard as the archive action; everything else falls
// through to the plain entities/rollup responses. `rows` is fixed (no filter-position
// interplay needed by these tests) -- unlike mockFetch() above, which keys off the
// request's own status param.
function isArchiveUrl(url: string): boolean {
  return /\/(offboard|onboard)$/.test(new URL(url).pathname)
}

function mockFetchArchiveAware(rows: Entity[], archiveOutcome: { ok: boolean; status: number; body: unknown } = { ok: true, status: 200, body: null }) {
  const archiveCalls: Array<{ url: string; method: string }> = []
  const fetchMock = vi.fn((url: string, init?: RequestInit) => {
    if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
    if (isArchiveUrl(url)) {
      archiveCalls.push({ url, method: init?.method ?? '' })
      const body = archiveOutcome.ok ? (archiveOutcome.body ?? rows[0]) : archiveOutcome.body
      return Promise.resolve({ ok: archiveOutcome.ok, status: archiveOutcome.status, json: () => Promise.resolve(body) })
    }
    return Promise.resolve(entitiesResponse(rows))
  })
  vi.stubGlobal('fetch', fetchMock)
  return { fetchMock, archiveCalls }
}

// Only genuine GET .../entities list calls -- excludes rollup AND the offboard/onboard
// sub-resource POSTs (whose pathname does not end in "/entities"), so a POST firing
// cannot be miscounted as filtered.run() having refetched the list.
function entitiesListGetCalls(fetchMock: ReturnType<typeof mockFetchArchiveAware>['fetchMock']): number {
  return fetchMock.mock.calls.filter(([url, init]: [string, RequestInit?]) => {
    if (isRollupUrl(url)) return false
    if (init?.method && init.method !== 'GET') return false
    return new URL(url).pathname.endsWith('/entities')
  }).length
}

function rowFor(name: string): HTMLElement {
  return screen.getByText(name).closest('.pf-list-row') as HTMLElement
}

describe('ClientsView: archive/restore action, per row (BUG-01-12)', () => {
  it('[archive-arms-then-confirms] arms on first click (no request yet), fires on second (POST offboard)', async () => {
    const { archiveCalls } = mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    const action = within(rowFor('Okafor & Partners')).getByRole('button')

    fireEvent.click(action)
    expect(archiveCalls, 'the first click must only arm -- it must not fire a request').toHaveLength(0)

    fireEvent.click(action)
    await waitFor(() => expect(archiveCalls).toHaveLength(1))
    expect(archiveCalls[0].url).toContain('/offboard')
  })

  // QA (BUG-01-12, Mode B): every other test in this block only exercises ACTIVE_ROWS
  // (offboard). Nothing local proved the archived branch calls onboardEntity rather than
  // reusing offboardEntity -- only the e2e spec (deploy-gate) restores a row end to end.
  it('restore on an archived row arms then fires POST onboard, never offboard', async () => {
    const { archiveCalls } = mockFetchArchiveAware(ARCHIVED_ROWS)
    render(<ClientsView ctx={clientsCtx(ARCHIVED_ROWS)} />)
    await screen.findByText('Honeywell Group')

    const action = within(rowFor('Honeywell Group')).getByRole('button')

    fireEvent.click(action) // arm
    expect(archiveCalls, 'the first click must only arm -- it must not fire a request').toHaveLength(0)

    fireEvent.click(action) // confirm
    await waitFor(() => expect(archiveCalls).toHaveLength(1))
    expect(archiveCalls[0].url).toContain('/onboard')
    expect(archiveCalls[0].url).not.toContain('/offboard')
  })

  it('clicking the action never opens the edit modal', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    const action = within(rowFor('Okafor & Partners')).getByRole('button')
    fireEvent.click(action)

    expect(screen.queryByRole('dialog'), 'the action must e.stopPropagation() -- the row-click edit modal must not open').toBeNull()
  })

  it('on success, both refetchEntities and filtered.run fire (table and switcher stay in agreement)', async () => {
    const refetchEntities = vi.fn()
    const { fetchMock } = mockFetchArchiveAware(ACTIVE_ROWS)
    const ctx = clientsCtx(ACTIVE_ROWS)
    ctx.refetchEntities = refetchEntities
    render(<ClientsView ctx={ctx} />)
    await screen.findByText('Okafor & Partners')

    const entitiesGetsBefore = entitiesListGetCalls(fetchMock)
    const action = within(rowFor('Okafor & Partners')).getByRole('button')

    fireEvent.click(action) // arm
    fireEvent.click(action) // confirm

    await waitFor(() => expect(refetchEntities, 'onSuccess must call ctx.refetchEntities so the switcher follows').toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(entitiesListGetCalls(fetchMock), "onSuccess must also refetch this view's own filtered rows").toBeGreaterThan(entitiesGetsBefore),
    )
  })

  // QA (BUG-01-12, Mode B): the test above only counts refetch calls -- it never proves
  // the row's OWN pill actually flips, because mockFetchArchiveAware's GET handler is
  // stateless (always returns the same fixed `rows`, regardless of any prior archive
  // call). This stands up a stateful GET so AC #4's pill flip is observable locally, not
  // provable only at the e2e/deploy-gate layer against a real backend.
  it('on success, the refetched row itself flips from ACTIVE to ARCHIVED', async () => {
    let status: 'active' | 'archived' = 'active'
    const fetchMock = vi.fn((url: string) => {
      if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
      if (isArchiveUrl(url)) {
        status = 'archived'
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(entity({ id: 'a1', status })) })
      }
      return Promise.resolve(entitiesResponse([entity({ id: 'a1', name: 'Okafor & Partners', status })]))
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ClientsView ctx={clientsCtx([entity({ id: 'a1', name: 'Okafor & Partners', status: 'active' })])} />)
    await screen.findByText('Okafor & Partners')

    const row = rowFor('Okafor & Partners')
    expect(within(row).getByText('ACTIVE', { exact: true })).toBeDefined()

    const action = within(row).getByRole('button')
    fireEvent.click(action) // arm
    fireEvent.click(action) // confirm

    await within(row).findByText('ARCHIVED', { exact: true })
    expect(within(row).queryByText('ACTIVE', { exact: true }), 'the stale ACTIVE pill must not linger').toBeNull()
  })

  it('on a 409, the server message renders inline on the row and the row is not reported as changed', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS, { ok: false, status: 409, body: { error: 'redundant transition' } })
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    const row = rowFor('Okafor & Partners')
    const action = within(row).getByRole('button')

    fireEvent.click(action) // arm
    fireEvent.click(action) // confirm -> 409

    await within(row).findByText('redundant transition')
    expect(within(row).getByText('ACTIVE', { exact: true }), 'a failed transition must not flip the row').toBeDefined()
  })

  // Gap flagged in architecture validation: archiveActionFor's `armed` is two-state (not
  // bulkPhaseReducer's idle/armed/submitting) -- nothing stops a second confirm click from
  // re-entering the handler while the first request is still in flight. The component
  // needs its own in-flight guard.
  it('a fast double-click on confirm fires exactly one POST', async () => {
    const { archiveCalls } = mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    const action = within(rowFor('Okafor & Partners')).getByRole('button')

    fireEvent.click(action) // arm
    fireEvent.click(action) // confirm #1
    fireEvent.click(action) // confirm #2, before #1's promise has settled

    expect(archiveCalls, 'a fast double-click on confirm must not fire two POSTs').toHaveLength(1)
  })

  // QA (BUG-01-12, Mode B): gridTemplateColumns is two separate literals (header + row) --
  // nothing else in this file catches the two drifting apart. 5 tracks, header and row
  // agreeing exactly, so the Action column stays lined up with its own header label.
  it('the header and every row share the same 5-track grid (the Action column stays aligned)', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS)
    const { container } = render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    const head = container.querySelector('.pf-list-head') as HTMLElement
    const row = rowFor('Okafor & Partners')

    // Tokenize on top-level whitespace only -- a naive \s+ split would cut
    // "minmax(160px, 1fr)" into two tracks at its own internal comma-space.
    const tracks = (v: string) => v.trim().match(/(?:[^\s(]|\([^)]*\))+/g) ?? []
    const headCols = tracks(head.style.gridTemplateColumns)
    const rowCols = tracks(row.style.gridTemplateColumns)

    expect(headCols, 'header must carry the fifth (Action) track').toHaveLength(5)
    expect(rowCols, 'row must carry the fifth (Action) track').toHaveLength(5)
    expect(rowCols).toEqual(headCols)
  })
})

// AC-4, the rendered half. ClientsView.test.ts pins healthPillStyle's copy directly; this
// pins that HealthCell still routes the joined rollup count through it, so the widened
// overlay reaches the pill as a bigger number and nothing else.
function clientRow(entityId: string, needsAttention: number) {
  return {
    entity_id: entityId,
    entity_name: entityId,
    counts: { draft: 0, validated: 0, queued: 0, submitted: 0, accepted: 0, rejected: 0, failed: 0 },
    needs_attention: needsAttention,
    awaiting_approval: 0,
    metrics: {},
    top_violations: [],
  }
}

function mockFetchWithRollup(rollup: Rollup) {
  const fetchMock = vi.fn((url: string) => {
    if (isRollupUrl(url)) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(rollup) })
    return Promise.resolve(entitiesResponse(rowsForStatus(new URL(url).searchParams.get('status'))))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('ClientsView: the health pill is unchanged by the needs-attention widening', () => {
  it('a joined count renders as the plural pill, and a row with no rollup entry reads NO INVOICES YET', async () => {
    // a1 = Okafor & Partners (2 needing attention), a2 = Beta Traders (clear), r1 = Honeywell
    // Group (absent from `clients` -- INNER JOIN, zero invoices).
    mockFetchWithRollup({ ...ZERO_ROLLUP, clients: [clientRow('a1', 2), clientRow('a2', 0)] })

    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    await within(rowFor('Okafor & Partners')).findByText('2 NEED ATTENTION')
    expect(within(rowFor('Beta Traders')).getByText('ALL CLEAR')).toBeDefined()
    expect(within(rowFor('Honeywell Group')).getByText('NO INVOICES YET')).toBeDefined()
  })

  // QA adversarial, the asymmetry the Reports carve-out creates. Same violation count on
  // both renders; only the overlay widens, and this pill is meant to follow it.
  it('a widened overlay moves the pill, and the violation count beside it never does', async () => {
    const withViolations = (needsAttention: number) => ({ ...clientRow('a1', needsAttention), metrics: { blocked_by_rules: { num: 1, den: 20 } } })

    mockFetchWithRollup({ ...ZERO_ROLLUP, clients: [withViolations(3)] })
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await screen.findByText('Okafor & Partners')
    await within(rowFor('Okafor & Partners')).findByText('3 NEED ATTENTION')

    cleanup()
    vi.unstubAllGlobals()

    mockFetchWithRollup({ ...ZERO_ROLLUP, clients: [withViolations(9)] })
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await screen.findByText('Okafor & Partners')
    await within(rowFor('Okafor & Partners')).findByText('9 NEED ATTENTION')
  })
})
