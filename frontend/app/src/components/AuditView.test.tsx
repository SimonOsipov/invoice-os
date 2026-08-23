// @vitest-environment jsdom
// AUDIT-06-07's RED specs. The `mockFetchSequence` / narrowed-ctx idiom is
// ApprovalsView.test.tsx's, unchanged.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AuditEvent, AuditResponse } from '../lib/audit'
import { auditCsv, auditExportToastCopy } from '../lib/auditCsv'
import { AUDIT_COPY, AUDIT_EXPORT_CAP } from '../lib/auditView'
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
// Distinguishes the lifetime probe from the main request by an exact param, never call order:
// the probe carries limit=1 and no date window; nothing else does.
function isProbeUrl(url: string): boolean {
  const params = new URL(url).searchParams
  return params.get('limit') === '1' && !params.has('from')
}

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
    expect(screen.getByTestId('audit-pill-invoice')).toBeTruthy()
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

    await waitFor(() => expect(screen.getByTestId('audit-pill-invoice')).toBeTruthy())
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

  // Gating the card on `landed` hid it forever here: the effect that sets `landed` only runs
  // on a response, so a first load that never lands one left nothing to clear the filter with.
  it('auditFilterCard_presentWhenTheFirstLoadItselfFails', async () => {
    const fetchMock = vi.fn(() => Promise.reject(new Error('boom')))
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)

    await waitFor(() => expect(screen.getByText('Something went wrong'), 'control needle: the error rung must land').toBeTruthy())
    expect(
      screen.getByTestId('audit-filter-card'),
      'the card must be present even when no response ever landed',
    ).toBeTruthy()
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
    // The pager's range readout is fed by the main request's total; the immutability strip
    // is not -- it reads the unfiltered probe, so it cannot witness a main-request race.
    releaseSecond!(logResponse({ total: 22 }))
    await waitFor(() => expect(screen.getByTestId('audit-pager').textContent).toContain('of 22'))
    releaseFirst!(logResponse({ total: 999 }))
    await new Promise((r) => setTimeout(r, 10))
    expect(screen.getByTestId('audit-pager').textContent, 'a late stale response must not clobber the current one').toContain('of 22')
    expect(screen.getByTestId('audit-pager').textContent).not.toContain('999')
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

// AC#7: a selected actor must keep its row/pill at count 0 when facets.actor drops it.
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

// AC#7 (task-658): the Test Specs table names this case for AuditView.test.tsx, following the
// actor-facet-vanishes pattern above; it was not present in the shipped commit -- QA gap-fill.
describe('AuditView company control (AUDIT-07-06 QA)', () => {
  it('auditCompanyFilter_pillKeepsTheNameAfterTheBucketVanishes', async () => {
    const withCompany = logResponse({
      facets: { event: [], actor: [], company: [{ value: 'co-acme', name: 'Acme Ltd', count: 12 }] },
    })
    const withoutCompany = logResponse({ facets: { event: [], actor: [], company: [] } })
    let calls = 0
    const fetchMock = vi.fn(() => {
      calls += 1
      return Promise.resolve(calls === 1 ? withCompany : withoutCompany)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-company-trigger'))
    fireEvent.click(screen.getByTestId('audit-company-row-co-acme'))

    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(2))
    await waitFor(() =>
      expect(
        screen.getByTestId('audit-company-trigger').textContent,
        'the facet dropped the bucket -- the pill must keep the name captured at selection time',
      ).toContain('Acme Ltd'),
    )
    // Control needle -- the row really is gone from the facet-driven list, so the pill above
    // survives via captured state and not because the source row is still rendering it.
    expect(
      screen.queryByTestId('audit-company-row-co-acme'),
      'control needle: the bucket really vanished from the popover row list',
    ).toBeNull()
  })
})

// AUDIT-07-07 (task-659): the pills row lives in AuditFilterCard as its second row.
// Testids are keyed on pill.key -- audit-pill-${key} -- plus audit-clear-all.
describe('AuditView pills row (AUDIT-07-07)', () => {
  it('auditPills_defaultShowsTheThirtyDayPill', async () => {
    mockFetchSequence([logResponse()])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    const pills = screen.getAllByTestId(/^audit-pill-/)
    expect(pills.length, 'only the default 30-day pill exists at first landing').toBe(1)
    expect(screen.getByTestId('audit-pill-range').textContent).toContain('Last 30 days')
  })

  it('auditPills_everyAppliedFilterHasOne', async () => {
    const fetchMock = vi.fn(() => Promise.resolve(logResponse()))
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'kept' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })
    await waitFor(() => expect(screen.getByTestId('audit-pill-q')).toBeTruthy())
    await waitFor(() => expect(screen.getByTestId('audit-event-trigger')).toHaveProperty('disabled', false))

    fireEvent.click(screen.getByTestId('audit-event-trigger'))
    fireEvent.click(screen.getByTestId('audit-event-row-invoice.kept_as_is'))
    fireEvent.click(screen.getByTestId('audit-event-row-invoice.approval_approved'))
    await waitFor(() => expect(screen.getByTestId('audit-pill-event:invoice.approval_approved')).toBeTruthy())
    await waitFor(() => expect(screen.getByTestId('audit-actor-trigger')).toHaveProperty('disabled', false))

    fireEvent.click(screen.getByTestId('audit-actor-trigger'))
    fireEvent.click(screen.getByTestId('audit-actor-kind-people'))
    await waitFor(() => expect(screen.getByTestId('audit-pill-actorKind')).toBeTruthy())
    await waitFor(() => expect(screen.getByTestId('audit-company-trigger')).toHaveProperty('disabled', false))

    fireEvent.click(screen.getByTestId('audit-company-trigger'))
    fireEvent.click(screen.getByTestId('audit-company-kind-workspace'))
    await waitFor(() => expect(screen.getByTestId('audit-pill-company')).toBeTruthy())

    await applyInvoiceFilter()
    await waitFor(() => expect(screen.getByTestId('audit-pill-invoice')).toBeTruthy())

    // range (always-on) + q + 2 events + actorKind + company + invoice = 7 -- matches
    // auditFilters.test.ts's auditFilters_everyNonDefaultFieldGetsExactlyOnePill, not the
    // "6" shorthand in the plan's Test Specs table, which undercounts the 2-event expansion.
    const pills = screen.getAllByTestId(/^audit-pill-/)
    expect(pills.length, 'every applied filter has exactly one pill, including the default').toBe(7)
    for (const p of pills) expect(p.tagName, 'each pill is itself the remove control').toBe('BUTTON')
  })

  it('auditPills_removingOneRemovesOnlyThat', async () => {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'kept' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })
    await waitFor(() => expect(calls.some((u) => u.includes('q=kept'))).toBe(true))

    fireEvent.click(screen.getByTestId('audit-event-trigger'))
    fireEvent.click(screen.getByTestId('audit-event-row-invoice.kept_as_is'))
    await waitFor(() => expect(calls.some((u) => u.includes('event=invoice.kept_as_is'))).toBe(true))

    fireEvent.click(screen.getByTestId('audit-company-trigger'))
    fireEvent.click(screen.getByTestId('audit-company-kind-workspace'))
    await waitFor(() => expect(calls.some((u) => u.includes('company=workspace'))).toBe(true))

    const callsBefore = calls.length
    fireEvent.click(screen.getByTestId('audit-pill-company'))
    await waitFor(() => expect(calls.length).toBeGreaterThan(callsBefore))

    const last = calls[calls.length - 1]
    expect(last, 'company must be dropped by removing only its pill').not.toContain('company=')
    expect(last, 'q must survive the removal').toContain('q=kept')
    expect(last, 'event must survive the removal').toContain('event=invoice.kept_as_is')
  })

  it('auditPills_removeFiresExactlyOneRefetch', async () => {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'kept' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })
    await waitFor(() => expect(calls.some((u) => u.includes('q=kept'))).toBe(true))

    const callsBefore = calls.length
    fireEvent.click(screen.getByTestId('audit-pill-q'))
    await waitFor(() => expect(calls.length).toBeGreaterThan(callsBefore))
    // Assert the delta itself, not just ">0" -- a double-fire bug would still pass a bare floor.
    expect(calls.length - callsBefore, 'removing one pill must fire exactly one refetch').toBe(1)
  })

  it('auditPills_clearAllReturnsToDefault', async () => {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'kept' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })
    await waitFor(() => expect(calls.some((u) => u.includes('q=kept'))).toBe(true))

    fireEvent.click(screen.getByTestId('audit-company-trigger'))
    fireEvent.click(screen.getByTestId('audit-company-kind-workspace'))
    await waitFor(() => expect(calls.some((u) => u.includes('company=workspace'))).toBe(true))

    fireEvent.click(screen.getByTestId('audit-clear-all'))
    await waitFor(() => expect(screen.getByTestId('audit-pill-range')).toBeTruthy())

    const last = calls[calls.length - 1]
    const params = new URL(last).searchParams
    // Exclude pagination keys (limit/cursor) -- they're attached unconditionally by AuditView,
    // outside filterQuery (AuditView.tsx), so they're not part of what Clear all controls.
    // The filter-only params must be exactly the 30-day default, nothing else surviving.
    const filterKeys = Array.from(params.keys()).filter((k) => k !== 'limit' && k !== 'cursor')
    expect(filterKeys, 'Clear all must leave only the 30-day default on the wire').toEqual(['from'])
  })

  // AUDIT-07-07 QA (task-659): AC#4's "exactly one refetch" claim for Clear all was untested --
  // auditPills_clearAllReturnsToDefault only inspects the last URL, never the call-count delta.
  it('auditPills_clearAllFiresExactlyOneRefetch', async () => {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-search-trigger'))
    fireEvent.change(screen.getByTestId('audit-search-input'), { target: { value: 'kept' } })
    fireEvent.keyDown(screen.getByTestId('audit-search-input'), { key: 'Enter' })
    await waitFor(() => expect(calls.some((u) => u.includes('q=kept'))).toBe(true))

    const callsBefore = calls.length
    fireEvent.click(screen.getByTestId('audit-clear-all'))
    await waitFor(() => expect(calls.length).toBeGreaterThan(callsBefore))
    expect(calls.length - callsBefore, 'Clear all must fire exactly one refetch').toBe(1)
  })

  it('auditPills_clearAllAbsentAtDefault', async () => {
    mockFetchSequence([logResponse()])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    // Control needle: the pills row itself must render before the absence check means anything.
    expect(screen.getByTestId('audit-pill-range'), 'control needle: the pills row renders at default').toBeTruthy()
    expect(screen.queryByTestId('audit-clear-all'), 'Clear all must be absent with nothing beyond the default').toBeNull()
  })

  it('auditPills_invoicePillCopyIsUnchanged', async () => {
    mockFetchSequence([logResponse()])
    render(<AuditView ctx={auditCtx()} />)
    await applyInvoiceFilter()
    await waitFor(() => expect(screen.getByTestId('audit-pill-invoice')).toBeTruthy())
    // Byte-identical to AUDIT-06's shipped copy (lib/auditView.ts:55-57).
    expect(screen.getByTestId('audit-pill-invoice').textContent).toContain('Invoice INV-9')
    cleanup()

    mockFetchSequence([logResponse({ events: [auditEvent({ payload: { id: 'inv-9' } })] })])
    render(<AuditView ctx={auditCtx()} />)
    await applyInvoiceFilter()
    await waitFor(() => expect(screen.getByTestId('audit-pill-invoice')).toBeTruthy())
    expect(screen.getByTestId('audit-pill-invoice').textContent).toContain('One invoice')
  })
})

