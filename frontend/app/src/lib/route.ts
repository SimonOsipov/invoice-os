import type { CreateStep, SettingsTab, View } from '../types'

import { clampFilterText } from './invoices'

// `dashboard` is the bare root, not `/dashboard`: the landing hand-off and the persona
// strip (App.tsx:1618) both land on pathname `/`.
export const ROUTE_PATHS: Record<View, string> = {
  dashboard: '/',
  invoices: '/invoices',
  approvals: '/approvals',
  rules: '/rules',
  customers: '/customers',
  reports: '/reports',
  workflows: '/workflows',
  clients: '/clients',
  audit: '/audit',
  settings: '/settings',
  create: '/create',
  detail: '/invoice',
  extraction: '/extraction',
}

const PATH_TO_VIEW = new Map<string, View>(
  (Object.entries(ROUTE_PATHS) as [View, string][]).map(([view, path]) => [path, view]),
)

export interface Route {
  view: View
  id: string | null
}

// Keyed by the drill-down's own first segment, which is not always the target view's
// bare-path segment: `/invoices/<id>` -> `detail`, not the `invoices` list view.
const DRILLDOWN_SEGMENT: Record<string, View> = {
  invoices: 'detail',
  extraction: 'extraction',
}

export function routePath(view: View, id?: string | null): string {
  if (id != null && view === 'detail') return `/invoices/${encodeURIComponent(id)}`
  if (id != null && view === 'extraction') return `/extraction/${encodeURIComponent(id)}`
  return ROUTE_PATHS[view]
}

// Strict: exact match, case-sensitive, at most one trailing slash. Anything else — an
// unknown path, wrong case, a third segment — returns null rather than degrading to a
// nearby view.
export function parseRoute(pathname: string): Route | null {
  const normalized = pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname
  const singleMatch = PATH_TO_VIEW.get(normalized)
  if (singleMatch !== undefined) return { view: singleMatch, id: null }

  const segments = normalized.split('/').filter((s) => s.length > 0)
  if (segments.length !== 2) return null
  const view = DRILLDOWN_SEGMENT[segments[0]]
  if (view === undefined) return null
  try {
    return { view, id: decodeURIComponent(segments[1]) }
  } catch {
    return null // malformed percent escape, e.g. /invoices/%zz
  }
}

// Owned params: `invoices` owns `q`, `audit` owns `invoice`, `settings` owns its tab as a
// path segment, `detail`/`extraction` own `id` as a path segment. No other view owns
// anything, so nothing else is ever emitted or read.
const SETTINGS_TAB_TABLE: Record<SettingsTab, true> = {
  members: true,
  roles: true,
  connectors: true,
  api: true,
  signing: true,
  company: true,
}
// The Record keeps this exhaustive; the Set (not `in`) keeps prototype keys out.
const SETTINGS_TAB_IDS = new Set<string>(Object.keys(SETTINGS_TAB_TABLE))

const INVOICE_ID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/

const REVIEW_PATH_PREFIX = '/imports/'
const REVIEW_PATH_SUFFIX = '/review'
export const REVIEW_PATH_MAX_IDS = 5 // importRun.ts's MAX_RUN_FILES

// Anchored at both ends: a prefix+slice parser would hand back '../../etc' or a
// '<uuid>/extra' suffix as a batch id, and an id whose empty form widens the review query
// to the whole tenant. Case is accepted both ways because uuid.Parse is, server side.
const REVIEW_UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/

// Raw comma, never %2C: `,` is a legal RFC-3986 sub-delim and the ids are canonical uuids.
// No cap check here — the cap is the parser's, mirroring formatReviewHash.
export function reviewPath(ids: string[]): string {
  return `${REVIEW_PATH_PREFIX}${ids.join(',')}${REVIEW_PATH_SUFFIX}`
}

// Null — never [] — for anything that is not 1..REVIEW_PATH_MAX_IDS comma-separated
// canonical uuids. All-or-nothing: one bad segment poisons the whole list, and the cap is
// a rejection, never a truncation. Each id's own case is preserved verbatim.
export function parseReviewPath(pathname: string): string[] | null {
  const normalized = pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname
  if (!normalized.startsWith(REVIEW_PATH_PREFIX) || !normalized.endsWith(REVIEW_PATH_SUFFIX)) return null
  const inner = normalized.slice(REVIEW_PATH_PREFIX.length, normalized.length - REVIEW_PATH_SUFFIX.length)
  const segments = inner.split(',')
  if (segments.length > REVIEW_PATH_MAX_IDS) return null
  return segments.every((s) => REVIEW_UUID.test(s)) ? segments : null
}

