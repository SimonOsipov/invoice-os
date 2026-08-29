// Activity feed's pure core: chip set, per-chip counts, rest/expanded slice, overflow copy.
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
  // Never 'Audit trail': import-wizard.spec.ts:576 pins zero matches on this page.
  cardTitle: 'Activity',
  loading: 'Loading activity…',
  // Scoped, never the Audit screen's firm-wide wording -- that would be a lie beside one
  // invoice. invoiceActivity_emptyScopedLogIsHonest holds the split.
  emptyScopedTitle: 'No activity recorded for this invoice',
  emptyScopedBody: 'Edits, validations, approvals and transmissions appear here as they happen.',
  // An ADDITION to the two scoped lines, never a swap. Rendered only on log_is_empty.
  emptyWorkspaceAlso: 'This workspace has not recorded anything at all yet.',
  // The incidental zero, distinct from documentsInert's structural one.
  chipZeroInert: 'A chip with no count is dimmed: this invoice has no events of that kind in the loaded page.',
  // D-AC-6: the Documents zero is structural, not "nothing yet". document.* and extraction.*
  // are both outside the invoice-scoped read's two lists
  // (TestAuditScoped_EventGateExcludesACollidingID). Names the FAMILIES, never the labels:
  // the old enumeration went stale twice unnoticed --
  // invoiceActivity_documentsInertCopyNamesEveryFamilyTheChipCovers derives them now.
  documentsInert:
    'Document and extraction events are recorded against the workspace, not against a single invoice, so none can appear here.',
  // Single source for the label, shared with the toggle note and subtask 05's button.
  auditLink: 'Open in Audit →',
} as const

// The five chips that stand for an AuditDomain. `all` stands for the page and is excluded.
const DOMAIN_CHIP_KEYS = new Set<string>(ACTIVITY_CHIP_ORDER.filter((k) => k !== 'all'))

// A domain with no chip -- and an identifier the vocabulary does not know, whose domain is
// null -- counts under `all` and nowhere else.
// invoiceActivity_unmappedEventCountsUnderEverythingOnly.
function chipOf(e: AuditEvent): ActivityChipKey | null {
  const { domain } = auditEventView(e.event)
  return domain != null && DOMAIN_CHIP_KEYS.has(domain) ? (domain as ActivityChipKey) : null
}

export function activityChips(events: AuditEvent[]): ActivityChip[] {
  const counts = new Map<ActivityChipKey, number>()
  for (const e of events) {
    const key = chipOf(e)
    if (key != null) counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  return ACTIVITY_CHIP_ORDER.map((key) => {
    const count = key === 'all' ? events.length : (counts.get(key) ?? 0)
    return {
      key,
      label: ACTIVITY_CHIP_LABELS[key],
      count,
      // Not `key === 'documents' || count === 0`: the permanence of the Documents zero is
      // the vocabulary's doing, proven by invoiceActivity_documentsChipIsAlwaysInert.
      inert: count === 0,
      reason: key === 'documents' ? ACTIVITY_COPY.documentsInert : null,
    }
  })
}

// filter + slice only. reader.go:288-293 already ordered these newest-first and
// Array.prototype.sort would reorder in place -- invoiceActivity_chipFilterPreservesServerOrder
// scrambles created_at against array order, invoiceActivity_doesNotMutateItsInput holds the rest.
export function activityRows(events: AuditEvent[], chip: ActivityChipKey, showAll: boolean): AuditEvent[] {
  const rows = events.filter((e) => chip === 'all' || chipOf(e) === chip)
  return showAll ? rows : rows.slice(0, ACTIVITY_REST_ROWS)
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
  // `shown`, never `total`: the label may name only what the toggle puts on screen.
  const label =
    n.shown <= ACTIVITY_REST_ROWS
      ? null
      : n.showAll
        ? 'Show fewer'
        : `Show all ${n.shown.toLocaleString('en-NG')} events`

  let note: string | null = null
  if (n.total > n.fetched) {
    const loaded = n.fetched.toLocaleString('en-NG')
    note = `The ${loaded} most recent of ${n.total.toLocaleString('en-NG')} events are loaded. Chip counts and rows cover those ${loaded} — use ${ACTIVITY_COPY.auditLink} for the whole log.`
  }
  return { label, note }
}
