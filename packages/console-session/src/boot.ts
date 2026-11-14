import { ApiError, sessionFetch } from '@invoice-os/api-client/client'

import { postSignOut } from './signOut'
import { clearConsoleSession, decodeJwtPayload, isStaffToken, loadConsoleSession, nonEmpty, saveConsoleSession, type ConsoleSession } from './session'
import {
  appStartUrl,
  consumeSignInState,
  ensureSignInState,
  landingSignInUrl,
  mintSignInState,
  peekSignInState,
  readHandoffCode,
  type ConsoleOutcome,
  type ConsoleTarget,
} from './state'

export type ConsoleBoot = { kind: 'open'; session: ConsoleSession | null } | { kind: 'leave'; url: string; replace?: true }

const TIMEOUT_MS = 15000

// Wire shape of /auth/exchange and /auth/refresh answers: internal/gateway/signin.go, refresh.go.
// The refresh token lives in the gateway cookie; `renewable` says whether it was set.
async function answerFrom(url: string, body: unknown): Promise<{ access_token?: unknown; renewable?: unknown } | null> {
  return sessionFetch<{ access_token?: unknown; renewable?: unknown } | null>(url, {
    method: 'POST',
    body,
    signal: AbortSignal.timeout(TIMEOUT_MS),
  })
}

// A landing session needs `renewable: true`; a support session borrowed from the app (console-handoff) accepts false.
export async function redeemHandoffCode(gateway: string, code: string, state: string, borrowed = false): Promise<ConsoleSession> {
  const a = await answerFrom(`${gateway}/auth/exchange`, { code, state })
  const token = a?.access_token
  const renewable = a?.renewable === true
  if (!nonEmpty(token) || (!borrowed && !renewable)) throw new ApiError('malformed', 'answer without a renewable session', 200)
  return { token, borrowed: !renewable }
}

// Renews through the cookie; `legacyRefreshToken` adopts a pre-v3 stored token into it.
export async function renewConsoleSession(gateway: string, legacyRefreshToken?: string): Promise<ConsoleSession> {
  const a = await answerFrom(`${gateway}/auth/refresh`, legacyRefreshToken === undefined ? undefined : { refresh_token: legacyRefreshToken })
  const token = a?.access_token
  if (!nonEmpty(token)) throw new ApiError('malformed', 'answer without an access token', 200)
  return { token, borrowed: false }
}

const hasPending = (key: string): boolean => {
  try {
    return localStorage.getItem(key) !== null
  } catch {
    return false
  }
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
  const pendingKey = `${storageKey}:sign-out-pending`
  const clearPending = () => {
    try {
      localStorage.removeItem(pendingKey)
    } catch {
      // storage denied: nothing to settle
    }
  }
  const code = readHandoffCode(o.search)
  // A sign-out that got no answer is sent again before anything else, never on a hand-off load.
  if (code === null && gateway !== null && hasPending(pendingKey)) {
    if ((await postSignOut(gateway)) !== 'unreached') clearPending()
  }
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
  // A usable landing session on the support console stays: StaffGate strips the param and the stored record renews.
  const keepsLanding = auth === 'app' && target === 'support' && loadConsoleSession(storageKey)?.borrowed === false
  if (!keepsLanding && auth === 'app' && app !== null) {
    return { kind: 'leave', url: appStartUrl(app, mintSignInState(now), target), replace: true }
  }
  if (!keepsLanding && (auth === 'start' || auth === 'app')) {
    // replace drops the ?auth= entry so Back from the landing skips it.
    return { kind: 'leave', url: landingSignInUrl(landing, mintSignInState(now), target, 'ready'), replace: true }
  }

  const state = code === null ? null : consumeSignInState(now)
  if (code !== null && state !== null) {
    if (gateway === null) return leave('failed')
    try {
      const s = await redeemHandoffCode(gateway, code, state, target === 'support' && app !== null)
      clearPending()
      return isStaffToken(s.token) ? open(s) : leave('not-staff')
    } catch {
      return leave('failed')
    }
  }

  const stored = loadConsoleSession(storageKey)
  if (stored === null || gateway === null) return leave()
  if (stored.borrowed) {
    // Kept for a second tab, but never a loop: an expired record, or one whose bounce already ran, is cleared.
    const exp = decodeJwtPayload(stored.token)?.exp
    const expired = typeof exp !== 'number' || exp * 1000 <= now
    const bounced = app !== null && peekSignInState(now) !== null
    if (expired || bounced) {
      clearConsoleSession(storageKey)
      return leave()
    }
    if (app === null) return leave()
    return { kind: 'leave', url: appStartUrl(app, ensureSignInState(now), target), replace: true }
  }
  try {
    const s = await renewConsoleSession(gateway, stored.legacyRefreshToken)
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
