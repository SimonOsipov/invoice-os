// @vitest-environment jsdom
// AUDIT-06-07's RED specs. The `mockFetchSequence` / narrowed-ctx idiom is
// ApprovalsView.test.tsx's, unchanged.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AuditEvent, AuditResponse } from '../lib/audit'
import { createAuthedFetch } from '../lib/authedFetch'
import type { PlatformCtx } from '../types'

import { AuditView } from './AuditView'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function mockFetchSequence(responses: MockResponse[]) {
  const fetchMock = vi.fn()
  for (const r of responses) fetchMock.mockResolvedValue(r)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function auditEvent(over: Partial<AuditEvent> = {}): AuditEvent {
  return {
    id: 'evt-1',
    created_at: '2026-08-20T09:15:00Z',
    event: 'invoice.created',
    actor: 'c0000000-0000-0000-0000-000000000001',
    actor_name: 'Chinedu Okafor',
    actor_kind: 'person',
    entity_id: 'ent-1',
    company_name: 'Honeywell Group',
    company_scope: 'company',
    payload: { id: 'inv-9', invoice_number: 'INV-9' },
    ...over,
  }
}

function logResponse(over: Partial<AuditResponse> = {}): MockResponse {
  const body: AuditResponse = {
    events: [auditEvent()],
    page: { limit: 25, has_more: false, next_cursor: null },
    total: 1,
    log_is_empty: false,
    facets: { event: [], actor: [], company: [] },
    ...over,
  }
  return { ok: true, status: 200, json: () => Promise.resolve(body) }
}

function auditCtx(): PlatformCtx {
  return {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
  } as unknown as PlatformCtx
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

// Applies the screen's only filter in this story: the row expansion's invoice affordance.
// The filter card itself is AUDIT-07's.
async function applyInvoiceFilter() {
  await waitFor(() => expect(screen.getAllByTestId('audit-row').length).toBeGreaterThan(0))
  fireEvent.click(screen.getAllByTestId('audit-row')[0])
  fireEvent.click(screen.getByTestId('audit-invoice-affordance'))
}

describe('AuditView states', () => {
  it('auditStates_emptyByFilterNamesTheFilters', async () => {
    // First page loads one row; filtering to its invoice returns nothing, with the log
    // itself demonstrably non-empty. Keyed on the URL, not on call order -- the refetch
    // fires from the click's own effect, before any re-stub could land.
    const fetchMock = vi.fn((url: string) =>
      Promise.resolve(url.includes('invoice_id=') ? logResponse({ events: [], total: 0, log_is_empty: false }) : logResponse()),
    )
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await applyInvoiceFilter()

    await waitFor(() => expect(screen.getByTestId('audit-empty-by-filter')).toBeTruthy())
    const copy = screen.getByTestId('audit-empty-by-filter').textContent ?? ''
    expect(copy).toContain('The log is not empty')
    // N is the LIFETIME count, captured from the last unfiltered response -- `total` on
    // this filtered response is 0 and would read as "It holds 0 events".
    expect(copy).toContain('1')
    expect(copy).not.toContain('0 events')
    // The pills are repeated beside the copy, so the user can see what to remove.
    expect(screen.getByTestId('audit-filter-pill')).toBeTruthy()
  })

  it('auditStates_newWorkspaceSaysNothingAboutFilters', async () => {
    mockFetchSequence([logResponse({ events: [], total: 0, log_is_empty: true })])
    render(<AuditView ctx={auditCtx()} />)

    await waitFor(() => expect(screen.getByTestId('audit-new-workspace')).toBeTruthy())
    const copy = screen.getByTestId('audit-new-workspace').textContent ?? ''
    expect(copy.toLowerCase()).not.toContain('filter')
    expect(screen.queryByTestId('audit-empty-by-filter')).toBeNull()
  })

  it('auditStates_distinctionComesFromTheFlagAlone', async () => {
    // `total` is held at 0 across both renders. Only the flag moves, and the state moves
    // with it -- so nothing here is inferred from the response's shape.
    mockFetchSequence([logResponse({ events: [], total: 0, log_is_empty: true })])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-new-workspace')).toBeTruthy())
    cleanup()

    mockFetchSequence([logResponse({ events: [], total: 0, log_is_empty: false })])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-empty-by-filter')).toBeTruthy())
    expect(screen.queryByTestId('audit-new-workspace')).toBeNull()
  })
})

describe('AuditView pagination', () => {
  // Every call's URL, so a cursor can be checked as the exact string the server minted.
  function recordingFetch(body: (url: string) => MockResponse) {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(body(url))
    })
    vi.stubGlobal('fetch', fetchMock)
    return calls
  }

  it('auditPager_nextSendsTheReturnedCursor', async () => {
    // Opaque on purpose: the reader mints it and the screen must not parse, decode or
    // rebuild it.
    const CURSOR = 'eyJ0IjoiMjAyNi0wOC0yMFQwOToxNTowMFoiLCJpIjoiZXZ0LTEifQ=='
    const calls = recordingFetch((url) =>
      url.includes('cursor=')
        ? logResponse({ page: { limit: 25, has_more: false, next_cursor: null }, total: 2 })
        : logResponse({ page: { limit: 25, has_more: true, next_cursor: CURSOR }, total: 2 }),
    )
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-pager-next')).toBeTruthy())
    fireEvent.click(screen.getByTestId('audit-pager-next'))

    await waitFor(() => expect(calls.some((u) => u.includes('cursor='))).toBe(true))
    const sent = new URL(calls.find((u) => u.includes('cursor='))!).searchParams.get('cursor')
    expect(sent).toBe(CURSOR)
    // Forward-only reader: an offset would be a silent fallback to a different pagination.
    expect(calls.every((u) => !u.includes('offset='))).toBe(true)
  })

  it('auditPager_disabledAtEndOfLog', async () => {
    recordingFetch(() => logResponse({ page: { limit: 25, has_more: false, next_cursor: null } }))
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-pager-next')).toBeTruthy())
    expect(screen.getByTestId('audit-pager-next')).toHaveProperty('disabled', true)
    expect(screen.getByTestId('audit-pager-prev')).toHaveProperty('disabled', true)
  })

  it('auditPager_pageSizeChangeResetsCursor', async () => {
    const CURSOR = 'cursor-minted-at-limit-25'
    const calls = recordingFetch((url) =>
      url.includes('cursor=')
        ? logResponse({ page: { limit: 25, has_more: true, next_cursor: 'second' }, total: 200 })
        : logResponse({ page: { limit: 25, has_more: true, next_cursor: CURSOR }, total: 200 }),
    )
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-pager-next')).toBeTruthy())
    fireEvent.click(screen.getByTestId('audit-pager-next'))
    await waitFor(() => expect(calls.some((u) => u.includes('cursor='))).toBe(true))

    fireEvent.change(screen.getByTestId('audit-page-size'), { target: { value: '100' } })
    await waitFor(() => expect(calls.some((u) => u.includes('limit=100'))).toBe(true))
    // A cursor minted at limit=25 addresses a different page boundary; carrying it into a
    // limit=100 request would skip or repeat rows.
    const resized = calls.filter((u) => u.includes('limit=100'))
    expect(resized.length).toBeGreaterThan(0)
    for (const u of resized) expect(u).not.toContain('cursor=')
  })
})

