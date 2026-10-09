// The one effectful seam behind a consent choice: persist it, then make the page
// obey it. Kept out of App.tsx so the mount stays declarative and testable.
import { ensureTag, setAnalyticsRevoked, tagIsLoaded } from './analytics'
import { analyticsAllowed, readConsent, writeConsent, type ConsentRecord, type ConsentStore } from './consent'
import { clearGaCookies } from './gaCookies'
import type { ConsentChoice } from './components/CookieNotice'

export function applyChoice(
  choice: ConsentChoice,
  opts?: { hostname?: string; store?: ConsentStore | null; hosts?: readonly string[] },
): ConsentRecord {
  const accepted = choice === 'accept'
  const hostname = opts?.hostname ?? window.location.hostname
  const record = writeConsent(accepted, opts?.store, undefined, undefined, hostname)

  // On EVERY choice, not inside ensureTag's injection branch: a second Accept in one
  // page load returns early from ensureTag, which would leave the tag resident but
  // muted and the visitor's consent silently ignored. Pinned by T3-14.
  setAnalyticsRevoked(!accepted)

  if (accepted) ensureTag(hostname, record, opts?.hosts)
  else clearGaCookies(hostname)

  return record
}

/** Re-reads the shared choice so an open tab obeys an answer given in another tab. */
export function syncConsent(opts?: { hostname?: string; hosts?: readonly string[] }): ConsentRecord | null {
  const hostname = opts?.hostname ?? window.location.hostname
  const record = readConsent()
  const allowed = analyticsAllowed(record)
  setAnalyticsRevoked(!allowed)
  if (allowed) ensureTag(hostname, record, opts?.hosts)
  else if (tagIsLoaded()) clearGaCookies(hostname)
  return record
}
