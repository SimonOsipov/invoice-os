// Server-side sign-out: revokes every session of the account (POST /auth/sign-out).
import { apiFetch } from '@invoice-os/api-client'

export const REVOKE_TIMEOUT_MS = 5000

// STUB (SECAUD-17-11): the executor rewrites this over sessionFetch; 'unreached' is not produced yet.
export const SIGN_OUT_PENDING_KEY = 'invoice-os.sign-out-pending'
export function markSignOutPending(): void {}
export function clearSignOutPending(): void {}
export async function settlePendingSignOut(_base: string, _search: string): Promise<void> {}

// Never rejects.
export async function revokeSessions(base: string, refreshToken?: string): Promise<'revoked' | 'failed' | 'unreached'> {
  try {
    await apiFetch<string>(`${base}/auth/sign-out`, {
      method: 'POST',
      body: { refresh_token: refreshToken },
      responseType: 'text',
      signal: AbortSignal.timeout(REVOKE_TIMEOUT_MS),
    })
    return 'revoked'
  } catch {
    return 'failed'
  }
}
