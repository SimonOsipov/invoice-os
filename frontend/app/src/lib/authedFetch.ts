// App-side 401 authed-fetch seam (M3-07-02, Decision (f)).
//
// Contract (see the M3-07 story's Architecture > Components and Decision (f)):
// - `isUnauthorized(e)` is a pure predicate: true iff `e instanceof ApiError && e.kind
//   === 'http' && e.status === 401`.
// - `createAuthedFetch(getToken, onUnauthorized)` returns `authedFetch<T>(url, opts?)`
//   that calls `apiFetch<T>(url, { ...opts, token })` with the getter's token (awaited only
//   when it is a promise), invokes `onUnauthorized()` iff `isUnauthorized(caught)`, and
//   always rethrows.
// No `@invoice-os/api-client` **package** change (Constraint); this file only consumes
// its exports. Instantiated in App.tsx's `Workspace` via `makeAuthedFetch` (M3-08-03).
//
// AUDIT-10-07 added the suspension seam alongside it, same shape: `isSuspended(e)` plus an
// `onSuspended` callback, fired instead of `onUnauthorized`, always rethrowing.
//
import { ApiError, apiFetch, type ApiFetchOptions } from '@invoice-os/api-client'
import type { Session } from '../auth'
import type { AuthedFetch } from './portfolio'

// The 403 body every gated handler returns for a caller whose membership in the requested
// workspace is not active (AUDIT-10). Written down once in Go as db.NotActiveMemberMessage;
// this hand-maintained copy is pinned to it by wireMirrors.test.ts.
export const NOT_ACTIVE_MEMBER_MESSAGE = 'your membership in this workspace is not active'

// A plain token must reach the transport synchronously: importApi's XHR tests respond in the same tick.
export function isPromiseLike<T>(v: T | PromiseLike<T>): v is PromiseLike<T> {
  return typeof (v as { then?: unknown } | null)?.then === 'function'
}

export function isUnauthorized(e: unknown): boolean {
  return e instanceof ApiError && e.kind === 'http' && e.status === 401
}

// The message, not the bare 403: ErrNoMembership and ErrNotPermitted are 403 too, so a
// status-only predicate would tell an unauthorized preparer their membership was suspended.
export function isSuspended(e: unknown): boolean {
  return (
    e instanceof ApiError &&
    e.kind === 'http' &&
    e.status === 403 &&
    (e.body as { error?: unknown } | undefined)?.error === NOT_ACTIVE_MEMBER_MESSAGE
  )
}

// onSuspended is optional so the ~30 ctx-fixture call sites stay two-argument; both live
// construction sites pass it (App.tsx, pinned by App.suspended.test.tsx).
export function createAuthedFetch(
  getToken: () => string | null | Promise<string | null>,
  onUnauthorized: () => void,
  onSuspended?: () => void,
): <T>(url: string, opts?: ApiFetchOptions) => Promise<T> {
  return async function authedFetch<T>(url: string, opts?: ApiFetchOptions): Promise<T> {
    try {
      const token = getToken()
      return await apiFetch<T>(url, { ...opts, token: isPromiseLike(token) ? await token : token })
    } catch (e) {
      // A SessionEndedError is not an ApiError, so neither seam fires.
      if (isUnauthorized(e)) onUnauthorized()
      else if (isSuspended(e)) onSuspended?.()
      throw e
    }
  }
}

// The app-side factory `Workspace` instantiates (portfolio.authedfetch.test.ts A1-A6).
// `freshToken` (the renewer's) is the getter when given; otherwise `session.token` is read
// at call time, never captured (A5).
export function makeAuthedFetch(
  session: Session,
  onUnauthorized: () => void,
  onSuspended?: () => void,
  freshToken?: () => string | null | Promise<string | null>,
): AuthedFetch {
  return createAuthedFetch(freshToken ?? (() => session.token), onUnauthorized, onSuspended)
}
