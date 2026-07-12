// RED specs (M3-08-03, task-58, A1-A6) — pin `makeAuthedFetch` (src/lib/authedFetch.ts)
// as the AC-5 live-caller integration: a real portfolio caller (`listEntities` /
// `createEntity`, the M3-08-01 helpers) driven through the app-side 401 seam, proving a
// 401 invokes `onSignOut` exactly once (closing the M3-07 carry-over — M3-07's U9 only
// ever wired `onUnauthorized` directly, with no in-app caller).
//
// Every spec below currently fails because `makeAuthedFetch`'s stub throws `new
// Error('not implemented')` before ever constructing an authedFetch closure — that IS
// the correct RED reason (assertion / not-implemented), not an import/compile error.
// `listEntities`/`createEntity` themselves are already implemented (M3-08-01, task-56)
// and are used here UNMODIFIED as the live callers — they are not what's under test.
//
// Mirrors authedFetch.test.ts's / portfolio.test.ts's `vi.stubGlobal('fetch', ...)`
// pattern: `fetch` is stubbed, but `makeAuthedFetch` → `createAuthedFetch` → `apiFetch`
// and `listEntities`/`createEntity` are all REAL, so a simulated 401/200/500 produces a
// genuine `ApiError` through the full, real call chain — proof at the integration level.
import { describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { APP_PERSONAS, type Session } from '../auth'
import { makeAuthedFetch } from './authedFetch'
import { createEntity, listEntities, updateEntity, type EntityInput } from './portfolio'

const base = 'https://gw'

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

// Simulates a transport failure (fetch itself rejects, not a non-2xx response) —
// mirrors client.test.ts's mockFetchRejecting, used by B3 below.
function mockFetchRejecting(err: unknown) {
  const fetchMock = vi.fn().mockRejectedValue(err)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

// Calls a thunk and returns the caught rejection/throw — tolerates both a synchronous
// throw (today's makeAuthedFetch stub) and an eventual async rejection (the real
// implementation) — mirrors authedFetch.test.ts's captureRejection helper.
async function captureRejection(thunk: () => unknown): Promise<unknown> {
  try {
    await thunk()
  } catch (err) {
    return err
  }
  throw new Error('expected the call to reject, but it resolved')
}

// A full, valid Session (mirrors authedFetch.test.ts's seededSession()), parametrized by
// token so A5/A6 can vary just that field without an `as Session` cast.
function buildSession(token: string | null): Session {
  return {
    persona: APP_PERSONAS.firm,
    token,
    me: {
      tenant: { id: '11111111-1111-1111-1111-111111111111', name: 'Okafor & Partners' },
      user: { id: 'c0000000-0000-0000-0000-000000000001', role: 'authenticated' },
    },
    verified: true,
  }
}

const emptyListBody = { entities: [], pagination: { limit: 200, offset: 0, total: 0 } }

describe('makeAuthedFetch: AC-5 live-caller 401 integration', () => {
  it('A1: a 401 on a live listEntities call rejects ApiError{status:401} AND calls onSignOut exactly once', async () => {
    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
    const signOutSpy = vi.fn()
    const af = makeAuthedFetch(buildSession('tok'), signOutSpy)

    const err = await captureRejection(() => listEntities(af, base))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    expect(signOutSpy).toHaveBeenCalledTimes(1)
  })

  it('A2: a 200 on a live listEntities call resolves and does NOT call onSignOut', async () => {
    mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(emptyListBody) })
    const signOutSpy = vi.fn()
    const af = makeAuthedFetch(buildSession('tok'), signOutSpy)

    const result = await listEntities(af, base)

    expect(result).toEqual([])
    expect(signOutSpy).not.toHaveBeenCalled()
  })

  it('A3: a 401 on a live createEntity call fully clears an app-side session store (closes the M3-07 carry-over)', async () => {
    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
    const store: { session: { token: string } | null } = { session: { token: 'tok' } }
    const onSignOut = () => {
      store.session = null
    }
    const af = makeAuthedFetch(buildSession('tok'), onSignOut)
    const input: EntityInput = { name: 'Acme', tin: '0000000000' }

    await captureRejection(() => createEntity(af, base, input))

    expect(store.session).toBeNull()
  })

  it('A4: a 500 on a live listEntities call rejects ApiError{status:500} and does NOT call onSignOut (only 401 logs out)', async () => {
    mockFetchOnce({ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) })
    const signOutSpy = vi.fn()
    const af = makeAuthedFetch(buildSession('tok'), signOutSpy)

    const err = await captureRejection(() => listEntities(af, base))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(500)
    expect(signOutSpy).not.toHaveBeenCalled()
  })
})

