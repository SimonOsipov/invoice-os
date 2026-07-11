// RED specs (M3-06-01, C1-C8) — pin the apiFetch/gatewayBase contract before the
// executor implements the bodies in client.ts. Every assertion here is written to
// keep working unchanged once the stub throws are replaced with real logic:
// - the "http"/"network"/"malformed" cases go through captureRejection(), which
//   wraps the call in a thunk so it tolerates BOTH the current synchronous
//   "not implemented" throw and the eventual real async rejection.
// - the header/body-injection cases go through tryCall(), which swallows the
//   pre-implementation throw so the fetch-mock assertions below it still run
//   (and currently fail because the mock was never called — the right RED reason).
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, apiFetch, gatewayBase } from './client'

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

function mockFetchRejecting(err: unknown) {
  const fetchMock = vi.fn().mockRejectedValue(err)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// Calls a (currently throwing) apiFetch and returns the caught error, tolerating
// both a synchronous throw (today's stub) and an eventual async rejection.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected apiFetch to reject, but it resolved')
}

// Calls a (currently throwing) apiFetch and swallows the failure, so assertions
// on the fetch mock below still execute pre-implementation.
async function tryCall(thunk: () => unknown): Promise<void> {
  try {
    await thunk()
  } catch {
    // ignored — pinned by the C1-C4 specs; irrelevant to header/body assertions.
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('apiFetch error envelope', () => {
  it('C1: rejects ApiError{kind:"http"} on non-2xx, carrying the gateway message + status', async () => {
    mockFetchOnce({ ok: false, status: 422, json: () => Promise.resolve({ error: 'invalid TIN' }) })

    const err = await captureRejection(() => apiFetch('/x'))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(422)
    expect(apiErr.message).toContain('invalid TIN')
  })

  it('C2: rejects ApiError{kind:"network"} when fetch itself rejects (transport failure)', async () => {
    mockFetchRejecting(new TypeError('Failed to fetch'))

    const err = await captureRejection(() => apiFetch('/x'))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('network')
    expect(apiErr.status).toBeNull()
  })

  it('C3: rejects ApiError{kind:"malformed"} when res.json() throws on a 2xx response', async () => {
    mockFetchOnce({
      ok: true,
      status: 200,
      json: () => Promise.reject(new SyntaxError('Unexpected end of JSON input')),
    })

    const err = await captureRejection(() => apiFetch('/x'))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('malformed')
    expect(apiErr.status).toBe(200)
  })

  it('C4: resolves the parsed body on a 2xx response', async () => {
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ id: 't1' }) })

    const result = await apiFetch<{ id: string }>('/x')

    expect(result).toEqual({ id: 't1' })
  })
})

describe('apiFetch auth header + body injection', () => {
  it('C5: injects Authorization: Bearer <token> when opts.token is set', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })

    await tryCall(() => apiFetch('/x', { token: 'jwt' }))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    const headers = new Headers(init?.headers)
    expect(headers.get('Authorization')).toBe('Bearer jwt')
  })

  it('C6: omits the Authorization header when no token is given', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })

    await tryCall(() => apiFetch('/x'))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    const headers = new Headers(init?.headers)
    expect(headers.has('Authorization')).toBe(false)
  })

  it('C7: JSON-serializes opts.body and sets Content-Type: application/json', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })

    await tryCall(() => apiFetch('/x', { method: 'POST', body: { a: 1 } }))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    expect(init?.method).toBe('POST')
    expect(init?.body).toBe('{"a":1}')
    const headers = new Headers(init?.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
  })
})

describe('gatewayBase', () => {
  it('C8a: returns the trimmed base (trailing slash stripped) when VITE_GATEWAY_URL is set', () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw/')

    expect(gatewayBase()).toBe('https://gw')
  })

  it('C8b: returns null when VITE_GATEWAY_URL is the empty string', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')

    expect(gatewayBase()).toBeNull()
  })

  it('C8c: returns null when VITE_GATEWAY_URL is unset', () => {
    expect(gatewayBase()).toBeNull()
  })
})
