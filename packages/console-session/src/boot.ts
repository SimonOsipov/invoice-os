import { ApiError, apiFetch } from '@invoice-os/api-client/client'

import { clearConsoleSession, isStaffToken, loadConsoleSession, saveConsoleSession, type ConsoleSession } from './session'
import {
  consumeSignInState,
  ensureSignInState,
  landingSignInUrl,
  mintSignInState,
  readHandoffCode,
  type ConsoleOutcome,
  type ConsoleTarget,
} from './state'

export type ConsoleBoot = { kind: 'open'; session: ConsoleSession | null } | { kind: 'leave'; url: string }

const TIMEOUT_MS = 15000

// Wire shape of /auth/exchange and /auth/refresh answers: internal/gateway/signin.go, refresh.go.
async function pairFrom(url: string, body: unknown): Promise<ConsoleSession> {
  const a = await apiFetch<{ access_token?: unknown; refresh_token?: unknown } | null>(url, {
    method: 'POST',
    body,
    signal: AbortSignal.timeout(TIMEOUT_MS),
  })
  const token = a?.access_token
  const refreshToken = a?.refresh_token
  if (typeof token !== 'string' || token === '' || typeof refreshToken !== 'string' || refreshToken === '') {
    throw new ApiError('malformed', 'answer without both tokens', 200)
  }
  return { token, refreshToken }
}

export function redeemHandoffCode(gateway: string, code: string, state: string): Promise<ConsoleSession> {
  return pairFrom(`${gateway}/auth/exchange`, { code, state })
}

export function renewConsoleSession(gateway: string, s: ConsoleSession): Promise<ConsoleSession> {
  return pairFrom(`${gateway}/auth/refresh`, { refresh_token: s.refreshToken })
}

export async function resolveConsoleBoot(o: {
  search: string
  storageKey: string
  target: ConsoleTarget
  gateway: string | null
  landing: string | null
  now?: number
}): Promise<ConsoleBoot> {
  const { landing, gateway, storageKey, target } = o
  const now = o.now ?? Date.now()
  if (landing === null) return { kind: 'open', session: null }
  const leave = (outcome?: ConsoleOutcome): ConsoleBoot => ({
    kind: 'leave',
    url: landingSignInUrl(landing, ensureSignInState(now), target, outcome),
  })
  const open = (session: ConsoleSession): ConsoleBoot => {
    saveConsoleSession(storageKey, session)
    return { kind: 'open', session }
  }

  if (new URLSearchParams(o.search).get('auth') === 'start') {
    return { kind: 'leave', url: landingSignInUrl(landing, mintSignInState(now), target, 'ready') }
  }

  const code = readHandoffCode(o.search)
  const state = code === null ? null : consumeSignInState(now)
  if (code !== null && state !== null) {
    if (gateway === null) return leave('failed')
    try {
      const s = await redeemHandoffCode(gateway, code, state)
      return isStaffToken(s.token) ? open(s) : leave('not-staff')
    } catch {
      return leave('failed')
    }
  }

  const stored = loadConsoleSession(storageKey)
  if (stored === null || gateway === null) return leave()
  try {
    const s = await renewConsoleSession(gateway, stored)
    if (isStaffToken(s.token)) return open(s)
    clearConsoleSession(storageKey)
    return leave('not-staff')
  } catch (e) {
    if (e instanceof ApiError && e.kind === 'http' && (e.status === 400 || e.status === 401)) {
      clearConsoleSession(storageKey)
      return leave()
    }
    return leave('failed')
  }
}
