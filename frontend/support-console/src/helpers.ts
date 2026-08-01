// Pure presentation helpers, extracted from the prototype's `st()` / `sev()` /
// `mkSpark()` (Support Console.dc.html:770-791, 977-985) so they can be unit-tested
// without rendering. Everything here returns design-system token NAMES, never literals.

import type { HealthCard, JobState, Severity } from './types'

export interface StatusStyle {
  bg: string
  border: string
  text: string
  label: string
}

// proto:770. The submission-state triplet: background, hairline, text — plus the
// uppercase badge label. `dot` in the prototype was always equal to `text`, so callers
// here just reuse `text` rather than carrying a fourth identical field.
const STATUS: Record<JobState, StatusStyle> = {
  queued: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'QUEUED' },
  submitting: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'SUBMITTING' },
  pending: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'PENDING' },
  accepted: { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)', label: 'ACCEPTED' },
  rejected: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'REJECTED' },
  failed: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'FAILED' },
  'dead-letter': { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'DEAD-LETTER' },
}

export function statusStyle(state: JobState): StatusStyle {
  return STATUS[state]
}

// proto:783. Rule severity uses the same triplet vocabulary as submission state, but a
// different mapping — warn is amber, info is muted, and there is no green.
const SEVERITY: Record<Severity, StatusStyle> = {
  error: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'ERROR' },
  warn: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'WARN' },
  info: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'INFO' },
}

export function severityStyle(severity: Severity): StatusStyle {
  return SEVERITY[severity]
}

// proto:1145. The drawer's final timeline row titles itself from the badge label —
// 'DEAD-LETTER' becomes 'Dead-letter', 'ACCEPTED' becomes 'Accepted'.
export function sentenceCase(label: string): string {
  return label.charAt(0) + label.slice(1).toLowerCase()
}

// proto:977. Builds the two SVG path strings behind each system-health sparkline: the
// stroked line and the filled area under it, in a fixed 220x44 viewBox.
//
// A flat series (every point equal) would divide by zero, so the range floors at 1 — the
// prototype's own `|| 1` guard, kept because 'Dead-letter' genuinely goes flat at 0 once
// every dead-letter job has been re-driven.
export const SPARK_WIDTH = 220
export const SPARK_HEIGHT = 44

export function sparkline(points: number[]): { line: string; area: string } {
  if (points.length < 2) {
    return { line: '', area: '' }
  }
  const max = Math.max(...points)
  const min = Math.min(...points)
  const range = max - min || 1
  const step = SPARK_WIDTH / (points.length - 1)
  const xy = points.map((p, i) => [i * step, SPARK_HEIGHT - 4 - ((p - min) / range) * (SPARK_HEIGHT - 10)] as const)
  const line = xy.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`).join(' ')
  const area = `${line} L${SPARK_WIDTH} ${SPARK_HEIGHT} L0 ${SPARK_HEIGHT} Z`
  return { line, area }
}

// The health card's accent pair. proto:988 passed stroke+fill positionally per card;
// this derives both from the card's own tone so a card can never be given a green dot
// and an amber fill.
export function healthTone(tone: HealthCard['tone']): { dot: string; stroke: string; fill: string } {
  if (tone === 'amber') {
    return { dot: 'var(--status-amber-text)', stroke: 'var(--status-amber-text)', fill: 'var(--status-amber-bg)' }
  }
  if (tone === 'red') {
    return { dot: 'var(--status-red-text)', stroke: 'var(--status-red-text)', fill: 'var(--status-red-bg)' }
  }
  return { dot: 'var(--status-green-text)', stroke: 'var(--action)', fill: 'var(--action-tint)' }
}

// proto:803. The APP request payload the job drawer and the audit drawer both show.
export function requestJSON(j: { id: string; tin: string; invoice: string; app: string }, env: string): string {
  return `{
  "idempotency_key": "${j.id.replace('job_', 'idem_')}",
  "environment": "${env}",
  "tenant_tin": "${j.tin.replace('TIN ', '')}",
  "invoice": {
    "invoice_no": "${j.invoice}",
    "currency": "NGN",
    "vat_rate": 7.5,
    "lines": [ { "desc": "Freight", "net": 4120000, "vat": 309000 } ]
  },
  "app_target": "${j.app}"
}`
}

// proto:805. The APP response, keyed on terminal state.
export function responseJSON(j: { state: JobState; invoice: string }): string {
  switch (j.state) {
    case 'accepted':
      return `{
  "status": "ACCEPTED",
  "irn": "IRN-NG-${j.invoice.slice(-5)}-A91",
  "qr": "data:csid;base64,iVBORw0…",
  "cleared_at": "2026-06-30T09:14:22Z"
}`
    case 'rejected':
      return `{
  "status": "REJECTED",
  "code": "MBS-422",
  "errors": [ { "field": "buyer.tin", "msg": "TIN not registered with NRS" } ]
}`
    case 'dead-letter':
      return `{
  "status": "ERROR",
  "http": 503,
  "code": "GATEWAY_TIMEOUT",
  "retries_exhausted": true,
  "last_attempt": "2026-06-30T08:02:10Z"
}`
    case 'failed':
      return `{
  "status": "SCHEMA_ERROR",
  "errors": [ { "ptr": "/lines/2/vat_rate", "msg": "required" } ]
}`
    default:
      return `{
  "status": "PENDING",
  "poll_after": "2026-06-30T09:20:00Z"
}`
  }
}
