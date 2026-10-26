import { useCallback, useEffect, useRef, useState } from 'react'

import { ErrorState, gatewayBase, toApiError, type ApiError } from '@invoice-os/api-client'
import { ACCOUNT_PAGE, DELETE_ACCOUNT, accountDeletionBody, readAccountDeletion, type AccountDeletionPreview } from '../lib/accountDeletion'
import type { PlatformCtx } from '../types'
import { DeleteAccountDialog } from './DeleteAccountDialog'
import { DeleteWorkspaceDialog } from './DeleteWorkspaceDialog'

// Same dead-button treatment as WorkspaceSettings.
const DISABLED: React.CSSProperties = { opacity: 0.45, cursor: 'not-allowed', filter: 'none' }

const same = (a: AccountDeletionPreview, b: AccountDeletionPreview) => a.outcome === b.outcome && a.heirName === b.heirName

// ceiling: someone who joins while the DEL-04 dialog is open still meets the schedule; any admin may cancel it (DEL-08 D24).
export function AccountView({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const [preview, setPreview] = useState<AccountDeletionPreview | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [open, setOpen] = useState(false)
  const [checking, setChecking] = useState(false)
  const fetcher = useRef(ctx.authedFetch)
  fetcher.current = ctx.authedFetch
  const latest = useRef(0)

  // An answer to anything but the latest read is dropped.
  const load = useCallback(async (): Promise<AccountDeletionPreview | null> => {
    if (base == null) return null
    const id = ++latest.current
    setError(null)
    try {
      const r = await readAccountDeletion(fetcher.current, base)
      if (id !== latest.current) return null
      setPreview(r)
      return r
    } catch (e) {
      if (id === latest.current) setError(toApiError(e))
      return null
    }
  }, [base])

  useEffect(() => {
    void load()
  }, [load])

  const name = ctx.user.tenantName
  const disabled = base == null || preview === null || checking || (preview.outcome === 'delete_workspace' && name == null)

  async function onDelete() {
    if (preview === null) return
    setChecking(true)
    const fresh = await load()
    setChecking(false)
    if (fresh !== null && same(fresh, preview)) setOpen(true)
  }

  function onClose() {
    setOpen(false)
    void load()
  }

  return (
    <div style={{ padding: '30px 36px 56px' }}>
      <div style={{ marginBottom: 22 }}>
        <div className="eyebrow" style={{ marginBottom: 10 }}>YOUR ACCOUNT</div>
        <h1 style={{ fontSize: 26, letterSpacing: '-0.025em', margin: '0 0 4px' }}>{ACCOUNT_PAGE.title}</h1>
      </div>
      {preview === null && error !== null ? (
        <ErrorState error={error} onRetry={() => void load()} />
      ) : (
        <div data-testid="account-delete-card" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden', maxWidth: 720 }}>
          <div style={{ padding: '14px 20px', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
            <div style={{ minWidth: 0 }}>
              <div style={{ fontSize: 15, fontWeight: 700 }}>{DELETE_ACCOUNT.title}</div>
              <div data-testid="account-delete-body" style={{ marginTop: 4, fontSize: 13, lineHeight: 1.55, color: 'var(--fg-3)', overflowWrap: 'anywhere' }}>
                {preview !== null &&
                  accountDeletionBody(preview, name).map((part, i) =>
                    i % 2 === 1 ? <b key={i} style={{ fontWeight: 600, color: 'var(--fg-1)' }}>{part}</b> : part,
                  )}
              </div>
              {preview !== null && error !== null && (
                <div data-testid="account-delete-error" style={{ marginTop: 6, fontSize: 12.5, lineHeight: 1.55, color: 'var(--status-red-text)', overflowWrap: 'anywhere' }}>
                  {error.message}
                </div>
              )}
            </div>
            <button
              onClick={onDelete}
              disabled={disabled}
              data-testid="account-delete"
              className="v2-btn pf-btn"
              style={{ flex: 'none', height: 32, fontSize: 13, background: 'var(--status-red-text)', color: 'var(--primary-foreground)', ...(disabled ? DISABLED : null) }}
            >
              {preview?.outcome === 'delete_workspace' ? DELETE_ACCOUNT.buttonWorkspace : DELETE_ACCOUNT.buttonLeave}
            </button>
          </div>
        </div>
      )}
      {open && base != null && preview !== null && (
        preview.outcome === 'delete_workspace' ? (
          name != null && <DeleteWorkspaceDialog ctx={ctx} base={base} name={name} onClose={onClose} />
        ) : (
          <DeleteAccountDialog ctx={ctx} base={base} preview={preview} outcome={preview.outcome} onClose={onClose} />
        )
      )}
    </div>
  )
}
