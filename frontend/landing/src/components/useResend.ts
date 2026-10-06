// Shared by the sign-in and registration windows: one resend request at a time, no answer after a reset.
import { useCallback, useRef, useState } from 'react'
import { resendVerification } from '../register'

export function useResend(email: string | undefined, send: (email: string) => Promise<void> = resendVerification) {
  const [resending, setResending] = useState(false)
  const [note, setNote] = useState<{ ok: boolean }>()
  // Bumped by reset, so an older answer is dropped.
  const seq = useRef(0)

  const reset = useCallback(() => {
    seq.current++
    setResending(false)
    setNote(undefined)
  }, [])

  // Call as `() => resend()`: an onClick handler would pass the event as `to`.
  async function resend(to: string | undefined = email) {
    if (resending || to === undefined) return
    setNote(undefined)
    setResending(true)
    const mine = seq.current
    let ok = true
    try {
      await send(to)
    } catch {
      ok = false
    }
    if (mine !== seq.current) return
    setNote({ ok })
    setResending(false)
  }

  return { resending, note, resend, reset }
}
