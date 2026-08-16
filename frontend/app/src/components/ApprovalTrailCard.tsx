// Approvals trail rail card (APPR-13-03, task-553). Row rhythm precedent is Status
// history (InvoiceDetail.tsx:1024-1052) -- the sibling ordered-events card on this same
// rail, two slots below; only the numbered medallion (WorkflowSimulator.tsx:77-90) has no
// Status-history equivalent and is borrowed for that one piece. Wrapper/header recipe
// copied from SourceDocumentCard.tsx:84-90; the run-state pill sits on the header's right
// (same space-between slot SourceDocumentCard gives its READ ONLY badge), shown only once
// the run has resolved to 'ready'.
import type { ReactNode } from 'react'

import { ErrorState, Loading, type AsyncState } from '@invoice-os/api-client'

import {
  APPROVAL_TRAIL_COPY,
  approvalRunStateView,
  approvalTrailDecisions,
  approvalTrailSteps,
  type ApprovalRun,
  type TrailDecisionView,
  type TrailStepView,
} from '../lib/approvals'

const STATE_TONE: Record<'amber' | 'green' | 'red' | 'muted', { bg: string; border: string; text: string }> = {
  amber: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' },
  green: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' },
  red: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)' },
  muted: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' },
}

function StatePill({ state }: { state: string }) {
  const view = approvalRunStateView(state)
  const tone = STATE_TONE[view.tone]
  return (
    <span
      data-testid="approval-trail-state"
      style={{ flex: 'none', display: 'inline-flex', alignItems: 'center', gap: 5, background: tone.bg, border: `1px solid ${tone.border}`, borderRadius: 999, padding: '2px 8px' }}
    >
      <span style={{ flex: 'none', width: 5, height: 5, borderRadius: 99, background: tone.text }} />
      <span className="mono" style={{ fontSize: 8.5, fontWeight: 600, color: tone.text, letterSpacing: '0.05em' }}>
        {view.label}
      </span>
    </span>
  )
}

// Row rhythm is Status history's (flex/gap, in-flow connector) -- the medallion is the
// only piece borrowed from WorkflowSimulator's ladder (ord1, or a check once satisfied).
function StepRow({ view, satisfied, isLast }: { view: TrailStepView; satisfied: boolean; isLast: boolean }) {
  return (
    <div data-testid="approval-trail-step" style={{ display: 'flex', gap: 12, paddingBottom: isLast ? 0 : 16 }}>
      <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', flex: 'none' }}>
        <span
          className="mono"
          style={{ flex: 'none', width: 24, height: 24, borderRadius: 99, display: 'grid', placeItems: 'center', background: 'var(--bg-3)', color: 'var(--fg-2)', fontSize: 11, fontWeight: 700 }}
        >
          {satisfied ? '✓' : view.ord1}
        </span>
        {!isLast && <span style={{ width: 1, flex: 1, background: 'var(--line-2)', minHeight: 20 }} />}
      </div>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 10 }}>
          <div style={{ fontSize: 12.5, fontWeight: 600, lineHeight: 1.3 }}>
            {view.kindLabel} · {view.roleTitle}
          </div>
          <span className="mono" style={{ flex: 'none', fontSize: 10.5, color: view.overdue ? 'var(--status-red-text)' : 'var(--fg-3)' }}>
            {view.dueLabel ?? '—'}
          </span>
        </div>
        {view.holderText != null && (
          <div style={{ marginTop: 3, fontSize: 11, color: view.holderWarn ? 'var(--status-amber-text)' : 'var(--fg-3)' }}>
            {view.holderText}
            {view.holderWarn && (
              <span className="mono" style={{ marginLeft: 6, fontSize: 9.5, letterSpacing: '0.04em' }}>
                {APPROVAL_TRAIL_COPY.unstaffedSeat}
              </span>
            )}
          </div>
        )}
        {view.kind === 'notify' && (
          <div data-testid="approval-trail-notify-note" style={{ marginTop: 3, fontSize: 11, color: 'var(--fg-3)', lineHeight: 1.5 }}>
            <span className="mono">
              {view.notifyTarget} · {view.notifyChannel}
            </span>
            <div>{view.notifyNote}</div>
          </div>
        )}
        {view.kind === 'autoapprove' && <div style={{ marginTop: 3, fontSize: 11, color: 'var(--fg-3)' }}>{APPROVAL_TRAIL_COPY.autoApproved}</div>}
      </div>
    </div>
  )
}

