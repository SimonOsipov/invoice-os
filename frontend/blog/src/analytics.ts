// The landing's consent and GA4 code, bound to this host. The only importer of landing source.
import { bootAnalytics } from '../../landing/src/analytics'
import { readConsent, type ConsentRecord } from '../../landing/src/consent'
import { applyChoice, syncConsent } from '../../landing/src/consentActions'
import { clearGaCookies } from '../../landing/src/gaCookies'
import { bootChat, loadChat, removeChat } from '../../landing/src/hubspotChat'
import type { ConsentChoice } from '../../landing/src/components/CookieNotice'
import { BLOG_HOSTNAMES } from '../../landing/src/hubspot'

export { CookieNotice } from '../../landing/src/components/CookieNotice'
export { readConsent } from '../../landing/src/consent'
export { PRODUCTION_HOSTNAMES } from '../../landing/src/hubspot'
export { BLOG_BASE } from '../../landing/src/sitePaths'
export { trackBlogDemoOpen, trackBlogPageView } from '../../landing/src/analytics'

// A Reject made elsewhere reaches a _ga left on the old blog host (D-1 window) at the next Blog load.
export const bootBlogAnalytics = (): boolean => {
  const record = readConsent()
  if (record && !record.analytics) clearGaCookies(window.location.hostname)
  return bootAnalytics(BLOG_HOSTNAMES)
}

const applyChat = (record: ConsentRecord | null): void => {
  loadChat(window.location.hostname, record, BLOG_HOSTNAMES)
  removeChat(window.location.hostname, record, BLOG_HOSTNAMES)
}
export const chooseConsent = (choice: ConsentChoice) => {
  const record = applyChoice(choice, { hosts: BLOG_HOSTNAMES })
  applyChat(record)
  return record
}
export const syncBlogConsent = () => {
  const record = syncConsent({ hosts: BLOG_HOSTNAMES })
  if (record) applyChat(record)
  return record
}
export const bootBlogChat = (): boolean => bootChat(BLOG_HOSTNAMES)
