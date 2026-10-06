import { RESET_DONE, RESET_FAILED, type ResetOutcome } from '../passwordReset'
import type { VerifyOutcome } from '../verify'
import { Button } from './ds/Button'

const COPY = {
  verified: 'Your email address is verified. Please sign in.',
  failed: 'That link did not work. It may have expired or already been used.',
  reset: RESET_DONE,
  'reset-failed': RESET_FAILED,
} as const

const TONE = {
  verified: { background: 'var(--status-success-bg)', color: 'var(--status-success-fg)' },
  failed: { background: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
  reset: { background: 'var(--status-success-bg)', color: 'var(--status-success-fg)' },
  'reset-failed': { background: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
} as const

export function VerifyNotice({ outcome, onDismiss, onRequestReset }: { outcome: NonNullable<VerifyOutcome | ResetOutcome>; onDismiss: () => void; onRequestReset?: () => void }) {
  return (
    <div className="container" style={{ paddingTop: 16 }}>
      <div
        role="status"
        style={{
          ...TONE[outcome],
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          flexWrap: 'wrap',
          gap: 12,
          padding: '12px 16px',
          borderRadius: 'var(--radius)',
          fontSize: 14,
        }}
      >
        <span>{COPY[outcome]}</span>
        {outcome === 'reset-failed' && onRequestReset && (
          <Button variant="text" onClick={onRequestReset} style={{ color: 'inherit', borderBottomColor: 'currentColor' }}>
            Request a new link
          </Button>
        )}
        <Button variant="text" onClick={onDismiss} style={{ color: 'inherit', borderBottomColor: 'currentColor' }}>
          Dismiss
        </Button>
      </div>
    </div>
  )
}
