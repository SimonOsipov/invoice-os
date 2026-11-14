// Landing → app hand-off code redemption.
import { ApiError, apiFetch, sessionFetch } from '@invoice-os/api-client'

import type { Me, Session } from '../auth'
import { trackWorkspaceCreated } from '../chat'
import { trimName } from './pendingInvite'
import { refreshAccess } from './renewal'
import { clearSignOutPending } from './revoke'
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

// A tenant-less sign-in with invites waiting, or none (invites empty): a removed person, or `firstWorkspace` for an account with no answers, no membership (`live_membership` false). The Join screen resolves it (joinInvite, createOwnWorkspace).
export type JoinOffer = { kind: 'join'; token: string; renewable: boolean; invites: PendingInvite[]; answers: ProvisionBody | null; firstWorkspace?: true }

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

function isEmptyInvites(body: unknown): boolean {
  const list = (body as { invitations?: unknown } | null | undefined)?.invitations
  return Array.isArray(list) && list.length === 0
}

// A missing or non-boolean key reads false (an old tenancy revision omits it).
function isRemoved(body: unknown): boolean {
  return (body as { removed?: unknown } | null | undefined)?.removed === true
}

// Only an explicit false: a missing or non-boolean key reads as a live member, so no form.
function hasNoLiveMembership(body: unknown): boolean {
  return (body as { live_membership?: unknown } | null | undefined)?.live_membership === false
}

export function isJoinOffer(x: Session | JoinOffer): x is JoinOffer {
  return 'kind' in x && x.kind === 'join'
}

// A /me 503 retries within the 15 s budget; any other failure, the timeout included, rejects at once.
const ME_RETRY_DELAYS_MS = [500, 1000]

// The gateway answers 503 when GoTrue is slow; the single-use code is spent, so only /me can retry.
async function readMeRetrying(base: string, token: string, signal: AbortSignal): Promise<Me> {
  for (const delay of ME_RETRY_DELAYS_MS) {
    try {
      return await apiFetch<Me>(`${base}/api/tenancy/v1/me`, { token, signal })
    } catch (err) {
      const transient = err instanceof ApiError && err.status === 503
      if (!transient || signal.aborted) {
        throw err
      }
    }
    await new Promise<void>((resolve, reject) => {
      const onAbort = () => {
        clearTimeout(timer)
        reject(signal.reason)
      }
      const timer = setTimeout(() => {
        signal.removeEventListener('abort', onAbort)
        resolve()
      }, delay)
      signal.addEventListener('abort', onAbort, { once: true })
    })
  }
  return apiFetch<Me>(`${base}/api/tenancy/v1/me`, { token, signal })
}

export async function redeemHandoff(base: string, code: string, state: string, now: number = Date.now(), invite: { token: string; name: string | null } | null = null): Promise<Session | JoinOffer> {
  const signal = AbortSignal.timeout(15000)
  const { access_token: token, renewable: renewableRaw } = await sessionFetch<{ access_token: string; renewable?: unknown }>(`${base}/auth/exchange`, {
    method: 'POST',
    body: { code, state },
    signal,
  })
  // The exchange set the cookie, so a sign-out owed from before is moot.
  clearSignOutPending()
  const renewable = renewableRaw === true
  if (invite !== null) {
    return acceptInviteAndRead(base, token, renewable, invite, now, signal)
  }
  let me: Me
  try {
    me = await readMeRetrying(base, token, signal)
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 403)) {
      throw err
    }
    const answers = registrationAnswers(token)
    // Only an empty list means no invites; a failed or malformed lookup ends at 'failed', never provisioning.
    const invites = await apiFetch<unknown>(`${base}/api/tenancy/v1/invitations/mine`, { token, signal }).catch((lookupErr: unknown) => {
      throw new Error('invitation lookup failed', { cause: lookupErr })
    })
    if (isPendingInvites(invites)) {
      return { kind: 'join', token, renewable, invites: invites.invitations, answers }
    }
    if (!isEmptyInvites(invites)) {
      throw new Error('malformed invitation list')
    }
    if (isRemoved(invites)) {
      return { kind: 'join', token, renewable, invites: [], answers }
    }
    if (answers === null) {
      if (hasNoLiveMembership(invites)) {
        return { kind: 'join', token, renewable, invites: [], answers: null, firstWorkspace: true }
      }
      throw err
    }
    return provisionAndRead(base, { token, renewable }, answers, now, signal)
  }
  if (!isHandoffMe(me)) {
    throw new Error('malformed /me')
  }
  // Without `renewable` the session has no renewal. The code may have waited in the gateway's
  // store for up to HandoffTTL, so receipt is backdated by it.
  const renewal = renewable ? { receivedAt: now - HANDOFF_TTL_MS } : undefined
  return { persona: handoffPersona(me), token, me, verified: true, handoff: true, ...(renewal ? { renewal } : {}) }
}

