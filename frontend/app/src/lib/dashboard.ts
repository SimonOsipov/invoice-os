// App-side dashboard rollup data-access helpers (M4-10-01, task-189).
//
// Types mirror the wire shapes in internal/dashboard/dashboard.go: `Bucket` is embedded
// anonymously in `Client`, so encoding/json promotes EVERY Bucket key — `counts`, both
// overlays, `metrics`, `top_violations` — to the row's top level. RollupClient below
// spells that promotion out explicitly rather than modeling the Go embedding.
// `Rollup.clients`/`.top_violations` are never null on the
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
// - scopedBucket ([dashboard-scope-per-client], persona-handoff-fix step 2) resolves
//   which Bucket a CLIENT-scoped surface (DashboardActive's KPIs/donut/needs-attention,
//   Sidebar's nav badges) renders for the CURRENT selection — in-house to rollup.totals
//   (its one "client" IS the tenant), firm mode to the selected entity's own `clients` row,
//   both a null entityId and an entity absent from `clients` (zero invoices, INNER JOIN)
//   to EMPTY_BUCKET. Reuses entityHealth's own `clients.find` join rather than a second
//   lookup convention.
import type { AuthedFetch } from './portfolio'
import type { DonutSeg } from '../types'
import type { AsyncState, AsyncStatus } from '@invoice-os/api-client'

import type { InvoiceStatus } from './invoices'
import { fmtShort } from './format'

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

// dashboard.go Metric — a num/den pair; the registry below decides how to read it, not
// the wire (no kind discriminator).
export interface Metric {
  num: number
  den: number
}
export type Metrics = Record<string, Metric>

// dashboard.go Bucket — the 7-state counts plus TWO overlapping overlays, neither ever a
// donut input: needs_attention (rejected ∪ failed ∪ drafts-with-an-error-severity-violation
// ∪ drafts an approver sent back)
// and awaiting_approval (validated invoices an active approval policy blocks). They
// partition by invoice status, so neither is derivable from the other — do not merge them.
// Sibling of RollupClient below — kept in sync by hand, not by `extends`.
export interface RollupBucket {
  counts: Counts
  needs_attention: number
  awaiting_approval: number
  metrics: Metrics
  top_violations: RuleCount[]
}

// dashboard.go Client — Bucket is embedded anonymously there, so counts/needs_attention/
// awaiting_approval/metrics/top_violations promote to this row's top level on the wire;
// entity_id/entity_name are the row's own fields. Only entities WITH at least one invoice appear
// here (INNER JOIN, store.go). Sibling of RollupBucket above — kept in sync by hand.
export interface RollupClient {
  entity_id: string
  entity_name: string
  counts: Counts
  needs_attention: number
  awaiting_approval: number
  metrics: Metrics
  top_violations: RuleCount[]
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
// its first letter capitalized (Draft, Validated, …), and the segment colour comes from
// DONUT_COLOR below — deliberately NOT the badge palette, see its comment.
const CANONICAL_STATES: InvoiceStatus[] = [
  'draft',
  'validated',
  'queued',
  'submitted',
  'accepted',
  'rejected',
  'failed',
]

// The donut needs seven distinguishable arcs, but the badge palette collapses the
// seven states onto four colours — and it puts the duplicates ADJACENT (queued
// beside submitted, rejected beside failed), so contiguous arcs of the same fill
// merged into one band and the legend showed duplicate swatches.
//
// The prototype solves exactly this with a lightness ramp inside one hue rather
// than a separator or a gap (proto:1740 pairs deep --status-green-text with light
// --teal-400). This applies the same idea across all three families, so no two
// neighbouring arcs share a fill. Badges are unaffected — they keep
// invoiceStatusStyle, where a shared colour is fine because each pill is labelled.
const DONUT_COLOR: Record<InvoiceStatus, string> = {
  draft: 'var(--status-muted-border)',
  validated: 'var(--teal-400)',
  queued: 'var(--accent)',
  submitted: 'var(--status-amber-text)',
  accepted: 'var(--status-green-text)',
  rejected: 'var(--status-red-text)',
  failed: 'var(--destructive)',
}

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
      color: DONUT_COLOR[state],
      count: String(count),
      pct: Math.round((count / total) * 100) + '%',
      dash: len.toFixed(1) + ' ' + (C - len).toFixed(1),
      offset: (-acc).toFixed(1),
    }
    acc += len
    return seg
  })
}

// Domain acronyms that stay uppercase inside a human label. Everything else is
// sentence case: rule keys are machine identifiers, and Title-Casing them word
// by word leaked "Vat Standard Rate" and "Supplier Tin Required" into the UI.
// The system is sentence case everywhere, with VAT/TIN/MBS/FIRS uppercase.
const RULE_KEY_ACRONYMS = new Set(['vat', 'tin', 'mbs', 'firs', 'irn', 'csid', 'ngn', 'ubl', 'xml', 'json', 'pdf', 'api', 'erp', 'id'])