describe('AuditView immutability strip', () => {
  it('auditStrip_statesTheGuaranteeAsFact', async () => {
    mockFetchSequence([logResponse({ total: 1248 })])
    render(<AuditView ctx={auditCtx()} />)
    // The strip paints before the fetch settles; the count only appears once it does.
    await waitFor(() => expect(screen.getByTestId('audit-immutability-strip').textContent).toContain('1,248'))
    const copy = (screen.getByTestId('audit-immutability-strip').textContent ?? '').toLowerCase()

    // The claim is literally true: GRANT SELECT, INSERT only, plus triggers raising
    // restrict_violation on UPDATE/DELETE/TRUNCATE (pinned by TestAudit_NoTruncate).
    // Softening it into marketing language would understate what the database enforces.
    for (const hedge of ['designed to', 'aims to', 'intended to', 'strives', 'should not', 'we try']) {
      expect(copy, `the strip hedges: "${hedge}"`).not.toContain(hedge)
    }
    expect(copy).toContain('append-only')
    expect(copy).toContain('1,248')
    // Option A (user decision, 2026-08-23): the reader exposes no first-row date, so none
    // is rendered or approximated.
    expect(copy).not.toContain('since')
    expect(copy).not.toContain('first')
  })

  it('auditStrip_countIsNeverAFilteredTotal', async () => {
    // The unfiltered page reports 7 lifetime events; the filtered one reports 999, which
    // is not a lifetime figure and must never reach the strip.
    const fetchMock = vi.fn((url: string) =>
      Promise.resolve(url.includes('invoice_id=') ? logResponse({ total: 999 }) : logResponse({ total: 7 })),
    )
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-immutability-strip').textContent).toContain('7'))
    await applyInvoiceFilter()

    await waitFor(() => expect(screen.getByTestId('audit-filter-pill')).toBeTruthy())
    const copy = screen.getByTestId('audit-immutability-strip').textContent ?? ''
    expect(copy).not.toContain('999')
    expect(copy).toContain('7')
  })
})

