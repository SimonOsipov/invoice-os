// SENTRY-06-04: the app's four non-apiFetch paths report only what Q12 counts, and a
// broken reporter never changes the error a caller sees (D-9, D-24, D-29).
import { ApiError } from '@invoice-os/api-client'
import { captureApiFailure } from '@invoice-os/monitoring/report'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch, NOT_ACTIVE_MEMBER_MESSAGE } from './authedFetch'
import { evidenceBundleUrl, fetchEvidenceBundle, type BundleRequest } from './evidenceBundle'
import { fetchPageImage } from './extractionReview'
import { createImport, previewImport, uploadSourceDocument, type ImportAuth, type UploadPhase, type XhrCtor } from './importApi'
import { SessionEndedError } from './renewal'
import { fetchDocumentBytes } from './sourceDocument'

vi.mock('@invoice-os/monitoring/report', () => ({ captureApiFailure: vi.fn() }))

const reporter = vi.mocked(captureApiFailure)
const BASE = 'https://gw.test'
const REQ: BundleRequest = {
  entityId: '11111111-1111-1111-1111-111111111111',
  from: '2026-01-01T00:00:00.000Z',
  to: '2026-03-31T23:59:59.999Z',
}

const stubFetch = (impl: () => Promise<Response>) => {
  const m = vi.fn(impl)
  vi.stubGlobal('fetch', m)
  return m
}
const respond = (status: number, body = '{"error":"x"}') => stubFetch(async () => new Response(body, { status }))

