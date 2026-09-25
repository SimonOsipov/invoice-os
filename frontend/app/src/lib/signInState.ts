// Stub (D17): signatures only, so the Mode A reds fail on their assertions.
import { landingBase } from '../auth'

export const SIGN_IN_STATE_KEY = 'invoice-os.signInState'
export const SIGN_IN_STATE_TTL_MS = 10 * 60 * 1000

export type SignInOutcome = 'ready' | 'failed' | 'no-workspace'

export function ensureSignInState(_now: number = Date.now()): string {
  return ''
}

export function consumeSignInState(_now: number = Date.now()): string | null {
  return null
}

export function landingSignInUrl(_state: string, _outcome?: SignInOutcome): string | null {
  return landingBase()
}
