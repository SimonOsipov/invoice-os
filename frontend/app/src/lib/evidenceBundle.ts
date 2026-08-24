// Hand-maintained mirror of internal/archive/preview.go's Preview and manifest.go's
// manifestEntity / manifestPeriod / manifestCounts. EB-01-1's tag scan catches drift.

import type { AuditRange } from './auditFilters'
import type { AuthedFetch } from './portfolio'

export interface BundleEntity {}

export interface BundlePeriod {}

export interface BundleCounts {}

export interface EvidenceBundlePreview {}

// The frozen request triple. One object so preview and download cannot disagree.
export interface BundleRequest {
  entityId: string
  from: string
  to: string
}

export function bundleRequestFor(_entityId: string | null, _range: AuditRange, _now: Date): BundleRequest | null {
  throw new Error('not implemented')
}

export function getEvidenceBundlePreview(
  _f: AuthedFetch,
  _base: string,
  _r: BundleRequest,
  _signal?: AbortSignal,
): Promise<EvidenceBundlePreview> {
  throw new Error('not implemented')
}

export function evidenceBundleUrl(_base: string, _r: BundleRequest): string {
  throw new Error('not implemented')
}

export function fetchEvidenceBundle(
  _getToken: () => string | null,
  _base: string,
  _r: BundleRequest,
  _fallbackFilename: string,
  _signal?: AbortSignal,
): Promise<{ blob: Blob; filename: string }> {
  throw new Error('not implemented')
}
