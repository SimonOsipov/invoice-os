// The one effectful seam behind a consent choice: persist it, then make the page
// obey it. Kept out of App.tsx so the mount stays declarative and testable.
import { ensureTag, setAnalyticsRevoked } from './analytics'
import { writeConsent, type ConsentRecord, type ConsentStore } from './consent'
import { clearGaCookies } from './gaCookies'
import type { ConsentChoice } from './components/CookieNotice'

export function applyChoice(
  choice: ConsentChoice,
  opts?: { hostname?: string; store?: ConsentStore | null },
): ConsentRecord {
  const accepted = choice === 'accept'
  const record = writeConsent(accepted, opts?.store)

  // On EVERY choice, not inside ensureTag's injection branch: a second Accept in one
  // page load returns early from ensureTag, which would leave the tag resident but
  // muted and the visitor's consent silently ignored. Pinned by T3-14.
  setAnalyticsRevoked(!accepted)

  const hostname = opts?.hostname ?? window.location.hostname
  if (accepted) ensureTag(hostname, record)
  else clearGaCookies(hostname)

  return record
}
