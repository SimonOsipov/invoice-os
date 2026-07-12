// RED specs (M3-07-02, U1-U9) — pin the app-side 401 authed-fetch seam (Decision (f)/(g))
// before the executor implements the bodies in authedFetch.ts. U5-U9 mirror
// packages/api-client/src/client.test.ts's vi.stubGlobal('fetch', ...) pattern: `fetch`
// is stubbed, but `apiFetch` itself is the REAL @invoice-os/api-client export once wired
// up, so a simulated 401 produces a genuine ApiError{kind:'http', status:401} — proof at
// the integration level, not a re-implementation of apiFetch's own contract (already
// covered by C1-C8 in client.test.ts).
//
// Every spec below currently fails because createAuthedFetch's stub throws `new
// Error('not implemented')` before ever calling the real apiFetch/fetch — that IS the
// correct RED reason (assertion / not-implemented), not an import/compile/setup error.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { APP_PERSONAS, type Session } from '../auth'
import { createAuthedFetch, isUnauthorized } from './authedFetch'
import { SESSION_KEY, clearSession, loadSession, saveSession } from './session'

interface MockResponse {
  ok: boolean
  status: number
  statusText?: string
  json: () => Promise<unknown>
}

function mockFetchOnce(response: MockResponse) {
  const fetchMock = vi.fn().mockResolvedValue(response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// Calls a (currently throwing) authedFetch and returns the caught error, tolerating
// both a synchronous throw (today's stub) and an eventual async rejection — mirrors
// client.test.ts's captureRejection helper.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected authedFetch to reject, but it resolved')
}

// Calls a (currently throwing) authedFetch and swallows the failure, so assertions on
// the fetch mock / onUnauthorized spy below still execute pre-implementation.
async function tryCall(thunk: () => unknown): Promise<void> {
  try {
    await thunk()
  } catch {
    // ignored — pinned by the rejection-shape specs above; irrelevant to the
    // header-injection / onUnauthorized-not-called assertions below.
  }
}

// In-memory fake used for the U9 end-to-end spec — mirrors session.test.ts's
// createMemoryStorage, a minimal stand-in for the browser Storage interface.
function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
  }
}