describe('AuditView keeps the table mounted across a page change', () => {
  it('auditPager_tableAndPagerSurviveAPageLoad', async () => {
    // Found by the deploy gate (PR #180, ac2f576): with the pager inside the loading rung,
    // clicking next unmounted the very control under the pointer and swapped the table for
    // the skeleton -- the layout jump the skeleton exists to prevent. Only the FIRST load
    // may show the skeleton.
    let release: ((v: MockResponse) => void) | null = null
    const fetchMock = vi.fn((url: string) => {
      if (!url.includes('cursor=')) {
        return Promise.resolve(logResponse({ page: { limit: 25, has_more: true, next_cursor: 'c1' }, total: 60 }))
      }
      return new Promise<MockResponse>((res) => {
        release = res
      })
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-pager-next')).toHaveProperty('disabled', false))

    fireEvent.click(screen.getByTestId('audit-pager-next'))

    // The second page is still in flight here.
    await waitFor(() => expect(screen.getByTestId('audit-pager-next')).toHaveProperty('disabled', true))
    expect(screen.getByTestId('audit-table'), 'the table must stay on screen while the next page loads').toBeTruthy()
    expect(screen.getAllByTestId('audit-row').length).toBeGreaterThan(0)
    expect(screen.queryByTestId('audit-skeleton-row'), 'the skeleton belongs to the first load only').toBeNull()

    // And the readout still describes the rows actually on screen, not the page in flight.
    expect(screen.getByTestId('audit-pager').textContent).toContain('1–1')

    release!(logResponse({ page: { limit: 25, has_more: false, next_cursor: null }, total: 60 }))
    await waitFor(() => expect(screen.getByTestId('audit-pager-prev')).toHaveProperty('disabled', false))
  })
})

// AUDIT-07-03's RED specs (task-655): the filter card must mount as a sibling of the pills,
// survive a filter-driven refetch, and reset the page stack on every filter change.
describe('AuditView filter card (AUDIT-07-03)', () => {
  it('auditFilterCard_survivesARefetch', async () => {
    // P8: the card must not unmount while a filter-driven refetch is in flight. A paused
    // fetch lets the assertion land inside the loading window, not just after it settles.
    let release: ((v: MockResponse) => void) | null = null
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('q=narrowed')) {
        return new Promise<MockResponse>((res) => {
          release = res
        })
      }
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'narrowed' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })

    await waitFor(() => expect(fetchMock.mock.calls.some((c) => String(c[0]).includes('q=narrowed'))).toBe(true))
    // Control needle: proves this check really lands inside the in-flight window.
    expect(release, 'the refetch must actually be pending at this point').not.toBeNull()
    expect(screen.getByTestId('audit-filter-card'), 'card must stay mounted through the in-flight refetch').toBeTruthy()
    expect(screen.getByTestId('audit-search-trigger'), 'the control just touched must still be in the DOM').toBeTruthy()

    release!(logResponse({ total: 4 }))
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())
  })

  it('auditFilterCard_isNotInsideTheLoadedRung', () => {
    // A rendered check can pass on a structure that is still wrong -- this reads the source
    // directly. Vacuity floor first: both anchors must be found before the negative means anything.
    const BLOCK_ANCHOR = "(state === 'loaded' || state === 'filtered')"
    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'AuditView.tsx'), 'utf8')
    expect(src.length, 'AuditView.tsx must be non-empty').toBeGreaterThan(0)
    expect(src, 'AuditFilterCard must be mounted somewhere in the file').toContain('<AuditFilterCard')
    expect(src, 'the scan must be reading the real loaded/filtered rung').toContain(BLOCK_ANCHOR)

    const mountAt = src.indexOf('<AuditFilterCard')
    const blockStart = src.indexOf(BLOCK_ANCHOR)
    expect(mountAt, 'AuditFilterCard must mount before the loaded/filtered block, not inside it').toBeLessThan(blockStart)
  })

  it('auditFilter_changeResetsThePageStack', async () => {
    const PAGE2_CURSOR = 'page2-cursor'
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      if (url.includes('q=narrowed')) return Promise.resolve(logResponse({ total: 3 }))
      if (url.includes('cursor=')) return Promise.resolve(logResponse({ page: { limit: 25, has_more: false, next_cursor: null } }))
      return Promise.resolve(logResponse({ page: { limit: 25, has_more: true, next_cursor: PAGE2_CURSOR } }))
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-pager-next')).toHaveProperty('disabled', false))
    fireEvent.click(screen.getByTestId('audit-pager-next'))
    await waitFor(() => expect(calls.some((u) => u.includes('cursor='))).toBe(true))

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'narrowed' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })

    await waitFor(() => expect(calls.some((u) => u.includes('q=narrowed'))).toBe(true))
    const filterCall = calls.find((u) => u.includes('q=narrowed'))
    // Positive first: the request must actually carry the new filter, not merely lack a cursor.
    expect(filterCall, 'the filter change must reach the network with the new filter').toBeTruthy()
    expect(filterCall, 'a filter change must reset the cursor').not.toContain('cursor=')
  })

  it('auditFilterCard_hiddenOnNewWorkspace', async () => {
    // Control needle: the card must exist on a normal load before its absence means anything.
    mockFetchSequence([logResponse()])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card'), 'control needle: card renders on a normal load').toBeTruthy())
    cleanup()

    mockFetchSequence([logResponse({ events: [], total: 0, log_is_empty: true })])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-new-workspace')).toBeTruthy())
    expect(screen.queryByTestId('audit-filter-card'), 'card must be hidden on new-workspace').toBeNull()
  })
})

