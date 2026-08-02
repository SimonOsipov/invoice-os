// @vitest-environment jsdom
// Mode A RED specs -- ClientsView has no status filter yet: it renders `ctx.entities`
// verbatim and never issues its own entities fetch. These pin the All/Active/Archived
// control (defaulting to All, nothing hidden), a server-side status request per position,
// the filter-aware header count, a filter-specific empty state, and ctx.entities staying
// untouched by the filter. Mirrors CustomersView.test.tsx/ReportsView.test.tsx's own
// mock-fetch-by-pathname idiom, since ClientsView also fires an independent rollup fetch.
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
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

const ZERO_ROLLUP = {
  totals: { counts: { draft: 0, validated: 0, queued: 0, submitted: 0, accepted: 0, rejected: 0, failed: 0 }, needs_attention: 0 },
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
