// The Approvals rail card: the run's state, and who an OPEN run is waiting on. No step
// ladder and no decision ledger -- node 3 of the state strip carries the outcome, and the
// activity feed carries every approval event with its actor, time and reason (D-AC-11).
//
// Not a fetcher. GET /approval is a page-level useAsync in InvoiceDetail.tsx that the state
// strip reads too; this card is a pure consumer of that AsyncState. Moving the fetch here
// would give the two surfaces two answers.
import type { ReactNode } from 'react'

import { ErrorState, Loading, type AsyncState } from '@invoice-os/api-client'

import { APPROVAL_CARD_COPY, approvalStateView, type ApprovalRun, type ApprovalStateView } from '../lib/approvals'

// The badge tones; the strip has its own map.
const STATE_TONE: Record<ApprovalStateView['stateTone'], { bg: string; border: string; text: string }> = {
  amber: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
  green: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  red: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)' },
  muted: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' },
}

function StatePill({ view }: { view: ApprovalStateView }) {
  const tone = STATE_TONE[view.stateTone]
  return (
    <span
      data-testid="approval-state"
      style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 'var(--radius-sm)', padding: '2px 8px' }}
    >
      <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: tone.text }}>
        {view.stateLabel}
      </span>
    </span>
  )
}

function WaitingOn({ pending }: { pending: NonNullable<ApprovalStateView['pending']> }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div className="label">{APPROVAL_CARD_COPY.waitingOn}</div>
      <div data-testid="approval-holder" style={{ fontSize: 13.5, fontWeight: 600 }}>
        {pending.roleTitle}
      </div>
      {pending.holderText != null && (
        <div data-testid="approval-holder-name" style={{ fontSize: 12.5, color: 'var(--fg-2)' }}>
          {pending.holderText}
          {pending.holderWarn && (
            <span data-testid="approval-holder-warn" className="mono" style={{ marginLeft: 6, fontSize: 10, color: 'var(--status-amber-text)' }}>
              {APPROVAL_CARD_COPY.unstaffedSeat}
            </span>
          )}
        </div>
      )}
      {pending.dueLabel != null && (
        <div data-testid="approval-due" className="mono" style={{ fontSize: 11, color: pending.overdue ? 'var(--status-red-text)' : 'var(--fg-3)' }}>
          {pending.dueLabel}
        </div>
      )}
      {/* Points at the page-header pair (D-AC-7); this card renders no control of its own. */}
      <div
        data-testid="approval-decide-hint"
        style={{ marginTop: 4, paddingTop: 8, borderTop: '1px solid var(--line-1)', color: 'var(--fg-3)' }}
      >
        {APPROVAL_CARD_COPY.decideAbove}
      </div>
    </div>
  )
}

export function ApprovalStateCard({ run }: { run: AsyncState<ApprovalRun | null> & { run: () => void } }): ReactNode {
  // Non-null only once status is 'ready' -- the reducer contract (async-state.ts:49-52)
  // never carries data on any other status; status is still what drives every branch below.
  const data: ApprovalRun | null = run.status === 'ready' && run.data ? run.data : null
  const view = data ? approvalStateView(data) : null

  let body: ReactNode = null
  if (run.status === 'idle' || run.status === 'loading') {
    body = <Loading label={APPROVAL_CARD_COPY.loading} />
  } else if (run.status === 'error') {
    // Kept deliberately (F-E). The strip's mount gate admits `error`, so on an approval 500
    // node 3 captions `Not required` from run=null; without this branch that false
    // compliance claim is the only thing the operator is told.
    // approvalStateCard_a500KeepsTheRetryBesideTheStripsFalseClaim holds it.
    body = run.error ? <ErrorState error={run.error} onRetry={run.run} /> : null
  } else if (run.status === 'empty') {
    body = (
      <div data-testid="approval-empty">
        <div style={{ fontWeight: 600, marginBottom: 3 }}>{APPROVAL_CARD_COPY.emptyTitle}</div>
        <div style={{ color: 'var(--fg-3)' }}>{APPROVAL_CARD_COPY.emptyMessage}</div>
      </div>
    )
  } else if (view) {
    // Three mutually exclusive ready states: `voided` implies `pending === null`, because
    // pendingApprovalStep gates on run.state === 'open'.
    body = view.voided ? (
      <div data-testid="approval-voided" style={{ color: 'var(--fg-3)' }}>
        {APPROVAL_CARD_COPY.voided}
      </div>
    ) : view.pending === null ? (
      <div data-testid="approval-no-pending" style={{ fontSize: 12.5, color: 'var(--fg-3)', lineHeight: 1.5 }}>
        {APPROVAL_CARD_COPY.noPending}
      </div>
    ) : (
      <WaitingOn pending={view.pending} />
    )
  }

  return (
    <div data-testid="approval-card" style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
        <span className="card-title">{APPROVAL_CARD_COPY.cardTitle}</span>
        {view && <StatePill view={view} />}
      </div>
      <div style={{ padding: '14px 18px', fontSize: 12.5, lineHeight: 1.5 }}>{body}</div>
    </div>
  )
}
