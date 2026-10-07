// The landing reset client and outcome parse.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client/client'

import { readResetOutcome, requestPasswordReset } from './passwordReset'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('requestPasswordReset', () => {
  it('requestPasswordReset posts the trimmed address to the request route', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
    // internal/gateway/password_reset.go: the 202 body of every request.
    const ACCEPTED = { status: 'accepted' }
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(ACCEPTED), { status: 202 }))
    vi.stubGlobal('fetch', fetchMock)

    await requestPasswordReset('  a@corp.example ')

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('https://gw.x/auth/request-password-reset')
    expect(init.method).toBe('POST')
    const headers = new Headers(init.headers)
    expect(headers.get('Content-Type')).toBe('application/json')
    expect(headers.has('Authorization')).toBe(false)
    expect(init.body).toBe('{"email":"a@corp.example"}')
  })

  it('requestPasswordReset throws when the gateway is unset', async () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    const err = await requestPasswordReset('a@corp.example').then(
      () => undefined,
      (e: unknown) => e,
    )

    expect(err, 'expected the call to reject').toBeInstanceOf(ApiError)
    expect((err as ApiError).kind).toBe('malformed')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('readResetOutcome', () => {
  it('readResetOutcome reads exactly one recognised value', () => {
    // internal/gateway/reset_password.go: "/?reset=1" after a reset, "/?reset=failed" for a bad link.
    expect(readResetOutcome('?reset=1')).toBe('reset')
    expect(readResetOutcome('?reset=failed')).toBe('reset-failed')
    expect(readResetOutcome('?x=2&reset=1&y=3'), 'other params do not matter').toBe('reset')

    for (const search of ['', '?reset=0', '?reset=1&reset=1', '?reset=failed&reset=1', '?reset=', '?reset=FAILED', '?verified=1']) {
      expect(readResetOutcome(search), search).toBeNull()
    }
  })
})
