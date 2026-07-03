// Plain-TS ports of the prototype's helper methods (OpsConsole.dc.html,
// Component class, line ~759 onward). Kept dependency-free of React so they
// can be unit-testable pure functions; components call these to build the
// exact same derived/rendered values the prototype computed in renderVals().

import type { Env, Job, JobState, Rule, Severity } from './types'

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

const SEVERITY_STYLE: Record<Severity, [string, string, string, string]> = {
  error: ['var(--status-red-bg)', 'var(--status-red-border)', 'var(--status-red-text)', 'ERROR'],
  warn: ['var(--status-amber-bg)', 'var(--status-amber-border)', 'var(--status-amber-text)', 'WARN'],
  info: ['var(--status-muted-bg)', 'var(--status-muted-border)', 'var(--status-muted-text)', 'INFO'],
}

export function severityStyle(sev: Severity): StatusStyle {
  const [bg, border, text, label] = SEVERITY_STYLE[sev]
  return { bg, border, text, label, dot: text }
}

/* ---------- audit icon tone (inline in prototype's auditRows map) ---------- */

export type AuditTone = 'green' | 'red' | 'amber' | 'teal'

export function auditToneColor(tone: AuditTone): { bg: string; color: string } {
  if (tone === 'red') return { bg: 'var(--status-red-bg)', color: 'var(--status-red-text)' }
  if (tone === 'amber') return { bg: 'var(--status-amber-bg)', color: 'var(--status-amber-text)' }
  if (tone === 'green') return { bg: 'var(--status-green-bg)', color: 'var(--status-green-text)' }
  return { bg: 'var(--accent-tint)', color: 'var(--accent)' }
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

/* ---------- rule drawer builder (this.buildRuleDrawer) ---------- */

export type RuleParam = { label: string; value: string }

const RULE_PARAMS_BY_TYPE: Record<string, RuleParam[]> = {
  tax_math: [
    { label: 'Operation', value: 'multiply' },
    { label: 'Operand (rate)', value: '0.075' },
    { label: 'Tolerance', value: '±0.01 NGN' },
  ],
  'format-regex': [
    { label: 'Pattern', value: '^\\d{8}-\\d{4}$' },
    { label: 'Flags', value: 'none' },
  ],
  required: [{ label: 'Applies when', value: 'always' }],
  enum: [{ label: 'Allowed values', value: 'NGN, USD, EUR' }],
  range: [
    { label: 'Min', value: '1' },
    { label: 'Max', value: '100000' },
  ],
  cross_field: [
    { label: 'When', value: 'line.type == "service"' },
    { label: 'Require', value: 'line.wht > 0' },
  ],
  date_rule: [{ label: 'Constraint', value: 'issue_date >= prev.issue_date' }],
  'expression-CEL': [{ label: 'CEL', value: 'unique(invoice_no, seller_tin)' }],
}

export type RuleDrawerView = {
  key: string
  type: string
  field: string
  message: string
  params: RuleParam[]
  json: string
  enabledLabel: string
  enabledColor: string
}

export function buildRuleDrawer(r: Rule): RuleDrawerView {
  const params = RULE_PARAMS_BY_TYPE[r.type] || [{ label: 'Config', value: '—' }]
  const paramObj = params.reduce<Record<string, string>>((o, p) => {
    o[p.label.toLowerCase().replace(/[^a-z]/g, '_')] = p.value
    return o
  }, {})
  const json =
    '{\n  "key": "' +
    r.key +
    '",\n  "type": "' +
    r.type +
    '",\n  "field": "' +
    r.field +
    '",\n  "severity": "' +
    r.severity +
    '",\n  "scope": "' +
    r.scope +
    '",\n  "enabled": ' +
    r.enabled +
    ',\n  "params": ' +
    JSON.stringify(paramObj) +
    ',\n  "message": "' +
    r.message +
    '"\n}'
  return {
    key: r.key,
    type: r.type,
    field: r.field,
    message: r.message,
    params,
    json,
    enabledLabel: r.enabled ? 'ENABLED' : 'DISABLED',
    enabledColor: r.enabled ? 'var(--status-green-text)' : 'var(--status-red-text)',
  }
}

/* ---------- health sparkline builder (this.renderVals mkSpark/hc) ---------- */

export function mkSpark(pts: number[]): { spark: string; area: string } {
  const w = 220
  const h = 44
  const max = Math.max(...pts)
  const min = Math.min(...pts)
  const rng = max - min || 1
  const step = w / (pts.length - 1)
  const xy = pts.map((p, i): [number, number] => [i * step, h - 4 - ((p - min) / rng) * (h - 10)])
  const line = xy.map((p, i) => (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' ')
  const area = line + ' L' + w + ' ' + h + ' L0 ' + h + ' Z'
  return { spark: line, area }
}

export type HealthCard = {
  label: string
  value: string
  unit: string
  status: string
  dot: string
  stroke: string
  fill: string
  spark: string
  area: string
}

function healthCard(label: string, value: string, unit: string, status: string, dot: string, stroke: string, fill: string, pts: number[]): HealthCard {
  const sp = mkSpark(pts)
  return { label, value, unit, status, dot, stroke, fill, spark: sp.spark, area: sp.area }
}

export function buildHealthCards(dlCount: number): HealthCard[] {
  return [
    healthCard('Queue depth', '1,284', 'jobs', 'NORMAL', 'var(--status-green-text)', 'var(--accent)', 'var(--accent-tint)', [820, 900, 1100, 980, 1200, 1180, 1284]),
    healthCard('Worker throughput', '342', 'job/min', 'HEALTHY', 'var(--status-green-text)', 'var(--accent)', 'var(--accent-tint)', [300, 320, 290, 350, 330, 360, 342]),
    healthCard('APP latency p95', '1.8', 's', 'ELEVATED', 'var(--status-amber-text)', 'var(--status-amber-text)', 'var(--status-amber-bg)', [0.9, 1.0, 1.2, 1.4, 1.6, 1.7, 1.8]),
    healthCard('APP error rate', '2.4', '%', 'ELEVATED', 'var(--status-amber-text)', 'var(--status-amber-text)', 'var(--status-amber-bg)', [0.4, 0.6, 0.8, 1.5, 2.0, 2.2, 2.4]),
    healthCard('Dead-letter', String(dlCount), 'jobs', 'ATTENTION', 'var(--status-red-text)', 'var(--status-red-text)', 'var(--status-red-bg)', [0, 1, 1, 2, 2, 2, dlCount]),
    healthCard('Recon backlog', '4', 'open', 'NORMAL', 'var(--status-green-text)', 'var(--accent)', 'var(--accent-tint)', [9, 7, 6, 5, 4, 5, 4]),
  ]
}
