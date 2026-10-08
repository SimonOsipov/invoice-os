// The landing's consent and GA4 code, bound to this host. The only importer of landing source.
import { bootAnalytics } from '../../landing/src/analytics'
import { applyChoice } from '../../landing/src/consentActions'
import type { ConsentChoice } from '../../landing/src/components/CookieNotice'
import { LIBRARY_HOSTNAMES } from '../../landing/src/hubspot'

export { CookieNotice, type ConsentChoice } from '../../landing/src/components/CookieNotice'
export { readConsent, type ConsentRecord } from '../../landing/src/consent'
export { PRODUCTION_HOSTNAMES } from '../../landing/src/hubspot'
export { trackLibraryDemoOpen, trackLibraryPageView, trackOpenInPlatform, trackTourStart } from '../../landing/src/analytics'

// The library's _ga stays on its own host.
const cookieDomain = LIBRARY_HOSTNAMES[0]

export const bootLibraryAnalytics = (): boolean => bootAnalytics(LIBRARY_HOSTNAMES, cookieDomain)
export const chooseConsent = (choice: ConsentChoice) => applyChoice(choice, { hosts: LIBRARY_HOSTNAMES, cookieDomain })
