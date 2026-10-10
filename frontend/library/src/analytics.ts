// The landing's consent and GA4 code, bound to this host. The only importer of landing source.
import { bootAnalytics } from '../../landing/src/analytics'
import { readConsent } from '../../landing/src/consent'
import { applyChoice, syncConsent } from '../../landing/src/consentActions'
import { clearGaCookies } from '../../landing/src/gaCookies'
import type { ConsentChoice } from '../../landing/src/components/CookieNotice'
import { LIBRARY_HOSTNAMES } from '../../landing/src/hubspot'

export { CookieNotice, type ConsentChoice } from '../../landing/src/components/CookieNotice'
export { readConsent, type ConsentRecord } from '../../landing/src/consent'
export { PRODUCTION_HOSTNAMES } from '../../landing/src/hubspot'
export { trackLibraryDemoOpen, trackLibraryPageView, trackOpenInPlatform, trackTourStart } from '../../landing/src/analytics'

// A Reject made on www reaches a library-host _ga at the next Library load.
export const bootLibraryAnalytics = (): boolean => {
  const record = readConsent()
  if (record && !record.analytics) clearGaCookies(window.location.hostname)
  return bootAnalytics(LIBRARY_HOSTNAMES)
}
export const chooseConsent = (choice: ConsentChoice) => applyChoice(choice, { hosts: LIBRARY_HOSTNAMES })
export const syncLibraryConsent = () => syncConsent({ hosts: LIBRARY_HOSTNAMES })
