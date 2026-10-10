// @vitest-environment jsdom
import * as Sentry from '@sentry/react'
import { getRootSpan, serializeEnvelope, spanToJSON } from '@sentry/core'
import type { Envelope } from '@sentry/core'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CrashBoundary } from './CrashBoundary'
import { initMonitoring } from './init'
import { sentryOptions, type MonitoringConfig, type Service } from './options'
import { RecoveryScreen } from './RecoveryScreen'
import { RELEASE } from './release'
import { markReported, wasReported } from './reported'
import { captureApiFailure, type ApiFailureInput } from './report'

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
  vi.useRealTimers()
  undo.splice(0).forEach((f) => f())
  await reset()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
})

function Thrower({ message, error }: { message: string; error?: Error }): never {
  throw error ?? new Error(message)
}

// React 19 logs a caught render error through console.error (D-11); the window listener keeps an uncaught one from failing the run.
function renderCrash(thrown?: Error): { uncaught: unknown[] } {
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
        <Thrower message="boom-render" error={thrown} />
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

// Pinning Date fixes the page age, which the SDK reads from performance.timeOrigin.
async function navigationFlow(sink: string[], ageSeconds?: number): Promise<void> {
  if (ageSeconds !== undefined) {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(performance.timeOrigin + ageSeconds * 1000)
  }
  window.history.replaceState(null, '', `/?handoff=${CODE}&persona=firm`)
  boot(sink, 'app', { routeName })
  window.history.replaceState(null, '', '/')
  getRootSpan(Sentry.getActiveSpan()!).end()
  window.history.pushState(null, '', `/invoices/${UUID}?q=${TIN}`)
  getRootSpan(Sentry.getActiveSpan()!).end()
  await Sentry.flush(1000)
  await reset()
}

// Must stay the first test: the SDK's fetch handlers are module-global and an earlier app init leaves a tracing handler on them.
describe('console tracing', () => {
  it('tracePropagation_consolesSendNoTraceHeaders', async () => {
    for (const service of ['ops-console', 'support-console', 'library'] as const) {
      fetchCalls.length = 0
      boot([], service)
      expect(Sentry.getActiveSpan(), service).toBeUndefined()
      await fetch(`${GW}/api/x`)
      expect(fetchCalls.length, service).toBe(1)
      expect(fetchCalls[0].headers['sentry-trace'], service).toBeUndefined()
      expect(fetchCalls[0].headers.baggage, service).toBeUndefined()
      await reset()
    }
    fetchCalls.length = 0
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

  it('initMonitoring_acceptsEveryRealDsnShapeAndRejectsTheRest', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const valid = [
      DSN,
      'https://abc123@o123.ingest.us.sentry.io/4506789',
      'https://abc123@o123.ingest.sentry.io/4506789',
      'https://abc123:secret@sentry.example.com/12',
      'https://abc123@sentry.example.com:9000/12',
      'http://abc123@localhost:9000/12',
      'https://abc123@sentry.example.com/prefix/sub/12',
      `  ${DSN}\n`,
    ]
    for (const dsn of valid) {
      vi.stubEnv('VITE_SENTRY_DSN', dsn)
      expect(initMonitoring('ops-console'), dsn).toBe(true)
      expect(Sentry.getClient()?.getDsn(), `the SDK accepts ${dsn}`).toBeDefined()
      await reset()
    }
    const invalid = [
      'not-a-dsn',
      'https://@o1.ingest.sentry.io/1',
      'https://o1.ingest.sentry.io/1',
      'https://public@o1.ingest.sentry.io',
      'https://public@o1.ingest.sentry.io/',
      'https://public@o1.ingest.sentry.io/abc',
      'ftp://public@o1.ingest.sentry.io/1',
      'public@o1.ingest.sentry.io/1',
      '//public@o1.ingest.sentry.io/1',
      'https://',
    ]
    for (const dsn of invalid) {
      vi.stubEnv('VITE_SENTRY_DSN', dsn)
      expect(() => initMonitoring('ops-console'), dsn).not.toThrow()
      expect(initMonitoring('ops-console'), dsn).toBe(false)
      expect(Sentry.getClient(), dsn).toBeUndefined()
    }
    expect(err).not.toHaveBeenCalled()
  })
})

describe('crash boundary', () => {
  it('crashBoundary_rendersRecoveryAndCapturesOnce', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    const { uncaught } = renderCrash()

    expect(screen.getByRole('region', { name: 'Something went wrong' })).toBeDefined()
    const title = screen.getByRole('heading', { level: 1, name: 'Something went wrong' })
    expect(title.tagName, 'a div: `.asc-app h1` would override the title style').toBe('DIV')
    expect(screen.getByText('This page hit an unexpected error. Reload to continue.')).toBeDefined()
    expect((screen.getByRole('button', { name: 'Reload page' }) as HTMLButtonElement).disabled).toBe(false)
    expect(document.body.textContent, 'the screen shows no error text (D-12)').toBe(
      'BASComplyAFRICASomething went wrongThis page hit an unexpected error. Reload to continue.Reload page',
    )
    await Sentry.flush(1000)

    const events = items(sink, 'event')
    expect(events.length, 'exactly one error event').toBe(1)
    const values: any[] = events[0].body.exception.values
    expect(values.some((v) => String(v.value).includes('boom-render'))).toBe(true)
    expect(events[0].body.tags.service).toBe('app')
    expect(values.map((v) => v.mechanism?.type)).toContain('auto.function.react.error_boundary')
    expect(uncaught).toEqual([])
  })

  it('crashBoundary_aReportedApiErrorIsNotSentAgainButStillRecovers', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    const err = Object.assign(new Error(`gateway: tenant ${TIN} refused`), { name: 'ApiError', kind: 'http', status: 503 })
    markReported(err)
    const { uncaught } = renderCrash(err)
    expect(screen.getByRole('region', { name: 'Something went wrong' })).toBeDefined()
    await Sentry.flush(1000)
    expect(items(sink, 'event'), 'the transport already reported it').toEqual([])
    expect(sink.join('\n')).not.toContain(TIN)
    expect(uncaught).toEqual([])
  })

  it('crashBoundary_anUnreportedApiErrorCarriesNoServerText', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    const err = Object.assign(new Error(`gateway: tenant ${TIN} refused ?q=${CODE}`), { name: 'ApiError', kind: 'http', status: 422 })
    renderCrash(err)
    await Sentry.flush(1000)
    const events = items(sink, 'event')
    expect(events.length).toBe(1)
    expect(events[0].body.exception.values[0].value).toBe('http 422')
    expect(sink.join('\n')).not.toContain(TIN)
    expect(sink.join('\n')).not.toContain(CODE)
  })

  it('crashBoundary_withoutAClientStillRecovers', () => {
    expect(Sentry.getClient()).toBeUndefined()
    const { uncaught } = renderCrash()
    expect(screen.getByRole('region', { name: 'Something went wrong' })).toBeDefined()
    const title = screen.getByRole('heading', { level: 1, name: 'Something went wrong' })
    expect(title.tagName, 'a div: `.asc-app h1` would override the title style').toBe('DIV')
    expect(uncaught).toEqual([])
  })

  it('recoveryScreen_reloadCallsLocationReload', () => {
    const reload = vi.fn()
    const saved = Object.getOwnPropertyDescriptor(globalThis, 'location')!
    undo.push(() => Object.defineProperty(globalThis, 'location', saved))
    Object.defineProperty(globalThis, 'location', { configurable: true, value: { ...window.location, reload } })
    render(<RecoveryScreen brand={<span>B</span>} />)
    expect(reload).not.toHaveBeenCalled()
    const button = screen.getByRole('button', { name: 'Reload page' }) as HTMLButtonElement
    expect(button.type).toBe('button')
    fireEvent.click(button)
    expect(reload).toHaveBeenCalledTimes(1)
    expect(button.disabled, 'never disabled after a click').toBe(false)
    fireEvent.click(button)
    expect(reload).toHaveBeenCalledTimes(2)
  })

  it('crashBoundary_twoCrashesAreTwoScreensAndTwoIssues', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    vi.spyOn(console, 'error').mockImplementation(() => {})
    render(
      <>
        <CrashBoundary brand={<span>B1</span>}>
          <Thrower message="boom-first" />
        </CrashBoundary>
        <CrashBoundary brand={<span>B2</span>}>
          <Thrower message="boom-second" />
        </CrashBoundary>
      </>,
    )
    expect(screen.getAllByRole('region', { name: 'Something went wrong' }).length).toBe(2)
    expect(screen.getAllByRole('button', { name: 'Reload page' }).length).toBe(2)
    await Sentry.flush(1000)
    const events = items(sink, 'event')
    expect(events.length, 'two events').toBe(2)
    const raw = events.map((e) => JSON.stringify(e.body.exception.values))
    expect(raw.some((r) => r.includes('boom-first'))).toBe(true)
    expect(raw.some((r) => r.includes('boom-second'))).toBe(true)
  })

  it('crashBoundary_aCrashingBrandIsCaughtByAnOuterBoundary', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    vi.spyOn(console, 'error').mockImplementation(() => {})
    render(
      <CrashBoundary brand={<span>outer</span>}>
        <CrashBoundary brand={<Thrower message="boom-brand" />}>
          <Thrower message="boom-inner" />
        </CrashBoundary>
      </CrashBoundary>,
    )
    expect(screen.getAllByRole('region', { name: 'Something went wrong' }).length, 'one screen, not zero').toBe(1)
    expect(document.body.textContent).toContain('outer')
    await Sentry.flush(1000)
    const raw = items(sink, 'event').map((e) => JSON.stringify(e.body.exception.values))
    expect(raw.length, 'both crashes reported').toBe(2)
    expect(raw.some((r) => r.includes('boom-inner'))).toBe(true)
    expect(raw.some((r) => r.includes('boom-brand'))).toBe(true)
  })

  it('crashBoundary_keepsAnApiErrorThatCrashesARender', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const named = (name: string) => Object.assign(new Error(`crash-${name}`), { name })
    function Boom({ error }: { error: Error }): never {
      throw error
    }
    const reported = new TypeError('crash-reported')
    captureApiFailure({ kind: 'network', status: null, method: 'GET', url: `${GW}/api/z`, error: reported })
    await Sentry.flush(1000)
    sink.length = 0
    // An already-reported error is not captured again by the boundary; the recovery screen still renders.
    for (const [error, events] of [[named('ApiError'), 1], [named('SessionEndedError'), 1], [reported, 0]] as Array<[Error, number]>) {
      render(
        <CrashBoundary brand={<span>B</span>}>
          <Boom error={error} />
        </CrashBoundary>,
      )
      await Sentry.flush(1000)
      expect(items(sink, 'event').length, error.name + ' ' + error.message).toBe(events)
      expect(screen.getByRole('region', { name: 'Something went wrong' })).toBeDefined()
      sink.length = 0
      cleanup()
    }
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

describe('global handlers, odd inputs', () => {
  it('globalHandlers_reportedErrorsStayDroppedOnEveryGlobalPath', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    const flushed = async (fire: () => void): Promise<Item[]> => {
      sink.length = 0
      fire()
      await Sentry.flush(1000)
      return items(sink, 'event')
    }
    const te = new TypeError('Failed to fetch')
    expect((await flushed(() => window.onunhandledrejection!({ reason: te } as PromiseRejectionEvent))).length, 'unreported TypeError is kept').toBe(1)

    const reported = new TypeError('Failed to fetch (reported)')
    captureApiFailure({ kind: 'network', status: null, method: 'GET', url: `${GW}/api/y`, error: reported })
    await Sentry.flush(1000)
    expect((await flushed(() => window.onerror!('m', 'f.js', 1, 1, reported))).length, 'reported error through onerror').toBe(0)
    for (let i = 0; i < 2; i++) {
      expect((await flushed(() => window.onunhandledrejection!({ reason: reported } as PromiseRejectionEvent))).length, `rejection ${i}`).toBe(0)
    }
  })

  it('globalHandlers_oddRejectionReasonsNeverThrowAndAreReported', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    const reasons: unknown[] = [undefined, null, 'text', 42, {}, Symbol('s'), () => {}]
    for (const reason of reasons) {
      sink.length = 0
      expect(() => window.onunhandledrejection!({ reason } as PromiseRejectionEvent), String(reason)).not.toThrow()
      await Sentry.flush(1000)
      expect(items(sink, 'event').length, String(reason)).toBe(1)
    }
    sink.length = 0
    window.onunhandledrejection!({ reason: { name: 'ApiError' } } as PromiseRejectionEvent)
    await Sentry.flush(1000)
    expect(items(sink, 'event').length, 'a plain object named ApiError').toBe(0)
  })
})

