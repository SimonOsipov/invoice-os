// @vitest-environment jsdom
import {
  addBreadcrumb,
  BrowserClient,
  captureException,
  defaultStackParser,
  getActiveSpan,
  getCurrentScope,
  getDefaultIntegrations,
  getIsolationScope,
  setCurrentClient,
  setExtra,
  setTag,
  setUser,
  spanToJSON,
} from '@sentry/react'
import { htmlTreeAsString } from '@sentry/core'
import type { Envelope } from '@sentry/core'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { sentryOptions, type MonitoringConfig, type Service } from './options'
import { scrubSpan } from './scrub'

const N = 'TIN-NEEDLE-4242'
const DSN = 'https://public@o1.ingest.de.sentry.io/1'

type Item = Record<string, any>

// Runs the real SDK pipeline (integrations, scope, beforeSend*) with a recording transport.
function boot(service: Service = 'app', over: Partial<MonitoringConfig> = {}) {
  const sent: Envelope[] = []
  const o = sentryOptions({ service, dsn: DSN, release: 'r1', gateway: 'https://gw.test', ...over })
  expect(o, 'options for a real DSN').not.toBeNull()
  const opts = o!
  const client = new BrowserClient({
    ...opts,
    integrations: (opts.integrations as (d: ReturnType<typeof getDefaultIntegrations>) => any)(getDefaultIntegrations(opts)),
    stackParser: defaultStackParser,
    transport: () => ({
      send: async (e: Envelope) => {
        sent.push(e)
        return {}
      },
      flush: async () => true,
    }),
  })
  setCurrentClient(client)
  client.init()
  const items = (): Item[] => sent.flatMap((e) => e[1].map((i) => i[1] as Item))
  return { client, items, raw: () => JSON.stringify(sent) }
}

beforeEach(() => {
  window.history.replaceState(null, '', `/invoices?q=${N}#frag`)
  Object.defineProperty(document, 'referrer', { value: `https://www.test/?persona=${N}`, configurable: true })
})

afterEach(() => {
  getCurrentScope().clear()
  getIsolationScope().clear()
})