// Mirrors msgInviteNotValid, msgAlreadyMember and msgWrongAddress in internal/tenancy.
const INVITE_REFUSALS: Record<string, [number, InviteOutcome]> = {
  'this invite is no longer valid': [404, 'invalid'],
  'you already belong to a workspace': [409, 'already-member'],
  'this invite was sent to a different email address': [403, 'other-address'],
}

// The invitee's first token has no tenant, so /me waits for the refresh that carries the new membership.
async function acceptInviteAndRead(base: string, token: string, renewable: boolean, invite: { token: string; name: string | null }, now: number, signal: AbortSignal): Promise<Session> {
  try {
    await apiFetch(`${base}/api/tenancy/v1/invitations/accept`, { method: 'POST', token, body: invite.name === null ? { token: invite.token } : { token: invite.token, display_name: invite.name }, signal })
  } catch (err) {
    const message = err instanceof ApiError ? (err.body as { error?: unknown } | null | undefined)?.error : undefined
    const refusal = typeof message === 'string' && Object.hasOwn(INVITE_REFUSALS, message) ? INVITE_REFUSALS[message] : undefined
    if (err instanceof ApiError && refusal && err.status === refusal[0]) {
      throw new InviteRefusedError(refusal[1])
    }
    throw err
  }
  return renewAndRead(base, { token, renewable }, now, signal, 'no refresh token after accepting the invite')
}

export async function createOwnWorkspace(base: string, offer: JoinOffer, body: ProvisionBody, now: number = Date.now()): Promise<Session> {
  return provisionAndRead(base, offer, body, now, AbortSignal.timeout(15000))
}

// A 404 or 409 re-reads /me first: another tab may have accepted already.
// A renewal updates the offer's access token in place so the next Join uses the live one.
export async function joinInvite(base: string, offer: JoinOffer, id: string, displayName: string, now: number = Date.now()): Promise<Session> {
  const signal = AbortSignal.timeout(15000)
  try {
    await callRenewing(base, offer, signal, (token) =>
      apiFetch(`${base}/api/tenancy/v1/invitations/${encodeURIComponent(id)}/accept`, { method: 'POST', token, body: { display_name: trimName(displayName) }, signal }),
    )
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
      return await renewAndRead(base, offer, now, signal, 'no refresh token after the refusal')
    } catch {
      throw new InviteRefusedError(outcome)
    }
  }
  return renewAndRead(base, offer, now, signal, 'no refresh token after joining the invite')
}

async function provisionAndRead(base: string, auth: { token: string; renewable: boolean }, answers: ProvisionBody, now: number, signal: AbortSignal): Promise<Session> {
  try {
    await callRenewing(base, auth, signal, (t) => apiFetch(`${base}/api/tenancy/v1/workspaces`, { method: 'POST', token: t, body: answers, signal }))
    trackWorkspaceCreated()
  } catch (err) {
    // 409: the identity already holds a membership; the refresh below tells which.
    if (!(err instanceof ApiError && err.status === 409)) {
      throw err
    }
  }
  return renewAndRead(base, auth, now, signal, 'no refresh token after provisioning')
}

// The Join screen can outlive the access token: one 401 renews and retries once. 
async function callRenewing(base: string, auth: { token: string; renewable: boolean }, signal: AbortSignal, call: (token: string) => Promise<unknown>): Promise<unknown> {
  try {
    return await call(auth.token)
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 401) || !auth.renewable) {
      throw err
    }
    const access = await refreshAccess(base, signal)
    auth.token = access
    return call(access)
  }
}

async function renewAndRead(base: string, auth: { token: string; renewable: boolean }, now: number, signal: AbortSignal, missing: string): Promise<Session> {
  if (!auth.renewable) {
    throw new Error(missing)
  }
  const access = await refreshAccess(base, signal)
  auth.token = access
  const me = await readMeRetrying(base, access, signal)
  if (!isHandoffMe(me)) {
    throw new Error('malformed /me')
  }
  return { persona: handoffPersona(me), token: access, me, verified: true, handoff: true, renewal: { receivedAt: now } }
}

// A live hand-off session wins over every URL param.
export function isLiveHandoffSession(session: Session | null, now: number = Date.now()): boolean {
  return session?.handoff === true && !isTokenExpired(session.token, now)
}
