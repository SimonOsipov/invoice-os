// Plain-TS ports of the prototype's helper methods (Developer Console.dc.html,
// Component class, line ~798 onward). Kept dependency-free of React so they
// can be unit-testable pure functions; components call these to build the
// exact same derived/rendered values the prototype computed in renderVals().

import type { ReactNode } from 'react'
import type { Env, Job, JobState } from './types'

/* ---------- status styling (this.st / this.sev) ---------- */

export type StatusStyle = { bg: string; border: string; text: string; label: string; dot: string }

// `accepted` renders the label CLEARED (proto:803) — the state KEY stays `accepted`.
// This one entry feeds the filter chip, the table pill, the drawer badge and the
// drawer timeline's derived final step.
const JOB_STATE_STYLE: Record<JobState, [string, string, string, string]> = {
  queued: ['var(--status-muted-bg)', 'var(--status-muted-border)', 'var(--status-muted-text)', 'QUEUED'],
  submitting: ['#E6EEFA', '#AFC9EC', '#1E5AA8', 'SUBMITTING'],
  pending: ['var(--status-amber-bg)', 'var(--status-amber-border)', 'var(--status-amber-text)', 'PENDING'],
  accepted: ['var(--status-green-bg)', 'var(--status-green-border)', 'var(--status-green-text)', 'CLEARED'],
  rejected: ['var(--status-red-bg)', 'var(--status-red-border)', 'var(--status-red-text)', 'REJECTED'],
  failed: ['var(--status-red-bg)', 'var(--status-red-border)', 'var(--status-red-text)', 'FAILED'],
  'dead-letter': ['#F7D7D2', '#D98A80', '#8A1F18', 'DEAD-LETTER'],
}

export function jobStateStyle(state: JobState): StatusStyle {
  const [bg, border, text, label] = JOB_STATE_STYLE[state]
  return { bg, border, text, label, dot: text }
}

/* ---------- payload builders (this.reqJSON / this.resJSON) ---------- */

// proto:820-821 — seller/buyer shape. `tenant_tin`/`app_target` are gone.
export function reqJSON(j: Pick<Job, 'id' | 'buyer' | 'btin' | 'invoice' | 'raw' | 'desc'>, env: Env): string {
  const net = Math.round(j.raw / 1.075)
  const vat = j.raw - net
  return (
    '{\n  "idempotency_key": "' +
    j.id.replace('sub_', 'idem_') +
    '",\n  "environment": "' +
    env +
    '",\n  "seller": { "name": "Zephyr Pay", "tin": "31882204-0001" },\n  "buyer": { "name": "' +
    j.buyer +
    '", "tin": "' +
    j.btin +
    '" },\n  "invoice": {\n    "invoice_no": "' +
    j.invoice +
    '",\n    "currency": "NGN",\n    "total": ' +
    j.raw +
    ',\n    "vat_rate": 7.5,\n    "lines": [ { "desc": "' +
    j.desc +
    '", "net": ' +
    net +
    ', "vat": ' +
    vat +
    ' } ]\n  }\n}'
  )
}

// proto:824-828.
export function resJSON(j: { state: JobState; invoice: string }): string {
  if (j.state === 'accepted')
    return (
      '{\n  "status": "CLEARED",\n  "irn": "IRN-NG-' +
      j.invoice.slice(-5) +
      '-A91",\n  "csid": "MBS.9f2a\u2026c7",\n  "qr": "data:csid;base64,iVBORw0\u2026",\n  "cleared_at": "2026-07-18T09:14:22Z"\n}'
    )
  if (j.state === 'rejected')
    return '{\n  "status": "REJECTED",\n  "code": "MBS-422",\n  "errors": [ { "field": "buyer.tin", "msg": "TIN not registered with FIRS" } ]\n}'
  if (j.state === 'dead-letter')
    return '{\n  "status": "ERROR",\n  "http": 503,\n  "code": "GATEWAY_TIMEOUT",\n  "retries_exhausted": true,\n  "last_attempt": "2026-07-18T08:02:10Z"\n}'
  if (j.state === 'failed')
    return '{\n  "status": "SCHEMA_ERROR",\n  "errors": [ { "ptr": "/lines/2/description", "msg": "required" } ]\n}'
  return '{\n  "status": "PENDING",\n  "poll_after": "2026-07-18T09:20:00Z"\n}'
}

