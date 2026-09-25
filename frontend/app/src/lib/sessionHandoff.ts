// Landing → app hand-off code redemption.
import { apiFetch } from '@invoice-os/api-client'

import type { Me, Session } from '../auth'
import { handoffPersona, isTokenExpired } from './session'
import { BASE64URL_43 } from './signInState'

export { handoffPersona }

export const HANDOFF_PARAM = 'handoff'

export function readHandoffCode(search: string): string | null {
  const code = new URLSearchParams(search).get(HANDOFF_PARAM)
  return code !== null && BASE64URL_43.test(code) ? code : null
}

// No degraded fallback: any failure, a 15 s timeout included, rejects (a /me 403 means no workspace).
export async function redeemHandoff(base: string, code: string, state: string): Promise<Session> {
  const signal = AbortSignal.timeout(15000)
  const { access_token: token } = await apiFetch<{ access_token: string }>(`${base}/auth/exchange`, {
    method: 'POST',
    body: { code, state },
    signal,
  })
  const me = await apiFetch<Me>(`${base}/api/tenancy/v1/me`, { token, signal })
  return { persona: handoffPersona(me), token, me, verified: true, handoff: true }
}

// A live hand-off session wins over every URL param.
export function isLiveHandoffSession(session: Session | null, now: number = Date.now()): boolean {
  return session?.handoff === true && !isTokenExpired(session.token, now)
}
