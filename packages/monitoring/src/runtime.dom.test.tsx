// @vitest-environment jsdom
import * as Sentry from '@sentry/react'
import { getRootSpan, serializeEnvelope } from '@sentry/core'
import type { Envelope } from '@sentry/core'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CrashBoundary } from './CrashBoundary'
import { initMonitoring } from './init'
import { sentryOptions, type MonitoringConfig, type Service } from './options'
import { RecoveryScreen } from './RecoveryScreen'
import { RELEASE } from './release'
import { wasReported } from './reported'
import { captureApiFailure } from './report'

const DSN = 'https://public@o1.ingest.de.sentry.io/1'
const GW = 'https://gw.test'
const UUID = '3f2c9a1e-4b7d-4c2a-8e51-9d0f6a7b1c33'
const CODE = 'CODE-NEEDLE-77'
const TIN = 'TIN-NEEDLE-4242'
const SEL = 'SEL-NEEDLE-5'
const JWT = 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJORUVETEUifQ.c2lnTkVFRExF'
const routeName = (p: string): string => (p.startsWith('/invoices/') ? '/invoices/:id' : p)

interface Item {
  type: string
  body: any
}
interface FetchCall {
  url: string
  headers: Record<string, string>
}

const fetchCalls: FetchCall[] = []
// The SDK wraps globalThis.fetch once per module, so one stub lives for the whole file.
vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
  const headers: Record<string, string> = {}
  new Headers(init?.headers).forEach((v, k) => (headers[k] = v))
  fetchCalls.push({ url: input instanceof Request ? input.url : String(input), headers })
  return new Response('{}', { status: 200 })
})

const text = (e: Envelope): string => {
  const s = serializeEnvelope(e)
  return typeof s === 'string' ? s : new TextDecoder().decode(s)
}

function recordingTransport(sink: string[]) {
  return () => ({
    send: async (e: Envelope) => {
      sink.push(text(e))
      return {}
    },
    flush: async () => true,
  })
}

// Envelope text: header line, then (item header, item body) line pairs.
function items(sink: string[], type?: string): Item[] {
  const all = sink.flatMap((t) => {
    const lines = t.split('\n')
    const out: Item[] = []
    for (let i = 1; i + 1 < lines.length; i += 2) out.push({ type: JSON.parse(lines[i]).type, body: JSON.parse(lines[i + 1]) })
    return out
  })
  return type ? all.filter((i) => i.type === type) : all
}

function boot(sink: string[], service: Service = 'app', over: Partial<MonitoringConfig> = {}, transport = recordingTransport(sink)): void {
  const o = sentryOptions({ service, dsn: DSN, release: RELEASE, gateway: GW, ...over })
  expect(o, 'options for a real DSN').not.toBeNull()
  Sentry.init({ ...o!, transport })
}

async function reset(): Promise<void> {
  await Sentry.close()
  Sentry.getCurrentScope().clear()
  Sentry.getIsolationScope().clear()
  Sentry.getGlobalScope().clear()
  Sentry.getCurrentScope().setClient(undefined)
}

const undo: Array<() => void> = []

beforeEach(() => {
  fetchCalls.length = 0
  window.history.replaceState(null, '', '/')
})

afterEach(async () => {
  cleanup()
  undo.splice(0).forEach((f) => f())
  await reset()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
})

function Thrower({ message }: { message: string }): never {
  throw new Error(message)
}

// React 19 logs a caught render error through console.error (D-11); the window listener keeps an uncaught one from failing the run.
function renderCrash(): { uncaught: unknown[] } {
  const uncaught: unknown[] = []
  const onError = (e: ErrorEvent) => {
    e.preventDefault()
    uncaught.push(e.error)
  }
  window.addEventListener('error', onError)
  undo.push(() => window.removeEventListener('error', onError))
  vi.spyOn(console, 'error').mockImplementation(() => {})
  try {
    render(
      <CrashBoundary brand={<span>B</span>}>
        <Thrower message="boom-render" />
      </CrashBoundary>,
    )
  } catch (e) {
    uncaught.push(e)
  }
  return { uncaught }
}

