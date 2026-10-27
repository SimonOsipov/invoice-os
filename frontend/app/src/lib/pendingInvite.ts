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

const MAX_NAME_CHARS = 200
// Mirror NAME_REQUIRED and NAME_TOO_LONG in frontend/landing/src/register.ts.
const NAME_REQUIRED = 'Enter your name.'
const NAME_TOO_LONG = `Use ${MAX_NAME_CHARS} characters or fewer.`

// Trims as the API does: JS trim() plus U+0085, which Go's unicode.IsSpace strips and JS does not.
export function trimName(v: string): string {
  let s = v.trim()
  while (s.startsWith('\u0085') || s.endsWith('\u0085')) s = s.replace(/^\u0085/, '').replace(/\u0085$/, '').trim()
  return s
}

// Mirrors checkName in internal/tenancy/tenancy.go: trimmed, 1 to 200 characters (maxNameChars), no NUL.
export const validName = (v: string) => {
  const t = trimName(v)
  return t !== '' && [...t].length <= MAX_NAME_CHARS && !t.includes('\0')
}

// Why a name is invalid, as the landing invite page says it; a NUL gives no message.
export function nameError(v: string): string | null {
  if (v.includes('\0')) return null
  const n = [...trimName(v)].length
  return n === 0 ? NAME_REQUIRED : n > MAX_NAME_CHARS ? NAME_TOO_LONG : null
}

// The one `name` fragment value, trimmed and valid, else null.
export function readInviteName(hash: string): string | null {
  const names = new URLSearchParams(hash.replace(/^#/, '')).getAll('name')
  const name = names.length === 1 ? trimName(names[0]) : ''
  return validName(name) ? name : null
}

// A token replaces any held one; null removes it. The name rides in the same blob.
export function holdPendingInvite(token: string | null, now: number = Date.now(), name: string | null = null): void {
  try {
    if (token === null) {
      sessionStorage.removeItem(PENDING_INVITE_KEY)
    } else {
      sessionStorage.setItem(PENDING_INVITE_KEY, JSON.stringify({ v: PENDING_INVITE_SCHEMA_VERSION, t: token, at: now, ...(name === null ? {} : { n: name }) }))
    }
  } catch (e) {
    console.warn(`[pendingInvite] failed to store invite at "${PENDING_INVITE_KEY}":`, e)
  }
}

// The held token when live, else null; leaves the key alone.
export function peekPendingInvite(now: number = Date.now()): string | null {
  try {
    return liveInvite(sessionStorage.getItem(PENDING_INVITE_KEY), now)?.token ?? null
  } catch (e) {
    console.warn(`[pendingInvite] failed to read invite at "${PENDING_INVITE_KEY}":`, e)
    return null
  }
}

// One-shot: removes the key whatever it held, returns the invite only when live.
export function consumePendingInvite(now: number = Date.now()): { token: string; name: string | null } | null {
  try {
    const raw = sessionStorage.getItem(PENDING_INVITE_KEY)
    sessionStorage.removeItem(PENDING_INVITE_KEY)
    return liveInvite(raw, now)
  } catch (e) {
    console.warn(`[pendingInvite] failed to consume invite at "${PENDING_INVITE_KEY}":`, e)
    return null
  }
}

function liveInvite(raw: string | null, now: number): { token: string; name: string | null } | null {
  if (raw == null) return null
  const p = JSON.parse(raw) as { v?: unknown; t?: unknown; at?: unknown; n?: unknown } | null
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
  if (!(p.at <= now && now - p.at < SIGN_IN_STATE_TTL_MS)) return null
  return { token: p.t, name: typeof p.n === 'string' ? p.n : null }
}
