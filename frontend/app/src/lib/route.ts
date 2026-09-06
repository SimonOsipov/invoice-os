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

export function routePath(view: View): string {
  return ROUTE_PATHS[view]
}

// Strict: exact match, case-sensitive, at most one trailing slash. Anything else — an
// unknown path, a drill-down like `/invoices/<uuid>`, wrong case — returns null rather
// than degrading to a nearby view.
export function parseRoute(pathname: string): View | null {
  const normalized = pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname
  return PATH_TO_VIEW.get(normalized) ?? null
}
