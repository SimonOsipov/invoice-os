// Undo import: idle -> armed -> submitting -> done. The host decides whether to render it
// (the batch is `undoable`) and refetches in `onSettled`.
import { useEffect, useRef, useState, type CSSProperties } from 'react'

import { gatewayBase } from '@invoice-os/api-client'

import { isUnauthorized } from '../lib/authedFetch'
import { undoImport } from '../lib/importApi'
import type { PlatformCtx } from '../types'

type Phase = 'idle' | 'armed' | 'submitting' | 'done'

export const UNDO_IMPORT_COPY = {
  button: 'Undo import',
  prompt: "Discard this import's invoices and release the file?",
  cancel: 'Cancel',
  confirm: 'Undo import',
  sending: 'Undoing…',
  done: 'Import undone. The file can now be imported for another client.',
} as const

const BUSY: CSSProperties = { background: 'var(--bg-3)', color: 'var(--fg-4)', cursor: 'not-allowed' }

export function UndoImportControl({
  ctx,
  batchId,
  size,
  onSettled,
  onUndone,
}: {
  ctx: PlatformCtx
  batchId: string
  size?: number
  onSettled: () => void
  // Called on a 200 only, for a host that must keep the done line after its refetch.
  onUndone?: () => void
}) {
  const [phase, setPhase] = useState<Phase>('idle')
  const [error, setError] = useState<string | null>(null)
  const alive = useRef(true)
  const inFlight = useRef(false)
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  // 34 is the invoice-actions row, whose siblings set only a height.
  const sized: CSSProperties =
    size === undefined ? {} : size === 34 ? { height: size } : { height: size, padding: '0 16px', fontSize: 13 }
  const busy = phase === 'submitting'

  const confirm = async () => {
    const base = gatewayBase()
    if (base == null || inFlight.current) return
    inFlight.current = true
    setError(null)
    setPhase('submitting')
    try {
      await undoImport(ctx.authedFetch, base, batchId)
      if (!alive.current) return
      setPhase('done')
      onUndone?.()
      onSettled()
    } catch (err) {
      if (!alive.current) return
      if (isUnauthorized(err)) {
        // The authedFetch seam owns the sign-out; nothing to refetch.
        setPhase('armed')
        return
      }
      setError(err instanceof Error ? err.message : 'Something went wrong. Please try again.')
      setPhase('idle')
      onSettled()
    } finally {
      inFlight.current = false
    }
  }

  if (phase === 'done') {
    return (
      <span data-testid="undo-import-done" style={{ fontSize: 12.5, color: 'var(--fg-3)' }}>
        {UNDO_IMPORT_COPY.done}
      </span>
    )
  }

  return (
    <span style={{ display: 'inline-flex', flexDirection: 'column', alignItems: 'flex-start', gap: 6 }}>
      {phase === 'idle' ? (
        <button
          type="button"
          data-testid="undo-import"
          onClick={() => setPhase('armed')}
          className="v2-btn v2-btn-ghost pf-btn"
          style={sized}
        >
          {UNDO_IMPORT_COPY.button}
        </button>
      ) : (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <span data-testid="undo-import-prompt" style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-1)' }}>
            {UNDO_IMPORT_COPY.prompt}
          </span>
          <button
            type="button"
            data-testid="undo-import-cancel"
            onClick={() => setPhase('idle')}
            disabled={busy}
            className="v2-btn v2-btn-ghost pf-btn"
            style={{ ...sized, ...(busy ? BUSY : null) }}
          >
            {UNDO_IMPORT_COPY.cancel}
          </button>
          <button
            type="button"
            data-testid="undo-import-confirm"
            onClick={() => void confirm()}
            disabled={busy}
            className="v2-btn v2-btn-primary pf-btn"
            style={{ ...sized, ...(busy ? BUSY : null) }}
          >
            {busy ? UNDO_IMPORT_COPY.sending : UNDO_IMPORT_COPY.confirm}
          </button>
        </span>
      )}
      {error != null && (
        <span
          data-testid="undo-import-error"
          style={{ fontSize: 11.5, color: 'var(--fg-3)', lineHeight: 1.5 }}
        >
          {error}
        </span>
      )}
    </span>
  )
}
