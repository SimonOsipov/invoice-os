// App-side dashboard rollup data-access helpers (M4-10-01, task-189). STUB — the
// executor implements the bodies next; every export below throws so the RED specs in
// dashboard.test.ts fail on a thrown/assertion mismatch, not an import or type error.
//
// Types mirror the wire shapes in internal/dashboard/dashboard.go: `Bucket` is embedded
// anonymously in `Client`, so encoding/json promotes `counts`/`needs_attention` to the
// row's top level — RollupClient below spells that promotion out explicitly rather than
// modeling the Go embedding. `Rollup.clients`/`.top_violations` are never null on the
// wire (pre-declared []Client{}/[]RuleCount{}) but this stub types them as plain arrays,
// same as `InvoiceListResponse.invoices` in invoices.ts.
//
// getRollup is a thin wrapper around an injected authedFetch (the app-side 401 seam from
// M3-07-02, src/lib/authedFetch.ts) — mirrors listEntities/listInvoices:
// - getRollup: GET `${base}/api/dashboard/v1/rollup`, resolves the body verbatim.
// Non-2xx / network responses reject with the underlying error unchanged (apiFetch's own
// contract) — getRollup must not swallow or reshape it.
//
// donutSegments/deslug/topFailures/resolveCtaLabel/isEmptyRollup/dashboardViewState/
// entityHealth are pure viewmodel helpers, all node-vitest testable (no DOM):
// - donutSegments returns all 7 canonical states in order, zeros included — unlike the
//   deleted donutFrom (lib/charts.ts), it never filters zero-count segments, and
//   needs_attention is never an input so it can never surface as a segment.
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

export async function getRollup(_authedFetch: AuthedFetch, _base: string): Promise<Rollup> {
  throw new Error('not implemented')
}

// Canonical 7-state order for the donut and any other verbatim state listing.
const CANONICAL_STATES: InvoiceStatus[] = [
  'draft',
  'validated',
  'queued',
  'submitted',
  'accepted',
  'rejected',
  'failed',
]

export function donutSegments(_counts: Counts): DonutSeg[] {
  // Referenced so noUnusedLocals doesn't reject this stub's imports before the
  // executor fills in the real arc-math body.
  void CANONICAL_STATES
  void invoiceStatusStyle
  throw new Error('not implemented')
}

export function deslug(_ruleKey: string): string {
  throw new Error('not implemented')
}

export function topFailures(
  _v: RuleCount[],
): { label: string; ruleKey: string; count: number; bar: string }[] {
  throw new Error('not implemented')
}

export function resolveCtaLabel(_needsAttention: number): string {
  throw new Error('not implemented')
}

export function isEmptyRollup(_r: Rollup): boolean {
  throw new Error('not implemented')
}

export function dashboardViewState(_base: string | null, _s: AsyncState<Rollup>): AsyncStatus {
  throw new Error('not implemented')
}

export type EntityHealth =
  | { kind: 'no-invoices' }
  | { kind: 'needs-attention'; count: number }
  | { kind: 'clear' }

export function entityHealth(_clients: RollupClient[], _entityId: string): EntityHealth {
  throw new Error('not implemented')
}
