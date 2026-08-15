// Approvals screen pure core + fan-out client (APPR-12-02, task-527), covered by
// approvals.test.ts (A02-1..A02-18).
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

import { listInvoices, type InvoiceListResponse, type InvoiceRecord, type ListInvoicesOptions } from './invoices'
import type { AuthedFetch } from './portfolio'

export type ApprovalPhase = 'idle' | 'armed' | 'submitting'

// step / role / holder-warning / due / overdue / approve_blocked_reason -- wire values
// passed through, except roleLabel's own em-dash fallback for a null pending_role_title
// (the ONLY fallback this file may author, A02-15).
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
): Promise<ApproveResult[]> {
  const results: ApproveResult[] = []
  for (const [index, id] of ids.entries()) {
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

// Progress during the fan-out (D-b4, G-04-A, task-529 Mode A stub) -- onProgress
// (approveInvoices, above) has a caller to drive this once APPR-12-04 wires it up.
export function approvalProgressLabel(done: number, total: number): string {
  throw new Error(`not implemented (${done}/${total})`)
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
  sending: 'Approving…',
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
