// SENTRY-06-04: which failed API calls reach the reporter (Q12, D-9), and that the
// transport's own error is never changed by reporting (D-29).
/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

import { captureApiFailure } from '@invoice-os/monitoring/report'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { countsAsIssue, reportApiFailure } from './apiFailure'
import { ApiError, apiFetch } from './client'
import { stripComments } from './stripComments'

vi.mock('@invoice-os/monitoring/report', () => ({ captureApiFailure: vi.fn() }))

const reporter = vi.mocked(captureApiFailure)
const URL_ = 'https://gw.test/api/x?q=1'
const NOT_ACTIVE_MEMBER_MESSAGE = 'your membership in this workspace is not active'

function stubFetch(impl: () => Promise<Response>) {
  const m = vi.fn(impl)
  vi.stubGlobal('fetch', m)
  return m
}
const respond = (body: string, status: number) => stubFetch(async () => new Response(body, { status }))
const reject = (err: unknown) => stubFetch(() => Promise.reject(err))

// Braces: a returned mock would run as vitest's cleanup hook.
beforeEach(() => {
  reporter.mockReset()
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('countsAsIssue', () => {
  it('countsAsIssue_followsQ12', () => {
    const timeoutSignal = AbortSignal.abort(new DOMException('t', 'TimeoutError'))
    const cancelSignal = AbortSignal.abort()
    const http = (status: number, body?: unknown) => new ApiError('http', 'm', status, body)
    const rows: Array<[string, unknown, AbortSignal | undefined, boolean]> = [
      ['network, no signal', new ApiError('network', 'm', null), undefined, true],
      ['network, signal aborted by TimeoutError', new ApiError('network', 'm', null), timeoutSignal, true],
      ['http 500', http(500), undefined, true],
      ['http 503', http(503), undefined, true],
      ['http 599', http(599), undefined, true],
      ['malformed', new ApiError('malformed', 'm', 200), undefined, true],
      ['TypeError', new TypeError('Failed to fetch'), undefined, true],
      ['DOMException TimeoutError', new DOMException('t', 'TimeoutError'), undefined, true],
      ['http 400', http(400), undefined, false],
      ['http 401', http(401), undefined, false],
      ['http 403 not active', http(403, { error: NOT_ACTIVE_MEMBER_MESSAGE }), undefined, false],
      ['http 404', http(404), undefined, false],
      ['http 409', http(409), undefined, false],
      ['http 422', http(422), undefined, false],
      ['http 499', http(499), undefined, false],
      ['network, signal aborted by AbortError', new ApiError('network', 'm', null), cancelSignal, false],
      ['AbortError', new DOMException('a', 'AbortError'), undefined, false],
      ['plain Error', new Error('x'), undefined, false],
      // QA: boundaries, precedence of a cancel over every countable kind, odd inputs.
      ['http 600', http(600), undefined, false],
      ['http 0', http(0), undefined, false],
      ['http with null status', new ApiError('http', 'm', null), undefined, false],
      ['http 500, signal timed out', http(500), timeoutSignal, true],
      ['http 500, signal cancelled', http(500), cancelSignal, false],
      ['malformed, signal cancelled', new ApiError('malformed', 'm', 200), cancelSignal, false],
      ['TypeError, signal cancelled', new TypeError('Failed to fetch'), cancelSignal, false],
      ['TimeoutError, signal cancelled', new DOMException('t', 'TimeoutError'), cancelSignal, false],
      ['network, signal aborted with a string reason', new ApiError('network', 'm', null), AbortSignal.abort('why'), false],
      ['network, signal not aborted', new ApiError('network', 'm', null), new AbortController().signal, true],
      ['AbortError, signal timed out', new DOMException('a', 'AbortError'), timeoutSignal, false],
      ['other DOMException', new DOMException('n', 'NetworkError'), undefined, false],
      ['an Error named AbortError', Object.assign(new Error('a'), { name: 'AbortError' }), undefined, false],
      ['a string', 'boom', undefined, false],
      ['null', null, undefined, false],
    ]
    expect(rows.some((r) => r[3]) && rows.some((r) => !r[3])).toBe(true)
    const wrong = rows.filter(([, err, signal, want]) => countsAsIssue(err, signal) !== want).map((r) => r[0])
    expect(wrong).toEqual([])
  })
})

describe('apiFetch reporting', () => {
  it('apiFetch_reports5xxAndRethrowsTheSameError', async () => {
    respond('{"error":"upstream down"}', 503)
    const e = (await apiFetch(URL_).catch((x: unknown) => x)) as ApiError
    expect(e).toBeInstanceOf(ApiError)
    expect([e.kind, e.status, e.message, e.body]).toEqual(['http', 503, 'upstream down', { error: 'upstream down' }])
    expect(reporter).toHaveBeenCalledTimes(1)
    const arg = reporter.mock.calls[0][0]
    expect(arg).toEqual({ kind: 'http', status: 503, method: 'GET', url: URL_, error: e })
    expect(arg.error).toBe(e)
  })

  it('apiFetch_neverReports4xx', async () => {
    const run = async (status: number, body = '{"error":"x"}') => {
      reporter.mockClear()
      respond(body, status)
      await apiFetch(URL_).catch(() => undefined)
      return reporter.mock.calls.length
    }
    // On-path controls first: a reporter that never fires must not pass this row.
    expect([await run(500), await run(599)]).toEqual([1, 1])
    const refused = [
      await run(401),
      await run(403, JSON.stringify({ error: NOT_ACTIVE_MEMBER_MESSAGE })),
      await run(404),
      await run(409),
    ]
    expect(refused).toEqual([0, 0, 0, 0])
    expect([await run(400), await run(422), await run(499)]).toEqual([0, 0, 0])
    // Response cannot carry 600; a stub can.
    reporter.mockClear()
    stubFetch(async () => ({ ok: false, status: 600, statusText: 'x', json: async () => ({}) }) as unknown as Response)
    await apiFetch(URL_).catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(0)
  })

  it('apiFetch_reportsNetworkAndTimeoutButNotCancel', async () => {
    const te = new TypeError('Failed to fetch')
    reject(te)
    await apiFetch(URL_).catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(1)
    expect(reporter.mock.calls[0][0]).toMatchObject({ kind: 'network', status: null, method: 'GET', url: URL_ })

    reporter.mockClear()
    const timedOut = AbortSignal.abort(new DOMException('t', 'TimeoutError'))
    reject(timedOut.reason)
    await apiFetch(URL_, { signal: timedOut }).catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(1)
    expect(reporter.mock.calls[0][0]).toMatchObject({ kind: 'network', status: null })

    reporter.mockClear()
    const ac = new AbortController()
    ac.abort()
    reject(new DOMException('a', 'AbortError'))
    const e = (await apiFetch(URL_, { signal: ac.signal }).catch((x: unknown) => x)) as ApiError
    expect(e).toBeInstanceOf(ApiError)
    expect(e.kind).toBe('network')
    expect(reporter).toHaveBeenCalledTimes(0)
  })

  it('apiFetch_reportsMalformed', async () => {
    respond('not json', 200)
    const e = (await apiFetch(URL_).catch((x: unknown) => x)) as ApiError
    expect(e.kind).toBe('malformed')
    expect(reporter).toHaveBeenCalledTimes(1)
    expect(reporter.mock.calls[0][0]).toMatchObject({ kind: 'malformed', status: 200, method: 'GET', url: URL_ })
  })

  it('reporterFailure_neverChangesTheCallersError', async () => {
    reporter.mockImplementation(() => {
      throw new Error('sdk broke')
    })
    respond('{"error":"upstream down"}', 503)
    const e = (await apiFetch(URL_).catch((x: unknown) => x)) as ApiError
    expect(e).toBeInstanceOf(ApiError)
    expect(e.status).toBe(503)
    // On-path control: the reporter was reached, so the swallow above is what is under test.
    expect(reporter).toHaveBeenCalledTimes(1)
  })
})

describe('apiFetch reporting (adversarial)', () => {
  it('apiFetch_reportsTheMethodAndTheRethrownInstanceOnEveryKind', async () => {
    const instanceOf = async (post: () => void): Promise<[ApiError, Parameters<typeof captureApiFailure>[0]]> => {
      reporter.mockClear()
      post()
      const e = (await apiFetch(URL_, { method: 'POST', body: { a: 1 } }).catch((x: unknown) => x)) as ApiError
      expect(reporter).toHaveBeenCalledTimes(1)
      return [e, reporter.mock.calls[0][0]]
    }
    for (const arrange of [
      () => respond('{"error":"x"}', 502),
      () => respond('not json', 200),
      () => reject(new TypeError('Failed to fetch')),
    ]) {
      const [e, arg] = await instanceOf(arrange)
      expect(e).toBeInstanceOf(ApiError)
      expect(arg.error).toBe(e)
      expect(arg.method).toBe('POST')
      expect(arg.url).toBe(URL_)
    }
  })

  it('apiFetch_aRealTimeoutSignalReportsAndACallerAbortDoesNot', async () => {
    const timer = AbortSignal.timeout(1)
    await new Promise((r) => setTimeout(r, 25))
    expect(timer.aborted).toBe(true)
    expect((timer.reason as DOMException).name).toBe('TimeoutError')
    reject(timer.reason)
    const e = (await apiFetch(URL_, { signal: timer }).catch((x: unknown) => x)) as ApiError
    expect(e.kind).toBe('network')
    expect(reporter).toHaveBeenCalledTimes(1)

    for (const reason of [undefined, 'why', new Error('custom')]) {
      reporter.mockClear()
      const ac = new AbortController()
      ac.abort(reason)
      reject(ac.signal.reason)
      const err = (await apiFetch(URL_, { signal: ac.signal }).catch((x: unknown) => x)) as ApiError
      expect(err).toBeInstanceOf(ApiError)
      expect(err.kind).toBe('network')
      expect(reporter).toHaveBeenCalledTimes(0)
    }
  })

  it('apiFetch_aCancelledSignalSilencesAResponseThatRacedTheAbort', async () => {
    const ac = new AbortController()
    ac.abort()
    respond('{"error":"x"}', 503)
    await apiFetch(URL_, { signal: ac.signal }).catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(0)
    // On-path control: the same response with no signal reports.
    await apiFetch(URL_).catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(1)
  })

  it('reporterFailure_neverChangesTheError_onEveryKind', async () => {
    reporter.mockImplementation(() => {
      throw new Error('sdk broke')
    })
    const outcomes: Array<[string, () => void, string, number | null]> = [
      ['http', () => respond('{"error":"down"}', 503), 'down', 503],
      ['malformed', () => respond('not json', 200), 'malformed response body', 200],
      ['network', () => reject(new TypeError('Failed to fetch')), 'Failed to fetch', null],
    ]
    for (const [kind, arrange, message, status] of outcomes) {
      reporter.mockClear()
      arrange()
      const e = (await apiFetch(URL_).catch((x: unknown) => x)) as ApiError
      expect(e).toBeInstanceOf(ApiError)
      expect([e.kind, e.message, e.status]).toEqual([kind, message, status])
      expect(reporter).toHaveBeenCalledTimes(1)
    }
  })
})

describe('reportApiFailure', () => {
  it('reportApiFailure_mapsTheErrorToTheReporterInput', () => {
    const req = { method: 'PATCH', url: URL_ }
    const cases: Array<[unknown, unknown]> = [
      [new ApiError('http', 'm', 502), { kind: 'http', status: 502 }],
      [new ApiError('malformed', 'm', 200), { kind: 'malformed', status: 200 }],
      [new ApiError('network', 'm', null), { kind: 'network', status: null }],
      [new TypeError('Failed to fetch'), { kind: 'network', status: null }],
      [new DOMException('t', 'TimeoutError'), { kind: 'network', status: null }],
    ]
    for (const [err, want] of cases) {
      reporter.mockClear()
      reportApiFailure(err, req)
      expect(reporter).toHaveBeenCalledTimes(1)
      expect(reporter.mock.calls[0][0]).toMatchObject({ ...(want as object), method: 'PATCH', url: URL_ })
      expect(reporter.mock.calls[0][0].error).toBe(err)
    }
  })

  it('reportApiFailure_staysSilentWhenQ12DoesNotCount', () => {
    const cancelled = AbortSignal.abort()
    reportApiFailure(new ApiError('http', 'm', 503), { method: 'GET', url: URL_ }) // control
    expect(reporter).toHaveBeenCalledTimes(1)
    reporter.mockClear()
    for (const [err, signal] of [
      [new ApiError('http', 'm', 404), undefined],
      [new ApiError('http', 'm', 503), cancelled],
      [new DOMException('a', 'AbortError'), undefined],
      [new Error('x'), undefined],
    ] as Array<[unknown, AbortSignal | undefined]>) {
      reportApiFailure(err, { method: 'GET', url: URL_, signal })
    }
    expect(reporter).toHaveBeenCalledTimes(0)
  })

  it('reportApiFailure_swallowsAReporterThatThrowsAnything', () => {
    for (const thrown of [new Error('sdk broke'), 'a string', undefined, { toString: () => { throw new Error('nested') } }]) {
      reporter.mockReset()
      reporter.mockImplementation(() => {
        throw thrown
      })
      expect(() => reportApiFailure(new ApiError('http', 'm', 503), { method: 'GET', url: URL_ })).not.toThrow()
      expect(reporter).toHaveBeenCalledTimes(1)
    }
  })
})

// Source scan, because no runtime test can enumerate a transport that does not exist yet:
// a new raw fetch or XHR added to a frontend or package would never reach reportApiFailure.
describe('transport inventory', () => {
  const ROOT = resolve(__dirname, '../../..')
  const TRANSPORT_RE = /(^|[^a-z.])fetch\(|window\.fetch|globalThis\.fetch|new XMLHttpRequest|XMLHttpRequest\b|sendBeacon/i
  const SOURCE_FILE = /\.(ts|tsx|js|jsx|mjs)$/
  const TEST_FILE = /\.(test|spec)\./

  function walk(dir: string): string[] {
    return readdirSync(dir, { withFileTypes: true }).flatMap((d) =>
      d.isDirectory() ? (d.name === 'node_modules' ? [] : walk(join(dir, d.name))) : [join(dir, d.name)],
    )
  }
  function srcDirs(group: 'frontend' | 'packages'): string[] {
    return readdirSync(join(ROOT, group)).map((n) => join(ROOT, group, n, 'src'))
  }
  function sourceFiles(): string[] {
    return [...srcDirs('frontend'), ...srcDirs('packages')]
      .flatMap((d) => {
        try {
          return walk(d)
        } catch {
          return []
        }
      })
      .filter((f) => SOURCE_FILE.test(f) && !TEST_FILE.test(f))
  }
  const rel = (f: string) => f.slice(ROOT.length + 1)

  it('transportInventory_everyHitIsReportedOrCarvedOut', () => {
    // The two comment-only mentions (MembersView, RolesView) vanish with comments stripped.
    expect(TRANSPORT_RE.test('const r = await FETCH(u)')).toBe(true)
    expect(TRANSPORT_RE.test('authedFetch(u)')).toBe(false)

    const files = sourceFiles()
    expect(files.length).toBeGreaterThan(200)

    const hits: Record<string, number> = {}
    const code: Record<string, string> = {}
    for (const f of files) {
      const src = stripComments(readFileSync(f, 'utf8'))
      code[rel(f)] = src
      const n = src.split('\n').filter((l) => TRANSPORT_RE.test(l)).length
      if (n > 0) hits[rel(f)] = n
    }
    expect(hits).toEqual({
      'frontend/app/src/lib/evidenceBundle.ts': 1,
      'frontend/app/src/lib/extractionReview.ts': 1,
      'frontend/app/src/lib/importApi.ts': 4, // XhrCtor type + three xhrCtor defaults
      'frontend/app/src/lib/sourceDocument.ts': 1,
      'frontend/landing/src/hubspot.ts': 1,
      'frontend/landing/src/signIn.ts': 1,
      'packages/api-client/src/client.ts': 1,
    })

    const reported = Object.keys(hits)
    expect(reported.length).toBe(7)
    expect(reported.filter((f) => !code[f].includes('reportApiFailure('))).toEqual([])
  })
})
