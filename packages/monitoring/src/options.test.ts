import { getDefaultIntegrations } from '@sentry/react'
import type { BrowserOptions } from '@sentry/react'
import { describe, expect, it } from 'vitest'
import type { Breadcrumb, ErrorEvent, EventHint, SpanJSON, TransactionEvent } from '@sentry/core'
import { nameRouteSpan, sentryOptions, type MonitoringConfig, type Service } from './options'
import { markReported } from './reported'

const DSN = 'https://public@o1.ingest.de.sentry.io/1'
const GATEWAY = 'https://gw.test'
const SERVICES: Service[] = ['app', 'ops-console', 'support-console', 'landing', 'library']
const routeName = (p: string) => (p.startsWith('/invoices/') ? '/invoices/:id' : p)

const cfg = (over: Partial<MonitoringConfig> = {}): MonitoringConfig => ({
  service: 'app',
  dsn: ` ${DSN} `,
  release: 'r1',
  gateway: GATEWAY,
  routeName,
  ...over,
})

function build(over: Partial<MonitoringConfig> = {}): BrowserOptions {
  const o = sentryOptions(cfg(over))
  expect(o, 'sentryOptions returned null for a real DSN').not.toBeNull()
  return o as BrowserOptions
}

function integrationNames(o: BrowserOptions): string[] {
  expect(typeof o.integrations, 'integrations must be a function over the defaults').toBe('function')
  const apply = o.integrations as (d: ReturnType<typeof getDefaultIntegrations>) => Array<{ name: string }>
  return apply(getDefaultIntegrations({})).map((i) => i.name)
}

const D2 = { userInfo: false, cookies: false, httpHeaders: false, httpBodies: [], urlQueryParams: false }

describe('sentryOptions', () => {
  it('sentryOptions_emptyDsnIsOff', () => {
    for (const service of SERVICES) {
      for (const dsn of [undefined, '', ' \t ']) {
        expect(sentryOptions(cfg({ service, dsn })), `${service} dsn=${JSON.stringify(dsn)}`).toBeNull()
      }
    }
  })

  it.each(['app', 'landing'])('sentryOptions_carriesLabelsPrivacyAndTracing (%s)', (service) => {
    const o = build({ service: service as Service })
    expect(o.dsn).toBe(DSN)
    expect(o.environment).toBe('production')
    expect(o.release).toBe('r1')
    expect(o.initialScope).toMatchObject({ tags: { service } })
    expect(o.dataCollection).toEqual(D2)
    expect(o.enhanceFetchErrorMessages).toBe(false)
    expect(o.tracesSampleRate).toBe(1)
    for (const hook of ['beforeSend', 'beforeSendTransaction', 'beforeSendSpan', 'beforeBreadcrumb'] as const) {
      expect(typeof o[hook], hook).toBe('function')
    }
    const names = integrationNames(o)
    expect(names).toContain('Dedupe')
    expect(names).toContain('GlobalHandlers')
    expect(names).toContain('BrowserTracing')
    expect(names).not.toContain('BrowserSession')
    expect(names).not.toContain('CultureContext')
    expect('sendDefaultPii' in o).toBe(false)
    expect('tunnel' in o).toBe(false)
    expect('debug' in o).toBe(false)
  })

  it.each(['ops-console', 'support-console', 'library'] as const)('sentryOptions_consolesReportCrashesOnly (%s)', (service) => {
    const o = build({ service })
    expect(o.dsn).toBe(DSN)
    expect(o.environment).toBe('production')
    expect(o.release).toBe('r1')
    expect(o.initialScope).toMatchObject({ tags: { service } })
    expect(o.dataCollection).toEqual(D2)
    expect(o.enhanceFetchErrorMessages).toBe(false)
    expect(o.tracePropagationTargets).toEqual([])
    expect(o.tracesSampleRate).toBeUndefined()
    const names = integrationNames(o)
    expect(names).toContain('GlobalHandlers')
    expect(names).not.toContain('BrowserSession')
    expect(names).not.toContain('BrowserTracing')
  })

  it('sentryOptions_propagatesToTheGatewayOnly', () => {
    const targets = (build().tracePropagationTargets ?? []) as Array<string | RegExp>
    expect(targets.length, 'app with a gateway must carry propagation targets').toBeGreaterThan(0)
    const matches = (u: string) => targets.some((t) => (typeof t === 'string' ? u.includes(t) : t.test(u)))
    expect(matches('https://gw.test/api/invoice/v1/invoices?q=1')).toBe(true)
    expect(matches('https://gw.test/auth/refresh')).toBe(true)
    expect(matches('https://gw.test.evil.example/api/x')).toBe(false)
    expect(matches('https://evil.example/?u=https://gw.test/')).toBe(false)
    expect(matches('https://api.hubspot.com/submissions')).toBe(false)
    expect(matches('/api/x')).toBe(false)

    expect(build({ gateway: null }).tracePropagationTargets).toEqual([])
  })
})

