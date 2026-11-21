// @vitest-environment jsdom
// Pins the All/Active/Archived control (defaulting to All, nothing hidden), a
// server-side status request per position, the filter-aware header count, a
// filter-specific empty state, and ctx.entities staying untouched by the filter. Mirrors
// CustomersView.test.tsx/ReportsView.test.tsx's own mock-fetch-by-pathname idiom, since
// ClientsView also fires an independent rollup fetch.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import { CLIENT_ADMIN_ONLY } from '../lib/members'
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
function rowsForStatus(status: string | null, q: string | null = null): Entity[] {
  const subset = status === 'active' ? ACTIVE_ROWS : status === 'archived' ? ARCHIVED_ROWS : ALL_ROWS
  return subset.filter((e) => matchesQ(e, q))
}

// The server's q contract (store.go:122-125): case-insensitive substring of name or TIN.
function matchesQ(e: Entity, q: string | null): boolean {
  if (q == null || q === '') return true
  const needle = q.toLowerCase()
  return e.name.toLowerCase().includes(needle) || (e.tin ?? '').toLowerCase().includes(needle)
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
    const params = new URL(url).searchParams
    return Promise.resolve(entitiesResponse(rowsForStatus(params.get('status'), params.get('q'))))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function entitiesCallUrls(fetchMock: ReturnType<typeof mockFetch>): string[] {
  return fetchMock.mock.calls.map((c) => c[0] as string).filter((url) => !isRollupUrl(url))
}

function clientsCtx(entities: Entity[], over: Record<string, unknown> = {}): PlatformCtx {
  const ctx = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    user: { name: 'Firm User', initials: 'FU', tenantName: 'Acme Co', verified: true },
    entities,
    entitiesState: 'ready',
    entitiesError: null,
    refetchEntities: () => {},
    members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: 'admin', status: 'active', isYou: true }],
    membersState: 'ready',
    ...over,
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

    fireEvent.click(within(screen.getByTestId('clients-filter-bar')).getByRole('button', { name: 'Archived' }))

    // Copy chosen here (not pinned by the story) -- see the RED-run report.
    await screen.findByText('No clients match this filter')
    expect(within(screen.getByTestId('clients-filter-bar')).getAllByRole('button').map((b) => b.textContent), 'the bar stays while the filtered empty state shows').toEqual(['All', 'Active', 'Archived'])
    expect(screen.queryByText('No entities yet'), 'the generic empty state is wrong while active clients exist').toBeNull()
  })
})

const SELF = (role: string) => [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role, status: 'active', isYou: true }]

