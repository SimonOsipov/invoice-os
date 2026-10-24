import { request } from './api'

const METRICS_PATH = '/api/submission/v1/staff/metrics'

// Wire shape: internal/systemhealth/metrics.go.
export type RangeKey = '15m' | '1h' | '6h' | '24h' | '7d'
export type PanelKey =
  | 'req_per_min'
  | 'error_pct'
  | 'latency_p50_ms'
  | 'latency_p95_ms'
  | 'cpu_pct'
  | 'ram_pct'
  | 'instances'
  | 'cold_starts_per_min'
  | 'sql_cpu_pct'
  | 'sql_ram_pct'
  | 'sql_disk_pct'
  | 'sql_connections'
export type Point = [number, number] // [unix seconds, value]
export interface MetricSeries {
  name: string
  points: Point[]
}
export interface MetricPanel {
  key: PanelKey
  unit: string
  available: boolean
  series: MetricSeries[]
}
export interface StaffMetrics {
  range: RangeKey
  service: string
  start: string
  end: string
  step_seconds: number
  connection_budget: number
  panels: MetricPanel[]
  limits: { available: boolean; services: { name: string; cpu: string; memory: string; max_instances: number }[] }
  deploys: { available: boolean; revisions: { service: string; revision: string; created_at: string }[] }
}

export const RANGES: readonly RangeKey[] = ['15m', '1h', '6h', '24h', '7d']
export const DEFAULT_RANGE: RangeKey = '1h'
export const HEALTH_REFRESH_MS = 60_000

// ceiling: mirrors systemhealth.MetricServices; the route answers 400 for any other name
export const METRIC_SERVICES = [
  'app',
  'auth',
  'blog',
  'dashboard',
  'docling',
  'gateway',
  'invoice',
  'landing',
  'library',
  'notifications',
  'ops-console',
  'portfolio',
  'submission',
  'support-console',
  'tenancy',
  'validation',
  'worker',
] as const

// Terraform output dashboard_urls in invoice-os-infra.
export const DASHBOARDS = {
  services: 'https://console.cloud.google.com/monitoring/dashboards/builder/27155b37-f324-478d-9e9d-c91ede1837f9?project=ascomply-prod',
  database_edge: 'https://console.cloud.google.com/monitoring/dashboards/builder/3c070f78-6025-432a-b178-5cde3fe08514?project=ascomply-prod',
} as const

// HEALTH-00 Q7 defaults; strict ">".
export const THRESHOLDS = { error: 1, p95: 1000 } as const
export const CONNECTION_SHARE = 0.8

// auth is GoTrue and has no Sentry DSN.
export const NO_SENTRY: readonly string[] = ['auth']

export const fetchStaffMetrics = (range: RangeKey, service: string, signal?: AbortSignal): Promise<StaffMetrics> => {
  const q = new URLSearchParams({ range })
  if (service) q.set('service', service)
  return request<StaffMetrics>(`${METRICS_PATH}?${q.toString()}`, { signal })
}

export const panelOf = (m: StaffMetrics, key: PanelKey): MetricPanel | undefined => m.panels.find((p) => p.key === key)

export const hasData = (p: MetricPanel | undefined): p is MetricPanel => !!p && p.available && p.series.length > 0

export interface FleetSeries {
  requests: Point[]
  error: Point[]
  p95: Point[]
  connections: Point[]
}

const byTime = (a: Point, b: Point): number => a[0] - b[0]

const stepMap = (s: MetricSeries): Map<number, number> => new Map(s.points)

