import { ApiError, apiFetch } from '@invoice-os/api-client/client'

import { clearConsoleSession, isStaffToken, loadConsoleSession, saveConsoleSession, type ConsoleSession } from './session'
import {
  appStartUrl,
  consumeSignInState,
  ensureSignInState,
  landingSignInUrl,
  mintSignInState,
  readHandoffCode,
  type ConsoleOutcome,
  type ConsoleTarget,
} from './state'

export type ConsoleBoot = { kind: 'open'; session: ConsoleSession | null } | { kind: 'leave'; url: string; replace?: true }

const TIMEOUT_MS = 15000

const nonEmpty = (v: unknown): v is string => typeof v === 'string' && v !== ''

// Wire shape of /auth/exchange and /auth/refresh answers: internal/gateway/signin.go, refresh.go.
async function answerFrom(url: string, body: unknown): Promise<{ access_token?: unknown; refresh_token?: unknown } | null> {
  return apiFetch<{ access_token?: unknown; refresh_token?: unknown } | null>(url, {
    method: 'POST',
    body,
    signal: AbortSignal.timeout(TIMEOUT_MS),
  })
}

// Both tokens is a landing session; access_token alone is borrowed from the app (console-handoff).
export async function redeemHandoffCode(gateway: string, code: string, state: string): Promise<ConsoleSession> {
  const a = await answerFrom(`${gateway}/auth/exchange`, { code, state })
  const token = a?.access_token
  const refreshToken = a?.refresh_token
  if (!nonEmpty(token) || (refreshToken !== undefined && !nonEmpty(refreshToken))) {
    throw new ApiError('malformed', 'answer without an access token', 200)
  }
  return { token, refreshToken: refreshToken ?? null }
}

export async function renewConsoleSession(gateway: string, s: { token: string; refreshToken: string }): Promise<ConsoleSession> {
  const a = await answerFrom(`${gateway}/auth/refresh`, { refresh_token: s.refreshToken })
  const token = a?.access_token
  const refreshToken = a?.refresh_token
  if (!nonEmpty(token) || !nonEmpty(refreshToken)) throw new ApiError('malformed', 'answer without both tokens', 200)
  return { token, refreshToken }
}

export async function resolveConsoleBoot(o: {
  search: string
  storageKey: string
  target: ConsoleTarget
  gateway: string | null
  landing: string | null
  app?: string | null
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

  const app = o.app ?? null
  const auth = new URLSearchParams(o.search).get('auth')
  if (auth === 'app' && app !== null) {
    return { kind: 'leave', url: appStartUrl(app, mintSignInState(now), target), replace: true }
  }
  if (auth === 'start' || auth === 'app') {
    // replace drops the ?auth= entry so Back from the landing skips it.
    return { kind: 'leave', url: landingSignInUrl(landing, mintSignInState(now), target, 'ready'), replace: true }
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
  if (stored.refreshToken === null) {
    // Kept: a second console tab may be using it. The new exchange overwrites it.
    if (app === null) return leave()
    return { kind: 'leave', url: appStartUrl(app, ensureSignInState(now), target), replace: true }
  }
  try {
    const s = await renewConsoleSession(gateway, { token: stored.token, refreshToken: stored.refreshToken })
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
