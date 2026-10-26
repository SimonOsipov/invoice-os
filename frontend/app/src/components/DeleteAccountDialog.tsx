import { useCallback, useId, useState } from 'react'

import { toApiError } from '@invoice-os/api-client'
import { DELETE_ACCOUNT, accountDeletionBody, deleteMyAccount, phraseMatches, type AccountDeletionPreview } from '../lib/accountDeletion'
import { useDismiss } from '../lib/useDismiss'
import type { PlatformCtx } from '../types'

export function DeleteAccountDialog({ ctx, base, preview, outcome, onClose }: {
  ctx: PlatformCtx
  base: string
  preview: AccountDeletionPreview
  outcome: 'leave' | 'hand_on'
  onClose: () => void
}) {
  const [typed, setTyped] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const titleId = useId()
  const inputId = useId()

  const closeIfIdle = useCallback(() => {
    if (!pending) onClose()
  }, [pending, onClose])
  useDismiss(true, closeIfIdle)

  async function confirm() {
    setPending(true)
    setError(null)
    try {
      await deleteMyAccount(ctx.authedFetch, base, outcome, preview.heirUserId)
      ctx.signOut()
    } catch (e) {
      setError(toApiError(e).message)
      setPending(false)
    }
  }

  const canConfirm = phraseMatches(typed) && !pending

  return (
    <div
      onClick={closeIfIdle}
      data-testid="delete-account-dialog"
      style={{ position: 'fixed', inset: 0, zIndex: 80, background: 'oklch(20% .02 210 / 0.42)', backdropFilter: 'blur(2px)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 40, animation: 'popIn 140ms ease-out' }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-testid="delete-account-panel"
        style={{ width: 460, maxWidth: '100%', maxHeight: '86vh', background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', boxShadow: '0 24px 60px -20px oklch(20% .02 210 / 0.4)', display: 'flex', flexDirection: 'column', overflow: 'auto' }}
      >
        <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--line-1)' }}>
          <div id={titleId} className="card-title">{DELETE_ACCOUNT.title}</div>
        </div>
        <div style={{ padding: '16px 20px', display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-2)', overflowWrap: 'anywhere' }}>
            {accountDeletionBody(preview, ctx.user.tenantName).map((part, i) =>
              i % 2 === 1 ? <b key={i} style={{ fontWeight: 600, color: 'var(--fg-1)' }}>{part}</b> : part,
            )}
          </div>
          <label htmlFor={inputId} className="label">{DELETE_ACCOUNT.labelPhrase}</label>
          <input
            id={inputId}
            type="text"
            data-testid="delete-account-typed"
            autoComplete="off"
            value={typed}
            disabled={pending}
            onChange={(e) => setTyped(e.target.value)}
            style={{ boxSizing: 'border-box', width: '100%', height: 34, padding: '0 10px', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-md)', background: 'var(--bg-1)', fontSize: 13, color: 'var(--fg-1)' }}
          />
          {error !== null && (
            <div data-testid="delete-account-error" style={{ fontSize: 12.5, lineHeight: 1.55, color: 'var(--status-red-text)', overflowWrap: 'anywhere' }}>
              {error}
            </div>
          )}
        </div>
        <div style={{ padding: '0 20px 16px', display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
          <button type="button" onClick={closeIfIdle} disabled={pending} className="v2-btn v2-btn-ghost pf-btn" style={{ height: 34, fontSize: 13 }}>
            {DELETE_ACCOUNT.cancel}
          </button>
          <button
            type="button"
            onClick={confirm}
            disabled={!canConfirm}
            data-testid="delete-account-confirm"
            className="v2-btn pf-btn"
            style={{ height: 34, fontSize: 13, background: 'var(--status-red-text)', color: 'var(--primary-foreground)', ...(canConfirm ? null : { opacity: 0.45, cursor: 'not-allowed', filter: 'none' }) }}
          >
            {DELETE_ACCOUNT.buttonLeave}
          </button>
        </div>
      </div>
    </div>
  )
}
