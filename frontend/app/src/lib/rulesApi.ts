// Rules screen client: GET /api/validation/v1/rules (internal/validation GoldenRules) and
// /api/invoice/v1/custom-rules (internal/customrule). Only CustomRule carries `enabled`.
import type { Mode } from '../types'
import { INVOICE_FIELDS } from './invoiceFields'
import type { AuthedFetch } from './portfolio'
import type { Severity } from './validationApi'

// Keys mirror the Go json tags (wireMirrors.test.ts).
export interface GoldenRule {
  key: string
  type: string
  target: string
  params: Record<string, unknown>
  severity: Severity
  when: string | null
  message: string
}

export interface GoldenVersion {
  rule_set_version_id: string
  version: number
  state: 'in_force' | 'scheduled' | 'superseded'
  effective_from: string
  rule_count: number
}

export interface GoldenRules {
  today: string
  versions: GoldenVersion[]
  rule_set_version_id: string
  version: number
  effective_from: string
  rules: GoldenRule[]
}

export interface CustomRule {
  id: string
  key: string
  revision: number
  revision_id: string
  entity_id: string | null
  type: string
  target: string
  params: Record<string, unknown>
  severity: Severity
  when: string | null
  message: string
  enabled: boolean
  updated_at: string
  updated_by: string
}

// customrule.RuleInput.
export interface CustomRuleInput {
  entity_id: string | null
  type: string
  target: string
  params: Record<string, unknown>
  severity: Severity
  when: string | null
  message: string
  enabled: boolean
}

export function getGoldenRules(authedFetch: AuthedFetch, base: string): Promise<GoldenRules> {
  return authedFetch<GoldenRules>(`${base}/api/validation/v1/rules`)
}

export async function listCustomRules(authedFetch: AuthedFetch, base: string): Promise<CustomRule[]> {
  const res = await authedFetch<{ custom_rules: CustomRule[] }>(`${base}/api/invoice/v1/custom-rules`)
  return res.custom_rules
}

export function createCustomRule(authedFetch: AuthedFetch, base: string, input: CustomRuleInput): Promise<CustomRule> {
  return authedFetch<CustomRule>(`${base}/api/invoice/v1/custom-rules`, { method: 'POST', body: input })
}

export function updateCustomRule(authedFetch: AuthedFetch, base: string, id: string, input: CustomRuleInput): Promise<CustomRule> {
  return authedFetch<CustomRule>(`${base}/api/invoice/v1/custom-rules/${id}`, { method: 'PUT', body: input })
}

export async function deleteCustomRule(authedFetch: AuthedFetch, base: string, id: string): Promise<void> {
  await authedFetch<void>(`${base}/api/invoice/v1/custom-rules/${id}`, { method: 'DELETE' })
}

// D4: firm-wide rules plus the active client's; a null entity keeps firm-wide only.
export function rulesInScope(rules: readonly CustomRule[], entityId: string | null): CustomRule[] {
  return rules.filter((r) => r.entity_id === null || r.entity_id === entityId)
}

// Violation key prefix of a custom rule: customrule.Key.
const CUSTOM_KEY_PREFIX = 'custom:'

export function ruleKeyLabel(key: string): string {
  return key.startsWith(CUSTOM_KEY_PREFIX) ? 'Custom rule' : key
}

export function scopeLabel(rule: Pick<CustomRule, 'entity_id'>, mode: Mode): 'FIRM' | 'CLIENT' | 'CUSTOM' {
  if (mode === 'inhouse') return 'CUSTOM'
  return rule.entity_id === null ? 'FIRM' : 'CLIENT'
}

export const ADMIN_ONLY_REASON = 'Only an admin can change custom rules.'

export function ruleJSON(rule: CustomRule | GoldenRule): string {
  return JSON.stringify(rule, null, 2)
}

// The full PUT body of an existing rule; a toggle spreads this and flips `enabled` (D9).
export function ruleInputOf(r: CustomRule): CustomRuleInput {
  return {
    entity_id: r.entity_id,
    type: r.type,
    target: r.target,
    params: r.params,
    severity: r.severity,
    when: r.when,
    message: r.message,
    enabled: r.enabled,
  }
}

// ---- guided form (D6) ----

export type GuidedCheck = 'required' | 'regex' | 'enum' | 'range'

type FieldType = (typeof INVOICE_FIELDS)[number]['type']

export const GUIDED_FIELDS = INVOICE_FIELDS.filter((f) => !f.line)

// RuleType constants of internal/validation/rule.go: TypeRequired, TypeFormat, TypeEnum, TypeRange.
const CHECK_TYPE: Record<GuidedCheck, string> = {
  required: 'required',
  regex: 'format/regex',
  enum: 'enum',
  range: 'range',
}

const CHECKS_BY_TYPE: Record<FieldType, readonly GuidedCheck[]> = {
  text: ['required', 'regex', 'enum'],
  code: ['required', 'enum'],
  date: ['required'],
  time: ['required'],
  money: ['required', 'range'],
  quantity: ['required', 'range'],
  percent: ['required', 'range'],
}

