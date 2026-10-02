export type ConsoleTarget = 'ops' | 'support'
export type ConsoleOutcome = 'ready' | 'failed' | 'not-staff'

export const SIGN_IN_STATE_KEY = 'invoice-os.signInState'
export const SIGN_IN_STATE_TTL_MS = 10 * 60 * 1000
export const SIGN_IN_STATE_REUSE_MS = 60_000
export const BASE64URL_43 = /^[A-Za-z0-9_-]{43}$/

export function readHandoffCode(_search: string): string | null {
  return null
}

export function ensureSignInState(_now?: number): string {
  return ''
}

export function mintSignInState(_now?: number): string {
  return ''
}

export function consumeSignInState(_now?: number): string | null {
  return null
}

export function landingSignInUrl(_landing: string, _state: string, _target: ConsoleTarget, _outcome?: ConsoleOutcome): string {
  return ''
}
