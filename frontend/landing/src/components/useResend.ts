// Shared by the sign-in and registration windows: one resend request at a time, no answer after a reset.
import { useCallback, useRef, useState } from 'react'
import { resendVerification } from '../register'

export function useResend(email: string | undefined, _send: (email: string) => Promise<void> = resendVerification) {
  const [resending, setResending] = useState(false)
  const [note, setNote] = useState<{ ok: boolean }>()
  // Bumped by reset, so an older answer is dropped.
  const seq = useRef(0)

  const reset = useCallback(() => {
    seq.current++
    setResending(false)
    setNote(undefined)
  }, [])

  // Stub: `unknown` because callers pass resend as an onClick handler.
  async function resend(_to?: unknown) {
    if (resending || email === undefined) return
    setNote(undefined)
    setResending(true)
    const mine = seq.current
    let ok = true
    try {
      await resendVerification(email)
    } catch {
      ok = false
    }
    if (mine !== seq.current) return
    setNote({ ok })
    setResending(false)
  }

  return { resending, note, resend, reset }
}
