// Typed re-authoring of the prototype's `this.state` shape (Developer
// Console.dc.html, Component constructor, line ~744) plus the seed-data record
// shapes.

export type Screen = 'overview' | 'submissions' | 'evidence' | 'api' | 'billing' | 'status'
export type Env = 'sandbox' | 'live'
export type Range = '7d' | '30d' | '90d'

export type JobState = 'queued' | 'submitting' | 'pending' | 'accepted' | 'rejected' | 'failed' | 'dead-letter'
export type JobFilter = 'all' | JobState

// The client-facing submission record (proto:785-794). `tenant`/`tin`/`app` were
// operator concepts and are gone; `buyer`/`btin`/`raw`/`desc`/`latency` replace them.
// The type name stays `Job` — the prototype likewise keeps `jobs`/`jobRows`/`Job ID`.
export type Job = {
  id: string
  buyer: string
  btin: string
  invoice: string
  raw: number
  desc: string
  state: JobState
  attempts: number
  lastError: string
  age: string
  latency: string
}

export type DrawerState = { type: 'job' | 'evidence'; id: string } | null

export type ToastTone = 'ok' | 'red'
export type ToastState = { msg: string; tag: string; tone: ToastTone } | null
