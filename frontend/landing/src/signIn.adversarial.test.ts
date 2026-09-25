// Adversarial: the landing sign-in client off its happy path.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client/client'

import * as signIn from './signIn'
import { readSignInState, signInErrorMessage, signInWithPassword } from './signIn'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const INCORRECT = 'Email or password is incorrect.'
const UNVERIFIED = 'Verify your email address first. The link is in your inbox.'
const THROTTLED = 'Too many attempts. Try again in a minute.'
const UNAVAILABLE = 'Sign-in is unavailable right now. Try again shortly.'

const jsonResponse = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })

async function caught(p: Promise<unknown>): Promise<unknown> {
  try {
    await p
  } catch (e) {
    return e
  }
  throw new Error('expected a rejection')
}

describe('signInErrorMessage adversarial', () => {
  it('maps a value that is not an ApiError to the unavailable copy', () => {
    expect(signInErrorMessage(new ApiError('http', 'x', 401))).toBe(INCORRECT)
    const others: [string, unknown][] = [
      ['Error', new Error('boom')],
      ['TypeError', new TypeError('Failed to fetch')],
      ['ApiError look-alike 401', { kind: 'http', status: 401 }],
      ['ApiError look-alike 429', { name: 'ApiError', kind: 'http', status: 429 }],
      ['string', '401'],
      ['number', 401],
      ['null', null],
      ['undefined', undefined],
    ]
    expect(others.length).toBeGreaterThan(0)
    for (const [name, err] of others) {
      expect(signInErrorMessage(err), name).toBe(UNAVAILABLE)
    }
  })

  it('does not map 401, 403 or 429 carried by a network or malformed error', () => {
    expect(signInErrorMessage(new ApiError('http', 'x', 401))).toBe(INCORRECT)
    expect(signInErrorMessage(new ApiError('http', 'x', 403))).toBe(UNVERIFIED)
    expect(signInErrorMessage(new ApiError('http', 'x', 429))).toBe(THROTTLED)
    const cases: ApiError[] = []
    for (const kind of ['network', 'malformed'] as const) {
      for (const status of [401, 403, 429]) cases.push(new ApiError(kind, 'x', status))
    }
    expect(cases).toHaveLength(6)
    for (const err of cases) {
      expect(signInErrorMessage(err), `${err.kind} ${err.status}`).toBe(UNAVAILABLE)
    }
  })

  it('maps the neighbours of each mapped status to the unavailable copy', () => {
    const statuses = [400, 402, 404, 409, 422, 428, 430, 503]
    for (const status of statuses) {
      expect(signInErrorMessage(new ApiError('http', 'x', status)), String(status)).toBe(UNAVAILABLE)
    }
  })
})

describe('signInWithPassword adversarial', () => {
  it('throws malformed and makes no fetch when the gateway is unset', async () => {
    for (const gw of ['', '   ']) {
      vi.stubEnv('VITE_GATEWAY_URL', gw)
      const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ code: 'c' }))
      vi.stubGlobal('fetch', fetchMock)
      const err = await caught(signInWithPassword('ada@okafor.ng', 'pw', STATE))
      expect(err, `gateway ${JSON.stringify(gw)}`).toBeInstanceOf(ApiError)
      expect((err as ApiError).kind).toBe('malformed')
      expect(signInErrorMessage(err)).toBe(UNAVAILABLE)
      expect(fetchMock).not.toHaveBeenCalled()
    }

    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ code: 'c' }))
    vi.stubGlobal('fetch', fetchMock)
    await expect(signInWithPassword('ada@okafor.ng', 'pw', STATE)).resolves.toBe('c')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('throws malformed on a 200 without a usable code', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    const bodies: [string, unknown][] = [
      ['empty object', {}],
      ['null body', null],
      ['numeric code', { code: 123 }],
      ['empty code', { code: '' }],
      ['null code', { code: null }],
      ['array code', { code: ['c'] }],
      ['object code', { code: { v: 'c' } }],
      ['token instead of code', { access_token: 'jwt' }],
    ]
    expect(bodies.length).toBeGreaterThan(0)
    for (const [name, body] of bodies) {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(body)))
      const err = await caught(signInWithPassword('ada@okafor.ng', 'pw', STATE))
      expect(err, name).toBeInstanceOf(ApiError)
      expect((err as ApiError).kind, name).toBe('malformed')
      expect(signInErrorMessage(err), name).toBe(UNAVAILABLE)
    }

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ code: 'ok' })))
    await expect(signInWithPassword('ada@okafor.ng', 'pw', STATE)).resolves.toBe('ok')
  })

  it('surfaces each gateway refusal as the matching D12 copy', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    const cases: [number, string][] = [
      [401, INCORRECT],
      [403, UNVERIFIED],
      [429, THROTTLED],
      [400, UNAVAILABLE],
      [502, UNAVAILABLE],
    ]
    for (const [status, want] of cases) {
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ error: 'x' }, status)))
      const err = await caught(signInWithPassword('ada@okafor.ng', 'pw', STATE))
      expect(signInErrorMessage(err), String(status)).toBe(want)
    }

    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const err = await caught(signInWithPassword('ada@okafor.ng', 'pw', STATE))
    expect((err as ApiError).kind).toBe('network')
    expect(signInErrorMessage(err)).toBe(UNAVAILABLE)
  })
})

describe('readSignInState adversarial', () => {
  it('refuses a state that decodes to a non-base64url character', () => {
    expect(readSignInState(`?state=${STATE}`)).toBe(STATE)
    const refused: [string, string][] = [
      ['empty', '?state='],
      ['literal + decodes to a space', `?state=${STATE.slice(0, 42)}+`],
      ['encoded space', `?state=${STATE.slice(0, 42)}%20`],
      ['non-ASCII letter', `?state=${STATE.slice(0, 42)}%C3%A9`],
      ['upper-case key', `?STATE=${STATE}`],
      ['repeated, second empty', `?state=${STATE}&state=`],
    ]
    for (const [name, s] of refused) {
      expect(readSignInState(s), name).toBeNull()
    }
  })
})

describe('signIn module surface', () => {
  it('exports only the D12 and D25 functions and keeps the D12 copy private', () => {
    expect(Object.keys(signIn).sort()).toEqual([
      'handoffUrl',
      'readSignInState',
      'signInConfigured',
      'signInErrorMessage',
      'signInWithPassword',
      'startUrl',
    ])
  })
})
