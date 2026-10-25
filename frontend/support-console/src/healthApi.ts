import { request } from './api'

const HEALTH_PATH = '/api/submission/v1/staff/health'

// Wire shapes: internal/systemhealth/handler.go, internal/gateway/fleet.go.
// gateway.ServiceHealth
export interface ServiceHealth {
  name: string
  status: 'up' | 'down' // statusUp | statusDown
  error?: string
  build?: string
  sentry?: string
  contacts?: string
  ready?: boolean
  not_ready?: string[]
}
// gateway.FleetHealth; 'unknown' is systemhealth.FleetUnknown
export interface FleetHealth {
  status: 'ok' | 'degraded' | 'unknown' // fleetOK | fleetDegraded | FleetUnknown
  services: ServiceHealth[]
}
// systemhealth.JobTotals, systemhealth.JobView
export interface JobTotals {
  queue_depth: number
  dead_lettered: number
}
export interface JobView {
  total: JobTotals
  services: Record<string, JobTotals>
}
// systemhealth.TrendPoint; river_jobs is null for a sample with no kind counts
export interface TrendPoint {
  taken_at: string
  services_down: number | null
  queue_depth: number
  dead_lettered: number
  river_jobs: JobView | null
}
// systemhealth.Snapshot
export interface StaffHealth {
  checked_at: string
  fleet: FleetHealth
  jobs: Record<string, number>
  queue_depth: number
  river_jobs?: JobView // absent from a submission older than the support-console
  trend: TrendPoint[]
}

export const fetchStaffHealth = (signal?: AbortSignal): Promise<StaffHealth> => request<StaffHealth>(HEALTH_PATH, { signal })

export const fleetSummary = (fleet: FleetHealth): { up: number; total: number; down: string[] } => {
  if (fleet.status === 'unknown') return { up: 0, total: 0, down: [] }
  const down = fleet.services.filter((s) => s.status !== 'up').map((s) => s.name)
  return { up: fleet.services.length - down.length, total: fleet.services.length, down }
}

// Values of one signal in served (oldest first) order; null points are dropped.
export const series = (trend: TrendPoint[], key: 'services_down' | 'queue_depth' | 'dead_lettered'): number[] =>
  trend.flatMap((p) => (p[key] === null ? [] : [p[key]]))

// All services reads the total; a service that owns no job kind reads null.
export const jobsOf = (v: JobView, service: string): JobTotals | null => (service === '' ? v.total : (v.services[service] ?? null))

// One pick's values, oldest first; a sample without kind counts adds no point.
export const jobSeries = (trend: TrendPoint[], service: string, key: keyof JobTotals): number[] =>
  trend.flatMap((p) => {
    const t = p.river_jobs ? jobsOf(p.river_jobs, service) : null
    return t ? [t[key]] : []
  })

export const shortBuild = (build?: string): string => (build ? build.slice(0, 7) : '—')
