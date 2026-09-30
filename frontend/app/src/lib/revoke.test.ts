// revokeSessions: one POST to /auth/sign-out that never rejects and never waits long.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { revokeSessions } from './revoke'

const BASE = 'https://gw.test'
const SIGN_OUT = `${BASE}/auth/sign-out`

interface Call {
  url: string
  method: string
  auth: string | null
  body: unknown
  signal: AbortSignal | undefined
}

// Each row answers once: a Response, or a thrown transport error.
function fakeFetch(reply: () => Promise<Response>) {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init: { method?: string; headers: Headers; body?: string; signal?: AbortSignal }) => {
      calls.push({
        url,
        method: init.method ?? 'GET',
        auth: init.headers.get('Authorization'),
        body: init.body === undefined ? undefined : JSON.parse(init.body),
        signal: init.signal,
      })
      return reply()
    }),
  )
  return calls
}

const json = (status: number, body: unknown) => () =>
  Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('revokeSessions', () => {
  // The gateway's success answer has no body (internal/gateway/signout.go SignOutHandler: w.WriteHeader(http.StatusNoContent)).
  it("a 204 with no body resolves 'revoked'", async () => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const calls = fakeFetch(() => Promise.resolve(new Response(null, { status: 204 })))

    await expect(revokeSessions(BASE, 'R1')).resolves.toBe('revoked')

    expect(calls.map(({ signal: _s, ...c }) => c)).toEqual([{ url: SIGN_OUT, method: 'POST', auth: null, body: { refresh_token: 'R1' } }])
    expect(timeout).toHaveBeenCalledWith(5000)
    expect(calls[0]?.signal, 'the timeout signal reaches fetch').toBe(timeout.mock.results[0]?.value)
  })

  // Bodies copied from internal/gateway/signout.go SignOutHandler's writeError literals.
  const failures: [string, () => Promise<Response>][] = [
    ['401', json(401, { error: 'invalid or expired refresh token' })],
    ['502', json(502, { error: 'sign-out is unavailable' })],
    ['network throw', () => Promise.reject(new TypeError('Failed to fetch'))],
    ['AbortError', () => Promise.reject(new DOMException('The operation was aborted.', 'AbortError'))],
  ]

  it.each(failures)("%s resolves 'failed' and never rejects", async (_name, reply) => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const calls = fakeFetch(reply)

    await expect(revokeSessions(BASE, 'R1')).resolves.toBe('failed')

    expect(calls.map(({ signal: _s, ...c }) => c), 'the request was sent').toEqual([
      { url: SIGN_OUT, method: 'POST', auth: null, body: { refresh_token: 'R1' } },
    ])
    expect(timeout).toHaveBeenCalledWith(5000)
  })
})