// AUDIT-07-08 (task-660): the default 30-day window makes the main request's `total` a
// windowed count, not a lifetime one. The strip's N now comes from a dedicated, unfiltered
// limit=1 probe fired once per mount -- these specs pin that the probe fires, carries no
// filter params, and is the only source `lifetimeTotal` is ever read from.
describe('AuditView lifetime-count probe (AUDIT-07-08)', () => {
  it('auditProbe_oneUnfilteredRequestPerMount', async () => {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)

    // Vacuity floor: without this, the probe assertions below could pass on a recorder
    // that only ever saw the main request.
    await waitFor(() => expect(calls.length, 'mount must fire both the probe and the main request').toBeGreaterThanOrEqual(2))

    const probeUrls = calls.filter(isProbeUrl)
    expect(probeUrls.length, 'exactly one unfiltered limit=1 probe must fire per mount').toBe(1)
    const probeParams = new URL(probeUrls[0]).searchParams
    for (const key of ['from', 'to', 'event', 'company', 'q', 'actor', 'actor_kind']) {
      expect(probeParams.has(key), `the probe must not carry ${key}`).toBe(false)
    }
  })

  it('auditProbe_stripCountComesFromTheProbe', async () => {
    const fetchMock = vi.fn((url: string) =>
      Promise.resolve(isProbeUrl(url) ? logResponse({ total: 248113 }) : logResponse({ total: 1204 })),
    )
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)

    await waitFor(() => expect(screen.getByTestId('audit-immutability-strip').textContent).toContain('248,113'))
    const copy = screen.getByTestId('audit-immutability-strip').textContent ?? ''
    expect(copy, 'the strip must never read the main (windowed) total').not.toContain('1,204')
  })

  it('auditProbe_mainRequestStillCarriesTheThirtyDayWindow', async () => {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getAllByTestId('audit-row').length, 'control needle: the main request must resolve').toBeGreaterThan(0))

    const mainUrl = calls.find((u) => !isProbeUrl(u))
    expect(mainUrl, 'a non-probe request must exist').toBeTruthy()
    expect(new URL(mainUrl!).searchParams.has('from'), 'the main request must keep the 30-day default').toBe(true)
  })

  it('auditProbe_emptyByFilterUsesTheLifetimeNumber', async () => {
    const fetchMock = vi.fn((url: string) =>
      Promise.resolve(isProbeUrl(url) ? logResponse({ total: 7 }) : logResponse({ events: [], total: 0, log_is_empty: false })),
    )
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)

    // The rung appears when the main request lands; the probe is a separate promise, so the
    // copy is numbered only once its effect flushes. Snapshotting here races that.
    await waitFor(() => {
      const copy = screen.getByTestId('audit-empty-by-filter').textContent ?? ''
      expect(copy, 'the probe-sourced lifetime figure must be named, not the bare fallback').not.toBe(AUDIT_COPY.emptyByFilterBare)
      expect(copy, "the number named must be the probe's 7").toContain('7')
    })
  })

  it('auditProbe_failureDegradesSilently', async () => {
    const fetchMock = vi.fn((url: string) =>
      isProbeUrl(url) ? Promise.reject(new Error('probe boom')) : Promise.resolve(logResponse({ total: 340 })),
    )
    vi.stubGlobal('fetch', fetchMock)
    render(<AuditView ctx={auditCtx()} />)

    await waitFor(() => expect(screen.getAllByTestId('audit-row').length, 'the table must render despite the probe failing').toBeGreaterThan(0))
    // Let the rejected probe settle before asserting on its (lack of) effect.
    await new Promise((r) => setTimeout(r, 10))
    expect(screen.queryByText('Something went wrong'), 'a failed probe must not raise the error rung').toBeNull()
    const stripCopy = screen.getByTestId('audit-immutability-strip').textContent ?? ''
    expect(/\d/.test(stripCopy), 'with no lifetime figure available, the strip must show no number').toBe(false)
  })

  it('auditProbe_onlyTotalIsReadFromTheProbe', () => {
    // Source scan: a rendered check can't see which field an effect reads.
    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'AuditView.tsx'), 'utf8')
    expect(src.length, 'vacuity floor: AuditView.tsx must be non-empty').toBeGreaterThan(0)
    expect(src, "vacuity floor: the probe's own call literal must exist").toContain('limit: 1')

    const effectRe = /useEffect\(\s*\(\)\s*=>\s*\{([\s\S]*?)\},\s*\[[^\]]*\]\s*\)/g
    const bodies = Array.from(src.matchAll(effectRe), (m) => m[1])
    expect(bodies.length, 'control needle: at least one useEffect block must be found').toBeGreaterThan(0)

    const probeBody = bodies.find((b) => b.includes('setLifetimeTotal('))
    expect(probeBody, "the probe's own effect (the one setting lifetimeTotal) must exist").toBeTruthy()
    expect(probeBody, "the probe's effect must read total").toContain('total')
    for (const field of ['.events', '.facets', '.page', 'log_is_empty']) {
      expect(probeBody, `the probe's effect must not read ${field}`).not.toContain(field)
    }
  })
})
// AUDIT-07-10 (task-662): the export control, the DOM download and its toast. The pure
// halves (collectExportRows, auditCsv/auditCsvFilename/auditExportToastCopy) are already
// shipped and green -- these specs pin only the component's wiring onto them.
function isExportPageUrl(url: string): boolean {
  return new URL(url).searchParams.get('limit') === '100'
}

