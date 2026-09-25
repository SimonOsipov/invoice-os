// Landing sign-in client (AUTH-05 D12, D25).
import { ApiError, apiFetch, gatewayBase } from '@invoice-os/api-client/client'

import { appBase } from './auth'

const STATE_RE = /^[A-Za-z0-9_-]{43}$/

const INCORRECT = 'Email or password is incorrect.'
const UNVERIFIED = 'Verify your email address first. The link is in your inbox.'
const THROTTLED = 'Too many attempts. Try again in a minute.'
const UNAVAILABLE = 'Sign-in is unavailable right now. Try again shortly.'

export function handoffUrl(code: string): string | null {
  const base = appBase()
  return base ? `${base}?handoff=${encodeURIComponent(code)}` : null
}

export function signInConfigured(): boolean {
  return gatewayBase() !== null && appBase() !== null
}

// An unset gateway and a 200 without a code both throw 'malformed', which maps to UNAVAILABLE.
export async function signInWithPassword(email: string, password: string, state: string): Promise<string> {
  const base = gatewayBase()
  if (!base) throw new ApiError('malformed', 'gateway not configured')
  const body = await apiFetch<{ code?: unknown } | null>(`${base}/auth/sign-in`, {
    method: 'POST',
    body: { email, password, state },
  })
  const code = body?.code
  if (typeof code !== 'string' || !code) throw new ApiError('malformed', 'sign-in response has no code', 200)
  return code
}

export function signInErrorMessage(err: unknown): string {
  if (err instanceof ApiError && err.kind === 'http') {
    if (err.status === 401) return INCORRECT
    if (err.status === 403) return UNVERIFIED
    if (err.status === 429) return THROTTLED
  }
  return UNAVAILABLE
}

export function readSignInState(search: string): string | null {
  const all = new URLSearchParams(search).getAll('state')
  return all.length === 1 && STATE_RE.test(all[0]) ? all[0] : null
}

export function startUrl(): string | null {
  const base = appBase()
  return base ? `${base}?auth=start` : null
}