async function crashFlow(sink: string[]): Promise<void> {
  boot(sink, 'app')
  renderCrash()
  await Sentry.flush(1000)
  await reset()
}

async function pageloadFlow(sink: string[]): Promise<void> {
  window.history.replaceState(null, '', `/invoices/${UUID}?persona=firm&handoff=${CODE}&q=${TIN}`)
  boot(sink, 'app', { routeName })
  Sentry.captureException(new Error('on-page'))
  getRootSpan(Sentry.getActiveSpan()!).end()
  await Sentry.flush(1000)
  await reset()
}

async function navigationFlow(sink: string[]): Promise<void> {
  window.history.replaceState(null, '', `/?handoff=${CODE}&persona=firm`)
  boot(sink, 'app', { routeName })
  window.history.replaceState(null, '', '/')
  // A navigation within 1.5s of load is a redirect child of the page-load span, so end that first.
  getRootSpan(Sentry.getActiveSpan()!).end()
  window.history.pushState(null, '', `/invoices/${UUID}?q=${TIN}`)
  getRootSpan(Sentry.getActiveSpan()!).end()
  await Sentry.flush(1000)
  await reset()
}

// Must stay the first test: the SDK's fetch handlers are module-global and an earlier app init leaves a tracing handler on them.
describe('console tracing', () => {
  it('tracePropagation_consolesSendNoTraceHeaders', async () => {
    boot([], 'ops-console')
    expect(Sentry.getActiveSpan()).toBeUndefined()
    await fetch(`${GW}/api/x`)
    expect(fetchCalls.length).toBe(1)
    expect(fetchCalls[0].headers['sentry-trace']).toBeUndefined()
    expect(fetchCalls[0].headers.baggage).toBeUndefined()

    await reset()
    fetchCalls.length = 0
    boot([], 'app')
    await fetch(`${GW}/api/x`)
    expect(fetchCalls.length).toBe(1)
    expect(fetchCalls[0].headers['sentry-trace'], 'control: the app sends it').toBeDefined()
  })
})

describe('initMonitoring', () => {
  it('initMonitoring_emptyDsnLeavesTheSdkUntouched', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    for (const dsn of ['', ' ']) {
      vi.stubEnv('VITE_SENTRY_DSN', dsn)
      expect(initMonitoring('app', { gateway: GW })).toBe(false)
      expect(Sentry.getClient()).toBeUndefined()
    }
    expect(err).not.toHaveBeenCalled()
    expect(warn).not.toHaveBeenCalled()
    expect(fetchCalls).toEqual([])

    vi.stubEnv('VITE_SENTRY_DSN', DSN)
    expect(initMonitoring('app')).toBe(true)
    expect(Sentry.getClient()).toBeDefined()
  })

  it('initMonitoring_dsnBindsALabelledClient', async () => {
    vi.stubEnv('VITE_SENTRY_DSN', DSN)
    expect(initMonitoring('app', { gateway: GW })).toBe(true)
    const o = Sentry.getClient()!.getOptions()
    expect(o.environment).toBe('production')
    expect(o.release).toBe(RELEASE)
    await Sentry.close()
    expect(fetchCalls.filter((c) => c.url.includes('ingest.de.sentry.io'))).toEqual([])
  })
})

