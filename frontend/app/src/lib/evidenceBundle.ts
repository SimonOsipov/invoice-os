// Hand-maintained mirror of internal/archive/preview.go's Preview and manifest.go's
// manifestEntity / manifestPeriod / manifestCounts. EB-01-1's tag scan catches drift.

import { ApiError } from '@invoice-os/api-client'

import type { AuditRange } from './auditFilters'
import type { AuthedFetch } from './portfolio'

export interface BundleEntity {
  id: string
  name: string
  tin: string | null // Go *string, no omitempty -- the key is always present, may be null.
}

export interface BundlePeriod {
  // time.RFC3339 echo, so fractional seconds are dropped: this is NOT a byte round-trip of
  // BundleRequest.from/to. Surfaces render this, never the request.
  from: string
  to: string
  bounds: string
  basis: string
}

export interface BundleCounts {
  invoices: number
  status_transitions: number
  submissions: number
  exchange_attempts: number
  body_files: number
}

export interface EvidenceBundlePreview {
  entity: BundleEntity
  period: BundlePeriod
  filename: string // the server's name; the drawer reads it, never recomputes it.
  counts: BundleCounts
  over_limit: boolean // a 200 carrying the real counts, never a short-circuit.
}

// The frozen request triple. One object so preview and download cannot disagree.
export interface BundleRequest {
  entityId: string
  from: string
  to: string
}

const DAY_MS = 24 * 60 * 60 * 1000
const PRESET_DAYS = { '24h': 1, '7d': 7, '30d': 30 } as const

// AUDIT-07's rangeToQuery leaves a relative preset open-ended and the server 400s that with
// `to is required`, so this closes the range at now. EB-01-2 pins the three endpoints.
export function bundleRequestFor(entityId: string | null, range: AuditRange, now: Date): BundleRequest | null {
  if (!entityId) return null

  if (range.preset === 'custom') {
    if (!range.from || !range.to) return null
    // Byte-identical to auditFilters.ts:69,71 -- inclusive day bounds.
    return { entityId, from: `${range.from}T00:00:00.000Z`, to: `${range.to}T23:59:59.999Z` }
  }

  return {
    entityId,
    from: new Date(now.getTime() - PRESET_DAYS[range.preset] * DAY_MS).toISOString(),
    to: now.toISOString(),
  }
}

function bundleParams(r: BundleRequest): URLSearchParams {
  return new URLSearchParams({ entity_id: r.entityId, from: r.from, to: r.to })
}

// The download URL. The preview URL stays module-private: EB-01-5 asserts it off the
// recorded authedFetch argument, so a second export would be speculative.
export function evidenceBundleUrl(base: string, r: BundleRequest): string {
  return `${base}/api/invoice/v1/evidence-bundle?${bundleParams(r)}`
}

// No try/catch: apiFetch already lifts the server's {error} sentence into ApiError.message,
// and EB-01-11 pins that the very same instance reaches the caller.
export function getEvidenceBundlePreview(
  f: AuthedFetch,
  base: string,
  r: BundleRequest,
  signal?: AbortSignal,
): Promise<EvidenceBundlePreview> {
  return f<EvidenceBundlePreview>(`${base}/api/invoice/v1/evidence-bundle/preview?${bundleParams(r)}`, { signal })
}

// Whole-string unquoted grammar (contract 8.1 forbids a filename="([^"]+)" regex): a quoted
// value or a trailing parameter fails outright and falls back. EB-01-7, EB-01-9.
const DISPOSITION_RE = /^attachment; filename=([A-Za-z0-9._-]+)$/

function dispositionFilename(header: string | null, fallback: string): string {
  return header?.match(DISPOSITION_RE)?.[1] ?? fallback
}

// Bare fetch, not authedFetch: the body is a zip and apiFetch always res.json()s. blob(),
// not arrayBuffer(). Nothing wraps the fetch call, so an abort propagates untranslated to
// the caller that owns the controller. EB-01-6, EB-01-10.
export async function fetchEvidenceBundle(
  getToken: () => string | null,
  base: string,
  r: BundleRequest,
  fallbackFilename: string,
  signal?: AbortSignal,
): Promise<{ blob: Blob; filename: string }> {
  const res = await fetch(evidenceBundleUrl(base, r), {
    headers: { Authorization: `Bearer ${getToken()}` },
    signal,
  })

  if (!res.ok) {
    // apiFetch's !res.ok block (client.ts:65-77), not sourceDocument.ts:96 -- that one never
    // reads the body and loses the sentence EB-01-8 and EB-01-12 require.
    let body: unknown
    let msg = res.statusText
    try {
      body = await res.json()
      if (body && typeof body === 'object' && 'error' in body) {
        msg = String((body as { error: unknown }).error)
      }
    } catch {
      // best-effort -- no JSON body to read; fall back to statusText.
    }
    throw new ApiError('http', msg, res.status, body)
  }

  return {
    blob: await res.blob(),
    filename: dispositionFilename(res.headers.get('Content-Disposition'), fallbackFilename),
  }
}
