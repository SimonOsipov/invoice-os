// The landing registration client. register.ts is new, so each test goes red on the missing module first.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client/client'

import { MARKETING_CONSENT_TEXT } from './components/MarketingConsent'
import {
  FREE_MAIL_REFUSED,
  registerAccount,
  registerOutcome,
  registrationOpen,
  resendVerification,
  validateRegisterForm,
  type RegisterValues,
} from './register'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const VALID: RegisterValues = {
  email: 'ada@okafor.ng',
  password: 'pw-123456',
  displayName: 'Ada Okafor',
  workspaceName: 'Okafor & Partners',
  kind: 'firm',
  marketing: false,
}

const UNAVAILABLE = 'Registration is unavailable right now. Try again shortly.'

const httpError = (status: number, message: string) => new ApiError('http', message, status, { error: message })

describe('registrationOpen', () => {
  it('registrationOpen needs the flag and a configured sign-in', () => {
    const cases: [string, string | undefined, string | undefined, string | undefined, boolean][] = [
      ['flag true, both URLs', 'true', 'https://gw.x', 'https://app.x', true],
      ['flag TRUE', 'TRUE', 'https://gw.x', 'https://app.x', false],
      ['flag unset', undefined, 'https://gw.x', 'https://app.x', false],
      ['flag false', 'false', 'https://gw.x', 'https://app.x', false],
      ['flag true without the gateway URL', 'true', '', 'https://app.x', false],
      ['flag true without the app URL', 'true', 'https://gw.x', '', false],
      ['flag true with a blank app URL', 'true', 'https://gw.x', '   ', false],
      ['flag with a leading space', ' true', 'https://gw.x', 'https://app.x', false],
      ['flag with a trailing space', 'true ', 'https://gw.x', 'https://app.x', false],
      ['flag 1', '1', 'https://gw.x', 'https://app.x', false],
      ['flag yes', 'yes', 'https://gw.x', 'https://app.x', false],
      ['flag empty', '', 'https://gw.x', 'https://app.x', false],
    ]
    expect(cases.filter((c) => c[4])).toHaveLength(1)
    for (const [name, flag, gw, app, want] of cases) {
      vi.stubEnv('VITE_REGISTRATION_OPEN', flag)
      vi.stubEnv('VITE_GATEWAY_URL', gw)
      vi.stubEnv('VITE_APP_URL', app)
      expect(registrationOpen(), name).toBe(want)
    }
  })
})

describe('validateRegisterForm', () => {
  it('validateRegisterForm names every missing field', () => {
    expect(validateRegisterForm(VALID), 'control: a complete form has no errors').toEqual({})

    const empty = validateRegisterForm({ email: '', password: '', displayName: '', workspaceName: '', kind: '', marketing: false })
    expect(empty).toEqual({
      email: 'Enter your work email.',
      password: 'Choose a password.',
      displayName: 'Enter your name.',
      workspaceName: 'Enter your company or workspace name.',
      kind: 'Choose how this workspace files invoices.',
    })

    expect(validateRegisterForm({ ...VALID, kind: '' })).toEqual({ kind: 'Choose how this workspace files invoices.' })
    expect(validateRegisterForm({ ...VALID, email: 'a b@c.d' })).toEqual({ email: 'Enter a valid work email address.' })
    expect(validateRegisterForm({ ...VALID, email: 'ada@okafor' })).toEqual({ email: 'Enter a valid work email address.' })
    expect(validateRegisterForm({ ...VALID, email: '   ' }), 'blank is missing, not invalid').toEqual({ email: 'Enter your work email.' })
    expect(validateRegisterForm({ ...VALID, email: '  ADA@Okafor.NG ' }), 'trimmed, case kept').toEqual({})
    expect(validateRegisterForm({ ...VALID, displayName: '' })).toEqual({ displayName: 'Enter your name.' })
    expect(validateRegisterForm({ ...VALID, workspaceName: '' })).toEqual({ workspaceName: 'Enter your company or workspace name.' })
  })

  it('validateRegisterForm bounds the names in code points', () => {
    const emoji200 = '😀'.repeat(200)
    expect(emoji200.length, 'control: 200 emoji are 400 UTF-16 units').toBe(400)

    const accepted = [emoji200, ` ${emoji200} `, 'a'.repeat(200), ' Ada ']
    const refused = ['😀'.repeat(201), 'a'.repeat(201), '   ', '']
    for (const name of accepted) {
      const errors = validateRegisterForm({ ...VALID, displayName: name, workspaceName: name })
      expect(errors, `accepted ${JSON.stringify(name.slice(0, 8))}…`).toEqual({})
    }
    for (const name of refused) {
      const errors = validateRegisterForm({ ...VALID, displayName: name, workspaceName: name })
      expect(Object.keys(errors).sort(), `refused ${JSON.stringify(name.slice(0, 8))}…`).toEqual(['displayName', 'workspaceName'])
    }

    const tooLong = 'Use 200 characters or fewer.'
    expect(validateRegisterForm({ ...VALID, displayName: 'a'.repeat(201) })).toEqual({ displayName: tooLong })
    expect(validateRegisterForm({ ...VALID, workspaceName: 'a'.repeat(201) })).toEqual({ workspaceName: tooLong })

    const spaced: RegisterValues = { ...VALID, password: ' pw ' }
    expect(validateRegisterForm(spaced), 'the password is never trimmed').toEqual({})
    expect(spaced.password).toBe(' pw ')
    expect(validateRegisterForm({ ...VALID, password: '   ' }), 'a blank password is still a password').toEqual({})
  })
})

