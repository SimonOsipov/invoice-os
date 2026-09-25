// App-minted sign-in state: binds a hand-off code to this tab. Mirrors lib/deepLink.ts's
// versioned-blob, warn-never-throw conventions. Never reads window.location.
import { landingBase } from '../auth'

export const SIGN_IN_STATE_KEY = 'invoice-os.signInState'
export const SIGN_IN_STATE_SCHEMA_VERSION = 1
export const SIGN_IN_STATE_TTL_MS = 10 * 60 * 1000

export type SignInOutcome = 'ready' | 'failed' | 'no-workspace'

// 32 bytes in unpadded base64url: the shape of a sign-in state and a hand-off code.
export const BASE64URL_43 = /^[A-Za-z0-9_-]{43}$/

// 32 random bytes, base64url without padding: 43 characters.
function mint(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(32))
  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')
}

// The stored state when the blob is well-formed and under the TTL, else null.
function liveState(raw: string | null, now: number): string | null {
  if (raw == null) return null
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  const p = parsed as { v?: unknown; s?: unknown; at?: unknown } | null
  if (
    p == null ||
    p.v !== SIGN_IN_STATE_SCHEMA_VERSION ||
    typeof p.s !== 'string' ||
    !BASE64URL_43.test(p.s) ||
    typeof p.at !== 'number' ||
    Number.isNaN(p.at)
  ) {
    return null
  }
  return p.at <= now && now - p.at < SIGN_IN_STATE_TTL_MS ? p.s : null
}

// Reuses a live state, else mints and stores one. Storage failure yields an unstored state.
export function ensureSignInState(now: number = Date.now()): string {
  try {
    const s = liveState(sessionStorage.getItem(SIGN_IN_STATE_KEY), now)
    if (s) return s
  } catch (e) {
    console.warn(`[signInState] failed to store state at "${SIGN_IN_STATE_KEY}":`, e)
    return mint()
  }
  return mintSignInState(now)
}

// Mints and stores a new state, replacing any held one.
export function mintSignInState(now: number = Date.now()): string {
  const s = mint()
  try {
    sessionStorage.setItem(SIGN_IN_STATE_KEY, JSON.stringify({ v: SIGN_IN_STATE_SCHEMA_VERSION, s, at: now }))
  } catch (e) {
    console.warn(`[signInState] failed to store state at "${SIGN_IN_STATE_KEY}":`, e)
  }
  return s
}

// One-shot: removes the key whatever it held, returns the state only when live.
export function consumeSignInState(now: number = Date.now()): string | null {
  try {
    const raw = sessionStorage.getItem(SIGN_IN_STATE_KEY)
    sessionStorage.removeItem(SIGN_IN_STATE_KEY)
    return liveState(raw, now)
  } catch (e) {
    console.warn(`[signInState] failed to consume state at "${SIGN_IN_STATE_KEY}":`, e)
    return null
  }
}

export function landingSignInUrl(state: string, outcome?: SignInOutcome): string | null {
  const base = landingBase()
  if (!base) return null
  return `${base}/?state=${state}${outcome ? `&signin=${outcome}` : ''}`
}
