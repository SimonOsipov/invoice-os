import type { ViewId } from './types.ts'

// Copy of ROUTE_PATHS in frontend/app/src/lib/route.ts; appPaths_matchesRoutePathsInTheApp pins it.
export const APP_PATHS: Record<ViewId, string> = {
  create: '/create',
  invoices: '/invoices',
  rules: '/rules',
  approvals: '/approvals',
  workflows: '/workflows',
  dashboard: '/',
  reports: '/reports',
  audit: '/audit',
  clients: '/clients',
  customers: '/customers',
  settings: '/settings',
}
