// Per-connector integration detail, derived deterministically from the connector id
// via the same seeded PRNG the client fixtures use (lib/prng.ts). Every connector shows
// stable but distinct numbers across reloads and across list <-> detail navigation.
//
// The figures are internally consistent, not independently random:
// - IN ERP  = the sum of the 30-day pull volume (the funnel's period IS the chart's period)
// - HELD    = IN ERP - TRANSMITTED  ("pulled but not transmitted")
// - DRIFT   = TRANSMITTED - FIRS-ACCEPTED  ("not yet acknowledged by FIRS")
// - the master-data tax-code count is the length of the tax-code list rendered beside it
// - write-back covers the FIRS-accepted documents: stamped + pending + failed = ACCEPTED

import { hash, mulberry } from './prng'
import { pad2 } from './format'
import { CONNECTOR_TAX_CODES, type ConnectorDef } from '../data'
import type { ConnectorId, FieldMapRow } from '../types'

export type SyncEventKind = 'transmitted' | 'validated' | 'held' | 'scheduled' | 'pull'

export type SyncEvent = {
  time: string
  kind: SyncEventKind
  doc: string
  desc: string
}

export type HeldDoc = {
  doc: string
  reason: string
  age: string
}

export type ConnectorDetailData = {
  lastSync: string
  frequency: string
  queueDepth: number
  tokenExpires: string
  errorRate: string
  funnel: { inErp: number; validated: number; transmitted: number; accepted: number; drift: number }
  volume: number[]
  volumeTotal: number
  activity: SyncEvent[]
  master: { customers: number; taxCodes: number; items: number; uoms: number }
  writeBack: { stamped: number; pending: number; failed: number; pct: number }
  held: HeldDoc[]
  heldTotal: number
}

const FREQUENCIES = ['Every 5 min', 'Every 15 min', 'Every 30 min', 'Hourly']

const HELD_REASONS = [
  'Buyer TIN missing',
  'Billing address incomplete',
  'VAT does not reconcile to lines',
  'Currency not declared',
  'Duplicate invoice number',
  'Tax-point date outside open period',
]

/** Integer in [lo, hi]. */
function pick(rnd: () => number, lo: number, hi: number): number {
  return lo + Math.floor(rnd() * (hi - lo + 1))
}

function docId(rnd: () => number): string {
  return 'INV-' + pick(rnd, 2000, 2999)
}

// 30 daily pull counts with a weekly rhythm — the two weekend days of each 7-day
// block run light, which is what makes the bar chart read as an ERP and not as noise.
function volumeSeries(rnd: () => number): number[] {
  return Array.from({ length: 30 }, (_, i) => {
    const weekend = i % 7 === 5 || i % 7 === 6
    const base = 34 + rnd() * 56
    return Math.round(weekend ? base * 0.34 : base)
  })
}

function activityFeed(rnd: () => number, def: ConnectorDef): SyncEvent[] {
  const kinds: SyncEventKind[] = ['transmitted', 'validated', 'held', 'transmitted', 'pull', 'validated', 'transmitted']
  let minutes = 14 * 60 + 32
  const rows: SyncEvent[] = [
    { time: '15:00', kind: 'scheduled', doc: '—', desc: 'Next scheduled pull' },
  ]
  kinds.forEach((kind) => {
    const doc = docId(rnd)
    const desc =
      kind === 'transmitted'
        ? 'Transmitted to FIRS · IRN assigned'
        : kind === 'validated'
          ? 'Passed 16-check MBS rule pack'
          : kind === 'held'
            ? 'Held — ' + HELD_REASONS[pick(rnd, 0, HELD_REASONS.length - 1)]
            : 'Pulled ' + pick(rnd, 8, 46) + ' documents from ' + def.name
    rows.push({ time: pad2(Math.floor(minutes / 60)) + ':' + pad2(minutes % 60), kind, doc: kind === 'pull' ? '—' : doc, desc })
    minutes -= pick(rnd, 4, 23)
  })
  return rows
}

// Oldest first — an exceptions queue that isn't sorted by age is the one thing a
// finance team would never ship.
function heldDocs(rnd: () => number, count: number): HeldDoc[] {
  const rows = Array.from({ length: Math.min(4, count) }, () => ({
    doc: docId(rnd),
    reason: HELD_REASONS[pick(rnd, 0, HELD_REASONS.length - 1)],
    minutes: pick(rnd, 45, 2400),
  }))
  rows.sort((a, b) => b.minutes - a.minutes)
  return rows.map(({ doc, reason, minutes }) => {
    const h = Math.floor(minutes / 60)
    return { doc, reason, age: h < 24 ? h + 'h ' + (minutes % 60) + 'm' : Math.floor(h / 24) + 'd ' + (h % 24) + 'h' }
  })
}

export function connectorDetail(def: ConnectorDef): ConnectorDetailData {
  const rnd = mulberry(hash('connector:' + def.id))

  const volume = volumeSeries(rnd)
  const inErp = volume.reduce((s, v) => s + v, 0)
  const validated = inErp - pick(rnd, 8, 60)
  const transmitted = validated - pick(rnd, 3, 26)
  // Weighted so roughly half the connectors sit at a clean drift of 0 (green badge).
  const drift = rnd() < 0.45 ? 0 : pick(rnd, 1, 9)
  const accepted = transmitted - drift
  const heldTotal = inErp - transmitted

  // Write-back is driven from a target percentage rather than an absolute pending count:
  // at these volumes a fixed 3-22 pending rounds to a 99% bar on every connector, which
  // makes the progress bar read as decoration. The remainder splits pending/failed.
  const stamped = Math.round(accepted * (0.86 + rnd() * 0.135))
  const failed = Math.min(accepted - stamped, pick(rnd, 0, 5))
  const pending = accepted - stamped - failed

  return {
    lastSync: pick(rnd, 1, 14) + ' min ago',
    frequency: FREQUENCIES[pick(rnd, 0, FREQUENCIES.length - 1)],
    queueDepth: pick(rnd, 0, 38),
    tokenExpires: 'in ' + pick(rnd, 4, 88) + ' days',
    errorRate: (rnd() * 1.4).toFixed(2) + '%',
    funnel: { inErp, validated, transmitted, accepted, drift },
    volume,
    volumeTotal: inErp,
    activity: activityFeed(rnd, def),
    master: { customers: pick(rnd, 180, 1400), taxCodes: CONNECTOR_TAX_CODES.length, items: pick(rnd, 400, 4200), uoms: pick(rnd, 8, 26) },
    writeBack: { stamped, pending, failed, pct: Math.round((stamped / accepted) * 100) },
    held: heldDocs(rnd, heldTotal),
    heldTotal,
  }
}

/** The mapping a connector renders: the saved override when one exists, else its default. */
export function mappingFor(def: ConnectorDef, overrides: Partial<Record<ConnectorId, FieldMapRow[]>>): FieldMapRow[] {
  return overrides[def.id] ?? def.mapping
}
