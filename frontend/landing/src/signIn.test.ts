// The landing sign-in client.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client/client'

import {
  handoffUrl,
  readSignInState,
  signInConfigured,
  signInErrorMessage,
  signInWithPassword,
  startUrl,
} from './signIn'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

// 43 base64url characters, including both non-alphanumeric members.
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'

const search = (entries: [string, string][]) => '?' + new URLSearchParams(entries).toString()

describe('handoffUrl', () => {
  it('handoffUrl builds the app-root hand-off', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x/')
    expect(handoffUrl('abc')).toBe('https://app.x?handoff=abc')
    expect(handoffUrl('a+b/c=')).toBe('https://app.x?handoff=a%2Bb%2Fc%3D')
  })

  it('handoffUrl is null without an app URL', () => {
    for (const v of ['', '   ']) {
      vi.stubEnv('VITE_APP_URL', v)
      expect(handoffUrl('abc'), `VITE_APP_URL ${JSON.stringify(v)}`).toBeNull()
    }
  })
})

describe('signInConfigured', () => {
  it('signInConfigured needs both bases', () => {
    const cases: [string, string, boolean][] = [
      ['https://gw.x', 'https://app.x', true],
      ['https://gw.x', '', false],
      ['', 'https://app.x', false],
      ['', '', false],
    ]
    expect(cases.length).toBe(4)
    const got = cases.map(([gw, app]) => {
      vi.stubEnv('VITE_GATEWAY_URL', gw)
      vi.stubEnv('VITE_APP_URL', app)
      return signInConfigured()
    })
    expect(got).toEqual(cases.map((c) => c[2]))
  })
})

describe('signInWithPassword', () => {
  it('signInWithPassword posts the credentials and returns the code', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: 'the-code' }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    const code = await signInWithPassword('ada@okafor.ng', ' s3cret ', STATE)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw.x/auth/sign-in')
    expect(init.method).toBe('POST')
    const headers = new Headers(init.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.has('Authorization')).toBe(false)
    const body = JSON.parse(init.body as string) as Record<string, unknown>
    expect(Object.keys(body).sort()).toEqual(['email', 'password', 'state'])
    expect(body).toStrictEqual({ email: 'ada@okafor.ng', password: ' s3cret ', state: STATE })
    expect(code).toBe('the-code')
  })
})

describe('readSignInState', () => {
  it('readSignInState accepts only a 43-character base64url state', () => {
    expect(STATE).toHaveLength(43)
    expect(readSignInState(search([['state', STATE]]))).toBe(STATE)
    expect(readSignInState(search([['state', STATE], ['signin', 'ready']]))).toBe(STATE)

    const refused: [string, string][] = [
      ['42 characters', search([['state', STATE.slice(0, 42)]])],
      ['44 characters', search([['state', STATE + 'a']])],
      ['a +', search([['state', STATE.slice(0, 42) + '+']])],
      ['a /', search([['state', STATE.slice(0, 42) + '/']])],
      ['an =', search([['state', STATE.slice(0, 42) + '=']])],
      ['absent', ''],
      ['absent, other params', '?signin=ready'],
      ['repeated', search([['state', STATE], ['state', STATE]])],
    ]
    expect(refused.length).toBeGreaterThan(0)
    for (const [name, s] of refused) {
      expect(readSignInState(s), name).toBeNull()
    }
  })
})

describe('startUrl', () => {
  it('startUrl builds the app start bounce', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x/')
    expect(startUrl()).toBe('https://app.x?auth=start')
    vi.stubEnv('VITE_APP_URL', '')
    expect(startUrl()).toBeNull()
  })
})

describe('signInErrorMessage', () => {
  it('signInErrorMessage maps each status', () => {
    // Copy, verbatim.
    const INCORRECT = 'Email or password is incorrect.' // 401
    const UNVERIFIED = 'Verify your email address first. The link is in your inbox.' // 403
    const THROTTLED = 'Too many attempts. Try again in a minute.' // 429
    const UNAVAILABLE = 'Sign-in is unavailable right now. Try again shortly.' // anything else

    const cases: [string, unknown, string][] = [
      ['401', new ApiError('http', 'invalid email or password', 401), INCORRECT],
      ['403', new ApiError('http', 'email address not verified', 403), UNVERIFIED],
      ['429', new ApiError('http', 'too many requests', 429), THROTTLED],
      ['500', new ApiError('http', 'boom', 500), UNAVAILABLE],
      ['502', new ApiError('http', 'sign-in is unavailable', 502), UNAVAILABLE],
      ['network', new ApiError('network', 'Failed to fetch'), UNAVAILABLE],
      ['malformed', new ApiError('malformed', 'malformed response body', 200), UNAVAILABLE],
    ]
    expect(cases.length).toBeGreaterThan(0)
    for (const [name, err, want] of cases) {
      expect(signInErrorMessage(err), name).toBe(want)
    }
  })
})
