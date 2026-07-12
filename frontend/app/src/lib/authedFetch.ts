// App-side 401 authed-fetch seam (M3-07-02, Decision (f)).
//
// Contract (see the M3-07 story's Architecture > Components and Decision (f)):
// - `isUnauthorized(e)` is a pure predicate: true iff `e instanceof ApiError && e.kind
//   === 'http' && e.status === 401`.
// - `createAuthedFetch(getToken, onUnauthorized)` returns `authedFetch<T>(url, opts?)`
//   that calls `apiFetch<T>(url, { ...opts, token: getToken() })`, invokes
//   `onUnauthorized()` iff `isUnauthorized(caught)`, and always rethrows.
// No `@invoice-os/api-client` **package** change (Constraint); this file only consumes
// its exports. Instantiated in App.tsx's `Workspace` via `makeAuthedFetch` (M3-08-03).
//
import { ApiError, apiFetch, type ApiFetchOptions } from '@invoice-os/api-client'
import type { Session } from '../auth'
import type { AuthedFetch } from './portfolio'

export function isUnauthorized(e: unknown): boolean {
  return e instanceof ApiError && e.kind === 'http' && e.status === 401
}

export function createAuthedFetch(
  getToken: () => string | null,
  onUnauthorized: () => void,
): <T>(url: string, opts?: ApiFetchOptions) => Promise<T> {
  return async function authedFetch<T>(url: string, opts?: ApiFetchOptions): Promise<T> {
    try {
      return await apiFetch<T>(url, { ...opts, token: getToken() })
    } catch (e) {
      if (isUnauthorized(e)) onUnauthorized()
      throw e
    }
  }
}

// makeAuthedFetch(session, onSignOut) — the app-side factory `Workspace` instantiates
// (M3-08-03, task-58), covered by the live-caller specs in portfolio.authedfetch.test.ts
// (A1-A6).
//
// Contract: a thin pure wrapper around `createAuthedFetch` that closes over `session`,
// reading `session.token` at CALL time (`() => session.token`), not construction time —
// a live re-sign-in swaps the `Session` object under React state, so a captured token
// snapshot would go stale (A5) — and forwards `onSignOut` unchanged as the
// `onUnauthorized` callback (A1/A3). This narrows the Obsidian M3-08 story's [A-c]
// code-review-only residual to just `Workspace`'s `useMemo` forwarding `session` +
// `onSignOut` into this factory — the token-read + onUnauthorized wiring itself becomes
// node-testable here, through the live `listEntities`/`createEntity` callers.
export function makeAuthedFetch(session: Session, onSignOut: () => void): AuthedFetch {
  return createAuthedFetch(() => session.token, onSignOut)
}
