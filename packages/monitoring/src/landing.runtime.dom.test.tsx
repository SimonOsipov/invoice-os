// @vitest-environment jsdom
import * as Sentry from '@sentry/react'
import { getRootSpan, serializeEnvelope, spanToJSON } from '@sentry/core'
import type { Envelope, Span } from '@sentry/core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { sentryOptions, type MonitoringConfig, type Service } from './options'
import { RELEASE } from './release'
import { captureApiFailure } from './report'

// Own file: `navigationsWired` in options.ts is module-global, so an earlier `app` boot would patch history for landing.
// The `app` row of envelopes_carryOnlyTheDisclosedContexts therefore stays last.
const DSN = 'https://public@o1.ingest.de.sentry.io/1'
const GW = 'https://gw.test'
const HUBSPOT = 'https://api-eu1.hsforms.com/submissions/v3/integration/submit/P/G'
const SEL = 'SEL-NEEDLE-5'
const LEAD = 'LEAD-NEEDLE@example.test'
const COMPANY = 'COMPANY-NEEDLE'
const SPA_QUERY = '?persona=PERSONA-NEEDLE&state=STATE-NEEDLE&signin=ready#FRAG-NEEDLE'
const landingRoute = (p: string): string => (p === '/' ? '/' : p === '/privacy' ? '/privacy' : '<unmatched>')
const LANDING: Service = 'landing'

interface Item {
  type: string
  body: any
}
interface FetchCall {
  url: string
  headers: Record<string, string>
}

const fetchCalls: FetchCall[] = []
let hubspotStatus = 200
let hubspotDown = false
// The SDK wraps globalThis.fetch once per module, so one stub lives for the whole file.
vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
  const headers: Record<string, string> = {}
  new Headers(init?.headers).forEach((v, k) => (headers[k] = v))
  const url = input instanceof Request ? input.url : String(input)
  fetchCalls.push({ url, headers })
  if (hubspotDown && url.includes('hsforms.com')) throw new TypeError('Failed to fetch')
  return new Response('{}', { status: url.includes('hsforms.com') ? hubspotStatus : 200 })
})

// The SDK wraps XHR once per module, so the no-op `send` is in place before the first boot.
XMLHttpRequest.prototype.send = function () {}

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

function boot(sink: string[], over: Partial<MonitoringConfig> = {}, transport = recordingTransport(sink)): void {
  const o = sentryOptions({ service: LANDING, dsn: DSN, release: RELEASE, routeName: landingRoute, ...over })
  expect(o, 'options for a real DSN').not.toBeNull()
  Sentry.init({ ...o!, transport })
}

async function reset(): Promise<void> {
  // An open idle span's timer would end it into the next test's client through the shared scope.
  const open = Sentry.getActiveSpan()
  if (open) getRootSpan(open).end()
  await Sentry.close()
  Sentry.getCurrentScope().clear()
  Sentry.getIsolationScope().clear()
  Sentry.getGlobalScope().clear()
  Sentry.getCurrentScope().setClient(undefined)
}

function pageLoad(): Span {
  const active = Sentry.getActiveSpan()
  expect(active, 'a page-load span is active').toBeDefined()
  return getRootSpan(active!)
}

const undo: Array<() => void> = []

beforeEach(() => {
  fetchCalls.length = 0
  hubspotStatus = 200
  hubspotDown = false
  window.history.replaceState(null, '', '/')
})

afterEach(async () => {
  vi.useRealTimers()
  undo.splice(0).forEach((f) => f())
  await reset()
  vi.restoreAllMocks()
})

// vi.unstubAllGlobals would also drop the file-level fetch stub the SDK wrapped, so plant and restore by hand.
function plant(target: object, key: string, value: unknown): void {
  const was = Object.getOwnPropertyDescriptor(target, key)
  Object.defineProperty(target, key, { configurable: true, writable: true, value })
  undo.push(() => (was ? Object.defineProperty(target, key, was) : delete (target as any)[key]))
}

const demoPost = (url = HUBSPOT) =>
  fetch(url, {
    method: 'POST',
    body: JSON.stringify({ fields: [{ name: 'email', value: LEAD }, { name: 'company', value: COMPANY }] }),
  })

