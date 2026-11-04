import { LIBRARY_BASE } from './analytics.ts'
import { GROUPS } from './content.ts'
import type { Feature, Group } from './types.ts'

export type Route =
  | { view: 'home' }
  | { view: 'group'; group: Group }
  | { view: 'feature'; group: Group; feature: Feature }
  | { view: 'not-found'; path: string }

const HOME: Route = { view: 'home' }

// A path outside the base reads as base-relative, so the old hosts keep serving until the old-host 301 is live.
const stripBase = (p: string): string => (p === LIBRARY_BASE || p.startsWith(`${LIBRARY_BASE}/`) ? p.slice(LIBRARY_BASE.length) : p)

// Caddy decodes the path before matching files; a malformed escape stays literal.
const decode = (s: string): string => {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

export function parseLibraryPath(raw: string): Route {
  const notFound: Route = { view: 'not-found', path: raw }
  const pathname = stripBase(decode(raw)).replace(/\/{2,}/g, '/')
  if (pathname === '' || pathname === '/') return HOME
  const m = /^\/([^/]+)(?:\/([^/]+))?\/?$/.exec(pathname)
  if (!m) return notFound
  const group = GROUPS.find((g) => g.id === m[1])
  if (!group) return notFound
  if (m[2] === undefined) return { view: 'group', group }
  const feature = group.feats.find((f) => f.id === m[2])
  return feature ? { view: 'feature', group, feature } : notFound
}

export function libraryPath(route: Route): string {
  if (route.view === 'home') return `${LIBRARY_BASE}/`
  if (route.view === 'not-found') return route.path
  if (route.view === 'group') return `${LIBRARY_BASE}/${route.group.id}`
  return `${LIBRARY_BASE}/${route.group.id}/${route.feature.id}`
}
