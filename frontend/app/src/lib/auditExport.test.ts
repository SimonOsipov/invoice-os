import { describe, expect, it, vi } from 'vitest'

import type { AuditEvent, AuditLogQuery, AuditResponse } from './audit'
import type { AuditExportResult } from './auditExport'
import { collectExportRows } from './auditExport'

let eventCounter = 0
function makeEvent(overrides: Partial<AuditEvent> = {}): AuditEvent {
  eventCounter += 1
  return {
    id: `evt-${eventCounter}`,
    created_at: '2026-08-20T09:15:00Z',
    event: 'invoice.created',
    actor: 'actor-uuid-0001',
    actor_name: 'Ada Obi',
    actor_kind: 'person',
    entity_id: null,
    company_name: null,
    company_scope: 'unattributed',
    payload: null,
    ...overrides,
  }
}

function makeEvents(count: number): AuditEvent[] {
  return Array.from({ length: count }, () => makeEvent())
}

interface PageSpec {
  events: AuditEvent[]
  total: number
  limit?: number
  has_more: boolean
  next_cursor: string | null
}

function makePage(spec: PageSpec): AuditResponse {
  return {
    events: spec.events,
    total: spec.total,
    log_is_empty: false,
    facets: { event: [], actor: [], company: [] },
    page: {
      limit: spec.limit ?? 100,
      has_more: spec.has_more,
      next_cursor: spec.next_cursor,
    },
  }
}

