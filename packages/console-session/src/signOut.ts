import { ApiError, sessionFetch } from '@invoice-os/api-client/client'

import { clearConsoleSession, loadConsoleSession } from './session'
import { appStartUrl, mintSignInState, type ConsoleTarget } from './state'

const REVOKE_TIMEOUT_MS = 5000

// 'unreached': no answer or a 403 (no Set-Cookie), so the cookie may still be on the device.
export async function postSignOut(gateway: string): Promise<'revoked' | 'failed' | 'unreached'> {
  try {
    await sessionFetch<string>(`${gateway}/auth/sign-out`, { method: 'POST', responseType: 'text', signal: AbortSignal.timeout(REVOKE_TIMEOUT_MS) })
    return 'revoked'
  } catch (e) {
    console.warn('[console-session] sign-out request failed:', e)
    return e instanceof ApiError && e.kind === 'http' && e.status !== 403 ? 'failed' : 'unreached'
  }
}

// Revokes through the gateway cookie, then always clears the record and leaves; a failed revoke warns once.
// A borrowed record has no cookie of its own, so it sends nothing. An unanswered sign-out leaves a marker for the next boot.
export async function signOutConsole(o: { storageKey: string; gateway: string | null; landing: string | null }): Promise<void> {
  const session = o.gateway ? loadConsoleSession(o.storageKey) : null
  if (o.gateway && session && !session.borrowed && (await postSignOut(o.gateway)) === 'unreached') {
    try {
      localStorage.setItem(`${o.storageKey}:sign-out-pending`, '1')
    } catch (e) {
      console.warn('[console-session] failed to store the pending sign-out:', e)
    }
  }
  clearConsoleSession(o.storageKey)
  if (o.landing) window.location.href = o.landing
  else window.location.reload()
}

// A 401 on a borrowed session goes back through the app, keeping the record; anything else signs out.
export async function expireConsole(o: {
  storageKey: string
  gateway: string | null
  landing: string | null
  app: string | null
  target: ConsoleTarget
}): Promise<void> {
  const session = loadConsoleSession(o.storageKey)
  if (o.app && session?.borrowed) {
    window.location.replace(appStartUrl(o.app, mintSignInState(), o.target))
    return
  }
  return signOutConsole(o)
}