export function deslug(ruleKey: string): string {
  return ruleKey
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((w, i) => {
      const lower = w.toLowerCase()
      if (RULE_KEY_ACRONYMS.has(lower)) return lower.toUpperCase()
      return i === 0 ? lower.charAt(0).toUpperCase() + lower.slice(1) : lower
    })
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

// Zero-state bucket for a firm-mode selection with nothing to scope to: no entity
// resolved yet ([entity-picker] trap 2 — loading/error/no-gateway/zero-entities
// placeholder) or a real entity with zero invoices (INNER JOIN excludes it from
// `clients`, dashboard/store.go). Never widened to rollup.totals — that would silently
// re-show every OTHER client's numbers under this one's name, exactly the bug
// [dashboard-scope-per-client] replaces.
const EMPTY_COUNTS: Counts = { draft: 0, validated: 0, queued: 0, submitted: 0, accepted: 0, rejected: 0, failed: 0 }
export const EMPTY_BUCKET: RollupBucket = { counts: EMPTY_COUNTS, needs_attention: 0, awaiting_approval: 0, metrics: {}, top_violations: [] }

// Resolves which Bucket a CLIENT-scoped surface renders for the current selection
// ([dashboard-scope-per-client]). In-house has ZERO business_entities rows
// (db/seed.dev.sql seeds the firm tenant only, [entity-picker] trap 1) — its one
// "client" IS the tenant, so rollup.totals is already the correct (and only) scope,
// never a `clients` lookup keyed by its always-null entityId. Firm mode scopes to the
// selected entity's own row; `entityId === null` and "entity has no row in `clients`"
// both fall through to EMPTY_BUCKET rather than rollup.totals.
export function scopedBucket(isInhouse: boolean, entityId: string | null, rollup: Rollup): RollupBucket {
  if (isInhouse) return rollup.totals
  if (entityId == null) return EMPTY_BUCKET
  const client = rollup.clients.find((c) => c.entity_id === entityId)
  return client
    ? {
        counts: client.counts,
        needs_attention: client.needs_attention,
        awaiting_approval: client.awaiting_approval,
        metrics: client.metrics,
        top_violations: client.top_violations,
      }
    : EMPTY_BUCKET
}

// Registry deciding how each metric key reads — no kind discriminator on the wire.
const METRIC_KIND: Record<string, 'ratio' | 'count' | 'amount'> = {
  readiness: 'ratio',
  bar_field_completeness: 'ratio',
  bar_tax_accuracy: 'ratio',
  bar_identifiers_format: 'ratio',
  blocked_by_rules: 'count',
  failed_in_transmission: 'count',
  never_validated: 'count',
  vat_tracked: 'amount',
}

// null — never 0 — is the empty signal for an absent metric or a zero denominator.
export function metricRatio(m: Metrics, key: string): number | null {
  const metric = m[key]
  if (!metric || metric.den === 0) return null
  return Math.round((metric.num / metric.den) * 100)
}

export function metricCount(m: Metrics, key: string): number | null {
  const metric = m[key]
  return metric ? metric.num : null
}

// An unregistered key reads '—' rather than throwing — formatMetric must survive a
// future metric the frontend hasn't been told the kind of yet.
export function formatMetric(m: Metrics, key: string): string {
  const kind = METRIC_KIND[key]
  if (kind === 'ratio') {
    const pct = metricRatio(m, key)
    return pct === null ? '—' : pct + '%'
  }
  if (kind === 'count') {
    const count = metricCount(m, key)
    return count === null ? '—' : String(count)
  }
  if (kind === 'amount') {
    const metric = m[key]
    return metric ? fmtShort(metric.num / 100) : '—' // wire is kobo; naira once, here
  }
  return '—'
}

// Ring geometry and colour bands ported verbatim from dashboardMock.ts's shipped tile
// (R=50) so the real data doesn't visually shift it.
export function readinessRing(m: Metrics): { score: number | null; circ: string; offset: string; color: string } {
  const circ = 2 * Math.PI * 50
  const score = metricRatio(m, 'readiness')
  const color =
    score === null
      ? 'var(--status-muted-text)'
      : score >= 85
        ? 'var(--action)'
        : score >= 70
          ? 'var(--status-amber-text)'
          : 'var(--status-red-text)'
  const offset = score === null ? circ : circ * (1 - score / 100)
  return { score, circ: circ.toFixed(1), offset: offset.toFixed(1), color }
}

const BAR_METRICS: { key: string; label: string }[] = [
  { key: 'bar_field_completeness', label: 'Field completeness' },
  { key: 'bar_tax_accuracy', label: 'Tax accuracy · VAT / WHT' },
  { key: 'bar_identifiers_format', label: 'Identifiers & format' },
]

export function readinessBars(m: Metrics): { label: string; pct: number | null; pctLabel: string; color: string }[] {
  return BAR_METRICS.map(({ key, label }) => {
    const pct = metricRatio(m, key)
    const color = pct === null ? 'var(--status-muted-text)' : pct >= 85 ? 'var(--status-green-text)' : 'var(--status-amber-text)'
    return { label, pct, pctLabel: pct === null ? '—' : pct + '%', color }
  })
}

const NOTE_CLAUSES: { key: string; suffix: string }[] = [
  { key: 'blocked_by_rules', suffix: 'blocked by rules' },
  { key: 'failed_in_transmission', suffix: 'failed in transmission' },
  { key: 'never_validated', suffix: 'not yet checked' },
]

// [note-copy]: fixed clause order, zero clauses omitted, all-zero and no-metrics each
// get their own pinned sentence.
export function readinessNote(m: Metrics): string {
  if (Object.keys(m).length === 0) return 'No invoices yet'
  const clauses = NOTE_CLAUSES.map(({ key, suffix }) => ({ n: metricCount(m, key) ?? 0, suffix })).filter((c) => c.n > 0)
  if (clauses.length === 0) return 'All invoices checked and clear of blocking rules.'
  return clauses.map((c) => `${c.n} ${c.suffix}`).join(' · ')
}
