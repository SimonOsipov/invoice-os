// @vitest-environment jsdom
// AUDIT-06-07's RED specs. The `mockFetchSequence` / narrowed-ctx idiom is
// ApprovalsView.test.tsx's, unchanged.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { StrictMode } from 'react'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AuditEvent, AuditResponse } from '../lib/audit'
import { auditCsv, auditExportToastCopy } from '../lib/auditCsv'
import { AUDIT_FILTER_DEFAULT, auditFilterPills } from '../lib/auditFilters'
import { AUDIT_COPY, AUDIT_EXPORT_CAP, invoiceFilterPillLabel } from '../lib/auditView'
import { createAuthedFetch } from '../lib/authedFetch'
import { EVIDENCE_COPY } from '../lib/evidenceBundleView'
import type { AuditPrefilter, PlatformCtx } from '../types'

import { AuditExportToast } from './AuditExportToast'
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
    entities: [],
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
  // A failing assertion skips the explicit dl.restore(), leaking the download spies into
  // the next case and turning one real failure into several false ones.
  vi.restoreAllMocks()
})

// Applies the screen's only filter in this story: the row expansion's invoice affordance.
// The filter card itself is AUDIT-07's.
// Distinguishes the lifetime probe from the main request by an exact param, never call order:
// the probe is the only request carrying limit=1. It is NOT "the one with no date window" any
// more -- a prefiltered mount (AUDIT-09-05) sends no `from` on the main request either, which
// is why the prefilter specs key on limit=25 instead.
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

// AUDIT-08-03 (task-666): the header's primary bundle trigger, specified as a strict diff
// against AUDIT-07's shipped ghost. Authored RED at Stage 2.5 against an unimplemented
// component, so EB-03-1..4, 6 and 7 fail on a missing element. EB-03-8/9 assert an ABSENCE
// and EB-03-10 fences the sibling, so all three are green from the start by design.
const ZIP_CAPTION = 'ZIP · ONE COMPANY, ONE PERIOD'
// shieldGlyph15's two paths (glyphs.tsx:19), restated so the spec pins the drawn shape
// rather than reading back whatever the component imported.
const SHIELD_PATHS = ['M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10Z', 'm9 12 2 2 4-4']

// The three states the header gate admits. Each returns only after its own landed needle,
// so a fixture that silently resolves to `loading` cannot satisfy a spec vacuously.
async function landLoaded() {
  mockFetchSequence([logResponse()])
  render(<AuditView ctx={auditCtx()} />)
  await waitFor(() =>
    expect(screen.getAllByTestId('audit-row').length, 'landed needle: the loaded rung must render rows').toBeGreaterThan(0),
  )
}

async function landFiltered() {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(logResponse())))
  render(<AuditView ctx={auditCtx()} />)
  await applyInvoiceFilter()
  await waitFor(() =>
    expect(screen.getByTestId('audit-pill-invoice'), 'landed needle: the invoice pill proves the filtered rung').toBeTruthy(),
  )
  expect(
    screen.getAllByTestId('audit-row').length,
    'landed needle: filtered still has rows, so this is not empty-by-filter wearing a pill',
  ).toBeGreaterThan(0)
}

async function landEmptyByFilter() {
  mockFetchSequence([logResponse({ events: [], total: 0, log_is_empty: false })])
  render(<AuditView ctx={auditCtx()} />)
  await waitFor(() =>
    expect(screen.getByTestId('audit-empty-by-filter'), 'landed needle: the empty-by-filter rung must land').toBeTruthy(),
  )
}

const GATED_STATES = [
  { name: 'loaded', land: landLoaded },
  { name: 'filtered', land: landFiltered },
  { name: 'empty-by-filter', land: landEmptyByFilter },
]

function glyphPaths(el: HTMLElement): string[] {
  return Array.from(el.querySelectorAll('svg path'), (p) => p.getAttribute('d') ?? '')
}