describe('crash boundary', () => {
  it('crashBoundary_rendersRecoveryAndCapturesOnce', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    const { uncaught } = renderCrash()

    expect(screen.getByRole('region', { name: 'Something went wrong' })).toBeDefined()
    expect(screen.getByText('This page hit an unexpected error. Reload to continue.')).toBeDefined()
    expect((screen.getByRole('button', { name: 'Reload page' }) as HTMLButtonElement).disabled).toBe(false)
    await Sentry.flush(1000)

    const events = items(sink, 'event')
    expect(events.length, 'exactly one error event').toBe(1)
    const values: any[] = events[0].body.exception.values
    expect(values.some((v) => String(v.value).includes('boom-render'))).toBe(true)
    expect(events[0].body.tags.service).toBe('app')
    expect(values.map((v) => v.mechanism?.type)).toContain('auto.function.react.error_boundary')
    expect(uncaught).toEqual([])
  })

  it('crashBoundary_withoutAClientStillRecovers', () => {
    expect(Sentry.getClient()).toBeUndefined()
    const { uncaught } = renderCrash()
    expect(screen.getByRole('region', { name: 'Something went wrong' })).toBeDefined()
    expect(uncaught).toEqual([])
  })

  it('recoveryScreen_reloadCallsLocationReload', () => {
    const reload = vi.fn()
    const saved = Object.getOwnPropertyDescriptor(globalThis, 'location')!
    undo.push(() => Object.defineProperty(globalThis, 'location', saved))
    Object.defineProperty(globalThis, 'location', { configurable: true, value: { ...window.location, reload } })
    render(<RecoveryScreen brand={<span>B</span>} />)
    expect(reload).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Reload page' }))
    expect(reload).toHaveBeenCalledTimes(1)
  })
})

describe('global handlers', () => {
  it('globalHandlers_reportOnlyUndecidedEscapes', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    const rejection = async (reason: unknown): Promise<number> => {
      sink.length = 0
      window.onunhandledrejection!({ reason } as PromiseRejectionEvent)
      await Sentry.flush(1000)
      return items(sink, 'event').length
    }

    window.onerror!('m', 'f.js', 1, 1, new Error('boom-global'))
    await Sentry.flush(1000)
    expect(items(sink, 'event').length, 'uncaught Error').toBe(1)
    expect(await rejection(new Error('boom-rejection')), 'unhandled rejection with an Error').toBe(1)

    const decided = [
      Object.assign(new Error('x-api'), { name: 'ApiError' }),
      new DOMException('x-abort', 'AbortError'),
      new DOMException('x-timeout', 'TimeoutError'),
      Object.assign(new Error('x-session'), { name: 'SessionEndedError' }),
    ]
    for (const reason of decided) expect(await rejection(reason), String((reason as Error).name)).toBe(0)

    sink.length = 0
    const te = new TypeError('Failed to fetch')
    captureApiFailure({ kind: 'network', status: null, method: 'GET', url: `${GW}/api/x`, error: te })
    window.onunhandledrejection!({ reason: te } as PromiseRejectionEvent)
    await Sentry.flush(1000)
    const events = items(sink, 'event')
    expect(events.length, 'one event in total').toBe(1)
    expect(events[0].body.exception.values.at(-1).type).toBe('ApiFailure')
  })
})

