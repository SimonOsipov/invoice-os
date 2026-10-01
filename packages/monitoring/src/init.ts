import * as Sentry from '@sentry/react'
import { sentryOptions, type Service } from './options'
import { RELEASE } from './release'

// A DSN the SDK cannot parse makes it log "Invalid Sentry Dsn", so treat it as off.
function isSentryDsn(dsn: string | undefined): boolean {
  try {
    const u = new URL(dsn ?? '')
    return /^https?:$/.test(u.protocol) && u.username !== '' && /^\d+$/.test(u.pathname.split('/').pop() ?? '')
  } catch {
    return false
  }
}

export function initMonitoring(service: Service, opts?: { gateway?: string | null; routeName?: (p: string) => string }): boolean {
  const options = sentryOptions({ service, dsn: import.meta.env.VITE_SENTRY_DSN, release: RELEASE, ...opts })
  if (options === null || !isSentryDsn(options.dsn)) return false
  Sentry.init(options)
  return true
}
