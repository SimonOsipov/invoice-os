import { request } from './api'

const AUDIT_PATH = '/api/invoice/v1/staff/audit-log'
const PAGE_SIZE = 50

// Wire shapes: internal/audit/staff_read.go.
// StaffRow
export interface StaffAuditRow {
  id: string
  tenant_id: string
  tenant_name: string | null
  created_at: string
  event: string
  /** The stored value, raw: a member UUID, `system`, or any other string. */
  actor: string
  actor_name: string
  actor_kind: 'person' | 'system' | 'raw'
  payload: unknown
}
// audit.PageInfo
export interface AuditPageInfo {
  limit: number
  has_more: boolean
  next_cursor: string | null
}
// StaffPage
export interface StaffAuditPage {
  events: StaffAuditRow[]
  page: AuditPageInfo
}

/** Each value is empty or set; `from` and `to` are UTC days (`YYYY-MM-DD`). */
export interface AuditFilters {
  tenant?: string
  event?: string
  actor?: string
  from?: string
  to?: string
}

export const RANGE_REASON = 'End date must be on or after the start date.'

// Inclusive UTC days, the bounds the app's evidence bundle uses.
export const dayStart = (d: string): string => `${d}T00:00:00.000Z`
export const dayEnd = (d: string): string => `${d}T23:59:59.999Z`

export const rangeInverted = (f: AuditFilters): boolean => !!f.from && !!f.to && f.from > f.to

const utcIso = (iso: string): string | null => {
  const t = Date.parse(iso)
  return Number.isNaN(t) ? null : new Date(t).toISOString()
}
export const utcDay = (iso: string): string => utcIso(iso)?.slice(0, 10) ?? iso
export const auditStamp = (iso: string): string => {
  const u = utcIso(iso)
  return u ? `${u.slice(0, 10)} ${u.slice(11, 19)} UTC` : iso
}

export const fetchAuditEvents = (filters: AuditFilters, cursor?: string, signal?: AbortSignal): Promise<StaffAuditPage> => {
  const q = new URLSearchParams({ limit: String(PAGE_SIZE) })
  if (filters.tenant) q.set('tenant', filters.tenant)
  if (filters.event) q.set('event', filters.event)
  if (filters.actor) q.set('actor', filters.actor)
  if (filters.from) q.set('from', dayStart(filters.from))
  if (filters.to) q.set('to', dayEnd(filters.to))
  if (cursor) q.set('cursor', cursor)
  return request<StaffAuditPage>(`${AUDIT_PATH}?${q}`, { signal })
}