describe('sentryOptions gateway origin and error transaction', () => {
  it('sentryOptions_normalisesTheGatewayOrigin', () => {
    for (const gateway of ['https://GW.test', 'https://gw.test:443', 'https://gw.test/api/']) {
      const t = targetsOf(build({ gateway }))
      expect(t.length, gateway).toBe(1)
      expect(matchesAny(t, new URL('https://gw.test/api/x').toString()), gateway).toBe(true)
      expect(matchesAny(t, 'https://gw.test.evil.example/api/x'), gateway).toBe(false)
    }
    expect(build({ gateway: 'not a url' }).tracePropagationTargets).toEqual([])
  })

  it('sentryOptions_beforeSendRenamesTheErrorTransaction', () => {
    const ev = { transaction: '/invoices/u1?q=x' } as ErrorEvent
    const out = build().beforeSend!(ev, {}) as ErrorEvent
    expect(out.transaction).toBe('/invoices/:id')
    const bare = build({ routeName: undefined }).beforeSend!(ev, {}) as ErrorEvent
    expect(bare.transaction).toBe('/invoices/u1')
    const none = build().beforeSend!({} as ErrorEvent, {}) as ErrorEvent
    expect(none.transaction, 'an event with no transaction is not given one').toBeUndefined()
  })
})

const M = 'TIN-NEEDLE-4242'
const targetsOf = (o: BrowserOptions) => (o.tracePropagationTargets ?? []) as Array<string | RegExp>
const matchesAny = (targets: Array<string | RegExp>, u: string) => targets.some((t) => (typeof t === 'string' ? u.includes(t) : t.test(u)))

describe('sentryOptions more', () => {
  it('sentryOptions_hooksAreWiredToTheScrubbers', () => {
    for (const service of SERVICES) {
      const o = build({ service })
      const ev = o.beforeSend!(
        { type: undefined, user: { id: M }, message: `m ?q=${M}`, request: { url: `https://a.test/?q=${M}` } } as unknown as ErrorEvent,
        {} as EventHint,
      ) as ErrorEvent
      expect(ev, `${service} beforeSend keeps an ordinary event`).not.toBeNull()
      expect(JSON.stringify(ev), service).not.toContain(M)
      expect(ev.user).toBeUndefined()

      const tx = o.beforeSendTransaction!(
        { type: 'transaction', request: { url: 'https://a.test/' }, spans: [{ description: `x ?q=${M}` }] } as unknown as TransactionEvent,
        {} as EventHint,
      ) as TransactionEvent
      expect(tx.request, `${service} beforeSendTransaction drops request`).toBeUndefined()
      expect(JSON.stringify(tx), service).not.toContain(M)

      const span = o.beforeSendSpan!({ span_id: 's', description: `b[title="${M}"]`, data: {} } as unknown as SpanJSON)
      expect(JSON.stringify(span), service).not.toContain(M)
      expect((span as SpanJSON).description).toBe('b[title="[redacted]"]')

      expect(o.beforeBreadcrumb!({ category: 'console', message: M } as Breadcrumb, {})).toBeNull()
      const crumb = o.beforeBreadcrumb!({ category: 'fetch', data: { url: `https://a.test/x?q=${M}` } } as Breadcrumb, {})
      expect(crumb?.data?.url).toBe('https://a.test/x')
    }
  })

  it('sentryOptions_beforeSendDropsOnlyDecidedGlobalHandlerEvents', () => {
    const o = build()
    const evt = (type: string) => ({ type: undefined, exception: { values: [{ type: 'Error', value: 'x', mechanism: { type, handled: false } }] } }) as unknown as ErrorEvent
    const GH = 'auto.browser.global_handlers.onunhandledrejection'
    expect(o.beforeSend!(evt(GH), { originalException: { name: 'ApiError' } } as EventHint)).toBeNull()
    const te = new TypeError('Failed to fetch')
    markReported(te)
    expect(o.beforeSend!(evt(GH), { originalException: te } as EventHint)).toBeNull()
    expect(o.beforeSend!(evt(GH), { originalException: new TypeError('x') } as EventHint)).not.toBeNull()
    expect(o.beforeSend!(evt('auto.function.react.error_boundary'), { originalException: { name: 'ApiError' } } as EventHint)).not.toBeNull()
  })

  it('sentryOptions_integrationsKeepEveryDefaultExceptBrowserSessionAndCultureContext', () => {
    const defaults = getDefaultIntegrations({}).map((i) => i.name)
    expect(defaults, 'the SDK defaults include the two dropped integrations').toEqual(
      expect.arrayContaining(['BrowserSession', 'CultureContext', 'Dedupe', 'GlobalHandlers']),
    )
    const want = defaults.filter((n) => n !== 'BrowserSession' && n !== 'CultureContext')
    expect(integrationNames(build({ service: 'ops-console' }))).toEqual(want)
    expect(integrationNames(build({ service: 'support-console' }))).toEqual(want)
    expect(integrationNames(build({ service: 'app' }))).toEqual([...want, 'BrowserTracing'])
    expect(integrationNames(build({ service: 'landing' }))).toEqual([...want, 'BrowserTracing'])
  })

  it('sentryOptions_anUnknownServiceIsTreatedAsCrashesOnly', () => {
    const o = build({ service: 'bogus' as Service })
    expect(o.tracesSampleRate).toBeUndefined()
    expect(o.tracePropagationTargets).toEqual([])
    expect(integrationNames(o)).not.toContain('BrowserTracing')
    expect(integrationNames(o)).not.toContain('BrowserSession')
  })

  it('sentryOptions_dsnIsTrimmedForEveryService', () => {
    for (const service of SERVICES) expect(build({ service, dsn: `\n ${DSN}\t ` }).dsn, service).toBe(DSN)
  })

  it('sentryOptions_noPrivacyFlagIsSetForAnyService', () => {
    for (const service of SERVICES) {
      const o = build({ service })
      for (const k of ['sendDefaultPii', 'tunnel', 'debug', 'propagateTraceparent', 'tracesSampler', 'replaysSessionSampleRate']) {
        expect(k in o, `${service} ${k}`).toBe(false)
      }
    }
  })

  it('sentryOptions_gatewayEdges', () => {
    const slash = targetsOf(build({ gateway: 'https://gw.test/' }))
    expect(slash.length, 'a trailing-slash gateway still yields targets').toBeGreaterThan(0)
    expect(matchesAny(slash, 'https://gw.test/api/x')).toBe(true)
    expect(matchesAny(targetsOf(build({ gateway: 'https://gw.test///' })), 'https://gw.test/auth/x')).toBe(true)

    const dotted = targetsOf(build({ gateway: 'https://gw.test' }))
    expect(matchesAny(dotted, 'https://gwXtest/api/x'), 'a dot in the gateway is literal').toBe(false)
    const meta = targetsOf(build({ gateway: 'https://gw.test:8080' }))
    expect(matchesAny(meta, 'https://gw.test:8080/api/x')).toBe(true)
    expect(matchesAny(meta, 'https://gw.test:80800/api/x')).toBe(false)
    expect(matchesAny(targetsOf(build({ gateway: 'https://g(w).test' })), 'https://g(w).test/api/x'), 'regex metacharacters are escaped').toBe(true)

    for (const gateway of [undefined, null, '', '/', '//']) {
      expect(build({ gateway }).tracePropagationTargets, JSON.stringify(gateway)).toEqual([])
    }
    for (const service of ['ops-console', 'support-console', 'landing'] as const) {
      expect(build({ service: service as Service, gateway: GATEWAY }).tracePropagationTargets, service).toEqual([])
    }
  })

  it('sentryOptions_everyReturnIsAFreshObject', () => {
    expect(build()).not.toBe(build())
  })
})

