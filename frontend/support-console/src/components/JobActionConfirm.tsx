import { useState } from 'react'
import { REDRIVE_ICON } from '../icons'
import { reasonValid } from '../rulesApi'
import { Modal } from './Modal'

export type JobAction = 'redrive' | 'cancel'

type Props = {
  invoiceNumber: string
  action: JobAction
  busy: boolean
  onClose: () => void
  onConfirm: (reason: string) => void
}

export const REASON_NOTICE = 'The customer sees this reason in their audit log, from ASComply support. Leave out names, emails and internal notes.'

// Same shape as KillConfirm; the dismiss reads Back so a cancel confirm never shows two Cancel buttons.
export function JobActionConfirm({ invoiceNumber, action, busy, onClose, onConfirm }: Props) {
  const [reason, setReason] = useState('')
  const redrive = action === 'redrive'
  const blocked = busy || !reasonValid(reason)
  return (
    <Modal onClose={onClose}>
      <div style={{ padding: '22px 24px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 11, marginBottom: 14 }}>
          {redrive && (
            <span style={{ flex: 'none', width: 36, height: 36, borderRadius: 'var(--radius-md)', background: 'var(--status-green-bg)', color: 'var(--status-green-text)', display: 'grid', placeItems: 'center' }}>{REDRIVE_ICON}</span>
          )}
          <h3 style={{ fontSize: 17, margin: 0, overflowWrap: 'anywhere' }}>
            {redrive ? 'Re-drive' : 'Cancel'} {invoiceNumber}?
          </h3>
        </div>
        <p style={{ fontSize: 13.5, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 0 14px' }}>
          {redrive
            ? 'The job returns to the queue with its attempts reset, and the invoice is submitted again.'
            : 'The job is cancelled and the invoice is marked failed. It is not submitted.'}
        </p>
        <div className="label" style={{ marginBottom: 6 }}>
          Reason
        </div>
        <div className="ops-input ops-field" style={{ display: 'flex', alignItems: 'center' }}>
          <input
            style={{ border: 0, outline: 'none', background: 'transparent', fontFamily: 'var(--font-sans)', fontSize: 13, color: 'var(--fg-1)', height: 30, flex: 1, padding: 0 }}
            placeholder="Why is this changing?"
            aria-label="Reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
        </div>
        <p style={{ fontSize: 12, lineHeight: 1.5, color: 'var(--fg-3)', margin: '8px 0 0' }}>{REASON_NOTICE}</p>
      </div>
      <div style={{ padding: '14px 24px', borderTop: '1px solid var(--line-1)', display: 'flex', justifyContent: 'flex-end', gap: 10 }}>
        <button type="button" onClick={onClose} className="ops-btn v2-btn v2-btn-ghost" style={{ height: 38 }}>
          Back
        </button>
        <button
          type="button"
          disabled={blocked}
          onClick={() => onConfirm(reason.trim())}
          className={redrive ? 'ops-btn v2-btn v2-btn-primary' : 'ops-btn'}
          style={{
            height: 38,
            ...(!redrive && { border: 0, padding: '0 18px', borderRadius: 'var(--radius-btn)', background: 'var(--status-red-text)', color: 'var(--primary-foreground)', fontFamily: 'var(--font-sans)', fontSize: 14, fontWeight: 600 }),
            ...(blocked ? { opacity: 0.45, filter: 'none', cursor: 'not-allowed' } : redrive ? {} : { cursor: 'pointer' }),
          }}
        >
          {redrive ? 'Re-drive job' : 'Cancel job'}
        </button>
      </div>
    </Modal>
  )
}
