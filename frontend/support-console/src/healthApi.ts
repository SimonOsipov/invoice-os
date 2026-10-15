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
// systemhealth.TrendPoint
export interface TrendPoint {
  taken_at: string
  services_down: number | null
  queue_depth: number
  dead_lettered: number
}
// systemhealth.Snapshot
export interface StaffHealth {
  checked_at: string
  fleet: FleetHealth
  jobs: Record<string, number>
  queue_depth: number
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

export const shortBuild = (build?: string): string => (build ? build.slice(0, 7) : '—')
