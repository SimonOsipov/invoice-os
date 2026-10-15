import { request } from './api'
import type { CodeListLatest, CodeListSync, Rule, Severity } from './types'

export interface DiffRow {
  sign: '+' | '~' | '−'
  key: string
  detail: string
  tag: 'ADDED' | 'CHANGED' | 'REMOVED'
  kind: 'added' | 'changed' | 'removed'
}

// Wire shapes: internal/validation/staff_rules.go (rules), staff_versions.go (versions).
interface WireRule {
  key: string
  type: string
  target: string
  params: Record<string, unknown>
  severity: 'error' | 'warning' | 'info'
  when: string | null
  scope: string
  message: string
  enabled: boolean
}
interface WireRules {
  rule_set_version_id: string
  version: number
  rules: WireRule[]
}

export interface RulesInForce {
  version: number
  rules: Rule[]
}

// The state CASE in the Versions query, internal/validation/staff_versions.go; no Go constant names them.
export type VersionState = 'draft' | 'in_force' | 'scheduled' | 'superseded' | 'retired'
export interface RuleVersion {
  rule_set_version_id: string
  version: number
  state: VersionState
  effective_from: string | null
  opened_at: string | null
  rule_count: number
}
export interface VersionList {
  today: string
  versions: RuleVersion[]
}

const RULES_PATH = '/api/validation/v1/staff/rules'
const VERSIONS_PATH = '/api/validation/v1/staff/rule-versions'
export const REASON_MAX = 500

export const toRule = (w: WireRule): Rule => ({
  key: w.key,
  type: w.type,
  field: w.target,
  severity: (w.severity === 'warning' ? 'warn' : w.severity) as Severity,
  scope: w.scope,
  enabled: w.enabled,
  message: w.message,
  params: w.params,
  when: w.when,
})

const STATE_LABELS: Record<VersionState, string> = {
  draft: 'DRAFT',
  in_force: 'IN FORCE',
  scheduled: 'SCHEDULED',
  superseded: 'SUPERSEDED',
  retired: 'RETIRED',
}
export const stateLabel = (state: VersionState): string => STATE_LABELS[state]

export const versionMeta = (v: Pick<RuleVersion, 'effective_from' | 'rule_count'> & { state?: VersionState }): string =>
  `${v.effective_from ? `eff. ${v.effective_from}` : v.state === 'retired' ? 'never in force' : 'editing'} · ${v.rule_count} rules`

// The server trims, then counts runes; Array.from counts code points.
export const reasonValid = (reason: string): boolean => {
  const n = Array.from(reason.trim()).length
  return n >= 1 && n <= REASON_MAX
}

export const fetchVersions = (signal?: AbortSignal): Promise<VersionList> => request<VersionList>(VERSIONS_PATH, { signal })

export const fetchRules = async (version?: number, signal?: AbortSignal): Promise<RulesInForce> => {
  const w = await request<WireRules>(version === undefined ? RULES_PATH : `${RULES_PATH}?version=${version}`, { signal })
  return { version: w.version, rules: w.rules.map(toRule) }
}

export const fetchRulesInForce = (signal?: AbortSignal): Promise<RulesInForce> => fetchRules(undefined, signal)

export const switchRule = (key: string, enabled: boolean, reason: string) =>
  request<{ key: string; enabled: boolean }>(`${RULES_PATH}/${encodeURIComponent(key)}`, { method: 'PATCH', body: { enabled, reason } })

// Draft routes: internal/validation/staff_drafts.go. Keys are encoded; the gateway's exact staff route refuses `%`, which no key holds.
const DRAFT_PATH = `${VERSIONS_PATH}/draft`
const draftRulePath = (key: string): string => `${DRAFT_PATH}/rules/${encodeURIComponent(key)}`

export type WireSeverity = 'error' | 'warning' | 'info'
export interface DraftRuleBody {
  type: string
  target: string
  params: Record<string, unknown>
  severity: WireSeverity
  when: string | null
  message: string
  enabled: boolean
}
export type NewDraftRuleBody = DraftRuleBody & { key: string }

export interface DraftWrite {
  key: string
  rule_set_version: number
  rule_set_version_id: string
}
export interface Violation {
  rule_key: string
  severity: WireSeverity
  message: string
  path?: string
}
export interface DraftTestResult {
  draft: { rule_set_version: number; violations: Violation[]; error: string | null }
  in_force: { rule_set_version: number; violations: Violation[] }
}
export interface Published {
  version: number
  effective_from: string
  rule_count: number
}

