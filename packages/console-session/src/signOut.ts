import { apiFetch } from '@invoice-os/api-client/client'

import { clearConsoleSession, loadConsoleSession } from './session'
import { appStartUrl, mintSignInState, type ConsoleTarget } from './state'

const REVOKE_TIMEOUT_MS = 5000

// Revokes server-side, then always clears the record and leaves; a failed revoke warns once.
// A borrowed record has no refresh token to revoke, so it sends nothing.
export async function signOutConsole(o: { storageKey: string; gateway: string | null; landing: string | null }): Promise<void> {
  const session = o.gateway ? loadConsoleSession(o.storageKey) : null
  if (o.gateway && session && session.refreshToken !== null) {
    try {
      await apiFetch<string>(`${o.gateway}/auth/sign-out`, {
        method: 'POST',
        body: { refresh_token: session.refreshToken },
        responseType: 'text',
        signal: AbortSignal.timeout(REVOKE_TIMEOUT_MS),
      })
    } catch (e) {
      console.warn('[console-session] sign-out request failed:', e)
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
  if (o.app && session?.refreshToken === null) {
    window.location.replace(appStartUrl(o.app, mintSignInState(), o.target))
    return
  }
  return signOutConsole(o)
}