describe('AuditView evidence-bundle trigger (AUDIT-08-03)', () => {
  it('EB-03-1 bundleButton_isPrimaryNotGhost', async () => {
    await landLoaded()
    const bundle = screen.getByTestId('audit-bundle-open')
    const ghost = screen.getByTestId('audit-export')

    expect(bundle.className, 'the new control carries primary weight').toContain('v2-btn-primary')
    expect(bundle.className, 'a primary must not also carry the ghost class').not.toContain('v2-btn-ghost')
    expect(bundle.className, "the repo's button base class").toContain('v2-btn ')
    expect(bundle.className, "and the platform's button class").toContain('pf-btn')
    expect(bundle.getAttribute('type'), 'a button outside a form still declares its type').toBe('button')

    expect(ghost.className, 'the sibling stays ghost').toContain('v2-btn-ghost')
    expect(ghost.className, 'the sibling must not be promoted to primary').not.toContain('v2-btn-primary')

    // The diff is weight and glyph, never geometry: one measured 36px control height on the
    // row. Compared against the sibling, not against literals, so the pair can only drift
    // together -- and the sibling's own literals are fenced by EB-03-10.
    for (const prop of ['display', 'alignItems', 'gap', 'height', 'padding', 'fontSize'] as const) {
      expect(bundle.style[prop], `geometry must match the sibling byte-for-byte: ${prop}`).toBe(ghost.style[prop])
    }
    expect(bundle.style.height, 'control needle: the compared geometry must be real, not two empty strings').toBe('36px')
  })

  it('EB-03-2 bundleButton_captionIsTheZipTag', async () => {
    await landLoaded()
    const bundle = screen.getByTestId('audit-bundle-open')

    const mono = bundle.querySelector('.mono')
    expect(mono, "the caption is a .mono span, the sibling's grammar").toBeTruthy()
    expect(mono?.textContent, 'the exact format tag, middle dot U+00B7').toBe(ZIP_CAPTION)
    expect(
      mono?.textContent,
      'the literal must be sourced from EVIDENCE_COPY.openCaption, never typed into the component',
    ).toBe(EVIDENCE_COPY.openCaption)
    expect(ZIP_CAPTION.charCodeAt(4), 'control needle: the pinned string separator is U+00B7, not a bullet').toBe(0x00b7)

    // D-08-05: the sibling diff is glyph + caption + class, not wording. Like the ghost, this
    // control carries no aria-label and no prose label such as "Download evidence bundle" --
    // the visible mono caption is its only text. Nothing here is a missing label to "fix".
    expect(bundle.hasAttribute('aria-label'), 'no aria-label: the visible caption is the accessible name').toBe(false)
    expect(bundle.textContent?.trim(), 'no prose label beside the caption').toBe(ZIP_CAPTION)

    expect(screen.getByTestId('audit-export').textContent?.trim(), "the sibling's caption is untouched").toBe(
      AUDIT_COPY.exportCaption,
    )
  })

  it('EB-03-3 bundleButton_glyphDiffersFromTheSiblings', async () => {
    await landLoaded()
    const bundle = screen.getByTestId('audit-bundle-open')
    const ghost = screen.getByTestId('audit-export')

    const bundlePaths = glyphPaths(bundle)
    const ghostPaths = glyphPaths(ghost)
    // Vacuity floor, before any comparison: two EMPTY collections are trivially "different",
    // so a selector typo would read as a pass. A scan is only worth its haystack.
    expect(bundlePaths.length, 'vacuity floor: the primary must draw at least one glyph path').toBeGreaterThan(0)
    expect(ghostPaths.length, 'vacuity floor: the ghost must draw at least one glyph path').toBeGreaterThan(0)

    expect(bundlePaths.join('|'), 'the two controls must not share one glyph').not.toBe(ghostPaths.join('|'))
    expect(bundlePaths, 'the primary must carry shieldGlyph15 exactly -- no new glyph, no redraw').toEqual(SHIELD_PATHS)

    // AC-3's weight claim: 15 at 1.6, the sibling's. A 2px shield beside a 1.6px download is
    // two glyph weights on one row.
    const bundleSvg = bundle.querySelector('svg')
    const ghostSvg = ghost.querySelector('svg')
    expect(bundleSvg?.getAttribute('width'), 'no resize at the call site').toBe('15')
    expect(bundleSvg?.getAttribute('height'), 'no resize at the call site').toBe('15')
    expect(bundleSvg?.getAttribute('stroke-width'), "Icon's default weight, not an override").toBe('1.6')
    expect(bundleSvg?.getAttribute('stroke-width'), 'one glyph weight on the row').toBe(ghostSvg?.getAttribute('stroke-width'))
  })

  it('EB-03-4 bundleButton_precedesTheGhostSibling', async () => {
    await landLoaded()
    const bundle = screen.getByTestId('audit-bundle-open')
    const ghost = screen.getByTestId('audit-export')

    expect(bundle.parentElement, 'both controls sit in one row').toBe(ghost.parentElement)
    expect(
      bundle.compareDocumentPosition(ghost) & Node.DOCUMENT_POSITION_FOLLOWING,
      'the primary reads first, so the ghost stays rightmost',
    ).toBeTruthy()

    // The pair needs a designed gutter: the shipped wrapper is textAlign:right with no flex
    // and no gap, so a bare second child would render on collapsed whitespace. The gap VALUE
    // is not asserted here -- the topology sweep (L5) owns the pixel relationship.
    const row = bundle.parentElement as HTMLElement
    expect(row.style.display, 'the pair sits in a flex row').toBe('flex')
    expect(row.style.justifyContent, "flush right, so the ghost keeps the content column's edge").toBe('flex-end')
    expect(row.style.gap, 'the gutter must be designed, not collapsed whitespace').not.toBe('')
  })

  it.each(GATED_STATES)('EB-03-5 bundleButton_isNeverDisabledInAnyReachableState: $name', async ({ land }) => {
    await land()

    const btn = screen.getByTestId('audit-bundle-open') as HTMLButtonElement
    expect(btn.disabled, 'the drawer opens whatever the screen shows -- the bundle is not the view').toBe(false)
    expect(btn.hasAttribute('disabled'), 'no disabled prop is written at all').toBe(false)
    // No dead disabled recipe shipped: the filter:none neutraliser exists only for a primary
    // that CAN render disabled, and no reachable state renders this one disabled.
    expect(btn.style.filter, 'no filter neutraliser').toBe('')
    expect(btn.style.opacity, 'no dim').toBe('')
    expect(btn.style.cursor, 'no not-allowed cursor').toBe('')
  })

  it('EB-03-6 bundleButton_staysEnabledWhileTheGhostIsDisabled', async () => {
    await landEmptyByFilter()

    const ghost = screen.getByTestId('audit-export') as HTMLButtonElement
    expect(ghost.disabled, 'zero rows still disables the ghost -- AUDIT-07 unchanged').toBe(true)
    const reason = screen.getByTestId('audit-export-reason')
    expect((reason.textContent ?? '').trim().length, "the ghost's refusal stays visible text").toBeGreaterThan(0)

    const bundle = screen.getByTestId('audit-bundle-open') as HTMLButtonElement
    expect(bundle.disabled, 'a live primary beside a dimmed ghost is the product claim, not a defect').toBe(false)
    expect(reason.parentElement, 'the reason line stays a sibling below the pair, never a flex item beside it').not.toBe(
      bundle.parentElement,
    )
  })

  it('EB-03-7 bundleButton_opensTheDrawerState', async () => {
    await landLoaded()

    const btn = screen.getByTestId('audit-bundle-open')
    // The first dialog trigger in this SPA. The four aria-expanded sites in src/ are in-place
    // expanders and popovers, none of which open an overlay -- this is a new pattern here,
    // and the correct ARIA for a control that opens a modal dialog.
    expect(btn.getAttribute('aria-haspopup'), 'a control that opens a dialog must say so').toBe('dialog')
    expect(btn.getAttribute('aria-expanded'), 'closed before the first click').toBe('false')

    fireEvent.click(btn)

    await waitFor(() =>
      expect(
        screen.getByTestId('audit-bundle-open').getAttribute('aria-expanded'),
        'the click flips the seam subtask 04 mounts the drawer against',
      ).toBe('true'),
    )
  })

  it('EB-03-8 bundleButton_absentOnANewWorkspace', async () => {
    mockFetchSequence([logResponse({ events: [], total: 0, log_is_empty: true })])
    render(<AuditView ctx={auditCtx()} />)
    // Positive rung FIRST: queryBy...toBeNull passes against a blank screen, so the absence
    // is only evidence of the gate once the new-workspace rung has demonstrably rendered.
    await waitFor(() =>
      expect(screen.getByTestId('audit-new-workspace'), 'landed needle: the new-workspace rung must render').toBeTruthy(),
    )

    expect(screen.queryByTestId('audit-bundle-open'), 'absent on a new workspace, not merely disabled').toBeNull()
    expect(screen.queryByTestId('audit-export'), 'one gate, both controls').toBeNull()
  })

  it('EB-03-9 bundleButton_absentWhileLoadingAndOnError', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(() => new Promise<MockResponse>(() => {})),
    )
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() =>
      expect(
        screen.getAllByTestId('audit-skeleton-row').length,
        'landed needle: the loading rung draws its skeleton',
      ).toBeGreaterThan(0),
    )
    expect(screen.queryByTestId('audit-bundle-open'), 'absent while loading').toBeNull()
    expect(screen.queryByTestId('audit-export'), 'one gate, both controls').toBeNull()
    cleanup()

    vi.stubGlobal(
      'fetch',
      vi.fn(() => Promise.reject(new Error('boom'))),
    )
    render(<AuditView ctx={auditCtx()} />)
    await waitFor(() => expect(screen.getByText('Something went wrong'), 'landed needle: the error rung must render').toBeTruthy())
    expect(screen.queryByTestId('audit-bundle-open'), 'absent on error').toBeNull()
    expect(screen.queryByTestId('audit-export'), 'one gate, both controls').toBeNull()
  })

  it('EB-03-10 auditExportControl_isUnchanged', async () => {
    // AUDIT-07's regression fence (AC-6). Literals, not the component's own constants: a
    // fence that reads the thing it guards cannot see the thing it guards move.
    const assertGhost = (btn: HTMLElement) => {
      expect(btn.className).toBe('v2-btn v2-btn-ghost pf-btn')
      expect(btn.getAttribute('type')).toBe('button')
      expect(btn.style.display).toBe('inline-flex')
      expect(btn.style.alignItems).toBe('center')
      expect(btn.style.gap).toBe('8px')
      expect(btn.style.height).toBe('36px')
      expect(btn.style.fontSize).toBe('13px')
      expect(btn.style.paddingTop).toBe('0px')
      expect(btn.style.paddingLeft).toBe('14px')
      expect(btn.style.paddingRight).toBe('14px')
      expect(btn.textContent?.trim()).toBe('CSV · THE ROWS ON SCREEN')
    }

    await landLoaded()
    const enabled = screen.getByTestId('audit-export') as HTMLButtonElement
    assertGhost(enabled)
    expect(enabled.disabled, 'rows on screen leave the ghost live').toBe(false)
    expect(enabled.hasAttribute('aria-describedby'), 'no reason to describe while rows match').toBe(false)
    expect(screen.queryByTestId('audit-export-reason'), 'no reason line while rows match').toBeNull()
    cleanup()

    await landEmptyByFilter()
    const dimmed = screen.getByTestId('audit-export') as HTMLButtonElement
    assertGhost(dimmed)
    expect(dimmed.disabled, 'zero rows still disables it').toBe(true)
    expect(dimmed.style.opacity, 'the dim recipe is unchanged').toBe('0.4')
    expect(dimmed.style.cursor).toBe('not-allowed')
    expect(dimmed.style.background).toBe('transparent')
    expect(dimmed.getAttribute('aria-describedby')).toBe('audit-export-reason')

    const reason = screen.getByTestId('audit-export-reason')
    expect(reason.id).toBe('audit-export-reason')
    expect(reason.textContent).toBe('No rows match the current filters — nothing to export.')
  })
})

