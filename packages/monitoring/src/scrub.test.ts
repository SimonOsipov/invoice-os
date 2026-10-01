import type { Breadcrumb, ErrorEvent, EventHint, SpanJSON, TransactionEvent } from '@sentry/core'
import { describe, expect, it } from 'vitest'
import { markReported, wasReported } from './reported'
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

  it('stripQuery_removesEveryRunAndStopsAtWhitespace', () => {
    const rows: Array<[string, string]> = [
      ['a?b c?d #e f', 'a c  f'],
      ['https://gw.test/api/x?q=1&b=2#frag', 'https://gw.test/api/x'],
      ['a?b\nc', 'a\nc'],
      ['a\tb?x\tc', 'a\tb\tc'],
      ['?', ''],
      ['', ''],
    ]
    for (const [input, want] of rows) expect(stripQuery(input), JSON.stringify(input)).toBe(want)
  })

  it('redactSecrets_redactsEveryOccurrenceAndLeavesOtherTextAlone', () => {
    const rows: Array<[string, string]> = [
      ['Bearer a Bearer b', 'Bearer [redacted] Bearer [redacted]'],
      ['Bearer\ttabbed', 'Bearer [redacted]'],
      ['eyJa.eyJb.sig and eyJc.eyJd.sig2', '[redacted] and [redacted]'],
      ['eyJhbGci-_9.eyJzdWI_-1.c2ln-_bg', '[redacted]'],
      ['eyJhbGci.eyJzdWIi only two parts', 'eyJhbGci.eyJzdWIi only two parts'],
      ['x[aria-label="a b"][name="c"]', 'x[aria-label="[redacted]"][name="[redacted]"]'],
      ['button#save.v2-btn[title="x y"]', 'button#save.v2-btn[title="[redacted]"]'],
      ['a[title="x?y#z"]', 'a[title="[redacted]"]'],
      ['plain text, no secrets [not-an-attr]', 'plain text, no secrets [not-an-attr]'],
      ['', ''],
    ]
    for (const [input, want] of rows) expect(redactSecrets(input), JSON.stringify(input)).toBe(want)
  })

  it('redactSecrets_redactsAnAttributeValueThatContainsAQuote', () => {
    // The SDK's htmlTreeAsString writes [name="value"] without escaping the value.
    const rows: Array<[string, string[]]> = [
      [`button[title="Acme "Ltd" TIN 1234"]`, ['Acme', 'Ltd', '1234']],
      [`img[alt="say "hi""][type="x"]`, ['say', 'hi']],
      [`a[title="Joe's "Best" Foods"] > b`, ['Joe', 'Best', 'Foods']],
    ]
    for (const [input, needles] of rows) {
      const out = redactSecrets(input)
      expect(out, input).toContain('[redacted]')
      for (const needle of needles) expect(out, input).not.toContain(needle)
    }
  })
})

