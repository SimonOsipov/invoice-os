import { ApiError, sessionFetch } from '@invoice-os/api-client/client'

import { clearConsoleSession, loadConsoleSession } from './session'
import { iapExpiredFreshKey, iapMintedAtKey, IAP_FRESH_MS, markSignedOut } from './iap'
import { appStartUrl, mintSignInState, type ConsoleTarget } from './state'

const REVOKE_TIMEOUT_MS = 5000

// 'unreached': no answer or a 403 (no Set-Cookie), so the cookie may still be on the device.
export async function postSignOut(gateway: string): Promise<'revoked' | 'failed' | 'unreached'> {
  try {
    await sessionFetch<string>(`${gateway}/auth/sign-out`, {
      method: 'POST',
      responseType: 'text',
      signal: AbortSignal.timeout(REVOKE_TIMEOUT_MS),
    })
    return 'revoked'
  } catch (e) {
    console.warn('[console-session] sign-out request failed:', e)
    return e instanceof ApiError && e.kind === 'http' && e.status !== 403 ? 'failed' : 'unreached'
  }
}

// Revokes through the gateway cookie, then clears the record; a failed revoke warns once.
// A borrowed record has no cookie of its own, so it sends nothing. An unanswered sign-out leaves a marker for the next boot.
// alwaysRevoke: also revoke with no stored record (the cookie can outlive it).
export async function endConsoleSession(o: { storageKey: string; gateway: string | null }, alwaysRevoke = false): Promise<void> {
  const session = o.gateway ? loadConsoleSession(o.storageKey) : null
  if (o.gateway && (session ? !session.borrowed : alwaysRevoke) && (await postSignOut(o.gateway)) === 'unreached') {
    try {
      localStorage.setItem(`${o.storageKey}:sign-out-pending`, '1')
    } catch (e) {
      console.warn('[console-session] failed to store the pending sign-out:', e)
    }
  }
  clearConsoleSession(o.storageKey)
}

// Ends the session, then leaves. IAP mode stays on the console: the signed-out marker shows the signed-out screen.
export async function signOutConsole(o: { storageKey: string; gateway: string | null; landing: string | null; iap?: boolean }): Promise<void> {
  await endConsoleSession(o)
  if (o.iap) {
    markSignedOut(o.storageKey)
    window.location.reload()
  } else if (o.landing) window.location.href = o.landing
  else window.location.reload()
}

// A 401 on a borrowed session goes back through the app, keeping the record; anything else signs out.
export async function expireConsole(o: {
  storageKey: string
  gateway: string | null
  landing: string | null
  app: string | null
  target: ConsoleTarget
  iap?: boolean
}): Promise<void> {
  const session = loadConsoleSession(o.storageKey)
  if (o.app && session?.borrowed) {
    window.location.replace(appStartUrl(o.app, mintSignInState(), o.target))
    return
  }
  if (!o.iap) return signOutConsole(o)
  // No signed-out marker: the next boot mints. A lapse right after a mint makes it show "failed" instead.
  clearConsoleSession(o.storageKey)
  try {
    const at = Number(sessionStorage.getItem(iapMintedAtKey(o.storageKey)))
    if (at > 0 && Date.now() - at < IAP_FRESH_MS) sessionStorage.setItem(iapExpiredFreshKey(o.storageKey), '1')
  } catch {
    // storage denied: no guard to arm
  }
  window.location.reload()
}
