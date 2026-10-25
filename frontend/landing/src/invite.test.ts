// The landing's read of `?invite=`, as readResetOutcome reads `?reset=` (passwordReset.ts).
// The outcome values are the ones the app sends back: frontend/app/src/lib/sessionHandoff.ts InviteOutcome.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { INVITE_PARAM, inviteNameValid, inviteSignInUrl, readInviteOutcome, registerInvitee } from './invite'

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

  it('inviteNameValid counts characters as the server counts runes', () => {
    expect(inviteNameValid('😀'.repeat(200))).toBe(true)
    expect(inviteNameValid('😀'.repeat(201))).toBe(false)
    expect(inviteNameValid(' ')).toBe(false)
    expect(inviteNameValid('a\0b')).toBe(false)
  })
})
