// Typed re-authoring of the prototype's `this.state` shape (Developer
// Console.dc.html, Component constructor, line ~744) plus the seed-data record
// shapes.

export type Screen = 'overview' | 'submissions' | 'evidence' | 'api' | 'billing' | 'status'
export type Env = 'sandbox' | 'live'
export type Range = '7d' | '30d' | '90d'

export type JobState = 'queued' | 'submitting' | 'pending' | 'accepted' | 'rejected' | 'failed' | 'dead-letter'
export type JobFilter = 'all' | JobState

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

export type DrawerState = { type: 'job' | 'evidence'; id: string } | null

export type ToastTone = 'ok' | 'red'
export type ToastState = { msg: string; tag: string; tone: ToastTone } | null
