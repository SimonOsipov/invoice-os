import type { ErrorEvent, StartSpanOptions } from '@sentry/core'
import { browserTracingIntegration } from '@sentry/react'
import type { BrowserOptions } from '@sentry/react'
import { dropEvent, keepBreadcrumb, scrubEvent, scrubSpan, scrubTransaction } from './scrub'

export type Service = 'app' | 'ops-console' | 'support-console'

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

export function sentryOptions(c: MonitoringConfig): BrowserOptions | null {
  const dsn = (c.dsn ?? '').trim()
  if (dsn === '') return null
  const app = c.service === 'app'
  const tracing = app ? [browserTracingIntegration({ beforeStartSpan: nameRouteSpan(c.routeName) })] : []
  const origin = gatewayOrigin(c.gateway)
  return {
    dsn,
    environment: 'production',
    release: c.release,
    initialScope: { tags: { service: c.service } },
    dataCollection: { userInfo: false, cookies: false, httpHeaders: false, httpBodies: [], urlQueryParams: false },
    enhanceFetchErrorMessages: false,
    integrations: (defaults) => [...defaults.filter((i) => i.name !== 'BrowserSession'), ...tracing],
    beforeSend: (e, h) => (dropEvent(e, h) ? null : nameErrorTransaction(scrubEvent(e), c.routeName)),
    beforeSendTransaction: scrubTransaction,
    beforeSendSpan: scrubSpan,
    beforeBreadcrumb: keepBreadcrumb,
    ...(app ? { tracesSampleRate: 1 } : {}),
    tracePropagationTargets: app && origin !== '' ? [new RegExp('^' + escapeRegExp(origin) + '/')] : [],
  }
}
