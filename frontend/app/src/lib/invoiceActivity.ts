// Activity feed's pure core: chip set, per-chip counts, rest/expanded slice, overflow copy.
// STUB ONLY -- the three functions throw pending the executor; invoiceActivity.test.ts pins
// the contract red against them.
//
// Chip counts are derived off the fetched page, which inverts [filters-are-server-side]
// (lib/reviewBatch.ts:29). The exception is narrow: the invoice scoping stays server-side on
// `a.invoice_id`, and the reader exposes no domain facet to group by. When the page is capped
// the counts understate -- that is what activityToggleCopy's note says out loud.

import type { AuditEvent } from './audit'
import { auditEventView } from './auditVocabulary'

export type ActivityChipKey = 'all' | 'invoices' | 'approvals' | 'documents' | 'submissions' | 'reconciliation'

export interface ActivityChip {
  key: ActivityChipKey
  label: string
  count: number
  inert: boolean
  // Non-null only where the zero is STRUCTURAL, not incidental.
  reason: string | null
}

export interface ActivityToggle {
  label: string | null // null when the chip's whole set is already on screen
  note: string | null // null unless the server holds more than the page we fetched
}

export const ACTIVITY_REST_ROWS = 5

// internal/audit/handlers.go:39 maxLimit. Pinned to AUDIT_PAGE_SIZES by
// invoiceActivity_fetchLimitIsTheServersMax -- a vitest cannot read the Go.
export const ACTIVITY_FETCH_LIMIT = 100

// Declared as an explicit array rather than Object.keys(ACTIVITY_CHIP_LABELS): chip ORDER is
// a design fact (D-AC-5), and key-insertion order is not a contract worth resting it on.
export const ACTIVITY_CHIP_ORDER: ActivityChipKey[] = [
  'all',
  'invoices',
  'approvals',
  'documents',
  'submissions',
  'reconciliation',
]

export const ACTIVITY_CHIP_LABELS: Record<ActivityChipKey, string> = {
  all: 'Everything',
  invoices: 'Invoice & status',
  approvals: 'Approvals',
  documents: 'Documents',
  submissions: 'Transmission',
  reconciliation: 'Reconciliation',
}

export const ACTIVITY_COPY = {
  // D-AC-6: the Documents zero is structural, not "nothing yet". document.* is gated out of
  // the invoice-scoped read (TestAuditScoped_EventGateExcludesACollidingID).
  documentsInert:
    'Document uploads and reads are recorded against the workspace, not against a single invoice, so none can appear here.',
  // Single source for the label, shared with the toggle note and subtask 05's button.
  auditLink: 'Open in Audit →',
} as const

export function activityChips(events: AuditEvent[]): ActivityChip[] {
  void events
  void auditEventView
  throw new Error('not implemented')
}

export function activityRows(events: AuditEvent[], chip: ActivityChipKey, showAll: boolean): AuditEvent[] {
  void events
  void chip
  void showAll
  throw new Error('not implemented')
}

// Object arg, not three bare numbers (reviewBatch.ts:909 records why). `label` depends only on
// shown+showAll, `note` only on total vs fetched -- orthogonal, so the copy cannot overclaim.
// No singular branch: every number this prints is above ACTIVITY_REST_ROWS or ACTIVITY_FETCH_LIMIT.
export function activityToggleCopy(n: {
  shown: number // activityRows(events, chip, true).length -- the ACTIVE chip's rows
  total: number // res.total: the server's scoped count, ALL domains
  fetched: number // res.events.length
  showAll: boolean
}): ActivityToggle {
  void n
  throw new Error('not implemented')
}
