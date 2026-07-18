// Plain-TS ports of the prototype's helper methods (Developer Console.dc.html,
// Component class, line ~798 onward). Kept dependency-free of React so they
// can be unit-testable pure functions; components call these to build the
// exact same derived/rendered values the prototype computed in renderVals().

import type { Env, Job, JobState } from './types'

/* ---------- status styling (this.st / this.sev) ---------- */

export type StatusStyle = { bg: string; border: string; text: string; label: string; dot: string }

const JOB_STATE_STYLE: Record<JobState, [string, string, string, string]> = {
  queued: ['var(--status-muted-bg)', 'var(--status-muted-border)', 'var(--status-muted-text)', 'QUEUED'],
  submitting: ['#E6EEFA', '#AFC9EC', '#1E5AA8', 'SUBMITTING'],
  pending: ['var(--status-amber-bg)', 'var(--status-amber-border)', 'var(--status-amber-text)', 'PENDING'],
  accepted: ['var(--status-green-bg)', 'var(--status-green-border)', 'var(--status-green-text)', 'ACCEPTED'],
  rejected: ['var(--status-red-bg)', 'var(--status-red-border)', 'var(--status-red-text)', 'REJECTED'],
  failed: ['var(--status-red-bg)', 'var(--status-red-border)', 'var(--status-red-text)', 'FAILED'],
  'dead-letter': ['#F7D7D2', '#D98A80', '#8A1F18', 'DEAD-LETTER'],
}

export function jobStateStyle(state: JobState): StatusStyle {
  const [bg, border, text, label] = JOB_STATE_STYLE[state]
  return { bg, border, text, label, dot: text }
}

/* ---------- payload builders (this.reqJSON / this.resJSON) ---------- */

export function reqJSON(j: { id: string; tin: string; invoice: string; app: string }, env: Env): string {
  return (
    '{\n  "idempotency_key": "' +
    j.id.replace('job_', 'idem_') +
    '",\n  "environment": "' +
    env +
    '",\n  "tenant_tin": "' +
    j.tin.replace('TIN ', '') +
    '",\n  "invoice": {\n    "invoice_no": "' +
    j.invoice +
    '",\n    "currency": "NGN",\n    "vat_rate": 7.5,\n    "lines": [ { "desc": "Freight", "net": 4120000, "vat": 309000 } ]\n  },\n  "app_target": "' +
    j.app +
    '"\n}'
  )
}

export function resJSON(j: { state: JobState; invoice: string }): string {
  if (j.state === 'accepted')
    return (
      '{\n  "status": "ACCEPTED",\n  "irn": "IRN-NG-' +
      j.invoice.slice(-5) +
      '-A91",\n  "qr": "data:csid;base64,iVBORw0…",\n  "cleared_at": "2026-06-30T09:14:22Z"\n}'
    )
  if (j.state === 'rejected')
    return '{\n  "status": "REJECTED",\n  "code": "MBS-422",\n  "errors": [ { "field": "buyer.tin", "msg": "TIN not registered with FIRS" } ]\n}'
  if (j.state === 'dead-letter')
    return '{\n  "status": "ERROR",\n  "http": 503,\n  "code": "GATEWAY_TIMEOUT",\n  "retries_exhausted": true,\n  "last_attempt": "2026-06-30T08:02:10Z"\n}'
  if (j.state === 'failed')
    return '{\n  "status": "SCHEMA_ERROR",\n  "errors": [ { "ptr": "/lines/2/vat_rate", "msg": "required" } ]\n}'
  return '{\n  "status": "PENDING",\n  "poll_after": "2026-06-30T09:20:00Z"\n}'
}

/* ---------- job drawer builder (this.buildJobDrawer) ---------- */

export type JobTimelineStep = { label: string; ts: string; detail: string; color: string; dotBg: string; dotBorder: string; line: string }
export type JobRetryEntry = { at: string; backoff: string }
export type JobPollEntry = { at: string; result: string; color: string }

export type JobDrawerView = {
  id: string
  tenant: string
  invoice: string
  app: string
  attempts: number
  age: string
  idem: string
  stBg: string
  stBorder: string
  stText: string
  stDot: string
  stLabel: string
  timeline: JobTimelineStep[]
  retries: JobRetryEntry[]
  polls: JobPollEntry[]
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

export function buildJobDrawer(j: Job, env: Env): JobDrawerView {
  const b = jobStateStyle(j.state)
  const isDeadEnd = j.state === 'dead-letter' || j.state === 'failed' || j.state === 'rejected'
  const finalLabel = b.label.charAt(0) + b.label.slice(1).toLowerCase()
  const timeline: JobTimelineStep[] = [
    timelineStep('Ingested', true, false, '08:01:55', 'Validated against rule-set v8'),
    timelineStep('Queued', true, false, '08:01:58', 'idempotency key assigned'),
    timelineStep('Submitting', true, false, '08:02:01', 'POST → ' + j.app),
    isDeadEnd
      ? timelineStep(finalLabel, true, true, '08:02:10', j.lastError)
      : timelineStep(finalLabel, true, true, '09:14:22', j.state === 'accepted' ? 'IRN cleared' : 'awaiting APP clearance'),
  ]
  return {
    id: j.id,
    tenant: j.tenant,
    invoice: j.invoice,
    app: j.app,
    attempts: j.attempts,
    age: j.age,
    idem: j.id.replace('job_', 'idem_') + 'c3',
    stBg: b.bg,
    stBorder: b.border,
    stText: b.text,
    stDot: b.dot,
    stLabel: b.label,
    timeline,
    retries: [
      { at: 'attempt 1 · 08:02:01', backoff: '+0s' },
      { at: 'attempt 2 · 08:02:11', backoff: '+10s' },
      { at: 'attempt 3 · 08:02:41', backoff: '+30s' },
    ].slice(0, Math.max(1, j.attempts)),
    polls: [
      { at: '08:05:00', result: '202 pending', color: 'var(--status-amber-text)' },
      { at: '08:20:00', result: '202 pending', color: 'var(--status-amber-text)' },
      {
        at: '09:14:22',
        result: j.state === 'accepted' ? '200 accepted' : '503 timeout',
        color: j.state === 'accepted' ? 'var(--status-green-text)' : 'var(--status-red-text)',
      },
    ],
    request: reqJSON(j, env),
    response: resJSON(j),
  }
}
