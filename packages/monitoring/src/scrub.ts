import type { Breadcrumb, ErrorEvent, EventHint, SpanJSON, TransactionEvent } from '@sentry/core'
import { wasReported } from './reported'

// ceiling: quoted text in a thrown message is not redacted, unlike Go's filter; revisit when an asc-frontend issue shows customer text in quotes
const DROPPED_KEYS = new Set(['http.query', 'http.fragment'])
const GLOBAL_HANDLERS = 'auto.browser.global_handlers'
const TRANSPORT_DECIDED = new Set(['ApiError', 'AbortError', 'TimeoutError', 'SessionEndedError'])
const CAPACITY_KEYS = ['effectiveConnectionType', 'connectionType', 'deviceMemory', 'hardwareConcurrency', 'connection.rtt']
const KEPT_BREADCRUMBS = new Set(['navigation', 'fetch', 'xhr'])

type Bag = Record<string, unknown>

export function stripQuery(s: string): string {
  return s.replace(/[?#]\S*/g, '')
}

export function redactSecrets(s: string): string {
  return s
    .replace(/Bearer\s+\S+/gi, 'Bearer [redacted]')
    .replace(/eyJ[\w-]+\.[\w-]+\.[\w-]*/g, '[redacted]')
    // The SDK does not escape quotes: a value ends at the first `"]` before end, ` > `, `[x="` or the last `"]`.
    // ceiling: a value holding `"] > ` or `"][x="` leaks its tail, revisit when an asc-frontend issue shows one
    .replace(/\[([\w-]+)="[\s\S]*?"\](?=$| > |\[[\w-]+="|(?![\s\S]*"\]))/g, '[$1="[redacted]"]')
}

// Redact before stripping: a `?` or `#` inside an attribute value would otherwise cut it short.
const clean = (s: string): string => stripQuery(redactSecrets(s))

function walk(v: unknown, keys = false): unknown {
  if (typeof v === 'string') return clean(v)
  if (Array.isArray(v)) return v.map((x) => walk(x, keys))
  if (v !== null && typeof v === 'object') {
    const out: Bag = {}
    for (const [k, x] of Object.entries(v)) {
      if (DROPPED_KEYS.has(k)) continue
      out[keys ? clean(k) : k] = walk(x, keys)
    }
    return out
  }
  return v
}

function dropCapacity(bag: unknown): void {
  if (bag && typeof bag === 'object') for (const k of CAPACITY_KEYS) delete (bag as Bag)[k]
}

function scrubSpanLike<T extends { description?: string; data?: unknown }>(span: T): T {
  const out: T = { ...span }
  if (typeof out.description === 'string') out.description = clean(out.description)
  if (out.data && typeof out.data === 'object') {
    const data = walk(out.data) as Bag
    delete data['client.address']
    dropCapacity(data)
    out.data = data
  }
  return out
}

function scrubBreadcrumbs(b: unknown): unknown {
  if (Array.isArray(b)) return b.map((x) => walk(x))
  if (b && typeof b === 'object' && Array.isArray((b as Bag).values)) return { ...b, values: walk((b as Bag).values) }
  return b
}

export function scrubEvent(event: ErrorEvent): ErrorEvent {
  const out = { ...event } as Bag
  delete out.user
  for (const k of ['message', 'transaction'] as const) {
    if (typeof out[k] === 'string') out[k] = clean(out[k])
  }
  const exception = out.exception as { values?: Array<{ value?: string }> } | undefined
  if (exception?.values) {
    out.exception = {
      ...exception,
      values: exception.values.map((e) => (typeof e.value === 'string' ? { ...e, value: clean(e.value) } : e)),
    }
  }
  if (out.tags) out.tags = walk(out.tags, true)
  if (out.logentry) out.logentry = walk(out.logentry)
  if (out.extra) out.extra = walk(out.extra, true)
  if (out.contexts) out.contexts = walk(out.contexts, true)
  if (out.breadcrumbs) out.breadcrumbs = scrubBreadcrumbs(out.breadcrumbs)
  if (out.request) {
    const { url, headers } = out.request as { url?: string; headers?: Record<string, string> }
    const request: Bag = {}
    if (typeof url === 'string') request.url = stripQuery(url)
    const ua = Object.entries(headers ?? {}).filter(([k]) => k.toLowerCase() === 'user-agent')
    if (ua.length > 0) request.headers = Object.fromEntries(ua)
    out.request = request
  }
  return out as unknown as ErrorEvent
}

export function scrubTransaction(event: TransactionEvent): TransactionEvent {
  const out = scrubEvent(event as unknown as ErrorEvent) as unknown as TransactionEvent
  delete out.request
  if (out.measurements) out.measurements = walk(out.measurements, true) as typeof out.measurements
  dropCapacity(out.measurements)
  dropCapacity((out.contexts as { trace?: { data?: unknown } } | undefined)?.trace?.data)
  if (out.spans) out.spans = out.spans.map((s) => scrubSpanLike(s))
  return out
}

export function scrubSpan(span: SpanJSON): SpanJSON {
  return scrubSpanLike(span)
}

export function keepBreadcrumb(b: Breadcrumb): Breadcrumb | null {
  if (!b.category || !KEPT_BREADCRUMBS.has(b.category)) return null
  if (!b.data) return b
  const data = { ...b.data }
  for (const k of ['url', 'from', 'to']) {
    if (typeof data[k] === 'string') data[k] = stripQuery(data[k])
  }
  return { ...b, data }
}

export function dropEvent(event: ErrorEvent, hint: EventHint): boolean {
  const err = hint.originalException
  if (wasReported(err)) return true
  const mechanism = event.exception?.values?.[0]?.mechanism?.type
  if (!mechanism?.startsWith(GLOBAL_HANDLERS)) return false
  const name = err !== null && typeof err === 'object' ? (err as { name?: unknown }).name : undefined
  return typeof name === 'string' && TRANSPORT_DECIDED.has(name)
}

// An ApiError message is the gateway's text; the event carries `${kind} ${status}` like the transport report (monitoring cannot import ApiError).
export function scrubApiError(event: ErrorEvent, hint: EventHint): ErrorEvent {
  const err = hint.originalException as { name?: unknown; kind?: unknown; status?: unknown; message?: unknown } | null | undefined
  if (err === null || typeof err !== 'object' || err.name !== 'ApiError' || typeof err.kind !== 'string' || typeof err.message !== 'string') return event
  const values = event.exception?.values
  if (!values) return event
  const fixed = `${err.kind} ${String(err.status ?? '-')}`
  return { ...event, exception: { ...event.exception, values: values.map((v) => (v.value === err.message ? { ...v, value: fixed } : v)) } }
}

export function apiRoute(url: string): string {
  let path: string
  try {
    path = (url.startsWith('/') ? new URL(url, 'http://x') : new URL(url)).pathname
  } catch {
    return ':id'
  }
  const segments = path.split('/').filter(Boolean).map((s) => (/^([a-z][a-z-]*|v\d+)$/.test(s) ? s : ':id'))
  return '/' + segments.join('/')
}