describe('registerAccount', () => {
  it('registerAccount posts the snake_case body', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ status: 'verification_pending' }), { status: 202 })))
    vi.stubGlobal('fetch', fetchMock)

    const cases: [RegisterValues, Record<string, unknown>][] = [
      [
        { email: ' Ada@Corp.example ', password: ' pw ', displayName: '  Ada Okafor ', workspaceName: ' Okafor & Partners  ', kind: 'in_house', marketing: false },
        { email: 'Ada@Corp.example', password: ' pw ', display_name: 'Ada Okafor', workspace_name: 'Okafor & Partners', kind: 'in_house' },
      ],
      [
        { email: 'b@corp.example', password: '   ', displayName: 'B', workspaceName: 'W', kind: 'firm', marketing: false },
        { email: 'b@corp.example', password: '   ', display_name: 'B', workspace_name: 'W', kind: 'firm' },
      ],
      [
        { email: 'c@corp.example', password: 'pw', displayName: 'C\u0000', workspaceName: 'W\u0000x', kind: 'firm', marketing: false },
        { email: 'c@corp.example', password: 'pw', display_name: 'C\u0000', workspace_name: 'W\u0000x', kind: 'firm' },
      ],
    ]
    for (const [values] of cases) await registerAccount(values)
    expect(fetchMock).toHaveBeenCalledTimes(cases.length)
    cases.forEach(([, want], i) => {
      const [url, init] = fetchMock.mock.calls[i] as [string, RequestInit]
      expect(url).toBe('https://gw.x/auth/register')
      expect(init.method).toBe('POST')
      const headers = new Headers(init.headers)
      expect(headers.get('Content-Type')).toBe('application/json')
      expect(headers.has('Authorization')).toBe(false)
      const body = JSON.parse(init.body as string) as Record<string, unknown>
      expect(Object.keys(body).sort()).toEqual(['display_name', 'email', 'kind', 'password', 'workspace_name'])
      expect(body).toStrictEqual(want)
    })
  })

  it('registerAccount rejects when the gateway is not configured and sends nothing', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await expect(registerAccount(VALID)).rejects.toThrow()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

// The story's [copy] sentence, pinned once: a wording change is a deliberate edit here and in the story.
const STORY_MARKETING_TEXT = 'Allow marketing communications: ASComply Africa may email me product news and offers. I can unsubscribe at any time.'

describe('the marketing consent sentence', () => {
  it('MARKETING_CONSENT_TEXT is the story sentence', () => {
    expect(MARKETING_CONSENT_TEXT).toBe(STORY_MARKETING_TEXT)
  })
})

describe('registerAccount marketing consent', () => {
  const post = async (v: RegisterValues) => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ status: 'verification_pending' }), { status: 202 })))
    vi.stubGlobal('fetch', fetchMock)
    await registerAccount(v)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    return JSON.parse((fetchMock.mock.calls[0] as [string, RequestInit])[1].body as string) as Record<string, unknown>
  }

  it('registerAccount sends the marketing sentence only when ticked', async () => {
    const ticked = await post({ ...VALID, marketing: true })
    expect(ticked).toHaveProperty('marketing_consent_text')
    expect(ticked.marketing_consent_text).toBe(MARKETING_CONSENT_TEXT)
    expect(Object.keys(ticked).sort(), 'ticked adds that one key').toEqual(['display_name', 'email', 'kind', 'marketing_consent_text', 'password', 'workspace_name'])
    expect(ticked).toStrictEqual({
      email: VALID.email,
      password: VALID.password,
      display_name: VALID.displayName,
      workspace_name: VALID.workspaceName,
      kind: 'firm',
      marketing_consent_text: MARKETING_CONSENT_TEXT,
    })

    const padded = await post({ email: ' d@corp.example ', password: 'pw', displayName: ' D ', workspaceName: ' W ', kind: 'in_house', marketing: true })
    expect(padded, 'a ticked body still trims every other field').toStrictEqual({
      email: 'd@corp.example',
      password: 'pw',
      display_name: 'D',
      workspace_name: 'W',
      kind: 'in_house',
      marketing_consent_text: MARKETING_CONSENT_TEXT,
    })

    const unticked = await post({ ...VALID, marketing: false })
    expect(Object.keys(unticked).sort(), 'control: the five-key body is unchanged').toEqual(['display_name', 'email', 'kind', 'password', 'workspace_name'])
    expect('marketing_consent_text' in unticked, 'no key at all, not "" / null / false').toBe(false)
    expect(JSON.stringify(unticked)).not.toContain('marketing')
  })
})

