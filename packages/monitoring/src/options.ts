// RED STUB — QA Mode A. The executor implements sentryOptions, nameRouteSpan (D-2, D-7, D-14, D-16, D-27).
import type { StartSpanOptions } from '@sentry/core'
import type { BrowserOptions } from '@sentry/react'

export type Service = 'app' | 'ops-console' | 'support-console'

export interface MonitoringConfig {
  service: Service
  dsn: string | undefined
  release: string
  gateway?: string | null
  routeName?: (pathname: string) => string
}

export function nameRouteSpan(_routeName?: (p: string) => string): (o: StartSpanOptions) => StartSpanOptions {
  return (o) => o
}

export function sentryOptions(c: MonitoringConfig): BrowserOptions | null {
  return { dsn: c.dsn }
}
