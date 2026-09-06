import type { View } from '../types'

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

// Boot's one total reader: an unparseable pathname must never throw out of a useState
// initializer, so it falls back to the dashboard triple instead.
export function seedFromPath(pathname: string): { view: View; invoiceId: string | null; jobId: string | null } {
  const route = parseRoute(pathname)
  if (route === null) return { view: 'dashboard', invoiceId: null, jobId: null }
  return {
    view: route.view,
    invoiceId: route.view === 'detail' ? route.id : null,
    jobId: route.view === 'extraction' ? route.id : null,
  }
}
