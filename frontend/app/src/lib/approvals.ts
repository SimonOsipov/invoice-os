// Approvals screen pure core + fan-out client (APPR-12-02, task-527), covered by
// approvals.test.ts (A02-1..A02-18). Also carries the invoice-detail run read model +
// decide client (APPR-13-01, task-550) and the trail projection + copy consts
// (APPR-13-02, task-552) -- each carries its own comment block further down.
//
// listAwaitingApproval wraps invoices.ts's listInvoices, forcing awaiting_approval=true
// (ListFilter.AwaitingApproval, internal/invoice/handlers.go:349-451, shipped APPR-08-07)
// regardless of what opts carries -- the Omit<> below makes that a compile-time
// guarantee too. Returns the envelope WHOLE, same contract listInvoices already has.
//
// isApprovableRow/approvalRowView read can_approve/approve_blocked_reason straight off
// InvoiceRecord (APPR-12-09, task-526) -- the server's own answer, fail-closed (U5): ONE
// clause, no run_state conjunct. pending_holder_warn stays a WARNING, never a gate (D-p).
//
// approveInvoices: POST /v1/invoices/{id}/approvals, one per id (DecideHandler,
// internal/approval/handlers.go:262-308; route cmd/invoice/main.go:235). Body
// {decision:'approved'} (handlers.go:252-255 -- `reason` is required only for
// 'rejected', so it's never sent here). Sequential, concurrency exactly 1: request n+1
// waits for n to settle, since each POST opens a tenant transaction over the same run
// rows. A per-item non-2xx -- 401 included, authedFetch.ts:23-29's own rethrow -- is
// caught INSIDE the loop and never aborts the run; this fan-out has no pre-flight step
// that could reject the whole call. `message` on a failed result is the server's own
// error text, read off the caught error's `.message` (ApiError extends Error,
// packages/api-client/src/client.ts:9-21) byte-identical -- no SPA-authored fallback
// ([gates-on-the-wire]). Endpoint error set (decisionStatusForErr, handlers.go:225-246):
// 400 body/decision/reason validation, 401 unauthorized, 403 AXIS-1/AXIS-2 role
// refusals, 404 no run, 409 run already closed / invoice no longer awaiting approval,
// 500. No 422.
//
// Every user-visible string in this feature is declared here
// ([bulk-copy-lives-in-the-lib]): approvalsBarView's generated fields plus
// APPROVALS_COPY's static chrome. The confirm copy names the ACTION, never the OUTCOME
// (epic Q12) -- the page is a snapshot and another approver can decide a row between
// the fetch and the confirm.

import { ApiError } from '@invoice-os/api-client'

import { actorLabel } from './actor'
import { fmtDate, fmtDateTime } from './format'
import { listInvoices, type InvoiceListResponse, type InvoiceRecord, type ListInvoicesOptions } from './invoices'
import type { AuthedFetch } from './portfolio'

export type ApprovalPhase = 'idle' | 'armed' | 'submitting'

// step / role / holder-warning / due / overdue / approve_blocked_reason -- wire values
// passed through, except roleLabel's own em-dash fallback for a null pending_role_title
// (A02-15; approvalTrailSteps's roleTitle repeats the same em-dash convention below).
export interface ApprovalRowView {
  approvable: boolean
  pendingOrd: number | null
  // Human-facing step label (G4, APPR-12-03): pending_ord is 0-based on the wire
  // (gate_test.go:1086 pins Ord 0 as a legitimate pending step) and null on a row with
  // no run at all (store.go:691-694's vacuous NOT EXISTS) -- +1 and an em dash live
  // here, never in the component.
  stepLabel: string
  roleLabel: string
  pendingHolderWarn: boolean
  dueAt: string | null
  overdue: boolean
  blockedReason: string | null
}

// Mirrors BulkBarView (reviewBatch.ts), collapsed to ONE action/gate -- this screen has
// no "approve all on page" variant, so canSubmitAll/submitAllLabel have no counterpart.
export interface ApprovalsBarView {
  visible: boolean
  eligible: string[]
  notReady: number
  countLabel: string
  note: string | null
  submitLabel: string
  confirmPrompt: string
  confirmDetail: string
  confirmLabel: string
  canApprove: boolean
}

// One entry per requested id, in request order -- never a count. `message` carries the
// server's own error text byte-identically ([gates-on-the-wire]); this file authors no
// fallback string for it.
export type ApproveResult = { id: string; ok: true } | { id: string; ok: false; message: string }

export interface ApprovalResultRow {
  invoiceNumber: string
  ok: boolean
  label: string
  message: string | null
}

