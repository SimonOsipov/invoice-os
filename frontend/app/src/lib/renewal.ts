// On-demand session renewal: clock and storage are injected, no React.
import { ApiError, apiFetch } from '@invoice-os/api-client'

import type { Session } from '../auth'
import { decodeJwtPayload } from './session'

export const RENEW_AT_FRACTION = 0.8
export const RENEW_TIMEOUT_MS = 15_000

export class SessionEndedError extends Error {
  constructor(message = 'session ended') {
    super(message)
    this.name = 'SessionEndedError'
  }
}

export function tokenTimes(token: string | null): { iat: number; exp: number } | null {
  const claims = decodeJwtPayload(token)
  const iat = claims?.iat
  const exp = claims?.exp
  return typeof iat === 'number' && Number.isFinite(iat) && typeof exp === 'number' && Number.isFinite(exp) ? { iat, exp } : null
}

export function tokenTenant(token: string | null): string | null {
  const meta = decodeJwtPayload(token)?.app_metadata as { tenant_id?: unknown } | null | undefined
  return typeof meta?.tenant_id === 'string' ? meta.tenant_id : null
}

// The lifetime is exp − iat (server clock), counted from local receipt, so device skew cancels out.
function lifetimeFrom(session: Session, fraction: number): number | null {
  const times = tokenTimes(session.token)
  if (!session.renewal || !times) {
    return null
  }
  return session.renewal.receivedAt + (times.exp - times.iat) * 1000 * fraction
}

export function renewAt(session: Session): number | null {
  return lifetimeFrom(session, RENEW_AT_FRACTION)
}

export function deadline(session: Session): number | null {
  return lifetimeFrom(session, 1)
}

// Unreadable times on a renewable session are due at once; a session without renewal never is.
export function isRenewalDue(session: Session, now: number = Date.now()): boolean {
  if (!session.renewal) {
    return false
  }
  const at = renewAt(session)
  return at === null || now >= at
}

// A 200 without two non-empty string tokens throws a 'malformed' ApiError, which the renewer treats as transient.
export async function refreshTokens(base: string, refreshToken: string, signal: AbortSignal): Promise<{ access: string; refresh: string }> {
  const body = await apiFetch<{ access_token?: unknown; refresh_token?: unknown } | null>(`${base}/auth/refresh`, {
    method: 'POST',
    body: { refresh_token: refreshToken },
    signal,
  })
  const access = body?.access_token
  const refresh = body?.refresh_token
  if (typeof access !== 'string' || access === '' || typeof refresh !== 'string' || refresh === '') {
    throw new ApiError('malformed', 'refresh answered without both tokens', 200)
  }
  return { access, refresh }
}

export async function refreshSession(base: string, session: Session, now: number = Date.now()): Promise<Session> {
  const { access, refresh } = await refreshTokens(base, session.renewal?.refreshToken ?? '', AbortSignal.timeout(RENEW_TIMEOUT_MS))
  return { ...session, token: access, renewal: { refreshToken: refresh, receivedAt: now } }
}

export interface RenewerOptions {
  base: string
  now?: () => number
  load: () => Session | null
  onRenewed: (next: Session) => void
  onEnded: (end: { keepStorage: boolean }) => void
}

export interface Renewer {
  track(session: Session | null): void
  tracking(): boolean
  fresh(): string | null | Promise<string | null>
}

const sameSubject = (a: Session | null, b: Session): boolean => a !== null && a.me?.user.id === b.me?.user.id

export function createRenewer({ base, now = () => Date.now(), load, onRenewed, onEnded }: RenewerOptions): Renewer {
  let current: Session | null = null
  let ended: Session | null = null
  let inflight: Promise<string | null> | null = null

  function end(session: Session, keepStorage: boolean): never {
    if (ended !== session) {
      ended = session
      onEnded({ keepStorage })
    }
    throw new SessionEndedError()
  }

  async function renew(start: Session): Promise<string | null> {
    const stored = load()
    if (!stored || !sameSubject(stored, start)) {
      end(start, true)
    }
    // Another tab already renewed: adopt its session without a request.
    if (stored.renewal?.refreshToken !== start.renewal?.refreshToken && !isRenewalDue(stored, now())) {
      current = stored
      onRenewed(stored)
      return stored.token
    }

    let next: Session | null = null
    let failure: unknown = null
    try {
      next = await refreshSession(base, stored, now())
    } catch (e) {
      failure = e
    }

    // The session changed while the request was out: discard the answer.
    if (current !== start) {
      throw new SessionEndedError()
    }
    // Another tab signed out or in: leave its storage alone.
    if (!sameSubject(load(), start)) {
      end(start, true)
    }
    if (next) {
      if (tokenTenant(next.token) !== start.me?.tenant.id || !tokenTimes(next.token)) {
        end(start, false)
      }
      current = next
      onRenewed(next)
      return next.token
    }
    if (failure instanceof ApiError && failure.kind === 'http' && (failure.status === 400 || failure.status === 401)) {
      end(start, false)
    }
    // The refresh token may outlive the access token: the record stays for the next boot.
    const until = deadline(start)
    if (until === null || now() >= until) {
      end(start, true)
    }
    return start.token
  }

  return {
    track(session) {
      if (session === current) {
        return
      }
      current = session
      inflight = null
      if (session !== null && session !== ended) {
        ended = null
      }
    },
    tracking() {
      return current !== null
    },
    fresh() {
      const session = current
      if (session === null) {
        return null
      }
      if (session === ended) {
        return Promise.reject(new SessionEndedError())
      }
      if (!isRenewalDue(session, now())) {
        return session.token
      }
      if (!inflight) {
        const p = renew(session).finally(() => {
          if (inflight === p) {
            inflight = null
          }
        })
        inflight = p
      }
      return inflight
    },
  }
}