describe('ClientsView: Add client follows the role (LOGFIX-06-03)', () => {
  it.each(['preparer', 'reviewer'])('Clients: a non-admin sees Add client disabled with the reason (%s)', async (role) => {
    mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS, { members: SELF(role) })} />)
    await screen.findByText('Okafor & Partners')
    const add = screen.getByRole('button', { name: 'Add client' }) as HTMLButtonElement
    expect(add.disabled).toBe(true)
    expect(add.title).toBe(CLIENT_ADMIN_ONLY)
    expect(document.getElementById(add.getAttribute('aria-describedby') as string)?.textContent).toBe(CLIENT_ADMIN_ONLY)
    for (const name of ['All', 'Active', 'Archived']) expect((screen.getByRole('button', { name }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('FBAR the admin-only note stays with Add client, above the bar', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS, { members: SELF('reviewer') })} />)
    await screen.findByText('Okafor & Partners')

    const header = screen.getByRole('heading', { level: 1, name: 'Client portfolio' }).parentElement!.parentElement as HTMLElement
    const note = screen.getByTestId('clients-admin-only')
    const bar = screen.getByTestId('clients-filter-bar')
    expect(header.compareDocumentPosition(note) & Node.DOCUMENT_POSITION_FOLLOWING, 'the note follows the header').toBeTruthy()
    expect(note.compareDocumentPosition(bar) & Node.DOCUMENT_POSITION_FOLLOWING, 'the note precedes the bar').toBeTruthy()
    expect(within(bar).queryByRole('button', { name: 'Add client' }), 'no action in the bar').toBeNull()
  })

  it('Clients: a non-admin with no clients sees No company created and a disabled Add client', async () => {
    mockFetchArchiveAware([])
    render(<ClientsView ctx={clientsCtx([], { members: SELF('preparer') })} />)
    await screen.findByText('No company created')
    expect(screen.getByText('Your workspace admin adds the company. You can start when it exists.')).toBeTruthy()
    expect(screen.queryByText('Add your first business entity to get started.')).toBeNull()
    expect((screen.getByRole('button', { name: 'Add client' }) as HTMLButtonElement).disabled).toBe(true)
  })

  const SELF_ACCESS = (role: string, clientAccess: 'all' | string[]) => [{ ...SELF(role)[0], clientAccess }]

  it('ClientsView_aMemberWithNoClientsSeesTheCause', async () => {
    mockFetchArchiveAware([])
    render(<ClientsView ctx={clientsCtx([], { members: SELF_ACCESS('reviewer', []) })} />)
    await screen.findByText('No clients assigned')
    expect(screen.getByText('An admin has not given you any clients yet.')).toBeTruthy()
    expect(screen.queryByText('No company created')).toBeNull()
  })

  it('ClientsView_anAllClientsMemberWithNoClientsKeepsTodaysCopy', async () => {
    mockFetchArchiveAware([])
    render(<ClientsView ctx={clientsCtx([], { members: SELF_ACCESS('preparer', 'all') })} />)
    await screen.findByText('No company created')
    expect(screen.queryByText('An admin has not given you any clients yet.')).toBeNull()
  })

  it('ClientsView_theFilterEmptyStateIsUnchanged', async () => {
    mockFetchArchiveAware([])
    render(<ClientsView ctx={clientsCtx([], { members: SELF_ACCESS('reviewer', []) })} />)
    await screen.findByText('No clients assigned')
    fireEvent.click(screen.getByRole('button', { name: 'Archived' }))
    await screen.findByText('No clients match this filter')
    expect(screen.queryByText('An admin has not given you any clients yet.')).toBeNull()
  })

  it.each(['loading', 'error'])('Clients: no Add client and no note while the roster is %s; rows keep today\'s behaviour', async (membersState) => {
    mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS, { members: [], membersState })} />)
    await screen.findByText('Okafor & Partners')
    expect(screen.queryByRole('button', { name: 'Add client' })).toBeNull()
    expect(screen.queryByTestId('clients-admin-only')).toBeNull()
    const archive = within(rowFor('Okafor & Partners')).getByRole('button') as HTMLButtonElement
    expect(archive.disabled).toBe(false)
    fireEvent.click(rowFor('Okafor & Partners'))
    expect(screen.getByRole('dialog', { name: 'Edit client' })).toBeTruthy()
  })

  it.each(['preparer', 'reviewer'])('Clients: a non-admin cannot archive or restore (%s)', async (role) => {
    const { archiveCalls } = mockFetchArchiveAware(ALL_ROWS)
    render(<ClientsView ctx={clientsCtx(ALL_ROWS, { members: SELF(role) })} />)
    await screen.findByText('Honeywell Group')
    for (const name of ['Okafor & Partners', 'Beta Traders', 'Honeywell Group']) {
      const btn = within(rowFor(name)).getByRole('button') as HTMLButtonElement
      expect(btn.disabled, name).toBe(true)
      expect(btn.title).toBe(CLIENT_ADMIN_ONLY)
      expect(document.getElementById(btn.getAttribute('aria-describedby') as string)?.textContent).toBe(CLIENT_ADMIN_ONLY)
      fireEvent.click(btn)
      fireEvent.click(btn)
    }
    expect(archiveCalls).toHaveLength(0)
  })

  it.each(['preparer', 'reviewer'])('Clients: a non-admin row click opens the dialog read-only (%s)', async (role) => {
    const full = entity({ id: 'f1', name: 'Full Co', email: 'a@b.co', telephone: '0801', street: '1 Main', city: 'Lagos' } as Partial<Entity>)
    const fetchMock = mockFetchArchiveAware([full]).fetchMock
    render(<ClientsView ctx={clientsCtx([full], { members: SELF(role) })} />)
    await screen.findByText('Full Co')
    fireEvent.click(rowFor('Full Co'))
    const dialog = screen.getByRole('dialog', { name: 'Client details' })
    const inputs = [...dialog.querySelectorAll('input')] as HTMLInputElement[]
    expect(inputs).toHaveLength(13)
    for (const i of inputs) expect(i.disabled).toBe(true)
    const values = inputs.map((i) => i.value)
    for (const v of ['Full Co', '00000000001', 'RC123456', 'logistics', '12 Marina Rd, Lagos', 'a@b.co', '0801', '1 Main', 'Lagos']) expect(values).toContain(v)
    expect(within(dialog).queryByRole('button', { name: 'Save changes' })).toBeNull()
    expect(within(dialog).getByTestId('entity-readonly-note').textContent).toBe(CLIENT_ADMIN_ONLY)
    fireEvent.click(within(dialog).getByText('Close', { selector: 'button' })) // the footer button; the X shares its aria-label
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toHaveLength(0)
  })

  it('Clients: an admin keeps every control enabled and sees no note', async () => {
    mockFetchArchiveAware(ALL_ROWS)
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await screen.findByText('Honeywell Group')
    expect((screen.getByRole('button', { name: 'Add client' }) as HTMLButtonElement).disabled).toBe(false)
    for (const name of ['Okafor & Partners', 'Beta Traders', 'Honeywell Group']) {
      expect((within(rowFor(name)).getByRole('button') as HTMLButtonElement).disabled, name).toBe(false)
    }
    expect(screen.queryByTestId('clients-admin-only')).toBeNull()
    fireEvent.click(rowFor('Okafor & Partners'))
    expect(screen.getByRole('dialog', { name: 'Edit client' })).toBeTruthy()
    expect(screen.queryByTestId('entity-readonly-note')).toBeNull()
  })

  it('Clients: the disabled recipe matches Invite people', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS, { members: SELF('preparer') })} />)
    await screen.findByText('Okafor & Partners')
    for (const el of [screen.getByRole('button', { name: 'Add client' }), within(rowFor('Okafor & Partners')).getByRole('button')] as HTMLButtonElement[]) {
      expect(el.disabled).toBe(true)
      expect(el.style.opacity).toBe('0.45')
      expect(el.style.cursor).toBe('not-allowed')
      expect(el.style.filter).toBe('none')
    }
  })

  it('Clients: an admin demoted mid-session sees the server\'s 403 on the row', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS, { ok: false, status: 403, body: { error: 'only an admin can change a client' } })
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS, { refetchMembers: vi.fn() })} />)
    await screen.findByText('Okafor & Partners')
    const row = rowFor('Okafor & Partners')
    const action = within(row).getByRole('button')
    fireEvent.click(action)
    fireEvent.click(action)
    await within(row).findByText('only an admin can change a client')
    expect(within(row).getByRole('button').textContent).toBe('Archive')
  })

  it('Clients: a 403 on archive refetches the roster', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS, { ok: false, status: 403, body: { error: 'only an admin can change a client' } })
    const refetchMembers = vi.fn()
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS, { refetchMembers })} />)
    await screen.findByText('Okafor & Partners')
    const action = within(rowFor('Okafor & Partners')).getByRole('button')
    fireEvent.click(action)
    fireEvent.click(action)
    await waitFor(() => expect(refetchMembers).toHaveBeenCalledTimes(1))
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
      const params = new URL(url).searchParams
      const base = rowsForStatus(params.get('status'), params.get('q'))
      const rows = created ? [...base, newEntity] : base
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
    const params = new URL(url).searchParams
    return Promise.resolve(entitiesResponse(rowsForStatus(params.get('status'), params.get('q'))))
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

