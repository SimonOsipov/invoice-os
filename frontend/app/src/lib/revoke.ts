// Server-side sign-out: revokes every session of the account (POST /auth/sign-out).
import { apiFetch } from '@invoice-os/api-client'

export const REVOKE_TIMEOUT_MS = 5000

// Never rejects. 'text': the 204 has no body, which the JSON path reads as malformed.
export async function revokeSessions(base: string, refreshToken: string): Promise<'revoked' | 'failed'> {
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
