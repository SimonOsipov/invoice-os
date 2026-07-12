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
import { createEntity, listEntities, type EntityInput } from './portfolio'

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
