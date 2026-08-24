// Every user-visible string on the evidence-bundle drawer, plus the refusals, manifest rows
// and period wording it derives -- per [bulk-copy-lives-in-the-lib] (lib/auditView.ts:1-6).
// Nothing here may call the manifest "signed" (D-08-01: SHA-256 checksums, not a signature);
// EB-02-1 is the drift guard and scans the VALUES, never this file's text.

import type { BundlePeriod, BundleRequest, EvidenceBundlePreview } from './evidenceBundle'

// STUB (AUDIT-08-02 Stage 2.5): empty on purpose so EB-02-1/2's floor goes red on the count.
export const EVIDENCE_COPY = {} as const

export type BundleBlock =
  | { kind: 'no-company' }
  | { kind: 'no-period' }
  | { kind: 'invalid-range' }
  | { kind: 'empty'; company: string; period: string }
  | { kind: 'over-limit'; invoices: number; limit: number }
  | null

// STUB: deliberately wrong so EB-02-12 fails on the assertion, not by luck.
// The real value is pinned against internal/archive/archive.go's maxBundleInvoices.
export const BUNDLE_INVOICE_LIMIT = -1

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

export function bundleBlockFor(
  _entityId: string | null,
  _req: BundleRequest | null,
  _preview: EvidenceBundlePreview | null,
): BundleBlock {
  throw new Error('not implemented')
}

export function bundleBlockReason(_block: BundleBlock): string | null {
  throw new Error('not implemented')
}

export function bundleManifestLines(_preview: EvidenceBundlePreview): BundleManifestLine[] {
  throw new Error('not implemented')
}

export function bundlePeriodLabel(_period: BundlePeriod): string {
  throw new Error('not implemented')
}

export function bundleBasisLine(_period: BundlePeriod): string {
  throw new Error('not implemented')
}

export function bundleReadyLine(_bytes: number): string {
  throw new Error('not implemented')
}

export function bundleToastCopy(_input: BundleToastInput): string {
  throw new Error('not implemented')
}
