// SENTRY-06-04: the app's four non-apiFetch paths report only what Q12 counts, and a
// broken reporter never changes the error a caller sees (D-9, D-24, D-29).
import { ApiError } from '@invoice-os/api-client'
import { captureApiFailure } from '@invoice-os/monitoring/report'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch, NOT_ACTIVE_MEMBER_MESSAGE } from './authedFetch'
import { evidenceBundleUrl, fetchEvidenceBundle, type BundleRequest } from './evidenceBundle'
import { fetchPageImage } from './extractionReview'
import { previewImport, type ImportAuth, type XhrCtor } from './importApi'
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
