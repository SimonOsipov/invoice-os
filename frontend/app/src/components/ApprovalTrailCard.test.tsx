// @vitest-environment jsdom
// RED specs (task-553, APPR-13-03, Mode A) -- ApprovalTrailCard.tsx is a stub that throws
// 'not implemented' on every render, so each test below fails on that throw. Assertions
// are written against the shipped projection (lib/approvals.ts, APPR-13-02) and must all
// go green once Stage 3 replaces the stub.
import { render, screen, within, cleanup } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { AsyncState } from '@invoice-os/api-client'

import { APP_PERSONAS } from '../auth'
import {
  APPROVAL_TRAIL_COPY,
  approvalTrailDecisions,
  approvalTrailSteps,
  type ApprovalRun,
  type ApprovalRunDecision,
  type ApprovalRunStep,
} from '../lib/approvals'
import { ApprovalTrailCard } from './ApprovalTrailCard'

afterEach(cleanup)

function stepFixture(over: Partial<ApprovalRunStep> = {}): ApprovalRunStep {
  return {
    ord: 0,
    kind: 'approval',
    state: 'pending',
    workflow_role_key: 'role-1',
    workflow_role_title: 'Finance lead',
    holder: null,
    sla_hours: null,
    due_at: null,
    overdue: false,
    satisfied_at: null,
    satisfied_by: null,
    notify_target: null,
    notify_channel: null,
    ...over,
  }
}

function decisionFixture(over: Partial<ApprovalRunDecision> = {}): ApprovalRunDecision {
  return {
    run_step_id: 'step-1',
    ord: 0,
    decision: 'approved',
    actor: APP_PERSONAS.firm.subject,
    decided_at: '2026-08-02T09:00:00Z',
    reason: null,
    ...over,
  }
}

function runFixture(over: Partial<ApprovalRun> = {}): ApprovalRun {
  return {
    run_id: 'run-1',
    state: 'open',
    opened_at: '2026-08-01T00:00:00Z',
    closed_at: null,
    closed_by: null,
    steps: [],
    decisions: [],
    ...over,
  }
}

function readyRun(data: ApprovalRun): AsyncState<ApprovalRun | null> & { run: () => void } {
  return { status: 'ready', data, error: null, run: vi.fn() }
}

