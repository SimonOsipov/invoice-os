// Server-side sign-out: revokes every session of the account (POST /auth/sign-out).

export const REVOKE_TIMEOUT_MS = 5000

// Stub: sends nothing yet.
export async function revokeSessions(_base: string, _refreshToken: string): Promise<'revoked' | 'failed'> {
  return 'failed'
}