describe('real SDK pipeline with the shipped options', () => {
  it('pipeline_errorEnvelopeCarriesNoCustomerText', async () => {
    const { client, items, raw } = boot()
    setUser({ id: N, email: `${N}@x.test`, ip_address: '1.2.3.4' })
    setTag('route', `/x?q=${N}`)
    addBreadcrumb({ category: 'console', message: N })
    addBreadcrumb({ category: 'ui.click', message: `button[title="${N}"]` })
    addBreadcrumb({ category: 'fetch', data: { url: `https://gw.test/api/x?actor=${N}`, method: 'GET' } })
    captureException(new Error(`boom ?q=${N} Bearer ${N}`))
    await client.flush(1000)

    const events = items().filter((i) => i.exception)
    expect(events.length, 'one error event must be sent').toBe(1)
    const ev = events[0]
    expect(raw()).not.toContain(N)
    expect(raw()).not.toContain('persona')
    expect(raw()).not.toContain('1.2.3.4')
    expect(ev.user).toBeUndefined()
    expect(ev.request.url).toBe('http://localhost:3000/invoices')
    expect(Object.keys(ev.request.headers)).toEqual(['User-Agent'])
    expect(ev.exception.values[0].value).toBe('boom  Bearer [redacted]')
    expect(ev.environment).toBe('production')
    expect(ev.release).toBe('r1')
    const crumbs: Item[] = ev.breadcrumbs ?? []
    expect(crumbs.map((c) => c.category)).toEqual(['fetch'])
    expect(crumbs[0].data.url).toBe('https://gw.test/api/x')
  })

  it('pipeline_globalHandlerEventsOfDecidedErrorsAreDropped', async () => {
    const { client, items } = boot()
    const GH = 'auto.browser.global_handlers.onunhandledrejection'
    const apiError = Object.assign(new Error('gateway said no'), { name: 'ApiError' })
    captureException(apiError, { mechanism: { type: GH, handled: false } })
    captureException(Object.assign(new Error('again'), { name: 'ApiError' }), {
      mechanism: { type: 'auto.function.react.error_boundary', handled: false },
    })
    captureException(new TypeError('plain code bug'), { mechanism: { type: GH, handled: false } })
    await client.flush(1000)
    const values = items().flatMap((i) => (i.exception?.values ?? []).map((v: Item) => v.value))
    expect(values.sort()).toEqual(['again', 'plain code bug'])
  })

  it('pipeline_transactionAndSpansAreScrubbed', async () => {
    const { client, items, raw } = boot()
    client.captureEvent({
      type: 'transaction',
      transaction: `/invoices/:id?q=${N}`,
      start_timestamp: 1,
      timestamp: 2,
      request: { url: `https://app.test/?q=${N}`, headers: { Referer: `https://x.test/?p=${N}` } },
      contexts: {
        trace: {
          trace_id: 'a'.repeat(32),
          span_id: 'b'.repeat(16),
          data: { 'lcp.element': `img[alt="${N}"]`, 'http.query': `?q=${N}` },
        },
      },
      spans: [
        {
          trace_id: 'a'.repeat(32),
          span_id: 'c'.repeat(16),
          start_timestamp: 1,
          description: `button[title="${N}"]`,
          data: { 'url.full': `https://gw.test/api/x?q=${N}`, 'http.fragment': `#${N}`, 'client.address': '1.2.3.4' },
        },
      ],
    })
    await client.flush(1000)
    const tx = items().filter((i) => i.type === 'transaction')
    expect(tx.length, 'the transaction must be sent').toBe(1)
    expect(raw()).not.toContain(N)
    expect(raw()).not.toContain('1.2.3.4')
    expect(tx[0].request).toBeUndefined()
    expect(tx[0].spans.length).toBeGreaterThan(0)
    expect(tx[0].spans[0].description).toBe('button[title="[redacted]"]')
  })

  it('pipeline_consolesDoNotStartTracing', async () => {
    const { client } = boot('ops-console')
    expect(client.getOptions().tracesSampleRate).toBeUndefined()
    expect(client.getOptions().tracePropagationTargets).toEqual([])
    expect(client.getIntegrationByName('BrowserTracing')).toBeUndefined()
    expect(client.getIntegrationByName('BrowserSession')).toBeUndefined()
    expect(client.getIntegrationByName('GlobalHandlers')).toBeDefined()
  })

  it('pipeline_pageLoadSpanIsNamedByTheRouteHook', () => {
    boot('app', { routeName: (p) => `ROUTE:${p}` })
    const span = getActiveSpan()
    expect(span, 'the app starts a page-load span at init').toBeDefined()
    expect(spanToJSON(span!).description).toBe('ROUTE:/invoices')
  })

  it('pipeline_errorEventTransactionCarriesTheRoutePattern', async () => {
    const { client, items } = boot('app', { routeName: (p) => `ROUTE:${p}` })
    captureException(new Error('boom'))
    await client.flush(1000)
    expect(items().find((i) => i.exception)?.transaction).toBe('ROUTE:/invoices')
  })

  it('pipeline_pageLoadSpanWithoutAHookIsUnmatched', () => {
    boot('app')
    expect(spanToJSON(getActiveSpan()!).description).toBe('<unmatched>')
  })

  it('pipeline_consolesStartNoPageLoadSpan', () => {
    boot('support-console', { routeName: (p) => p })
    expect(getActiveSpan()).toBeUndefined()
  })

  it('pipeline_circularExtraStillSends', async () => {
    const { client, items, raw } = boot()
    const cyc: Record<string, unknown> = { note: `KEEP ?q=${N}` }
    cyc.self = cyc
    setExtra('cyc', cyc)
    captureException(new Error('circular'))
    await client.flush(1000)
    const events = items().filter((i) => i.exception)
    expect(events.length, 'an event with a circular extra must still be sent').toBe(1)
    expect(raw()).not.toContain(N)
    expect(raw()).toContain('KEEP')
  })

  it('pipeline_selectorWithAQuotedAttributeValueIsRedacted', () => {
    const button = document.createElement('button')
    button.setAttribute('title', `${N} "Ltd" TIN 1234`)
    document.body.appendChild(button)
    const selector = htmlTreeAsString(button)
    expect(selector, 'the SDK builds a selector with the attribute').toContain('title=')
    const out = scrubSpan({ op: 'ui.interaction.click', description: selector, data: { 'lcp.element': selector } } as never)
    expect(JSON.stringify(out)).not.toContain(N)
    expect(JSON.stringify(out)).not.toContain('TIN 1234')
    expect(out.description).toContain('button')
  })

  it('pipeline_selectorWhoseValueHoldsAQuoteBracketIsRedacted', () => {
    const button = document.createElement('button')
    button.setAttribute('title', `a"] ${N} TIN 1234`)
    document.body.appendChild(button)
    const selector = htmlTreeAsString(button)
    expect(selector, 'the SDK writes the raw value').toContain('title="a"]')
    const out = scrubSpan({ op: 'ui.interaction.click', description: selector, data: { 'lcp.element': selector } } as never)
    expect(JSON.stringify(out)).not.toContain(N)
    expect(JSON.stringify(out)).not.toContain('TIN 1234')
  })
})