// The one DOM seam under test, same spy idiom as ReviewAlreadyImportedTab.test.tsx's
// AIMPTAB-QA-5: createObjectURL/revokeObjectURL plus the anchor's own click.
function stubDownload() {
  let capturedBlob: Blob | null = null
  const createSpy = vi.spyOn(URL, 'createObjectURL').mockImplementation((b) => {
    capturedBlob = b as Blob
    return 'blob:audit-export'
  })
  const revokeSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  const clicks: { href: string; download: string }[] = []
  const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicks.push({ href: this.href, download: this.download })
  })
  return {
    createSpy,
    revokeSpy,
    clickSpy,
    clicks,
    get blob() {
      return capturedBlob
    },
    restore() {
      createSpy.mockRestore()
      revokeSpy.mockRestore()
      clickSpy.mockRestore()
    },
  }
}

async function blobBytes(blob: Blob): Promise<Uint8Array> {
  const buf = await new Promise<ArrayBuffer>((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(r.result as ArrayBuffer)
    r.onerror = reject
    r.readAsArrayBuffer(blob)
  })
  return new Uint8Array(buf)
}

describe('AuditView export control (AUDIT-07-10)', () => {
  // Every scenario's main/probe response carries a non-empty row so the button starts
  // enabled -- the abort must come from the export loop, never from the zero-rows gate.
  const abortScenarios: { name: string; pages: Array<'reject' | MockResponse> }[] = [
    { name: 'a rejected page', pages: ['reject'] },
    {
      name: 'a limit echo other than 100',
      pages: [logResponse({ events: [auditEvent()], total: 1, page: { limit: 25, has_more: false, next_cursor: null } })],
    },
    {
      name: 'a repeated cursor',
      pages: [
        logResponse({ events: [auditEvent()], total: 2, page: { limit: 100, has_more: true, next_cursor: 'stall' } }),
        logResponse({ events: [auditEvent()], total: 2, page: { limit: 100, has_more: true, next_cursor: 'stall' } }),
      ],
    },
    {
      name: 'has_more with a null cursor',
      pages: [logResponse({ events: [auditEvent()], total: 5, page: { limit: 100, has_more: true, next_cursor: null } })],
    },
    {
      name: 'an empty page claiming has_more',
      pages: [logResponse({ events: [], total: 5, page: { limit: 100, has_more: true, next_cursor: 'x' } })],
    },
  ]

  // Floor: auditExport_oneClickOneDownload below proves createObjectURL fires on a clean
  // export, so a zero-call result here can't be an unwired spy.
  it.each(abortScenarios)('auditExport_neverWritesAPartialFile: $name', async ({ pages }) => {
    let exportCalls = 0
    const fetchMock = vi.fn((url: string) => {
      if (!isExportPageUrl(url)) return Promise.resolve(logResponse())
      const spec = pages[Math.min(exportCalls, pages.length - 1)]
      exportCalls += 1
      return spec === 'reject' ? Promise.reject(new Error('export page failed')) : Promise.resolve(spec)
    })
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders before it can be clicked').toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-export'))

    await waitFor(() => expect(screen.getByTestId('audit-export-toast'), 'an error toast must appear once the loop aborts').toBeTruthy())
    expect(dl.createSpy, 'a partial export must never reach createObjectURL').not.toHaveBeenCalled()

    dl.restore()
  })

  it('auditExport_toastNamesTheStartingFilterSet', async () => {
    const originalEvent = auditEvent({ id: 'evt-original', actor_name: 'Original Actor' })
    const calls: string[] = []
    let release: ((v: MockResponse) => void) | null = null
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      if (isExportPageUrl(url)) return new Promise<MockResponse>((res) => (release = res))
      return Promise.resolve(logResponse())
    })
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders').toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-export'))
    await waitFor(() => expect(release, 'the export request must actually be pending').not.toBeNull())

    // Change the company filter while the export is still in flight.
    fireEvent.click(screen.getByTestId('audit-company-trigger'))
    fireEvent.click(screen.getByTestId('audit-company-kind-workspace'))
    await waitFor(() => expect(screen.getByTestId('audit-pill-company'), 'the filter change must actually land').toBeTruthy())

    release!(logResponse({ events: [originalEvent], total: 1, page: { limit: 100, has_more: false, next_cursor: null } }))

    await waitFor(() => {
      expect(dl.clicks.length, 'the download must complete despite the mid-flight filter change').toBe(1)
      expect(screen.getByTestId('audit-export-toast'), 'a completion toast must render').toBeTruthy()
    })

    const exportCalls = calls.filter(isExportPageUrl)
    expect(exportCalls.length, 'the mid-flight filter change must not trigger a second export request').toBe(1)
    expect(exportCalls[0], 'no export request may carry the post-change company filter').not.toContain('company=')

    const bytes = await blobBytes(dl.blob as Blob)
    const text = new TextDecoder('utf-8').decode(bytes.slice(3))
    expect(text, "the file written must reflect the ORIGINAL filter's row, not the changed one").toContain('Original Actor')

    const toastText = screen.getByTestId('audit-export-toast').textContent ?? ''
    expect(toastText, "the toast must report the original filter's row count (1)").toMatch(/\b1\b/)

    dl.restore()
  })

  it('auditExport_secondClickIsBlockedWhileRunning', async () => {
    let release: ((v: MockResponse) => void) | null = null
    const fetchMock = vi.fn((url: string) =>
      isExportPageUrl(url) ? new Promise<MockResponse>((res) => (release = res)) : Promise.resolve(logResponse()),
    )
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders').toBeTruthy())

    const btn = screen.getByTestId('audit-export') as HTMLButtonElement
    fireEvent.click(btn)
    await waitFor(() => expect(btn.disabled, 'the button must disable as soon as the export starts').toBe(true))
    // jsdom still dispatches a click on a disabled button (only the native .click() method
    // respects the attribute) -- this proves the HANDLER guards re-entry, not just the DOM.
    fireEvent.click(btn)

    release!(logResponse({ events: [auditEvent()], total: 1, page: { limit: 100, has_more: false, next_cursor: null } }))
    await waitFor(() => expect(dl.clicks.length, 'the single run must complete').toBe(1))

    expect(dl.createSpy, 'a click while disabled must not add a second download').toHaveBeenCalledTimes(1)
    dl.restore()
  })

  it('auditExport_secondClickNeverIssuesASecondExportRequest: the disabled control never reaches the handler a second time, so no second network call fires', async () => {
    const exportCalls: string[] = []
    let release: ((v: MockResponse) => void) | null = null
    const fetchMock = vi.fn((url: string) => {
      if (!isExportPageUrl(url)) return Promise.resolve(logResponse())
      exportCalls.push(url)
      return new Promise<MockResponse>((res) => (release = res))
    })
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders').toBeTruthy())

    const btn = screen.getByTestId('audit-export') as HTMLButtonElement
    fireEvent.click(btn)
    await waitFor(() => expect(exportCalls.length, 'the first click must issue exactly one export request').toBe(1))
    await waitFor(() => expect(btn.disabled, 'the button must disable as soon as the export starts').toBe(true))

    fireEvent.click(btn)

    expect(exportCalls.length, 'a second click while exporting must never issue a second export request').toBe(1)

    release!(logResponse({ events: [auditEvent()], total: 1, page: { limit: 100, has_more: false, next_cursor: null } }))
    await waitFor(() => expect(dl.clicks.length, 'the single run must complete').toBe(1))

    dl.restore()
  })

  it('auditExport_disabledAtZeroRows', async () => {
    const calls: string[] = []
    const fetchMock = vi.fn((url: string) => {
      calls.push(url)
      return Promise.resolve(logResponse({ events: [], total: 0, log_is_empty: false }))
    })
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-empty-by-filter'), 'control needle: the zero-row state must land').toBeTruthy())

    const btn = screen.getByTestId('audit-export') as HTMLButtonElement
    expect(btn.disabled, 'zero rows must disable the export control').toBe(true)
    expect(btn.style.opacity, 'the disabled dim must read exactly 0.4').toBe('0.4')
    expect(btn.style.cursor, 'the disabled cursor must read not-allowed').toBe('not-allowed')
    expect(btn.style.background, 'the disabled background must read transparent').toBe('transparent')
    expect(btn.hidden, 'a disabled control must still be a real, unhidden DOM node').toBe(false)

    const callsBefore = calls.length
    fireEvent.click(btn) // reaches the handler in jsdom regardless of the disabled attribute
    await new Promise((r) => setTimeout(r, 10))
    expect(calls.length, 'a click at zero rows must fire no export request').toBe(callsBefore)
    expect(dl.createSpy, 'a click at zero rows must never reach the download step').not.toHaveBeenCalled()

    dl.restore()
  })

  it('auditExport_disabledReasonIsVisibleText', async () => {
    mockFetchSequence([logResponse({ events: [], total: 0, log_is_empty: false })])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-empty-by-filter'), 'control needle: the zero-row state must land').toBeTruthy())

    const btn = screen.getByTestId('audit-export')
    const describedbyId = btn.getAttribute('aria-describedby')
    expect(describedbyId, 'the disabled control must point aria-describedby at a reason element').toBeTruthy()

    const reason = screen.getByTestId('audit-export-reason')
    expect(reason.id, 'aria-describedby must resolve to the visible reason element, not some other id').toBe(describedbyId)
    expect((reason.textContent ?? '').trim().length, 'the reason text must not be empty').toBeGreaterThan(0)
    expect(reason.hidden, 'a title-only or aria-label-only reason would leave no visible element -- this one must not be hidden').toBe(false)
  })

  it('auditExport_carriesNoTitleAttribute', async () => {
    mockFetchSequence([logResponse({ events: [], total: 0, log_is_empty: false })])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'positive floor: the button resolves at zero rows').toBeTruthy())
    const zeroRowsBtn = screen.getByTestId('audit-export')
    cleanup()

    mockFetchSequence([logResponse()])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'positive floor: the button resolves at non-zero rows').toBeTruthy())
    const nonZeroRowsBtn = screen.getByTestId('audit-export')

    expect(zeroRowsBtn.hasAttribute('title'), 'the zero-row control must carry no title attribute').toBe(false)
    expect(nonZeroRowsBtn.hasAttribute('title'), 'the non-zero-row control must carry no title attribute').toBe(false)
  })

  it('auditExport_oneClickOneDownload', async () => {
    const rows = Array.from({ length: 12 }, (_, i) => auditEvent({ id: `evt-${i}`, actor_name: `Actor ${i}` }))
    const fetchMock = vi.fn((url: string) =>
      isExportPageUrl(url)
        ? Promise.resolve(logResponse({ events: rows, total: 12, page: { limit: 100, has_more: false, next_cursor: null } }))
        : Promise.resolve(logResponse({ events: [rows[0]], total: 12 })),
    )
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders').toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-export'))

    await waitFor(() => {
      expect(dl.createSpy, 'exactly one Blob must be created').toHaveBeenCalledTimes(1)
      expect(dl.clicks.length, 'exactly one anchor click must fire the download').toBe(1)
      expect(screen.getByTestId('audit-export-toast'), 'a completion toast must render').toBeTruthy()
    })
    expect(dl.clicks[0].download, 'the anchor must carry a download filename').toBeTruthy()

    const bytes = await blobBytes(dl.blob as Blob)
    expect(Array.from(bytes.slice(0, 3)), 'the file must open cleanly in Excel: byte-exact UTF-8 BOM').toEqual([0xef, 0xbb, 0xbf])
    expect(new TextDecoder('utf-8').decode(bytes.slice(3)), "the CSV body must be the pure serializer's own output").toBe(auditCsv(rows))

    dl.restore()
  })

  it('auditExport_captionIsTheFormatTag', async () => {
    mockFetchSequence([logResponse()])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders').toBeTruthy())

    expect(screen.getByTestId('audit-export').textContent?.trim(), 'the caption is the exact format tag').toBe('CSV · THE ROWS ON SCREEN')
  })

  it('auditExport_hiddenOnNewWorkspace', async () => {
    mockFetchSequence([logResponse({ events: [], total: 0, log_is_empty: true })])
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-new-workspace'), 'control needle: the new-workspace state must land').toBeTruthy())

    expect(screen.queryByTestId('audit-export'), 'export must be entirely absent on a new workspace, not merely disabled').toBeNull()
  })

  it('auditExport_toastTextIsTheLibFunctionsOutput: the rendered toast text equals auditExportToastCopy\'s own output for the same inputs', async () => {
    const rows = [auditEvent({ id: 'evt-pin', actor_name: 'Pin Actor' })]
    const fetchMock = vi.fn((url: string) =>
      isExportPageUrl(url)
        ? Promise.resolve(logResponse({ events: rows, total: 1, page: { limit: 100, has_more: false, next_cursor: null } }))
        : Promise.resolve(logResponse({ events: rows, total: 1 })),
    )
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders').toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-export'))
    await waitFor(() => expect(screen.getByTestId('audit-export-toast'), 'a completion toast must render').toBeTruthy())

    const filename = dl.clicks[0]?.download ?? ''
    expect(filename, 'positive floor: the anchor must actually carry a download filename').toBeTruthy()
    const expected = auditExportToastCopy({
      rows: rows.length,
      bytes: (dl.blob as Blob).size,
      filename,
      truncated: false,
      cap: AUDIT_EXPORT_CAP,
    })

    await waitFor(() => {
      expect(
        screen.getByTestId('audit-export-toast').textContent,
        "the rendered toast text must be the lib function's own output, not a hand-written copy of it",
      ).toBe(expected)
    })

    dl.restore()
  })

  it('auditExport_capWiringStopsAt2000AndNamesItInTheToast: an endless stream stops at 2000 and the toast states it in the same sentence as the count', async () => {
    let n = 0
    const fetchMock = vi.fn((url: string) => {
      if (!isExportPageUrl(url)) return Promise.resolve(logResponse())
      n += 1
      return Promise.resolve(
        logResponse({
          events: Array.from({ length: 100 }, (_, i) => auditEvent({ id: `evt-cap-${n}-${i}` })),
          total: 100_000,
          page: { limit: 100, has_more: true, next_cursor: `cursor-${n}` },
        }),
      )
    })
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders').toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-export'))

    await waitFor(
      () => {
        expect(dl.clicks.length, 'an endless stream must still complete, stopped by the cap').toBe(1)
        expect(screen.getByTestId('audit-export-toast'), 'a completion toast must render').toBeTruthy()
      },
      { timeout: 3000 },
    )

    const bytes = await blobBytes(dl.blob as Blob)
    const dataLines = new TextDecoder('utf-8')
      .decode(bytes.slice(3))
      .split('\n')
      .filter((l) => l.length > 0)
      .slice(1)
    // Hardcoded 2000, not AUDIT_EXPORT_CAP: this must catch AuditView wiring in a different cap.
    expect(dataLines.length, 'the written file must hold exactly 2000 data rows').toBe(2000)

    await waitFor(() => {
      const toastText = screen.getByTestId('audit-export-toast').textContent ?? ''
      const sentences = toastText.split(/\.\s+/).filter((s) => s.length > 0)
      const capSentence = sentences.find((s) => s.includes('2000'))
      expect(capSentence, 'no sentence in the toast names the 2000-row count').toBeDefined()
      expect(capSentence, 'the sentence naming the count must also state the cap').toMatch(/cap|capped|limit|maximum|most/i)
    })

    dl.restore()
  })

  it('auditExport_exactlyAtCapWithNoMoreIsNotTruncated: landing on 2000 via a real has_more:false never claims a cap that never bit', async () => {
    const rows = Array.from({ length: 2000 }, (_, i) => auditEvent({ id: `evt-exact-${i}` }))
    const fetchMock = vi.fn((url: string) =>
      isExportPageUrl(url)
        ? Promise.resolve(logResponse({ events: rows, total: 2000, page: { limit: 100, has_more: false, next_cursor: null } }))
        : Promise.resolve(logResponse({ events: [rows[0]], total: 2000 })),
    )
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders').toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-export'))

    await waitFor(() => {
      expect(dl.clicks.length, 'export must complete').toBe(1)
      expect(screen.getByTestId('audit-export-toast'), 'a completion toast must render').toBeTruthy()
    })

    await waitFor(() => {
      const toastText = screen.getByTestId('audit-export-toast').textContent ?? ''
      expect(toastText, 'must still name the 2000 rows actually exported').toMatch(/\b2000\b/)
      expect(toastText, 'a clean has_more:false stop must never claim a cap, even landing exactly on the cap number').not.toMatch(/cap/i)
    })

    dl.restore()
  })

  it('auditExport_unmountMidExportSurfacesNoWarningOrUnhandledRejection', async () => {
    let release: ((v: MockResponse) => void) | null = null
    const fetchMock = vi.fn((url: string) =>
      isExportPageUrl(url) ? new Promise<MockResponse>((res) => (release = res)) : Promise.resolve(logResponse()),
    )
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    const rejections: unknown[] = []
    const onRejection = (e: PromiseRejectionEvent) => rejections.push(e.reason)
    window.addEventListener('unhandledrejection', onRejection)

    const { unmount } = render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByTestId('audit-export'), 'control needle: export renders').toBeTruthy())

    fireEvent.click(screen.getByTestId('audit-export'))
    await waitFor(() => expect(release, 'the export request must actually be pending').not.toBeNull())

    unmount()
    release!(logResponse({ events: [auditEvent()], total: 1, page: { limit: 100, has_more: false, next_cursor: null } }))
    await act(async () => {
      for (let i = 0; i < 20; i++) await Promise.resolve()
    })

    expect(consoleError, 'resolving an export after unmount must not log a console error').not.toHaveBeenCalled()
    expect(rejections, 'resolving an export after unmount must not surface as an unhandled rejection').toEqual([])

    window.removeEventListener('unhandledrejection', onRejection)
    consoleError.mockRestore()
    dl.restore()
  })
})
