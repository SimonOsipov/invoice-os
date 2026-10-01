import type { Breadcrumb, ErrorEvent, EventHint, SpanJSON, TransactionEvent } from '@sentry/core'
import { describe, expect, it } from 'vitest'
import { markReported } from './reported'
import {
  apiRoute,
  dropEvent,
  keepBreadcrumb,
  redactSecrets,
  scrubEvent,
  scrubSpan,
  scrubTransaction,
  stripQuery,
} from './scrub'

const M = 'TIN-NEEDLE-4242'
const as = <T>(x: unknown) => x as T
const json = (x: unknown) => JSON.stringify(x)

/** Every key path whose last segment is `key`, at any depth. */
function findKey(node: unknown, key: string, path = '$'): string[] {
  if (node === null || typeof node !== 'object') return []
  const hits: string[] = []
  for (const [k, v] of Object.entries(node)) {
    if (k === key) hits.push(`${path}.${k}`)
    hits.push(...findKey(v, key, `${path}.${k}`))
  }
  return hits
}

describe('stripQuery and redactSecrets', () => {
  it('stripQuery_andRedactSecrets', () => {
    const strip: Array<[string, string]> = [
      ['GET https://gw.test/api/x?q=12345678-0001 failed', 'GET https://gw.test/api/x failed'],
      ['/invoices#frag', '/invoices'],
      ['no query here', 'no query here'],
    ]
    const redact: Array<[string, string]> = [
      ['Authorization: Bearer abc.def-ghi', 'Authorization: Bearer [redacted]'],
      ['token eyJhbGciOi.eyJzdWIi.c2lnbmF0dXJl end', 'token [redacted] end'],
      [
        'body > div.asc-app > button#save.v2-btn[type="button"][title="Acme Ltd TIN 1234"]',
        'body > div.asc-app > button#save.v2-btn[type="[redacted]"][title="[redacted]"]',
      ],
    ]
    for (const [input, want] of strip) expect(stripQuery(input), input).toBe(want)
    for (const [input, want] of redact) expect(redactSecrets(input), input).toBe(want)
  })
})

describe('scrubEvent', () => {
  it('scrubEvent_requestKeepsOnlyUrlPathAndUserAgent', () => {
    const out = scrubEvent(
      as<ErrorEvent>({
        type: undefined,
        user: { ip_address: '{{auto}}', id: M },
        request: {
          url: `https://app.test/invoices?q=${M}#x`,
          query_string: `q=${M}`,
          cookies: { s: M },
          data: M,
          headers: { Referer: 'https://www.test/?persona=firm', 'user-agent': 'UA' },
        },
      }),
    )
    expect(out.request, 'request must survive, scrubbed').toBeDefined()
    expect(out.user).toBeUndefined()
    expect(out.request?.url).toBe('https://app.test/invoices')
    expect(out.request).not.toHaveProperty('query_string')
    expect(out.request).not.toHaveProperty('cookies')
    expect(out.request).not.toHaveProperty('data')
    expect(out.request?.headers).toEqual({ 'user-agent': 'UA' })
    expect(json(out)).not.toContain(M)
    expect(json(out)).not.toContain('persona')
  })

  it('scrubEvent_keepsUserAgentCaseInsensitively', () => {
    const out = scrubEvent(
      as<ErrorEvent>({ request: { url: 'https://app.test/', headers: { 'USER-AGENT': 'UA', Cookie: M, Authorization: `Bearer ${M}` } } }),
    )
    expect(out.request?.headers, 'headers must survive with the User-Agent').toBeDefined()
    expect(Object.values(out.request?.headers ?? {})).toEqual(['UA'])
  })

  it('scrubEvent_scrubsEveryTextField', () => {
    const event = as<ErrorEvent>({
      message: `GET /x?q=${M} failed, Bearer ${M}`,
      exception: {
        values: [
          { type: 'Error', value: `first ?q=${M}` },
          { type: 'Error', value: `second Bearer ${M}` },
        ],
      },
      transaction: `/invoices?q=${M}`,
      tags: { [`k?${M}`]: `v?${M}` },
      extra: { a: { b: `deep ?q=${M}` } },
      contexts: {
        trace: { data: { u: `https://gw.test/x?q=${M}`, 'http.query': `q=${M}` } },
        x: { n: 7 },
      },
      breadcrumbs: {
        values: [{ category: 'fetch', message: `m ?q=${M}`, data: { url: `https://gw.test/x?q=${M}`, 'http.query': `q=${M}` } }],
      },
    })
    const out = scrubEvent(event)
    expect(json(out).length, 'scrubbed event must not be empty').toBeGreaterThan(2)
    expect(json(out)).not.toContain(M)
    expect(findKey(out, 'http.query')).toEqual([])
    expect(findKey(out, 'http.fragment')).toEqual([])
    expect((out.contexts as { x: { n: number } }).x.n).toBe(7)
    expect(out.exception?.values?.[0].value).toContain('first')
    expect(out.exception?.values?.[1].value).toContain('second')
  })

  it('scrubEvent_scrubsTheSdkShapedBreadcrumbArray', () => {
    // The SDK's own Event.breadcrumbs type is Breadcrumb[], not { values: [] }.
    const out = scrubEvent(
      as<ErrorEvent>({
        breadcrumbs: [{ category: 'fetch', message: `m ?q=${M}`, data: { url: `https://gw.test/x?q=${M}`, 'http.query': `q=${M}` } }],
      }),
    )
    expect(Array.isArray(out.breadcrumbs) && out.breadcrumbs.length > 0, 'breadcrumbs must survive').toBe(true)
    expect(json(out)).not.toContain(M)
    expect(findKey(out, 'http.query')).toEqual([])
  })
})

