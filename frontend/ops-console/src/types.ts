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

/* ------------------------------------------------------------------ */
/* API & webhooks (proto:992-1026)                                     */
/* ------------------------------------------------------------------ */

// proto:992-995. `full` and `mask` are BOTH literal seed strings — the mask is
// 'fb_live_sk_' + 20 x U+00B7 MIDDLE DOT + the last four chars, built by the
// prototype at seed time, not derived from `full` at render.
export type ApiKey = {
  id: string
  tag: string
  name: string
  full: string
  mask: string
  tagBg: string
  tagBorder: string
  tagText: string
  created: string
  lastUsed: string
  borderColor: string
}

// proto:1002-1005. The prototype wraps each event in a `{ name }` object purely so its
// template engine can iterate it; flattened to `string[]` here.
export type Webhook = {
  url: string
  env: string
  envBg: string
  envBorder: string
  envText: string
  events: string[]
}

// proto:1007-1012 / 1016-1022. `id` has no prototype counterpart: it exists only because
// the natural keys collide (`invoice.cleared` x3, `POST /v2/invoices` x4) and React needs
// a stable, index-free key per row or it logs a duplicate-key console.error.
export type Delivery = { id: string; event: string; code: number; latency: string; retry: string }
export type ApiRequest = { id: string; m: string; ep: string; code: number; lat: string }

// proto:1024-1026. `width` is a literal percentage string per env, NOT current/limit:
// live is 341/500 = 68.2%, and the prototype pins the bar at 68%. Computing it would
// silently drift from the design.
export type RateLimit = { current: string; limit: string; width: string; color: string; detail: string }
