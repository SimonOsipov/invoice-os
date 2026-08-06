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
  // Optional so the ~40 existing {ok,status,json} call sites keep compiling; a fixture
  // that omits it is the trap T2 relies on (an unconditional res.text() throws there).
  text?: () => Promise<string>
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

// RED specs (task-400, BUG-04-04, Mode A) -- responseType. The UBL route serves XML on
// 2xx but EVERY refusal is the shared {error} JSON envelope (writeError -> writeJSON),
// so only the 2xx branch is responseType-aware. T5/T7 are the oracle for that asymmetry:
// they fail if the text branch is hoisted above `if (!res.ok)` or the error path is made
// text-aware, either of which puts the literal '{"error":"..."}' into ApiError.message.
describe('apiFetch responseType (task-400, BUG-04-04)', () => {
  const UBL_XML = '<?xml version="1.0" encoding="UTF-8"?>\n<Invoice><cbc:ID>INV-1</cbc:ID></Invoice>'
  // Byte-identical to internal/invoice/ubl.go's ublBlockedPrefix + "at least one line
  // item." -- em dash U+2014, single spaces.
  const REASON = 'This invoice cannot be rendered as a UBL document — it is missing at least one line item.'
  // What a real Response.json() does to XML bytes; also a trap detector.
  const notJSON = () => Promise.reject(new SyntaxError('not JSON'))

  it("T1: responseType:'text' resolves the raw body verbatim", async () => {
    mockFetchOnce({ ok: true, status: 200, text: () => Promise.resolve(UBL_XML), json: notJSON })

    const result = await apiFetch<string>('/x', { responseType: 'text' })

    expect(result).toBe(UBL_XML)
  })

  it('T2: no responseType still parses JSON', async () => {
    // No `text` on this mock -- an unconditional res.text() would throw here.
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ a: 1 }) })

    const result = await apiFetch<{ a: number }>('/x')

    expect(result).toEqual({ a: 1 })
  })

  it("T3: an explicit responseType:'json' is identical to omitting it", async () => {
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve({ a: 1 }) })

    const result = await apiFetch<{ a: number }>('/x', { responseType: 'json' })

    expect(result).toEqual({ a: 1 })
  })

  it("T4: a 2xx whose json() rejects still throws ApiError('malformed')", async () => {
    mockFetchOnce({ ok: true, status: 200, json: notJSON })

    const err = await captureRejection(() => apiFetch('/x'))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('malformed')
    expect((err as ApiError).status).toBe(200)
  })

  it("T5: a 409 with responseType:'text' still carries the {error} envelope", async () => {
    mockFetchOnce({
      ok: false,
      status: 409,
      statusText: 'Conflict',
      json: () => Promise.resolve({ error: REASON }),
      // The trap: a responseType-aware error path yields this raw string as the message.
      text: () => Promise.resolve(JSON.stringify({ error: REASON })),
    })

    const err = await captureRejection(() => apiFetch('/x', { responseType: 'text' }))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(409)
    expect(apiErr.message).toBe(REASON)
    expect(apiErr.body).toEqual({ error: REASON })
  })

  it("T6: a 404 with responseType:'text' keeps envelope handling", async () => {
    mockFetchOnce({
      ok: false,
      status: 404,
      statusText: 'Not Found',
      json: () => Promise.resolve({ error: 'not found' }),
      text: () => Promise.resolve(JSON.stringify({ error: 'not found' })),
    })

    const err = await captureRejection(() => apiFetch('/x', { responseType: 'text' }))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(404)
    expect((err as ApiError).message).toBe('not found')
  })

  it('T7: a non-2xx with an unreadable body falls back to statusText', async () => {
    mockFetchOnce({
      ok: false,
      status: 409,
      statusText: 'Conflict',
      json: notJSON,
      text: () => Promise.resolve('<html>gateway page</html>'),
    })

    const err = await captureRejection(() => apiFetch('/x', { responseType: 'text' }))

    expect(err).toBeInstanceOf(ApiError)
    const apiErr = err as ApiError
    expect(apiErr.kind).toBe('http')
    expect(apiErr.status).toBe(409)
    expect(apiErr.message).toBe('Conflict')
    expect(apiErr.body).toBeUndefined()
  })

  it("T8: a network failure with responseType:'text' still throws ApiError('network')", async () => {
    mockFetchRejecting(new TypeError('Failed to fetch'))

    const err = await captureRejection(() => apiFetch('/x', { responseType: 'text' }))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('network')
    expect((err as ApiError).status).toBeNull()
  })

  it('T9: responseType is read, never forwarded to fetch', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, text: () => Promise.resolve(UBL_XML), json: notJSON })

    await tryCall(() => apiFetch('/x', { method: 'GET', responseType: 'text', token: 'jwt' }))

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    const headers = new Headers(init?.headers)
    expect(headers.get('Authorization')).toBe('Bearer jwt')
    expect(init?.body).toBeUndefined()
    expect(init).not.toHaveProperty('responseType')
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
