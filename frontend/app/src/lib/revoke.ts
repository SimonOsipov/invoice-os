// Server-side sign-out: revokes every session of the account (POST /auth/sign-out) through the gateway cookie.
import { ApiError, sessionFetch } from '@invoice-os/api-client'

export const REVOKE_TIMEOUT_MS = 5000
export const SIGN_OUT_PENDING_KEY = 'invoice-os.sign-out-pending'

// 'unreached': no answer or a 403 (no Set-Cookie), so the cookie may still be on the device. Never rejects.
export async function revokeSessions(base: string): Promise<'revoked' | 'failed' | 'unreached'> {
  try {
    await sessionFetch<string>(`${base}/auth/sign-out`, { method: 'POST', responseType: 'text', signal: AbortSignal.timeout(REVOKE_TIMEOUT_MS) })
    return 'revoked'
  } catch (e) {
    return e instanceof ApiError && e.kind === 'http' && e.status !== 403 ? 'failed' : 'unreached'
  }
}

export function markSignOutPending(): void {
  try {
    localStorage.setItem(SIGN_OUT_PENDING_KEY, '1')
  } catch (e) {
    console.warn('[session] failed to store the pending sign-out:', e)
  }
}

export function clearSignOutPending(): void {
  try {
    localStorage.removeItem(SIGN_OUT_PENDING_KEY)
  } catch {
    // storage denied: nothing to settle
  }
}

// Sends a sign-out that got no answer, never on a hand-off load. Never rejects.
export async function settlePendingSignOut(base: string, search: string): Promise<void> {
  try {
    if (localStorage.getItem(SIGN_OUT_PENDING_KEY) === null || new URLSearchParams(search).has('handoff')) {
      return
    }
  } catch {
    return
  }
  if ((await revokeSessions(base)) !== 'unreached') {
    clearSignOutPending()
  }
}
