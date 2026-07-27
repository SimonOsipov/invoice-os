// Support Console session (the landing page is the single sign-in front door).
//
// Deliberately NOT access control, and not a security boundary — the same call
// frontend/ops-console/src/session.ts documents, and for the same reason: this console
// is entirely mock-data (data.tsx) with no backend to protect, so a fabricated
// localStorage entry is enough to get in. Real enforcement needs an operator identity the
// gateway will actually vouch for (its `authorize()` refuses every tenant-less token
// today, internal/gateway/gateway.go) — M7. This is the routing shape that lands into, so
// it becomes a swap rather than a rewrite.
//
// Mirrors the ops console's module one-for-one so the two stay one idiom, and keeps its
// own storage KEY: they are separate origins with unrelated identities, and a support
// operator is emphatically not a developer-console user.

export const SUPPORT_SESSION_KEY = 'invoice-os.support-session'
export const SUPPORT_SESSION_SCHEMA_VERSION = 1

// The landing's support-engineer persona (LANDING_PERSONAS 'support') is the only identity
// routed here. A record rather than a bare union so the membership check stays a lookup.
//
// `support` is THIS console's persona id. The developer console's is `developer` — it was
// called `support` until this story, which is why its sidebar rendered "Amara Okafor ·
// DEVELOPER · ADMIN" under a persona the landing labelled "Support officer" (the M4-20
// repositioning artefact). Adding a real support console forced the two apart: an id that
// names the role, and a `target` that names the Railway service.
export const SUPPORT_OPERATORS = {
  support: { name: 'Emeka Iroha', org: 'ASComply Operations' },
} as const

export type SupportOperatorId = keyof typeof SUPPORT_OPERATORS

export interface SupportSession {
  operator: SupportOperatorId
}

// Pure: a `?persona=` value from the landing hand-off -> a known operator, or null.
// Unknown values (a tenant persona, the developer persona, a typo, absent) are NOT a
// sign-in.
export function operatorFromParam(raw: string | null): SupportOperatorId | null {
  return raw !== null && Object.prototype.hasOwnProperty.call(SUPPORT_OPERATORS, raw) ? (raw as SupportOperatorId) : null
}

// Pure: parse + validate a persisted blob. Returns null for absent, unparseable, wrong
// schema version, or unknown operator. Every NON-absent failure warns (never console.error
// — the topology suite gates on a clean console); absent is the normal signed-out case.
export function parseStoredSupportSession(raw: string | null): SupportSession | null {
  if (raw == null) {
    return null
  }
  try {
    const parsed = JSON.parse(raw)
    const operator = parsed?.v === SUPPORT_SESSION_SCHEMA_VERSION ? operatorFromParam(parsed.operator ?? null) : null
    if (operator) {
      return { operator }
    }
    console.warn(`[support-session] ignoring corrupt persisted session at "${SUPPORT_SESSION_KEY}"`)
    return null
  } catch (e) {
    console.warn(`[support-session] failed to parse persisted session at "${SUPPORT_SESSION_KEY}":`, e)
    return null
  }
}

// The try/catch wraps the actual localStorage CALL, not a presence check: under native
// Node `globalThis.localStorage` exists but its methods throw (app-side finding C10.1).
export function loadSupportSession(): SupportSession | null {
  try {
    return parseStoredSupportSession(localStorage.getItem(SUPPORT_SESSION_KEY))
  } catch (e) {
    console.warn(`[support-session] failed to read persisted session at "${SUPPORT_SESSION_KEY}":`, e)
    return null
  }
}

export function saveSupportSession(session: SupportSession): void {
  try {
    localStorage.setItem(SUPPORT_SESSION_KEY, JSON.stringify({ v: SUPPORT_SESSION_SCHEMA_VERSION, operator: session.operator }))
  } catch (e) {
    console.warn(`[support-session] failed to persist session at "${SUPPORT_SESSION_KEY}":`, e)
  }
}

export function clearSupportSession(): void {
  try {
    localStorage.removeItem(SUPPORT_SESSION_KEY)
  } catch (e) {
    console.warn(`[support-session] failed to clear persisted session at "${SUPPORT_SESSION_KEY}":`, e)
  }
}

// Boot resolution. A `?persona=` deep link from the landing hand-off wins and is persisted
// (so the following reload doesn't need the param); otherwise fall back to what was stored.
// null means "not signed in" — the caller sends the user to the front door.
//
// The deep link wins over a stored session for the same reason the ops console does it:
// it is a FRESH sign-in decision made on the landing page moments ago.
export function resolveSupportBootSession(search: string): SupportSession | null {
  const fromLink = operatorFromParam(new URLSearchParams(search).get('persona'))
  if (fromLink) {
    const session = { operator: fromLink }
    saveSupportSession(session)
    return session
  }
  return loadSupportSession()
}
