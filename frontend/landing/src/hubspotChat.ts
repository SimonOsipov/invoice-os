// HubSpot live chat behind the production-host and consent gate. Imports inert under
// `node`: `window` and `document` resolve at call time.
import { analyticsAllowed, readConsent, type ConsentRecord } from './consent'
import { expireCookies } from './gaCookies'
import { isProductionHost, PRODUCTION_HOSTNAMES } from './hubspot'

export const HUBSPOT_PORTAL_ID = '148915098'
export const CHAT_SCRIPT_HOST = 'js-eu1.hs-scripts.com'
export const CHAT_SCRIPT_URL = `https://${CHAT_SCRIPT_HOST}/${HUBSPOT_PORTAL_ID}.js`
/** HubSpot's own snippet id: its analytics script looks for it and embeds the loader once. */
export const CHAT_SCRIPT_ID = 'hs-script-loader'

/** Two-label domains the privacy page names. */
export const HUBSPOT_CHAT_DOMAINS: readonly string[] = [
  'hs-scripts.com',
  'hs-analytics.net',
  'hs-banner.com',
  'hscollectedforms.net',
  'hsappstatic.net',
  'usemessages.com',
  'hubspot.com',
]

export const HUBSPOT_COOKIE_NAMES: readonly string[] = [
  '__hstc',
  'hubspotutk',
  '__hssrc',
  '__hssc',
  'messagesUtk',
  'hs-messages-is-open',
  'hs-messages-hide-welcome-message',
]

const OPT_OUT_COOKIE = '__hs_do_not_track'

type ChatSdk = { widget: { load(): void; remove(): void }; clear(opts?: { resetWidget?: boolean }): void }
type ChatWindow = Window & {
  _hsq?: unknown[]
  HubSpotConversations?: ChatSdk
  hsConversationsSettings?: Record<string, unknown>
  hsConversationsOnReady?: Array<() => void>
}

type State = 'idle' | 'loaded' | 'removed'
let state: State = 'idle'

export function shouldLoadChat(hostname: string, allowed: boolean, hosts: readonly string[] = PRODUCTION_HOSTNAMES): boolean {
  return isProductionHost(hostname, hosts) && allowed
}

function cookieNamesIn(raw: string): string[] {
  return raw.split(';').map((pair) => pair.split('=')[0].trim())
}

function pushHsq(w: ChatWindow, command: unknown[]): void {
  ;(w._hsq ??= []).push(command)
}

function setLoadImmediately(w: ChatWindow, value: boolean): void {
  w.hsConversationsSettings = { ...w.hsConversationsSettings, loadImmediately: value }
}

function showWidget(w: ChatWindow): void {
  if (state !== 'loaded') return
  try {
    w.HubSpotConversations?.widget.load()
  } catch {
    // The vendor SDK is best-effort.
  }
}

/** Idempotent. Returns whether the chat is loaded AFTER the call. */
export function loadChat(hostname: string, record: ConsentRecord | null, hosts?: readonly string[]): boolean {
  if (state === 'loaded') return true
  if (!shouldLoadChat(hostname, analyticsAllowed(record), hosts)) return false

  const w = window as ChatWindow
  // Lift an opt-out before the loader's preamble sends its first page view. After a Reject the
  // queued `doNotTrack` may still be unread, with no cookie yet.
  if (state === 'removed' || cookieNamesIn(document.cookie).includes(OPT_OUT_COOKIE)) pushHsq(w, ['doNotTrack', { track: true }])

  if (state === 'idle') {
    const s = document.createElement('script')
    s.id = CHAT_SCRIPT_ID
    s.async = true
    s.defer = true
    s.src = CHAT_SCRIPT_URL
    document.head.appendChild(s)
  } else {
    state = 'loaded'
    setLoadImmediately(w, true)
    // Absent SDK: the queue runs once the embed is ready, whatever loadImmediately it read.
    if (w.HubSpotConversations) showWidget(w)
    else (w.hsConversationsOnReady ??= []).push(() => showWidget(w))
  }
  state = 'loaded'
  return true
}

/** Expires the HubSpot cookies present, on the host and every parent domain. */
export function clearHubSpotCookies(hostname: string, doc: Pick<Document, 'cookie'> = document): string[] {
  const present = new Set(cookieNamesIn(doc.cookie))
  const names = HUBSPOT_COOKIE_NAMES.filter((name) => present.has(name))
  if (names.length > 0) expireCookies(names, hostname, doc)
  return names
}

/** Expires `__hs_do_not_track`, which HubSpot writes on the registrable domain; the tracker only runs on www. */
export function clearOptOutCookie(hostname: string, doc: Pick<Document, 'cookie'> = document): void {
  if (cookieNamesIn(doc.cookie).includes(OPT_OUT_COOKIE)) expireCookies([OPT_OUT_COOKIE], hostname, doc)
}

/** Removes the widget in place. `clear({resetWidget: true})` would build a new one. */
export function removeChat(hostname: string, record: ConsentRecord | null, hosts?: readonly string[]): void {
  if (!isProductionHost(hostname, hosts) || analyticsAllowed(record)) return

  if (state === 'loaded') {
    const w = window as ChatWindow
    setLoadImmediately(w, false)
    try {
      w.HubSpotConversations?.widget.remove()
      w.HubSpotConversations?.clear()
    } catch {
      // The vendor SDK is best-effort; the cookies below still go.
    }
    pushHsq(w, ['doNotTrack'])
    state = 'removed'
  }
  clearHubSpotCookies(hostname)
}

export function bootChat(hosts?: readonly string[]): boolean {
  const hostname = window.location.hostname
  const record = readConsent()
  if (record && !record.analytics) {
    clearHubSpotCookies(hostname)
    return false
  }
  return loadChat(hostname, record, hosts)
}