describe('collectExportRows', () => {
  it('auditExport_loopWalksEveryPage: 100/100/37 pages collapse into 237 rows over 3 calls, each carrying the prior cursor', async () => {
    const pages = [
      makePage({ events: makeEvents(100), total: 237, has_more: true, next_cursor: 'cursor-1' }),
      makePage({ events: makeEvents(100), total: 237, has_more: true, next_cursor: 'cursor-2' }),
      makePage({ events: makeEvents(37), total: 237, has_more: false, next_cursor: null }),
    ]
    const fetchPage = vi.fn(async (_query: AuditLogQuery) => pages[fetchPage.mock.calls.length - 1])

    const result = await collectExportRows(fetchPage, {}, 1000)

    expect(result.rows.length, 'expected all three pages worth of rows (237) to be collected').toBe(237)
    expect(fetchPage.mock.calls.length, 'expected exactly one fetchPage call per page (3)').toBe(3)
    expect(fetchPage.mock.calls[1][0].cursor, 'the second call must carry page 1\'s next_cursor').toBe('cursor-1')
    expect(fetchPage.mock.calls[2][0].cursor, 'the third call must carry page 2\'s next_cursor').toBe('cursor-2')
  })

  it('auditExport_stopsAtTheCap: an endless has_more:true stream stops exactly at the cap and reports truncated', async () => {
    let n = 0
    const fetchPage = vi.fn(async () => {
      n += 1
      return makePage({ events: makeEvents(100), total: 100_000, has_more: true, next_cursor: `cursor-${n}` })
    })

    const result = await collectExportRows(fetchPage, {}, 300)

    expect(result.rows.length, 'the loop must stop at exactly the cap, not one page over or under').toBe(300)
    expect(fetchPage.mock.calls.length, 'call count must equal cap/limit (300/100 = 3)').toBe(3)
    expect(result.truncated, 'stopping at the cap while has_more was still true must report truncated').toBe(true)
  })

  it('auditExport_reportsRowsNotTotal: the reported count is rows actually collected, never either total value', async () => {
    const pages = [
      makePage({ events: makeEvents(100), total: 500, has_more: true, next_cursor: 'cursor-1' }),
      makePage({ events: makeEvents(100), total: 700, has_more: true, next_cursor: 'cursor-2' }),
      makePage({ events: makeEvents(50), total: 900, has_more: false, next_cursor: null }),
    ]
    const fetchPage = vi.fn(async () => pages[fetchPage.mock.calls.length - 1])

    const result = await collectExportRows(fetchPage, {}, 1000)

    expect(result.rows.length, 'expected the 250 rows actually returned across the three pages').toBe(250)
    expect(result.rows.length, 'the reported count must never equal the first page\'s total (500)').not.toBe(500)
    expect(result.rows.length, 'the reported count must never equal the last page\'s total (900)').not.toBe(900)
  })

  it('auditExport_holdsItsStartingQuery: mutating the caller\'s query object mid-loop must not change any in-flight request', async () => {
    const query: AuditLogQuery = { q: 'alpha', actor_kind: 'people' }
    const pages = [
      makePage({ events: makeEvents(10), total: 20, has_more: true, next_cursor: 'cursor-1' }),
      makePage({ events: makeEvents(10), total: 20, has_more: false, next_cursor: null }),
    ]
    const fetchPage = vi.fn(async (_query: AuditLogQuery) => {
      const page = pages[fetchPage.mock.calls.length - 1]
      // simulate the caller (e.g. a filter panel) changing state while request 1 is in flight
      query.q = 'mutated-mid-loop'
      return page
    })

    await collectExportRows(fetchPage, query, 1000)

    expect(fetchPage.mock.calls.length, 'expected both pages to have been requested').toBe(2)
    for (const [i, call] of fetchPage.mock.calls.entries()) {
      expect(call[0].q, `request ${i + 1} must carry the original q ("alpha"), not the mid-loop mutation`).toBe('alpha')
    }
  })

  it('auditExport_abortsOnAnError: a rejected page throws, names the request index, and yields no partial result', async () => {
    const page1 = makePage({ events: makeEvents(100), total: 500, has_more: true, next_cursor: 'cursor-1' })
    const fetchPage = vi.fn(async () => {
      if (fetchPage.mock.calls.length === 1) return page1
      throw new Error('network error')
    })

    let result: AuditExportResult | undefined
    let error: unknown
    try {
      result = await collectExportRows(fetchPage, {}, 1000)
    } catch (e) {
      error = e
    }

    expect(result, 'collectExportRows must not resolve with partial rows when a page request fails').toBeUndefined()
    expect(error, 'collectExportRows must reject when a page request fails').toBeInstanceOf(Error)
    expect((error as Error).message, 'the rejection must name the failing request index (2)').toMatch(/\b2\b/)
  })

  it('auditExport_throwsOnAShrunkPageLimit: an echoed limit other than 100 throws; the same stub echoing 100 is a valid positive floor', async () => {
    const validFetchPage = vi.fn(async () =>
      makePage({ events: makeEvents(10), total: 10, limit: 100, has_more: false, next_cursor: null }),
    )

    const floor = await collectExportRows(validFetchPage, {}, 1000)

    expect(floor.rows.length, 'positive floor: a stub that correctly echoes limit 100 must return rows').toBe(10)

    const shrunkFetchPage = vi.fn(async () =>
      makePage({ events: makeEvents(10), total: 10, limit: 25, has_more: false, next_cursor: null }),
    )

    await expect(
      collectExportRows(shrunkFetchPage, {}, 1000),
      'a page echoing limit 25 instead of the requested 100 must throw',
    ).rejects.toThrow()
  })

  it('auditExport_throwsOnARepeatedCursor: the same next_cursor returned twice throws before the cap, discarding rows collected so far', async () => {
    const fetchPage = vi.fn(async () =>
      makePage({ events: makeEvents(100), total: 100_000, has_more: true, next_cursor: 'stalled-cursor' }),
    )

    let result: AuditExportResult | undefined
    let error: unknown
    try {
      result = await collectExportRows(fetchPage, {}, 100_000)
    } catch (e) {
      error = e
    }

    expect(
      fetchPage.mock.calls.length,
      'expected the repeat to surface on the second sighting of the cursor (2 calls), not before',
    ).toBe(2)
    expect(result, 'a repeated cursor must not resolve -- collected rows must be discarded, not deduplicated').toBeUndefined()
    expect(error, 'a repeated cursor must reject the loop').toBeInstanceOf(Error)
    expect(
      fetchPage.mock.calls.length,
      'the stall must be caught on the repeat, well short of the 1000-call cap',
    ).toBeLessThan(1000)
  })

  it('auditExport_throwsOnHasMoreWithNullCursor: has_more:true with a null next_cursor throws', async () => {
    const page1 = makePage({ events: makeEvents(100), total: 200, has_more: true, next_cursor: 'cursor-1' })
    const page2 = makePage({ events: makeEvents(100), total: 200, has_more: true, next_cursor: null })
    const fetchPage = vi.fn(async () => (fetchPage.mock.calls.length === 1 ? page1 : page2))

    let result: AuditExportResult | undefined
    let error: unknown
    try {
      result = await collectExportRows(fetchPage, {}, 1000)
    } catch (e) {
      error = e
    }

    expect(fetchPage.mock.calls.length, 'expected page 1 to be fetched and page 2 to trip the check (2 calls)').toBe(2)
    expect(result, 'has_more:true with a null next_cursor must not resolve with partial rows').toBeUndefined()
    expect(error, 'has_more:true paired with next_cursor:null is an unfollowable page and must reject').toBeInstanceOf(Error)
  })

  it('auditExport_throwsOnAnEmptyPageThatClaimsMore: a zero-event page with has_more:true throws', async () => {
    const page1 = makePage({ events: makeEvents(100), total: 100, has_more: true, next_cursor: 'cursor-1' })
    const page2 = makePage({ events: [], total: 100, has_more: true, next_cursor: 'cursor-2' })
    const fetchPage = vi.fn(async () => (fetchPage.mock.calls.length === 1 ? page1 : page2))

    let result: AuditExportResult | undefined
    let error: unknown
    try {
      result = await collectExportRows(fetchPage, {}, 1000)
    } catch (e) {
      error = e
    }

    expect(fetchPage.mock.calls.length, 'expected page 1 to be fetched and the empty page 2 to trip the check (2 calls)').toBe(2)
    expect(result, 'an empty page claiming has_more must not resolve with partial rows').toBeUndefined()
    expect(error, 'a page with zero events but has_more:true is an inconsistent cursor and must reject').toBeInstanceOf(Error)
  })
})