// Forces awaitingApproval:true regardless of what opts carries -- the Omit below makes
// that a compile-time guarantee, not just a default.
export async function listAwaitingApproval(
  authedFetch: AuthedFetch,
  base: string,
  opts: Omit<ListInvoicesOptions, 'awaitingApproval'> = {},
): Promise<InvoiceListResponse> {
  return listInvoices(authedFetch, base, { ...opts, awaitingApproval: true })
}

// The server's own answer, fail-closed -- ONE clause, no client-side conjuncts (U5).
export function isApprovableRow(row: Pick<InvoiceRecord, 'can_approve'>): boolean {
  return row.can_approve === true
}

export function approvableIds(rows: InvoiceRecord[]): string[] {
  return rows.filter((row) => isApprovableRow(row)).map((row) => row.id)
}

// A plain filter over approvableIds(rows), a new array every call -- no instance-identity
// guarantee here; that lives in [APPR-12-04]'s component effect (Plan gap G1).
export function pruneApprovalSelection(sel: string[], rows: InvoiceRecord[]): string[] {
  const approvable = new Set(approvableIds(rows))
  return sel.filter((id) => approvable.has(id))
}

// Mirrors invoices.ts's selectAllState -- `every()` over an empty array is vacuously
// true, so the empty-set case is guarded explicitly rather than relying on it.
export function approvalSelectAllState(selected: string[], rows: InvoiceRecord[]): 'none' | 'some' | 'all' {
  const approvable = approvableIds(rows)
  if (approvable.length === 0) return 'none'
  const selectedSet = new Set(selected)
  const matched = approvable.filter((id) => selectedSet.has(id)).length
  if (matched === 0) return 'none'
  return matched === approvable.length ? 'all' : 'some'
}

export function approvalRowView(
  row: Pick<InvoiceRecord, 'can_approve' | 'approve_blocked_reason' | 'approval'>,
): ApprovalRowView {
  const approval = row.approval
  const pendingOrd = approval?.pending_ord ?? null
  return {
    approvable: isApprovableRow(row),
    pendingOrd,
    stepLabel: pendingOrd == null ? '—' : `Step ${pendingOrd + 1}`,
    roleLabel: approval?.pending_role_title ?? '—',
    pendingHolderWarn: approval?.pending_holder_warn === true,
    dueAt: approval?.due_at ?? null,
    overdue: approval?.overdue === true,
    blockedReason: row.approve_blocked_reason,
  }
}

export function approvalsBarView(
  selected: string[],
  rows: InvoiceRecord[],
  phase: ApprovalPhase,
  pageLoading: boolean,
): ApprovalsBarView {
  // CONSUMED, never re-derived: this exact array is what the confirm handler sends.
  const eligible = pruneApprovalSelection(selected, rows)
  const pageEligible = approvableIds(rows).length
  const notReady = rows.length - pageEligible
  const n = eligible.length
  // ONE gate, shared by both actions: nothing may be sent while the page is being
  // replaced, and nothing may be sent twice.
  const open = !pageLoading && phase !== 'submitting'

  return {
    visible: n > 0,
    eligible,
    notReady,
    // The scope is IN the string -- "on this page", not a tenant-wide count.
    countLabel: `${n} selected on this page`,
    note:
      notReady > 0
        ? `${notReady} of the ${rows.length} ${rows.length === 1 ? 'row' : 'rows'} on this page aren't eligible for approval.`
        : null,
    submitLabel: `Approve ${n} on this page`,
    // Singular at one, so the prompt never reads "Approve 1 invoices".
    confirmPrompt: `Approve ${n} ${n === 1 ? 'invoice' : 'invoices'}?`,
    // Names the ACTION, never claims the OUTCOME: the page is a snapshot, and another
    // approver may act on one of these rows between this fetch and the confirm click.
    confirmDetail: `This can't be undone once you confirm, and another approver may have already acted on one of these rows.`,
    confirmLabel: `Yes, approve ${n} now`,
    canApprove: n > 0 && open,
  }
}

