// The landing's consent and GA4 code, bound to this host.
import { bootAnalytics } from '../../landing/src/analytics'
import { applyChoice, syncConsent } from '../../landing/src/consentActions'
import { bootChat } from '../../landing/src/hubspotChat'
import type { ConsentChoice } from '../../landing/src/components/CookieNotice'

export { CookieNotice, type ConsentChoice } from '../../landing/src/components/CookieNotice'
export { readConsent } from '../../landing/src/consent'
export { PRODUCTION_HOSTNAMES } from '../../landing/src/hubspot'
export { READINESS_CONSENT_TEXT as CONSENT_TEXT } from '../../landing/src/readinessConsent'

export const bootCheckAnalytics = (): boolean => bootAnalytics()
export const bootCheckChat = (): boolean => bootChat()
export const chooseConsent = (choice: ConsentChoice) => applyChoice(choice)
export const syncCheckConsent = () => syncConsent()