describe('ApprovalTrailCard', () => {
  it('the ladder renders one row per step in ord order', () => {
    const steps = [
      stepFixture({ ord: 0, kind: 'approval', state: 'pending', workflow_role_title: 'Finance lead' }),
      stepFixture({ ord: 1, kind: 'notify', state: 'pending', notify_target: 'ap@acme.test', notify_channel: 'email' }),
      stepFixture({ ord: 2, kind: 'approval', state: 'pending', workflow_role_title: 'Ops lead' }),
    ]
    const run = runFixture({ steps })

    render(<ApprovalTrailCard run={readyRun(run)} />)

    const rows = screen.getAllByTestId('approval-trail-step')
    expect(rows).toHaveLength(3)
    approvalTrailSteps(run).forEach((step, i) => {
      expect(within(rows[i]).getByText(String(step.ord1))).toBeTruthy()
    })
  })

  it('a notify step states that nothing was delivered', () => {
    const steps = [stepFixture({ ord: 0, kind: 'notify', state: 'pending', notify_target: 'ap@acme.test', notify_channel: 'email' })]
    const run = runFixture({ steps })

    render(<ApprovalTrailCard run={readyRun(run)} />)

    const note = screen.getByTestId('approval-trail-notify-note')
    expect(note.textContent).toContain('ap@acme.test')
    expect(note.textContent).toContain('email')
    expect(note.textContent).toContain(APPROVAL_TRAIL_COPY.notifyNote)
  })

  it('a cancelled run says it was voided by an edit', () => {
    const run = runFixture({ state: 'cancelled' })

    render(<ApprovalTrailCard run={readyRun(run)} />)

    const banner = screen.getByTestId('approval-trail-voided')
    expect(banner.textContent).toBe(APPROVAL_TRAIL_COPY.voided)
    expect(screen.queryByText(/stale/i)).toBeNull()
  })

  it('an unstaffed holder is warned, not hidden', () => {
    const steps = [stepFixture({ ord: 0, holder: { text: 'Nobody assigned', warn: true } })]
    const run = runFixture({ steps })

    render(<ApprovalTrailCard run={readyRun(run)} />)

    const row = screen.getByTestId('approval-trail-step')
    const holderLine = within(row).getByText('Nobody assigned')
    expect(holderLine.style.color).toBe('var(--status-amber-text)')
    expect(within(row).getByText(APPROVAL_TRAIL_COPY.unstaffedSeat)).toBeTruthy()
  })

  it('an overdue step reads Overdue in red', () => {
    const steps = [stepFixture({ ord: 0, overdue: true, due_at: '2026-07-01T00:00:00Z' })]
    const run = runFixture({ steps })

    render(<ApprovalTrailCard run={readyRun(run)} />)

    const row = screen.getByTestId('approval-trail-step')
    const dueCell = within(row).getByText(APPROVAL_TRAIL_COPY.overdue)
    expect(dueCell.style.color).toBe('var(--status-red-text)')
  })

  it('the decision ledger shows who, when and why', () => {
    const decisions = [decisionFixture({ decision: 'rejected', decided_at: '2026-08-02T09:00:00Z', reason: 'Budget exceeded' })]
    const run = runFixture({ decisions })

    render(<ApprovalTrailCard run={readyRun(run)} />)

    const rows = screen.getAllByTestId('approval-trail-decision')
    expect(rows).toHaveLength(1)
    const [view] = approvalTrailDecisions(run)
    expect(within(rows[0]).getByText(view.outcomeLabel)).toBeTruthy()
    expect(within(rows[0]).getByText(view.actorText)).toBeTruthy()
    expect(within(rows[0]).getByText(view.whenLabel)).toBeTruthy()
    expect(within(rows[0]).getByText('Budget exceeded')).toBeTruthy()
  })

  it('an arm-time closed run is honest about nobody being asked', () => {
    const run = runFixture({ state: 'approved', closed_by: 'system', decisions: [] })

    render(<ApprovalTrailCard run={readyRun(run)} />)

    expect(screen.queryAllByTestId('approval-trail-decision')).toHaveLength(0)
    expect(screen.getByText(APPROVAL_TRAIL_COPY.noDecisions)).toBeTruthy()
  })

  // Design call (orchestrator, not D-35): the row rhythm copies Status history
  // (InvoiceDetail.tsx:1032-1049) -- a flex row with an in-flow, non-absolute connector --
  // not WorkflowSimulator's absolutely-positioned line + CSS-triangle arrowhead
  // (WorkflowSimulator.tsx:78-84). Only the numbered medallion is WorkflowSimulator's.
  it("a step row follows Status history's flex/gap contract, not WorkflowSimulator's absolutely-positioned connector", () => {
    const steps = [
      stepFixture({ ord: 0, workflow_role_title: 'Finance lead' }),
      stepFixture({ ord: 1, workflow_role_title: 'Ops lead' }),
    ]
    const run = runFixture({ steps })

    render(<ApprovalTrailCard run={readyRun(run)} />)

    const rows = screen.getAllByTestId('approval-trail-step')
    expect(rows).toHaveLength(2)
    rows.forEach((row) => {
      expect(row.style.display).toBe('flex')
      expect(row.style.gap).toBe('12px')
      expect(row.style.position).not.toBe('relative')
    })
    const connector = rows[0].querySelector('[style*="var(--line-2)"]')
    expect(connector).toBeTruthy()
    expect((connector as HTMLElement).style.position).not.toBe('absolute')
  })
})
