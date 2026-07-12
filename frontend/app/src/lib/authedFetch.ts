// App-side 401 authed-fetch seam (M3-07-02, Decision (f)).
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
import { ApiError, apiFetch, type ApiFetchOptions } from '@invoice-os/api-client'

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
