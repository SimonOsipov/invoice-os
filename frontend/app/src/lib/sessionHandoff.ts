// Landing → app hand-off code redemption (AUTH-05 D8, D9, D25).
import { apiFetch } from '@invoice-os/api-client'

import { APP_PERSONAS, type Me, type Persona, type Session } from '../auth'
import { isTokenExpired } from './session'

export const HANDOFF_PARAM = 'handoff'

const CODE_RE = /^[A-Za-z0-9_-]{43}$/

export function readHandoffCode(search: string): string | null {
  const code = new URLSearchParams(search).get(HANDOFF_PARAM)
  return code !== null && CODE_RE.test(code) ? code : null
}

// Identity comes from /me; the rest stays the firm persona until AUTH-09 (D8).
export function handoffPersona(me: Me): Persona {
  return { ...APP_PERSONAS.firm, subject: me.user.id, tenantId: me.tenant.id }
}

// No degraded fallback: any failure rejects with the ApiError (a /me 403 means no workspace).
export async function redeemHandoff(base: string, code: string, state: string): Promise<Session> {
  const { access_token: token } = await apiFetch<{ access_token: string }>(`${base}/auth/exchange`, {
    method: 'POST',
    body: { code, state },
  })
  const me = await apiFetch<Me>(`${base}/api/tenancy/v1/me`, { token })
  return { persona: handoffPersona(me), token, me, verified: true, handoff: true }
}

// A live hand-off session wins over every URL param (D18).
export function isLiveHandoffSession(session: Session | null, now: number = Date.now()): boolean {
  return session?.handoff === true && !isTokenExpired(session.token, now)
}
