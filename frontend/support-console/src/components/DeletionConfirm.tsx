import { useState } from 'react'
import { reasonValid } from '../rulesApi'
import { Modal } from './Modal'

type Props = {
  title: string
  body: string
  // Omitted: the modal offers no confirm (the last active member).
  confirmLabel?: string
  nameToMatch?: string
  notice: string
  busy: boolean
  // False while the member preview loads: the confirm stays blocked.
  ready?: boolean
  onClose: () => void
  onConfirm: (reason: string, typedName: string) => void
}

export const PURGE_REASON_NOTICE = 'The reason stays in the staff audit log after the workspace is gone. Leave out names, emails and internal notes.'

// Same name rule as the app's nameMatches (frontend/app/src/lib/workspaceDeletion.ts): exact, no trimming.
const nameMatches = (typed: string, name: string): boolean => typed === name

const INPUT = { border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)', height: 30, flex: 1, padding: 0 } as const

// Same shape as JobActionConfirm; the dismiss reads Back, the confirm is the destructive red.
export function DeletionConfirm({ title, body, confirmLabel, nameToMatch, notice, busy, ready = true, onClose, onConfirm }: Props) {
  const [reason, setReason] = useState('')
  const [typed, setTyped] = useState('')
  const blocked = busy || !ready || !reasonValid(reason) || (nameToMatch !== undefined && !nameMatches(typed, nameToMatch))
  return (
    <Modal onClose={onClose}>
      <div style={{ padding: '22px 24px' }}>
        <h3 style={{ fontSize: 17, margin: '0 0 14px', overflowWrap: 'anywhere' }}>{title}</h3>
        <p style={{ fontSize: 13.5, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 0 14px', overflowWrap: 'anywhere' }}>{body}</p>
        {confirmLabel && (
          <>
            {nameToMatch !== undefined && (
              <>
                <div className="label" style={{ marginBottom: 6 }}>
                  Type the workspace name to confirm
                </div>
                <div className="mono" style={{ fontSize: 12.5, fontWeight: 600, margin: '0 0 8px', overflowWrap: 'anywhere' }}>
                  {nameToMatch}
                </div>
                <div className="ops-input ops-field" style={{ display: 'flex', alignItems: 'center', marginBottom: 14 }}>
                  <input style={INPUT} aria-label="Workspace name" autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} />
                </div>
              </>
            )}
            <div className="label" style={{ marginBottom: 6 }}>
              Reason
            </div>
            <div className="ops-input ops-field" style={{ display: 'flex', alignItems: 'center' }}>
              <input style={INPUT} placeholder="Why is this changing?" aria-label="Reason" value={reason} onChange={(e) => setReason(e.target.value)} />
            </div>
            <p style={{ fontSize: 12, lineHeight: 1.5, color: 'var(--fg-3)', margin: '8px 0 0' }}>{notice}</p>
          </>
        )}
      </div>
      <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
        <button type="button" onClick={onClose} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 38 }}>
          Back
        </button>
        {confirmLabel && (
          <button
            type="button"
            disabled={blocked}
            onClick={() => onConfirm(reason.trim(), typed)}
            className="ops-btn"
            style={{
              height: 38,
              border: 0,
              padding: '0 18px',
              borderRadius: 'var(--radius-btn)',
              background: 'var(--status-red-text)',
              color: 'var(--primary-foreground)',
              fontFamily: 'var(--font-sans)',
              fontSize: 14,
              fontWeight: 600,
              ...(blocked ? { opacity: 0.45, filter: 'none', cursor: 'not-allowed' } : { cursor: 'pointer' }),
            }}
          >
            {confirmLabel}
          </button>
        )}
      </div>
    </Modal>
  )
}
