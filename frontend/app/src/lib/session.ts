// Session persistence: a signed-in session is mirrored to localStorage so a reload or new
// tab returns the user to their workspace; a cleared session (Sign out or a 401) wipes the key.
//
// Persisted shape (localStorage[SESSION_KEY]):
//   { v: SESSION_SCHEMA_VERSION, personaId: PersonaId, token: string|null, me: Me|null, verified: boolean, handoff?: true }
//   a hand-off record may add the pair { refresh_token: string, received_at: number } (epoch ms at receipt).
// `persona` is stored by id only and rehydrated from APP_PERSONAS — persona definitions
// (name/subject/tenantId/role) are canonical in code, so persisting only the id avoids
// stale-persona drift and reduces the corruption guard to a simple membership check.
// A hand-off record (`handoff: true`) rebuilds its persona from `me` instead; its mode comes
// from `me.tenant.kind` and its card identity from `me.user`. A persona record keeps its APP_PERSONAS mode.

import { APP_PERSONAS, type Me, type Persona, type Session, type TenantKind } from '../auth'
import type { Mode } from '../types'
import { memberInitials } from './members'

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
    // Written only when set, so a persona record stays byte-identical.
    ...(session.handoff ? { handoff: true } : {}),
    // Hand-off only, so serialize and parse stay symmetric.
    ...(session.handoff && session.renewal
      ? { refresh_token: session.renewal.refreshToken, received_at: session.renewal.receivedAt }
      : {}),
  })
}

const MODE_BY_KIND: Record<TenantKind, Mode> = { firm: 'firm', in_house: 'inhouse' }

// A real session's mode is tenants.kind; APP_PERSONAS modes serve the demo door only.
export function handoffPersona(me: Me): Persona {
  // name/initials/email blank: the card reads me.user, never the firm persona's stand-in.
  return { ...APP_PERSONAS.firm, name: '', initials: '', email: '', mode: MODE_BY_KIND[me.tenant.kind], subject: me.user.id, tenantId: me.tenant.id }
}

// The pair is optional; when present it must be complete, well-typed and on a hand-off record.
function renewalPairOk(p: { handoff?: unknown; refresh_token?: unknown; received_at?: unknown }): boolean {
  if (p.refresh_token === undefined && p.received_at === undefined) {
    return true
  }
  return (
    p.handoff === true &&
    typeof p.refresh_token === 'string' &&
    p.refresh_token !== '' &&
    typeof p.received_at === 'number' &&
    Number.isFinite(p.received_at)
  )
}

// typeof first: an array or an object with toString would pass hasOwnProperty by coercion.
export function isHandoffMe(me: unknown): me is Me {
  const m = me as { user?: { id?: unknown }; tenant?: { id?: unknown; kind?: unknown } } | null
  const kind = m?.tenant?.kind
  return (
    typeof m?.user?.id === 'string' &&
    typeof m?.tenant?.id === 'string' &&
    typeof kind === 'string' &&
    Object.prototype.hasOwnProperty.call(MODE_BY_KIND, kind)
  )
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
      (parsed.me === null || (typeof parsed.me === 'object' && parsed.me !== null)) &&
      (parsed.handoff !== true || isHandoffMe(parsed.me)) &&
      renewalPairOk(parsed)
    ) {
      if (parsed.handoff === true) {
        return {
          persona: handoffPersona(parsed.me),
          token: parsed.token,
          me: parsed.me,
          verified: parsed.verified,
          handoff: true,
          ...(parsed.refresh_token !== undefined
            ? { renewal: { refreshToken: parsed.refresh_token, receivedAt: parsed.received_at } }
            : {}),
        }
      }
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

// Unverified read of a three-part JWT's payload; null for anything else.
export function decodeJwtPayload(token: string | null): Record<string, unknown> | null {
  const parts = token?.split('.')
  if (parts?.length !== 3 || !parts[1]) {
    return null
  }
  try {
    const b64 = parts[1].replace(/-/g, '+').replace(/_/g, '/')
    // atob yields Latin-1; decode the bytes as UTF-8 so non-ASCII names survive.
    const bytes = Uint8Array.from(atob(b64.padEnd(Math.ceil(b64.length / 4) * 4, '=')), (c) => c.charCodeAt(0))
    const claims: unknown = JSON.parse(new TextDecoder().decode(bytes))
    return claims !== null && typeof claims === 'object' && !Array.isArray(claims) ? (claims as Record<string, unknown>) : null
  } catch {
    return null
  }
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
  const exp = decodeJwtPayload(token)?.exp
  return typeof exp === 'number' && nowMs >= exp * 1000
}

// Boot-time session resolution: an expired token with no renewal is NOT a session (the
// workspace would only 401). One with a renewal is kept: its refresh token outlives it.
//
// This deliberately does NOT distinguish "expired" from "never signed in". It used to,
// because the two left the app by different doors — expired to the landing page, absent to
// the in-app picker. Now that the landing page is the single front door, every sessionless
// visit goes there (App.tsx), so the distinction had exactly one consumer and no remaining
// behavioural difference. Keeping the flag would have meant two mechanisms for one outcome.
export function resolveBootSession(now: number = Date.now()): Session | null {
  const session = loadSession()
  return session !== null && !session.renewal && isTokenExpired(session.token, now) ? null : session
}

// Trimmed text, or null for blank and non-string values.
function presentText(v: unknown): string | null {
  return typeof v === 'string' && v.trim() !== '' ? v.trim() : null
}

// A hand-off session names the person from /me (display name, else email, else nothing);
// a persona session keeps its persona.
export function cardIdentity(session: Session): { name: string; initials: string } {
  if (!session.handoff) return { name: session.persona.name, initials: session.persona.initials }
  const displayName = presentText(session.me?.user.display_name)
  const email = presentText(session.me?.user.email)
  return { name: displayName ?? email ?? '', initials: memberInitials(displayName, email, '') }
}
