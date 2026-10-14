import { request } from './api'
import { dayEnd, dayStart, type AuditPageInfo } from './auditApi'

const AI_CALLS_PATH = '/api/invoice/v1/staff/ai-calls'
const PAGE_SIZE = 50

// Wire shapes: internal/aicalls/staff_read.go.
// Call
export interface AiCall {
  id: string
  tenant_id: string
  tenant_name: string
  created_at: string
  client: string
  purpose: string
  /** Empty when Jev got no answer. */
  model: string
  input_tokens: number
  output_tokens: number
  /** Decimal string as stored; never parsed. */
  cost: string
  latency_ms: number
  attempts: number
  outcome: string
}
// Page
export interface AiCallsPage {
  calls: AiCall[]
  page: AuditPageInfo
}

// Total
export interface AiTotal {
  day: string
  tenant_id: string
  tenant_name: string
  calls: number
  input_tokens: number
  output_tokens: number
  cost: string
  failed: number
}

/** Each value is empty or set; `from` and `to` are UTC days (`YYYY-MM-DD`). */
export interface AiFilters {
  tenant?: string
  purpose?: string
  outcome?: string
  from?: string
  to?: string
}

export const fetchAiCalls = (filters: AiFilters, failed: boolean, cursor?: string, signal?: AbortSignal): Promise<AiCallsPage> => {
  const q = new URLSearchParams({ limit: String(PAGE_SIZE) })
  if (filters.tenant) q.set('tenant', filters.tenant)
  if (filters.purpose) q.set('purpose', filters.purpose)
  if (filters.outcome) q.set('outcome', filters.outcome)
  if (failed) q.set('failed', 'true')
  if (filters.from) q.set('from', dayStart(filters.from))
  if (filters.to) q.set('to', dayEnd(filters.to))
  if (cursor) q.set('cursor', cursor)
  return request<AiCallsPage>(`${AI_CALLS_PATH}?${q}`, { signal })
}

export const formatCount = (n: number): string => n.toLocaleString('en-US')
export const formatCost = (s: string): string => `$${s}`
export const formatLatency = (ms: number): string => `${formatCount(ms)} ms`
// Mirrors the server: a failed call is any outcome other than `ok`.
export const isFailed = (outcome: string): boolean => outcome !== 'ok'
