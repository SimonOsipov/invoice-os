// The landing sign-in client.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client/client'

import {
  bounceToStart,
  handoffUrl,
  isUnverified,
  PREFLIGHT_MS,
  readSignInConsole,
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

describe('handoffUrl to a console', () => {
  it('handoffUrl_routesToTheTargetConsole', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x/')
    vi.stubEnv('VITE_OPS_URL', 'https://ops.x/')
    vi.stubEnv('VITE_SUPPORT_URL', 'https://support.x/')
    expect(handoffUrl('abc', 'ops')).toBe('https://ops.x?handoff=abc')
    expect(handoffUrl('abc', 'support')).toBe('https://support.x?handoff=abc')
    expect(handoffUrl('abc')).toBe('https://app.x?handoff=abc')
    expect(handoffUrl('a+b/c=', 'ops')).toBe('https://ops.x?handoff=a%2Bb%2Fc%3D')

    // An unset base is null for its own target only, never a fall-through to another base.
    const unset: [string, string, 'ops' | 'support' | undefined, string | null][] = [
      ['VITE_OPS_URL', '', 'ops', null],
      ['VITE_OPS_URL', '   ', 'ops', null],
      ['VITE_SUPPORT_URL', '', 'support', null],
      ['VITE_APP_URL', '', undefined, null],
      ['VITE_OPS_URL', '', 'support', 'https://support.x?handoff=abc'],
      ['VITE_SUPPORT_URL', '', undefined, 'https://app.x?handoff=abc'],
    ]
    expect(unset.length).toBeGreaterThan(0)
    for (const [name, value, target, want] of unset) {
      vi.stubEnv('VITE_APP_URL', 'https://app.x/')
      vi.stubEnv('VITE_OPS_URL', 'https://ops.x/')
      vi.stubEnv('VITE_SUPPORT_URL', 'https://support.x/')
      vi.stubEnv(name, value)
      expect(handoffUrl('abc', target), `${name}=${JSON.stringify(value)} target=${target}`).toBe(want)
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

describe('readSignInConsole', () => {
  it('readSignInConsole_acceptsExactlyOneKnownValue', () => {
    expect(readSignInConsole(search([['console', 'ops']]))).toBe('ops')
    expect(readSignInConsole(search([['console', 'support']]))).toBe('support')
    expect(readSignInConsole(search([['state', STATE], ['console', 'ops'], ['signin', 'ready']]))).toBe('ops')

    const refused: [string, string][] = [
      ['absent', ''],
      ['absent, other params', '?signin=ready'],
      ['the same value twice', search([['console', 'ops'], ['console', 'ops']])],
      ['two different values', search([['console', 'ops'], ['console', 'support']])],
      ['an unknown value', search([['console', 'app']])],
      ['a URL', search([['console', 'https://x']])],
      ['upper case', search([['console', 'OPS']])],
      ['trailing space', search([['console', 'ops ']])],
      ['leading space', search([['console', ' support']])],
      ['empty value', '?console='],
      ['no value', '?console'],
      ['a list', search([['console', 'ops,support']])],
      ['a differently cased key', search([['Console', 'ops']])],
      ['a prefix', search([['console', 'opsx']])],
    ]
    expect(refused.length).toBeGreaterThan(0)
    for (const [name, s] of refused) {
      expect(readSignInConsole(s), name).toBeNull()
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

describe('startUrl to a console', () => {
  it('startUrl_bouncesThroughTheTargetConsole', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x/')
    vi.stubEnv('VITE_OPS_URL', 'https://ops.x/')
    vi.stubEnv('VITE_SUPPORT_URL', 'https://support.x/')
    expect(startUrl('ops')).toBe('https://ops.x?auth=start')
    expect(startUrl('support')).toBe('https://support.x?auth=start')
    expect(startUrl()).toBe('https://app.x?auth=start')

    vi.stubEnv('VITE_SUPPORT_URL', '')
    expect(startUrl('support')).toBeNull()
    expect(startUrl('ops')).toBe('https://ops.x?auth=start')
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

describe('isUnverified', () => {
  it('isUnverified is true only for an http 403', () => {
    expect(isUnverified(new ApiError('http', 'email address not verified', 403))).toBe(true)
    const others: [string, unknown][] = [
      ['401', new ApiError('http', 'invalid email or password', 401)],
      ['429', new ApiError('http', 'too many requests', 429)],
      ['502', new ApiError('http', 'sign-in is unavailable', 502)],
      ['malformed 403', new ApiError('malformed', 'malformed response body', 403)],
      ['network', new ApiError('network', 'Failed to fetch')],
      ['plain Error', new Error('403')],
      ['null', null],
    ]
    expect(others.length).toBeGreaterThan(0)
    for (const [name, err] of others) expect(isUnverified(err), name).toBe(false)
  })
})

describe('bounceToStart', () => {
  let hrefs: string[]
  beforeEach(() => {
    hrefs = []
    vi.stubGlobal('window', { location: { set href(v: string) { hrefs.push(v) } } })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('bounceToStart_preflightsThenAssignsTheStartUrl', async () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x/')
    vi.stubEnv('VITE_SUPPORT_URL', 'https://support.x/')
    const f = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', f)
    expect(await bounceToStart()).toBe(true)
    expect(f).toHaveBeenCalledTimes(1)
    expect(f).toHaveBeenCalledWith('https://app.x', expect.objectContaining({ mode: 'no-cors', signal: expect.any(AbortSignal) }))
    expect(hrefs).toEqual(['https://app.x?auth=start'])
    expect(await bounceToStart('support')).toBe(true)
    expect(hrefs[1]).toBe('https://support.x?auth=start')
  })

  it('bounceToStart_preflightRejects_assignsNothing', async () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x/')
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    expect(await bounceToStart()).toBe(false)
    expect(hrefs).toEqual([])
  })

  it('bounceToStart_preflightTimesOut_assignsNothing', async () => {
    vi.useFakeTimers()
    vi.stubEnv('VITE_APP_URL', 'https://app.x/')
    vi.stubGlobal('fetch', vi.fn((_u: string, init: RequestInit) => new Promise((_res, rej) => {
      init.signal!.addEventListener('abort', () => rej(new DOMException('aborted', 'AbortError')))
    })))
    let settled: boolean | undefined
    void bounceToStart().then((v) => { settled = v })
    await vi.advanceTimersByTimeAsync(PREFLIGHT_MS - 1)
    expect(settled).toBeUndefined()
    await vi.advanceTimersByTimeAsync(1)
    expect(settled).toBe(false)
    expect(hrefs).toEqual([])
  })

  it('bounceToStart_withoutAUrl_assignsNothing', async () => {
    vi.stubEnv('VITE_APP_URL', '')
    vi.stubEnv('VITE_SUPPORT_URL', '')
    const f = vi.fn()
    vi.stubGlobal('fetch', f)
    expect(await bounceToStart()).toBe(false)
    expect(await bounceToStart('support')).toBe(false)
    expect(f).not.toHaveBeenCalled()
    expect(hrefs).toEqual([])
  })
})
