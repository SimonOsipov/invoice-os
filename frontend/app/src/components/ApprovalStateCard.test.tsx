// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// AUDIT-09-06's RED specs, written before the component (Mode A). ApprovalStateCard.tsx is
// a stub that throws 'not implemented', so every render-based spec below fails on that
// throw; the two source/copy specs fail on a real assertion instead (see their comments).
// Contract: .ralph/AUDIT-09-06-arch.md section 3 (the component), 4.5 (the guarantee
// ledger), 6.1 (the spec table), 7 (the branch ladder).

import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, type AsyncState } from '@invoice-os/api-client'

import * as approvalsLib from '../lib/approvals'
import type { ApprovalRun, ApprovalRunDecision, ApprovalRunStep } from '../lib/approvals'
import { fmtDate } from '../lib/format'

import { ApprovalStateCard } from './ApprovalStateCard'

afterEach(cleanup)

const SOURCE = join(__dirname, 'ApprovalStateCard.tsx')

// arch 3.2's fourteen keys. approvals.ts is NOT this subtask's file for a Mode A commit,
// and the const is RENAMED here (APPROVAL_TRAIL_COPY -> APPROVAL_CARD_COPY, arch 3.5), so
// importing it by either name would either not compile now or not compile later. The ten
// unchanged values are byte-copied from APPROVAL_TRAIL_COPY as it stands today -- that
// byte-identity IS AC-9, and approvalStateCard_copyConstIsPrunedAndByteIdentical pins it
// against the live const the moment the rename lands.
const EXPECTED_COPY = {
  cardTitle: 'Approvals',
  loading: 'Loading approval status…',
  emptyTitle: 'No approval run',
  emptyMessage:
    'Nothing on this invoice is waiting on a sign-off. Either this workspace has no active approval policy, or this invoice has not been validated yet.',
  voided: 'This approval was voided by an edit — the invoice must be approved again from step one.',
  waitingOn: 'Waiting on',
  noPending: 'Nobody is waiting on this invoice.',
  decideAbove: 'Approve or reject from the buttons at the top of this page.',
  unstaffedSeat: 'Unstaffed seat',
  overdue: 'Overdue',
  stateOpen: 'In progress',
  stateApproved: 'Approved',
  stateRejected: 'Rejected',
  stateCancelled: 'Voided',
} as const

type CopyKey = keyof typeof EXPECTED_COPY

// The thirteen APPROVAL_TRAIL_COPY values arch 4.4 deletes, byte-copied from the const as
// it stands today. approvalStateCard_dropsTheStepKindAndStateLabels asserts none of them
// reaches a leaf text node.
const RETIRED_COPY = [
  'Steps',
  'Decisions',
  'No decision has been recorded on this run.',
  'No message is delivered — notifications are recorded but not yet sent.',
  'Settled automatically — nobody was asked.',
  'Approval',
  'Condition',
  'Notification',
  'Auto-approved',
  'Waiting',
  'Signed',
  'Skipped',
  'Rejected',
] as const

// arch 4.3. Every one of these dies with ApprovalTrailCard.
const RETIRED_TESTIDS = [
  'approval-trail-card',
  'approval-trail',
  'approval-trail-state',
  'approval-trail-step',
  'approval-trail-decision',
  'approval-trail-voided',
  'approval-trail-empty',
  'approval-trail-notify-note',
] as const

const liveCopy = (approvalsLib as unknown as Record<string, Record<string, string> | undefined>).APPROVAL_CARD_COPY

// Reached through the namespace, not a named import: arch 3.1 has not landed, and a named
// import of a symbol that does not exist yet would be a compile error instead of a red
// assertion. approvalStateCard_aVoidedRunNeverClaimsAHolder needs it -- the component
// double-guards the voided case, so the predicate's own gate is invisible from the DOM.
const pendingApprovalStep = (approvalsLib as unknown as { pendingApprovalStep?: (run: ApprovalRun) => ApprovalRunStep | null })
  .pendingApprovalStep