export const openDraft = () => request<{ version: number; from_version: number }>(DRAFT_PATH, { method: 'POST' })
export const addDraftRule = (body: NewDraftRuleBody) => request<DraftWrite>(`${DRAFT_PATH}/rules`, { method: 'POST', body })
export const editDraftRule = (key: string, body: DraftRuleBody) => request<DraftWrite & { changed: boolean }>(draftRulePath(key), { method: 'PUT', body })
export const removeDraftRule = (key: string) => request<DraftWrite>(draftRulePath(key), { method: 'DELETE' })
export const testDraft = (invoice: unknown) => request<DraftTestResult>(`${DRAFT_PATH}/test`, { method: 'POST', body: { invoice } })
export const publishDraft = (effectiveFrom: string) => request<Published>(`${DRAFT_PATH}/publish`, { method: 'POST', body: { effective_from: effectiveFrom } })

// Rule types: the RuleType constants in internal/validation/rule.go.
export const RULE_TYPES = ['required', 'format/regex', 'enum', 'range', 'tax_math', 'cross_field', 'conditional', 'date', 'cel', 'line_sum']
export const SEVERITIES: WireSeverity[] = ['error', 'warning', 'info']

// The drawer form: params is the JSON text, when is blank for no guard.
export interface RuleForm {
  key: string
  type: string
  target: string
  severity: WireSeverity
  params: string
  when: string
  message: string
  enabled: boolean
}
export const EMPTY_FORM: RuleForm = { key: '', type: 'required', target: '', severity: 'error', params: '{}', when: '', message: '', enabled: true }

export const formFromRule = (r: Rule): RuleForm => ({
  key: r.key,
  type: r.type,
  target: r.field,
  severity: r.severity === 'warn' ? 'warning' : r.severity,
  params: JSON.stringify(r.params, null, 2),
  when: r.when ?? '',
  message: r.message,
  enabled: r.enabled,
})

// Call only on a form draftRuleErrors passed: params parses to an object.
export const ruleBody = (f: RuleForm): DraftRuleBody => ({
  type: f.type,
  target: f.target,
  params: f.params.trim() === '' ? {} : JSON.parse(f.params),
  severity: f.severity,
  when: f.when.trim() === '' ? null : f.when,
  message: f.message.trim(),
  enabled: f.enabled,
})

const KEY_RE = /^[a-z0-9]+(-[a-z0-9]+)*$/
const count = (s: string): number => Array.from(s).length

// The server checks that need no CEL (validateDraftRule in staff_drafts.go). Edit mode passes no draftKeys.
export const draftRuleErrors = (f: RuleForm, draftKeys: string[]): string[] => {
  const errs: string[] = []
  if (f.key === '') errs.push('Key is required.')
  else if (count(f.key) > 64) errs.push('Key is at most 64 characters.')
  else if (!KEY_RE.test(f.key)) errs.push('Key is lowercase letters and digits joined by single hyphens.')
  else if (draftKeys.includes(f.key)) errs.push('Key already in the draft.')
  if (f.type.trim() === '') errs.push('Type is required.')
  if (!SEVERITIES.includes(f.severity)) errs.push('Severity is required.')
  if (count(f.target) > 200) errs.push('Target is at most 200 characters.')
  if (f.params.trim() !== '') {
    try {
      const p: unknown = JSON.parse(f.params)
      if (p === null || typeof p !== 'object' || Array.isArray(p)) errs.push('Params must be a JSON object.')
    } catch {
      errs.push('Params is not valid JSON.')
    }
  }
  if (count(f.when) > 2000) errs.push('When is at most 2000 characters.')
  const n = count(f.message.trim())
  if (n < 1) errs.push('Message is required.')
  else if (n > REASON_MAX) errs.push('Message is at most 500 characters.')
  return errs
}

const ruleLine = (r: Rule): string => [r.type, r.severity, r.field].filter(Boolean).join(' · ')
const FIELDS: [string, (r: Rule) => string][] = [
  ['type', (r) => r.type],
  ['target', (r) => r.field],
  ['params', (r) => JSON.stringify(r.params)],
  ['severity', (r) => r.severity],
  ['when', (r) => r.when ?? ''],
  ['message', (r) => r.message],
  ['enabled', (r) => String(r.enabled)],
]

