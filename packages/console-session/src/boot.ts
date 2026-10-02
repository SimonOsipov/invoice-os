import type { ConsoleSession } from './session'
import type { ConsoleTarget } from './state'

export type ConsoleBoot = { kind: 'open'; session: ConsoleSession | null } | { kind: 'leave'; url: string }

export async function redeemHandoffCode(_gateway: string, _code: string, _state: string): Promise<ConsoleSession> {
  return { token: '', refreshToken: '' }
}

export async function renewConsoleSession(_gateway: string, _s: ConsoleSession): Promise<ConsoleSession> {
  return { token: '', refreshToken: '' }
}

export async function resolveConsoleBoot(_o: {
  search: string
  storageKey: string
  target: ConsoleTarget
  gateway: string | null
  landing: string | null
  now?: number
}): Promise<ConsoleBoot> {
  return { kind: 'leave', url: '' }
}