// Reads the live const the moment the rename lands, so a user-approved copy revision (F-H)
// is a one-place change; falls back to EXPECTED_COPY while the const is still named for the
// retired card. The byte pin lives in exactly one spec, not in fourteen.
function copyOf(key: CopyKey): string {
  return liveCopy?.[key] ?? EXPECTED_COPY[key]
}

// ---- fixtures. stepFixture/runFixture/readyRun/idleRun/errorRun are
// ApprovalTrailCard.test.tsx:24-78's, unchanged, so the surviving guarantees are
// re-asserted against the same shapes. decisionFixture is kept (arch 6.1 drops it) because
// a dropped-guarantee spec needs a run that DOES carry decisions -- an absence proved on a
// run with no decisions in it proves nothing.

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

// Actor is a literal, not APP_PERSONAS: no surface consumes it any more, so the persona
// import would be an unused coupling.
function decisionFixture(over: Partial<ApprovalRunDecision> = {}): ApprovalRunDecision {
  return {
    run_step_id: 'step-1',
    ord: 0,
    decision: 'approved',
    actor: 'c0000000-0000-0000-0000-000000000001',
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

type RunProp = AsyncState<ApprovalRun | null> & { run: () => void }

function readyRun(data: ApprovalRun): RunProp {
  return { status: 'ready', data, error: null, run: vi.fn() }
}

function idleRun(): RunProp {
  return { status: 'idle', data: null, error: null, run: vi.fn() }
}

function loadingRun(): RunProp {
  return { status: 'loading', data: null, error: null, run: vi.fn() }
}

// The 404 path: getInvoiceApprovalRun returns null, resolveStatus maps null to 'empty' and
// the reducer clears data (async-state.ts). AC-9's branch.
function emptyRun(): RunProp {
  return { status: 'empty', data: null, error: null, run: vi.fn() }
}

function errorRun(): RunProp {
  return { status: 'error', data: null, error: new ApiError('http', 'boom', 500), run: vi.fn() }
}

function card(): HTMLElement {
  return screen.getByTestId('approval-card')
}

// Every non-empty leaf text node under the card. The absence specs assert against this
// array and NOT against queryByText, so each one can carry its positive control on the
// same locator: a card that failed to render leaves the array empty and the positive
// assertion fails before the absence assertion is reached.
function leafTexts(root: HTMLElement): string[] {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  const out: string[] = []
  let node: Node | null
  while ((node = walker.nextNode())) {
    const text = (node.textContent ?? '').trim()
    if (text !== '') out.push(text)
  }
  return out
}

describe('ApprovalStateCard', () => {
  // ---- AC-2 / AC-5 / AC-9: what the card says --------------------------------------

  it('approvalStateCard_rendersStateAndHolder', () => {
    const run = runFixture({
      steps: [stepFixture({ ord: 0, workflow_role_title: 'Finance lead', holder: { text: 'Ada Obi', warn: false } })],
    })

    render(<ApprovalStateCard run={readyRun(run)} />)

    const el = card()
    expect(within(el).getByTestId('approval-state').textContent).toBe(copyOf('stateOpen'))
    expect(within(el).getByTestId('approval-holder').textContent).toContain('Finance lead')
    expect(within(el).getByTestId('approval-holder-name').textContent).toBe('Ada Obi')
    expect(within(el).getByText(copyOf('waitingOn'))).toBeTruthy()
    expect(within(el).getByTestId('approval-decide-hint').textContent).toBe(copyOf('decideAbove'))
    expect(within(el).getByText(copyOf('cardTitle'))).toBeTruthy()
  })

  it('approvalStateCard_noRunIsHonest', () => {
    render(<ApprovalStateCard run={emptyRun()} />)

    const el = card()
    const empty = within(el).getByTestId('approval-empty')
    // AC-9: byte-identical to the retired card's strings.
    expect(within(empty).getByText(copyOf('emptyTitle'))).toBeTruthy()
    expect(within(empty).getByText(copyOf('emptyMessage'))).toBeTruthy()
    // Positive control for the two absences below.
    expect(within(el).queryAllByTestId('approval-empty')).toHaveLength(1)
    expect(within(el).queryAllByTestId('approval-holder')).toHaveLength(0)
    // No run resolved, so no state to pill (arch 3.3 renders the pill only when `view`).
    expect(within(el).queryAllByTestId('approval-state')).toHaveLength(0)
  })

  it('approvalStateCard_showsLoadingNotTheEmptyOrReadyState', () => {
    for (const [label, state] of [
      ['idle', idleRun()],
      ['loading', loadingRun()],
    ] as const) {
      render(<ApprovalStateCard run={state} />)
      const el = card()
      expect(within(el).getByText(copyOf('loading')), label).toBeTruthy()
      expect(within(el).queryAllByTestId('approval-empty'), label).toHaveLength(0)
      expect(within(el).queryAllByTestId('approval-holder'), label).toHaveLength(0)
      expect(within(el).queryAllByTestId('approval-state'), label).toHaveLength(0)
      cleanup()
    }
  })

  it('approvalStateCard_keepsUnstaffedAndOverdue', () => {
    const step = stepFixture({
      ord: 0,
      workflow_role_title: 'Compliance lead',
      holder: { text: 'Nobody assigned', warn: true },
      overdue: true,
    })

    render(<ApprovalStateCard run={readyRun(runFixture({ steps: [step] }))} />)

    let el = card()
    expect(within(el).getByTestId('approval-holder-warn').textContent).toBe(copyOf('unstaffedSeat'))
    expect(within(el).getByTestId('approval-holder-name').style.color).toBe('var(--status-amber-text)')
    const due = within(el).getByTestId('approval-due')
    expect(due.textContent).toBe(copyOf('overdue'))
    expect(due.style.color).toBe('var(--status-red-text)')
    cleanup()

    // Positive control on the same three locators: flip warn and overdue off and the two
    // markers disappear while the holder line stays. Without it the amber/red assertions
    // above could be satisfied by a card that paints every holder amber.
    const calm = stepFixture({ ord: 0, workflow_role_title: 'Compliance lead', holder: { text: 'Ada Obi', warn: false }, overdue: false })
    render(<ApprovalStateCard run={readyRun(runFixture({ steps: [calm] }))} />)
    el = card()
    expect(within(el).queryAllByTestId('approval-holder-warn')).toHaveLength(0)
    expect(within(el).getByTestId('approval-holder-name').style.color).toBe('var(--fg-3)')
    expect(within(el).queryAllByTestId('approval-due')).toHaveLength(0)
  })

  it('approvalStateCard_overdueBeatsAFormattedDueDate', () => {
    const DUE = '2026-07-01T00:00:00Z'

    render(<ApprovalStateCard run={readyRun(runFixture({ steps: [stepFixture({ ord: 0, overdue: true, due_at: DUE })] }))} />)

    let due = within(card()).getByTestId('approval-due')
    expect(due.textContent).toBe(copyOf('overdue'))
    expect(due.textContent).not.toBe(fmtDate(DUE))
    cleanup()

    // Positive control on the same locator: the same due_at DOES render as a date once the
    // server stops calling the step overdue, so the assertion above is about precedence and
    // not about a cell that never renders a date at all.
    render(<ApprovalStateCard run={readyRun(runFixture({ steps: [stepFixture({ ord: 0, overdue: false, due_at: DUE })] }))} />)
    due = within(card()).getByTestId('approval-due')
    expect(due.textContent).toBe(fmtDate(DUE))
    expect(due.textContent).not.toBe(copyOf('overdue'))
  })

  it('approvalStateCard_aPendingStepWithNoRolePrintsNoNullOrUndefined', () => {
    // The shape read_model.go:183-186 produces when the role row is gone.
    const orphan = stepFixture({ ord: 0, workflow_role_key: null, workflow_role_title: null, holder: null })

    render(<ApprovalStateCard run={readyRun(runFixture({ steps: [orphan] }))} />)

    let el = card()
    expect(within(el).getByTestId('approval-holder').textContent).toContain('—')
    expect(within(el).queryAllByTestId('approval-holder-name')).toHaveLength(0)
    const texts = leafTexts(el)
    expect(texts).toContain('—')
    expect(texts.join(' ')).not.toMatch(/\bnull\b|\bundefined\b/)
    cleanup()

    // Positive control on the same locator: a step that DOES carry a holder renders exactly
    // one holder-name line, so the zero above is the null-safety and not a dead testid.
    render(<ApprovalStateCard run={readyRun(runFixture({ steps: [stepFixture({ ord: 0, holder: { text: 'Ada Obi', warn: false } })] }))} />)
    el = card()
    expect(within(el).queryAllByTestId('approval-holder-name')).toHaveLength(1)
  })

  // ---- the pending-step predicate --------------------------------------------------

  it('approvalStateCard_aVoidedRunNeverClaimsAHolder', () => {
    // P-13: CancelLiveRunTx (internal/approval/engine.go) updates approval_runs ONLY and
    // never touches approval_run_steps, so a voided run keeps its steps 'pending'. A
    // pendingApprovalStep that does not gate on run.state === 'open' renders
    // "Waiting on Musa Danjuma" directly beside a "Voided" pill.
    const steps = [
      stepFixture({ ord: 0, kind: 'approval', state: 'pending', workflow_role_title: 'Compliance lead', holder: { text: 'Musa Danjuma', warn: false } }),
    ]

    // The predicate is asserted DIRECTLY, and first. Arch 3.3's component double-guards
    // this case -- its `view.voided ?` ternary shadows `pending` -- so deleting the gate
    // from pendingApprovalStep changes nothing in the DOM and the render assertions below
    // survive that mutation. Measured, not assumed: the mutation was run and only these
    // two lines killed it.
    expect(pendingApprovalStep, 'pendingApprovalStep is not exported from lib/approvals yet (arch 3.1)').toBeTypeOf('function')
    expect(
      pendingApprovalStep?.(runFixture({ state: 'cancelled', steps })),
      'a voided run keeps its pending steps (P-13); the predicate must gate on run.state === open',
    ).toBeNull()
    // Positive control on the same function: the identical steps on an open run DO yield
    // the step, so the null above is the gate and not a predicate that never matches.
    expect(pendingApprovalStep?.(runFixture({ state: 'open', steps }))).toBe(steps[0])

    // Positive control FIRST, on byte-identical steps: the only difference between the two
    // renders below is run.state, so the absence cannot be explained by a fixture that lost
    // its pending step or by a card that never names a holder.
    render(<ApprovalStateCard run={readyRun(runFixture({ state: 'open', steps }))} />)
    expect(leafTexts(card())).toContain('Musa Danjuma')
    cleanup()

    const voided = runFixture({ state: 'cancelled', steps })
    // Fixture guard: the steps really are still pending on the voided run.
    expect(voided.steps.filter((s) => s.kind === 'approval' && s.state === 'pending')).toHaveLength(1)

    render(<ApprovalStateCard run={readyRun(voided)} />)

    const el = card()
    expect(within(el).getByTestId('approval-voided').textContent).toBe(copyOf('voided'))
    expect(within(el).getByTestId('approval-state').textContent).toBe(copyOf('stateCancelled'))
    expect(within(el).queryAllByTestId('approval-holder')).toHaveLength(0)
    expect(within(el).queryAllByTestId('approval-holder-name')).toHaveLength(0)
    const texts = leafTexts(el)
    expect(texts).toContain(copyOf('voided'))
    expect(texts).not.toContain('Musa Danjuma')
    expect(texts).not.toContain('Compliance lead')
    expect(texts).not.toContain(copyOf('waitingOn'))
  })

  it('approvalStateCard_aClosedRunNamesNobody', () => {
    const run = runFixture({ state: 'approved', steps: [], closed_at: '2026-08-02T09:00:00Z' })

    render(<ApprovalStateCard run={readyRun(run)} />)

    const el = card()
    expect(within(el).getByTestId('approval-no-pending').textContent).toBe(copyOf('noPending'))
    expect(within(el).getByTestId('approval-state').textContent).toBe(copyOf('stateApproved'))
    expect(within(el).queryAllByTestId('approval-holder')).toHaveLength(0)
    expect(within(el).queryAllByTestId('approval-voided')).toHaveLength(0)
  })

  it('approvalStateCard_ignoresANonApprovalPendingStep', () => {
    // P-14: all three server predicates carry kind = 'approval' explicitly
    // (decision.go:162-165, gate.go:133-136, gate.go:243-246).
    const steps = [
      stepFixture({ ord: 0, kind: 'notify', state: 'pending', workflow_role_title: 'AP Team' }),
      stepFixture({ ord: 1, kind: 'approval', state: 'pending', workflow_role_title: 'Finance lead' }),
    ]

    render(<ApprovalStateCard run={readyRun(runFixture({ steps }))} />)

    const el = card()
    expect(within(el).queryAllByTestId('approval-holder')).toHaveLength(1)
    const texts = leafTexts(el)
    expect(texts).toContain('Finance lead')
    expect(texts).not.toContain('AP Team')
  })

  it('approvalStateCard_namesTheLowestOrdPendingStepOnly', () => {
    // Supplied out of array order: a .find() that trusts array position picks ord 2.
    const steps = [
      stepFixture({ ord: 2, kind: 'approval', state: 'pending', workflow_role_title: 'Tax Reviewer' }),
      stepFixture({ ord: 1, kind: 'approval', state: 'pending', workflow_role_title: 'Engagement Manager' }),
    ]

    render(<ApprovalStateCard run={readyRun(runFixture({ steps }))} />)

    const el = card()
    expect(within(el).queryAllByTestId('approval-holder')).toHaveLength(1)
    const texts = leafTexts(el)
    expect(texts).toContain('Engagement Manager')
    expect(texts).not.toContain('Tax Reviewer')
  })

  // ---- F-E: the error branch stays -------------------------------------------------

  it('approvalStateCard_a500KeepsTheRetryBesideTheStripsFalseClaim', () => {
    // F-E. The strip's mount gate excludes only 'idle' and 'loading', so an errored
    // approval fetch still renders node 3 as 'Not required' from run = null
    // (invoiceStrip.ts). This branch is the only thing contradicting that claim.
    // G-12: asserted positively and INSIDE approval-card -- the retired
    // queryByText('Something went wrong') was scoped to nothing and would go green if some
    // other card grew an error state. The Retry lookup is scoped for the same reason (F-U
    // M-2: an unscoped Retry now also matches the Activity card's ErrorState).
    const state = errorRun()

    render(<ApprovalStateCard run={state} />)

    const el = card()
    expect(within(el).getByText('Something went wrong')).toBeTruthy()
    expect(within(el).getByText('boom')).toBeTruthy()
    fireEvent.click(within(el).getByRole('button', { name: 'Retry' }))
    expect(state.run).toHaveBeenCalledTimes(1)
  })

  // ---- AC-3 / AC-4: source scans ---------------------------------------------------

  it('approvalStateCard_readsTheRunNotAuditPayloads', () => {
    const src = readFileSync(SOURCE, 'utf8')
    // Control needle: prove the scan read THIS file before trusting its silence.
    expect(src, 'the source scan read the wrong file').toContain('export function ApprovalStateCard')
    // Multi-line, not /^import .*$/: a braced import list spans lines, and a line-bounded
    // regex captures only `import {` -- every specifier below would then be silently absent
    // and the ban half of this scan would pass on any file forever (F-M).
    const imports = src.match(/^import\b[\s\S]*?from '[^']*'/gm) ?? []
    expect(imports.length, 'the import scan found nothing -- the regex, not the file, is wrong').toBeGreaterThanOrEqual(3)
    const joined = imports.join('\n')
    expect(joined, 'the card reads the approval projection').toContain('../lib/approvals')
    expect(joined, 'ErrorState/Loading/AsyncState come from the package').toContain('@invoice-os/api-client')
    // Positive half of AC-3: the card consumes the projection rather than re-deriving the
    // pending step inline, so the server's predicate lives in exactly one place (arch 3.1).
    expect(src, 'the card must call approvalStateView').toContain('approvalStateView(')
    for (const banned of ['../lib/audit', 'auditEventView', 'auditVocabulary', 'AuditRow']) {
      expect(joined, banned + ' would source the card from audit payloads (AC-3, P-11)').not.toContain(banned)
    }
    // P-10: the PAGE owns GET /approval; a fetch here would give the strip and this card
    // two answers. Scanned over the IMPORTS, not the whole source -- arch 3.3's own comment
    // names useAsync when it explains why the card is not a fetcher, and a whole-file ban
    // would red on the correct implementation.
    for (const banned of ['useAsync', 'authedFetch', 'getInvoiceApprovalRun']) {
      expect(joined, banned + ' would make the card a fetcher (P-10, AUDIT-09-02 arch 0)').not.toContain(banned)
    }
  })

  it('approvalStateCard_hasNoDecisionControl', () => {
    // Positive control on the same locator, FIRST: the error branch DOES put exactly one
    // button inside the card, so the four zeros below are the absence of a decision control
    // and not a queryAllByRole that never matches anything.
    render(<ApprovalStateCard run={errorRun()} />)
    expect(within(card()).queryAllByRole('button')).toHaveLength(1)
    expect(within(card()).getByRole('button', { name: 'Retry' })).toBeTruthy()
    cleanup()

    const pending = runFixture({ steps: [stepFixture({ ord: 0, holder: { text: 'Ada Obi', warn: false } })] })
    for (const [label, state] of [
      ['idle', idleRun()],
      ['loading', loadingRun()],
      ['empty', emptyRun()],
      ['ready', readyRun(pending)],
    ] as const) {
      render(<ApprovalStateCard run={state} />)
      expect(within(card()).queryAllByRole('button'), label).toHaveLength(0)
      cleanup()
    }

    const src = readFileSync(SOURCE, 'utf8')
    expect(src, 'the source scan read the wrong file').toContain('export function ApprovalStateCard')
    for (const banned of ['detail-approve', 'detail-reject', 'Remind', 'decideInvoice', '<button']) {
      expect(src, banned + ": the only Approve control on the page is the header's (AC-4)").not.toContain(banned)
    }
  })

  // ---- arch 4.4 / 4.5: what is dropped, and proved dropped -------------------------

  it('approvalStateCard_copyConstIsPrunedAndByteIdentical', () => {
    // Red today on the first assertion, not on a throw: the rename in arch 3.5 has not
    // landed. AC-9's byte-identity lives here and nowhere else -- every other spec reads
    // copyOf(), which returns the live value once this passes.
    expect(liveCopy, 'APPROVAL_CARD_COPY is not exported from lib/approvals yet (arch 3.5)').toBeTruthy()
    for (const [key, want] of Object.entries(EXPECTED_COPY)) {
      expect(liveCopy?.[key], 'APPROVAL_CARD_COPY.' + key).toBe(want)
    }
    expect(Object.keys(liveCopy ?? {}).sort(), 'arch 3.2 pins fourteen keys').toEqual(Object.keys(EXPECTED_COPY).sort())
    expect(
      (approvalsLib as unknown as Record<string, unknown>).APPROVAL_TRAIL_COPY,
      'APPROVAL_TRAIL_COPY names a card this subtask deletes (arch 3.5)',
    ).toBeUndefined()
    for (const gone of ['approvalTrailSteps', 'approvalTrailDecisions']) {
      expect((approvalsLib as unknown as Record<string, unknown>)[gone], gone + ' lost its only consumer').toBeUndefined()
    }
  })

  it('approvalStateCard_dropsTheStepLadder', () => {
    // Retired guarantee: "the ladder renders one row per step in ord order". The full step
    // list is now on NO surface (arch 4.5) -- what replaces it is the claim that exactly
    // one step is named, and it is the lowest-ord pending one.
    const steps = [
      stepFixture({ ord: 0, workflow_role_title: 'Engagement Manager', holder: { text: 'Ada Obi', warn: false } }),
      stepFixture({ ord: 1, workflow_role_title: 'Tax Reviewer', holder: { text: 'Musa Danjuma', warn: false } }),
      stepFixture({ ord: 2, workflow_role_title: 'Managing Partner', holder: { text: 'Ngozi Eze', warn: false } }),
    ]

    render(<ApprovalStateCard run={readyRun(runFixture({ steps }))} />)

    const el = card()
    const texts = leafTexts(el)
    // Positive control on the same locator: the ord-0 seat IS named.
    expect(texts).toContain('Engagement Manager')
    expect(texts).toContain('Ada Obi')
    expect(within(el).queryAllByTestId('approval-holder')).toHaveLength(1)
    expect(within(el).queryAllByTestId('approval-holder-name')).toHaveLength(1)
    for (const laddered of ['Tax Reviewer', 'Musa Danjuma', 'Managing Partner', 'Ngozi Eze']) {
      expect(texts, laddered + ' is a ladder row; this card names one step').not.toContain(laddered)
    }
  })

  it('approvalStateCard_dropsTheDecisionLedger', () => {
    // Retired guarantee: "the decision ledger shows who, when and why". Moved to the
    // Activity feed (D-AC-11, subtask 07) -- restating it in a 340px rail is the
    // third-card restatement this story exists to prevent.
    const run = runFixture({
      steps: [stepFixture({ ord: 2, workflow_role_title: 'Finance lead', holder: { text: 'Ada Obi', warn: false } })],
      decisions: [
        decisionFixture({ ord: 0, decision: 'approved', reason: 'Cleared by finance' }),
        decisionFixture({ ord: 1, decision: 'rejected', reason: 'Budget exceeded', decided_at: '2026-08-03T10:00:00Z' }),
      ],
    })
    // Fixture guard: the run really does carry decisions, so the absence below is the card
    // dropping the ledger and not an empty ledger rendering as nothing.
    expect(run.decisions).toHaveLength(2)

    render(<ApprovalStateCard run={readyRun(run)} />)

    const texts = leafTexts(card())
    // Positive control on the same locator.
    expect(texts).toContain('Finance lead')
    for (const ledger of ['Cleared by finance', 'Budget exceeded', 'c0000000-0000-0000-0000-000000000001']) {
      expect(texts, ledger + ' belongs to the Activity feed, not this card').not.toContain(ledger)
    }
  })

  it('approvalStateCard_dropsTheNotifyDisclosure', () => {
    // Retired guarantee: the third "no message is delivered" disclosure. DROPPED with the
    // ladder (arch 4.5); docs/approvals.md's constant count must be corrected in the same
    // commit (F-S). A notify step is rendered nowhere on the invoice page any more.
    const steps = [
      stepFixture({ ord: 0, kind: 'notify', state: 'pending', workflow_role_title: 'AP Team', notify_target: 'ap@acme.test', notify_channel: 'email' }),
      stepFixture({ ord: 1, workflow_role_title: 'Finance lead', holder: { text: 'Ada Obi', warn: false } }),
    ]

    render(<ApprovalStateCard run={readyRun(runFixture({ steps }))} />)

    const el = card()
    const texts = leafTexts(el)
    // Positive control on the same locator.
    expect(texts).toContain('Finance lead')
    expect(within(el).queryAllByTestId('approval-trail-notify-note')).toHaveLength(0)
    for (const dropped of ['No message is delivered — notifications are recorded but not yet sent.', 'ap@acme.test', 'email', 'AP Team']) {
      expect(texts, dropped + ' is notify-step chrome this card does not render').not.toContain(dropped)
    }
  })

  it('approvalStateCard_dropsTheAutoApproveNote', () => {
    // Retired guarantee: "Settled automatically — nobody was asked." An autoapprove-settled
    // run closes as 'approved' at arm time (engine.go:194-199); the card now shows the
    // Approved pill and noPending, and no longer says nobody was asked. Named as dropped in
    // arch 4.5 -- the feed carries the same fact by inference only.
    const run = runFixture({
      state: 'approved',
      closed_at: '2026-08-01T00:00:10Z',
      closed_by: 'system',
      steps: [stepFixture({ ord: 0, kind: 'autoapprove', state: 'satisfied', workflow_role_title: 'Ops Bot', satisfied_at: '2026-08-01T00:00:10Z' })],
    })

    render(<ApprovalStateCard run={readyRun(run)} />)

    const texts = leafTexts(card())
    // Positive control on the same locator: the run's outcome IS on screen.
    expect(texts).toContain(copyOf('stateApproved'))
    expect(texts).toContain(copyOf('noPending'))
    for (const dropped of ['Settled automatically — nobody was asked.', 'Auto-approved', 'Ops Bot']) {
      expect(texts, dropped + ' has no surface after this subtask').not.toContain(dropped)
    }
  })

  it('approvalStateCard_dropsTheStepKindAndStateLabels', () => {
    // Retired guarantee: the ladder's kind and state labels. All thirteen pruned
    // APPROVAL_TRAIL_COPY values (arch 4.4) must be absent from a run that would have
    // exercised every one of them.
    const steps = [
      stepFixture({ ord: 0, kind: 'notify', state: 'satisfied', workflow_role_title: 'AP Team' }),
      stepFixture({ ord: 1, kind: 'condition', state: 'skipped', workflow_role_title: 'Threshold' }),
      stepFixture({ ord: 2, kind: 'approval', state: 'pending', workflow_role_title: 'Finance lead' }),
      stepFixture({ ord: 3, kind: 'autoapprove', state: 'pending', workflow_role_title: 'Ops Bot' }),
    ]

    render(<ApprovalStateCard run={readyRun(runFixture({ steps }))} />)

    const texts = leafTexts(card())
    // Positive control on the same locator and the same axis as the ban: two leaves the
    // card DOES author survive the walk.
    expect(texts).toContain(copyOf('waitingOn'))
    expect(texts).toContain('Finance lead')
    for (const retired of RETIRED_COPY) {
      expect(texts, retired + ' is a retired APPROVAL_TRAIL_COPY value').not.toContain(retired)
    }
  })

  it('approvalStateCard_dropsTheRetiredTestids', () => {
    const run = runFixture({
      steps: [stepFixture({ ord: 0, workflow_role_title: 'Finance lead', holder: { text: 'Nobody assigned', warn: true }, overdue: true })],
    })

    render(<ApprovalStateCard run={readyRun(run)} />)

    const el = card()
    // Positive control on the same locator: every replacement testid this fixture should
    // produce is present exactly once (arch 4.3), so the eight zeros below cannot be
    // satisfied by a card that rendered nothing.
    for (const live of ['approval-state', 'approval-holder', 'approval-holder-name', 'approval-holder-warn', 'approval-due', 'approval-decide-hint']) {
      expect(within(el).queryAllByTestId(live), live + ' is missing').toHaveLength(1)
    }
    for (const retired of RETIRED_TESTIDS) {
      expect(screen.queryAllByTestId(retired), retired + ' died with ApprovalTrailCard').toHaveLength(0)
    }
  })

  it('approvalStateCard_authorsNoCopyOfItsOwn', () => {
    // Retired-spec idiom (ApprovalTrailCard.test.tsx:266), simpler because there are no
    // projection label maps: every leaf must be a copy value, a string the FIXTURE
    // supplied, or one of the two glyphs the component splices in.
    const step = stepFixture({
      ord: 0,
      workflow_role_title: 'Compliance lead',
      holder: { text: 'Nobody assigned', warn: true },
      overdue: true,
      due_at: '2026-07-01T00:00:00Z',
    })

    render(<ApprovalStateCard run={readyRun(runFixture({ steps: [step] }))} />)

    const knownCopy = new Set<string>([...Object.values(EXPECTED_COPY), ...Object.values(liveCopy ?? {})])
    // Derived from the fixture object, never hand-typed twice.
    const knownData = new Set<string>([step.workflow_role_title, step.holder?.text].filter((s): s is string => s != null))
    const knownGlyphs = new Set(['·', '—'])

    const unexplained: string[] = []
    let matched = 0
    for (const text of leafTexts(card())) {
      if (knownCopy.has(text) || knownData.has(text) || knownGlyphs.has(text)) {
        matched++
        continue
      }
      unexplained.push(text)
    }

    // Needle: any string the component authors itself lands here, not silently in `matched`.
    expect(unexplained).toEqual([])
    // Floor: cardTitle, stateOpen, waitingOn, roleTitle, holderText, unstaffedSeat, overdue,
    // decideAbove. A card that rendered nothing would vacuously pass the line above.
    expect(matched).toBeGreaterThanOrEqual(8)
  })
})
