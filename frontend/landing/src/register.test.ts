// The landing registration client. register.ts is new, so each test goes red on the missing module first.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client/client'

import {
  FREE_MAIL_REFUSED,
  registerAccount,
  registerOutcome,
  registrationOpen,
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

    const empty = validateRegisterForm({ email: '', password: '', displayName: '', workspaceName: '', kind: '' })
    expect(Object.keys(empty).sort()).toEqual(['displayName', 'email', 'kind', 'password', 'workspaceName'])
    for (const [field, message] of Object.entries(empty)) {
      expect(message, field).toEqual(expect.any(String))
      expect(message, field).not.toBe('')
    }
    expect(empty.email).toBe('Enter your work email.')
    expect(empty.kind).toBe('Choose how this workspace files invoices.')

    expect(validateRegisterForm({ ...VALID, kind: '' })).toEqual({ kind: 'Choose how this workspace files invoices.' })
    expect(validateRegisterForm({ ...VALID, email: 'a b@c.d' })).toEqual({ email: 'Enter a valid work email address.' })
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

    const spaced: RegisterValues = { ...VALID, password: ' pw ' }
    expect(validateRegisterForm(spaced), 'the password is never trimmed').toEqual({})
    expect(spaced.password).toBe(' pw ')
    expect(validateRegisterForm({ ...VALID, password: '   ' }), 'a blank password is still a password').toEqual({})
  })
})

describe('registerAccount', () => {
  it('registerAccount posts the snake_case body', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: 'verification_pending' }), { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await registerAccount({
      email: ' Ada@Corp.example ',
      password: ' pw ',
      displayName: '  Ada Okafor ',
      workspaceName: ' Okafor & Partners  ',
      kind: 'in_house',
    })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw.x/auth/register')
    expect(init.method).toBe('POST')
    const headers = new Headers(init.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.has('Authorization')).toBe(false)
    const body = JSON.parse(init.body as string) as Record<string, unknown>
    expect(Object.keys(body).sort()).toEqual(['display_name', 'email', 'kind', 'password', 'workspace_name'])
    expect(body).toStrictEqual({
      email: 'Ada@Corp.example',
      password: ' pw ',
      display_name: 'Ada Okafor',
      workspace_name: 'Okafor & Partners',
      kind: 'in_house',
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
