// Names and values copy the gateway's redirect targets in internal/gateway/reset_password.go.
export { requestPasswordReset } from './register'

export const RESET_PARAM = 'reset'
const RESET_VALUE = '1'
const FAILED_VALUE = 'failed'

export const RESET_SENT = 'If this address has an account, a reset link is on its way.'
export const RESET_DONE = 'Your password is changed. Sign in with your new password.'
export const RESET_FAILED = 'That reset link did not work. It may have expired or already been used.'

export type ResetOutcome = 'reset' | 'reset-failed' | null

// Exactly one recognised value reads as an outcome; anything else, repeats included, is none.
export function readResetOutcome(search: string): ResetOutcome {
  const values = new URLSearchParams(search).getAll(RESET_PARAM)
  if (values.length !== 1) return null
  if (values[0] === RESET_VALUE) return 'reset'
  return values[0] === FAILED_VALUE ? 'reset-failed' : null
}
