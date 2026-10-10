import { ApiError, apiFetch, gatewayBase } from '@invoice-os/api-client'
import { loadConsoleSession } from '@invoice-os/console-session'
import { SESSION_KEY } from './auth'
import type { Rule, Severity } from './types'

// Wire shapes: internal/validation/staff_rules.go.
interface WireRule {
  key: string
  type: string
  target: string
  severity: 'error' | 'warning' | 'info'
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

const RULES_PATH = '/api/validation/v1/staff/rules'
export const REASON_MAX = 500

export const toRule = (w: WireRule): Rule => ({
  key: w.key,
  type: w.type,
  field: w.target,
  severity: (w.severity === 'warning' ? 'warn' : w.severity) as Severity,
  scope: 'global',
  enabled: w.enabled,
  message: w.message,
})

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

export const fetchRulesInForce = async (signal?: AbortSignal): Promise<RulesInForce> => {
  const w = await request<WireRules>(RULES_PATH, { signal })
  return { version: w.version, rules: w.rules.map(toRule) }
}

export const switchRule = (key: string, enabled: boolean, reason: string) =>
  request<{ key: string; enabled: boolean }>(`${RULES_PATH}/${encodeURIComponent(key)}`, { method: 'PATCH', body: { enabled, reason } })
