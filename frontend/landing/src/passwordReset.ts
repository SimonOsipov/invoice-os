// Mode A stub (RESEND-04-06): inert until the executor writes the real bodies.
export const RESET_PARAM = 'reset'
export const RESET_SENT = ''

export type ResetOutcome = 'reset' | 'reset-failed' | null

export function readResetOutcome(_search: string): ResetOutcome {
  return null
}

export async function requestPasswordReset(_email: string): Promise<void> {}