// --- the v2 roster (CL rows) ------------------------------------------
// Fixtures are typed by hand: ClientsView.test.ts shadows this file out of tsc.
function filterButton(name: string): HTMLElement {
  return screen.getByRole('button', { name })
}

describe('ClientsView: the v2 roster (RESKIN2-05-01)', () => {
  it('CL-01 the h1 takes the heading rule\'s weight', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    const h1 = screen.getByRole('heading', { level: 1, name: 'Client portfolio' })
    expect(h1.style.fontSize, 'control: the h1 keeps its size').toBe('26px')
    expect(h1.style.fontWeight, 'the h1 must carry no inline font-weight').toBe('')
  })

  it('CL-02 the active filter is filled primary; no filter carries a radius', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    const all = filterButton('All').style
    expect(all.background, 'control: the active filter is filled with --action').toBe('var(--action)')
    expect(all.color, 'the active filter label is --primary-foreground').toBe('var(--primary-foreground)')
    for (const name of ['Active', 'Archived']) {
      const s = filterButton(name).style
      expect(s.background, `${name} resting background`).toBe('var(--bg-2)')
      expect(s.color, `${name} resting colour`).toBe('var(--fg-2)')
    }
    for (const name of ['All', 'Active', 'Archived']) {
      expect(filterButton(name).style.borderRadius, `${name} must carry no inline radius (pf-chip owns it)`).toBe('')
    }
  })

  it('CL-03 the status filters sit in their own bar, and the header holds only Add client', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    const header = screen.getByRole('heading', { level: 1, name: 'Client portfolio' }).parentElement!.parentElement as HTMLElement
    expect(within(header).getByRole('button', { name: 'Add client' })).toBeTruthy()
    for (const name of ['All', 'Active', 'Archived']) expect(within(header).queryByRole('button', { name }), `${name} must not sit in the header`).toBeNull()

    const bar = screen.getByTestId('clients-filter-bar')
    expect(within(bar).getAllByRole('button').map((b) => b.textContent)).toEqual(['All', 'Active', 'Archived'])
    const bg = (name: string) => within(bar).getByRole('button', { name }).style.background
    expect(bg('All')).toBe('var(--action)')
    expect(bg('Active')).toBe('var(--bg-2)')
    expect(bg('Archived')).toBe('var(--bg-2)')
    expect(header.compareDocumentPosition(bar) & Node.DOCUMENT_POSITION_FOLLOWING, 'the bar follows the header').toBeTruthy()
    expect(bar.compareDocumentPosition(rowFor('Okafor & Partners')) & Node.DOCUMENT_POSITION_FOLLOWING, 'the roster follows the bar').toBeTruthy()
  })

  it('FBAR the Clients bar renders in loading, error and with no Add client', async () => {
    const bar = () => {
      const el = screen.getByTestId('clients-filter-bar')
      expect(within(el).getByRole('textbox', { name: 'Search clients' }), 'the search box stays').toBeTruthy()
      return within(el).getAllByRole('button').map((b) => b.textContent)
    }
    const chips = ['All', 'Active', 'Archived']

    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    expect(bar(), 'loading').toEqual(chips)
    cleanup()

    vi.stubGlobal('fetch', vi.fn((url: string) => Promise.resolve(isRollupUrl(url) ? rollupResponse() : { ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) })))
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Something went wrong')
    expect(bar(), 'error').toEqual(chips)
    cleanup()

    mockFetchArchiveAware(ACTIVE_ROWS)
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS, { members: [], membersState: 'loading' })} />)
    await screen.findByText('Okafor & Partners')
    expect(screen.queryByRole('button', { name: 'Add client' })).toBeNull()
    expect(bar(), 'no Add client').toEqual(chips)
  })

  it('CL-04 status and health are 4px mono chips with no dot, in a plain wrapper', async () => {
    mockFetchWithRollup({ ...ZERO_ROLLUP, clients: [clientRow('a1', 1)] })
    render(<ClientsView ctx={clientsCtx([entity({ id: 'a1', name: 'Okafor & Partners', status: 'active' })])} />)
    await screen.findByText('Okafor & Partners')
    const row = rowFor('Okafor & Partners')
    const chips = [await within(row).findByText('ACTIVE', { exact: true }), await within(row).findByText('1 NEEDS ATTENTION', { exact: true })]

    expect(chips).toHaveLength(2)
    for (const chip of chips) {
      const what = chip.textContent
      expect(chip.classList.contains('mono'), `${what} is a mono span`).toBe(true)
      expect(chip.style.borderRadius, `${what} corner`).toBe('var(--radius-sm)')
      expect(chip.style.padding, `${what} padding`).toBe('3px 9px')
      expect(chip.style.letterSpacing, `${what} tracking`).toBe('0.04em')
      expect(chip.style.display, `${what} display`).toBe('inline-flex')
      expect(chip.style.fontSize, `${what} size`).toBe('10px')
      expect(chip.style.fontWeight, `${what} weight`).toBe('600')
      expect(chip.childElementCount, `${what} holds no dot`).toBe(0)
      const wrapper = chip.parentElement as HTMLElement
      expect(wrapper.tagName, `${what} sits in a plain span`).toBe('SPAN')
      expect(wrapper.getAttribute('style'), `${what} wrapper takes no style`).toBeNull()
    }
  })

  it('CL-05 the avatar is a circle; Sector is 13px', async () => {
    mockFetchArchiveAware([entity({ id: 'a1', name: 'Okafor & Partners', sector: 'Retail' })])
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    expect(screen.getByText('OP').style.borderRadius, 'the initials bubble is a circle').toBe('50%')
    expect(screen.getByText('Retail').style.fontSize, 'Sector is 13px').toBe('13px')
  })

  it('CL-06 the armed action reads red, with its notice in the notice typography', async () => {
    const rowA = entity({ id: 'a1', name: 'Okafor & Partners', status: 'active' })
    const rowR = entity({ id: 'r1', name: 'Honeywell Group', status: 'archived' })
    const rowB = entity({ id: 'b1', name: 'Beta Traders', status: 'active' })
    mockFetchArchiveAware([rowA, rowR, rowB])
    const ctx = clientsCtx([rowA, rowR, rowB])
    ctx.activeEntity = rowA as unknown as PlatformCtx['activeEntity']
    render(<ClientsView ctx={ctx} />)
    await screen.findByText('Okafor & Partners')

    const buttonOf = (name: string) => within(rowFor(name)).getByRole('button')
    expect(buttonOf('Okafor & Partners').style.color, 'resting Archive is --fg-1').toBe('var(--fg-1)')
    expect(buttonOf('Honeywell Group').style.color, 'resting Restore is --fg-1').toBe('var(--fg-1)')

    fireEvent.click(buttonOf('Okafor & Partners'))
    const a = buttonOf('Okafor & Partners')
    expect(a.textContent).toBe('Confirm archive')
    expect(a.style.color, 'armed Archive reads red').toBe('var(--status-red-text)')
    expect(a.style.height).toBe('30px')
    expect(a.style.padding).toBe('0px 12px')
    expect(a.style.fontSize).toBe('12.5px')
    const notice = within(rowFor('Okafor & Partners')).getByText("You'll stay in this workspace after this.")
    expect(notice.style.fontSize, 'notice size').toBe('11.5px')
    expect(notice.style.lineHeight, 'notice leading').toBe('1.4')
    expect(buttonOf('Beta Traders').style.color, 'an unarmed sibling stays --fg-1').toBe('var(--fg-1)')

    fireEvent.click(buttonOf('Honeywell Group'))
    const r = buttonOf('Honeywell Group')
    expect(r.textContent).toBe('Confirm restore')
    expect(r.style.color, 'armed Restore reads red').toBe('var(--status-red-text)')
  })

  it('CL-07 the roster carries no v1 vocabulary (boundary)', async () => {
    const rows = [entity({ id: 'a1', name: 'Okafor & Partners', status: 'active' }), entity({ id: 'r1', name: 'Honeywell Group', status: 'archived' })]
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) =>
        Promise.resolve(isRollupUrl(url) ? { ok: true, status: 200, json: () => Promise.resolve({ ...ZERO_ROLLUP, clients: [clientRow('a1', 1)] }) } : entitiesResponse(rows)),
      ),
    )
    const { container } = render(<ClientsView ctx={clientsCtx(rows)} />)
    await screen.findByText('Okafor & Partners')
    await within(rowFor('Okafor & Partners')).findByText('1 NEEDS ATTENTION')

    const html = container.innerHTML
    const radii = Array.from(html.matchAll(/(?<![-\w])border-radius:\s*([^;"]+)/g), (m) => m[1].trim())
    expect(radii.filter((r) => r === 'var(--radius-sm)').length, 'control: the chips carry --radius-sm').toBeGreaterThanOrEqual(2)
    expect(radii.length).toBeGreaterThan(0)
    for (const r of radii) expect(['var(--radius-sm)', 'var(--radius-md)', '50%'], `corner "${r}"`).toContain(r)
    for (const needle of ['oklch', 'gradient', 'box-shadow']) expect(html, needle).not.toContain(needle)
  })

  it('CL-08 a refused archive reads in the notice typography (error)', async () => {
    mockFetchArchiveAware(ACTIVE_ROWS, { ok: false, status: 409, body: { error: 'redundant transition' } })
    render(<ClientsView ctx={clientsCtx(ACTIVE_ROWS)} />)
    await screen.findByText('Okafor & Partners')

    const action = within(rowFor('Okafor & Partners')).getByRole('button')
    fireEvent.click(action) // arm
    fireEvent.click(action) // confirm -> 409
    const message = await within(rowFor('Okafor & Partners')).findByText('redundant transition')

    expect(message.style.color, 'control: the message stays red').toBe('var(--status-red-text)')
    expect(message.style.fontSize).toBe('11.5px')
    expect(message.style.lineHeight).toBe('1.4')
  })
})