describe('scrubTransaction', () => {
  it('scrubTransaction_dropsRequestAndScrubsSpans', () => {
    const tx = as<TransactionEvent>({
      type: 'transaction',
      request: { url: `https://app.test/?q=${M}`, headers: { 'user-agent': 'UA' } },
      contexts: { trace: { data: { 'lcp.element': `img[alt="${M}"]`, 'cls.source.1': `div[title="${M}"]` } } },
      spans: [
        {
          description: `GET https://gw.test/api/x?q=${M}`,
          data: {
            'http.url': `https://gw.test/api/x?q=${M}`,
            'url.full': `https://gw.test/api/x?q=${M}`,
            'http.query': `?q=${M}`,
            'http.fragment': '#f',
          },
        },
      ],
    })
    const out = scrubTransaction(tx)
    expect(out.spans?.length, 'spans must survive').toBe(1)
    expect(out).not.toHaveProperty('request')
    expect(out.spans?.[0].description).toBe('GET https://gw.test/api/x')
    const trace = (out.contexts as { trace: { data: Record<string, string> } }).trace.data
    expect(trace['lcp.element']).toBe('img[alt="[redacted]"]')
    expect(trace['cls.source.1']).toBe('div[title="[redacted]"]')
    expect(findKey(out, 'http.query')).toEqual([])
    expect(findKey(out, 'http.fragment')).toEqual([])
    expect(json(out)).not.toContain(M)
  })
})

describe('scrubSpan', () => {
  it('scrubSpan_scrubsAStandaloneSpan', () => {
    const span = as<SpanJSON>({
      op: 'ui.interaction.click',
      span_id: 's1',
      description: `body > button[title="${M}"]`,
      data: {
        'client.address': '{{auto}}',
        'url.full': `https://app.test/?q=${M}`,
        'http.query': `?q=${M}`,
        transaction: '/invoices/:id',
      },
    })
    const out = scrubSpan(span)
    expect(out.description).toBe('body > button[title="[redacted]"]')
    expect(out.data).not.toHaveProperty('client.address')
    expect(out.data).not.toHaveProperty('http.query')
    expect(String(out.data['url.full'])).not.toContain('?')
    expect(out.op).toBe('ui.interaction.click')
    expect(out.span_id).toBe('s1')
    expect(out.data.transaction).toBe('/invoices/:id')
    expect(json(out)).not.toContain(M)
  })
})

