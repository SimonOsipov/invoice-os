// Approvals screen pure core + fan-out client (APPR-12-02, task-527). STUB ONLY --
// every body throws pending the executor's implementation; approvals.test.ts pins the
// real contract red against these stubs.

import type { InvoiceListResponse, InvoiceRecord, ListInvoicesOptions } from './invoices'
import type { AuthedFetch } from './portfolio'

export type ApprovalPhase = 'idle' | 'armed' | 'submitting'

// step / role / holder-warning / due / overdue / approve_blocked_reason -- wire values
// passed through, except roleLabel's own em-dash fallback for a null pending_role_title
// (the ONLY fallback this file may author, A02-15).
export interface ApprovalRowView {
  approvable: boolean
  pendingOrd: number | null
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
  message: string | null
}

// Forces awaitingApproval:true regardless of what opts carries -- the Omit below makes
// that a compile-time guarantee, not just a default.
export async function listAwaitingApproval(
  authedFetch: AuthedFetch,
  base: string,
  opts: Omit<ListInvoicesOptions, 'awaitingApproval'> = {},
): Promise<InvoiceListResponse> {
  void authedFetch
  void base
  void opts
  throw new Error('not implemented')
}

// The server's own answer, fail-closed -- ONE clause, no client-side conjuncts (U5).
export function isApprovableRow(row: Pick<InvoiceRecord, 'can_approve'>): boolean {
  void row
  throw new Error('not implemented')
}

export function approvableIds(rows: InvoiceRecord[]): string[] {
  void rows
  throw new Error('not implemented')
}

// A plain filter over approvableIds(rows), a new array every call -- no instance-identity
// guarantee here; that lives in [APPR-12-04]'s component effect (Plan gap G1).
export function pruneApprovalSelection(sel: string[], rows: InvoiceRecord[]): string[] {
  void sel
  void rows
  throw new Error('not implemented')
}

export function approvalSelectAllState(selected: string[], rows: InvoiceRecord[]): 'none' | 'some' | 'all' {
  void selected
  void rows
  throw new Error('not implemented')
}

export function approvalRowView(
  row: Pick<InvoiceRecord, 'can_approve' | 'approve_blocked_reason' | 'approval'>,
): ApprovalRowView {
  void row
  throw new Error('not implemented')
}

export function approvalsBarView(
  selected: string[],
  rows: InvoiceRecord[],
  phase: ApprovalPhase,
  pageLoading: boolean,
): ApprovalsBarView {
  void selected
  void rows
  void phase
  void pageLoading
  throw new Error('not implemented')
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
  void authedFetch
  void base
  void ids
  void onProgress
  throw new Error('not implemented')
}

export function approvalOutcome(results: ApproveResult[], numbersById: Map<string, string>): ApprovalResultRow[] {
  void results
  void numbersById
  throw new Error('not implemented')
}

// Static chrome only -- placeholder pending the executor's copy.
export const APPROVALS_COPY = {} as const
