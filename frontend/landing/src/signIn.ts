// Landing sign-in client.
import { ApiError, apiFetch, gatewayBase } from '@invoice-os/api-client/client'

import { appBase, consoleBase } from './auth'

const STATE_RE = /^[A-Za-z0-9_-]{43}$/

const INCORRECT = 'Email or password is incorrect.'
const UNVERIFIED = 'Verify your email address first. The link is in your inbox.'
const THROTTLED = 'Too many attempts. Try again in a minute.'
export const SIGN_IN_UNAVAILABLE = 'Sign-in is unavailable right now. Try again shortly.'

export type ConsoleTarget = 'ops' | 'support'

export function handoffUrl(code: string, target?: ConsoleTarget): string | null {
  const base = target ? consoleBase(target) : appBase()
  return base ? `${base}?handoff=${encodeURIComponent(code)}` : null
}

export function signInConfigured(): boolean {
  return gatewayBase() !== null && appBase() !== null
}

// An unset gateway and a 200 without a code both throw 'malformed', which maps to SIGN_IN_UNAVAILABLE.
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
  if (isUnverified(err)) return UNVERIFIED
  if (err instanceof ApiError && err.kind === 'http') {
    if (err.status === 401) return INCORRECT
    if (err.status === 429) return THROTTLED
  }
  return SIGN_IN_UNAVAILABLE
}

export function isUnverified(err: unknown): boolean {
  return err instanceof ApiError && err.kind === 'http' && err.status === 403
}

export function readSignInState(search: string): string | null {
  const all = new URLSearchParams(search).getAll('state')
  return all.length === 1 && STATE_RE.test(all[0]) ? all[0] : null
}

export function readSignInConsole(search: string): ConsoleTarget | null {
  const all = new URLSearchParams(search).getAll('console')
  return all.length === 1 && (all[0] === 'ops' || all[0] === 'support') ? all[0] : null
}

export function startUrl(target?: ConsoleTarget): string | null {
  const base = target ? consoleBase(target) : appBase()
  return base ? `${base}?auth=start` : null
}

export const PREFLIGHT_MS = 3000

// Preflight so a dead app origin shows SIGN_IN_UNAVAILABLE instead of a browser error page.
export async function bounceToStart(target?: ConsoleTarget): Promise<boolean> {
  const base = target ? consoleBase(target) : appBase()
  const url = startUrl(target)
  if (!base || !url) return false
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), PREFLIGHT_MS)
  try {
    await fetch(base, { mode: 'no-cors', cache: 'no-store', signal: ctrl.signal })
  } catch {
    return false
  } finally {
    clearTimeout(timer)
  }
  window.location.href = url
  return true
}
