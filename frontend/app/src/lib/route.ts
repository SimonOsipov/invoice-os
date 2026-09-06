import type { SettingsTab, View } from '../types'

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

// Owned params: `invoices` owns `q`, `audit` owns `invoice`, `settings` owns its tab as a
// path segment. No other view owns anything, so nothing else is ever emitted or read.
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

export type RouteParams = { settingsTab?: SettingsTab; q?: string; auditInvoice?: string | null }

export type ParsedLocation = {
  view: View | null
  settingsTab: SettingsTab
  q: string
  auditInvoice: string | null
}

// Path plus query, never a hash. Omit the default: the `members` tab, an empty `q` and an
// absent id all serialise to nothing.
export function routeUrl(view: View, params: RouteParams = {}): string {
  const path = ROUTE_PATHS[view]
  if (view === 'settings') {
    const tab = params.settingsTab
    return tab && tab !== 'members' ? `${path}/${tab}` : path
  }
  if (view === 'invoices' && params.q) {
    return `${path}?${new URLSearchParams({ q: params.q }).toString()}`
  }
  if (view === 'audit' && params.auditInvoice) {
    return `${path}?${new URLSearchParams({ invoice: params.auditInvoice }).toString()}`
  }
  return path
}

// Total: every input yields a renderable result. `/settings/<seg>` is the only two-segment
// path; everything else delegates to parseRoute and inherits its strictness.
export function parseLocation(pathname: string, search: string): ParsedLocation {
  let view = parseRoute(pathname)
  let settingsTab: SettingsTab = 'members'
  const normalized = pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname
  if (view === null && normalized.startsWith('/settings/')) {
    const seg = normalized.slice('/settings/'.length)
    if (seg.length > 0 && !/[/?#]/.test(seg)) {
      view = 'settings'
      settingsTab = SETTINGS_TAB_IDS.has(seg) ? (seg as SettingsTab) : 'members'
    }
  }

  // Read only the param the parsed view owns, so parse is the exact inverse of routeUrl.
  const query = new URLSearchParams(search)
  const q = view === 'invoices' ? clampFilterText(query.get('q') ?? '') : ''
  const rawInvoice = view === 'audit' ? query.get('invoice') : null
  // A malformed id is dropped rather than forwarded: the audit reader 400s on it, which
  // renders an error state where the ordinary unfiltered list is correct.
  const auditInvoice = rawInvoice !== null && INVOICE_ID.test(rawInvoice) ? rawInvoice : null

  return { view, settingsTab, q, auditInvoice }
}