export function fleetSeries(m: StaffMetrics): FleetSeries {
  const req = panelOf(m, 'req_per_min')
  const err = panelOf(m, 'error_pct')
  const lat = panelOf(m, 'latency_p95_ms')
  const conns = panelOf(m, 'sql_connections')

  const requests = new Map<number, number>()
  const reqBy = new Map<string, Map<number, number>>()
  if (hasData(req)) {
    for (const s of req.series) {
      const at = stepMap(s)
      reqBy.set(s.name, at)
      for (const [t, v] of at) requests.set(t, (requests.get(t) ?? 0) + v)
    }
  }

  const weighted = new Map<number, [number, number]>() // [Σ error × requests, Σ requests]
  if (hasData(err)) {
    for (const s of err.series) {
      const rq = reqBy.get(s.name)
      if (!rq) continue
      for (const [t, e] of s.points) {
        const r = rq.get(t)
        if (r === undefined) continue
        const [num, den] = weighted.get(t) ?? [0, 0]
        weighted.set(t, [num + e * r, den + r])
      }
    }
  }

  const worst = new Map<number, number>()
  if (hasData(lat)) {
    for (const s of lat.series) for (const [t, v] of s.points) worst.set(t, Math.max(worst.get(t) ?? -Infinity, v))
  }

  return {
    requests: [...requests].sort(byTime),
    error: [...weighted].map(([t, [num, den]]): Point => [t, den === 0 ? 0 : num / den]).sort(byTime),
    p95: [...worst].sort(byTime),
    connections: hasData(conns) ? (conns.series.find((s) => s.name === 'database') ?? conns.series[0]).points.slice().sort(byTime) : [],
  }
}

export const latestOf = (points: Point[]): number | null => {
  let last: Point | null = null
  for (const p of points) if (!last || p[0] > last[0]) last = p
  return last ? last[1] : null
}

export const peakOf = (points: Point[]): number | null => (points.length ? Math.max(...points.map((p) => p[1])) : null)

export type Stat = 'error' | 'p95' | 'connections'
export type Verdict = 'high' | 'peaked' | 'ok'

// latest and peak are the displayed numbers (formatValue(...).n), so the word agrees with the printed value.
export function judge(stat: Stat, latest: number, peak: number, budget: number): Verdict {
  const limit = stat === 'connections' ? CONNECTION_SHARE * budget : THRESHOLDS[stat]
  if (latest > limit) return 'high'
  return peak > limit ? 'peaked' : 'ok'
}

const SLOTS = 7

// A service keeps one colour on every panel: the 7 busiest by peak req/min, slots in name order.
export function colourMap(m: StaffMetrics): Record<string, string> {
  const req = panelOf(m, 'req_per_min')
  let names: string[]
  if (hasData(req)) {
    names = req.series
      .map((s) => ({ name: s.name, peak: peakOf(s.points) ?? -Infinity }))
      .sort((a, b) => b.peak - a.peak || (a.name < b.name ? -1 : 1))
      .slice(0, SLOTS)
      .map((s) => s.name)
  } else {
    const first = m.panels.find(hasData)
    names = first ? first.series.map((s) => s.name) : []
  }
  names = [...new Set(names)].sort().slice(0, SLOTS)
  return Object.fromEntries(names.map((n, i) => [n, `var(--series-${i + 1})`]))
}

export interface Formatted {
  value: string
  unit: string
  n: number
}

export function formatValue(unit: string, v: number): Formatted {
  if (unit === 'ms') {
    const r = Math.round(v)
    if (r < 1000) return { value: String(r), unit: 'ms', n: r }
    const s = (v / 1000).toFixed(2)
    return { value: s, unit: 's', n: Math.round(Number(s) * 1000) }
  }
  if (unit === 'req/min' || unit === '/min') {
    if (v < 10) return { value: v.toFixed(1), unit, n: Number(v.toFixed(1)) }
    if (v < 1000) return { value: String(Math.round(v)), unit, n: Math.round(v) }
    const k = (v / 1000).toFixed(1)
    return { value: `${k}k`, unit, n: Math.round(Number(k) * 1000) }
  }
  if (unit === '%' || unit === '% of limit') {
    const s = v < 10 ? v.toFixed(2) : v.toFixed(1)
    return { value: s, unit, n: Number(s) }
  }
  const r = Math.round(v)
  return { value: String(r), unit: unit === 'count' ? '' : unit, n: r }
}