describe('registerOutcome', () => {
  it('registerOutcome maps every answer', () => {
    expect(FREE_MAIL_REFUSED.length, 'control: the literal is not empty').toBeGreaterThan(40)

    const free = registerOutcome(httpError(400, FREE_MAIL_REFUSED))
    expect(free).toMatchObject({ field: 'email', message: expect.any(String) })
    expect(free, 'the free-mail answer is inline, not form-level').not.toHaveProperty('form')

    const weak = 'Password should be at least 6 characters.'
    expect(registerOutcome(httpError(400, weak))).toEqual({ form: weak })

    expect(registerOutcome(httpError(503, 'registration is closed'))).toEqual({ form: 'Registration is not open yet.' })
    expect(registerOutcome(httpError(429, 'slow down'))).toEqual({ form: 'Too many attempts. Try again in a minute.' })
    expect(registerOutcome(httpError(502, 'bad gateway'))).toEqual({ form: UNAVAILABLE })
    expect(registerOutcome(new ApiError('network', 'Failed to fetch'))).toEqual({ form: UNAVAILABLE })
  })

  it('registerOutcome keeps the gateway message only for a 400', () => {
    expect(registerOutcome(new ApiError('malformed', 'malformed response body', 202))).toEqual({ form: UNAVAILABLE })
    expect(registerOutcome(new Error('boom'))).toEqual({ form: UNAVAILABLE })
    expect(registerOutcome(httpError(500, 'pq: secret detail'))).toEqual({ form: UNAVAILABLE })
    expect(registerOutcome(httpError(409, FREE_MAIL_REFUSED)), 'the literal on a non-400 is not a free-mail answer').toEqual({ form: UNAVAILABLE })
  })
})

describe('registerAccount through the wire', () => {
  const outcomeFor = async (res: Response | Error) => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => (res instanceof Error ? Promise.reject(res) : Promise.resolve(res))))
    const err = await registerAccount(VALID).then(
      () => null,
      (e: unknown) => e,
    )
    expect(err, 'a refused registration rejects').not.toBeNull()
    return registerOutcome(err)
  }
  const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status })

  it('registerOutcome maps what the gateway and the network really send', async () => {
    expect(await outcomeFor(json(400, { error: FREE_MAIL_REFUSED }))).toEqual({ field: 'email', message: FREE_MAIL_REFUSED })
    expect(await outcomeFor(json(400, { error: 'workspace_name must not contain a NUL byte' }))).toEqual({
      form: 'workspace_name must not contain a NUL byte',
    })
    expect(await outcomeFor(json(400, { error: 'kind must be "firm" or "in_house"' }))).toEqual({ form: 'kind must be "firm" or "in_house"' })
    expect(await outcomeFor(json(503, { error: 'registration is closed' }))).toEqual({ form: 'Registration is not open yet.' })
    expect(await outcomeFor(new Response('<html>Service Unavailable</html>', { status: 503 })), 'non-JSON 503').toEqual({
      form: 'Registration is not open yet.',
    })
    expect(await outcomeFor(json(429, { error: 'too many requests' }))).toEqual({ form: 'Too many attempts. Try again in a minute.' })
    expect(await outcomeFor(new Response('', { status: 502 }))).toEqual({ form: UNAVAILABLE })
    expect(await outcomeFor(new TypeError('Failed to fetch')), 'fetch rejects').toEqual({ form: UNAVAILABLE })
  })

  it('registerOutcome never shows an empty message for a 400 without one', async () => {
    for (const res of [json(400, {}), json(400, { error: '' }), json(400, { error: '  ' }), new Response('<html>Bad Request</html>', { status: 400 }), new Response('', { status: 400 })]) {
      expect(await outcomeFor(res)).toEqual({ form: UNAVAILABLE })
    }
  })
})

describe('resendVerification', () => {
  it('resendVerification posts the trimmed address to the resend route', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
    // internal/gateway/resend_verification.go: the 202 body of every resend.
    const ACCEPTED = { status: 'accepted' }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(ACCEPTED), { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await resendVerification('  a@corp.example ')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw.x/auth/resend-verification')
    expect(init.method).toBe('POST')
    const headers = new Headers(init.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.has('Authorization')).toBe(false)
    expect(init.body).toBe('{"email":"a@corp.example"}')
  })

  it('resendVerification throws when the gateway is unset', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    const err = await resendVerification('a@corp.example').then(
      () => undefined,
      (e: unknown) => e,
    )

    expect(err, 'expected the call to reject').toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('malformed')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
