import type { AuditEvent, AuditLogQuery, AuditResponse } from './audit'

export type AuditExportFetchPage = (query: AuditLogQuery) => Promise<AuditResponse>

export interface AuditExportResult {
  rows: AuditEvent[]
  truncated: boolean
}

// Walks has_more/next_cursor pages until the cap or the log ends. Any inconsistent page
// (bad limit echo, stalled cursor, unfollowable has_more, zero-event page claiming more, or
// a rejected request) throws -- a capped loop must never mask a stalled cursor as a clean stop.
export async function collectExportRows(
  fetchPage: AuditExportFetchPage,
  query: AuditLogQuery,
  cap: number,
): Promise<AuditExportResult> {
  // Snapshot now, before the caller can mutate its object mid-loop.
  const startQuery = { ...query }
  const rows: AuditEvent[] = []
  const seenCursors = new Set<string>()
  let cursor: string | undefined
  let requestIndex = 0

  while (true) {
    requestIndex += 1
    const request: AuditLogQuery = { ...startQuery, limit: 100 }
    if (cursor !== undefined) request.cursor = cursor

    let page: AuditResponse
    try {
      page = await fetchPage(request)
    } catch (err) {
      const reason = err instanceof Error ? err.message : String(err)
      throw new Error(`audit export request ${requestIndex} failed: ${reason}`)
    }

    if (page.page.limit !== 100) {
      throw new Error(`audit export request ${requestIndex} echoed limit ${page.page.limit}, expected 100`)
    }
    if (page.events.length === 0 && page.page.has_more) {
      throw new Error(`audit export request ${requestIndex} returned zero events but has_more was true`)
    }

    rows.push(...page.events)

    if (!page.page.has_more) {
      return { rows, truncated: false }
    }
    if (rows.length >= cap) {
      return { rows, truncated: true }
    }
    if (page.page.next_cursor === null) {
      throw new Error(`audit export request ${requestIndex} has has_more true but next_cursor is null`)
    }
    if (seenCursors.has(page.page.next_cursor)) {
      throw new Error(`audit export request ${requestIndex} repeated cursor ${page.page.next_cursor}`)
    }
    seenCursors.add(page.page.next_cursor)
    cursor = page.page.next_cursor
  }
}