function DecisionRow({ view }: { view: TrailDecisionView }) {
  return (
    <div data-testid="approval-trail-decision" style={{ padding: '10px 0', borderTop: '1px solid var(--line-1)' }}>
      <span style={{ fontSize: 12.5, fontWeight: 600 }}>{view.outcomeLabel}</span>
      <div style={{ marginTop: 2, fontSize: 10.5, color: 'var(--fg-3)' }}>
        <span className={view.actorMono ? 'mono' : undefined}>{view.actorText}</span>
        {' · '}
        <span>{view.whenLabel}</span>
      </div>
      {view.reason != null && <div style={{ marginTop: 4, fontSize: 11.5, color: 'var(--fg-2)', lineHeight: 1.5 }}>{view.reason}</div>}
    </div>
  )
}

export function ApprovalTrailCard({ run }: { run: AsyncState<ApprovalRun | null> & { run: () => void } }) {
  // Non-null only once status is 'ready' -- the reducer contract (async-state.ts:49-52)
  // never carries data on any other status; status is still what drives every branch below.
  const data: ApprovalRun | null = run.status === 'ready' && run.data ? run.data : null

  let body: ReactNode = null
  if (run.status === 'idle' || run.status === 'loading') {
    body = <Loading label={APPROVAL_TRAIL_COPY.loading} />
  } else if (run.status === 'error') {
    body = run.error ? <ErrorState error={run.error} onRetry={run.run} /> : null
  } else if (run.status === 'empty') {
    body = (
      <div data-testid="approval-trail-empty" style={{ padding: '14px 16px', border: '1px dashed var(--line-3)', borderRadius: 'var(--radius-md)', background: 'transparent' }}>
        <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--fg-2)' }}>{APPROVAL_TRAIL_COPY.emptyTitle}</div>
        <div style={{ marginTop: 4, fontSize: 12.5, lineHeight: 1.55, color: 'var(--fg-3)' }}>{APPROVAL_TRAIL_COPY.emptyMessage}</div>
      </div>
    )
  } else if (run.status === 'ready' && data) {
    const stepViews = approvalTrailSteps(data)
    const decisionViews = approvalTrailDecisions(data)
    body = (
      <>
        {data.state === 'cancelled' && (
          <div
            data-testid="approval-trail-voided"
            style={{
              marginBottom: 14,
              padding: '10px 12px',
              borderRadius: 'var(--radius-md)',
              background: 'var(--status-amber-bg)',
              border: '1px solid var(--status-amber-border)',
              fontSize: 12.5,
              color: 'var(--status-amber-text)',
              lineHeight: 1.5,
            }}
          >
            {APPROVAL_TRAIL_COPY.voided}
          </div>
        )}
        <div style={{ fontSize: 10.5, fontWeight: 700, letterSpacing: '0.05em', textTransform: 'uppercase', color: 'var(--fg-3)', marginBottom: 8 }}>
          {APPROVAL_TRAIL_COPY.stepsHeading}
        </div>
        <div style={{ display: 'flex', flexDirection: 'column' }}>
          {stepViews.map((view, i) => (
            <StepRow key={i} view={view} satisfied={data.steps[i]?.state === 'satisfied'} isLast={i === stepViews.length - 1} />
          ))}
        </div>
        <div style={{ fontSize: 10.5, fontWeight: 700, letterSpacing: '0.05em', textTransform: 'uppercase', color: 'var(--fg-3)', margin: '18px 0 4px' }}>
          {APPROVAL_TRAIL_COPY.decisionsHeading}
        </div>
        {decisionViews.length === 0 ? (
          <div style={{ fontSize: 12, color: 'var(--fg-3)' }}>{APPROVAL_TRAIL_COPY.noDecisions}</div>
        ) : (
          decisionViews.map((view, i) => <DecisionRow key={i} view={view} />)
        )}
      </>
    )
  }

  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
      <div style={{ padding: '13px 18px', borderBottom: '1px solid var(--line-1)', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
        <span className="card-title">{APPROVAL_TRAIL_COPY.cardTitle}</span>
        {data && <StatePill state={data.state} />}
      </div>
      <div data-testid="approval-trail" style={{ padding: 16 }}>
        {body}
      </div>
    </div>
  )
}
