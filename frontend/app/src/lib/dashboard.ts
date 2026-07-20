// App-side dashboard rollup data-access helpers (M4-10-01, task-189).
//
// Types mirror the wire shapes in internal/dashboard/dashboard.go: `Bucket` is embedded
// anonymously in `Client`, so encoding/json promotes `counts`/`needs_attention` to the
// row's top level — RollupClient below spells that promotion out explicitly rather than
// modeling the Go embedding. `Rollup.clients`/`.top_violations` are never null on the
// wire (pre-declared []Client{}/[]RuleCount{}) but this module types them as plain arrays,
// same as `InvoiceListResponse.invoices` in invoices.ts.
//
// getRollup is a thin wrapper around an injected authedFetch (the app-side 401 seam from
// M3-07-02, src/lib/authedFetch.ts) — mirrors listEntities/listInvoices:
// - getRollup: GET `${base}/api/dashboard/v1/rollup`, resolves the body verbatim.
// Non-2xx / network responses reject with the underlying error unchanged (apiFetch's own
// contract) — getRollup must not swallow or reshape it (kind-normalization is useAsync's
// job via toApiError, not getRollup's).
//
// donutSegments/deslug/topFailures/resolveCtaLabel/isEmptyRollup/dashboardViewState/
// entityHealth are pure viewmodel helpers, all node-vitest testable (no DOM):
// - donutSegments returns all 7 canonical states in order, zeros included — unlike the
//   deleted donutFrom (lib/charts.ts), it never filters zero-count segments, and
//   needs_attention is never an input so it can never surface as a segment. Arc math
//   (R=49, C=2*pi*R, per-seg dash/offset) is ported from donutFrom over the fixed 7 states.
// - dashboardViewState mirrors invoicesViewState (invoices.ts) — the no-gateway
//   zero-network short-circuit: base==null => 'idle' regardless of async status.
import type { AuthedFetch } from './portfolio'
import type { DonutSeg } from '../types'
import type { AsyncState, AsyncStatus } from '@invoice-os/api-client'

import { invoiceStatusStyle, type InvoiceStatus } from './invoices'

// The 7-state count bucket (dashboard.go Bucket.Counts), no omitempty on the wire — a
// zero state still serializes as an explicit 0.
export interface Counts {
  draft: number
  validated: number
  queued: number
  submitted: number
  accepted: number
  rejected: number
  failed: number
}

// dashboard.go Bucket — the 7-state counts plus the overlapping needs_attention overlay
// (rejected ∪ failed ∪ drafts-with-an-error-severity-violation). Never a donut input.
export interface RollupBucket {
  counts: Counts
  needs_attention: number
}

// dashboard.go Client — Bucket is embedded anonymously there, so counts/needs_attention
// promote to this row's top level on the wire; entity_id/entity_name are the row's own
// fields. Only entities WITH at least one invoice appear here (INNER JOIN, store.go).
export interface RollupClient {
  entity_id: string
  entity_name: string
  counts: Counts
  needs_attention: number
}

// dashboard.go RuleCount — one top_violations row, server-ordered invoices DESC,
// rule_key ASC.
export interface RuleCount {
  rule_key: string
  invoices: number
}

// GET /api/dashboard/v1/rollup response envelope (dashboard.go Rollup).
export interface Rollup {
  totals: RollupBucket
  clients: RollupClient[]
  top_violations: RuleCount[]
}

export async function getRollup(authedFetch: AuthedFetch, base: string): Promise<Rollup> {
  return authedFetch<Rollup>(`${base}/api/dashboard/v1/rollup`)
}

// Canonical 7-state order for the donut and any other verbatim state listing. Keys are the
// lowercase InvoiceStatus (== the rollup's Counts keys); the human label is the key with
// its first letter capitalized (Draft, Validated, …), and the segment colour reuses the
// canonical per-state palette via invoiceStatusStyle(state).text.
const CANONICAL_STATES: InvoiceStatus[] = [
  'draft',
  'validated',
  'queued',
  'submitted',
  'accepted',
  'rejected',
  'failed',
]

export function donutSegments(counts: Counts): DonutSeg[] {
  const total = CANONICAL_STATES.reduce((sum, state) => sum + counts[state], 0) || 1
  const R = 49
  const C = 2 * Math.PI * R
  let acc = 0
  return CANONICAL_STATES.map((state) => {
    const count = counts[state]
    const len = (count / total) * C
    const seg: DonutSeg = {
      label: state[0].toUpperCase() + state.slice(1),
      color: invoiceStatusStyle(state).text,
      count: String(count),
      pct: Math.round((count / total) * 100) + '%',
      dash: len.toFixed(1) + ' ' + (C - len).toFixed(1),
      offset: (-acc).toFixed(1),
    }
    acc += len
    return seg
  })
}

export function deslug(ruleKey: string): string {
  return ruleKey
    .replace(/[-_]/g, ' ')
    .split(' ')
    .map((word) => (word ? word[0].toUpperCase() + word.slice(1) : word))
    .join(' ')
}

export function topFailures(
  v: RuleCount[],
): { label: string; ruleKey: string; count: number; bar: string }[] {
  const max = Math.max(1, ...v.map((x) => x.invoices))
  return v.map((x) => ({
    label: deslug(x.rule_key),
    ruleKey: x.rule_key,
    count: x.invoices,
    bar: Math.round((x.invoices / max) * 100) + '%',
  }))
}

export function resolveCtaLabel(needsAttention: number): string {
  if (needsAttention === 0) return 'All clear'
  const noun = needsAttention === 1 ? 'issue' : 'issues'
  return `Resolve ${needsAttention} ${noun} →`
}

export function isEmptyRollup(r: Rollup): boolean {
  return Object.values(r.totals.counts).every((n) => n === 0)
}

export function dashboardViewState(base: string | null, s: AsyncState<Rollup>): AsyncStatus {
  if (base == null) return 'idle'
  return s.status
}

export type EntityHealth =
  | { kind: 'no-invoices' }
  | { kind: 'needs-attention'; count: number }
  | { kind: 'clear' }

export function entityHealth(clients: RollupClient[], entityId: string): EntityHealth {
  const client = clients.find((c) => c.entity_id === entityId)
  if (!client) return { kind: 'no-invoices' }
  if (client.needs_attention > 0) return { kind: 'needs-attention', count: client.needs_attention }
  return { kind: 'clear' }
}
