// GA4 tag injection behind the production-hostname and consent gates. Nothing here
// touches the DOM at module scope: analytics.ts must import inert under `node`.
import { analyticsAllowed, readConsent, type ConsentRecord } from './consent'
import { isProductionHost } from './hubspot'

type GtagWindow = Window & {
  dataLayer?: IArguments[]
  gtag?: (...args: unknown[]) => void
}

let loaded = false

/** Null when unset or blank — hubspotTarget()'s contract. */
export function measurementId(): string | null {
  // Read inside the body, never at module scope: a module-scope read is baked at
  // import time and cannot be driven by vi.stubEnv (hubspot.ts:17-18).
  const id = (import.meta.env.VITE_GA_MEASUREMENT_ID ?? '').trim()
  return id || null
}

/** THE GATE. Production hostname AND consent AND a configured property. */
export function shouldLoadTag(hostname: string, allowed: boolean, id: string | null): boolean {
  return isProductionHost(hostname) && allowed && id !== null
}

export function tagSrc(id: string): string {
  return `https://www.googletagmanager.com/gtag/js?id=${id}`
}

/** Idempotent. Returns whether the tag is loaded AFTER the call, not whether this call injected it. */
export function ensureTag(hostname: string, record: ConsentRecord | null): boolean {
  if (loaded) return true

  const id = measurementId()
  // `!id` is the narrowing TS cannot take from shouldLoadTag's boolean return.
  if (!id || !shouldLoadTag(hostname, analyticsAllowed(record), id)) return false

  const w = window as GtagWindow
  w.dataLayer = w.dataLayer || []
  // GA4 parses an `arguments` OBJECT; a pushed array is accepted and then silently
  // ignored. Pinned by "the shim pushes an arguments object, not an array".
  w.gtag = function gtag() {
    w.dataLayer!.push(arguments)
  }

  const s = document.createElement('script')
  s.async = true
  s.src = tagSrc(id)
  document.head.appendChild(s)

  // `config` sends page_view itself and GA4 derives the traffic source from the
  // referrer and utm_*; a manual page_view would double-count.
  w.gtag('js', new Date())
  w.gtag('config', id)

  loaded = true
  return true
}

export function bootAnalytics(): boolean {
  return ensureTag(window.location.hostname, readConsent())
}
