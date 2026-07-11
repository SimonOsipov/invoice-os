// App-side 401 authed-fetch seam (M3-07-02, Decision (f)). STUB — the executor
// implements the bodies next; every export below throws so the RED specs in
// authedFetch.test.ts fail on a thrown/assertion mismatch, not an import or type error.
//
// Contract (see the M3-07 story's Architecture > Components and Decision (f)):
// - `isUnauthorized(e)` is a pure predicate: true iff `e instanceof ApiError && e.kind
//   === 'http' && e.status === 401`.
// - `createAuthedFetch(getToken, onUnauthorized)` returns `authedFetch<T>(url, opts?)`
//   that calls `apiFetch<T>(url, { ...opts, token: getToken() })`, invokes
//   `onUnauthorized()` iff `isUnauthorized(caught)`, and always rethrows.
// No `@invoice-os/api-client` **package** change (Constraint); this file only consumes
// its exports. Not instantiated in App.tsx yet — no in-app caller exists until M3-08/09.
//
// `apiFetch`/`ApiError` are referenced only by the real implementation (next, not this
// stub) — importing them unused here would fail noUnusedLocals under this app's strict
// tsconfig (mirrors the session.ts stub's rationale, M3-07-01). Only the type-only
// `ApiFetchOptions` is referenced by this stub's signature.
import type { ApiFetchOptions } from '@invoice-os/api-client'

export function isUnauthorized(_e: unknown): boolean {
  throw new Error('not implemented')
}

export function createAuthedFetch(
  _getToken: () => string | null,
  _onUnauthorized: () => void,
): <T>(url: string, opts?: ApiFetchOptions) => Promise<T> {
  return function authedFetch<T>(_url: string, _opts?: ApiFetchOptions): Promise<T> {
    throw new Error('not implemented')
  }
}