// The URL half of reviewBatch.ts's reviewHash gate, clause for clause.
export function reviewNavIds(view: View, createStep: CreateStep, reviewBatchIds: string[]): string[] {
  return view === 'create' && createStep === 'review' && reviewBatchIds.length > 0 ? reviewBatchIds : []
}

export type RouteParams = {
  id?: string | null
  settingsTab?: SettingsTab
  q?: string
  auditInvoice?: string | null
  reviewBatchIds?: string[]
}

// TOTAL: view never null, unknown paths fall back to 'dashboard'. invoiceId/jobId are
// non-null only when view is the matching drill-down (mirrors parseRoute's Route.id).
export type ParsedLocation = {
  view: View
  invoiceId: string | null
  jobId: string | null
  settingsTab: SettingsTab
  q: string
  auditInvoice: string | null
  reviewBatchIds: string[]
}

// Path plus query, never a hash. Omit the default: the `members` tab, an empty `q` and an
// absent id all serialise to nothing. The path half delegates to routePath so the
// drill-down segment logic lives in exactly one place.
export function routeUrl(view: View, params: RouteParams = {}): string {
  const path = routePath(view, params.id)
  if (view === 'settings') {
    const tab = params.settingsTab
    return tab && tab !== 'members' ? `${path}/${tab}` : path
  }
  if (view === 'create') {
    const ids = params.reviewBatchIds
    return ids && ids.length > 0 ? reviewPath(ids) : path
  }
  if (view === 'invoices' && params.q) {
    return `${path}?${new URLSearchParams({ q: params.q }).toString()}`
  }
  if (view === 'audit' && params.auditInvoice) {
    return `${path}?${new URLSearchParams({ invoice: params.auditInvoice }).toString()}`
  }
  return path
}

// The query half of routeUrl, for a caller that needs the params without the path (the
// signed-out capture stores the two in separate fields). '' when the view owns none.
export function routeQuery(view: View, params: RouteParams = {}): string {
  const url = routeUrl(view, params)
  const i = url.indexOf('?')
  return i === -1 ? '' : url.slice(i)
}

// Total: every input yields a renderable result. `/settings/<seg>` is the only two-segment
// path parseRoute doesn't own itself; everything else delegates to parseRoute and inherits
// its strictness, with an unparseable path falling back to 'dashboard' rather than null.
export function parseLocation(pathname: string, search: string): ParsedLocation {
  const route = parseRoute(pathname)
  let view: View = route?.view ?? 'dashboard'
  const invoiceId = route?.view === 'detail' ? route.id : null
  const jobId = route?.view === 'extraction' ? route.id : null
  let settingsTab: SettingsTab = 'members'
  let reviewBatchIds: string[] = []

  if (route === null) {
    const normalized = pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname
    // Disjoint prefixes, so this branch and the /settings/ one below can never both fire.
    const ids = normalized.startsWith(REVIEW_PATH_PREFIX) ? parseReviewPath(normalized) : null
    if (ids !== null) {
      view = 'create'
      reviewBatchIds = ids
    }
    if (normalized.startsWith('/settings/')) {
      const seg = normalized.slice('/settings/'.length)
      if (seg.length > 0 && !/[/?#]/.test(seg)) {
        view = 'settings'
        settingsTab = SETTINGS_TAB_IDS.has(seg) ? (seg as SettingsTab) : 'members'
      }
    }
  }

  // Read only the param the parsed view owns, so parse is the exact inverse of routeUrl.
  const query = new URLSearchParams(search)
  const q = view === 'invoices' ? clampFilterText(query.get('q') ?? '') : ''
  const rawInvoice = view === 'audit' ? query.get('invoice') : null
  // A malformed id is dropped rather than forwarded: the audit reader 400s on it, which
  // renders an error state where the ordinary unfiltered list is correct.
  const auditInvoice = rawInvoice !== null && INVOICE_ID.test(rawInvoice) ? rawInvoice : null

  return { view, invoiceId, jobId, settingsTab, q, auditInvoice, reviewBatchIds }
}
