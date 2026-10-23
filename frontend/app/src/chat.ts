// The app's consent and HubSpot chat, bound to `APP_HOSTNAMES`. The only importer of landing source.
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

export { APP_NOTICE_BODY, CookieNotice, type ConsentChoice } from '../../landing/src/components/CookieNotice'
export { readConsent, type ConsentRecord } from '../../landing/src/consent'

export function privacyHref(): string {
  return `${landingBase() ?? `https://${PRODUCTION_HOSTNAMES[0]}`}/privacy`
}

const hsq = (): unknown[] => ((window as Window & { _hsq?: unknown[] })._hsq ??= [])

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
  if (accepted) {
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
  if (record.analytics) {
    loadAppChat(record, email)
  } else {
    const host = window.location.hostname
    removeChat(host, record, APP_HOSTNAMES)
    clearHubSpotCookies(host)
  }
  return record
}

export function reportAppPage(): void {
  if (!chatIsLoaded()) return
  hsq().push(['setPath', window.location.pathname], ['trackPageView'])
}

export function endAppChat(): void {
  endChat(window.location.hostname)
}
