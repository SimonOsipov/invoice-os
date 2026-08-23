// Audit-reader wire types, mirrored key-for-key from internal/audit/reader.go. The mirror is
// hand-maintained and no compiler links the two sides -- audit.test.ts's tag scan is what
// catches drift. e2e/api/client.ts carries the same shape for the API suite; both must agree.
//
// `payload` is `unknown`, not a named shape: it is jsonb whose keys differ per event, and
// typing it would be a lie the mirror could not catch.

import type { AuthedFetch } from './portfolio'

export type AuditCompanyScope = 'company' | 'workspace' | 'unattributed'

export interface AuditEvent {
  id: string
  created_at: string
  event: string
  actor: string
  actor_name: string
  actor_kind: string
  entity_id: string | null
  company_name: string | null
  company_scope: AuditCompanyScope
  payload: unknown
}

export interface AuditPageInfo {
  limit: number
  has_more: boolean
  next_cursor: string | null
}

// kind is `json:"kind,omitempty"` on the Go side -- absent on event and company facets,
// present only on actor facets, hence optional here unlike every other key.
export interface AuditFacet {
  value: string | null
  name: string | null
  kind?: string
  count: number
}

export interface AuditFacets {
  event: AuditFacet[]
  actor: AuditFacet[]
  company: AuditFacet[]
}

export interface AuditResponse {
  events: AuditEvent[]
  page: AuditPageInfo
  total: number
  // Distinguishes a genuinely empty log from one the filters emptied. The server computes it
  // only when total===0, via an unfiltered probe (internal/audit/store.go). The screen reads
  // this flag and never infers the distinction.
  log_is_empty: boolean
  facets: AuditFacets
}

export interface AuditLogQuery {
  limit?: number
  cursor?: string
  from?: string
  to?: string
  event?: string[]
  actor?: string[]
  actor_kind?: 'people' | 'system'
  company?: string
  q?: string
  invoice_id?: string
}

// The store coerces nil slices to [], but the Go struct carries no omitempty guarantee, so a
// bare Response{} still marshals them as null. Tolerate both.
export function normaliseAuditResponse(res: AuditResponse): AuditResponse {
  return {
    ...res,
    events: res.events ?? [],
    facets: {
      event: res.facets?.event ?? [],
      actor: res.facets?.actor ?? [],
      company: res.facets?.company ?? [],
    },
  }
}

// GET /v1/audit-log on the invoice binary. `!== undefined` rather than truthiness, so q=''
// stays sendable and is not silently dropped.
export async function getAuditLog(
  authedFetch: AuthedFetch,
  base: string,
  query: AuditLogQuery = {},
): Promise<AuditResponse> {
  const params = new URLSearchParams()
  if (query.limit !== undefined) params.set('limit', String(query.limit))
  if (query.cursor !== undefined) params.set('cursor', query.cursor)
  if (query.from !== undefined) params.set('from', query.from)
  if (query.to !== undefined) params.set('to', query.to)
  for (const e of query.event ?? []) params.append('event', e)
  for (const a of query.actor ?? []) params.append('actor', a)
  if (query.actor_kind !== undefined) params.set('actor_kind', query.actor_kind)
  if (query.company !== undefined) params.set('company', query.company)
  if (query.q !== undefined) params.set('q', query.q)
  if (query.invoice_id !== undefined) params.set('invoice_id', query.invoice_id)
  const qs = params.toString() ? `?${params.toString()}` : ''
  const res = await authedFetch<AuditResponse>(`${base}/api/invoice/v1/audit-log${qs}`)
  return normaliseAuditResponse(res)
}