// QA (task-655): the plan's Test Specs table names 8 cases; only 4 were authored RED. These
// close the rest, plus mutation-driven gaps found while verifying: `survivesARefetch` passes
// even with the card nested inside the loaded/filtered block, because `landed` keeps `state`
// at 'loaded'/'filtered' through a same-shape refetch -- only error/empty-by-filter actually
// exercise the mount boundary behaviourally.
describe('AuditView filter card adversarial coverage (AUDIT-07-03 QA)', () => {
  it('auditFilterCard_presentOnEmptyByFilter', async () => {
    const fetchMock = vi.fn((url: string) =>
      Promise.resolve(url.includes('q=narrowed') ? logResponse({ events: [], total: 0, log_is_empty: false }) : logResponse()),
    )
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'narrowed' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })

    await waitFor(() => expect(screen.getByTestId('audit-empty-by-filter')).toBeTruthy())
    expect(screen.getByTestId('audit-filter-card'), 'card must stay mounted in empty-by-filter').toBeTruthy()
  })

  it('auditFilterCard_presentOnError', async () => {
    const fetchMock = vi.fn((url: string) =>
      url.includes('q=broken') ? Promise.reject(new Error('boom')) : Promise.resolve(logResponse()),
    )
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'broken' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })

    await waitFor(() => expect(screen.getByText('Something went wrong')).toBeTruthy())
    expect(screen.getByTestId('audit-filter-card'), 'card must stay mounted in the error rung').toBeTruthy()
  })

  it('auditFilter_facetsPropComesFromTheResponseObject', () => {
    // AuditFilterCard draws event-facet counts as of AUDIT-07-04; actor/company are still
    // AUDIT-07-05..06's. AuditView itself never renders a count, so a rendered check on
    // AuditView cannot see this AC. Source-level, same idiom as isNotInsideTheLoadedRung.
    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'AuditView.tsx'), 'utf8')
    expect(src.length).toBeGreaterThan(0)
    expect(src, 'the scan must be reading the real mount').toContain('<AuditFilterCard')
    expect(src, 'facets must be sourced from the response object').toContain('facets={shown?.res.facets')
    expect(src, 'facets must never be a client-side tally').not.toMatch(/facets=\{[^}]*events\.length/)
  })

  it('auditFilter_busyDisablesTheControls', async () => {
    let release: ((v: MockResponse) => void) | null = null
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('q=narrowed')) {
        return new Promise<MockResponse>((res) => {
          release = res
        })
      }
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'narrowed' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })

    await waitFor(() => expect(release, 'the refetch must actually be pending').not.toBeNull())
    expect(screen.getByTestId('audit-search-trigger')).toHaveProperty('disabled', true)
    expect(screen.getByTestId('audit-date-trigger')).toHaveProperty('disabled', true)

    release!(logResponse({ total: 4 }))
    await waitFor(() => expect(screen.getByTestId('audit-search-trigger')).toHaveProperty('disabled', false))
  })

  it('auditFilter_rapidChangesEachResetTheCursorAndLastWins', async () => {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'first' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })
    await waitFor(() => expect(calls.some((u) => u.includes('q=first'))).toBe(true))

    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'second' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })
    await waitFor(() => expect(calls.some((u) => u.includes('q=second'))).toBe(true))

    const filterCalls = calls.filter((u) => u.includes('q=first') || u.includes('q=second'))
    expect(filterCalls.length).toBe(2)
    for (const u of filterCalls) expect(u, 'every filter change must reset the cursor').not.toContain('cursor=')
    expect(calls[calls.length - 1], 'the network must see the last filter, not an intermediate one').toContain('q=second')
  })

  it('auditFilter_lateInFlightResponseIsDiscardedByALaterFilterChange', async () => {
    let releaseFirst: ((v: MockResponse) => void) | null = null
    let releaseSecond: ((v: MockResponse) => void) | null = null
    const fetchMock = vi.fn((url: string) => {
      if (url.includes('q=first')) return new Promise<MockResponse>((res) => (releaseFirst = res))
      if (url.includes('q=second')) return new Promise<MockResponse>((res) => (releaseSecond = res))
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'first' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })
    await waitFor(() => expect(releaseFirst, 'the first request must be in flight').not.toBeNull())

    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'second' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })
    await waitFor(() => expect(releaseSecond, 'the second request must be in flight').not.toBeNull())

    // Resolve out of order: the stale "first" response lands after "second".
    releaseSecond!(logResponse({ total: 22 }))
    await waitFor(() => expect(screen.getByTestId('audit-immutability-strip').textContent).toContain('22'))
    releaseFirst!(logResponse({ total: 999 }))
    await new Promise((r) => setTimeout(r, 10))
    expect(screen.getByTestId('audit-immutability-strip').textContent, 'a late stale response must not clobber the current one').toContain('22')
    expect(screen.getByTestId('audit-immutability-strip').textContent).not.toContain('999')
  })

  it('auditFilter_dateWindowFrozenAcrossUnrelatedRerenders', async () => {
    // Pins the useMemo deviation: without it, an unrelated re-render (row expansion here)
    // recomputes `now`, changes the deps string and refetches -- confirmed empirically to
    // spiral into a "Maximum update depth exceeded" loop (102 fetches in 300ms) when tried.
    const fetchMock = vi.fn(() => Promise.resolve(logResponse()))
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getAllByTestId('audit-row').length).toBeGreaterThan(0))
    const callsAfterLoad = fetchMock.mock.calls.length

    fireEvent.click(screen.getAllByTestId('audit-row')[0])
    await new Promise((r) => setTimeout(r, 50))
    expect(fetchMock.mock.calls.length, 'an unrelated re-render must not trigger a refetch').toBe(callsAfterLoad)
  })

  it('auditFilter_dateWindowStableAcrossPageChange', async () => {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(
        url.includes('cursor=')
          ? logResponse({ page: { limit: 25, has_more: false, next_cursor: null } })
          : logResponse({ page: { limit: 25, has_more: true, next_cursor: 'p2' } }),
      )
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-pager-next')).toHaveProperty('disabled', false))
    const firstFrom = new URL(calls[0]).searchParams.get('from')
    expect(firstFrom, 'the 30-day default must carry a from= param').toBeTruthy()

    fireEvent.click(screen.getByTestId('audit-pager-next'))
    await waitFor(() => expect(calls.some((u) => u.includes('cursor='))).toBe(true))
    const secondFrom = new URL(calls.find((u) => u.includes('cursor='))!).searchParams.get('from')
    // A drifting `from` between page 1 and page 2 would shift the query window under a
    // cursor minted against the earlier one -- the memo exists to prevent exactly this.
    expect(secondFrom, 'the date window must not drift across a page change').toBe(firstFrom)
  })
})

