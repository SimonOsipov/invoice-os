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

/* ------------------------------------------------------------------ */
/* Usage & billing (proto:1029-1041)                                   */
/* ------------------------------------------------------------------ */

// proto:1029-1034. `amount` is a plain string, never `number | string`: the Evidence
// exports row renders the word `included`, and every other row is already a formatted
// ₦ string by the time it lands here (the arithmetic happens in data.tsx via
// `computeBillLine`, so the component stays pure layout).
export type BillItem = {
  label: string
  detail: string
  qty: string
  amount: string
  color: string
}

export type InvoiceKind = 'paid' | 'open'

// proto:1036-1040. `amount` is deliberately inconsistent across rows: the OPEN row is
// compact (`₦5.08M`, from spendTotals().proj) while the three PAID rows are full-digit
// literals. That is the prototype's own formatting, not drift — do not unify.
export type PastInvoice = {
  id: string
  period: string
  amount: string
  kind: InvoiceKind
}

// proto:1035's `invSt(kind)`, re-authored as a two-entry lookup map to match the
// METHOD_BG/METHOD_FG precedent in data.tsx rather than as a function.
export type InvoiceStatus = {
  bg: string
  border: string
  text: string
  label: string
}

// proto:468-487. The quota meter's display literals. `included` is NOT here — it is
// `SCALE_PLAN.includedRequests`, the same value `computeQuota` is called with, so the
// allowance has exactly one source. The two bar widths are literals too: the track
// (proto:474) is a flex row of two segments summing to 100%, so there is no single
// "fill" and `computeQuota().widthPct` is the wrong input. 40000/48214 = 82.96% and
// 8214/48214 = 17.04% round to exactly these figures — consistent, not drift.
export type Quota = {
  used: number
  includedWidth: string
  overWidth: string
  clearedInvoices: number
  evidenceExports: number
}
