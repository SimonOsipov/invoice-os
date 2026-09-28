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