describe('what the SDK sends', () => {
  it('pageload_isNamedByRouteAndCarriesNoQuery', async () => {
    const sink: string[] = []
    await pageloadFlow(sink)
    const tx = items(sink, 'transaction')
    expect(tx.length, 'the page-load transaction').toBe(1)
    expect(tx[0].body.transaction).toBe('/invoices/:id')
    const ev = items(sink, 'event')
    expect(ev.length, 'the on-page error').toBe(1)
    expect(ev[0].body.request.url).not.toContain('?')
    const raw = sink.join('\n')
    for (const needle of [CODE, TIN, 'persona=', 'handoff=']) expect(raw).not.toContain(needle)
  })

  it('sessionSecrets_neverLeave', async () => {
    const sink: string[] = []
    const stored = JSON.stringify({ v: 1, token: JWT })
    // Node 26 shadows jsdom's localStorage with an undefined global, so the page's storage is a stub.
    const store = { getItem: (k: string) => (k === 'invoice-os.session' ? stored : null) }
    vi.stubGlobal('localStorage', store)
    boot(sink, 'app')
    await fetch(`${GW}/api/tenancy/v1/me`, { headers: { Authorization: `Bearer ${JWT}` } })
    Sentry.captureException(new Error(`failed with Bearer ${JWT}`))
    getRootSpan(Sentry.getActiveSpan()!).end()
    await Sentry.flush(1000)

    const all = items(sink)
    const crumbs = all.flatMap((i) => (i.body.breadcrumbs ?? []) as any[])
    expect(
      crumbs.some((c) => c.category === 'fetch' && c.data?.url === `${GW}/api/tenancy/v1/me`),
      'fetch breadcrumb',
    ).toBe(true)
    const spans = items(sink, 'transaction').flatMap((i) => (i.body.spans ?? []) as any[])
    expect(spans.some((s) => s.op === 'http.client' && String(s.description).includes('/api/tenancy/v1/me')), 'http.client span').toBe(true)
    expect(items(sink, 'event').length, 'the error event').toBe(1)
    expect(store.getItem('invoice-os.session')).toContain(JWT)

    const raw = sink.join('\n')
    for (const part of [JWT, ...JWT.split('.')]) expect(raw).not.toContain(part)
  })

  it('envelopes_neverInferIpAndSendNoSessions', async () => {
    const sink: string[] = []
    await crashFlow(sink)
    await pageloadFlow(sink)
    await navigationFlow(sink)

    const typed = items(sink).filter((i) => i.type === 'event' || i.type === 'transaction')
    expect(typed.some((i) => i.type === 'event'), 'an event item').toBe(true)
    expect(typed.some((i) => i.type === 'transaction'), 'a transaction item').toBe(true)
    for (const i of typed) expect(i.body.sdk.settings.infer_ip, i.type).toBe('never')
    expect(items(sink).map((i) => i.type)).not.toContain('session')
    expect(items(sink).map((i) => i.type)).not.toContain('sessions')
    const raw = sink.join('\n')
    for (const needle of ['ip_address', 'client.address', '"user":']) expect(raw).not.toContain(needle)
  })

  it('tracePropagation_gatewayOnly', async () => {
    const sink: string[] = []
    window.history.replaceState(null, '', `/invoices/${UUID}`)
    boot(sink, 'app', { routeName })
    const span = Sentry.getActiveSpan()
    expect(span, 'the app starts a page-load span').toBeDefined()
    await fetch(`${GW}/api/x?y=1`)
    await fetch('https://api.hubspot.com/x')
    expect(fetchCalls.length).toBe(2)
    const [gateway, third] = fetchCalls
    expect(gateway.headers['sentry-trace']).toMatch(new RegExp(`^${span!.spanContext().traceId}-[0-9a-f]{16}`))
    expect(gateway.headers.baggage).toBeDefined()
    expect(gateway.headers.baggage).not.toContain('y=1')
    expect(gateway.headers.baggage).not.toContain('?')
    const txn = /(?:^|,)sentry-transaction=([^,]*)/.exec(gateway.headers.baggage)
    if (txn) expect(decodeURIComponent(txn[1])).toBe('/invoices/:id')
    expect(gateway.headers.traceparent).toBeUndefined()
    expect(third.headers['sentry-trace']).toBeUndefined()
    expect(third.headers.baggage).toBeUndefined()
    expect(third.headers.traceparent).toBeUndefined()
  })

  it('sdk_neverWritesConsoleErrors', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const send = vi.fn().mockRejectedValue(new Error('network down'))
    boot([], 'ops-console', {}, () => ({ send, flush: async () => true }))
    Sentry.captureException(new Error('x'))
    await Sentry.flush(1000)
    expect(send).toHaveBeenCalledTimes(1)
    expect(err).not.toHaveBeenCalled()

    await reset()
    vi.stubEnv('VITE_SENTRY_DSN', 'not-a-dsn')
    expect(initMonitoring('ops-console')).toBe(true)
    // The SDK's DSN parser logs one "Invalid Sentry Dsn" line at init, so D-19's "writes nothing" cannot hold.
    for (const call of err.mock.calls) expect(String(call[0])).toMatch(/^Invalid Sentry Dsn: not-a-dsn$/)
  })

  it('captureApiFailure_fixedMessageAndRoute', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    const error = new Error('upstream')
    captureApiFailure({
      kind: 'http',
      status: 503,
      method: 'get',
      url: `${GW}/api/invoice/v1/invoices/${UUID}/approval?q=${TIN}`,
      error,
    })
    await Sentry.flush(1000)

    const events = items(sink, 'event')
    expect(events.length, 'one event').toBe(1)
    const ev = events[0].body
    const route = '/api/invoice/v1/invoices/:id/approval'
    expect(ev.exception.values[0].type).toBe('ApiFailure')
    expect(ev.exception.values[0].value).toBe(`http 503 GET ${route}`)
    expect(ev.fingerprint).toEqual(['api-failure', 'http', '503', 'GET', route])
    expect(ev.tags['api.kind']).toBe('http')
    expect(ev.tags['api.status']).toBe('503')
    for (const needle of [TIN, UUID]) expect(sink.join('\n')).not.toContain(needle)
    expect(wasReported(error)).toBe(true)
  })

  it('navigation_isNamedByRouteAndCarriesNoQuery', async () => {
    const sink: string[] = []
    await navigationFlow(sink)
    const nav = items(sink, 'transaction').filter((i) => i.body.contexts.trace.op === 'navigation')
    expect(nav.length, 'the navigation transaction').toBe(1)
    expect(nav[0].body.transaction).toBe('/invoices/:id')
    const raw = sink.join('\n')
    for (const needle of [CODE, TIN, 'persona=', 'handoff=']) expect(raw).not.toContain(needle)
    const crumbs = items(sink)
      .flatMap((i) => (i.body.breadcrumbs ?? []) as any[])
      .filter((c) => c.category === 'navigation')
    expect(crumbs.length, 'navigation breadcrumbs').toBeGreaterThan(0)
    for (const c of crumbs) {
      expect(c.data.from).not.toContain('?')
      expect(c.data.to).not.toContain('?')
    }
  })

  it('apiFailures_backToBackRepeatsAreDeduplicated', async () => {
    // One call site: the SDK's dedupe compares stack frames.
    const fail = (route: string) =>
      captureApiFailure({ kind: 'http', status: 503, method: 'GET', url: `${GW}/api/${route}`, error: new Error('upstream') })
    const run = (routes: string[]) => {
      for (const r of routes) fail(r)
    }
    const sink: string[] = []
    boot(sink, 'app')
    run(['a', 'a'])
    await Sentry.flush(1000)
    expect(items(sink, 'event').length, 'two identical failures').toBe(1)

    // A fresh init resets dedupe's memory, so the first call below is not a repeat of the last one above.
    await reset()
    sink.length = 0
    boot(sink, 'app')
    run(['a', 'b', 'a'])
    await Sentry.flush(1000)
    expect(items(sink, 'event').length, 'a different failure between').toBe(3)
  })

  it('webVitalSelectors_areRedacted', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    Sentry.startInactiveSpan({ name: `button[title="${SEL}"]`, op: 'ui.interaction.click', experimental: { standalone: true } }).end()
    const root = getRootSpan(Sentry.getActiveSpan()!)
    root.setAttribute('lcp.element', `img[alt="${SEL}"]`)
    root.end()
    await Sentry.flush(1000)

    const spans = items(sink, 'span')
    expect(spans.length, 'the standalone span').toBeGreaterThan(0)
    expect(spans.some((s) => (s.body.description ?? s.body.name) === 'button[title="[redacted]"]')).toBe(true)
    const tx = items(sink, 'transaction')
    expect(tx.length, 'the page-load transaction').toBe(1)
    expect(tx[0].body.contexts.trace.data['lcp.element']).toBe('img[alt="[redacted]"]')
    expect(sink.join('\n')).not.toContain(SEL)
  })
})
