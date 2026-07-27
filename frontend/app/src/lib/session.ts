// Session persistence module (M3-07-01). Makes the Platform app's mock session durable:
// a signed-in session is mirrored to localStorage so a reload / new tab / browser restart
// returns the user straight to their workspace, and a cleared session (Sign out or a 401)
// wipes the key. See the M3-07 story's Architect Decisions (a)-(c) for the corruption /
// version-guard contract.
//
// Persisted shape (localStorage[SESSION_KEY]):
//   { v: SESSION_SCHEMA_VERSION, personaId: PersonaId, token: string|null, me: Me|null, verified: boolean }
// `persona` is stored by id only and rehydrated from APP_PERSONAS — persona definitions
// (name/subject/tenantId/role) are canonical in code, so persisting only the id avoids
// stale-persona drift and reduces the corruption guard to a simple membership check.

import { APP_PERSONAS, type Session } from '../auth'

export const SESSION_KEY = 'invoice-os.session'
export const SESSION_SCHEMA_VERSION = 1

// Serialize to the minimal persisted record — persona is stored by id only (never whole).
export function serializeSession(session: Session): string {
  return JSON.stringify({
    v: SESSION_SCHEMA_VERSION,
    personaId: session.persona.id,
    token: session.token,
    me: session.me,
    verified: session.verified,
  })
}

// Parse + validate a persisted blob back into a Session, rebuilding `persona` from
// APP_PERSONAS. Returns null (→ fall back to SignIn) for: absent, JSON.parse failure,
// wrong schema version, unknown personaId, or a wrong-typed field. Every NON-absent
// failure logs console.warn (never console.error — the topology no-error gate); an
// absent blob is the normal "not signed in" case and warns nothing.
export function parseStoredSession(raw: string | null): Session | null {
  if (raw == null) {
    return null
  }
  try {
    const parsed = JSON.parse(raw)
    if (
      parsed != null &&
      parsed.v === SESSION_SCHEMA_VERSION &&
      Object.prototype.hasOwnProperty.call(APP_PERSONAS, parsed.personaId) &&
      (typeof parsed.token === 'string' || parsed.token === null) &&
      typeof parsed.verified === 'boolean' &&
      (parsed.me === null || (typeof parsed.me === 'object' && parsed.me !== null))
    ) {
      return {
        persona: APP_PERSONAS[parsed.personaId as keyof typeof APP_PERSONAS],
        token: parsed.token,
        me: parsed.me,
        verified: parsed.verified,
      }
    }
    console.warn(`[session] ignoring corrupt persisted session at "${SESSION_KEY}"`)
    return null
  } catch (e) {
    console.warn(`[session] failed to parse persisted session at "${SESSION_KEY}":`, e)
    return null
  }
}

// Read the persisted session. The try/catch wraps the actual localStorage.getItem CALL
// (not a presence check): under native Node `globalThis.localStorage` is present but its
// methods throw TypeError, so only wrapping the call site degrades cleanly (finding C10.1).
export function loadSession(): Session | null {
  try {
    return parseStoredSession(localStorage.getItem(SESSION_KEY))
  } catch (e) {
    console.warn(`[session] failed to read persisted session at "${SESSION_KEY}":`, e)
    return null
  }
}

// Mirror a session to storage. Never throws — a throwing setItem (quota, native-Node
// method, private-mode) degrades to console.warn so the app stays usable.
export function saveSession(session: Session): void {
  try {
    localStorage.setItem(SESSION_KEY, serializeSession(session))
  } catch (e) {
    console.warn(`[session] failed to persist session at "${SESSION_KEY}":`, e)
  }
}

// Remove the persisted session (Sign out / 401). Never throws.
export function clearSession(): void {
  try {
    localStorage.removeItem(SESSION_KEY)
  } catch (e) {
    console.warn(`[session] failed to clear persisted session at "${SESSION_KEY}":`, e)
  }
}

// The `?persona=` deep-link guard: auto-sign-in fires ONLY when boot produced no session
// (a rehydrated session wins over a stale deep-link param) AND the param is a known persona.
export function shouldAutoSignIn(bootSession: Session | null, personaParam: string | null): boolean {
  return bootSession === null && (personaParam === 'firm' || personaParam === 'inhouse')
}

// Read a JWT's `exp` WITHOUT verifying the signature. The browser cannot verify one — the
// gateway is the only authority — so this is a courtesy check, not a security control: it
// exists so a reload on a token the gateway will certainly reject doesn't boot into the
// workspace just to bounce straight back out of it behind a 401 card.
//
// Returns false for anything it cannot read a numeric `exp` out of (null token, opaque
// token, malformed payload). Never invent an expiry a token does not state — and note a
// token can be dead while UNEXPIRED: the dev mock issuer generates a fresh signing key and
// kid on every gateway start (internal/platform/auth/mockissuer.go), so a restart orphans
// every outstanding token. Those are caught by the 401 handler, which stays the real
// backstop. Same comparison the gateway makes (internal/platform/auth/claims.go:58).
export function isTokenExpired(token: string | null, nowMs: number = Date.now()): boolean {
  if (!token) {
    return false
  }
  const payload = token.split('.')[1]
  if (!payload) {
    return false
  }
  try {
    const b64 = payload.replace(/-/g, '+').replace(/_/g, '/')
    const claims = JSON.parse(atob(b64.padEnd(Math.ceil(b64.length / 4) * 4, '=')))
    return typeof claims?.exp === 'number' && nowMs >= claims.exp * 1000
  } catch {
    return false
  }
}

// Boot-time session resolution: a stored session whose token has already expired is NOT a
// session. Entering the workspace on one only buys a dashboard that 401s a moment later.
//
// This deliberately does NOT distinguish "expired" from "never signed in". It used to,
// because the two left the app by different doors — expired to the landing page, absent to
// the in-app picker. Now that the landing page is the single front door, every sessionless
// visit goes there (App.tsx), so the distinction had exactly one consumer and no remaining
// behavioural difference. Keeping the flag would have meant two mechanisms for one outcome.
export function resolveBootSession(now: number = Date.now()): Session | null {
  const session = loadSession()
  return session !== null && isTokenExpired(session.token, now) ? null : session
}