// Braces: a returned mock would run as vitest's cleanup hook.
beforeEach(() => {
  reporter.mockReset()
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('authedFetch', () => {
  it('authedFetch_seamsUnchangedWithReporting', async () => {
    const onUnauth = vi.fn()
    const onSusp = vi.fn()
    const authed = createAuthedFetch(() => 't', onUnauth, onSusp)
    const url = `${BASE}/api/x`

    respond(401)
    await authed(url).catch(() => undefined)
    respond(403, JSON.stringify({ error: NOT_ACTIVE_MEMBER_MESSAGE }))
    await authed(url).catch(() => undefined)
    expect([onUnauth.mock.calls.length, onSusp.mock.calls.length]).toEqual([1, 1])
    expect(reporter).toHaveBeenCalledTimes(0)

    // On-path control through the same authedFetch.
    respond(503)
    await authed(url).catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(1)
    expect(onUnauth).toHaveBeenCalledTimes(1)
    expect(onSusp).toHaveBeenCalledTimes(1)
  })

  it('authedFetch_aFailureOpensExactlyOneEvent', async () => {
    const authed = createAuthedFetch(() => 't', vi.fn(), vi.fn())
    stubFetch(() => Promise.reject(new TypeError('Failed to fetch')))
    await authed(`${BASE}/api/x`).catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(1)
    reporter.mockClear()
    respond(500)
    await authed(`${BASE}/api/x`).catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(1)
  })
})

// The existing importApi.test.ts FakeXhr is file-private; this is the minimal shape xhrJson drives.
class FakeXhr {
  static last: FakeXhr | undefined
  status = 0
  statusText = ''
  responseText = ''
  upload = { onprogress: null, onload: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null
  constructor() {
    FakeXhr.last = this
  }
  open(): void {}
  setRequestHeader(): void {}
  send(): void {}
  respond(status: number, responseText: string): void {
    this.status = status
    this.responseText = responseText
    this.onload?.()
  }
}

describe('xhrJson', () => {
  const PREVIEW_URL = `${BASE}/api/invoice/v1/imports/preview`
  const file = () => new File(['a,b\n1,2\n'], 'a.csv', { type: 'text/csv' })
  const auth = (over: Partial<ImportAuth> = {}): ImportAuth => ({ getToken: () => 't', onUnauthorized: vi.fn(), ...over })
  const start = (a: ImportAuth) => {
    const settled = previewImport(a, BASE, file(), FakeXhr as unknown as XhrCtor).catch((e: unknown) => e)
    return { xhr: FakeXhr.last!, settled }
  }

  it('xhrJson_reportsCountableOutcomesOnly', async () => {
    const reportedOnce = async (drive: (x: FakeXhr) => void) => {
      reporter.mockClear()
      const { xhr, settled } = start(auth())
      drive(xhr)
      expect(await settled).toBeInstanceOf(ApiError)
      return reporter.mock.calls.map((c) => c[0])
    }

    const bad502 = await reportedOnce((x) => x.respond(502, '{"error":"bad gateway"}'))
    expect(bad502).toHaveLength(1)
    expect(bad502[0]).toMatchObject({ kind: 'http', status: 502, method: 'POST', url: PREVIEW_URL })

    const html = await reportedOnce((x) => x.respond(200, '<html>'))
    expect(html).toHaveLength(1)
    expect(html[0]).toMatchObject({ kind: 'malformed', status: 200 })

    const onerror = await reportedOnce((x) => x.onerror?.())
    expect(onerror).toHaveLength(1)
    expect(onerror[0]).toMatchObject({ kind: 'network', status: null })

    const ontimeout = await reportedOnce((x) => x.ontimeout?.())
    expect(ontimeout).toHaveLength(1)
    expect(ontimeout[0]).toMatchObject({ kind: 'network', status: null })

    // Refused calls: paired above with the countable outcomes through the same transport.
    reporter.mockClear()
    const onUnauthorized = vi.fn()
    const unauth = start(auth({ onUnauthorized }))
    unauth.xhr.respond(401, '{"error":"no"}')
    expect(await unauth.settled).toBeInstanceOf(ApiError)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(reporter).toHaveBeenCalledTimes(0)

    const noToken = start(auth({ getToken: () => Promise.reject(new SessionEndedError()) }))
    expect(await noToken.settled).toMatchObject({ kind: 'network', message: 'session ended' })
    expect(reporter).toHaveBeenCalledTimes(0)
  })

  it('xhrJson_httpStatusBoundaries', async () => {
    const reports = async (status: number) => {
      reporter.mockClear()
      const { xhr, settled } = start(auth())
      xhr.respond(status, '{"error":"x"}')
      await settled
      return reporter.mock.calls.length
    }
    expect([await reports(500), await reports(599)]).toEqual([1, 1])
    expect([await reports(400), await reports(404), await reports(409), await reports(422), await reports(499), await reports(600)]).toEqual([
      0, 0, 0, 0, 0, 0,
    ])
  })

  it('xhrJson_aSuspendedRefusalReportsNothingAndStillFiresItsSeam', async () => {
    const onSuspended = vi.fn()
    const { xhr, settled } = start(auth({ onSuspended }))
    xhr.respond(403, JSON.stringify({ error: NOT_ACTIVE_MEMBER_MESSAGE }))
    expect(await settled).toBeInstanceOf(ApiError)
    expect(onSuspended).toHaveBeenCalledTimes(1)
    expect(reporter).toHaveBeenCalledTimes(0)
  })

  it('xhrJson_oneFailureOpensOneEvent', async () => {
    const { xhr, settled } = start(auth())
    xhr.onerror?.()
    xhr.ontimeout?.()
    xhr.respond(502, '{"error":"late"}')
    expect(await settled).toMatchObject({ kind: 'network' })
    expect(reporter).toHaveBeenCalledTimes(1)
  })

  it('xhrJson_everyWrapperReportsItsOwnUrl', async () => {
    const settle = async (run: (x: XhrCtor) => Promise<unknown>) => {
      reporter.mockClear()
      const settled = run(FakeXhr as unknown as XhrCtor).catch((e: unknown) => e)
      FakeXhr.last!.respond(502, '{"error":"bad gateway"}')
      expect(await settled).toBeInstanceOf(ApiError)
      return reporter.mock.calls.map((c) => c[0])
    }
    const create = await settle((x) =>
      createImport(auth(), BASE, { entityId: 'e', mapping: {}, documentId: 'd', rememberMapping: false } as never, vi.fn(), x),
    )
    const upload = await settle((x) => uploadSourceDocument(auth(), BASE, file(), null, x))
    expect(create).toHaveLength(1)
    expect(create[0]).toMatchObject({ kind: 'http', status: 502, method: 'POST', url: `${BASE}/api/invoice/v1/imports` })
    expect(upload).toHaveLength(1)
    expect(upload[0]).toMatchObject({ kind: 'http', status: 502, method: 'POST', url: `${BASE}/api/submission/v1/documents` })
  })

  it('xhrJson_aShapeFailureAfterA2xxIsNotReported', async () => {
    const { xhr, settled } = start(auth())
    xhr.respond(200, '{}')
    expect(await settled).toMatchObject({ kind: 'malformed', message: 'preview response is missing document_id' })
    expect(reporter).toHaveBeenCalledTimes(0)
  })

  it('xhrJson_aBrokenReporterNeverChangesTheRejectionOrThePhase', async () => {
    reporter.mockImplementation(() => {
      throw new Error('sdk broke')
    })
    const phases: UploadPhase[] = []
    const settled = uploadSourceDocument(auth(), BASE, file(), (p) => phases.push(p), FakeXhr as unknown as XhrCtor).catch((e: unknown) => e)
    FakeXhr.last!.respond(502, '{"error":"bad gateway"}')
    const e = await settled
    expect(e).toMatchObject({ kind: 'http', status: 502, message: 'bad gateway' })
    expect(phases.at(-1)).toEqual({ kind: 'error', error: e })
    expect(reporter).toHaveBeenCalledTimes(1)
  })
})

const HELPERS: Array<{
  name: string
  url: string
  call: (getToken: () => string | null | Promise<string | null>) => Promise<unknown>
}> = [
  {
    name: 'fetchDocumentBytes',
    url: `${BASE}/api/invoice/v1/documents/doc-1`,
    call: (g) => fetchDocumentBytes(g, BASE, 'doc-1', 'pdf', 'a.pdf'),
  },
  { name: 'fetchEvidenceBundle', url: evidenceBundleUrl(BASE, REQ), call: (g) => fetchEvidenceBundle(g, BASE, REQ, 'f.zip') },
  {
    name: 'fetchPageImage',
    url: `${BASE}/api/submission/v1/extractions/job-1/pages/2`,
    call: (g) => fetchPageImage(g, BASE, 'job-1', 2),
  },
]

describe('raw fetch helpers', () => {
  it.each(HELPERS)('rawFetchHelpers_reportAndRethrow: $name', async ({ url, call }) => {
    const token = () => 't'

    respond(500)
    const e500 = await call(token).catch((e: unknown) => e)
    expect(e500).toMatchObject({ kind: 'http', status: 500 })
    expect(reporter).toHaveBeenCalledTimes(1)
    expect(reporter.mock.calls[0][0]).toMatchObject({ kind: 'http', status: 500, method: 'GET', url })

    reporter.mockClear()
    const te = new TypeError('Failed to fetch')
    stubFetch(() => Promise.reject(te))
    await expect(call(token)).rejects.toBe(te)
    expect(reporter).toHaveBeenCalledTimes(1)
    expect(reporter.mock.calls[0][0]).toMatchObject({ kind: 'network', status: null, method: 'GET', url })
    expect(reporter.mock.calls[0][0].error).toBe(te)

    reporter.mockClear()
    respond(404)
    expect(await call(token).catch((e: unknown) => e)).toMatchObject({ kind: 'http', status: 404 })
    expect(reporter).toHaveBeenCalledTimes(0)

    const abort = new DOMException('a', 'AbortError')
    stubFetch(() => Promise.reject(abort))
    await expect(call(token)).rejects.toBe(abort)
    expect(reporter).toHaveBeenCalledTimes(0)

    const never = respond(200)
    await expect(call(() => Promise.reject(new SessionEndedError()))).rejects.toBeInstanceOf(SessionEndedError)
    expect(never).not.toHaveBeenCalled()
    expect(reporter).toHaveBeenCalledTimes(0)
  })
})

describe('raw fetch helpers body read', () => {
  const failingBody = (err: unknown) =>
    stubFetch(
      async () =>
        ({ ok: true, status: 200, statusText: 'OK', headers: new Headers(), arrayBuffer: () => Promise.reject(err), blob: () => Promise.reject(err) }) as unknown as Response,
    )

  it.each(HELPERS)('rawFetchHelpers_aBodyReadFailureReportsAsNetworkAndRethrowsTheSameError: $name', async ({ url, call }) => {
    const te = new TypeError('network error')
    failingBody(te)
    await expect(call(() => 't')).rejects.toBe(te)
    expect(reporter).toHaveBeenCalledTimes(1)
    expect(reporter.mock.calls[0][0]).toMatchObject({ kind: 'network', status: null, method: 'GET', url })
    expect(reporter.mock.calls[0][0].error).toBe(te)

    reporter.mockClear()
    const abort = new DOMException('a', 'AbortError')
    failingBody(abort)
    await expect(call(() => 't')).rejects.toBe(abort)
    expect(reporter).toHaveBeenCalledTimes(0)

    reporter.mockImplementation(() => {
      throw new Error('sdk broke')
    })
    failingBody(te)
    await expect(call(() => 't')).rejects.toBe(te)
  })
})

describe('raw fetch helpers (adversarial)', () => {
  it.each(HELPERS)('rawFetchHelpers_statusBoundariesAndTheRethrownInstance: $name', async ({ call }) => {
    const reports = async (status: number) => {
      reporter.mockClear()
      respond(status)
      const e = await call(() => 't').catch((x: unknown) => x)
      return { n: reporter.mock.calls.length, thrown: e, arg: reporter.mock.calls[0]?.[0] }
    }
    const five = await reports(500)
    expect(five.n).toBe(1)
    expect(five.thrown).toBeInstanceOf(ApiError)
    expect(five.arg.error).toBe(five.thrown)
    expect((await reports(599)).n).toBe(1)
    for (const status of [400, 401, 403, 404, 409, 422, 499]) expect((await reports(status)).n, String(status)).toBe(0)
    reporter.mockClear()
    stubFetch(async () => ({ ok: false, status: 600, statusText: 'x' }) as unknown as Response)
    await call(() => 't').catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(0)
  })

  it.each(HELPERS)('rawFetchHelpers_aTimeoutReportsAndRethrowsTheSameError: $name', async ({ call }) => {
    const timeout = new DOMException('t', 'TimeoutError')
    stubFetch(() => Promise.reject(timeout))
    await expect(call(() => 't')).rejects.toBe(timeout)
    expect(reporter).toHaveBeenCalledTimes(1)
    expect(reporter.mock.calls[0][0]).toMatchObject({ kind: 'network', status: null })
    expect(reporter.mock.calls[0][0].error).toBe(timeout)
  })

  it.each(HELPERS)('rawFetchHelpers_aBrokenReporterNeverChangesTheError: $name', async ({ call }) => {
    reporter.mockImplementation(() => {
      throw new Error('sdk broke')
    })
    respond(500)
    expect(await call(() => 't').catch((e: unknown) => e)).toMatchObject({ kind: 'http', status: 500 })
    const te = new TypeError('Failed to fetch')
    stubFetch(() => Promise.reject(te))
    await expect(call(() => 't')).rejects.toBe(te)
    expect(reporter).toHaveBeenCalledTimes(2)
  })

  it('fetchEvidenceBundle_theCallerSignalDecidesWhatAbortedMeans', async () => {
    const call = (signal: AbortSignal) => fetchEvidenceBundle(() => 't', BASE, REQ, 'f.zip', signal)
    const cancelled = new AbortController()
    cancelled.abort()
    const te = new TypeError('Failed to fetch')

    stubFetch(() => Promise.reject(te))
    await expect(call(cancelled.signal)).rejects.toBe(te)
    respond(500)
    await call(cancelled.signal).catch(() => undefined)
    const custom = new AbortController()
    custom.abort('why')
    stubFetch(() => Promise.reject(te))
    await expect(call(custom.signal)).rejects.toBe(te)
    expect(reporter).toHaveBeenCalledTimes(0)

    // On-path controls: the same failures with a live or timed-out signal report.
    const live = new AbortController()
    await expect(call(live.signal)).rejects.toBe(te)
    respond(500)
    await call(live.signal).catch(() => undefined)
    expect(reporter).toHaveBeenCalledTimes(2)

    reporter.mockClear()
    const timer = AbortSignal.timeout(1)
    await new Promise((r) => setTimeout(r, 25))
    stubFetch(() => Promise.reject(timer.reason))
    await expect(call(timer)).rejects.toBe(timer.reason)
    expect(reporter).toHaveBeenCalledTimes(1)
    expect(reporter.mock.calls[0][0]).toMatchObject({ kind: 'network', status: null })
  })

  it.each(HELPERS)('rawFetchHelpers_aTokenGetterThatThrowsSynchronouslyReportsNothing: $name', async ({ call }) => {
    const never = respond(200)
    await expect(
      call(() => {
        throw new SessionEndedError()
      }),
    ).rejects.toBeInstanceOf(SessionEndedError)
    expect(never).not.toHaveBeenCalled()
    expect(reporter).toHaveBeenCalledTimes(0)
  })
})

describe('reporter failure', () => {
  it('reporterFailure_neverChangesTheCallersError', async () => {
    reporter.mockImplementation(() => {
      throw new Error('sdk broke')
    })
    const te = new TypeError('Failed to fetch')
    stubFetch(() => Promise.reject(te))
    await expect(fetchDocumentBytes(() => 't', BASE, 'doc-1', 'pdf', 'a.pdf')).rejects.toBe(te)
    // On-path control: the reporter was reached, so the swallow above is what is under test.
    expect(reporter).toHaveBeenCalledTimes(1)
  })
})
