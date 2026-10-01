import { getDefaultIntegrations } from '@sentry/react'
import type { BrowserOptions } from '@sentry/react'
import { describe, expect, it } from 'vitest'
import { nameRouteSpan, sentryOptions, type MonitoringConfig, type Service } from './options'

const DSN = 'https://public@o1.ingest.de.sentry.io/1'
const GATEWAY = 'https://gw.test'
const SERVICES: Service[] = ['app', 'ops-console', 'support-console']
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

  it('sentryOptions_appCarriesLabelsPrivacyAndTracing', () => {
    const o = build()
    expect(o.dsn).toBe(DSN)
    expect(o.environment).toBe('production')
    expect(o.release).toBe('r1')
    expect(o.initialScope).toMatchObject({ tags: { service: 'app' } })
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
    expect('sendDefaultPii' in o).toBe(false)
    expect('tunnel' in o).toBe(false)
    expect('debug' in o).toBe(false)
  })

  it.each(['ops-console', 'support-console'] as const)('sentryOptions_consolesReportCrashesOnly (%s)', (service) => {
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