export function checksFor(type: FieldType): readonly GuidedCheck[] {
  return CHECKS_BY_TYPE[type]
}

// internal/invoice/payload.go party(): supplier_x and buyer_x nest as supplier.x and buyer.x.
export function payloadPath(key: string): string {
  return key.replace(/^(supplier|buyer)_/, '$1.')
}

export type GuidedSeverity = 'warning' | 'error'

// Free-text fields stay strings; the inputs bind to them. Unused fields of a check stay ''.
export interface GuidedState {
  field: string
  check: GuidedCheck
  pattern: string
  values: string
  min: string
  max: string
  message: string
  severity: GuidedSeverity
  entityId: string | null
}

export interface ExpressionState {
  target: string
  expr: string
  message: string
  severity: GuidedSeverity
  entityId: string | null
}

const MESSAGE_MAX_RUNES = 500 // internal/validation maxDraftMessageRunes

function splitValues(values: string): string[] {
  return values
    .split(',')
    .map((v) => v.trim())
    .filter((v) => v !== '')
}

function guidedParams(s: GuidedState): Record<string, unknown> {
  switch (s.check) {
    case 'required':
      return {}
    case 'regex':
      return { pattern: s.pattern }
    case 'enum':
      return { values: splitValues(s.values) }
    case 'range': {
      const p: Record<string, unknown> = {}
      if (s.min.trim() !== '') p.min = Number(s.min)
      if (s.max.trim() !== '') p.max = Number(s.max)
      return p
    }
  }
}

export function guidedRuleInput(s: GuidedState): CustomRuleInput {
  return {
    entity_id: s.entityId,
    type: CHECK_TYPE[s.check],
    target: payloadPath(s.field),
    params: guidedParams(s),
    severity: s.severity,
    when: null,
    message: s.message.trim(),
    enabled: true,
  }
}

export function expressionRuleInput(s: ExpressionState): CustomRuleInput {
  return {
    entity_id: s.entityId,
    type: 'cel',
    target: s.target,
    params: { expr: s.expr },
    severity: s.severity,
    when: null,
    message: s.message.trim(),
    enabled: true,
  }
}

function boundOf(p: Record<string, unknown>, k: 'min' | 'max'): string | null {
  if (!(k in p)) return ''
  return typeof p[k] === 'number' ? String(p[k]) : null
}

// The inverse of guidedRuleInput; null when the rule is not a shape the guided form writes.
export function guidedShapeOf(rule: Omit<CustomRuleInput, 'enabled'>): GuidedState | null {
  if (rule.when !== null || (rule.severity !== 'warning' && rule.severity !== 'error')) return null
  const check = (Object.keys(CHECK_TYPE) as GuidedCheck[]).find((c) => CHECK_TYPE[c] === rule.type)
  const field = GUIDED_FIELDS.find((f) => payloadPath(f.key) === rule.target)
  if (!check || !field || !checksFor(field.type).includes(check)) return null

  const s: GuidedState = {
    field: field.key,
    check,
    pattern: '',
    values: '',
    min: '',
    max: '',
    message: rule.message,
    severity: rule.severity,
    entityId: rule.entity_id,
  }
  const p = rule.params
  const keys = Object.keys(p)
  switch (check) {
    case 'required':
      return keys.length === 0 ? s : null
    case 'regex':
      if (keys.length !== 1 || typeof p.pattern !== 'string') return null
      return { ...s, pattern: p.pattern }
    case 'enum':
      if (keys.length !== 1 || !Array.isArray(p.values) || !p.values.every((v) => typeof v === 'string')) return null
      return { ...s, values: (p.values as string[]).join(', ') }
    case 'range': {
      const min = boundOf(p, 'min')
      const max = boundOf(p, 'max')
      if (min === null || max === null || keys.some((k) => k !== 'min' && k !== 'max')) return null
      return { ...s, min, max }
    }
  }
}

// The first reason the form cannot be saved, or null. The server accepts every one of these (P9).
export function formError(s: GuidedState | ExpressionState): string | null {
  const message = s.message.trim() // the server trims, then counts runes
  if (message === '') return 'Enter a message.'
  if ([...message].length > MESSAGE_MAX_RUNES) return `The message is limited to ${MESSAGE_MAX_RUNES} characters.`
  if ('expr' in s) return s.expr.trim() === '' ? 'Enter an expression.' : null
  switch (s.check) {
    case 'regex':
      return s.pattern === '' ? 'Enter a pattern.' : null
    case 'enum':
      return splitValues(s.values).length === 0 ? 'Enter at least one allowed value.' : null
    case 'range': {
      const bounds = [s.min, s.max].filter((b) => b.trim() !== '')
      if (bounds.length === 0) return 'Enter a minimum, a maximum or both.'
      if (bounds.some((b) => !Number.isFinite(Number(b)))) return 'Bounds must be numbers.'
      if (bounds.length === 2 && Number(s.min) > Number(s.max)) return 'The minimum cannot be greater than the maximum.'
      return null
    }
    default:
      return null
  }
}