describe('keepBreadcrumb', () => {
  it('keepBreadcrumb_allowlistsAndStrips', () => {
    const nav = keepBreadcrumb(as<Breadcrumb>({ category: 'navigation', data: { from: '/?persona=firm', to: '/' } }))
    expect(nav, 'navigation is kept').not.toBeNull()
    expect(nav?.data?.from).toBe('/')
    expect(nav?.data?.to).toBe('/')

    for (const category of ['fetch', 'xhr']) {
      const kept = keepBreadcrumb(as<Breadcrumb>({ category, data: { url: `https://gw.test/api/x?actor=${M}` } }))
      expect(kept, `${category} is kept`).not.toBeNull()
      expect(kept?.data?.url).toBe('https://gw.test/api/x')
    }

    for (const b of [
      { category: 'console', message: M },
      { category: 'ui.click', message: `button[title="${M}"]` },
      { category: 'ui.input' },
      { category: 'sentry.event' },
    ]) {
      expect(keepBreadcrumb(as<Breadcrumb>(b)), b.category).toBeNull()
    }
  })
})

describe('dropEvent', () => {
  it('dropEvent_onlyEscapesTheTransportAlreadyDecided', () => {
    const GH = 'auto.browser.global_handlers'
    const evt = (type: string | undefined) =>
      as<ErrorEvent>({ exception: type ? { values: [{ type: 'Error', value: 'x', mechanism: { type, handled: false } }] } : undefined })
    const sessionEnded = Object.assign(new Error('x'), { name: 'SessionEndedError' })
    const reportedTypeError = new TypeError('Failed to fetch')
    markReported(reportedTypeError)

    const drops: Array<[string, unknown]> = [
      [`${GH}.onunhandledrejection`, { name: 'ApiError' }],
      [`${GH}.onerror`, new DOMException('x', 'AbortError')],
      [`${GH}.onunhandledrejection`, new DOMException('x', 'TimeoutError')],
      [`${GH}.onunhandledrejection`, sessionEnded],
      [`${GH}.onunhandledrejection`, reportedTypeError],
    ]
    const keeps: Array<[string, unknown]> = [
      [`${GH}.onunhandledrejection`, new TypeError('x')],
      ['auto.function.react.error_boundary', { name: 'ApiError' }],
      ['auto.function.react.error_boundary', reportedTypeError],
      [`${GH}.onerror`, new Error('x')],
    ]
    for (const [type, err] of drops) {
      expect(dropEvent(evt(type), as<EventHint>({ originalException: err })), `drop ${type} ${String((err as Error).name)}`).toBe(true)
    }
    for (const [type, err] of keeps) {
      expect(dropEvent(evt(type), as<EventHint>({ originalException: err })), `keep ${type} ${String((err as Error).name)}`).toBe(false)
    }
    expect(dropEvent(evt(undefined), as<EventHint>({ originalException: { name: 'ApiError' } }))).toBe(false)
    expect(dropEvent(evt(undefined), as<EventHint>({}))).toBe(false)
  })
})

describe('apiRoute', () => {
  it('apiRoute_keepsOnlyStaticSegments', () => {
    const uuid = '3f2c9a10-7b1e-4c55-9d3a-0123456789ab'
    const rows: Array<[string, string]> = [
      [`https://gw.test/api/invoice/v1/invoices/${uuid}/approval?q=1`, '/api/invoice/v1/invoices/:id/approval'],
      [`https://gw.test/api/submission/v1/extractions/${uuid}/pages/3`, '/api/submission/v1/extractions/:id/pages/:id'],
      ['https://gw.test/api/invoice/v1/workflow-roles/finance-lead/members', '/api/invoice/v1/workflow-roles/finance-lead/members'],
      ['https://gw.test/api/invoice/v1/documents/a%20b', '/api/invoice/v1/documents/:id'],
      ['https://gw.test/auth/refresh#x', '/auth/refresh'],
      ['not a url', ':id'],
    ]
    for (const [url, want] of rows) expect(apiRoute(url), url).toBe(want)
  })
})
