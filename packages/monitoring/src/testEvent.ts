import * as Sentry from '@sentry/react'
import type { Service } from './options'

// Installs window.__ascSentryTest(passphrase): one labelled event when SHA-256(passphrase) matches the build digest.
// ceiling: the digest stays in the bundle until the variable is deleted and the SPA rebuilt
export function installTestEvent(service: Service, digest: string | undefined): void {
  const want = (digest ?? '').trim().toLowerCase()
  if (!/^[0-9a-f]{64}$/.test(want)) return
  ;(window as unknown as { __ascSentryTest: unknown }).__ascSentryTest = async (passphrase?: unknown): Promise<string | null> => {
    try {
      if (typeof passphrase !== 'string' || !crypto.subtle) return null
      const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(passphrase))
      const got = Array.from(new Uint8Array(buf), (b) => b.toString(16).padStart(2, '0')).join('')
      if (got !== want) return null
      const err = new Error('sentry test event')
      err.name = 'SentryTestEvent'
      // The fingerprint goes on an isolated scope so later events do not inherit it.
      return Sentry.withScope((scope) => {
        scope.setFingerprint(['sentry-test-event', service])
        return scope.captureException(err)
      })
    } catch {
      return null
    }
  }
}
