// Landing → app hand-off code redemption.
import { ApiError, apiFetch } from '@invoice-os/api-client'

import type { Me, Session } from '../auth'
import { refreshTokens } from './renewal'
import { decodeJwtPayload, handoffPersona, isHandoffMe, isTokenExpired } from './session'
import { BASE64URL_43 } from './signInState'

export { handoffPersona }

export const HANDOFF_PARAM = 'handoff'
// Mirrors HandoffTTL in internal/gateway/handoff.go.
export const HANDOFF_TTL_MS = 60_000

// Mirrors provisionRequest in internal/tenancy/tenancy.go.
export type ProvisionBody = { workspace_name: string; display_name: string; kind?: 'firm' | 'in_house' }

// The registration answers GoTrue stored in the token's user_metadata; null unless complete.
export function registrationAnswers(token: string): ProvisionBody | null {
  const meta = decodeJwtPayload(token)?.user_metadata as { registration?: Record<string, unknown> | null } | null | undefined
  const reg = meta?.registration
  if (reg === null || typeof reg !== 'object') {
    return null
  }
  const { workspace_name, display_name, kind } = reg
  if (typeof workspace_name !== 'string' || workspace_name.trim() === '' || typeof display_name !== 'string' || display_name.trim() === '') {
    return null
  }
  if (kind === undefined) {
    return { workspace_name, display_name }
  }
  return kind === 'firm' || kind === 'in_house' ? { workspace_name, display_name, kind } : null
}

export function readHandoffCode(search: string): string | null {
  const code = new URLSearchParams(search).get(HANDOFF_PARAM)
  return code !== null && BASE64URL_43.test(code) ? code : null
}

export type InviteOutcome = 'invalid' | 'already-member' | 'other-address'

// Red-phase stub: never thrown yet.
export class InviteRefusedError extends Error {
  readonly outcome: InviteOutcome
  constructor(outcome: InviteOutcome) {
    super(`invite refused: ${outcome}`)
    this.name = 'InviteRefusedError'
    this.outcome = outcome
  }
}

// No degraded fallback: any failure, a 15 s timeout included, rejects.
export async function redeemHandoff(base: string, code: string, state: string, now: number = Date.now(), _invite: string | null = null): Promise<Session> {
  const signal = AbortSignal.timeout(15000)
  const { access_token: token, refresh_token: refreshToken } = await apiFetch<{ access_token: string; refresh_token?: unknown }>(`${base}/auth/exchange`, {
    method: 'POST',
    body: { code, state },
    signal,
  })
  let me: Me
  try {
    me = await apiFetch<Me>(`${base}/api/tenancy/v1/me`, { token, signal })
  } catch (err) {
    const answers = err instanceof ApiError && err.status === 403 ? registrationAnswers(token) : null
    if (answers === null) {
      throw err
    }
    return provisionAndRead(base, token, refreshToken, answers, now, signal)
  }
  if (!isHandoffMe(me)) {
    throw new Error('malformed /me')
  }
  // A missing or malformed refresh token gives a session without renewal. The token may have
  // waited in the gateway's store for up to HandoffTTL, so receipt is backdated by it.
  const renewal = typeof refreshToken === 'string' && refreshToken !== '' ? { refreshToken, receivedAt: now - HANDOFF_TTL_MS } : undefined
  return { persona: handoffPersona(me), token, me, verified: true, handoff: true, ...(renewal ? { renewal } : {}) }
}

// ceiling: an account whose workspace an operator deleted re-provisions at its next sign-in; revisit when workspace deletion ships.
async function provisionAndRead(base: string, token: string, refreshToken: unknown, answers: ProvisionBody, now: number, signal: AbortSignal): Promise<Session> {
  try {
    await apiFetch(`${base}/api/tenancy/v1/workspaces`, { method: 'POST', token, body: answers, signal })
  } catch (err) {
    // 409: the identity already holds a membership; the refresh below tells which.
    if (!(err instanceof ApiError && err.status === 409)) {
      throw err
    }
  }
  if (typeof refreshToken !== 'string' || refreshToken === '') {
    throw new Error('no refresh token after provisioning')
  }
  const { access, refresh } = await refreshTokens(base, refreshToken, signal)
  const me = await apiFetch<Me>(`${base}/api/tenancy/v1/me`, { token: access, signal })
  if (!isHandoffMe(me)) {
    throw new Error('malformed /me')
  }
  return { persona: handoffPersona(me), token: access, me, verified: true, handoff: true, renewal: { refreshToken: refresh, receivedAt: now } }
}

// A live hand-off session wins over every URL param.
export function isLiveHandoffSession(session: Session | null, now: number = Date.now()): boolean {
  return session?.handoff === true && !isTokenExpired(session.token, now)
}
