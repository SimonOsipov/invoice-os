// revokeSessions: one cookie-mode POST to /auth/sign-out that never rejects and never waits long.
// settlePendingSignOut: a sign-out that got no answer is sent again before the app renders.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { SIGN_OUT_PENDING_KEY, revokeSessions, settlePendingSignOut } from './revoke'

const BASE = 'https://gw.test'
const SIGN_OUT = `${BASE}/auth/sign-out`
const COOKIE_MODE = { credentials: 'include', session: 'cookie' }

interface Call {
  url: string
  method: string
  auth: string | null
  body: unknown
  credentials: string | undefined
  session: string | null
  signal: AbortSignal | undefined
}

// Each row answers once: a Response, or a thrown transport error.
function fakeFetch(reply: () => Promise<Response>) {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init: { method?: string; headers: Headers; body?: string; signal?: AbortSignal; credentials?: string }) => {
      calls.push({
        url,
        method: init.method ?? 'GET',
        auth: init.headers.get('Authorization'),
        body: init.body === undefined ? undefined : JSON.parse(init.body),
        credentials: init.credentials,
        session: init.headers.get('X-Auth-Session'),
        signal: init.signal,
      })
      return reply()
    }),
  )
  return calls
}

const json = (status: number, body: unknown) => () =>
  Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const noBody = (status: number) => () => Promise.resolve(new Response(null, { status }))

// Node 22 (CI) has no web storage globals; Node 25's collide with jsdom's.
function memoryStorage(initial: Record<string, string> = {}) {
  const m = new Map(Object.entries(initial))
  const storage = {
    getItem: vi.fn((k: string) => m.get(k) ?? null),
    setItem: vi.fn((k: string, v: string) => void m.set(k, String(v))),
    removeItem: vi.fn((k: string) => void m.delete(k)),
    clear: vi.fn(() => m.clear()),
  }
  vi.stubGlobal('localStorage', storage)
  return storage
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('revokeSessions', () => {
  // The gateway's success answer has no body (internal/gateway/signout.go SignOutHandler: w.WriteHeader(http.StatusNoContent)).
  // Bodies copied from its writeError literals.
  it('revokeSessions_mapsFailure', async () => {
    const rows: [string, () => Promise<Response>, 'revoked' | 'failed' | 'unreached'][] = [
      ['204', noBody(204), 'revoked'],
      ['502', json(502, { error: 'sign-out is unavailable' }), 'failed'],
      ['429', json(429, { error: 'too many requests' }), 'failed'],
      ['401', json(401, { error: 'invalid or expired refresh token' }), 'failed'],
      ['network throw', () => Promise.reject(new TypeError('Failed to fetch')), 'unreached'],
      ['timeout', () => Promise.reject(new DOMException('signal timed out', 'TimeoutError')), 'unreached'],
      ['403 origin not allowed', json(403, { error: 'origin not allowed' }), 'unreached'],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, reply, want] of rows) {
      const timeout = vi.spyOn(AbortSignal, 'timeout')
      const calls = fakeFetch(reply)

      await expect(revokeSessions(BASE), name).resolves.toBe(want)

      expect(calls, `${name}: one request`).toHaveLength(1)
      const { signal, ...sent } = calls[0]!
      expect(sent, name).toEqual({ url: SIGN_OUT, method: 'POST', auth: null, body: undefined, ...COOKIE_MODE })
      expect(timeout, name).toHaveBeenCalledWith(5000)
      expect(signal, `${name}: the timeout signal reaches fetch`).toBe(timeout.mock.results[0]?.value)
      vi.restoreAllMocks()
    }
  })
})

describe('settlePendingSignOut (rows L3, L4)', () => {
  it('settlePendingSignOut_sendsOnceAndClearsOnAnAnswer', async () => {
    const rows: [string, () => Promise<Response>][] = [
      ['204', noBody(204)],
      ['401', json(401, { error: 'invalid or expired refresh token' })],
      ['502', json(502, { error: 'sign-out is unavailable' })],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, reply] of rows) {
      const storage = memoryStorage({ [SIGN_OUT_PENDING_KEY]: '1' })
      const calls = fakeFetch(reply)

      await expect(settlePendingSignOut(BASE, ''), name).resolves.toBeUndefined()

      expect(calls.map(({ signal: _s, ...c }) => c), name).toEqual([
        { url: SIGN_OUT, method: 'POST', auth: null, body: undefined, ...COOKIE_MODE },
      ])
      expect(storage.getItem(SIGN_OUT_PENDING_KEY), `${name}: an answer removes the marker`).toBeNull()
      vi.unstubAllGlobals()
    }
  })

  it('settlePendingSignOut_keepsTheMarkerWithoutAnAnswer', async () => {
    const rows: [string, () => Promise<Response>][] = [
      ['network error', () => Promise.reject(new TypeError('Failed to fetch'))],
      ['403', json(403, { error: 'origin not allowed' })],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, reply] of rows) {
      const storage = memoryStorage({ [SIGN_OUT_PENDING_KEY]: '1' })
      const calls = fakeFetch(reply)

      await expect(settlePendingSignOut(BASE, ''), `${name}: never rejects`).resolves.toBeUndefined()

      expect(calls, name).toHaveLength(1)
      expect(calls[0], name).toMatchObject({ url: SIGN_OUT, method: 'POST', ...COOKIE_MODE })
      expect(storage.getItem(SIGN_OUT_PENDING_KEY), `${name}: no answer keeps the marker`).toBe('1')
      vi.unstubAllGlobals()
    }

    // No marker: resolves at once and sends nothing.
    memoryStorage()
    const none = fakeFetch(noBody(204))
    await expect(settlePendingSignOut(BASE, '')).resolves.toBeUndefined()
    expect(none).toEqual([])
  })

  it('settlePendingSignOut_handoffSendsNothing', async () => {
    const storage = memoryStorage({ [SIGN_OUT_PENDING_KEY]: '1' })
    const calls = fakeFetch(noBody(204))

    await expect(settlePendingSignOut(BASE, '?handoff=abc')).resolves.toBeUndefined()

    expect(calls).toEqual([])
    expect(storage.getItem(SIGN_OUT_PENDING_KEY)).toBe('1')
    // Control: the same marker without the param is sent.
    await settlePendingSignOut(BASE, '?other=1')
    expect(calls).toHaveLength(1)
    expect(storage.getItem(SIGN_OUT_PENDING_KEY)).toBeNull()
  })
})