// Sequential fan-out, concurrency exactly 1: request n+1 is not issued until n settles.
// A per-item non-2xx (401 included) never aborts the run -- only a pre-flight failure
// rejects the whole call.
export async function approveInvoices(
  authedFetch: AuthedFetch,
  base: string,
  ids: string[],
  onProgress?: (result: ApproveResult, index: number) => void,
  // Checked at the row BOUNDARY only, never forwarded into authedFetch below -- an
  // already-sent row always settles (A16-4b); the next row just never fires (APPR-16-04).
  signal?: AbortSignal,
): Promise<ApproveResult[]> {
  const results: ApproveResult[] = []
  for (const [index, id] of ids.entries()) {
    if (signal?.aborted) break
    let result: ApproveResult
    try {
      await authedFetch<unknown>(`${base}/api/invoice/v1/invoices/${id}/approvals`, {
        method: 'POST',
        body: { decision: 'approved' },
      })
      result = { id, ok: true }
    } catch (e) {
      // authedFetch always rejects with ApiError (network/http/malformed), which
      // extends Error -- `.message` is the server's own text on the 'http' path.
      result = { id, ok: false, message: e instanceof Error ? e.message : String(e) }
    }
    results.push(result)
    onProgress?.(result, index)
  }
  return results
}

export function approvalOutcome(results: ApproveResult[], numbersById: Map<string, string>): ApprovalResultRow[] {
  return results.map((r) => ({
    invoiceNumber: numbersById.get(r.id) ?? r.id,
    ok: r.ok,
    // The row label, not the reason -- `message` (below) still carries the server's
    // own text for the failure case, byte-identical.
    label: r.ok ? 'Approved' : 'Not approved',
    message: r.ok ? null : r.message,
  }))
}

// Progress during the fan-out (D-b4, G-04-A), driven by approveInvoices' onProgress.
// APPROVALS_COPY.sending shows ACTIVITY; N sequential requests also owe a count. Names
// what was SENT, never how it went -- the outcomes are the results panel's job.
export function approvalProgressLabel(done: number, total: number): string {
  return `${done} of ${total} sent…`
}

// A template, so a plain APPROVALS_COPY key cannot hold it (G-04-F).
export function approvalSelectRowLabel(invoiceNumber: string): string {
  return `Select invoice ${invoiceNumber}`
}

// Static chrome only -- count-dependent copy lives on ApprovalsBarView instead.
//
// eyebrow/h1/subtitle through overdue (G2, APPR-12-03): the queue's own chrome --
// Loading/EmptyState/ErrorState all take caller-supplied strings
// (packages/api-client/src/components/{Loading,EmptyState,ErrorState}.tsx), so these
// would otherwise land inline in ApprovalsView.tsx. No "New invoice" CTA here --
// creating an invoice is the wrong action on an approval queue.
export const APPROVALS_COPY = {
  clear: 'Clear',
  cancel: 'Cancel',
  // The subtitle's own fallback: it renders as copy, so it lives here rather than as an
  // inline `??` operand in the component (LIB-SCAN-A, A04-11).
  tenantFallback: 'Your workspace',
  selectAllLabel: 'Select every invoice on this page you can approve',
  sending: 'Approving…',
  // D-25: states why the pager freezes during a fan-out (APPR-16-04).
  pagerReason: 'Paging is paused while approvals are sending.',
  resultInvoice: 'Invoice #',
  resultOutcome: 'Result',
  eyebrow: 'AWAITING YOUR APPROVAL',
  h1: 'Approvals',
  subtitle: 'invoices waiting on your sign-off.',
  loading: 'Loading approvals…',
  colInvoice: 'Invoice #',
  colBuyer: 'Buyer',
  colAmount: 'Amount',
  colStep: 'Step',
  colRole: 'Role',
  colDue: 'Due',
  emptyTitle: 'Nothing awaiting approval',
  emptyMessage: 'Invoices land here once they pass validation and a policy is armed.',
  emptyPageTitle: 'No approvals on this page',
  emptyPageMessage: 'Go back to see the rest of the queue.',
  unstaffedSeat: 'Unstaffed seat',
  overdue: 'Overdue',
} as const

// ---- Approval-run wire types (APPR-13-01), mirrored key-for-key from
// internal/approval/read_model.go's Resolved/RunStep/RunDecision/Run -- the app-side
// counterpart to e2e/api/client.ts's ApprovalResolved/ApprovalRunStep/
// ApprovalRunDecision/ApprovalRun. kind/state stay `string`, never a union: the DB
// column is untyped, and a union would make an unrecognised value a type error the SPA
// cannot represent honestly.

export interface ApprovalResolved {
  text: string
  warn: boolean
}

export interface ApprovalRunStep {
  ord: number
  kind: string
  state: string
  workflow_role_key: string | null
  workflow_role_title: string | null
  holder: ApprovalResolved | null
  sla_hours: number | null
  due_at: string | null
  overdue: boolean
  satisfied_at: string | null
  satisfied_by: string | null
  notify_target: string | null
  notify_channel: string | null
}

export interface ApprovalRunDecision {
  run_step_id: string
  ord: number
  decision: string
  actor: string
  decided_at: string
  reason: string | null
}

