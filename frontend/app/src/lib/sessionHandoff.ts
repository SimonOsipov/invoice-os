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

// Tenancy's three exact accept refusals; the landing shows a notice for each.
export class InviteRefusedError extends Error {
  readonly outcome: InviteOutcome
  constructor(outcome: InviteOutcome) {
    super(`invite refused: ${outcome}`)
    this.name = 'InviteRefusedError'
    this.outcome = outcome
  }
}

// Mirrors PendingInvite in internal/tenancy/store.go.
export interface PendingInvite {
  id: string
  workspace: string
  role: string
  inviter: string | null
  expires_at: string
}

// A tenant-less sign-in with invites waiting; the Join screen resolves it (joinInvite, createOwnWorkspace).
export type JoinOffer = { kind: 'join'; token: string; refreshToken: unknown; invites: PendingInvite[]; answers: ProvisionBody | null }

export function isPendingInvites(body: unknown): body is { invitations: PendingInvite[] } {
  const list = (body as { invitations?: unknown } | null | undefined)?.invitations
  return (
    Array.isArray(list) &&
    list.length > 0 &&
    list.every((i) => {
      const o = i as Record<string, unknown> | null
      return (
        o !== null &&
        typeof o === 'object' &&
        typeof o.id === 'string' &&
        o.id !== '' &&
        typeof o.workspace === 'string' &&
        typeof o.role === 'string' &&
        (o.inviter === null || typeof o.inviter === 'string') &&
        typeof o.expires_at === 'string'
      )
    })
  )
}

export function isJoinOffer(x: Session | JoinOffer): x is JoinOffer {
  return 'kind' in x && x.kind === 'join'
}

// No degraded fallback: any failure, a 15 s timeout included, rejects.
export async function redeemHandoff(base: string, code: string, state: string, now: number = Date.now(), invite: string | null = null): Promise<Session | JoinOffer> {
  const signal = AbortSignal.timeout(15000)
  const { access_token: token, refresh_token: refreshToken } = await apiFetch<{ access_token: string; refresh_token?: unknown }>(`${base}/auth/exchange`, {
    method: 'POST',
    body: { code, state },
    signal,
  })
  if (invite !== null) {
    return acceptInviteAndRead(base, token, refreshToken, invite, now, signal)
  }
  let me: Me
  try {
    me = await apiFetch<Me>(`${base}/api/tenancy/v1/me`, { token, signal })
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 403)) {
      throw err
    }
    const answers = registrationAnswers(token)
    // Invites first (D7); a failed or malformed lookup keeps today's path (D11).
    const invites = await apiFetch<unknown>(`${base}/api/tenancy/v1/invitations/mine`, { token, signal }).catch(() => null)
    if (isPendingInvites(invites)) {
      return { kind: 'join', token, refreshToken, invites: invites.invitations, answers }
    }
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

// Mirrors msgInviteNotValid, msgAlreadyMember and msgWrongAddress in internal/tenancy.
const INVITE_REFUSALS: Record<string, [number, InviteOutcome]> = {
  'this invite is no longer valid': [404, 'invalid'],
  'you already belong to a workspace': [409, 'already-member'],
  'this invite was sent to a different email address': [403, 'other-address'],
}

// The invitee's first token has no tenant, so /me waits for the refresh that carries the new membership.
async function acceptInviteAndRead(base: string, token: string, refreshToken: unknown, invite: string, now: number, signal: AbortSignal): Promise<Session> {
  try {
    await apiFetch(`${base}/api/tenancy/v1/invitations/accept`, { method: 'POST', token, body: { token: invite }, signal })
  } catch (err) {
    const message = err instanceof ApiError ? (err.body as { error?: unknown } | null | undefined)?.error : undefined
    const refusal = typeof message === 'string' && Object.hasOwn(INVITE_REFUSALS, message) ? INVITE_REFUSALS[message] : undefined
    if (err instanceof ApiError && refusal && err.status === refusal[0]) {
      throw new InviteRefusedError(refusal[1])
    }
    throw err
  }
  return renewAndRead(base, refreshToken, now, signal, 'no refresh token after accepting the invite')
}

export async function createOwnWorkspace(base: string, offer: JoinOffer, now: number = Date.now()): Promise<Session> {
  if (offer.answers === null) {
    throw new Error('no registration answers')
  }
  return provisionAndRead(base, offer.token, offer.refreshToken, offer.answers, now, AbortSignal.timeout(15000))
}

// A 404 or 409 re-reads /me first: another tab may have accepted already (D10).
export async function joinInvite(base: string, offer: JoinOffer, id: string, now: number = Date.now()): Promise<Session> {
  const signal = AbortSignal.timeout(15000)
  try {
    await apiFetch(`${base}/api/tenancy/v1/invitations/${encodeURIComponent(id)}/accept`, { method: 'POST', token: offer.token, signal })
  } catch (err) {
    const message = err instanceof ApiError ? (err.body as { error?: unknown } | null | undefined)?.error : undefined
    const outcome =
      err instanceof ApiError && message === 'this invite is no longer valid' && err.status === 404
        ? 'invalid'
        : err instanceof ApiError && message === 'you already belong to a workspace' && err.status === 409
          ? 'already-member'
          : null
    if (outcome === null) {
      throw err
    }
    try {
      return await renewAndRead(base, offer.refreshToken, now, signal, 'no refresh token after the refusal')
    } catch {
      throw new InviteRefusedError(outcome)
    }
  }
  return renewAndRead(base, offer.refreshToken, now, signal, 'no refresh token after joining the invite')
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
  return renewAndRead(base, refreshToken, now, signal, 'no refresh token after provisioning')
}

async function renewAndRead(base: string, refreshToken: unknown, now: number, signal: AbortSignal, missing: string): Promise<Session> {
  if (typeof refreshToken !== 'string' || refreshToken === '') {
    throw new Error(missing)
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
