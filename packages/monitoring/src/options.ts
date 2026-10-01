import type { StartSpanOptions } from '@sentry/core'
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

export function sentryOptions(c: MonitoringConfig): BrowserOptions | null {
  const dsn = (c.dsn ?? '').trim()
  if (dsn === '') return null
  const app = c.service === 'app'
  const tracing = app ? [browserTracingIntegration({ beforeStartSpan: nameRouteSpan(c.routeName) })] : []
  const gateway = (c.gateway ?? '').replace(/\/+$/, '')
  return {
    dsn,
    environment: 'production',
    release: c.release,
    initialScope: { tags: { service: c.service } },
    dataCollection: { userInfo: false, cookies: false, httpHeaders: false, httpBodies: [], urlQueryParams: false },
    enhanceFetchErrorMessages: false,
    integrations: (defaults) => [...defaults.filter((i) => i.name !== 'BrowserSession'), ...tracing],
    beforeSend: (e, h) => (dropEvent(e, h) ? null : scrubEvent(e)),
    beforeSendTransaction: scrubTransaction,
    beforeSendSpan: scrubSpan,
    beforeBreadcrumb: keepBreadcrumb,
    ...(app ? { tracesSampleRate: 1 } : {}),
    tracePropagationTargets: app && gateway !== '' ? [new RegExp('^' + escapeRegExp(gateway) + '/')] : [],
  }
}
