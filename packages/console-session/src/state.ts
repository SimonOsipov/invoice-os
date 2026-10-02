export type ConsoleTarget = 'ops' | 'support'
export type ConsoleOutcome = 'ready' | 'failed' | 'not-staff'

export const SIGN_IN_STATE_KEY = 'invoice-os.signInState'
const SIGN_IN_STATE_SCHEMA_VERSION = 1
export const SIGN_IN_STATE_TTL_MS = 10 * 60 * 1000
// The TTL minus landing's 9-minute hold, so a reused state is still live when landing posts it.
export const SIGN_IN_STATE_REUSE_MS = 60_000
export const BASE64URL_43 = /^[A-Za-z0-9_-]{43}$/

// Exactly one `handoff` param of exactly 43 base64url characters.
export function readHandoffCode(search: string): string | null {
  const all = new URLSearchParams(search).getAll('handoff')
  return all.length === 1 && BASE64URL_43.test(all[0]!) ? all[0]! : null
}

// 32 random bytes, unpadded base64url.
function mint(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(32))
  return btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')
}

function storedState(raw: string | null): { s: string; at: number } | null {
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
  return { s: p.s, at: p.at }
}

// Reuses a state minted under a minute ago, else mints and stores one. Storage failure yields an unstored state.
export function ensureSignInState(now: number = Date.now()): string {
  try {
    const p = storedState(sessionStorage.getItem(SIGN_IN_STATE_KEY))
    if (p && p.at <= now && now - p.at < SIGN_IN_STATE_REUSE_MS) return p.s
  } catch (e) {
    console.warn(`[console-session] failed to read state at "${SIGN_IN_STATE_KEY}":`, e)
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
    console.warn(`[console-session] failed to store state at "${SIGN_IN_STATE_KEY}":`, e)
  }
  return s
}

// One-shot: removes the key whatever it held, returns the state only when under the TTL.
export function consumeSignInState(now: number = Date.now()): string | null {
  try {
    const raw = sessionStorage.getItem(SIGN_IN_STATE_KEY)
    sessionStorage.removeItem(SIGN_IN_STATE_KEY)
    const p = storedState(raw)
    return p && p.at <= now && now - p.at < SIGN_IN_STATE_TTL_MS ? p.s : null
  } catch (e) {
    console.warn(`[console-session] failed to consume state at "${SIGN_IN_STATE_KEY}":`, e)
    return null
  }
}

export function landingSignInUrl(landing: string, state: string, target: ConsoleTarget, outcome?: ConsoleOutcome): string {
  return `${landing}/?state=${state}&console=${target}${outcome ? `&signin=${outcome}` : ''}`
}