// One landing visit: boot at `url`, run `during`, capture an error, end the page load, flush.
async function visit(sink: string[], url: string, during?: () => Promise<void> | void): Promise<void> {
  window.history.replaceState(null, '', url)
  boot(sink)
  await during?.()
  Sentry.captureException(new Error('on-landing'))
  pageLoad().end()
  await Sentry.flush(1000)
}

describe('landing tracing', () => {
  // Must stay first: the SDK's fetch handlers are module-global.
  it('tracePropagation_landingSendsNoTraceHeaders', async () => {
    boot([], { gateway: GW })
    pageLoad()
    await fetch(`${GW}/api/x`)
    await fetch('https://api-eu1.hsforms.com/x')
    expect(fetchCalls.length).toBe(2)
    for (const c of fetchCalls) {
      expect(c.headers['sentry-trace'], c.url).toBeUndefined()
      expect(c.headers.baggage, c.url).toBeUndefined()
      expect(c.headers.traceparent, c.url).toBeUndefined()
    }
  })

  it('tracePropagation_landingXhrSendsNoTraceHeaders', async () => {
    const headerNames = (): string[] => set.mock.calls.map((c) => String(c[0]).toLowerCase())
    const xhrTo = (url: string): void => {
      const x = new XMLHttpRequest()
      x.open('GET', url)
      x.send()
    }
    const set = vi.spyOn(XMLHttpRequest.prototype, 'setRequestHeader')
    boot([], { gateway: GW })
    pageLoad()
    xhrTo(`${GW}/api/x`)
    xhrTo('https://api-eu1.hsforms.com/x')
    for (const h of ['sentry-trace', 'baggage', 'traceparent']) expect(headerNames(), h).not.toContain(h)
    await reset()
    set.mockClear()

    // The SDK's XHR handlers are module-global, so the control boots after the landing phase.
    const o = sentryOptions({ service: LANDING, dsn: DSN, release: RELEASE, routeName: landingRoute, gateway: GW })!
    Sentry.init({ ...o, tracePropagationTargets: [/gw\.test/], transport: recordingTransport([]) })
    pageLoad()
    xhrTo(`${GW}/api/x`)
    expect(headerNames(), 'control: an XHR to a matching target carries the trace headers').toEqual(expect.arrayContaining(['sentry-trace', 'baggage']))
  })

  // One event per boot, so a regression on any one of the three is red on its own.
  it.each(['pushState', 'popstate', 'fragment'])('landing_aloneAHistoryEventStartsNoNavigation (%s)', async (event) => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(performance.timeOrigin + 5000)
    window.history.replaceState(null, '', '/start')
    window.history.pushState(null, '', '/privacy')
    const sink: string[] = []
    boot(sink)
    const pageload = pageLoad()
    const settled = (type: string) => new Promise((r) => window.addEventListener(type, r, { once: true }))
    if (event === 'pushState') window.history.pushState(null, '', '/x')
    if (event === 'popstate') {
      const popped = settled('popstate')
      window.history.back()
      await popped
      expect(window.location.pathname, 'control: back moved the location').toBe('/start')
    }
    if (event === 'fragment') {
      const hashed = settled('hashchange')
      window.location.hash = '#top'
      await hashed
      expect(window.location.hash, 'control: the fragment changed').toBe('#top')
    }
    expect(pageLoad(), 'still the page-load span').toBe(pageload)
    pageload.end()
    await Sentry.flush(1000)
    const tx = items(sink, 'transaction')
    expect(tx.map((i) => i.body.contexts.trace.op), 'only the page load').toEqual(['pageload'])
  })

  it('landing_pageLoadIsNamedAndNothingElseNavigates', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(performance.timeOrigin + 5000)
    window.history.replaceState(null, '', '/privacy')
    const sink: string[] = []
    boot(sink)
    const pageload = pageLoad()
    expect(spanToJSON(pageload).op).toBe('pageload')

    let popstates = 0
    const onPop = () => popstates++
    window.addEventListener('popstate', onPop)
    undo.push(() => window.removeEventListener('popstate', onPop))
    window.history.pushState(null, '', '/x')
    const popped = new Promise((r) => window.addEventListener('popstate', r, { once: true }))
    window.history.back()
    await popped
    const hashed = new Promise((r) => window.addEventListener('hashchange', r, { once: true }))
    window.location.hash = '#top'
    await hashed
    expect(popstates, 'control: the history events fired').toBeGreaterThanOrEqual(1)
    expect(window.location.pathname + window.location.hash).toBe('/privacy#top')

    expect(pageLoad(), 'still the page-load span').toBe(pageload)
    expect(spanToJSON(pageload).timestamp, 'page load not cut short').toBeUndefined()
    pageload.end()
    await Sentry.flush(1000)
    const tx = items(sink, 'transaction')
    expect(tx.length, 'exactly one transaction').toBe(1)
    expect(tx[0].body.contexts.trace.op).toBe('pageload')
    expect(tx[0].body.transaction).toBe('/privacy')
    expect(tx.filter((i) => i.body.contexts.trace.op === 'navigation')).toEqual([])
  })
})

