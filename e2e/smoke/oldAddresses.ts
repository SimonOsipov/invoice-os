// The stage-B mapping of an old subdomain URL to its address on www (D-13, D-22). Pure, no I/O:
// test:unit runs it with no deploy URLs.

const WWW = 'https://www.ascomply.com'
const BLOG_BASE = '/blog' // frontend/landing/src/sitePaths.ts
const LIBRARY_BASE = '/library' // frontend/landing/src/sitePaths.ts
const BASE_OF_HOST: Record<string, string> = {
  'blog.ascomply.com': BLOG_BASE,
  'library.ascomply.com': LIBRARY_BASE,
}

export function newAddressOf(oldUrl: string): string {
  const url = new URL(oldUrl)
  const base = BASE_OF_HOST[url.hostname]
  if (base === undefined) throw new Error(`${oldUrl} is not on an old host`)
  const underBase = url.pathname === base || url.pathname.startsWith(`${base}/`)
  return `${WWW}${underBase ? '' : base}${url.pathname}${url.search}`
}

/** The group and feature paths (`/invoices`, `/invoices/import-files`) of frontend/library/src/content.ts. */
export function libraryPathsOf(contentSource: string): string[] {
  const paths: string[] = []
  let group = ''
  for (const m of contentSource.matchAll(/\{ id: '([a-z-]+)', n: '\d+'|\bF\('([a-z-]+)'/g)) {
    if (m[1] !== undefined) {
      group = m[1]
      paths.push(`/${group}`)
    } else paths.push(`/${group}/${m[2]}`)
  }
  return paths
}

/** Every old URL the production crawl asks: the fixed ones plus one per sitemap loc and library path. */
export function oldUrls(blogLocs: string[], libraryPaths: string[]): string[] {
  const blog = 'https://blog.ascomply.com'
  const library = 'https://library.ascomply.com'
  const locPaths = blogLocs.map((loc) => {
    const path = new URL(loc).pathname
    return path === BLOG_BASE || path.startsWith(`${BLOG_BASE}/`) ? path.slice(BLOG_BASE.length) || '/' : path
  })
  const underBase = blogLocs.map((loc) => new URL(loc).pathname).find((p) => p.startsWith(`${BLOG_BASE}/`) && p !== `${BLOG_BASE}/`)
  return [
    ...['/', '/robots.txt', '/sitemap.xml', '/subscribe', `/subscribe/confirm?token=${'t'.repeat(43)}`, `${BLOG_BASE}/`, ...(underBase ? [underBase] : []), ...locPaths].map((p) => blog + p),
    ...['/', `${LIBRARY_BASE}/invoices`, ...libraryPaths].map((p) => library + p),
    'http://blog.ascomply.com/',
    'http://library.ascomply.com/',
  ]
}
