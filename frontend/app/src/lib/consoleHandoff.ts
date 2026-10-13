import { ApiError, apiFetch } from '@invoice-os/api-client'

import { BASE64URL_43 } from './signInState'

// The Support Console's app-session door (its `?auth=app` start).
export const consoleStartUrl = (supportUrl: string): string => `${supportUrl}/?auth=app`

// The console's `?auth=console&console=support&state=S` start: the state only for that exact shape.
export function readConsoleStart(search: string): string | null {
  const p = new URLSearchParams(search)
  const states = p.getAll('state')
  if (p.get('auth') !== 'console' || p.getAll('auth').length !== 1) return null
  const targets = p.getAll('console')
  if (targets.length !== 1 || targets[0] !== 'support') return null
  return states.length === 1 && BASE64URL_43.test(states[0]) ? states[0] : null
}

// Mirrors ConsoleHandoffHandler in internal/gateway/console_handoff.go: `{state}` in, `{code}` out.
export async function requestConsoleCode(base: string, token: string, state: string): Promise<string> {
  const body = await apiFetch<{ code?: unknown } | null>(`${base}/auth/console-handoff`, {
    method: 'POST',
    token,
    body: { state },
    signal: AbortSignal.timeout(15000),
  })
  const code = body?.code
  if (typeof code !== 'string' || !BASE64URL_43.test(code)) {
    throw new ApiError('malformed', 'console hand-off answered without a code', 200)
  }
  return code
}

export const consoleHandoffUrl = (supportUrl: string, code: string): string =>
  `${supportUrl}/?handoff=${encodeURIComponent(code)}`
