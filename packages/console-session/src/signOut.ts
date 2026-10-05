import { apiFetch } from '@invoice-os/api-client/client'

import { clearConsoleSession, loadConsoleSession } from './session'

const REVOKE_TIMEOUT_MS = 5000

// Revokes server-side, then always clears the record and leaves; a failed revoke warns once.
export async function signOutConsole(o: { storageKey: string; gateway: string | null; landing: string | null }): Promise<void> {
  const session = o.gateway ? loadConsoleSession(o.storageKey) : null
  if (o.gateway && session) {
    try {
      // 'text': the 204 has no body, which the JSON path reads as malformed.
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
