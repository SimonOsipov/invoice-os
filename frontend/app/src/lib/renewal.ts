// Session renewal (AUTH-06 D4-D7, D23). Stub: signatures only, bodies land with the implementation.
import type { Session } from '../auth'

export const RENEW_AT_FRACTION = 0.8
export const RENEW_TIMEOUT_MS = 15_000

export class SessionEndedError extends Error {
  constructor(message = 'session ended') {
    super(message)
    this.name = 'SessionEndedError'
  }
}

export function tokenTimes(_token: string | null): { iat: number; exp: number } | null {
  return null
}

export function tokenTenant(_token: string | null): string | null {
  return null
}

export function renewAt(_session: Session): number | null {
  return null
}

export function deadline(_session: Session): number | null {
  return null
}

export function isRenewalDue(_session: Session, _now: number = Date.now()): boolean {
  return false
}

export async function refreshSession(_base: string, _session: Session, _now: number = Date.now()): Promise<Session> {
  throw new Error('not implemented')
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
  fresh(): string | null | Promise<string | null>
}

export function createRenewer(_opts: RenewerOptions): Renewer {
  return {
    track() {},
    fresh() {
      return ''
    },
  }
}
