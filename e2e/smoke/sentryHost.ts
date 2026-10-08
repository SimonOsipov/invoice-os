/** Hostname-parsed, never a substring match: `https://evil.example/?x=sentry.io` is not Sentry. */
export function isSentryHost(rawUrl: string): boolean {
  let host: string
  try {
    host = new URL(rawUrl).hostname.toLowerCase()
  } catch {
    return false
  }
  return host === 'sentry.io' || host.endsWith('.sentry.io')
}

// Production custom domains (.claude/rules/ci-railway.md), where Sentry may be on. Support-console has none.
const PRODUCTION_HOSTS = new Set(['www.ascomply.com', 'app.ascomply.com', 'ops.ascomply.com'])

export function isProductionHost(rawUrl: string): boolean {
  return PRODUCTION_HOSTS.has(new URL(rawUrl).hostname.toLowerCase())
}
