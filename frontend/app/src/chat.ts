// The app's consent, Google tag and HubSpot chat, bound to `APP_HOSTNAMES`. The only importer of landing source.
import {
  ensureTag,
  setAnalyticsRevoked,
  setPageFields,
  tagIsLoaded,
  trackAppPageView,
  trackInvoiceValidated,
} from '../../landing/src/analytics'
import { analyticsAllowed, readConsent, writeConsent, type ConsentRecord } from '../../landing/src/consent'
import type { ConsentChoice } from '../../landing/src/components/CookieNotice'
import { clearGaCookies } from '../../landing/src/gaCookies'
import { APP_HOSTNAMES, PRODUCTION_HOSTNAMES } from '../../landing/src/hubspot'
import {
  CHAT_SCRIPT_ID,
  chatIsLoaded,
  clearHubSpotCookies,
  clearOptOutCookie,
  endChat,
  loadChat,
  removeChat,
  shouldLoadChat,
} from '../../landing/src/hubspotChat'
import { landingBase } from './auth'
import { routeName } from './lib/route'

export { APP_NOTICE_BODY, CookieNotice, type ConsentChoice } from '../../landing/src/components/CookieNotice'
export { readConsent, type ConsentRecord } from '../../landing/src/consent'

export function privacyHref(): string {
  return `${landingBase() ?? `https://${PRODUCTION_HOSTNAMES[0]}`}/privacy`
}

const hsq = (): unknown[] => ((window as Window & { _hsq?: unknown[] })._hsq ??= [])

// The bounded route pattern, never the live URL: no query, no id.
function appPage(): string {
  const name = routeName(window.location.pathname)
  return window.location.origin + (name.startsWith('/') ? name : `/${name}`)
}

// Same-origin referrer becomes its route pattern; a cross-origin one keeps its origin only.
function appReferrer(): string | undefined {
  try {
    const ref = new URL(document.referrer)
    if (ref.origin !== window.location.origin) return ref.origin
    const name = routeName(ref.pathname)
    return ref.origin + (name.startsWith('/') ? name : `/${name}`)
  } catch {
    return undefined
  }
}

function boundPageFields(): void {
  setPageFields(appPage(), appReferrer())
}

function loadAppTag(record: ConsentRecord | null): boolean {
  return ensureTag(window.location.hostname, record, APP_HOSTNAMES, { page_location: appPage() })
}

export function bootAppAnalytics(): boolean {
  const record = readConsent()
  // A remount lifts endAppChat's stop; no record holds events, a Reject stays revoked.
  setAnalyticsRevoked(record ? !record.analytics : false)
  boundPageFields()
  return loadAppTag(record)
}

export function reportValidatedInvoice<T extends { status: string }>(inv: T): T {
  if (inv.status === 'validated') trackInvoiceValidated()
  return inv
}

function pushIdentity(email: string | null): void {
  const q = hsq()
  const trimmed = email?.trim()
  if (trimmed) q.push(['identify', { email: trimmed }])
  q.push(['setPath', window.location.pathname], ['trackPageView'])
}

function loadAppChat(record: ConsentRecord | null, email: string | null): boolean {
  const host = window.location.hostname
  if (!shouldLoadChat(host, analyticsAllowed(record), APP_HOSTNAMES)) return false
  if (chatIsLoaded()) return true
  clearOptOutCookie(host)
  if (!document.getElementById(CHAT_SCRIPT_ID)) {
    pushIdentity(email)
    return loadChat(host, record, APP_HOSTNAMES)
  }
  // After a Reject the loader stays and the tracker has opted out: lift it before the page view.
  loadChat(host, record, APP_HOSTNAMES)
  pushIdentity(email)
  return true
}

export function bootAppChat(email: string | null): boolean {
  const record = readConsent()
  if (record && !record.analytics) {
    clearHubSpotCookies(window.location.hostname)
    return false
  }
  return loadAppChat(record, email)
}

export function chooseAppConsent(choice: ConsentChoice, email: string | null): ConsentRecord {
  const host = window.location.hostname
  const accepted = choice === 'accept'
  const record = writeConsent(accepted, undefined, undefined, undefined, host)
  setAnalyticsRevoked(!accepted)
  if (accepted) {
    loadAppTag(record)
    loadAppChat(record, email)
  } else {
    clearGaCookies(host)
    removeChat(host, record, APP_HOSTNAMES)
    clearHubSpotCookies(host)
  }
  return record
}

export function syncAppConsent(email: string | null): ConsentRecord | null {
  const record = readConsent()
  if (!record) return null
  setAnalyticsRevoked(!record.analytics)
  if (record.analytics) {
    loadAppTag(record)
    loadAppChat(record, email)
  } else {
    const host = window.location.hostname
    if (tagIsLoaded()) clearGaCookies(host)
    removeChat(host, record, APP_HOSTNAMES)
    clearHubSpotCookies(host)
  }
  return record
}

export function reportAppPage(): void {
  boundPageFields()
  trackAppPageView(appPage())
  if (!chatIsLoaded()) return
  hsq().push(['setPath', window.location.pathname], ['trackPageView'])
}

export function endAppChat(): void {
  // Page-lifetime stop, not a Reject: the consent record stays untouched.
  setAnalyticsRevoked(true)
  endChat(window.location.hostname)
}