// AUDIT-08-03 QA (Mode B): adversarial coverage beyond the acceptance criteria. Each case
// below closes a mutation that survived EB-03-1..10 -- the source-level halves of AC-3 and
// AC-5 that jsdom cannot see, the visual order jsdom cannot measure, and the interactions
// between the two controls that no single-button spec reaches.
describe('AuditView evidence-bundle trigger, adversarial (AUDIT-08-03 QA)', () => {
  // AC-5 says the component "writes no `disabled` prop"; AC-3 says the caption is "sourced
  // from EVIDENCE_COPY.openCaption". Both are claims about the SOURCE, and jsdom cannot see
  // either: React renders no attribute for `disabled={false}`, and a literal typed inline
  // produces the same textContent as the imported key. Measured, not assumed -- both
  // mutations were run and stayed green against EB-03-1..10.
  //
  // Scan idiom and vacuity floor are auditFilterCard_isNotInsideTheLoadedRung's. The control
  // needle is the ghost: the same slicing machinery must FIND a disabled prop where one is
  // written, otherwise the primary's clean slice proves only that the scan missed.
  it('EB-03-11 bundleButton_sourceWritesNoDisabledAndSourcesItsCaption', () => {
    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'AuditView.tsx'), 'utf8')
    expect(src.length, 'AuditView.tsx must be non-empty').toBeGreaterThan(0)

    const sliceButton = (testid: string) => {
      const start = src.indexOf(`data-testid="${testid}"`)
      expect(start, `the scan must find the ${testid} button`).toBeGreaterThan(-1)
      const end = src.indexOf('</button>', start)
      expect(end, `the ${testid} button must close`).toBeGreaterThan(start)
      return src.slice(start, end)
    }

    const primary = sliceButton('audit-bundle-open')
    const ghost = sliceButton('audit-export')
    expect(primary.length, 'vacuity floor: the primary slice must carry a real button body').toBeGreaterThan(100)
    expect(ghost.length, 'vacuity floor: the ghost slice must carry a real button body').toBeGreaterThan(100)

    // Control needles: the machinery finds what it is looking for when it is there.
    expect(ghost, 'control needle: the ghost DOES write a disabled prop, so the scan can see one').toContain('disabled=')
    expect(ghost, 'control needle: the ghost DOES ship a disabled style, so the scan can see one').toContain('not-allowed')
    expect(primary, 'control needle: the primary slice is the right region').toContain('v2-btn-primary')

    expect(primary, 'AC-5: no disabled prop is written at all -- not even disabled={false}').not.toContain('disabled')
    expect(primary, 'AC-5: no dead dim recipe').not.toContain('opacity')
    expect(primary, 'AC-5: no dead not-allowed cursor').not.toContain('not-allowed')
    expect(primary, 'AC-5: the filter:none neutraliser stays dropped as unreachable').not.toContain('filter:')

    // AC-3 / [bulk-copy-lives-in-the-lib]: the caption is read from the lib, never typed here.
    expect(primary, 'the caption comes from EVIDENCE_COPY.openCaption').toContain('EVIDENCE_COPY.openCaption')
    expect(primary, 'the literal must not be typed into the component beside the key').not.toContain('ZIP ')
  })

  // A row-reverse (or wrap-reverse) flips what the user sees while leaving DOM order, and so
  // EB-03-4, untouched -- measured: that mutation stayed green. AC-1's claim is visual
  // ("left of audit-export"), so this fences the shipped style; the pixel oracle is subtask
  // 07's L5 sweep, which jsdom cannot stand in for.
  it('EB-03-12 bundleRow_neverReversesItsVisualOrder', async () => {
    await landLoaded()
    const row = screen.getByTestId('audit-bundle-open').parentElement as HTMLElement

    expect(row.style.display, 'control needle: the scanned element really is the flex row').toBe('flex')
    expect(row.style.flexDirection, 'DOM order must equal visual order: no row-reverse').not.toMatch(/reverse/)
    expect(row.style.flexWrap, 'and no wrap-reverse').not.toMatch(/reverse/)
  })

  // Neither control carries an aria-label and both glyphs are aria-hidden, so each button's
  // accessible name is its mono caption alone. A screen-reader user must be able to tell the
  // two apart without the visual weight difference that separates them on screen.
  it('EB-03-13 headerControls_haveDistinctAccessibleNames', async () => {
    await landLoaded()
    const bundle = screen.getByTestId('audit-bundle-open')
    const ghost = screen.getByTestId('audit-export')

    for (const [name, btn] of [
      ['primary', bundle],
      ['ghost', ghost],
    ] as const) {
      expect(btn.hasAttribute('aria-label'), `${name}: the visible caption is the accessible name`).toBe(false)
      expect(btn.hasAttribute('aria-labelledby'), `${name}: nothing else supplies the name`).toBe(false)
      const svg = btn.querySelector('svg')
      expect(svg, `control needle: ${name} must actually draw a glyph`).toBeTruthy()
      expect(svg?.getAttribute('aria-hidden'), `${name}: the glyph must not leak into the name`).toBe('true')
    }

    const bundleName = (bundle.textContent ?? '').trim()
    const ghostName = (ghost.textContent ?? '').trim()
    expect(bundleName.length, 'a nameless button is unreachable by voice or screen reader').toBeGreaterThan(0)
    expect(ghostName.length, 'the sibling must stay named too').toBeGreaterThan(0)
    expect(bundleName, 'two controls on one row must not announce identically').not.toBe(ghostName)
  })

  // The pair shares a row and a click target size; a mis-wired handler or a stray form
  // submit would run AUDIT-07's export off the primary. Differential, not merely negative:
  // the same harness in the same test proves the ghost DOES download, so the primary's
  // silence is evidence rather than a dead spy.
  it('EB-03-14 bundleClick_doesNotRunTheGhostExport', async () => {
    // The export page must echo limit=100 or collectExportRows aborts and the control below
    // would "pass" on an ERROR toast with no download -- the exact false green this case exists
    // to rule out. Same shape as auditExport_oneClickOneDownload.
    const fetchMock = vi.fn((url: string) =>
      isExportPageUrl(url)
        ? Promise.resolve(logResponse({ page: { limit: 100, has_more: false, next_cursor: null } }))
        : Promise.resolve(logResponse()),
    )
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    try {
      render(<AuditView ctx={auditCtx()} />)
      await waitFor(() =>
        expect(screen.getAllByTestId('audit-row').length, 'landed needle: the loaded rung must render rows').toBeGreaterThan(0),
      )
      const callsBefore = fetchMock.mock.calls.length

      fireEvent.click(screen.getByTestId('audit-bundle-open'))
      await waitFor(() => expect(screen.getByTestId('audit-bundle-open').getAttribute('aria-expanded')).toBe('true'))

      expect(dl.createSpy, 'opening the drawer must not build a CSV blob').not.toHaveBeenCalled()
      expect(dl.clicks, 'opening the drawer must not click a download anchor').toEqual([])
      expect(screen.queryByTestId('audit-export-toast'), "opening the drawer must not raise the ghost's toast").toBeNull()
      expect(fetchMock.mock.calls.length, 'opening the drawer issues no request of its own in this subtask').toBe(callsBefore)

      // Control: the spies are live and this screen CAN export. A download, not merely a
      // toast -- an aborted export raises a toast too.
      fireEvent.click(screen.getByTestId('audit-export'))
      await waitFor(() => expect(dl.clicks.length, 'control needle: the download spy was never dead').toBe(1))
      expect(dl.createSpy, 'control needle: the ghost still builds its blob through the same spies').toHaveBeenCalledTimes(1)
    } finally {
      dl.restore()
    }
  })

  // The two controls are independent: the ghost being mid-export dims the ghost alone. The
  // needle separates `exporting` from `zeroRows` -- rows are on screen, so the reason line is
  // absent and the only thing that can have disabled the ghost is the in-flight export.
  it('EB-03-15 bundleButton_staysLiveWhileTheGhostIsExporting', async () => {
    const fetchMock = vi.fn((url: string) =>
      isExportPageUrl(url) ? new Promise<MockResponse>(() => {}) : Promise.resolve(logResponse()),
    )
    vi.stubGlobal('fetch', fetchMock)
    const dl = stubDownload()
    try {
      render(<AuditView ctx={auditCtx()} />)
      await waitFor(() => expect(screen.getAllByTestId('audit-row').length).toBeGreaterThan(0))

      fireEvent.click(screen.getByTestId('audit-export'))

      const ghost = await waitFor(() => {
        const g = screen.getByTestId('audit-export') as HTMLButtonElement
        expect(g.disabled, 'landed needle: the export must actually be in flight').toBe(true)
        return g
      })
      expect(ghost.style.opacity, "the ghost wears AUDIT-07's dim while exporting").toBe('0.4')
      expect(screen.queryByTestId('audit-export-reason'), 'needle: rows are on screen, so this is exporting, not zeroRows').toBeNull()

      const bundle = screen.getByTestId('audit-bundle-open') as HTMLButtonElement
      expect(bundle, 'the primary must survive the sibling being busy').toBeTruthy()
      expect(bundle.disabled, 'an in-flight CSV export says nothing about the bundle drawer').toBe(false)
      fireEvent.click(bundle)
      await waitFor(() => expect(screen.getByTestId('audit-bundle-open').getAttribute('aria-expanded')).toBe('true'))
    } finally {
      dl.restore()
    }
  })

  it('EB-03-16 bundleButton_doubleClickStaysOpen', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    await landLoaded()
    const btn = screen.getByTestId('audit-bundle-open')

    fireEvent.click(btn)
    fireEvent.click(btn)
    await waitFor(() =>
      expect(
        screen.getByTestId('audit-bundle-open').getAttribute('aria-expanded'),
        'a second click on an open trigger must not toggle the drawer shut',
      ).toBe('true'),
    )
    expect(consoleError, 'a repeated click must not warn or throw').not.toHaveBeenCalled()
    consoleError.mockRestore()
  })

  // AC-6 fences the reason line out of the row; this fences everything else out of it, so a
  // third control on this row is a deliberate change with a spec to update rather than an
  // accident that ships.
  it('EB-03-17 bundleRow_holdsExactlyTwoControls', async () => {
    await landLoaded()
    const row = screen.getByTestId('audit-bundle-open').parentElement as HTMLElement
    expect(row.style.display, 'control needle: the counted element really is the flex row').toBe('flex')

    expect(Array.from(row.children).map((c) => c.getAttribute('data-testid')), 'the row holds the pair and nothing else').toEqual([
      'audit-bundle-open',
      'audit-export',
    ])
    expect(row.querySelectorAll('button').length, 'two controls, no nested third').toBe(2)
  })
})

