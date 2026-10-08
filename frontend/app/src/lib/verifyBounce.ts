// A confirm-link token bounced from landing, and the one-shot marker that a confirm is pending.
// Mirrors pendingInvite.ts's versioned-blob, warn-never-throw conventions. Never reads window.location.
import { landingBase } from '../auth'
import { peekSignInState, SIGN_IN_STATE_TTL_MS } from './signInState'

export const PENDING_VERIFY_KEY = 'invoice-os.pendingVerify'
const PENDING_VERIFY_SCHEMA_VERSION = 1
const VERIFY_TOKEN = /^[A-Za-z0-9_-]{1,256}$/

// The token of `#token=<T>` given exactly once, else null.
export function readVerifyFragment(hash: string): string | null {
  const tokens = new URLSearchParams(hash.replace(/^#/, '')).getAll('token')
  return tokens.length === 1 && VERIFY_TOKEN.test(tokens[0]) ? tokens[0] : null
}

export function gatewayVerifyUrl(base: string, token: string, state: string): string {
  return `${base}/auth/verify?token=${encodeURIComponent(token)}&type=signup&state=${state}`
}

export function landingVerifyFailedUrl(): string | null {
  const base = landingBase()
  return base ? `${base}/?verify=failed` : null
}

// Binds the marker to the sign-in state stored now, so a later sign-in that mints a new state orphans it.
export function holdPendingVerify(now: number = Date.now()): void {
  try {
    sessionStorage.setItem(
      PENDING_VERIFY_KEY,
      JSON.stringify({ v: PENDING_VERIFY_SCHEMA_VERSION, at: now, s: peekSignInState(now) }),
    )
  } catch (e) {
    console.warn(`[verifyBounce] failed to store marker at "${PENDING_VERIFY_KEY}":`, e)
  }
}

// True when a live marker bound to the stored sign-in state is held; leaves the key alone.
export function peekPendingVerify(now: number = Date.now()): boolean {
  try {
    return liveMarker(sessionStorage.getItem(PENDING_VERIFY_KEY), now) && boundToStoredState(now)
  } catch (e) {
    console.warn(`[verifyBounce] failed to read marker at "${PENDING_VERIFY_KEY}":`, e)
    return false
  }
}

// One-shot: removes the key whatever it held, returns true only when it was live.
export function consumePendingVerify(now: number = Date.now()): boolean {
  try {
    const raw = sessionStorage.getItem(PENDING_VERIFY_KEY)
    sessionStorage.removeItem(PENDING_VERIFY_KEY)
    return liveMarker(raw, now)
  } catch (e) {
    console.warn(`[verifyBounce] failed to consume marker at "${PENDING_VERIFY_KEY}":`, e)
    return false
  }
}

function boundToStoredState(now: number): boolean {
  const p = JSON.parse(sessionStorage.getItem(PENDING_VERIFY_KEY) ?? 'null') as { s?: unknown } | null
  const stored = peekSignInState(now)
  return stored !== null && p?.s === stored
}

function liveMarker(raw: string | null, now: number): boolean {
  if (raw == null) return false
  const p = JSON.parse(raw) as { v?: unknown; at?: unknown } | null
  return (
    p != null &&
    p.v === PENDING_VERIFY_SCHEMA_VERSION &&
    typeof p.at === 'number' &&
    p.at <= now &&
    now - p.at < SIGN_IN_STATE_TTL_MS
  )
}
