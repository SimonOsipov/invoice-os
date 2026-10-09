// The landing's read of `?invite=`, as readResetOutcome reads `?reset=` (passwordReset.ts).
// The outcome values are the ones the app sends back: frontend/app/src/lib/sessionHandoff.ts InviteOutcome.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { INVITE_PARAM, readInviteOutcome, registerInvitee } from './invite'

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
