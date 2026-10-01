// SENTRY-06-04: which failed API calls reach the reporter (Q12, D-9), and that the
// transport's own error is never changed by reporting (D-29).
/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

import { captureApiFailure } from '@invoice-os/monitoring/report'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { countsAsIssue } from './apiFailure'
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
      'frontend/landing/src/hubspot.ts': 1, // carved out: landing's own form post
      'packages/api-client/src/client.ts': 1,
    })

    const reported = Object.keys(hits).filter((f) => f !== 'frontend/landing/src/hubspot.ts')
    expect(reported.length).toBe(5)
    expect(reported.filter((f) => !code[f].includes('reportApiFailure('))).toEqual([])
  })
})
