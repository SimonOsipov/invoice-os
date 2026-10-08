import type { ErrorEvent, Integration, StartSpanOptions } from '@sentry/core'
import { browserTracingIntegration, getClient, startBrowserTracingNavigationSpan } from '@sentry/react'
import type { BrowserOptions } from '@sentry/react'
import { dropEvent, keepBreadcrumb, scrubApiError, scrubEvent, scrubSpan, scrubTransaction } from './scrub'

export type Service = 'app' | 'ops-console' | 'support-console' | 'landing' | 'library'

export interface MonitoringConfig {
  service: Service
  dsn: string | undefined
  release: string
  gateway?: string | null
  routeName?: (pathname: string) => string
}

// The SDK puts the target path in o.name for page loads and navigations; names never carry ids or queries.
export function nameRouteSpan(routeName?: (p: string) => string): (o: StartSpanOptions) => StartSpanOptions {
  return (o) => ({ ...o, name: routeName ? routeName(o.name) : '<unmatched>' })
}

const escapeRegExp = (s: string): string => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

// The SDK matches targets against new URL(...).toString(), so compare the normalised origin.
function gatewayOrigin(gateway?: string | null): string {
  try {
    const o = new URL((gateway ?? '').trim()).origin
    return o === 'null' ? '' : o
  } catch {
    return ''
  }
}

// The SDK sets the scope's transaction name to the raw path; error events get the route pattern too.
function nameErrorTransaction(e: ErrorEvent, routeName?: (p: string) => string): ErrorEvent {
  return routeName && typeof e.transaction === 'string' ? { ...e, transaction: routeName(e.transaction) } : e
}

// The SDK's own navigation handler also fires on replaceState, which the app uses for canonicalisation and query edits.
// Only pushState and popstate are navigations here; the handler is installed once and finds the live client per call.
let navigationsWired = false

function startNavigation(url: string): void {
  const client = getClient()
  if (!client?.getIntegrationByName('BrowserTracing')) return
  startBrowserTracingNavigationSpan(
    client,
    { name: new URL(url, window.location.href).pathname, attributes: { 'sentry.source': 'url', 'sentry.origin': 'auto.navigation.browser' } },
    { url },
  )
}

function wireNavigations(): void {
  if (navigationsWired) return
  navigationsWired = true
  let last = window.location.href
  const push = window.history.pushState
  window.history.pushState = function (...args: Parameters<History['pushState']>) {
    const out = push.apply(this, args)
    const to = window.location.href
    if (to !== last) startNavigation(to)
    last = to
    return out
  }
  window.history.replaceState = new Proxy(window.history.replaceState, {
    apply(target, self, args: Parameters<History['replaceState']>) {
      const out = Reflect.apply(target, self, args)
      last = window.location.href
      return out
    },
  })
  window.addEventListener('popstate', () => {
    const to = window.location.href
    if (to !== last) startNavigation(to)
    last = to
  })
}

// ceiling: a pushState within ~1.5 s of page load is not demoted to a redirect child as the SDK would; revisit if the app gains a boot-time pushState
function appTracing(routeName?: (p: string) => string): Integration {
  const bt = browserTracingIntegration({ beforeStartSpan: nameRouteSpan(routeName), instrumentNavigation: false })
  return { ...bt, setup: (client) => (bt.setup?.(client), wireNavigations()) }
}

// ceiling: no INP or navigation spans, so only page loads are measured; revisit if the landing page gains client-side routing
function pageLoadTracing(routeName?: (p: string) => string): Integration {
  return browserTracingIntegration({ beforeStartSpan: nameRouteSpan(routeName), instrumentNavigation: false, enableInp: false })
}

export function sentryOptions(c: MonitoringConfig): BrowserOptions | null {
  const dsn = (c.dsn ?? '').trim()
  if (dsn === '') return null
  const app = c.service === 'app'
  const landing = c.service === 'landing'
  const tracing = app ? [appTracing(c.routeName)] : landing ? [pageLoadTracing(c.routeName)] : []
  const origin = gatewayOrigin(c.gateway)
  return {
    dsn,
    environment: 'production',
    release: c.release,
    initialScope: { tags: { service: c.service } },
    dataCollection: { userInfo: false, cookies: false, httpHeaders: false, httpBodies: [], urlQueryParams: false },
    enhanceFetchErrorMessages: false,
    integrations: (defaults) => [...defaults.filter((i) => i.name !== 'BrowserSession' && i.name !== 'CultureContext'), ...tracing],
    // ceiling: no allowUrls/denyUrls, so third-party gtag.js errors (loaded once analytics is allowed) spend quota; add denyUrls for googletagmanager.com if they show in asc-frontend
    beforeSend: (e, h) => (dropEvent(e, h) ? null : nameErrorTransaction(scrubEvent(scrubApiError(e, h)), c.routeName)),
    beforeSendTransaction: scrubTransaction,
    beforeSendSpan: scrubSpan,
    beforeBreadcrumb: keepBreadcrumb,
    // ceiling: landing samples every page load (1.0), crawlers included; lower it if transaction quota passes a threshold, and reword the privacy sentence "Each page you open" (Privacy.claims.test.tsx)
    ...(app || landing ? { tracesSampleRate: 1 } : {}),
    tracePropagationTargets: app && origin !== '' ? [new RegExp('^' + escapeRegExp(origin) + '/')] : [],
  }
}
