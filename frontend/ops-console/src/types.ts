// Typed re-authoring of the prototype's `this.state` shape (OpsConsole.dc.html,
// Component constructor, line ~703) plus the seed-data record shapes.

export type Screen = 'submissions' | 'rules' | 'audit' | 'tenants' | 'health'
export type Env = 'sandbox' | 'live'
export type SubTab = 'jobs' | 'recon'

export type JobState = 'queued' | 'submitting' | 'pending' | 'accepted' | 'rejected' | 'failed' | 'dead-letter'
export type JobFilter = 'all' | JobState

export type Severity = 'error' | 'warn' | 'info'
export type Scope = 'global' | 'tenant-override'

export type Job = {
  id: string
  tenant: string
  tin: string
  invoice: string
  state: JobState
  attempts: number
  lastError: string
  age: string
  app: string
}

export type Rule = {
  key: string
  type: string
  field: string
  severity: Severity
  scope: Scope
  enabled: boolean
  message: string
}

export type DrawerState = { type: 'job' | 'audit' | 'rule'; id: string } | null

export type ToastTone = 'ok' | 'red'
export type ToastState = { msg: string; tag: string; tone: ToastTone } | null