function seededSession(): Session {
  return {
    persona: APP_PERSONAS.firm,
    token: 'tok',
    me: {
      tenant: { id: '11111111-1111-1111-1111-111111111111', name: 'Okafor & Partners' },
      user: { id: 'c0000000-0000-0000-0000-000000000001', role: 'authenticated' },
    },
    verified: true,
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('isUnauthorized', () => {
  it('U1: true for ApiError{kind:"http", status:401}', () => {
    expect(isUnauthorized(new ApiError('http', 'x', 401))).toBe(true)
  })

  it('U2: false for ApiError{kind:"http", status:500}', () => {
    expect(isUnauthorized(new ApiError('http', 'x', 500))).toBe(false)
  })

  it('U3: false for ApiError{kind:"network", status:null}', () => {
    expect(isUnauthorized(new ApiError('network', 'x', null))).toBe(false)
  })

  it('U4: false for a non-ApiError value (plain Error, undefined)', () => {
    expect(isUnauthorized(new Error('x'))).toBe(false)
    expect(isUnauthorized(undefined)).toBe(false)
  })
})

describe('authedFetch 401 seam', () => {
  it('U5: on a simulated 401, rejects a real ApiError{status:401} AND invokes onUnauthorized exactly once', async () => {
    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
    const spy = vi.fn()
    const af = createAuthedFetch(() => 'tok', spy)

    const err = await captureRejection(() => af('/x'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('U6: on a 500, rejects but does NOT invoke onUnauthorized', async () => {
    mockFetchOnce({ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) })
    const spy = vi.fn()
    const af = createAuthedFetch(() => 'tok', spy)

    const err = await captureRejection(() => af('/x'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(500)
    expect(spy).not.toHaveBeenCalled()
  })

  it('U7: on a 2xx success, resolves the parsed body and does NOT invoke onUnauthorized', async () => {
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ id: 't1' }) })
    const spy = vi.fn()
    const af = createAuthedFetch(() => 'tok', spy)

    const result = await af<{ id: string }>('/x')

    expect(result).toEqual({ id: 't1' })
    expect(spy).not.toHaveBeenCalled()
  })

  it('U8: injects Authorization: Bearer <getToken()> per-call', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await tryCall(() => af('/x'))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    const headers = new Headers(init?.headers)
    expect(headers.get('Authorization')).toBe('Bearer tok')
  })
})

describe('authedFetch end-to-end: a real 401 clears the persisted session (Decision (g), → AC-3)', () => {
  it('U9: onUnauthorized = () => clearSession() empties a pre-seeded persisted session on a simulated 401', async () => {
    const storage = createMemoryStorage()
    vi.stubGlobal('localStorage', storage)
    saveSession(seededSession())
    expect(loadSession()).not.toBeNull() // sanity: the session really is seeded before the 401

    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
    const af = createAuthedFetch(() => 'tok', () => clearSession())

    await captureRejection(() => af('/x'))

    expect(storage.getItem(SESSION_KEY)).toBeNull()
  })
})

// Adversarial / edge coverage (added post-implementation, QA Mode B).
// AC-3 requires ONLY a real 401 to clear the session — a 403 (still an auth failure,
// but not "the token is dead") must NOT trigger onUnauthorized, and the discriminator
// must be the ApiError `kind`, not merely a numeric `status` field that happens to be
// 401 on a shape that isn't a real HTTP response (e.g. a malformed-body error that
// coincidentally carries the last-seen status).
describe('isUnauthorized: edge cases beyond the happy path', () => {
  it('E1: false for ApiError{kind:"http", status:403} (Forbidden is not a session-clearing signal)', () => {
    expect(isUnauthorized(new ApiError('http', 'x', 403))).toBe(false)
  })

  it("E2: false for ApiError{kind:'malformed', status:401} (kind must be 'http', not just a matching status)", () => {
    expect(isUnauthorized(new ApiError('malformed', 'malformed response body', 401))).toBe(false)
  })
})

describe('authedFetch: edge cases beyond the happy path', () => {
  it('E3: a 403 rejects but does NOT invoke onUnauthorized (only 401 logs the user out)', async () => {
    mockFetchOnce({ ok: false, status: 403, json: () => Promise.resolve({ error: 'forbidden' }) })
    const spy = vi.fn()
    const af = createAuthedFetch(() => 'tok', spy)

    const err = await captureRejection(() => af('/x'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(403)
    expect(spy).not.toHaveBeenCalled()
  })

  it('E4: getToken() returning null issues the request with NO Authorization header (token stays app-side, omitted when absent)', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ id: 't1' }) })
    const af = createAuthedFetch(() => null, vi.fn())

    const result = await af<{ id: string }>('/x')

    expect(result).toEqual({ id: 't1' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    const headers = new Headers(init?.headers)
    expect(headers.has('Authorization')).toBe(false)
  })

  it('E5: opts (method + body) pass through to the underlying fetch alongside the injected token', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ ok: true }) })
    const af = createAuthedFetch(() => 'tok', vi.fn())

    await af('/x', { method: 'POST', body: { a: 1 } })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [calledUrl, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(calledUrl).toBe('/x')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(JSON.stringify({ a: 1 }))
    const headers = new Headers(init.headers)
    expect(headers.get('Authorization')).toBe('Bearer tok')
    expect(headers.get('Content-Type')).toBe('application/json')
  })

  it('E6: onUnauthorized is still invoked exactly once when the 401 response body is unreadable (empty/non-JSON)', async () => {
    mockFetchOnce({
      ok: false,
      status: 401,
      json: () => Promise.reject(new Error('Unexpected end of JSON input')),
    })
    const spy = vi.fn()
    const af = createAuthedFetch(() => 'tok', spy)

    const err = await captureRejection(() => af('/x'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    expect(spy).toHaveBeenCalledTimes(1)
  })
})