describe('what the landing SDK sends', () => {
  it('pageload_isNamedByRouteAndCarriesNoQuery', async () => {
    const sink: string[] = []
    await visit(sink, `/privacy${SPA_QUERY}`)
    const tx = items(sink, 'transaction')
    expect(tx.length, 'the page-load transaction').toBe(1)
    expect(tx[0].body.transaction).toBe('/privacy')
    const ev = items(sink, 'event')
    expect(ev.length, 'the on-landing error').toBe(1)
    expect(ev[0].body.tags.service).toBe('landing')
    expect(ev[0].body.request.url, 'control: the event carries a url').toContain('/privacy')
    expect(ev[0].body.request.url).not.toContain('?')
    expect(ev[0].body.request.url).not.toContain('#')
    const raw = sink.join('\n')
    for (const needle of ['PERSONA-NEEDLE', 'STATE-NEEDLE', 'FRAG-NEEDLE', 'persona=', 'state=', 'signin=']) expect(raw, needle).not.toContain(needle)
  })

  it('demoForm_bodyNeverLeaves', async () => {
    const sink: string[] = []
    await visit(sink, '/', async () => {
      await demoPost()
    })
    const crumbs = items(sink).flatMap((i) => (i.body.breadcrumbs ?? []) as any[])
    expect(crumbs.some((c) => c.category === 'fetch' && c.data?.url === HUBSPOT && c.data?.method === 'POST'), 'fetch breadcrumb').toBe(true)
    const spans = items(sink, 'transaction').flatMap((i) => (i.body.spans ?? []) as any[])
    expect(spans.some((s) => s.op === 'http.client' && String(s.description).includes('hsforms.com')), 'http.client span').toBe(true)
    const raw = sink.join('\n')
    for (const needle of [LEAD, COMPANY]) expect(raw, needle).not.toContain(needle)
  })

  it('demoForm_aFailedPostLeavesOnlyItsStatus', async () => {
    hubspotStatus = 500
    const sink: string[] = []
    await visit(sink, '/', async () => {
      await demoPost()
    })
    const crumb = items(sink)
      .flatMap((i) => (i.body.breadcrumbs ?? []) as any[])
      .find((c) => c.category === 'fetch' && c.data?.url === HUBSPOT)
    expect(crumb, 'fetch breadcrumb').toBeDefined()
    expect(crumb.data.status_code).toBe(500)
    const raw = sink.join('\n')
    for (const needle of [LEAD, COMPANY]) expect(raw, needle).not.toContain(needle)
  })

  it.each([500, 404])('demoForm_failureIssueCarriesOnlyTheRoute %i', async (status) => {
    hubspotStatus = status
    const sink: string[] = []
    window.history.replaceState(null, '', '/')
    boot(sink)
    pageLoad()
    await demoPost()
    captureApiFailure({
      kind: 'http',
      status,
      method: 'POST',
      url: 'https://api-eu1.hsforms.com/submissions/v3/integration/submit/148915098/abc-123-needle',
      error: new Error(`hubspot ${status}`),
    })
    await Sentry.flush(1000)
    const events = items(sink, 'event')
    expect(events.length, 'one event').toBe(1)
    expect(events[0].body.exception.values[0].value).toBe(`http ${status} POST /submissions/v3/integration/submit/:id/:id`)
    expect(events[0].body.tags['api.status']).toBe(String(status))
    const raw = sink.join('\n')
    for (const needle of ['148915098', 'abc-123-needle', LEAD, COMPANY]) expect(raw, needle).not.toContain(needle)
  })

  it('demoForm_urlQueryAndFragmentNeverLeave', async () => {
    const sink: string[] = []
    await visit(sink, '/', async () => {
      await demoPost(`${HUBSPOT}?portal=QUERY-NEEDLE#HASH-NEEDLE`)
    })
    const crumb = items(sink)
      .flatMap((i) => (i.body.breadcrumbs ?? []) as any[])
      .find((c) => c.category === 'fetch' && String(c.data?.url).includes('hsforms.com'))
    expect(crumb, 'fetch breadcrumb').toBeDefined()
    expect(crumb.data.url).toBe(HUBSPOT)
    const raw = sink.join('\n')
    for (const needle of ['QUERY-NEEDLE', 'HASH-NEEDLE', 'portal=']) expect(raw, needle).not.toContain(needle)
  })

  it('demoForm_networkFailureMessageNamesNoHost', async () => {
    hubspotDown = true
    const sink: string[] = []
    await visit(sink, '/', async () => {
      Sentry.captureException(await demoPost().catch((e) => e))
    })
    const values = items(sink, 'event').map((i) => i.body.exception.values[0].value)
    expect(values, 'the failed POST and the visit error').toEqual(['Failed to fetch', 'on-landing'])
    const raw = sink.join('\n')
    for (const needle of [LEAD, COMPANY]) expect(raw, needle).not.toContain(needle)
  })

  it('referrerAndLaterUrlEditsNeverLeave', async () => {
    plant(document, 'referrer', 'https://elsewhere.test/?REFERRER-NEEDLE#REFERRER-FRAG')
    const sink: string[] = []
    const preScrub: string[] = []
    window.history.replaceState(null, '', '/privacy')
    boot(sink)
    Sentry.getClient()!.on('preprocessEvent', (e) => preScrub.push(JSON.stringify(e)))
    window.history.pushState(null, '', '/privacy?PUSH-NEEDLE=1#PUSH-FRAG')
    Sentry.captureException(new Error('after-url-edit'))
    pageLoad().end()
    await Sentry.flush(1000)
    expect(preScrub.join('\n'), 'control: the SDK reads the referrer').toContain('REFERRER-NEEDLE')
    const events = items(sink, 'event')
    expect(events.length, 'the after-url-edit error').toBe(1)
    expect(events[0].body.request.url, 'control: the event carries a url').toContain('/privacy')
    const raw = sink.join('\n')
    for (const needle of ['REFERRER-NEEDLE', 'REFERRER-FRAG', 'PUSH-NEEDLE', 'PUSH-FRAG']) expect(raw, needle).not.toContain(needle)
  })

  it('envelopes_neverInferIpAndSendNoSessions', async () => {
    const sink: string[] = []
    await visit(sink, `/privacy${SPA_QUERY}`)
    const typed = items(sink).filter((i) => i.type === 'event' || i.type === 'transaction')
    expect(typed.some((i) => i.type === 'event'), 'an event item').toBe(true)
    expect(typed.some((i) => i.type === 'transaction'), 'a transaction item').toBe(true)
    for (const i of typed) expect(i.body.sdk.settings.infer_ip, i.type).toBe('never')
    expect(items(sink).map((i) => i.type)).not.toContain('session')
    expect(items(sink).map((i) => i.type)).not.toContain('sessions')
    const raw = sink.join('\n')
    for (const needle of ['ip_address', 'client.address', '"user":']) expect(raw).not.toContain(needle)
  })

  it('envelopes_dropAnIdentityPlantedOnTheScopeAndSpans', async () => {
    const sink: string[] = []
    window.history.replaceState(null, '', '/')
    boot(sink)
    Sentry.setUser({ id: 'USER-NEEDLE', email: 'USER-NEEDLE@example.test', ip_address: '203.0.113.9' })
    const root = pageLoad()
    root.setAttribute('client.address', '203.0.113.9')
    Sentry.startSpan({ name: 'child', attributes: { 'client.address': '203.0.113.9' } }, () => {})
    Sentry.captureException(new Error('with-user'))
    root.end()
    await Sentry.flush(1000)
    const typed = items(sink).filter((i) => i.type === 'event' || i.type === 'transaction')
    expect(typed.map((i) => i.type).sort(), 'one event and one transaction').toEqual(['event', 'transaction'])
    const raw = sink.join('\n')
    for (const needle of ['USER-NEEDLE', '203.0.113.9', 'client.address', '"user":', 'ip_address']) expect(raw, needle).not.toContain(needle)
  })

  it('storage_landingWritesNothing', async () => {
    const cookie = vi.spyOn(Document.prototype, 'cookie', 'set').mockImplementation(() => {})
    const store = () => ({ getItem: () => null, setItem: vi.fn(), removeItem: vi.fn(), clear: vi.fn() })
    const local = store()
    const session = store()
    const idb = { open: vi.fn() }
    plant(globalThis, 'localStorage', local)
    plant(globalThis, 'sessionStorage', session)
    plant(globalThis, 'indexedDB', idb)

    document.cookie = 'x=1'
    local.setItem('x', '1')
    session.setItem('x', '1')
    idb.open('x')
    expect([cookie.mock.calls.length, local.setItem.mock.calls.length, session.setItem.mock.calls.length, idb.open.mock.calls.length], 'control: writes register').toEqual([1, 1, 1, 1])
    for (const m of [cookie, local.setItem, session.setItem, idb.open]) m.mockClear()

    const sink: string[] = []
    await visit(sink, '/', async () => {
      await demoPost()
    })
    await Sentry.close()
    expect(items(sink, 'event').length, 'control: an event was sent').toBe(1)
    expect(items(sink, 'transaction').length, 'control: a transaction was sent').toBe(1)
    expect(cookie).not.toHaveBeenCalled()
    for (const s of [local, session]) for (const m of [s.setItem, s.removeItem, s.clear]) expect(m).not.toHaveBeenCalled()
    expect(idb.open).not.toHaveBeenCalled()
  })

  it('sdk_neverWritesConsoleErrors', async () => {
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const send = vi.fn().mockRejectedValue(new Error('network down'))
    window.history.replaceState(null, '', '/privacy')
    boot([], {}, () => ({ send, flush: async () => true }))
    Sentry.captureException(new Error('x'))
    pageLoad().end()
    await Sentry.flush(1000)
    expect(send.mock.calls.length, 'the event and the transaction both tried to send').toBeGreaterThanOrEqual(2)
    expect(err).not.toHaveBeenCalled()
  })

  it('webVitalSelectors_areRedacted', async () => {
    const sink: string[] = []
    boot(sink)
    Sentry.startInactiveSpan({ name: `button[title="${SEL}"]`, op: 'ui.interaction.click', experimental: { standalone: true } }).end()
    const root = pageLoad()
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

// Last: the `app` row wires the history patch for the rest of the file.
describe('disclosed contexts', () => {
  const PLANTED = { connection: { effectiveType: '4g', type: 'wifi', rtt: 50 }, deviceMemory: 8, hardwareConcurrency: 12 }
  const GONE = ['culture', 'effectiveConnectionType', 'connectionType', 'connection.rtt', 'deviceMemory', 'hardwareConcurrency']
  // Contexts that name what went wrong, the page, the browser, OS or device type. QA records the measured set in the story.
  const ALLOWED = new Set(['trace', 'react'])

  it.each(['landing', 'app'])('envelopes_carryOnlyTheDisclosedContexts (%s)', async (service) => {
    for (const [k, value] of Object.entries(PLANTED)) plant(navigator, k, value)
    expect((navigator as any).deviceMemory, 'control: the planting reads back').toBe(8)

    const sink: string[] = []
    const preScrub: string[] = []
    window.history.replaceState(null, '', '/privacy')
    boot(sink, { service: service as Service, gateway: GW })
    Sentry.getClient()!.on('preprocessEvent', (e) => preScrub.push(JSON.stringify(e)))
    Sentry.captureException(new Error('on-landing'))
    pageLoad().end()
    await Sentry.flush(1000)

    const typed = items(sink).filter((i) => i.type === 'event' || i.type === 'transaction')
    expect(typed.some((i) => i.type === 'event'), 'an event item').toBe(true)
    expect(typed.some((i) => i.type === 'transaction'), 'a transaction item').toBe(true)
    const before = preScrub.join('\n')
    for (const needle of ['effectiveConnectionType', 'deviceMemory', 'hardwareConcurrency']) expect(before, `control: the SDK collects ${needle}`).toContain(needle)

    const raw = sink.join('\n')
    for (const needle of GONE) expect(raw, needle).not.toContain(needle)
    const keys = new Set(typed.flatMap((i) => Object.keys(i.body.contexts ?? {})))
    expect([...keys].sort(), 'measured contexts keys').toEqual([...keys].filter((k) => ALLOWED.has(k)).sort())
  })
})
