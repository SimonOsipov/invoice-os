import { request } from './api'

const JOBS_PATH = '/api/submission/v1/staff/jobs'

// Wire shape: internal/staffjobs/store.go.
export interface StaffJob {
  id: string
  tenant_id: string
  tenant_name: string
  invoice_id: string
  invoice_number: string
  invoice_status: string
  state: string
  attempts: number
  max_attempts: number
  last_error: string | null
  acknowledged: boolean
  created_at: string
  updated_at: string
}

export interface JobStep {
  from_status: string | null
  to_status: string
  actor: string
  changed_at: string
}

export interface JobDetail {
  job: StaffJob
  history: JobStep[]
}

// jobStates in internal/staffjobs/handlers.go (the submission_jobs_state_check set).
export const JOB_STATES = ['queued', 'submitting', 'pending', 'accepted', 'rejected', 'failed', 'dead_lettered', 'cancelled'] as const
export type JobState = (typeof JOB_STATES)[number]

export const fetchJobs = async (state?: JobState, tenant?: string, signal?: AbortSignal): Promise<StaffJob[]> => {
  const q = new URLSearchParams()
  if (state) q.set('state', state)
  if (tenant) q.set('tenant', tenant)
  const qs = q.toString()
  return (await request<{ jobs: StaffJob[] }>(qs ? `${JOBS_PATH}?${qs}` : JOBS_PATH, { signal })).jobs
}

const jobPath = (id: string): string => `${JOBS_PATH}/${encodeURIComponent(id)}`

export const fetchJob = (id: string, signal?: AbortSignal): Promise<JobDetail> => request<JobDetail>(jobPath(id), { signal })

export const redriveJob = (id: string, reason: string): Promise<JobDetail> => request<JobDetail>(`${jobPath(id)}/redrive`, { method: 'POST', body: { reason } })

export const cancelJob = (id: string, reason: string): Promise<JobDetail> => request<JobDetail>(`${jobPath(id)}/cancel`, { method: 'POST', body: { reason } })

export interface JobStyle {
  bg: string
  border: string
  text: string
  label: string
}

const MUTED = { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' }
const GREEN = { bg: 'var(--status-green-bg)', border: 'var(--status-green-border)', text: 'var(--status-green-text)' }
const AMBER = { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)' }
const RED = { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)' }
const tone = (t: Omit<JobStyle, 'label'>, label: string): JobStyle => ({ ...t, label })

const STATE_STYLE: Record<JobState, JobStyle> = {
  queued: tone(MUTED, 'QUEUED'),
  submitting: tone(GREEN, 'SUBMITTING'),
  pending: tone(AMBER, 'PENDING'),
  accepted: tone(GREEN, 'ACCEPTED'),
  rejected: tone(RED, 'REJECTED'),
  failed: tone(RED, 'FAILED'),
  dead_lettered: tone(RED, 'DEAD-LETTER'),
  cancelled: tone(MUTED, 'CANCELLED'),
}

// An unknown state reads muted, upper-cased.
export const jobStateStyle = (state: string): JobStyle =>
  (STATE_STYLE as Record<string, JobStyle | undefined>)[state] ?? tone(MUTED, state.toUpperCase())

export const jobAge = (createdAt: string, now: number = Date.now()): string => {
  const s = Math.max(0, Math.floor((now - Date.parse(createdAt)) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}
