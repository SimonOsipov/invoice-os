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

export type DemoCtaSource = 'nav' | 'hero' | 'audience' | 'pricing' | 'demo_cta' | 'footer'
export const DEMO_CTA_SOURCES: readonly DemoCtaSource[] =
  ['nav', 'hero', 'audience', 'pricing', 'demo_cta', 'footer']

const FORM_NAME = 'book_a_demo'

// The one choke point for every sender: no tag, no send. `window` is read only
// after the flag check, so a sender is inert under node and on every non-production
// host. Pinned by "a sender before ensureTag touches no browser global".
function send(name: string, params: Record<string, string | number>): void {
  if (!loaded) return
  ;(window as GtagWindow).gtag?.('event', name, params)
}

export function trackDemoOpen(source: DemoCtaSource): void {
  send('demo_open', { cta_location: source })
}

function trackDemoSubmitOk(): void {
  send('generate_lead', { form_name: FORM_NAME })
}

function trackDemoSubmitFailed(): void {
  send('demo_submit_failed', { form_name: FORM_NAME })
}

// Wraps the one call that reaches HubSpot rather than DemoModal's shared success
// transition, which the honeypot and closed-gate branches also reach. Both senders
// stay module-private so no other call site can attach them elsewhere.
export async function trackedHubSpotSubmit(run: () => Promise<void>): Promise<void> {
  try {
    await run()
  } catch (err) {
    trackDemoSubmitFailed()
    throw err
  }
  trackDemoSubmitOk()
}

const SCROLL_MILESTONES = [25, 50, 75, 100] as const

// Module scope, not the effect closure or a ref: a ref survives StrictMode's double
// effect but not a remount of App; module scope survives both. Pinned by
// "each milestone fires at most once per page load".
const firedMilestones = new Set<number>()

/** Pure and total. The finite check runs FIRST: after the `scrollable <= 0` branch a
 *  broken viewport read would return 100, a fully-seen page that was never seen.
 *  Pinned by "a non-finite viewport does not masquerade as a fully-seen page". */
export function scrollDepthPercent(scrollY: number, viewportH: number, documentH: number): number {
  if (!Number.isFinite(scrollY) || !Number.isFinite(viewportH) || !Number.isFinite(documentH)) return 0
  const scrollable = documentH - viewportH
  if (scrollable <= 0) return 100
  return Math.min(100, Math.max(0, Math.round((scrollY / scrollable) * 100)))
}

/** Fires every uncrossed milestone at or below `percent`, ascending, once each per page load. */
export function trackScrollDepth(percent: number): void {
  for (const m of SCROLL_MILESTONES) {
    if (percent < m || firedMilestones.has(m)) continue
    firedMilestones.add(m)
    send('scroll_depth', { percent_scrolled: m })
  }
}
