// An invite token held across the landing bounce. Mirrors signInState.ts's versioned-blob,
// warn-never-throw conventions. Never reads window.location.
import { BASE64URL_43, SIGN_IN_STATE_TTL_MS } from './signInState'

export const PENDING_INVITE_KEY = 'invoice-os.pendingInvite'
const PENDING_INVITE_SCHEMA_VERSION = 1

// The token of `#invite=<43 base64url>` given exactly once, else null.
export function readInviteFragment(hash: string): string | null {
  const invites = new URLSearchParams(hash.replace(/^#/, '')).getAll('invite')
  return invites.length === 1 && BASE64URL_43.test(invites[0]) ? invites[0] : null
}

// A token replaces any held one; null removes it.
export function holdPendingInvite(token: string | null, now: number = Date.now()): void {
  try {
    if (token === null) {
      sessionStorage.removeItem(PENDING_INVITE_KEY)
    } else {
      sessionStorage.setItem(PENDING_INVITE_KEY, JSON.stringify({ v: PENDING_INVITE_SCHEMA_VERSION, t: token, at: now }))
    }
  } catch (e) {
    console.warn(`[pendingInvite] failed to store invite at "${PENDING_INVITE_KEY}":`, e)
  }
}

// The held token when live, else null; leaves the key alone.
export function peekPendingInvite(now: number = Date.now()): string | null {
  try {
    return liveToken(sessionStorage.getItem(PENDING_INVITE_KEY), now)
  } catch (e) {
    console.warn(`[pendingInvite] failed to read invite at "${PENDING_INVITE_KEY}":`, e)
    return null
  }
}

// One-shot: removes the key whatever it held, returns the token only when live.
export function consumePendingInvite(now: number = Date.now()): string | null {
  try {
    const raw = sessionStorage.getItem(PENDING_INVITE_KEY)
    sessionStorage.removeItem(PENDING_INVITE_KEY)
    return liveToken(raw, now)
  } catch (e) {
    console.warn(`[pendingInvite] failed to consume invite at "${PENDING_INVITE_KEY}":`, e)
    return null
  }
}

function liveToken(raw: string | null, now: number): string | null {
  if (raw == null) return null
  const p = JSON.parse(raw) as { v?: unknown; t?: unknown; at?: unknown } | null
  if (
    p == null ||
    p.v !== PENDING_INVITE_SCHEMA_VERSION ||
    typeof p.t !== 'string' ||
    !BASE64URL_43.test(p.t) ||
    typeof p.at !== 'number' ||
    Number.isNaN(p.at)
  ) {
    return null
  }
  return p.at <= now && now - p.at < SIGN_IN_STATE_TTL_MS ? p.t : null
}
