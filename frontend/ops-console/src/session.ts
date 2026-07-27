// Ops Console session (the landing page is the single sign-in front door).
//
// The console had NO session concept at all: anyone with the URL saw everything, because
// the landing routed here with a bare navigation and nothing on this side recorded that a
// sign-in had happened. This adds the minimum needed to make "not signed in -> go to the
// front door" expressible.
//
// Deliberately NOT access control, and not a security boundary. The console is still
// entirely mock-data (data.tsx) with no backend to protect, and a fabricated localStorage
// entry is enough to get in. Real enforcement needs the console to consume a verified
// token (M7) on top of real identities (M8) — this is the routing shape those land into,
// so they become a swap rather than a rewrite.
//
// Mirrors frontend/app/src/lib/session.ts (versioned envelope, membership-checked id,
// warn-not-throw on corruption) so the two apps stay one idiom, and keeps its own storage
// KEY because the two SPAs are separate origins with unrelated identities.

export const OPS_SESSION_KEY = 'invoice-os.ops-session'
export const OPS_SESSION_SCHEMA_VERSION = 1

// The landing's developer persona (LANDING_PERSONAS 'developer') is the only identity
// routed here — the tenant-scoped firm/inhouse personas open the Platform app, and the
// cross-tenant support persona opens the Support Console. A record rather than a bare
// union so the membership check below stays a lookup.
//
// This key was `support` (name "Amara Okoye") until the Support Console shipped, which
// made the label actively wrong: this console renders "Amara Okafor · DEVELOPER · ADMIN"
// for org "Zephyr Pay" (Sidebar.tsx) and is the customer-facing developer platform, not an
// internal support tool. The name here now matches the one the sidebar draws.
export const OPS_OPERATORS = {
  developer: { name: 'Amara Okafor', org: 'Zephyr Pay' },
} as const

export type OpsOperatorId = keyof typeof OPS_OPERATORS

export interface OpsSession {
  operator: OpsOperatorId
}

// Pure: a `?persona=` value from the landing hand-off -> a known operator, or null.
// Unknown values (a tenant persona, a typo, absent) are NOT a sign-in.
export function operatorFromParam(raw: string | null): OpsOperatorId | null {
  return raw !== null && Object.prototype.hasOwnProperty.call(OPS_OPERATORS, raw) ? (raw as OpsOperatorId) : null
}

// Pure: parse + validate a persisted blob. Returns null for absent, unparseable, wrong
// schema version, or unknown operator. Every NON-absent failure warns (never console.error
// — the topology suite gates on a clean console); absent is the normal signed-out case.
export function parseStoredOpsSession(raw: string | null): OpsSession | null {
  if (raw == null) {
    return null
  }
  try {
    const parsed = JSON.parse(raw)
    const operator = parsed?.v === OPS_SESSION_SCHEMA_VERSION ? operatorFromParam(parsed.operator ?? null) : null
    if (operator) {
      return { operator }
    }
    console.warn(`[ops-session] ignoring corrupt persisted session at "${OPS_SESSION_KEY}"`)
    return null
  } catch (e) {
    console.warn(`[ops-session] failed to parse persisted session at "${OPS_SESSION_KEY}":`, e)
    return null
  }
}

// The try/catch wraps the actual localStorage CALL, not a presence check: under native
// Node `globalThis.localStorage` exists but its methods throw (app-side finding C10.1).
export function loadOpsSession(): OpsSession | null {
  try {
    return parseStoredOpsSession(localStorage.getItem(OPS_SESSION_KEY))
  } catch (e) {
    console.warn(`[ops-session] failed to read persisted session at "${OPS_SESSION_KEY}":`, e)
    return null
  }
}

export function saveOpsSession(session: OpsSession): void {
  try {
    localStorage.setItem(OPS_SESSION_KEY, JSON.stringify({ v: OPS_SESSION_SCHEMA_VERSION, operator: session.operator }))
  } catch (e) {
    console.warn(`[ops-session] failed to persist session at "${OPS_SESSION_KEY}":`, e)
  }
}

export function clearOpsSession(): void {
  try {
    localStorage.removeItem(OPS_SESSION_KEY)
  } catch (e) {
    console.warn(`[ops-session] failed to clear persisted session at "${OPS_SESSION_KEY}":`, e)
  }
}

// Boot resolution. A `?persona=` deep link from the landing hand-off wins and is persisted
// (so the following reload doesn't need the param); otherwise fall back to what was stored.
// null means "not signed in" — the caller sends the user to the front door.
//
// The deep link wins over a stored session on purpose: it is a FRESH sign-in decision made
// on the landing page moments ago, whereas the app-side rule is the opposite (a rehydrated
// session beats a stale param) because there the param is a leftover in an already-open
// workspace's URL. Same param, different question.
export function resolveOpsBootSession(search: string): OpsSession | null {
  const fromLink = operatorFromParam(new URLSearchParams(search).get('persona'))
  if (fromLink) {
    const session = { operator: fromLink }
    saveOpsSession(session)
    return session
  }
  return loadOpsSession()
}
