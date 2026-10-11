import { ApiError, apiFetch, gatewayBase } from '@invoice-os/api-client'
import { loadConsoleSession } from '@invoice-os/console-session'
import { SESSION_KEY } from './auth'
import type { Rule, Severity } from './types'

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
  scope: 'global',
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

const request = <T>(path: string, opts: { method?: string; body?: unknown; signal?: AbortSignal } = {}): Promise<T> => {
  const base = gatewayBase()
  if (!base) return Promise.reject(new ApiError('network', 'Gateway URL is not configured'))
  const token = loadConsoleSession(SESSION_KEY)?.token
  if (!token) return Promise.reject(new ApiError('http', 'Not signed in', 401))
  return apiFetch<T>(base + path, { ...opts, token })
}

export const fetchVersions = (signal?: AbortSignal): Promise<VersionList> => request<VersionList>(VERSIONS_PATH, { signal })

export const fetchRules = async (version?: number, signal?: AbortSignal): Promise<RulesInForce> => {
  const w = await request<WireRules>(version === undefined ? RULES_PATH : `${RULES_PATH}?version=${version}`, { signal })
  return { version: w.version, rules: w.rules.map(toRule) }
}

export const fetchRulesInForce = (signal?: AbortSignal): Promise<RulesInForce> => fetchRules(undefined, signal)

export const switchRule = (key: string, enabled: boolean, reason: string) =>
  request<{ key: string; enabled: boolean }>(`${RULES_PATH}/${encodeURIComponent(key)}`, { method: 'PATCH', body: { enabled, reason } })
