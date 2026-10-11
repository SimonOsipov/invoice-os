import { INVITE_NOTICES, type InviteOutcome } from '../invite'
import { RESET_DONE, RESET_FAILED, type ResetOutcome } from '../passwordReset'
import type { VerifyOutcome } from '../verify'
import { Button } from './ds/Button'

const COPY = {
  verified: 'Your email address is verified. Please sign in.',
  failed: 'This link is already used or expired. If you confirmed your email, sign in.',
  reset: RESET_DONE,
  'reset-failed': RESET_FAILED,
  ...INVITE_NOTICES,
} as const

const TONE = {
  verified: { background: 'var(--status-success-bg)', color: 'var(--status-success-fg)' },
  failed: { background: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
  reset: { background: 'var(--status-success-bg)', color: 'var(--status-success-fg)' },
  'reset-failed': { background: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
  'already-member': { background: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
  invalid: { background: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
  'other-address': { background: 'var(--status-red-bg)', color: 'var(--status-red-text)' },
} as const

export function VerifyNotice({ outcome, onDismiss, onRequestReset, onSignIn }: { outcome: NonNullable<VerifyOutcome | ResetOutcome | InviteOutcome>; onDismiss: () => void; onRequestReset?: () => void; onSignIn?: () => void }) {
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
        {outcome === 'failed' && onSignIn && (
          <Button variant="text" onClick={onSignIn} style={{ color: 'inherit', borderBottomColor: 'currentColor' }}>
            Sign in
          </Button>
        )}
        <Button variant="text" onClick={onDismiss} style={{ color: 'inherit', borderBottomColor: 'currentColor' }}>
          Dismiss
        </Button>
      </div>
    </div>
  )
}
