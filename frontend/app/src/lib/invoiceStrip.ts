// RED stub (AUDIT-09-01) -- the types are real, only the body is missing. Specs in
// invoiceStrip.test.ts fail on the throw below, not on a compile error.

import type { ActorLabel } from './actor'
import type { ApprovalRun } from './approvals'
import type { InvoiceStatus, StatusChange } from './invoices'

export type StripState = 'done' | 'current' | 'failed' | 'unreached' | 'not-required'

export interface StripNode {
  key: 'draft' | 'validated' | 'approved' | 'queued' | 'accepted'
  label: string
  state: StripState
  // Non-null iff the node was entered AND a source recorded when.
  at: string | null
  actor: ActorLabel | null
  caption: string
}

export function stripNodes(
  _history: StatusChange[],
  _run: ApprovalRun | null,
  _status: InvoiceStatus,
): StripNode[] {
  throw new Error('not implemented')
}