describe('captureApiFailure, odd inputs', () => {
  it('captureApiFailure_oddInputsNeverThrowAndNeverLeak', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    const SECRET = 'SECRET-UPSTREAM-31'
    const base: ApiFailureInput = { kind: 'http', status: 500, method: 'get', url: `${GW}/api/odd`, error: null }
    const cases: Array<[string, Partial<ApiFailureInput>, string]> = [
      ['null status is a dash', { kind: 'network', status: null, url: `${GW}/api/one` }, 'network - GET /api/one'],
      ['mixed-case method', { method: 'pOsT', url: `${GW}/api/two` }, 'http 500 POST /api/two'],
      ['relative url with query and fragment', { url: `/api/three?q=${TIN}#${CODE}` }, 'http 500 GET /api/three'],
      ['unparseable url', { url: 'http://', method: 'put' }, 'http 500 PUT :id'],
      ['empty url', { url: '', method: 'delete' }, 'http 500 DELETE :id'],
      ['credentials in the url', { url: `https://user:pw-NEEDLE@gw.test/api/four`, method: 'patch' }, 'http 500 PATCH /api/four'],
      ['status zero', { status: 0, kind: 'malformed', url: `${GW}/api/five` }, 'malformed 0 GET /api/five'],
      ['percent-encoded segment', { url: `${GW}/api/%7Bsecret%7D/six`, method: 'head' }, 'http 500 HEAD /api/:id/six'],
    ]
    const errors: unknown[] = [new Error(SECRET), undefined, null, SECRET, 42, Object.freeze(new Error(SECRET)), { message: SECRET }, Symbol(SECRET)]
    for (const [label, over, value] of cases) {
      const error = errors[cases.findIndex((c) => c[0] === label)]
      sink.length = 0
      expect(() => captureApiFailure({ ...base, ...over, error }), label).not.toThrow()
      await Sentry.flush(1000)
      const events = items(sink, 'event')
      expect(events.length, label).toBe(1)
      expect(events[0].body.exception.values[0].value, label).toBe(value)
      expect(events[0].body.exception.values[0].type, label).toBe('ApiFailure')
      const raw = sink.join('\n')
      for (const needle of [SECRET, TIN, CODE, 'pw-NEEDLE', 'user:', '%7B', 'secret']) expect(raw, `${label}: ${needle}`).not.toContain(needle)
      if (typeof error === 'object' && error !== null) expect(wasReported(error), label).toBe(true)
    }
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

  it('envelopes_dropAUserAndAClientAddressEvenWhenSet', async () => {
    const sink: string[] = []
    boot(sink, 'app')
    Sentry.setUser({ id: 'USER-NEEDLE-1', email: 'user-needle@example.test', ip_address: '203.0.113.9' })
    Sentry.captureException(new Error('with-user'))
    const span = Sentry.startInactiveSpan({ name: 'button', op: 'ui.interaction.click', experimental: { standalone: true } })
    span.setAttribute('client.address', '203.0.113.9')
    span.end()
    getRootSpan(Sentry.getActiveSpan()!).end()
    await Sentry.flush(1000)

    expect(items(sink, 'event').length, 'the event').toBe(1)
    expect(items(sink, 'span').length, 'the standalone span').toBeGreaterThan(0)
    expect(items(sink, 'transaction').length, 'the transaction').toBe(1)
    const raw = sink.join('\n')
    for (const needle of ['USER-NEEDLE-1', 'user-needle@example.test', '203.0.113.9', 'client.address', '"user":']) expect(raw).not.toContain(needle)
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
    expect(txn, 'a renamed span puts its route in baggage').not.toBeNull()
    expect(decodeURIComponent(txn![1])).toBe('/invoices/:id')
    expect(gateway.headers.baggage).not.toContain(UUID)
    expect(gateway.headers.traceparent).toBeUndefined()
    expect(third.headers['sentry-trace']).toBeUndefined()
    expect(third.headers.baggage).toBeUndefined()
    expect(third.headers.traceparent).toBeUndefined()

    fetchCalls.length = 0
    const lookalikes = [
      'https://gw.test.evil.example/api/x',
      'https://gw-test/api/x',
      `https://evil.example/?next=${GW}/api/x`,
      'http://gw.test/api/x',
      'https://gw.test:8443/api/x',
      `https://user@gw.test.evil.example/api/x`,
    ]
    for (const url of lookalikes) await fetch(url)
    expect(fetchCalls.map((c) => c.url)).toHaveLength(lookalikes.length)
    for (const c of fetchCalls) {
      expect(c.headers['sentry-trace'], c.url).toBeUndefined()
      expect(c.headers.baggage, c.url).toBeUndefined()
    }
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
    err.mockClear()
    expect(initMonitoring('ops-console')).toBe(false)
    expect(Sentry.getClient()).toBeUndefined()
    expect(err).toHaveBeenCalledTimes(0)
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
    // replaceState (the boot strip) is never a navigation, at any page age.
    for (const [age, bootStrip] of [[0.1, 0], [5, 0]]) {
      const sink: string[] = []
      await navigationFlow(sink, age)
      vi.useRealTimers()
      const nav = items(sink, 'transaction').filter((i) => i.body.contexts.trace.op === 'navigation')
      const names = nav.map((i) => i.body.transaction)
      expect(names.filter((n) => n === '/invoices/:id'), `the navigation transaction, age ${age}s`).toHaveLength(1)
      expect(names.filter((n) => n === '/').length, `boot-strip navigations, age ${age}s`).toBe(bootStrip)
      expect(names.length, `no navigation named by anything else, age ${age}s`).toBe(1 + bootStrip)
      const raw = sink.join('\n')
      for (const needle of [CODE, TIN, 'persona=', 'handoff=']) expect(raw, `age ${age}s`).not.toContain(needle)
      const crumbs = items(sink)
        .flatMap((i) => (i.body.breadcrumbs ?? []) as any[])
        .filter((c) => c.category === 'navigation')
      expect(crumbs.length, 'navigation breadcrumbs').toBeGreaterThan(0)
      for (const c of crumbs) {
        expect(c.data.from).not.toContain('?')
        expect(c.data.to).not.toContain('?')
      }
    }
  })

  it('navigation_replaceStateStartsNoNavigationAndKeepsThePageLoadOpen', async () => {
    // Page older than the SDK's redirect window, so a history call would start a root navigation.
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(performance.timeOrigin + 5000)
    const sink: string[] = []
    boot(sink, 'app', { routeName })
    const pageload = getRootSpan(Sentry.getActiveSpan()!)
    expect(spanToJSON(pageload).op).toBe('pageload')

    window.history.replaceState(null, '', '/settings/members')
    window.history.replaceState(null, '', `/settings/members?q=${TIN}`)
    expect(getRootSpan(Sentry.getActiveSpan()!), 'still the page-load span').toBe(pageload)
    expect(spanToJSON(pageload).timestamp, 'page load not cut short').toBeUndefined()

    window.history.pushState(null, '', `/invoices/${UUID}`)
    const nav = getRootSpan(Sentry.getActiveSpan()!)
    expect(spanToJSON(nav).op).toBe('navigation')
    expect(spanToJSON(pageload).timestamp, 'a real navigation ends the page load').toBeDefined()
    nav.end()

    window.history.replaceState(null, '', `/invoices/${UUID}?x=1`)
    const popped = new Promise((r) => window.addEventListener('popstate', r, { once: true }))
    window.history.back()
    await popped
    expect(spanToJSON(getRootSpan(Sentry.getActiveSpan()!)).op, 'popstate is a navigation').toBe('navigation')
    getRootSpan(Sentry.getActiveSpan()!).end()
    await Sentry.flush(1000)
    const ops = items(sink, 'transaction').map((i) => `${i.body.contexts.trace.op} ${i.body.transaction}`)
    expect(ops.filter((o) => o.startsWith('navigation'))).toEqual(['navigation /invoices/:id', 'navigation /settings/members'])
  })

  it('navigation_aHistoryCallThatLeavesTheUrlStartsNoNavigation', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(performance.timeOrigin + 5000)
    boot([], 'app', { routeName })
    const pageload = getRootSpan(Sentry.getActiveSpan()!)

    window.history.replaceState({ n: 0 }, '', '/settings/members')
    window.history.pushState({ n: 1 }, '', '/settings/members')
    expect(getRootSpan(Sentry.getActiveSpan()!), 'push to the URL a replace just wrote').toBe(pageload)

    window.history.pushState({ n: 2 }, '', '/invoices')
    const nav = getRootSpan(Sentry.getActiveSpan()!)
    expect(spanToJSON(nav).op, 'control: a push to a new URL navigates').toBe('navigation')

    window.history.pushState({ n: 3 }, '', '/invoices')
    expect(getRootSpan(Sentry.getActiveSpan()!), 'push to the current URL').toBe(nav)

    const popped = new Promise((r) => window.addEventListener('popstate', r, { once: true }))
    window.history.back()
    await popped
    expect(window.location.pathname).toBe('/invoices')
    expect(getRootSpan(Sentry.getActiveSpan()!), 'popstate onto the same URL').toBe(nav)
    expect(spanToJSON(nav).timestamp).toBeUndefined()
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
