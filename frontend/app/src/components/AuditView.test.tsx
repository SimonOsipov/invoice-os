// @vitest-environment jsdom
// AUDIT-06-07's RED specs. The `mockFetchSequence` / narrowed-ctx idiom is
// ApprovalsView.test.tsx's, unchanged.

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
