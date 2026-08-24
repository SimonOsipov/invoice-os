// Every user-visible string on the evidence-bundle drawer, plus the refusals, manifest rows
// and period wording it derives -- per [bulk-copy-lives-in-the-lib] (lib/auditView.ts:1-6).
// Nothing here may call the manifest "signed" (D-08-01: SHA-256 checksums, not a signature);
// EB-02-1 is the drift guard and scans the VALUES, never this file's text.

import { AUDIT_COPY } from './auditView'
import type { BundlePeriod, BundleRequest, EvidenceBundlePreview } from './evidenceBundle'
import { formatBytes } from './sourceDocument'

export const EVIDENCE_COPY = {
  // The header trigger's mono caption (AUDIT-08-03). Separator is U+00B7, the same one the
  // shipped ghost's caption uses; pinned by EB-03-2a in evidenceBundleView.test.ts.
  openCaption: 'ZIP · ONE COMPANY, ONE PERIOD',
  drawerTitle: 'Export evidence bundle',
  drawerSubtitle: 'One company, one period, one ZIP a regulator can verify on its own.',
  companyLabel: 'Company',
  companyPlaceholder: 'Choose a company',
  companyHelper:
    'One company per bundle. A regulator asks about one taxpayer at a time, and mixing two makes the manifest unverifiable.',
  periodLabel: 'Period',
  confirmHeading: 'YOU ARE ABOUT TO EXPORT',
  contentsHeading: 'WHAT THE ZIP CONTAINS',
  filenameLabel: 'File name',
  confirmFooter: 'Nothing downloads until you confirm.',
  // The seven row labels are the seven real ZIP entry families. No label may begin with
  // B/KB/MB/GB: textContent glues the previous row's value to it and false-trips EB-05-3.
  rowInvoices: 'Invoices (invoices.csv)',
  rowStatusTimelines: 'Status timelines (status_history.csv)',
  rowFirsReferences: 'FIRS references — IRN, CSID and QR payload (columns of invoices.csv)',
  rowSubmissions: 'Submissions (submissions.csv)',
  rowTransmissionAttempts: 'Transmission attempts (exchange.csv)',
  rowBodies: 'Recorded request and response bodies (bodies/)',
  rowManifest: 'MANIFEST · SHA-256 (manifest.json) — a per-entry checksum, not a cryptographic signature',
  prepareLabel: 'Prepare bundle',
  // D-08-18/D-08-17: the two things that ARE true about size at Form time. The counts on the
  // block are the magnitude signal; a digit here would be a smuggled estimate (EB-02-11).
  prepareHelper:
    'The exact size is not known until the bundle is built. A large bundle is held in your browser until you save it.',
  cancelLabel: 'Cancel',
  noCompanyReason: 'Pick one company to export.',
  noPeriodReason: 'Pick a period. A custom range needs both a start date and an end date.',
  // Reused, not rewritten: AUDIT-07 already ships this sentence (auditView.ts:26).
  invalidRangeReason: AUDIT_COPY.dateRangeInvalidReason,
  buildingTitle: 'Building the bundle',
  buildingNote: 'The export streams as a single response, so its progress cannot be broken into stages.',
  readyTitle: 'Bundle ready',
  downloadLabel: 'Download',
  startAnotherLabel: 'Start another',
  retryLabel: 'Try again',
} as const

export type BundleBlock =
  | { kind: 'no-company' }
  | { kind: 'no-period' }
  | { kind: 'invalid-range' }
  | { kind: 'empty'; company: string; period: string }
  | { kind: 'over-limit'; invoices: number; limit: number }
  | null

// Mirrors maxBundleInvoices (internal/archive/archive.go). EB-02-12 reads that file and fails
// on drift; the preview wire carries no limit field, so the client must hold one.
export const BUNDLE_INVOICE_LIMIT = 10000

export interface BundleManifestLine {
  label: string
  value: string | null
}

export interface BundleToastInput {
  filename: string
  invoices: number
  bytes: number
  company: string
  period: string
}

function num(n: number): string {
  return n.toLocaleString('en-NG')
}

