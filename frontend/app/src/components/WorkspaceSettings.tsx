import { useId, useState } from 'react'

import { gatewayBase } from '@invoice-os/api-client'
import { companySetupAccess } from '../lib/members'
import { DELETE_ADMIN_ONLY, DELETE_WORKSPACE } from '../lib/workspaceDeletion'
import type { PlatformCtx } from '../types'
import { DeleteWorkspaceDialog } from './DeleteWorkspaceDialog'

// Same dead-button treatment as SettingsView's DISABLED.
const DISABLED: React.CSSProperties = { opacity: 0.45, cursor: 'not-allowed', filter: 'none' }

export function WorkspaceSettings({ ctx }: { ctx: PlatformCtx }) {
  const base = gatewayBase()
  const name = ctx.user.tenantName
  const access = companySetupAccess(ctx.membersState, ctx.members)
  const disabled = base == null || access !== 'add' || name == null
  const reasonId = useId()
  const [open, setOpen] = useState(false)

  return (
    <div data-testid="settings-workspace-card" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden', maxWidth: 720 }}>
      <div style={{ padding: '14px 20px', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
        <div style={{ minWidth: 0 }}>
          <div style={{ fontSize: 15, fontWeight: 700 }}>{DELETE_WORKSPACE.title}</div>
          <div style={{ marginTop: 4, fontSize: 13, lineHeight: 1.55, color: 'var(--fg-3)' }}>{DELETE_WORKSPACE.body}</div>
        </div>
        <button
          onClick={() => setOpen(true)}
          disabled={disabled}
          data-testid="settings-workspace-delete"
          title={access === 'wait' ? DELETE_ADMIN_ONLY : undefined}
          aria-describedby={access === 'wait' ? reasonId : undefined}
          className="v2-btn pf-btn"
          style={{ flex: 'none', height: 32, fontSize: 13, background: 'var(--status-red-text)', color: 'var(--primary-foreground)', ...(disabled ? DISABLED : null) }}
        >
          {DELETE_WORKSPACE.button}
        </button>
      </div>
      {access === 'wait' && (
        <div id={reasonId} data-testid="settings-workspace-delete-reason" style={{ padding: '0 20px 12px', fontSize: 11.5, color: 'var(--fg-3)', textAlign: 'right' }}>
          {DELETE_ADMIN_ONLY}
        </div>
      )}
      {open && base != null && name != null && <DeleteWorkspaceDialog ctx={ctx} base={base} name={name} onClose={() => setOpen(false)} />}
    </div>
  )
}
