// Session persistence module (M3-07-01). STUB — the executor implements the bodies
// next; every export below throws so the RED specs in session.test.ts fail on a
// thrown/assertion mismatch, not an import or type error.
//
// Persisted shape (localStorage[SESSION_KEY]):
//   { v: SESSION_SCHEMA_VERSION, personaId: PersonaId, token: string|null, me: Me|null, verified: boolean }
// `persona` is stored by id only and rehydrated from APP_PERSONAS — see the M3-07
// story's Architect Decisions (a)-(c) for the corruption/version-guard contract.

// Only `Session` is referenced by this stub's signatures. `APP_PERSONAS`/`Persona`/`Me`
// are used by the real serialize/parse implementation (next, not this stub) to
// rebuild `persona` by id and narrow `me` — importing them here unused would fail
// noUnusedLocals under this app's strict tsconfig.
import type { Session } from '../auth'

export const SESSION_KEY = 'invoice-os.session'
export const SESSION_SCHEMA_VERSION = 1

export function serializeSession(_session: Session): string {
  throw new Error('not implemented')
}

export function parseStoredSession(_raw: string | null): Session | null {
  throw new Error('not implemented')
}

export function loadSession(): Session | null {
  throw new Error('not implemented')
}

export function saveSession(_session: Session): void {
  throw new Error('not implemented')
}

export function clearSession(): void {
  throw new Error('not implemented')
}

export function shouldAutoSignIn(_bootSession: Session | null, _personaParam: string | null): boolean {
  throw new Error('not implemented')
}