export interface ApprovalRun {
  run_id: string
  state: string
  opened_at: string
  closed_at: string | null
  closed_by: string | null
  steps: ApprovalRunStep[]
  decisions: ApprovalRunDecision[]
}

// Mirrors isUnauthorized's exact shape (authedFetch.ts:16-18), 404 swapped for 401.
export function isNoApprovalRun(e: unknown): boolean {
  return e instanceof ApiError && e.kind === 'http' && e.status === 404
}

// Resolves the run, null on 404. Catch sits OUTSIDE authedFetch so the 401->sign-out
// seam still fires on a 401 -- every non-404 error rethrows unwrapped.
export async function getInvoiceApprovalRun(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
): Promise<ApprovalRun | null> {
  try {
    return await authedFetch<ApprovalRun>(`${base}/api/invoice/v1/invoices/${id}/approval`)
  } catch (e) {
    if (isNoApprovalRun(e)) return null
    throw e
  }
}

// POSTs the decision. Body omits `reason` on 'approved'; 'rejected' sends it verbatim
// and untrimmed -- the server does the trimming.
export async function decideInvoice(
  authedFetch: AuthedFetch,
  base: string,
  id: string,
  decision: 'approved' | 'rejected',
  reason?: string,
): Promise<ApprovalRun> {
  const body = decision === 'rejected' ? { decision, reason } : { decision }
  return authedFetch<ApprovalRun>(`${base}/api/invoice/v1/invoices/${id}/approvals`, {
    method: 'POST',
    body,
  })
}

// Mirrors invoices.ts's canResolveOutside -- the reject-reason trim guard.
export function canRejectReason(reason: string): boolean {
  return reason.trim() !== ''
}

// 0/1/2-sentence de-dup for the approve/reject blocked reasons: null drops out,
// byte-identical strings collapse to one.
export function decisionBlockedReasons(approve: string | null, reject: string | null): string[] {
  if (approve == null) return reject == null ? [] : [reject]
  if (reject == null || reject === approve) return [approve]
  return [approve, reject]
}

// ---- Trail projection (APPR-13-02, task-552): the pure core the invoice-detail trail
// card renders. holderText is a straight passthrough of step.holder?.text -- never
// re-derived through ./roles's resolve()/inspectorResolve() (D-34), which need a
// Role[]/Member[] this projection never receives.

export interface TrailStepView {
  ord1: number
  kind: string
  kindLabel: string
  stateLabel: string
  roleTitle: string
  holderText: string | null
  holderWarn: boolean
  dueLabel: string | null
  overdue: boolean
  notifyNote: string | null
  // AC-4 (card renders "<target> · <channel>" on the notify row): straight passthrough
  // of notify_target/notify_channel, null for every non-notify kind exactly as
  // notifyNote is -- avoids a second read of the raw step for the same data (D-34).
  notifyTarget: string | null
  notifyChannel: string | null
}

export interface TrailDecisionView {
  ord1: number
  outcomeLabel: string
  actorText: string
  actorMono: boolean
  whenLabel: string
  reason: string | null
}

export function approvalRunStateView(state: string): { label: string; tone: 'amber' | 'green' | 'red' | 'muted' } {
  const known: Record<string, { label: string; tone: 'amber' | 'green' | 'red' | 'muted' }> = {
    open: { label: APPROVAL_TRAIL_COPY.stateOpen, tone: 'amber' },
    approved: { label: APPROVAL_TRAIL_COPY.stateApproved, tone: 'green' },
    rejected: { label: APPROVAL_TRAIL_COPY.stateRejected, tone: 'red' },
    cancelled: { label: APPROVAL_TRAIL_COPY.stateCancelled, tone: 'muted' },
  }
  // Unknown state falls back to its own raw value, never a guessed label (AC-3).
  return known[state] ?? { label: state, tone: 'muted' }
}