/* ---------- submission drawer builder (this.buildJobDrawer) ---------- */

export type JobTimelineStep = { label: string; ts: string; detail: string; color: string; dotBg: string; dotBorder: string; line: string }
export type JobValidationCheck = { label: string; icon: ReactNode; color: string; note: string }

export type JobDrawerView = {
  id: string
  buyer: string
  invoice: string
  amount: string
  attempts: number
  age: string
  idem: string
  stBg: string
  stBorder: string
  stText: string
  stDot: string
  stLabel: string
  checks: JobValidationCheck[]
  timeline: JobTimelineStep[]
  request: string
  response: string
}

function timelineStep(label: string, done: boolean, active: boolean, ts: string, detail: string): JobTimelineStep {
  return {
    label,
    ts,
    detail,
    color: active ? 'var(--fg-1)' : done ? 'var(--fg-2)' : 'var(--fg-4)',
    dotBg: active || done ? 'var(--accent)' : 'var(--bg-3)',
    dotBorder: active || done ? 'var(--accent)' : 'var(--line-3)',
    line: done ? 'var(--accent)' : 'var(--line-2)',
  }
}

// The pass/fail glyphs are injected rather than imported so this module stays free
// of React values — the prototype's own builder takes them the same way (proto:1160).
export function buildSubmissionDrawer(j: Job, env: Env, amount: string, checkGlyph: ReactNode, xGlyph: ReactNode): JobDrawerView {
  const b = jobStateStyle(j.state)
  const isDeadEnd = j.state === 'dead-letter' || j.state === 'failed' || j.state === 'rejected'
  const finalLabel = b.label.charAt(0) + b.label.slice(1).toLowerCase()
  const timeline: JobTimelineStep[] = [
    timelineStep('Received', true, false, '08:01:55', 'POST /v2/invoices \u00b7 validated locally'),
    timelineStep('Queued', true, false, '08:01:58', 'idempotency key assigned'),
    timelineStep('Submitting', true, false, '08:02:01', 'forwarded to FIRS/MBS'),
    isDeadEnd
      ? timelineStep(finalLabel, true, true, '08:02:10', j.lastError)
      : timelineStep(
          finalLabel,
          true,
          true,
          j.state === 'accepted' ? '09:14:22' : '09:20:00',
          j.state === 'accepted' ? 'IRN issued \u00b7 evidence signed' : 'awaiting clearance',
        ),
  ]
  const ok = { icon: checkGlyph, color: 'var(--status-green-text)' }
  const fail = { icon: xGlyph, color: 'var(--status-red-text)' }
  const checks: JobValidationCheck[] = [
    { label: 'Buyer TIN registered', ...(j.state === 'rejected' ? fail : ok), note: j.state === 'rejected' ? 'NOT REGISTERED' : 'PASS' },
    { label: 'VAT math (7.5%)', ...ok, note: 'PASS' },
    { label: 'Line descriptions present', ...(j.state === 'failed' ? fail : ok), note: j.state === 'failed' ? 'MISSING' : 'PASS' },
    { label: 'Currency supported (NGN)', ...ok, note: 'PASS' },
    { label: 'Invoice number unique', ...ok, note: 'PASS' },
  ]
  return {
    id: j.id,
    buyer: j.buyer,
    invoice: j.invoice,
    amount,
    attempts: j.attempts,
    age: j.age,
    idem: j.id.replace('sub_', 'idem_') + 'c3',
    stBg: b.bg,
    stBorder: b.border,
    stText: b.text,
    stDot: b.dot,
    stLabel: b.label,
    checks,
    timeline,
    request: reqJSON(j, env),
    response: resJSON(j),
  }
}
