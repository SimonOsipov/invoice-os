// Names and values copy the gateway's redirect targets in internal/gateway/register.go.
export const VERIFIED_PARAM = 'verified'
export const VERIFY_PARAM = 'verify'
const VERIFIED_VALUE = '1'
const FAILED_VALUE = 'failed'

export type VerifyOutcome = 'verified' | 'failed' | null

// Exactly one recognised value reads as an outcome; anything else, repeats and both params included, is none.
export function readVerifyOutcome(search: string): VerifyOutcome {
  const p = new URLSearchParams(search)
  const verified = p.getAll(VERIFIED_PARAM)
  const failed = p.getAll(VERIFY_PARAM)
  const isVerified = verified.length === 1 && verified[0] === VERIFIED_VALUE
  const isFailed = failed.length === 1 && failed[0] === FAILED_VALUE
  if (isVerified === isFailed) return null
  return isVerified ? 'verified' : 'failed'
}
