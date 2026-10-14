import { ApiError, gatewayBase, readBody, reportApiFailure } from '@invoice-os/api-client'
import { loadConsoleSession } from '@invoice-os/console-session'
import { request } from './api'
import { SESSION_KEY } from './auth'

const AUDIT_PATH = '/api/invoice/v1/staff/audit-log'
const CHANGES_PATH = '/api/invoice/v1/staff/staff-audit-log'
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

// StaffChange
export interface StaffChange {
  id: string
  actor: string
  event: string
  rule_set_version_id: string | null
  tenant_id: string | null
  tenant_name: string | null
  subject_id: string | null
  payload: unknown
  created_at: string
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

export const fetchStaffChanges = async (signal?: AbortSignal): Promise<StaffChange[]> => (await request<{ changes: StaffChange[] }>(CHANGES_PATH, { signal })).changes

export interface StaffChangeLink {
  tenant: string
  event: string
  day: string
  focusAt: string
}

/** The tenant log row that shares a tenant change's tenant, event and `created_at`; null for a global change. */
export const staffChangeLink = (c: StaffChange): StaffChangeLink | null =>
  c.tenant_id ? { tenant: c.tenant_id, event: c.event, day: utcDay(c.created_at), focusAt: c.created_at } : null

const staffTenantPath = (tenantId: string): string => `/api/invoice/v1/staff/tenants/${tenantId}`

// archive.StaffEntity
export interface StaffEntity {
  id: string
  name: string
  tin: string | null
}

// archive manifestEntity
export interface BundleEntity {
  id: string
  name: string
  tin: string | null
}
// archive manifestPeriod
export interface BundlePeriod {
  from: string
  to: string
  bounds: string
  basis: string
}
// archive manifestCounts
export interface BundleCounts {
  invoices: number
  line_items: number
  status_transitions: number
  submissions: number
  exchange_attempts: number
  body_files: number
}
// archive.Preview
export interface BundlePreview {
  entity: BundleEntity
  period: BundlePeriod
  filename: string
  counts: BundleCounts
  over_limit: boolean
}

/** One company and a period of UTC days (`YYYY-MM-DD`). */
export interface BundleRequest {
  entityId: string
  from: string
  to: string
}

// maxBundleInvoices (internal/archive/archive.go); the preview carries no limit.
export const BUNDLE_INVOICE_LIMIT = 10000

const DAY_MS = 24 * 60 * 60 * 1000

/** The last 30 UTC days, today included. */
export const defaultBundlePeriod = (now: Date = new Date()): { from: string; to: string } => ({
  from: new Date(now.getTime() - 30 * DAY_MS).toISOString().slice(0, 10),
  to: now.toISOString().slice(0, 10),
})

const bundleQuery = (r: BundleRequest): string => new URLSearchParams({ entity_id: r.entityId, from: dayStart(r.from), to: dayEnd(r.to) }).toString()

export const fetchStaffEntities = async (tenantId: string, signal?: AbortSignal): Promise<StaffEntity[]> =>
  (await request<{ entities: StaffEntity[] }>(`${staffTenantPath(tenantId)}/entities`, { signal })).entities

export const fetchBundlePreview = (tenantId: string, r: BundleRequest, signal?: AbortSignal): Promise<BundlePreview> =>
  request<BundlePreview>(`${staffTenantPath(tenantId)}/evidence-bundle/preview?${bundleQuery(r)}`, { signal })

// Whole-string unquoted grammar; a quoted value or a trailing parameter falls back.
const DISPOSITION_RE = /^attachment; filename=([A-Za-z0-9._-]+)$/

// Bare fetch: the body is a zip, which `request` would parse as JSON.
export const downloadBundle = async (tenantId: string, r: BundleRequest, fallbackFilename: string): Promise<{ blob: Blob; filename: string }> => {
  const base = gatewayBase()
  if (!base) throw new ApiError('network', 'Gateway URL is not configured')
  const token = loadConsoleSession(SESSION_KEY)?.token
  if (!token) throw new ApiError('http', 'Not signed in', 401)
  const url = `${base}${staffTenantPath(tenantId)}/evidence-bundle?${bundleQuery(r)}`
  let res: Response
  try {
    res = await fetch(url, { headers: { Authorization: `Bearer ${token}` } })
  } catch (e) {
    reportApiFailure(e, { method: 'GET', url })
    throw e
  }
  if (!res.ok) {
    let body: unknown
    let msg = res.statusText
    try {
      body = await res.json()
      if (body && typeof body === 'object' && 'error' in body) msg = String((body as { error: unknown }).error)
    } catch {
      // no JSON body: the status text stands
    }
    const error = new ApiError('http', msg, res.status, body)
    reportApiFailure(error, { method: 'GET', url })
    throw error
  }
  return {
    blob: await readBody(() => res.blob(), { method: 'GET', url }),
    filename: res.headers.get('Content-Disposition')?.match(DISPOSITION_RE)?.[1] ?? fallbackFilename,
  }
}