// --- the search box (CSRCH-01-02) ---------------------------------------------------
// Fake timers drive the 300ms debounce; flush() settles fetch promises without moving the clock.
const SEARCH_ROWS: Entity[] = [
  entity({ id: 's1', name: 'Alpha Co', tin: '11122233301', status: 'active' }),
  entity({ id: 's2', name: 'Abby Ltd', tin: '44455566602', status: 'active' }),
  entity({ id: 's3', name: 'Zed Works', tin: '77788899903', status: 'archived' }),
]

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

const flush = () => advance(0)

function searchBox(): HTMLInputElement {
  return screen.getByRole('textbox', { name: 'Search clients' }) as HTMLInputElement
}

function typeSearch(v: string) {
  fireEvent.change(searchBox(), { target: { value: v } })
}

async function mountFake(fetchMock: ReturnType<typeof mockFetch>, ctx = clientsCtx(ALL_ROWS)) {
  render(<ClientsView ctx={ctx} />)
  await flush()
  await flush()
  return { fetchMock, ctx }
}

const paramsOf = (url: string) => new URL(url).searchParams

describe('ClientsView: the search box (CSRCH-01-02)', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('CSRCH the search box leads the Clients bar', async () => {
    mockFetch()
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await screen.findByText('Okafor & Partners')
    const bar = screen.getByTestId('clients-filter-bar')
    const box = within(bar).getByRole('textbox', { name: 'Search clients' }) as HTMLInputElement
    expect(box.placeholder).toBe('Search name or TIN')
    expect(box.disabled).toBe(false)
    expect(box.compareDocumentPosition(within(bar).getByRole('button', { name: 'All' })) & Node.DOCUMENT_POSITION_FOLLOWING, 'the box precedes All').toBeTruthy()
  })

  it('CSRCH typing waits for a pause, then sends one q request', async () => {
    vi.useFakeTimers()
    const { fetchMock } = await mountFake(mockFetch())
    const before = entitiesCallUrls(fetchMock).length
    for (const v of ['o', 'ok', 'oka']) {
      typeSearch(v)
      await advance(80)
    }
    expect(entitiesCallUrls(fetchMock).slice(before).filter((u) => paramsOf(u).has('q')), 'no q request before the pause').toHaveLength(0)
    await advance(300)
    const withQ = entitiesCallUrls(fetchMock).filter((u) => paramsOf(u).has('q'))
    expect(withQ).toHaveLength(1)
    expect(paramsOf(withQ[0]).get('q')).toBe('oka')
    expect(screen.getByText('Okafor & Partners')).toBeTruthy()
    expect(screen.queryByText('Beta Traders')).toBeNull()
  })

  it('CSRCH a TIN fragment narrows the list', async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn((url: string) => {
      if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
      return Promise.resolve(entitiesResponse(SEARCH_ROWS.filter((e) => matchesQ(e, paramsOf(url).get('q')))))
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ClientsView ctx={clientsCtx(SEARCH_ROWS)} />)
    await flush()
    expect(screen.getByText('Alpha Co')).toBeTruthy()
    typeSearch('5566')
    await advance(300)
    const last = entitiesCallUrls(fetchMock as unknown as ReturnType<typeof mockFetch>).at(-1)!
    expect(paramsOf(last).get('q')).toBe('5566')
    expect(screen.getByText('Abby Ltd')).toBeTruthy()
    expect(screen.queryByText('Alpha Co')).toBeNull()
  })

  it('CSRCH the typed text is trimmed on the wire', async () => {
    vi.useFakeTimers()
    const { fetchMock } = await mountFake(mockFetch())
    typeSearch('  okafor  ')
    await advance(300)
    expect(paramsOf(entitiesCallUrls(fetchMock).at(-1)!).get('q')).toBe('okafor')
    expect(searchBox().value).toBe('  okafor  ')
  })

  it('CSRCH a spaces-only search sends nothing', async () => {
    vi.useFakeTimers()
    const { fetchMock } = await mountFake(mockFetch())
    expect(entitiesCallUrls(fetchMock)).toHaveLength(1)
    typeSearch('   ')
    await advance(300)
    expect(entitiesCallUrls(fetchMock), 'no further entities call').toHaveLength(1)
    for (const n of ['Okafor & Partners', 'Beta Traders', 'Honeywell Group']) expect(screen.getByText(n)).toBeTruthy()
  })

  async function committedActiveSearch() {
    vi.useFakeTimers()
    const out = await mountFake(mockFetch())
    fireEvent.click(screen.getByRole('button', { name: 'Active' }))
    await flush()
    typeSearch('okafor')
    await advance(300)
    expect(screen.queryByText('Beta Traders'), 'the search committed').toBeNull()
    return out.fetchMock
  }

  it("CSRCH backspacing a search to empty restores the chip's list", async () => {
    const fetchMock = await committedActiveSearch()
    const before = entitiesCallUrls(fetchMock).length
    for (let v = 'okafor'; v.length > 0; ) {
      v = v.slice(0, -1)
      typeSearch(v)
    }
    await advance(299)
    expect(entitiesCallUrls(fetchMock), 'no call before 300ms').toHaveLength(before)
    await advance(1)
    const calls = entitiesCallUrls(fetchMock).slice(before)
    expect(calls).toHaveLength(1)
    expect(paramsOf(calls[0]).get('status')).toBe('active')
    expect(paramsOf(calls[0]).has('q')).toBe(false)
    expect(screen.getByText('Okafor & Partners')).toBeTruthy()
    expect(screen.getByText('Beta Traders')).toBeTruthy()
  })

  it("CSRCH replacing a search with spaces restores the chip's list", async () => {
    const fetchMock = await committedActiveSearch()
    const before = entitiesCallUrls(fetchMock).length
    typeSearch('   ')
    await advance(300)
    const calls = entitiesCallUrls(fetchMock).slice(before)
    expect(calls).toHaveLength(1)
    expect(paramsOf(calls[0]).get('status')).toBe('active')
    expect(paramsOf(calls[0]).has('q')).toBe(false)
    expect(screen.getByText('Okafor & Partners')).toBeTruthy()
    expect(screen.getByText('Beta Traders')).toBeTruthy()
  })

  it('CSRCH the chip and the search apply together', async () => {
    vi.useFakeTimers()
    const { fetchMock } = await mountFake(mockFetch())
    typeSearch('honey')
    await advance(300)
    fireEvent.click(screen.getByRole('button', { name: 'Archived' }))
    await flush()
    let p = paramsOf(entitiesCallUrls(fetchMock).at(-1)!)
    expect([p.get('status'), p.get('q')]).toEqual(['archived', 'honey'])
    expect(screen.getByText('Honeywell Group')).toBeTruthy()
    expect(screen.queryByText('Okafor & Partners')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Active' }))
    await flush()
    p = paramsOf(entitiesCallUrls(fetchMock).at(-1)!)
    expect([p.get('status'), p.get('q')]).toEqual(['active', 'honey'])
    expect(screen.getByTestId('clients-no-match')).toBeTruthy()
  })

  it('CSRCH a chip change before the pause ends ends on the typed q and chip', async () => {
    vi.useFakeTimers()
    let release: (r: MockResponse) => void = () => {}
    const held = new Promise<MockResponse>((res) => {
      release = res
    })
    let heldOnce = false
    const fetchMock = vi.fn((url: string) => {
      if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
      const p = paramsOf(url)
      if (p.get('status') === 'archived' && !p.has('q') && !heldOnce) {
        heldOnce = true
        return held
      }
      return Promise.resolve(entitiesResponse(rowsForStatus(p.get('status'), p.get('q'))))
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await flush()
    typeSearch('honey')
    await advance(100)
    fireEvent.click(screen.getByRole('button', { name: 'Archived' }))
    await advance(300)
    const p = paramsOf(entitiesCallUrls(fetchMock as unknown as ReturnType<typeof mockFetch>).at(-1)!)
    expect([p.get('status'), p.get('q')]).toEqual(['archived', 'honey'])
    // The superseded response lands last, with the wrong (unsearched) rows.
    release(entitiesResponse(ALL_ROWS))
    await flush()
    expect(screen.getByText('Honeywell Group')).toBeTruthy()
    expect(screen.queryByText('Okafor & Partners')).toBeNull()
  })

  it('CSRCH no match names the search and offers Clear search', async () => {
    vi.useFakeTimers()
    await mountFake(mockFetch())
    typeSearch(' zzz ')
    await advance(300)
    const box = screen.getByTestId('clients-no-match')
    expect(within(box).getByText('No clients match “zzz”')).toBeTruthy()
    expect(within(box).getByTestId('clients-clear-search').textContent).toBe('Clear search')
    expect(screen.queryByText('No entities yet')).toBeNull()
    expect(screen.queryByText('No clients match this filter')).toBeNull()
  })

  it("CSRCH Clear search restores the chip's list at once", async () => {
    vi.useFakeTimers()
    const { fetchMock } = await mountFake(mockFetch())
    fireEvent.click(screen.getByRole('button', { name: 'Archived' }))
    await flush()
    typeSearch('zzz')
    await advance(300)
    expect(screen.getByTestId('clients-no-match')).toBeTruthy()
    fireEvent.click(screen.getByTestId('clients-clear-search'))
    await flush() // no timer advance: the clear must not wait for the debounce
    const p = paramsOf(entitiesCallUrls(fetchMock).at(-1)!)
    expect([p.get('status'), p.has('q')]).toEqual(['archived', false])
    expect(searchBox().value).toBe('')
    expect(screen.getByText('Honeywell Group')).toBeTruthy()
  })

  it('CSRCH a loading search shows the loading state', async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn((url: string) => {
      if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
      if (paramsOf(url).has('q')) return new Promise(() => {})
      return Promise.resolve(entitiesResponse(ALL_ROWS))
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await flush()
    typeSearch('oka')
    await advance(300)
    expect(screen.getByText('Loading entities…')).toBeTruthy()
    expect(searchBox().value).toBe('oka')
  })

  it('CSRCH Retry repeats the same search and chip', async () => {
    vi.useFakeTimers()
    let failed = false
    const fetchMock = vi.fn((url: string) => {
      if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
      const p = paramsOf(url)
      if (p.get('q') === 'oka' && !failed) {
        failed = true
        return Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) })
      }
      return Promise.resolve(entitiesResponse(rowsForStatus(p.get('status'), p.get('q'))))
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await flush()
    fireEvent.click(screen.getByRole('button', { name: 'Active' }))
    await flush()
    typeSearch('oka')
    await advance(300)
    await flush()
    expect(screen.getByText('Something went wrong')).toBeTruthy()
    const urls = () => entitiesCallUrls(fetchMock as unknown as ReturnType<typeof mockFetch>)
    const failedUrl = urls().at(-1)!
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await flush()
    expect(urls().at(-1)).toBe(failedUrl)
    expect(paramsOf(failedUrl).get('status')).toBe('active')
    expect(paramsOf(failedUrl).get('q')).toBe('oka')
    expect(screen.getByText('Okafor & Partners')).toBeTruthy()
  })

  it('CSRCH a stale response does not overwrite a newer search', async () => {
    vi.useFakeTimers()
    const pending = new Map<string, (r: MockResponse) => void>()
    const fetchMock = vi.fn((url: string) => {
      if (isRollupUrl(url)) return Promise.resolve(rollupResponse())
      const q = paramsOf(url).get('q')
      if (q == null) return Promise.resolve(entitiesResponse(SEARCH_ROWS))
      return new Promise<MockResponse>((res) => pending.set(q, res))
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ClientsView ctx={clientsCtx(SEARCH_ROWS)} />)
    await flush()
    typeSearch('a')
    await advance(300)
    typeSearch('ab')
    await advance(300)
    pending.get('ab')!(entitiesResponse(SEARCH_ROWS.filter((e) => matchesQ(e, 'ab'))))
    await flush()
    pending.get('a')!(entitiesResponse(SEARCH_ROWS.filter((e) => matchesQ(e, 'a'))))
    await flush()
    expect(screen.getByText('Abby Ltd')).toBeTruthy()
    expect(screen.queryByText('Alpha Co'), 'the late q=a response must not win').toBeNull()
  })

  it('CSRCH searching leaves the switcher roster alone', async () => {
    vi.useFakeTimers()
    mockFetch()
    const ctx = clientsCtx(ALL_ROWS)
    const refetchEntities = vi.fn()
    ctx.refetchEntities = refetchEntities
    const rosterBefore = ctx.entities
    render(<ClientsView ctx={ctx} />)
    await flush()
    typeSearch('oka')
    await advance(300)
    typeSearch('')
    await advance(300)
    expect(refetchEntities).not.toHaveBeenCalled()
    expect(ctx.entities).toBe(rosterBefore)
    expect(ctx.entities).toEqual(ALL_ROWS)
  })

  it('CSRCH an archive under a search refetches with that search', async () => {
    vi.useFakeTimers()
    const { fetchMock } = mockFetchArchiveAware(rowsForStatus(null, 'okafor'))
    render(<ClientsView ctx={clientsCtx(ALL_ROWS)} />)
    await flush()
    typeSearch('okafor')
    await advance(300)
    const action = within(rowFor('Okafor & Partners')).getByRole('button')
    fireEvent.click(action) // arm
    fireEvent.click(action) // confirm
    await flush()
    await flush()
    const listUrls = fetchMock.mock.calls.map((c) => c[0] as string).filter((u) => !isRollupUrl(u) && !isArchiveUrl(u))
    expect(paramsOf(listUrls.at(-1)!).get('q')).toBe('okafor')
    expect(entitiesListGetCalls(fetchMock), 'the archive refetched the list').toBeGreaterThanOrEqual(3)
  })
})
