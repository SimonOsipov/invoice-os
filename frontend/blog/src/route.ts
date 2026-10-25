import { BLOG_BASE } from './analytics'

export type Route =
  | { view: 'home' }
  | { view: 'subscribe' }
  | { view: 'confirm' }
  | { view: 'category'; id: string }
  | { view: 'post'; slug: string }
  | { view: 'missing'; path: string }

const HOME: Route = { view: 'home' }

// Segments are not decoded or checked against data; the page that renders them resolves them.
// A path outside the base reads as base-relative, so the old hosts keep serving until the old-host 301 is live.
const stripBase = (p: string): string => (p === BLOG_BASE || p.startsWith(`${BLOG_BASE}/`) ? p.slice(BLOG_BASE.length) : p)

export function parseBlogPath(raw: string): Route {
  const pathname = stripBase(raw)
  const cat = /^\/category\/([^/]+)\/?$/.exec(pathname)
  if (cat) return { view: 'category', id: cat[1] }
  if (/^\/subscribe\/confirm\/?$/.test(pathname)) return { view: 'confirm' }
  const one = /^\/([^/]+)\/?$/.exec(pathname)
  if (!one) return pathname === '' || pathname === '/' ? HOME : { view: 'missing', path: pathname }
  if (one[1] === 'subscribe') return { view: 'subscribe' }
  if (one[1] === 'category') return { view: 'missing', path: pathname }
  return { view: 'post', slug: one[1] }
}

export function blogPath(route: Route): string {
  switch (route.view) {
    case 'home':
      return `${BLOG_BASE}/`
    case 'subscribe':
      return `${BLOG_BASE}/subscribe`
    case 'confirm':
      return `${BLOG_BASE}/subscribe/confirm`
    case 'category':
      return `${BLOG_BASE}/category/${route.id}`
    case 'post':
      return `${BLOG_BASE}/${route.slug}`
    case 'missing':
      return `${BLOG_BASE}${route.path}`
  }
}
