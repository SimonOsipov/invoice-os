// Adversarial/edge/negative coverage (M3-06-01 QA pass) — added AFTER C1-C8 went
// green. These target branches and edge cases the happy-path RED specs (C1-C8)
// do not exercise: swallowed inner json-parse failures on the http path, missing
// `error` key in the gateway envelope, non-Error rejects on the network path,
// falsy-but-meaningful token/body values, gatewayBase trimming, and the
// malformed-vs-http branch-order precedence.
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

async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected apiFetch to reject, but it resolved')
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('adversarial: http error, non-JSON error body', () => {
  it('swallows the inner json-parse failure and falls back to statusText, body undefined', async () => {
    mockFetchOnce({
      ok: false,
      status: 500,
      statusText: 'Internal Server Error',
      json: () => Promise.reject(new SyntaxError('Unexpected end of JSON input')),
    })

    const err = await captureRejection(() => apiFetch('/x'))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(500)
    expect(apiErr.message).toBe('Internal Server Error')
    expect(apiErr.body).toBeUndefined()
  })
})

describe('adversarial: http error, JSON body without an `error` key', () => {
  it('falls back to statusText but retains the parsed body when the envelope is {}', async () => {
    mockFetchOnce({
      ok: false,
      status: 400,
      statusText: 'Bad Request',
      json: () => Promise.resolve({}),
    })

    const err = await captureRejection(() => apiFetch('/x'))

    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(400)
    expect(apiErr.message).toBe('Bad Request')
    expect(apiErr.body).toEqual({})
  })

  it('falls back to statusText but retains the parsed body when the envelope has no `error` key', async () => {
    mockFetchOnce({
      ok: false,
      status: 400,
      statusText: 'Bad Request',
      json: () => Promise.resolve({ message: 'x' }),
    })

    const err = await captureRejection(() => apiFetch('/x'))

    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(400)
    expect(apiErr.message).toBe('Bad Request')
    expect(apiErr.body).toEqual({ message: 'x' })
  })
})

describe('adversarial: network branch, non-Error thrown', () => {
  it('does not crash reading .message when fetch rejects with a bare string', async () => {
    mockFetchRejecting('connection reset')

    const err = await captureRejection(() => apiFetch('/x'))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('network')
    expect(apiErr.status).toBeNull()
  })

  it('does not crash reading .message when fetch rejects with a plain object', async () => {
    mockFetchRejecting({})

    const err = await captureRejection(() => apiFetch('/x'))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('network')
    expect(apiErr.status).toBeNull()
  })
})

describe('adversarial: token falsy-but-present', () => {
  it('omits Authorization when token is the empty string (not "Bearer ")', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })

    await apiFetch('/x', { token: '' })

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    const headers = new Headers(init?.headers)
    expect(headers.has('Authorization')).toBe(false)
  })

  it('omits Authorization when token is null', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })

    await apiFetch('/x', { token: null })

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    const headers = new Headers(init?.headers)
    expect(headers.has('Authorization')).toBe(false)
  })
})

describe('adversarial: body falsy-but-defined uses `!== undefined`, not truthiness', () => {
  it('sets Content-Type and serializes body:0', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })

    await apiFetch('/x', { method: 'POST', body: 0 })

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    expect(init?.body).toBe('0')
    const headers = new Headers(init?.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
  })

  it('sets Content-Type and serializes body:false', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })

    await apiFetch('/x', { method: 'POST', body: false })

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    expect(init?.body).toBe('false')
    const headers = new Headers(init?.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
  })

  it('omits Content-Type and sends no body when opts.body is undefined', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })

    await apiFetch('/x')

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    expect(init?.body).toBeUndefined()
    const headers = new Headers(init?.headers)
    expect(headers.has('Content-Type')).toBe(false)
  })
})

describe('adversarial: gatewayBase trailing-slash/whitespace handling', () => {
  it('strips multiple trailing slashes', () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw///')
    expect(gatewayBase()).toBe('https://gw')
  })

  it('trims surrounding whitespace before stripping the trailing slash', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '  https://gw/  ')
    expect(gatewayBase()).toBe('https://gw')
  })

  it('returns null when the value is just a slash', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '/')
    expect(gatewayBase()).toBeNull()
  })

  it('returns null when the value is only whitespace', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '   ')
    expect(gatewayBase()).toBeNull()
  })
})

describe('adversarial: method + signal pass-through', () => {
  it('forwards a custom method and an AbortSignal to the fetch RequestInit', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({}) })
    const controller = new AbortController()

    await apiFetch('/x', { method: 'PUT', signal: controller.signal })

    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    expect(init?.method).toBe('PUT')
    expect(init?.signal).toBe(controller.signal)
  })
})

describe('adversarial: malformed vs http branch-order precedence', () => {
  it('a 2xx response with unparseable JSON is "malformed" (status set)', async () => {
    mockFetchOnce({
      ok: true,
      status: 204,
      json: () => Promise.reject(new SyntaxError('Unexpected end of JSON input')),
    })

    const err = await captureRejection(() => apiFetch('/x'))

    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('malformed')
    expect(apiErr.status).toBe(204)
  })

  it('a non-2xx response with unparseable JSON is "http", NOT "malformed" (status set)', async () => {
    mockFetchOnce({
      ok: false,
      status: 503,
      statusText: 'Service Unavailable',
      json: () => Promise.reject(new SyntaxError('Unexpected end of JSON input')),
    })

    const err = await captureRejection(() => apiFetch('/x'))

    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(503)
  })
})