// Answers "is there a stated reason to refuse?", not "is Prepare enabled?" -- a preview that
// has not landed yet is not a refusal. The order is fixed; EB-02-13 pins it.
export function bundleBlockFor(
  entityId: string | null,
  req: BundleRequest | null,
  preview: EvidenceBundlePreview | null,
): BundleBlock {
  if (!entityId) return { kind: 'no-company' }
  if (req == null) return { kind: 'no-period' }
  // Byte compare, not Date.parse: both endpoints are fixed-width RFC3339 UTC out of
  // bundleRequestFor, and NaN > NaN is false -- a malformed input would slip through.
  if (req.from > req.to) return { kind: 'invalid-range' }

  if (preview == null) return null
  if (preview.counts.invoices === 0) {
    return { kind: 'empty', company: preview.entity.name, period: bundlePeriodLabel(preview.period) }
  }
  if (preview.over_limit) {
    return { kind: 'over-limit', invoices: preview.counts.invoices, limit: BUNDLE_INVOICE_LIMIT }
  }
  return null
}

export function bundleBlockReason(block: BundleBlock): string | null {
  if (block == null) return null
  switch (block.kind) {
    case 'no-company':
      return EVIDENCE_COPY.noCompanyReason
    case 'no-period':
      return EVIDENCE_COPY.noPeriodReason
    case 'invalid-range':
      return EVIDENCE_COPY.invalidRangeReason
    case 'empty':
      return `No invoices were added to ${block.company} in ${block.period}, so there is nothing to export.`
    case 'over-limit':
      return `${num(block.invoices)} invoices is more than one bundle can hold. The limit is ${num(block.limit)}. Narrow the period and try again.`
  }
}

// One row per real ZIP entry family (assemble.go:78-105, exchange.go:108,116,
// manifest.go:146). FIRS references are columns 13-15 of invoices.csv, not a file, so that
// row and the manifest row carry no count (D-08-12).
export function bundleManifestLines(preview: EvidenceBundlePreview): BundleManifestLine[] {
  const c = preview.counts
  return [
    { label: EVIDENCE_COPY.rowInvoices, value: num(c.invoices) },
    { label: EVIDENCE_COPY.rowStatusTimelines, value: num(c.status_transitions) },
    { label: EVIDENCE_COPY.rowFirsReferences, value: null },
    { label: EVIDENCE_COPY.rowSubmissions, value: num(c.submissions) },
    { label: EVIDENCE_COPY.rowTransmissionAttempts, value: num(c.exchange_attempts) },
    { label: EVIDENCE_COPY.rowBodies, value: num(c.body_files) },
    { label: EVIDENCE_COPY.rowManifest, value: null },
  ]
}

const MONTHS = [
  'January',
  'February',
  'March',
  'April',
  'May',
  'June',
  'July',
  'August',
  'September',
  'October',
  'November',
  'December',
]

// Prefix match, the toDateInputValue idiom (format.ts:30-34). No Date construction --
// new Date(iso).toLocaleDateString() shifts a day in negative UTC offsets (BUG-03-02).
// The prefix IS the UTC calendar date: bundlePeriod emits r.From.UTC().Format(RFC3339).
function calendarDay(iso: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(iso)
  if (!m) return iso
  const month = MONTHS[Number(m[2]) - 1]
  return month ? `${Number(m[3])} ${month} ${m[1]}` : iso
}

export function bundlePeriodLabel(period: BundlePeriod): string {
  return `${calendarDay(period.from)} – ${calendarDay(period.to)}`
}

// Derived from the server's basis, never hardcoded (D-08-11). An unrecognised basis is named
// plainly rather than dressed up as the created_at claim (EB-02-6b).
export function bundleBasisLine(period: BundlePeriod): string {
  const bounds = period.bounds === 'inclusive' ? 'Both dates are included. ' : ''
  if (period.basis === 'invoices.created_at') {
    return `${bounds}The period selects invoices by when they were added to ASComply — not by the date on the invoice.`
  }
  return `${bounds}The period selects invoices by the field ${period.basis}.`
}

// formatBytes is the shipped one (sourceDocument.ts:272), already reused by auditCsv.ts:6.
export function bundleReadyLine(bytes: number): string {
  return `ZIP · ${formatBytes(bytes)} · MANIFEST · SHA-256`
}

export function bundleToastCopy({ filename, invoices, bytes, company, period }: BundleToastInput): string {
  const n = `${num(invoices)} ${invoices === 1 ? 'invoice' : 'invoices'}`
  return `Saved ${filename} — ${n} for ${company}, ${period} (${formatBytes(bytes)}).`
}