describe('AuditExportToast testId prop (AUDIT-08-06)', () => {
  // EB-06-9 -- asserting a default is the easiest kind of spec to write vacuously: the first
  // render alone passes against a component that ignores the prop entirely. The second render
  // is what makes the prop provably live wire.
  it('toast_defaultTestIdUnchangedForTheCsvExport', () => {
    render(<AuditExportToast kind="success" text="x" onDismiss={vi.fn()} />)
    expect(screen.getByTestId('audit-export-toast')).toBeTruthy()
    cleanup()

    render(<AuditExportToast kind="success" text="x" onDismiss={vi.fn()} testId="probe-toast" />)
    expect(screen.getByTestId('probe-toast')).toBeTruthy()
    expect(screen.queryByTestId('audit-export-toast')).toBeNull()
  })
})

// AUDIT-09-05 Mode A: the "Open in Audit ->" pre-filter hand-off. AuditView reads
// ctx.auditPrefilter ONCE, in a lazy useState initializer, and seeds
// {...AUDIT_FILTER_DEFAULT, range:{preset:'custom'}, invoiceId, invoiceNumber}. Workspace --
// not this component -- clears the atom, in the effect phase of the commit that mounted it.
describe('AuditView pre-filter hand-off (AUDIT-09-05)', () => {
  const PREFILTER_ID = 'aaaaaaaa-0000-4000-8000-000000000001'
  const PREFILTER_NUMBER = 'INV-1'
  const NO_FACETS = { event: [], actor: [], company: [] }

  function prefilteredCtx(pre: AuditPrefilter | null): PlatformCtx {
    return { ...auditCtx(), auditPrefilter: pre } as unknown as PlatformCtx
  }

  // The main request, keyed on limit=25 (AUDIT_PAGE_INITIAL.limit). NOT on "carries a date
  // window" -- that is exactly what a prefiltered mount stops doing (see isProbeUrl above).
  function isMainUrl(url: string): boolean {
    return new URL(url).searchParams.get('limit') === '25'
  }

  function recordCalls(body: (url: string) => MockResponse): string[] {
    const calls: string[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        calls.push(url)
        return Promise.resolve(body(url))
      }),
    )
    return calls
  }

  function mainParams(calls: string[]): URLSearchParams {
    const main = calls.find(isMainUrl)
    expect(main, `no main (limit=25) request was made -- calls: ${JSON.stringify(calls)}`).toBeTruthy()
    return new URL(main!).searchParams
  }

  // Naive: it would also cut a `//` inside a string literal, and AuditView.tsx has none. The
  // paired control needles in the scan below fail loudly if it ever eats too much or too little.
  function stripComments(src: string): string {
    return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '')
  }

  it('auditView_seedsFromPrefilterWithNoDateWindow', async () => {
    const seeded = recordCalls(() => logResponse())
    render(<AuditView ctx={prefilteredCtx({ invoiceId: PREFILTER_ID, invoiceNumber: PREFILTER_NUMBER })} />)
    await waitFor(() => expect(seeded.some(isMainUrl)).toBe(true))

    const pre = mainParams(seeded)
    expect(pre.get('invoice_id'), 'the seeded mount must scope the log to the invoice').toBe(PREFILTER_ID)
    // AUDIT_FILTER_DEFAULT's 30-day window is a PRE-APPLIED filter, so landing on it would show
    // zero rows for an invoice older than a month -- one click after the feed showed them.
    expect(pre.has('from'), 'the seeded range must apply no date filter').toBe(false)
    expect(pre.has('to'), 'the seeded range must apply no date filter').toBe(false)
    cleanup()

    // Control on the same locator: the same request, unprefiltered, carries the window and no
    // invoice. Without it the two absences above could be a recorder that saw nothing.
    const plain = recordCalls(() => logResponse())
    render(<AuditView ctx={prefilteredCtx(null)} />)
    await waitFor(() => expect(plain.some(isMainUrl)).toBe(true))

    const bare = mainParams(plain)
    expect(bare.has('from'), 'control: an unprefiltered mount DOES window by 30 days').toBe(true)
    expect(bare.has('invoice_id'), 'control: an unprefiltered mount sends no invoice').toBe(false)
  })

  it('auditView_prefilterPillReadsTheNumber', async () => {
    mockFetchSequence([logResponse()])
    render(<AuditView ctx={prefilteredCtx({ invoiceId: PREFILTER_ID, invoiceNumber: PREFILTER_NUMBER })} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    expect(screen.queryByTestId('audit-pill-invoice'), 'the seeded mount must render an invoice pill').not.toBeNull()
    // Sourced from the function that owns the copy, never retyped here.
    expect(screen.getByTestId('audit-pill-invoice').textContent).toContain(invoiceFilterPillLabel(PREFILTER_NUMBER))
    // rangePillFor returns null for a custom range with no from/to, so the screen makes no
    // date claim it is not applying.
    expect(screen.queryByTestId('audit-pill-range'), 'a seeded mount must not claim a date window').toBeNull()
    cleanup()

    // The same two locators, inverted, on a plain mount.
    mockFetchSequence([logResponse()])
    render(<AuditView ctx={prefilteredCtx(null)} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())
    expect(screen.getByTestId('audit-pill-range').textContent, 'control: the default pill is the 30-day one').toContain(
      'Last 30 days',
    )
    expect(screen.queryByTestId('audit-pill-invoice'), 'control: no invoice pill without a prefilter').toBeNull()
  })

  it('auditView_prefilterWithNoNumberStillScopesTheLog', async () => {
    // AuditPrefilter.invoiceNumber is nullable because a payload need not carry one. The id is
    // what the reader filters on; the number only decides which of two labels the pill reads.
    const calls = recordCalls(() => logResponse())
    render(<AuditView ctx={prefilteredCtx({ invoiceId: PREFILTER_ID, invoiceNumber: null })} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())

    expect(screen.queryByTestId('audit-pill-invoice'), 'a null number must still produce an invoice pill').not.toBeNull()
    expect(screen.getByTestId('audit-pill-invoice').textContent).toContain(invoiceFilterPillLabel(null))
    expect(screen.getByTestId('audit-pill-invoice').textContent, 'the null branch must not print the id').not.toContain(
      PREFILTER_ID,
    )
    expect(mainParams(calls).get('invoice_id')).toBe(PREFILTER_ID)
  })

  it('auditView_removingThePrefilterPillClearsBothFields', async () => {
    const calls = recordCalls(() => logResponse())
    render(<AuditView ctx={prefilteredCtx({ invoiceId: PREFILTER_ID, invoiceNumber: PREFILTER_NUMBER })} />)
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())
    expect(screen.queryByTestId('audit-pill-invoice'), 'vacuity floor: the seed must have taken').not.toBeNull()
    expect(calls.some((u) => u.includes('invoice_id=')), 'vacuity floor: the seed must have reached the wire').toBe(true)

    const before = calls.length
    fireEvent.click(screen.getByTestId('audit-pill-invoice'))
    await waitFor(() => expect(calls.length).toBeGreaterThan(before))

    expect(calls.length - before, 'removing one pill must fire exactly one refetch').toBe(1)
    const after = new URL(calls[calls.length - 1]!).searchParams
    expect(after.has('invoice_id'), 'the invoice filter must be off the wire').toBe(false)
    // Removing an INVOICE filter is not removing a DATE filter: the log stays unwindowed and
    // `Clear all` is the route back to 30 days (arch section 3).
    expect(after.has('from'), 'removing the invoice pill must not restore the 30-day window').toBe(false)
    await waitFor(() => expect(screen.queryByTestId('audit-pill-invoice')).toBeNull())

    // The invoiceNumber half is not observable through the DOM -- the pill renders only while
    // invoiceId is non-null -- so it is asserted on the exact remover the card calls, built
    // from the state this mount seeds.
    const seededState = {
      ...AUDIT_FILTER_DEFAULT,
      range: { preset: 'custom' as const },
      invoiceId: PREFILTER_ID,
      invoiceNumber: PREFILTER_NUMBER,
    }
    const invoicePill = auditFilterPills(seededState, NO_FACETS).find((p) => p.key === 'invoice')
    expect(invoicePill, 'the seeded state must produce an invoice pill').toBeTruthy()
    const next = invoicePill!.onRemove(seededState)
    expect(next.invoiceId, 'removal must clear the id').toBeNull()
    expect(next.invoiceNumber, 'removal must clear the number too').toBeNull()
  })

  it('auditView_prefilterIsConsumedOnce', async () => {
    const seeded = recordCalls(() => logResponse())
    render(<AuditView ctx={prefilteredCtx({ invoiceId: PREFILTER_ID, invoiceNumber: PREFILTER_NUMBER })} />)
    await waitFor(() => expect(seeded.some(isMainUrl)).toBe(true))
    expect(mainParams(seeded).get('invoice_id'), 'vacuity floor: the first mount must consume the atom').toBe(PREFILTER_ID)
    cleanup()

    // Workspace has cleared the atom by now, so the NEXT mount sees null and must be an
    // ordinary landing: no invoice, and the 30-day window restored.
    const later = recordCalls(() => logResponse())
    render(<AuditView ctx={prefilteredCtx(null)} />)
    await waitFor(() => expect(later.some(isMainUrl)).toBe(true))

    const params = mainParams(later)
    expect(params.has('invoice_id'), 'a later mount must not re-consume the prefilter').toBe(false)
    expect(params.has('from'), 'a later mount must restore the 30-day window').toBe(true)
    expect(screen.queryByTestId('audit-pill-invoice'), 'and must show no invoice pill').toBeNull()
    expect(screen.getByTestId('audit-pill-range'), 'control: the 30-day pill is back').toBeTruthy()
  })

  // THE HAZARD. filterState lives above every rung, so a refetch cannot destroy it -- provided
  // the seed never runs again. Moving the seed into useEffect([ctx.auditPrefilter]) reds this
  // immediately: the effect fires the instant Workspace clears the atom, and the user goes from
  // the invoice's events to the whole workspace log with nothing on screen saying so.
  it('auditView_aSeededFilterSurvivesARefetchAndTheClear', async () => {
    const CURSOR = 'seeded-page-2'
    const calls = recordCalls((url) =>
      url.includes('cursor=')
        ? logResponse({ page: { limit: 25, has_more: false, next_cursor: null }, total: 2 })
        : logResponse({ page: { limit: 25, has_more: true, next_cursor: CURSOR }, total: 2 }),
    )
    const { rerender } = render(
      <AuditView ctx={prefilteredCtx({ invoiceId: PREFILTER_ID, invoiceNumber: PREFILTER_NUMBER })} />,
    )
    await waitFor(() => expect(screen.getByTestId('audit-pager-next')).toHaveProperty('disabled', false))
    expect(screen.queryByTestId('audit-pill-invoice'), 'vacuity floor: the seed must have taken').not.toBeNull()

    // Workspace clears the atom in the effect phase of the very commit that mounted this
    // component, which arrives here as a re-render with a null atom.
    const before = calls.length
    rerender(<AuditView ctx={prefilteredCtx(null)} />)
    await waitFor(() => expect(screen.getByTestId('audit-pager-next')).toHaveProperty('disabled', false))
    expect(screen.queryByTestId('audit-pill-invoice'), 'the clear must not drop the pill').not.toBeNull()
    expect(calls.length, 'the clear must not trigger a refetch').toBe(before)

    fireEvent.click(screen.getByTestId('audit-pager-next'))
    await waitFor(() => expect(calls.some((u) => u.includes('cursor='))).toBe(true))

    const paged = new URL(calls.find((u) => u.includes('cursor='))!).searchParams
    expect(paged.get('invoice_id'), 'the filter must survive the refetch').toBe(PREFILTER_ID)
    expect(paged.has('from'), 'and must still apply no date window').toBe(false)
    expect(screen.queryByTestId('audit-pill-invoice'), 'and the pill must still be on screen').not.toBeNull()
  })

  // AC-6. "Renders identically" is false and unmeetable -- a prefiltered mount MUST differ in
  // the pills row, that is the feature. So: the structural surface is equal, and the difference
  // is CLOSED at exactly four ids.
  it('auditView_prefilterChangesNothingElse', async () => {
    // Panel TRIGGERS, never panel contents: FilterPopover mounts its panel only when open, so
    // audit-search-input and friends are absent in both renders by default.
    const STRUCTURAL = [
      'audit-subtitle',
      'audit-immutability-strip',
      'audit-bundle-open',
      'audit-export',
      'audit-filter-card',
      'audit-search-trigger',
      'audit-date-trigger',
      'audit-event-trigger',
      'audit-actor-trigger',
      'audit-company-trigger',
      'audit-table',
      'audit-table-head',
      'audit-row',
      'audit-expansion',
      'audit-pager',
      'audit-page-size',
      'audit-pager-prev',
      'audit-pager-next',
    ]

    const testIdsOnScreen = () =>
      new Set(Array.from(document.querySelectorAll('[data-testid]')).map((el) => el.getAttribute('data-testid')!))

    // A row is EXPANDED in both renders: audit-invoice-affordance lives inside the expansion
    // and is one of the four legitimate differences. Without expanding it is absent in both,
    // and the closed set would silently under-report.
    mockFetchSequence([logResponse()])
    render(<AuditView ctx={prefilteredCtx(null)} />)
    await waitFor(() => expect(screen.getAllByTestId('audit-row').length).toBe(1))
    fireEvent.click(screen.getAllByTestId('audit-row')[0]!)
    const plain = testIdsOnScreen()
    // Vacuity floor: two empty sets compare equal, and a mistyped id would drop out of both.
    for (const id of STRUCTURAL) {
      expect(plain.has(id), `${id} is not on the plain render -- the enumeration is stale`).toBe(true)
    }
    cleanup()

    mockFetchSequence([logResponse()])
    render(<AuditView ctx={prefilteredCtx({ invoiceId: PREFILTER_ID, invoiceNumber: PREFILTER_NUMBER })} />)
    await waitFor(() => expect(screen.getAllByTestId('audit-row').length).toBe(1))
    fireEvent.click(screen.getAllByTestId('audit-row')[0]!)
    const seeded = testIdsOnScreen()

    for (const id of STRUCTURAL) {
      expect(seeded.has(id), `${id} disappeared on a prefiltered mount`).toBe(true)
    }

    const lost = [...plain].filter((id) => !seeded.has(id)).sort()
    const gained = [...seeded].filter((id) => !plain.has(id)).sort()
    // Closed, not "and maybe others": the range pill goes because the seed applies no window,
    // the row affordance goes because `filtered` is true from the first render, and the invoice
    // pill plus Clear all arrive because the seeded state is not the default.
    expect(lost, 'a prefiltered mount lost something other than the range pill and the row affordance').toEqual([
      'audit-invoice-affordance',
      'audit-pill-range',
    ])
    expect(gained, 'a prefiltered mount gained something other than the invoice pill and Clear all').toEqual([
      'audit-clear-all',
      'audit-pill-invoice',
    ])
  })

  it('auditView_theSeedSurvivesStrictModesDoubleMount', async () => {
    const pre: AuditPrefilter = { invoiceId: PREFILTER_ID, invoiceNumber: PREFILTER_NUMBER }
    const frozen = JSON.stringify(pre)
    const ctx = prefilteredCtx(pre)
    const calls = recordCalls(() => logResponse())

    render(
      <StrictMode>
        <AuditView ctx={ctx} />
      </StrictMode>,
    )
    await waitFor(() => expect(screen.getByTestId('audit-filter-card')).toBeTruthy())
    expect(screen.queryByTestId('audit-pill-invoice'), 'vacuity floor: the seed must have taken').not.toBeNull()

    // Control needle: StrictMode runs a mount effect setup -> cleanup -> setup, so useAsync
    // fires twice. Without two main requests this spec is a single mount in a wrapper.
    const main = calls.filter(isMainUrl)
    expect(main.length, 'StrictMode did not double the mount -- this spec proves nothing').toBeGreaterThanOrEqual(2)

    // EVERY main request, not just the first: a consume-once atom read twice must produce the
    // same seed both times.
    for (const url of main) {
      const p = new URL(url).searchParams
      expect(p.get('invoice_id'), `a main request lost the seed: ${url}`).toBe(PREFILTER_ID)
      expect(p.has('from'), `a main request regained the 30-day window: ${url}`).toBe(false)
    }

    // The atom belongs to Workspace. Consuming it by mutating the object handed in would work
    // on the first StrictMode pass and fail on the second.
    expect(
      JSON.stringify((ctx as unknown as { auditPrefilter: AuditPrefilter | null }).auditPrefilter),
      'AuditView must not mutate the atom it reads',
    ).toBe(frozen)
  })

  it('auditView_seedsTheFilterInARenderNeverInAnEffect', () => {
    const raw = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'AuditView.tsx'), 'utf8')
    const src = stripComments(raw)
    expect(src, 'the scan read the wrong file').toContain('export function AuditView')
    // Paired control needles: the stripper must remove a comment that is really there.
    expect(raw, 'control: the reference comment must exist to be stripped').toContain('The one DOM step in the export')
    expect(src, 'control: the stripper removed nothing').not.toContain('The one DOM step in the export')

    const hits = src.split('ctx.auditPrefilter').length - 1
    expect(hits, 'the atom must be read exactly once -- a second read is an effect keyed on it').toBe(1)
    const line = src.split('\n').find((l) => l.includes('ctx.auditPrefilter'))
    expect(line, 'the one read must be the lazy useState initializer, never an effect').toContain(
      'useState<AuditFilterState>(',
    )
  })
})