describe('makeAuthedFetch: token-read semantics', () => {
  it('A5: reads session.token at CALL time, not construction time (closure, not a captured snapshot)', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(emptyListBody) })
    const session = buildSession('old')
    const af = makeAuthedFetch(session, vi.fn())
    session.token = 'new'

    await listEntities(af, base)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    const headers = new Headers(init?.headers)
    expect(headers.get('Authorization')).toBe('Bearer new')
  })

  it('A6: a null session.token (no-gateway showcase) issues the request with NO Authorization header', async () => {
    const fetchMock = mockFetchOnce({ ok: true, status: 200, json: () => Promise.resolve(emptyListBody) })
    const signOutSpy = vi.fn()
    const af = makeAuthedFetch(buildSession(null), signOutSpy)

    const result = await listEntities(af, base)

    expect(result).toEqual([])
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined
    const headers = new Headers(init?.headers)
    expect(headers.has('Authorization')).toBe(false)
    expect(signOutSpy).not.toHaveBeenCalled()
  })
})

// Adversarial/edge coverage (QA, M3-08-03) — extends A1-A6 above, which only exercised
// list/create at 200/401/500. These specs close gaps A1-A6 left open: the PATCH
// (update) caller, a non-401 4xx (403) that must NOT trigger the seam, a transport-level
// (network) ApiError that must also NOT trigger the seam, closure independence across
// multiple makeAuthedFetch instances, and the fire-then-rethrow ORDER (onSignOut must
// run before the rejection reaches the caller, and must not be swallowed).
describe('makeAuthedFetch: adversarial coverage (B1-B5)', () => {
  it('B1: a 401 on a live updateEntity (PATCH) call rejects ApiError{status:401} AND calls onSignOut exactly once', async () => {
    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
    const signOutSpy = vi.fn()
    const af = makeAuthedFetch(buildSession('tok'), signOutSpy)

    const err = await captureRejection(() => updateEntity(af, base, 'e1', { name: 'New' }))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(401)
    expect(signOutSpy).toHaveBeenCalledTimes(1)
  })

  it('B2: a 403 on a live listEntities call rejects ApiError{status:403} and does NOT call onSignOut (only 401 logs out)', async () => {
    mockFetchOnce({ ok: false, status: 403, json: () => Promise.resolve({ error: 'forbidden' }) })
    const signOutSpy = vi.fn()
    const af = makeAuthedFetch(buildSession('tok'), signOutSpy)

    const err = await captureRejection(() => listEntities(af, base))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(403)
    expect(signOutSpy).not.toHaveBeenCalled()
  })

  it('B3: a transport failure (fetch itself rejects) produces ApiError{kind:"network"}, does NOT call onSignOut, and the rejection still propagates to the caller', async () => {
    mockFetchRejecting(new TypeError('Failed to fetch'))
    const signOutSpy = vi.fn()
    const af = makeAuthedFetch(buildSession('tok'), signOutSpy)

    const err = await captureRejection(() => listEntities(af, base))

    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('network')
    expect((err as ApiError).status).toBeNull()
    expect(signOutSpy).not.toHaveBeenCalled()
  })

  it('B4: two makeAuthedFetch instances built from the same session are independent closures — no shared mutable state, each fires only its own onSignOut on its own 401', async () => {
    const session = buildSession('tok')
    const signOutSpyA = vi.fn()
    const signOutSpyB = vi.fn()
    const afA = makeAuthedFetch(session, signOutSpyA)
    const afB = makeAuthedFetch(session, signOutSpyB)

    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
    await captureRejection(() => listEntities(afA, base))

    expect(signOutSpyA).toHaveBeenCalledTimes(1)
    expect(signOutSpyB).not.toHaveBeenCalled()

    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
    await captureRejection(() => listEntities(afB, base))

    expect(signOutSpyB).toHaveBeenCalledTimes(1)
    // afA's spy must be untouched by afB's later 401 — proves no shared state (e.g. a
    // module-level "already signed out" flag) between the two closures.
    expect(signOutSpyA).toHaveBeenCalledTimes(1)
  })

  it('B5: onSignOut fires BEFORE the rejection reaches the caller, and the error is not swallowed (the caller still observes the 401)', async () => {
    mockFetchOnce({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) })
    const order: string[] = []
    const onSignOut = () => {
      order.push('signOut')
    }
    const af = makeAuthedFetch(buildSession('tok'), onSignOut)

    let caught: unknown
    try {
      await listEntities(af, base)
      order.push('resolved-should-not-happen')
    } catch (err) {
      order.push('caller-caught')
      caught = err
    }

    expect(order).toEqual(['signOut', 'caller-caught'])
    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).status).toBe(401)
  })
})
