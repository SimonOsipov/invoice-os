// The landing's consent and GA4 code, bound to this host. The only importer of landing source.
import { bootAnalytics } from '../../landing/src/analytics'
import { readConsent } from '../../landing/src/consent'
import { applyChoice, syncConsent } from '../../landing/src/consentActions'
import { clearGaCookies } from '../../landing/src/gaCookies'
import type { ConsentChoice } from '../../landing/src/components/CookieNotice'
import { BLOG_HOSTNAMES } from '../../landing/src/hubspot'

export { CookieNotice } from '../../landing/src/components/CookieNotice'
export { readConsent } from '../../landing/src/consent'
export { PRODUCTION_HOSTNAMES } from '../../landing/src/hubspot'
export { trackBlogPageView } from '../../landing/src/analytics'

// A Reject made on www reaches a blog-host _ga at the next Blog load.
export const bootBlogAnalytics = (): boolean => {
  const record = readConsent()
  if (record && !record.analytics) clearGaCookies(window.location.hostname)
  return bootAnalytics(BLOG_HOSTNAMES)
}
export const chooseConsent = (choice: ConsentChoice) => applyChoice(choice, { hosts: BLOG_HOSTNAMES })
export const syncBlogConsent = () => syncConsent({ hosts: BLOG_HOSTNAMES })
