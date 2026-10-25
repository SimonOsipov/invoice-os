// The landing's read of `?invite=`, as readResetOutcome reads `?reset=` (passwordReset.ts).
// The outcome values are the ones the app sends back: frontend/app/src/lib/sessionHandoff.ts InviteOutcome.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { INVITE_PARAM, inviteNameError, inviteNameValid, inviteSignInUrl, readInviteOutcome, registerInvitee, trimName } from './invite'

describe('readInviteOutcome_exactlyOneRecognisedValue', () => {
  it('each recognised value reads as its outcome', () => {
    expect(INVITE_PARAM).toBe('invite')
    for (const v of ['already-member', 'invalid', 'other-address'] as const) {
      expect(readInviteOutcome(`?invite=${v}`), v).toBe(v)
      expect(readInviteOutcome(`?keep=1&invite=${v}`), `${v} beside another param`).toBe(v)
    }
  })

  it('an unknown, repeated or absent value reads as none', () => {
    // Control: a recognised value is not null, so a stub that returns null fails above, not here.
    expect(readInviteOutcome('?invite=invalid')).not.toBeNull()
    for (const s of ['?invite=x', '?invite=', '?invite=invalid&invite=invalid', '?invite=invalid&invite=other-address', '?invite=INVALID', '', '?keep=1']) {
      expect(readInviteOutcome(s), s).toBeNull()
    }
  })
})

describe('registerInvitee_postsTheTokenOnly', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
  })

  it('the body is strictly the token', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    const fetchMock = vi.fn(async () => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await registerInvitee('tok-123')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('https://gw.x/auth/invitation/register')
    expect(init.method).toBe('POST')
    expect(init.body).toBe('{"token":"tok-123"}')
  })
})

describe('inviteSignInUrl', () => {
  afterEach(() => vi.unstubAllEnvs())

  it('inviteSignInUrl puts the name in the fragment', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x')
    expect(inviteSignInUrl('T', ' Ada Obi ')).toBe('https://app.x?auth=start#invite=T&name=Ada%20Obi')
    expect(inviteSignInUrl('T', 'a&b#c=d')).toBe('https://app.x?auth=start#invite=T&name=a%26b%23c%3Dd')
  })

  it('inviteSignInUrl refuses a missing token, an invalid name or no app URL', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x')
    expect(inviteSignInUrl('T', 'Ada')).not.toBeNull()
    expect(inviteSignInUrl(null, 'Ada')).toBeNull()
    expect(inviteSignInUrl('T', '  ')).toBeNull()
    expect(inviteSignInUrl('T', 'Ada\0')).toBeNull()
    vi.stubEnv('VITE_APP_URL', '')
    expect(inviteSignInUrl('T', 'Ada')).toBeNull()
  })

  // Same literal as frontend/app/src/lib/pendingInvite.test.ts 'readInviteName reads what the landing writes'.
  it('inviteSignInUrl writes the hard name as the literal the app reads', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x')
    const hard = 'A&b=c#d+e \u{1F600}' + 'x'.repeat(189)
    expect([...hard]).toHaveLength(200)
    expect(inviteSignInUrl('T', ` ${hard} `)).toBe('https://app.x?auth=start#invite=T&name=A%26b%3Dc%23d%2Be%20%F0%9F%98%80xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx')
  })

  it('inviteNameValid counts characters as the server counts runes', () => {
    expect(inviteNameValid('😀'.repeat(200))).toBe(true)
    expect(inviteNameValid('😀'.repeat(201))).toBe(false)
    expect(inviteNameValid(' ')).toBe(false)
    expect(inviteNameValid('a\0b')).toBe(false)
  })
})

describe('trimName and inviteNameError', () => {
  it('trimName strips U+0085 as the API does', () => {
    expect(trimName(' \u0085 Ada \u0085 ')).toBe('Ada')
    expect(trimName('\u0085')).toBe('')
    expect(trimName('\uFEFF')).toBe('')
    expect(trimName('a\u200B')).toBe('a\u200B')
  })

  it('inviteNameError: the four outcomes', () => {
    expect(inviteNameError('Ada')).toBeNull()
    expect(inviteNameError('  ')).toBe('Enter your name.')
    expect(inviteNameError('\u0085')).toBe('Enter your name.')
    expect(inviteNameError('a'.repeat(201))).toBe('Use 200 characters or fewer.')
    expect(inviteNameError('a\0')).toBeNull()
    expect(inviteNameError('a'.repeat(201) + '\0')).toBeNull()
  })

  it('inviteSignInUrl puts the trimmed name in the fragment', () => {
    vi.stubEnv('VITE_APP_URL', 'https://app.x')
    expect(inviteSignInUrl('T', '\u0085 Ada Obi \u0085')).toBe('https://app.x?auth=start#invite=T&name=Ada%20Obi')
    expect(inviteSignInUrl('T', '\u0085')).toBeNull()
    vi.unstubAllEnvs()
  })
})
