import type { Breadcrumb, ErrorEvent, EventHint, SpanJSON, TransactionEvent } from '@sentry/core'
import { wasReported } from './reported'

// ceiling: quoted text in a thrown message is not redacted, unlike Go's filter; revisit when an asc-frontend issue shows customer text in quotes
const DROPPED_KEYS = new Set(['http.query', 'http.fragment'])
const GLOBAL_HANDLERS = 'auto.browser.global_handlers'
const TRANSPORT_DECIDED = new Set(['ApiError', 'AbortError', 'TimeoutError', 'SessionEndedError'])
const KEPT_BREADCRUMBS = new Set(['navigation', 'fetch', 'xhr'])

type Bag = Record<string, unknown>

export function stripQuery(s: string): string {
  return s.replace(/[?#]\S*/g, '')
}

export function redactSecrets(s: string): string {
  return s
    .replace(/Bearer\s+\S+/g, 'Bearer [redacted]')
    .replace(/eyJ[\w-]+\.[\w-]+\.[\w-]+/g, '[redacted]')
    .replace(/\[([\w-]+)="[^"]*"\]/g, '[$1="[redacted]"]')
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

function scrubSpanLike<T extends { description?: string; data?: unknown }>(span: T): T {
  const out: T = { ...span }
  if (typeof out.description === 'string') out.description = clean(out.description)
  if (out.data && typeof out.data === 'object') {
    const data = walk(out.data) as Bag
    delete data['client.address']
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
  if (out.extra) out.extra = walk(out.extra)
  if (out.contexts) out.contexts = walk(out.contexts)
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
  const mechanism = event.exception?.values?.[0]?.mechanism?.type
  if (!mechanism?.startsWith(GLOBAL_HANDLERS)) return false
  const err = hint.originalException
  const name = err !== null && typeof err === 'object' ? (err as { name?: unknown }).name : undefined
  return (typeof name === 'string' && TRANSPORT_DECIDED.has(name)) || wasReported(err)
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
