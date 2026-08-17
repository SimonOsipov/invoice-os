// The only place this repo writes `document.cookie`, and it writes nothing but
// expiries: Reject deletes Google's cookies, it never stores one of ours.
// `document` resolves at call time so the module imports inert under node.

const GA_COOKIE_NAME = /^_ga(_[A-Za-z0-9]+)?$/

const EXPIRED = '=; Max-Age=0; path=/; expires=Thu, 01 Jan 1970 00:00:00 GMT'

/** `_ga` and `_ga_<container>` only — `_gat` and `_gid` are Google's too but not ours to clear. */
export function isGaCookieName(name: string): boolean {
  return GA_COOKIE_NAME.test(name)
}

/** Every GA-owned name present in a raw `document.cookie` string. */
export function gaCookieNames(raw: string): string[] {
  return raw
    .split(';')
    .map((pair) => pair.split('=')[0].trim())
    .filter(isGaCookieName)
}

/** The host and each parent down to two labels, each with and without a leading dot.
 *  Two labels because a public-suffix list is a new dependency: on `foo.co.uk` this
 *  over-generates `co.uk`, which every browser discards as a public suffix, while the
 *  two useful variants still run. Pinned by "the over-generation on a public suffix is
 *  deliberate and harmless". */
export function cookieDomainVariants(hostname: string): string[] {
  const labels = hostname.split('.')
  if (labels.length < 2) return [hostname]

  const out: string[] = []
  for (let i = 0; i <= labels.length - 2; i += 1) {
    const domain = labels.slice(i).join('.')
    out.push(domain, `.${domain}`)
  }
  return out
}

/** Expires every GA cookie across every domain variant plus the host-only form, and
 *  returns the names it targeted. A `domain=` the browser rejects is a silent no-op,
 *  which is why enumerating variants is safe and why the oracle re-reads the jar. */
export function clearGaCookies(hostname?: string, doc: Pick<Document, 'cookie'> = document): string[] {
  // A real jar can hold one name twice — host-only and on the registrable domain.
  const names = [...new Set(gaCookieNames(doc.cookie))]
  if (names.length === 0) return []

  const domains = cookieDomainVariants(hostname ?? window.location.hostname)
  for (const name of names) {
    doc.cookie = `${name}${EXPIRED}`
    for (const domain of domains) {
      doc.cookie = `${name}${EXPIRED}; domain=${domain}`
    }
  }
  return names
}