// What publishing changes: the draft against the version in force, by key.
export const diffRules = (draft: Rule[], inForce: Rule[]): DiffRow[] => {
  const old = new Map(inForce.map((r) => [r.key, r]))
  const mine = new Map(draft.map((r) => [r.key, r]))
  const rows: DiffRow[] = []
  for (const r of draft) {
    const o = old.get(r.key)
    if (!o) rows.push({ sign: '+', key: r.key, detail: ruleLine(r), tag: 'ADDED', kind: 'added' })
    else {
      const changed = FIELDS.filter(([, get]) => get(r) !== get(o)).map(([name]) => name)
      if (changed.length) rows.push({ sign: '~', key: r.key, detail: `${changed.join(', ')} changed`, tag: 'CHANGED', kind: 'changed' })
    }
  }
  for (const r of inForce) if (!mine.has(r.key)) rows.push({ sign: '−', key: r.key, detail: ruleLine(r), tag: 'REMOVED', kind: 'removed' })
  return rows.sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0))
}

// D30: a draft cannot be rebased, so publish says what it does not carry.
export const publishWarnings = (versions: RuleVersion[], draftRules: Rule[] = [], inForceRules: Rule[] = []): string[] => {
  const draft = versions.find((v) => v.state === 'draft')
  if (!draft) return []
  const out: string[] = []
  const opened = draft.opened_at?.slice(0, 10)
  const inForce = versions.find((v) => v.state === 'in_force')
  if (inForce?.effective_from && opened && inForce.effective_from > opened) {
    out.push(`v${inForce.version} took effect after this draft was opened. The diff shows what publishing changes.`)
  }
  if (inForce) {
    const off = new Set(inForceRules.filter((r) => !r.enabled).map((r) => r.key))
    for (const r of draftRules) {
      if (r.enabled && off.has(r.key)) out.push(`${r.key} is switched off in v${inForce.version} but enabled in this draft. Publishing switches it back on.`)
    }
  }
  for (const v of versions) {
    if (v.state === 'scheduled' && v.effective_from) {
      out.push(
        `v${v.version} is scheduled from ${v.effective_from}. This draft does not carry its rules. A start date before ${v.effective_from} is replaced by v${v.version} on that date. A start date on or after ${v.effective_from} replaces v${v.version}.`,
      )
    }
  }
  return out
}

// internal/validation/sample_invoice.json, the object under its "invoice" key (rulesApi.test.ts pins the match).
export const SAMPLE_INVOICE = JSON.stringify(
  {
    invoice_number: 'INV-2026-000123',
    issue_date: '2026-07-11',
    currency: 'NGN',
    supplier: { tin: '12345678-0001', name: 'Acme Nigeria Ltd' },
    buyer: { tin: '87654321-0002', name: 'Buyer Ltd' },
    subtotal: 1000.0,
    vat: 75.0,
    total: 1075.0,
    line_items: [{ id: '1', description: 'Widget', quantity: 10.0, unit_price: 100.0, line_total: 1000.0 }],
  },
  null,
  2,
)

const SYNCS_PATH = '/api/validation/v1/staff/code-list-syncs'
export const fetchCodeListSummary = async (signal?: AbortSignal): Promise<CodeListLatest[]> => (await request<{ lists: CodeListLatest[] }>(SYNCS_PATH, { signal })).lists
export const fetchCodeListSyncs = async (list: string, signal?: AbortSignal): Promise<CodeListSync[]> =>
  (await request<{ list: string; syncs: CodeListSync[] }>(`${SYNCS_PATH}?list=${encodeURIComponent(list)}`, { signal })).syncs

export const CODES_SHOWN = 200
export const capCodes = (codes: string[], max: number, total = codes.length): { shown: string[]; more: number } => ({ shown: codes.slice(0, max), more: Math.max(0, total - max) })

// The date of a sync for the panel row; the time of day for the drawer block.
export const syncDate = (iso: string): string => iso.slice(0, 10)
export const syncStamp = (iso: string): string => `${iso.slice(0, 10)} ${iso.slice(11, 16)}${iso.endsWith('Z') ? ' UTC' : ''}`
