import { ApiError, gatewayBase } from '@invoice-os/api-client'
import { decodeJwtPayload, isStaffToken, loadConsoleSession, renewConsoleSession, saveConsoleSession } from '@invoice-os/console-session'
import { expireSession, SESSION_KEY } from './auth'
import { subOf } from './blog'

let renewing: Promise<void> | null = null

const renewOnce = (): Promise<void> => {
  renewing ??= (async () => {
    const s = loadConsoleSession(SESSION_KEY)
    const base = gatewayBase()
    if (!s?.refreshToken || !base) throw new ApiError('http', 'Not signed in', 401)
    const next = await renewConsoleSession(base, { token: s.token, refreshToken: s.refreshToken })
    if (!isStaffToken(next.token)) throw new ApiError('http', 'Not signed in', 401)
    saveConsoleSession(SESSION_KEY, next)
  })().finally(() => {
    renewing = null
  })
  return renewing
}

const is401 = (e: unknown): e is ApiError => e instanceof ApiError && e.status === 401

export async function withSession<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call()
  } catch (e) {
    if (!is401(e)) throw e
    try {
      await renewOnce()
    } catch {
      throw e
    }
    return call()
  }
}

let expired = false

export const expireOnce = (): void => {
  if (expired) return
  expired = true
  expireSession()
}

export const currentSub = (): string | null => subOf(decodeJwtPayload(loadConsoleSession(SESSION_KEY)?.token ?? null))