export function approvalTrailSteps(run: ApprovalRun): TrailStepView[] {
  const kindLabels: Record<string, string> = {
    approval: APPROVAL_TRAIL_COPY.kindApproval,
    condition: APPROVAL_TRAIL_COPY.kindCondition,
    notify: APPROVAL_TRAIL_COPY.kindNotify,
    autoapprove: APPROVAL_TRAIL_COPY.kindAutoapprove,
  }
  const stateLabels: Record<string, string> = {
    pending: APPROVAL_TRAIL_COPY.stepWaiting,
    satisfied: APPROVAL_TRAIL_COPY.stepSigned,
    skipped: APPROVAL_TRAIL_COPY.stepSkipped,
    rejected: APPROVAL_TRAIL_COPY.stepRejected,
  }
  return run.steps.map((step) => {
    const isNotify = step.kind === 'notify'
    // overdue is the server's own answer (read_model.go:161), passed through -- never
    // re-derived from due_at here. Overdue wins over a formatted due date, which wins
    // over null.
    let dueLabel: string | null = null
    if (step.overdue) {
      dueLabel = APPROVAL_TRAIL_COPY.overdue
    } else if (step.due_at != null) {
      dueLabel = fmtDate(step.due_at)
    }
    return {
      ord1: step.ord + 1,
      kind: step.kind,
      // Unknown kind/state falls back to its own raw value, never a guessed label (AC-3).
      kindLabel: kindLabels[step.kind] ?? step.kind,
      stateLabel: stateLabels[step.state] ?? step.state,
      roleTitle: step.workflow_role_title ?? '—',
      holderText: step.holder?.text ?? null,
      holderWarn: step.holder?.warn ?? false,
      dueLabel,
      overdue: step.overdue,
      notifyNote: isNotify ? APPROVAL_TRAIL_COPY.notifyNote : null,
      notifyTarget: isNotify ? (step.notify_target ?? null) : null,
      notifyChannel: isNotify ? (step.notify_channel ?? null) : null,
    }
  })
}

export function approvalTrailDecisions(run: ApprovalRun): TrailDecisionView[] {
  const outcomeLabels: Record<string, string> = {
    approved: APPROVAL_TRAIL_COPY.stateApproved,
    rejected: APPROVAL_TRAIL_COPY.stateRejected,
  }
  return run.decisions.map((decision) => {
    const actor = actorLabel(decision.actor)
    return {
      ord1: decision.ord + 1,
      outcomeLabel: outcomeLabels[decision.decision] ?? decision.decision,
      actorText: actor.text,
      actorMono: actor.mono,
      whenLabel: fmtDateTime(decision.decided_at),
      reason: decision.reason,
    }
  })
}

export const APPROVAL_TRAIL_COPY = {
  cardTitle: 'Approvals',
  loading: 'Loading the approval trail…',
  emptyTitle: 'No approval run',
  emptyMessage:
    'Nothing on this invoice is waiting on a sign-off. Either this workspace has no active approval policy, or this invoice has not been validated yet.',
  stepsHeading: 'Steps',
  decisionsHeading: 'Decisions',
  noDecisions: 'No decision has been recorded on this run.',
  voided: 'This approval was voided by an edit — the invoice must be approved again from step one.',
  notifyNote: 'No message is delivered — notifications are recorded but not yet sent.',
  autoApproved: 'Settled automatically — nobody was asked.',
  // Same string as APPROVALS_COPY.unstaffedSeat (:269) -- deliberate duplication, not
  // aliased: per-screen copy consts already repeat labels (e.g. `cancel`), so one edit
  // here never silently changes the queue screen's wording too.
  unstaffedSeat: 'Unstaffed seat',
  // Same string as APPROVALS_COPY.overdue (:270) -- deliberate duplication, see
  // unstaffedSeat above.
  overdue: 'Overdue',
  stateOpen: 'In progress',
  stateApproved: 'Approved',
  stateRejected: 'Rejected',
  stateCancelled: 'Voided',
  // kindApproval/kindAutoapprove diverge from WorkflowInspector.tsx's private TITLES map
  // ('Approval'/'Auto-approved' here vs 'Approval step'/'Auto-approve' there) --
  // deliberate: that map is the policy-authoring domain, this is the run-trail domain.
  kindApproval: 'Approval',
  kindCondition: 'Condition',
  kindNotify: 'Notification',
  kindAutoapprove: 'Auto-approved',
  stepWaiting: 'Waiting',
  stepSigned: 'Signed',
  stepSkipped: 'Skipped',
  stepRejected: 'Rejected',
} as const

// approveDetail names the ACTION, never claims the OUTCOME -- the same rule
// approvalsBarView.confirmDetail follows (:172-174).
export const DETAIL_DECISION_COPY = {
  approve: 'Approve',
  approvePrompt: 'Approve this invoice?',
  approveDetail: 'Another approver may have already acted on it.',
  approveConfirm: 'Yes, approve now',
  approveSending: 'Approving…',
  cancel: 'Cancel',
  reject: 'Reject',
  rejectPrompt: 'Why is this invoice being rejected?',
  rejectPlaceholder: 'Reason for rejection (required)',
  rejectConfirm: 'Reject invoice',
  rejectSending: 'Rejecting…',
} as const