describe('scrub gaps', () => {
  it('redactSecrets_redactsTheTailAfterAQuoteBracketInsideAnAttributeValue', () => {
    // htmlTreeAsString writes the raw value, so a value holding `"]` ends the lazy match early.
    const rows: Array<[string, string[]]> = [
      [`button[title="a"] TIN 1234"]`, ['TIN', '1234']],
      [`div > button[alt="x"] Acme Ltd"][title="y"] > span`, ['Acme', 'Ltd']],
      [`img[aria-label="p"] q"]`, ['q"]']],
    ]
    for (const [input, needles] of rows) {
      const out = redactSecrets(input)
      expect(out, input).toContain('[redacted]')
      for (const needle of needles) expect(out, input).not.toContain(needle)
    }
    expect(redactSecrets(`div > button[alt="x"] Acme Ltd"][title="y"] > span`)).toContain('div > button')
  })

  it('redactSecrets_redactsAnEmptySignatureJwtAndALowercaseBearer', () => {
    expect(redactSecrets('t eyJhbGciOi.eyJzdWIi. end')).toBe('t [redacted] end')
    expect(redactSecrets('authorization: bearer abc123')).toBe('authorization: Bearer [redacted]')
  })

  it('scrubEvent_scrubsLogentryAndExtraAndContextKeys', () => {
    const out = scrubEvent(
      as<ErrorEvent>({
        logentry: { message: `m ?q=${M}`, params: [`p Bearer ${M}`] },
        extra: { [`k?q=${M}`]: 1 },
        contexts: { [`c?q=${M}`]: { a: 1 } },
      }),
    )
    expect(json(out)).not.toContain(M)
    expect(Object.values(out.extra ?? {})).toEqual([1])
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

describe('scrubEvent sites', () => {
  const q = `KEEP ?q=${M}`
  const crumb = { category: 'fetch', message: q, data: { url: `https://gw.test/x?q=${M}`, note: q } }
  const sites: Array<[string, Record<string, unknown>]> = [
    ['message', { message: q }],
    ['transaction', { transaction: q }],
    ['exception.values[0].value', { exception: { values: [{ type: 'Error', value: q }, { type: 'Error', value: 'KEEP' }] } }],
    ['exception.values[1].value', { exception: { values: [{ type: 'Error', value: 'KEEP' }, { type: 'Error', value: q }] } }],
    ['tags value', { tags: { route: q } }],
    ['tags key', { tags: { [`KEEP?${M}`]: 'v' } }],
    ['extra object', { extra: { a: { b: { c: q } } } }],
    ['extra array', { extra: { list: ['x', { deep: [q] }] } }],
    ['contexts object', { contexts: { custom: { a: { b: q } } } }],
    ['contexts.trace.data', { contexts: { trace: { data: { u: q } } } }],
    ['breadcrumbs array message', { breadcrumbs: [crumb] }],
    ['breadcrumbs array data', { breadcrumbs: [{ category: 'fetch', data: { note: q } }] }],
    ['breadcrumbs values message', { breadcrumbs: { values: [{ category: 'fetch', message: q }] } }],
    ['breadcrumbs values data', { breadcrumbs: { values: [{ category: 'fetch', data: { url: `https://gw.test/x?q=${M}`, note: q } }] } }],
    ['bearer in message', { message: `KEEP Bearer ${M}` }],
    ['jwt in tag value', { tags: { t: 'KEEP eyJabc.eyJdef.ghi-' + M } }],
  ]
  it('scrubEvent_siteBeingWalkedIsNonEmpty', () => {
    expect(sites.length).toBeGreaterThan(10)
  })
  it.each(sites)('scrubEvent_siteIsScrubbed (%s)', (_name, partial) => {
    const out = scrubEvent(as<ErrorEvent>({ type: undefined, ...partial }))
    expect(json(out), 'the site must survive, scrubbed').toContain('KEEP')
    expect(json(out)).not.toContain(M)
  })

  it('scrubEvent_dropsHttpQueryAndFragmentAtAnyDepthAndKeepsSiblings', () => {
    const out = scrubEvent(
      as<ErrorEvent>({
        type: undefined,
        extra: { 'http.query': 'q', list: [{ 'http.fragment': '#f', keep: 'a' }], deep: { x: { 'http.query': 'q', keep: 'b' } } },
        contexts: { trace: { data: { 'http.query': 'q', 'http.fragment': '#f', keep: 'c' } } },
        breadcrumbs: [{ category: 'fetch', data: { 'http.query': 'q', keep: 'd' } }],
      }),
    )
    expect(findKey(out, 'http.query')).toEqual([])
    expect(findKey(out, 'http.fragment')).toEqual([])
    expect(json(out)).toContain('"keep":"a"')
    expect(json(out)).toContain('"keep":"b"')
    expect(json(out)).toContain('"keep":"c"')
    expect(json(out)).toContain('"keep":"d"')
  })

  it('scrubEvent_keepsNonStringScalarsUnchanged', () => {
    const out = scrubEvent(
      as<ErrorEvent>({
        type: undefined,
        tags: { n: 5, b: true },
        extra: { n: 7, b: false, z: null, arr: [1, true, null] },
        contexts: { x: { n: 0 } },
      }),
    )
    expect(out.tags).toEqual({ n: 5, b: true })
    expect(out.extra).toEqual({ n: 7, b: false, z: null, arr: [1, true, null] })
    expect(out.contexts).toEqual({ x: { n: 0 } })
  })

  it('scrubEvent_toleratesMissingParts', () => {
    expect(scrubEvent(as<ErrorEvent>({ type: undefined }))).toEqual({ type: undefined })
    expect(scrubEvent(as<ErrorEvent>({ type: undefined, exception: {} })).exception).toEqual({})
    expect(scrubEvent(as<ErrorEvent>({ type: undefined, request: {} })).request).toEqual({})
    const noUa = scrubEvent(as<ErrorEvent>({ type: undefined, request: { url: 'https://a.test/?q=1', headers: { Referer: 'r', Cookie: 'c' } } }))
    expect(noUa.request?.url).toBe('https://a.test/')
    expect(Object.keys(noUa.request?.headers ?? {})).toEqual([])
  })

  it('scrubEvent_keepsRequestUrlPathAndDropsEveryOtherRequestField', () => {
    const out = scrubEvent(as<ErrorEvent>({ type: undefined, request: { url: 'https://a.test/x#only-fragment', method: 'POST', env: { a: 1 } } }))
    expect(out.request).toEqual({ url: 'https://a.test/x' })
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

describe('scrubTransaction more', () => {
  it('scrubTransaction_scrubsEverySpanAndEveryClsSource', () => {
    const span = (n: number) => ({
      span_id: `s${n}`,
      op: 'http.client',
      description: `GET /x${n}?q=${M}`,
      data: { nested: { url: `/y?q=${M}`, 'http.query': 'q' }, list: [`/z?q=${M}`], n },
    })
    const out = scrubTransaction(
      as<TransactionEvent>({
        type: 'transaction',
        contexts: { trace: { data: { 'cls.source.1': `a[alt="${M}"]`, 'cls.source.2': `b[title="${M}"]`, 'cls.source.3': `c[name="${M}"]` } } },
        spans: [span(1), span(2), span(3)],
      }),
    )
    expect(out.type).toBe('transaction')
    expect(out.spans?.length).toBe(3)
    expect(json(out)).not.toContain(M)
    expect(findKey(out, 'http.query')).toEqual([])
    expect(out.spans?.map((s) => s.description)).toEqual(['GET /x1', 'GET /x2', 'GET /x3'])
    expect(out.spans?.map((s) => s.op)).toEqual(['http.client', 'http.client', 'http.client'])
    expect(out.spans?.map((s) => s.data.n)).toEqual([1, 2, 3])
    expect(json(out)).toContain('[redacted]')
  })

  it('scrubTransaction_toleratesNoSpansNoRequestAndSpansWithoutData', () => {
    expect(scrubTransaction(as<TransactionEvent>({ type: 'transaction' }))).toEqual({ type: 'transaction' })
    const out = scrubTransaction(as<TransactionEvent>({ type: 'transaction', spans: [{ span_id: 's', op: 'x' }] }))
    expect(out.spans).toEqual([{ span_id: 's', op: 'x' }])
  })

  it('scrubTransaction_scrubsMeasurementKeys', () => {
    // D-6: scrubTransaction applies the string rules to `measurements` keys.
    const out = scrubTransaction(
      as<TransactionEvent>({ type: 'transaction', measurements: { lcp: { value: 1, unit: 'millisecond' }, [`x?q=${M}`]: { value: 2, unit: 'millisecond' } } }),
    )
    expect(Object.keys(out.measurements ?? {}), 'measurements must survive').toContain('lcp')
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

describe('scrubSpan more', () => {
  it('scrubSpan_dropsFragmentAndKeepsIdsAndTimestamps', () => {
    const out = scrubSpan(
      as<SpanJSON>({
        trace_id: 't1',
        span_id: 's1',
        parent_span_id: 'p1',
        start_timestamp: 10,
        timestamp: 11,
        origin: 'auto.ui.browser.metrics',
        status: 'ok',
        op: 'ui.interaction.click',
        description: `body > div#root > button#${M}.v2-btn[title="${M}"]`,
        data: { 'http.fragment': `#${M}`, 'http.query': `?q=${M}`, nested: { 'url.full': `/a?q=${M}`, 'http.fragment': '#f' }, list: [`/b?q=${M}`], n: 3 },
      }),
    )
    expect(findKey(out, 'http.fragment')).toEqual([])
    expect(findKey(out, 'http.query')).toEqual([])
    expect(json(out)).not.toContain(M)
    expect(out).toMatchObject({ trace_id: 't1', span_id: 's1', parent_span_id: 'p1', start_timestamp: 10, timestamp: 11, origin: 'auto.ui.browser.metrics', status: 'ok' })
    expect(out.data.n).toBe(3)
    // An element with an #id loses its id, classes and attributes; the tag chain stays.
    expect(out.description).toBe('body > div > button')
  })

  it('scrubSpan_toleratesNoDescriptionAndNoData', () => {
    expect(scrubSpan(as<SpanJSON>({ span_id: 's' }))).toEqual({ span_id: 's' })
    expect(scrubSpan(as<SpanJSON>({ span_id: 's', data: {} })).data).toEqual({})
    expect(scrubSpan(as<SpanJSON>({ span_id: 's', data: null })).data).toBeNull()
  })

  it('scrubSpan_redactsBeforeItStrips', () => {
    // A `?` inside an attribute value must not cut the redaction short.
    const out = scrubSpan(as<SpanJSON>({ span_id: 's', description: `a[title="${M}?y"] > b[alt="p#q"]`, data: {} }))
    expect(out.description).toBe('a[title="[redacted]"] > b[alt="[redacted]"]')
  })

  it('scrubSpan_redactsEachSelectorAttributeName', () => {
    const out = scrubSpan(as<SpanJSON>({ span_id: 's', description: `a[alt="${M}"] > b[title="${M}"] > c[aria-label="${M}"] > d[name="${M}"] > e[type="${M}"]`, data: {} }))
    expect(out.description).toBe('a[alt="[redacted]"] > b[title="[redacted]"] > c[aria-label="[redacted]"] > d[name="[redacted]"] > e[type="[redacted]"]')
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

describe('keepBreadcrumb more', () => {
  it('keepBreadcrumb_stripsEveryUrlKeyAndKeepsTheRest', () => {
    const nav = keepBreadcrumb(as<Breadcrumb>({ category: 'navigation', data: { from: `/a?q=${M}`, to: `/b?persona=${M}#x`, other: 'a?b' } }))
    expect(nav?.data).toEqual({ from: '/a', to: '/b', other: 'a?b' })
    const fetch = keepBreadcrumb(as<Breadcrumb>({ category: 'fetch', data: { url: `https://gw.test/x?q=${M}`, method: 'GET', status_code: 200 } }))
    expect(fetch?.data).toEqual({ url: 'https://gw.test/x', method: 'GET', status_code: 200 })
  })

  it('keepBreadcrumb_toleratesMissingAndNonStringData', () => {
    expect(keepBreadcrumb(as<Breadcrumb>({ category: 'xhr' })), 'xhr without data is kept').not.toBeNull()
    const odd = keepBreadcrumb(as<Breadcrumb>({ category: 'fetch', data: { url: 5, from: null, to: undefined } }))
    expect(odd?.data).toEqual({ url: 5, from: null, to: undefined })
  })

  it('keepBreadcrumb_dropsEveryOtherCategory', () => {
    const categories = ['console', 'ui.click', 'ui.input', 'ui.scroll', 'sentry.event', 'sentry.transaction', 'http', 'Navigation', 'navigation.x', 'fetch ', '', undefined]
    expect(categories.length).toBeGreaterThan(0)
    for (const category of categories) {
      expect(keepBreadcrumb(as<Breadcrumb>({ category, message: M })), JSON.stringify(category)).toBeNull()
    }
  })

  it('keepBreadcrumb_doesNotMutateItsInput', () => {
    const data = { url: `https://gw.test/x?q=${M}` }
    keepBreadcrumb(as<Breadcrumb>({ category: 'fetch', data }))
    expect(data.url).toContain(M)
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

describe('dropEvent more', () => {
  const GH = 'auto.browser.global_handlers'
  const evt = (...types: string[]) =>
    as<ErrorEvent>({ exception: { values: types.map((type) => ({ type: 'Error', value: 'x', mechanism: { type, handled: false } })) } })
  const hint = (originalException: unknown) => as<EventHint>({ originalException })

  it.each(['ApiError', 'AbortError', 'TimeoutError', 'SessionEndedError'])('dropEvent_eachDecidedNameDropsFromBothHandlers (%s)', (name) => {
    for (const handler of ['onerror', 'onunhandledrejection']) {
      expect(dropEvent(evt(`${GH}.${handler}`), hint({ name })), `${name} via ${handler}`).toBe(true)
    }
    expect(dropEvent(evt('auto.function.react.error_boundary'), hint({ name })), `${name} via the boundary`).toBe(false)
    expect(dropEvent(evt('generic'), hint({ name })), `${name} captured by hand`).toBe(false)
  })

  it('dropEvent_keepsOtherNamesAndShapes', () => {
    const keeps: unknown[] = [
      { name: 'Error' },
      { name: 'TypeError' },
      { name: 'RangeError' },
      { name: 'apierror' },
      { name: 42 },
      { name: undefined },
      {},
      'ApiError',
      42,
      null,
      undefined,
    ]
    for (const err of keeps) {
      expect(dropEvent(evt(`${GH}.onunhandledrejection`), hint(err)), String(JSON.stringify(err))).toBe(false)
    }
  })

  it('dropEvent_readsTheFirstExceptionOnly', () => {
    const decided = hint({ name: 'ApiError' })
    expect(dropEvent(evt('generic', `${GH}.onerror`), decided)).toBe(false)
    expect(dropEvent(evt(`${GH}.onerror`, 'generic'), decided)).toBe(true)
    expect(dropEvent(as<ErrorEvent>({ exception: { values: [] } }), decided)).toBe(false)
    expect(dropEvent(as<ErrorEvent>({ exception: { values: [{ type: 'Error', value: 'x' }] } }), decided), 'no mechanism').toBe(false)
  })

  it('dropEvent_matchesTheHandlerPrefixOnly', () => {
    const decided = hint({ name: 'ApiError' })
    expect(dropEvent(evt(GH), decided), 'the bare prefix').toBe(true)
    expect(dropEvent(evt('auto.browser.globalhandlers.onerror'), decided)).toBe(false)
    expect(dropEvent(evt(`x.${GH}`), decided)).toBe(false)
  })

  it('dropEvent_followsMarkReportedForObjectsAndFunctions', () => {
    const frozen = Object.freeze(new TypeError('Failed to fetch'))
    const fn = () => 1
    const unreported = new TypeError('x')
    markReported(frozen)
    markReported(fn)
    expect(dropEvent(evt(`${GH}.onunhandledrejection`), hint(frozen))).toBe(true)
    expect(dropEvent(evt(`${GH}.onunhandledrejection`), hint(fn))).toBe(true)
    expect(dropEvent(evt(`${GH}.onunhandledrejection`), hint(unreported))).toBe(false)
  })

  it('markReported_ignoresPrimitivesAndWasReportedIsFalseForThem', () => {
    for (const v of ['str', 42, true, null, undefined, 10n, Symbol('s')]) {
      expect(() => markReported(v), String(typeof v)).not.toThrow()
      expect(wasReported(v), String(typeof v)).toBe(false)
    }
    const o = {}
    expect(wasReported(o)).toBe(false)
    markReported(o)
    expect(wasReported(o)).toBe(true)
    expect(wasReported({})).toBe(false)
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

  it('apiRoute_handlesShapesBeyondTheTable', () => {
    const uuid = '3f2c9a10-7b1e-4c55-9d3a-0123456789ab'
    const rows: Array<[string, string]> = [
      [`/api/invoice/v1/invoices/${uuid}`, '/api/invoice/v1/invoices/:id'],
      [`/api/invoice/v1/invoices/${uuid}?q=1#f`, '/api/invoice/v1/invoices/:id'],
      ['https://gw.test/api/invoice/v1/invoices/', '/api/invoice/v1/invoices'],
      ['https://gw.test', '/'],
      ['https://gw.test/api/Invoice/v1', '/api/:id/v1'],
      ['https://gw.test/api/a_b/v1x/v/2', '/api/:id/:id/v/:id'],
      ['https://gw.test/api/x/INV-1234', '/api/x/:id'],
      ['https://gw.test/api/-x/--', '/api/:id/:id'],
      ['https://gw.test/api/x/a%2Fb', '/api/x/:id'],
      ['', ':id'],
    ]
    for (const [url, want] of rows) expect(apiRoute(url), url).toBe(want)
    expect(apiRoute('https://u:p@gw.test/api/x?q=1')).toBe('/api/x')
  })
})