describe('nameRouteSpan more', () => {
  it('nameRouteSpan_keepsEveryOtherOptionAndNeverAddsAttributes', () => {
    const input = { name: '/invoices/u1', op: 'pageload', startTime: 5, attributes: { a: 1 }, forceTransaction: true }
    const out = nameRouteSpan(routeName)(input)
    expect(out).toEqual({ ...input, name: '/invoices/:id' })
    const bare = nameRouteSpan(routeName)({ name: '/x' })
    expect(bare).toEqual({ name: '/x' })
    expect('attributes' in nameRouteSpan(undefined)({ name: '/x' })).toBe(false)
  })

  it('nameRouteSpan_callsTheHookWithTheTargetNameOnly', () => {
    const seen: string[] = []
    const out = nameRouteSpan((p) => (seen.push(p), `R(${p})`))({ name: `/invoices/${M}` })
    expect(seen).toEqual([`/invoices/${M}`])
    expect(out.name).toBe(`R(/invoices/${M})`)
    expect(nameRouteSpan(undefined)({ name: `/invoices/${M}` }).name).not.toContain(M)
  })
})

describe('nameRouteSpan', () => {
  it('nameRouteSpan_namesFromTheTargetPath', () => {
    for (const op of ['navigation', 'pageload']) {
      const out = nameRouteSpan(routeName)({ name: '/invoices/u1', op, attributes: { a: 1 } })
      expect(out.name).toBe('/invoices/:id')
      expect(out.op).toBe(op)
      expect(out.attributes).toEqual({ a: 1 })
      expect(Object.keys(out.attributes ?? {})).not.toContain('sentry.source')
    }
    expect(nameRouteSpan(undefined)({ name: '/x' }).name).toBe('<unmatched>')
  })
})
