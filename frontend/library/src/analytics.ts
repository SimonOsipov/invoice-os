// The landing's consent and GA4 code, bound to this host. The only importer of landing source.
import { bootAnalytics } from '../../landing/src/analytics'
import { readConsent, type ConsentRecord } from '../../landing/src/consent'
import { applyChoice, syncConsent } from '../../landing/src/consentActions'
import { clearGaCookies } from '../../landing/src/gaCookies'
import { bootChat, loadChat, removeChat } from '../../landing/src/hubspotChat'
import type { ConsentChoice } from '../../landing/src/components/CookieNotice'
import { LIBRARY_HOSTNAMES } from '../../landing/src/hubspot'

export { CookieNotice, type ConsentChoice } from '../../landing/src/components/CookieNotice'
export { readConsent, type ConsentRecord } from '../../landing/src/consent'
export { PRODUCTION_HOSTNAMES } from '../../landing/src/hubspot'
export { LIBRARY_BASE } from '../../landing/src/sitePaths'
export { trackLibraryDemoOpen, trackLibraryPageView, trackOpenInPlatform, trackTourStart } from '../../landing/src/analytics'

// A Reject made elsewhere reaches a _ga left on the old library host until the old-host 301 is live, at the next Library load.
export const bootLibraryAnalytics = (): boolean => {
  const record = readConsent()
  if (record && !record.analytics) clearGaCookies(window.location.hostname)
  return bootAnalytics(LIBRARY_HOSTNAMES)
}

const applyChat = (record: ConsentRecord | null): void => {
  loadChat(window.location.hostname, record, LIBRARY_HOSTNAMES)
  removeChat(window.location.hostname, record, LIBRARY_HOSTNAMES)
}
export const chooseConsent = (choice: ConsentChoice) => {
  const record = applyChoice(choice, { hosts: LIBRARY_HOSTNAMES })
  applyChat(record)
  return record
}
export const syncLibraryConsent = () => {
  const record = syncConsent({ hosts: LIBRARY_HOSTNAMES })
  if (record) applyChat(record)
  return record
}
export const bootLibraryChat = (): boolean => bootChat(LIBRARY_HOSTNAMES)