// QA gap-fill (task-657, AC#7): FAILS today -- the actor popover renders strictly from
// facets.actor with no synthesis for a selected id the facet no longer lists, so this row
// never appears. Left red to document the unmet AC; not this agent's fix to make.
describe('AuditView actor control (AUDIT-07-05 QA)', () => {
  it('auditActorFilter_selectedActorMissingFromFacetKeepsItsPill', async () => {
    const withActor = logResponse({
      facets: { event: [], actor: [{ value: 'u1', name: 'Amara Chen', kind: 'person', count: 3 }], company: [] },
    })
    const withoutActor = logResponse({ facets: { event: [], actor: [], company: [] } })
    let calls = 0
    const fetchMock = vi.fn(() => {
      calls += 1
      return Promise.resolve(calls === 1 ? withActor : withoutActor)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-actor-trigger'))
    fireEvent.click(screen.getByTestId('audit-actor-row-u1'))

    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(2))
    await waitFor(() =>
      expect(
        screen.getByTestId('audit-actor-count-u1').textContent,
        'the facet dropped the actor -- the selected row must synthesize count 0',
      ).toBe('0'),
    )
    expect(
      screen.getByTestId('audit-actor-row-u1'),
      'a selected actor must not vanish from its own control when the facet no longer lists it',
    ).toBeTruthy()
  })
})
