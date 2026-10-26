import { useCallback, useEffect, useRef, useState } from 'react'

import { ErrorState, Loading, toApiError, type ApiError } from '@invoice-os/api-client'
import { BrandMark } from '../icons'
import { endAppChat } from '../chat'
import type { AuthedFetch } from '../lib/portfolio'
import { DELETION_NOTICE, cancelWorkspaceDeletion, deletionNoticeBody, formatDueDate, readWorkspaceDeletion } from '../lib/workspaceDeletion'

export type KnownDeletion = { dueAt: string | null; name: string | null; isAdmin: boolean }

// Replaces the workspace like SuspendedNotice; the lock refuses every tenant read, so no shell around it.
export function WorkspaceDeletionNotice({ authedFetch, base, known, onUnlocked, onSignOut }: {
  authedFetch: AuthedFetch
  base: string
  known: KnownDeletion
  onUnlocked: () => void
  onSignOut: () => void
}) {
  const [read, setRead] = useState<KnownDeletion | null>(null)
  const [readError, setReadError] = useState<ApiError | null>(null)
  const [cancelling, setCancelling] = useState(false)
  const [cancelError, setCancelError] = useState<string | null>(null)
  // Held in refs so a re-render never re-runs the mount read.
  const unlocked = useRef(onUnlocked)
  unlocked.current = onUnlocked
  const fetcher = useRef(authedFetch)
  fetcher.current = authedFetch
  const latest = useRef(0)

  // An answer to anything but the latest read is dropped.
  const load = useCallback(() => {
    const id = ++latest.current
    setReadError(null)
    readWorkspaceDeletion(fetcher.current, base).then(
      (r) => {
        if (id !== latest.current) return
        if (r.dueAt === null) unlocked.current()
        else setRead(r)
      },
      (e) => {
        if (id === latest.current) setReadError(toApiError(e))
      },
    )
  }, [base])

  useEffect(() => {
    endAppChat()
    load()
  }, [load])

  const dueAt = read?.dueAt ?? known.dueAt
  const name = read?.name ?? known.name
  const isAdmin = read?.isAdmin ?? known.isAdmin

  const cancel = async () => {
    setCancelling(true)
    setCancelError(null)
    try {
      await cancelWorkspaceDeletion(authedFetch, base)
      unlocked.current()
    } catch (e) {
      setCancelError(toApiError(e).message)
      setCancelling(false)
    }
  }

  return (
    <div
      className="asc-app"
      style={{ minHeight: '100vh', background: 'var(--bg-1)', fontFamily: 'var(--font-sans)', color: 'var(--fg-1)', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}
    >
      <div data-testid="workspace-deletion-notice" style={{ width: '100%', maxWidth: 452, background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 9, padding: '16px 18px', borderBottom: '1px solid var(--line-1)' }}>
          <BrandMark size={20} />
          <span style={{ fontWeight: 600, fontSize: 14, letterSpacing: '-0.02em' }}>ASComply</span>
        </div>
        <div style={{ padding: '28px 20px', display: 'flex', flexDirection: 'column', gap: 8 }}>
          <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--fg-1)' }}>{DELETION_NOTICE.title}</div>
          {dueAt !== null ? (
            <div style={{ fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)', overflowWrap: 'anywhere' }}>
              {deletionNoticeBody(formatDueDate(dueAt), name ?? 'this workspace')}
            </div>
          ) : readError !== null ? (
            <ErrorState error={readError} onRetry={load} />
          ) : (
            <Loading label="Loading…" />
          )}
          <div style={{ display: 'flex', gap: 8, marginTop: 10 }}>
            {isAdmin && (
              <button
                onClick={cancel}
                disabled={cancelling}
                data-testid="deletion-notice-cancel"
                className="v2-btn v2-btn-primary pf-btn"
                style={{ height: 34, padding: '0 12px', fontSize: 13 }}
              >
                {DELETION_NOTICE.cancel}
              </button>
            )}
            <button
              onClick={onSignOut}
              className="v2-btn v2-btn-ghost pf-btn"
              style={{ height: 34, padding: '0 12px', fontSize: 13 }}
            >
              {DELETION_NOTICE.signOut}
            </button>
          </div>
          {cancelError !== null && (
            <div data-testid="deletion-notice-error" style={{ fontSize: 12.5, lineHeight: 1.55, color: 'var(--status-red-text)' }}>
              {cancelError}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
