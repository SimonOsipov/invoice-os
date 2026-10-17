import { ApiError } from '@invoice-os/api-client'
import { expireConsole, renewConsoleSession, saveConsoleSession } from '@invoice-os/console-session'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { expireOnce, withSession } from './session'

vi.mock('@invoice-os/console-session', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@invoice-os/console-session')>()),
  renewConsoleSession: vi.fn(),
  saveConsoleSession: vi.fn(),
  expireConsole: vi.fn(),
}))

const GW = 'https://gw.test'
const b64url = (o: unknown) => btoa(JSON.stringify(o)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
const jwt = (claims: Record<string, unknown>) => `${b64url({ alg: 'RS256', typ: 'JWT' })}.${b64url(claims)}.c2ln`
const STAFF = jwt({ sub: 's1', app_metadata: { staff: true } })
const CUSTOMER = jwt({ sub: 'c1', app_metadata: { tenant_id: 't1' } })

const store = (raw: string | null) => vi.stubGlobal('localStorage', { getItem: (k: string) => (k === SESSION_KEY ? raw : null) })
const unauthorized = () => new ApiError('http', 'Unauthorized', 401)
const renew = vi.mocked(renewConsoleSession)

// Rejects 401 on its first n runs, then resolves 'ok'.
const flaky = (n: number) => {
  let runs = 0
  return vi.fn(async () => {
    if (runs++ < n) throw unauthorized()
    return 'ok'
  })
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', GW + '/')
  store(JSON.stringify({ v: 2, token: 'OLD', refresh_token: 'R' }))
  renew.mockReset()
  vi.mocked(saveConsoleSession).mockClear()
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('withSession', () => {
  it('withSession: a 401 renews once and retries', async () => {
    renew.mockResolvedValue({ token: STAFF, refreshToken: 'R2' })
    const call = flaky(1)
    await expect(withSession(call)).resolves.toBe('ok')
    expect(call).toHaveBeenCalledTimes(2)
    expect(renew).toHaveBeenCalledTimes(1)
    expect(renew).toHaveBeenCalledWith(GW, { token: 'OLD', refreshToken: 'R' })
    expect(saveConsoleSession).toHaveBeenCalledTimes(1)
    expect(saveConsoleSession).toHaveBeenCalledWith(SESSION_KEY, { token: STAFF, refreshToken: 'R2' })
  })

  it('withSession: a call that does not 401 runs once and never renews', async () => {
    const call = flaky(0)
    await expect(withSession(call)).resolves.toBe('ok')
    expect(call).toHaveBeenCalledTimes(1)
    expect(renew).not.toHaveBeenCalled()
  })

  it('withSession: parallel 401s share one renewal', async () => {
    renew.mockImplementation(() => new Promise((r) => setTimeout(() => r({ token: STAFF, refreshToken: 'R2' }), 10)))
    const calls = [flaky(1), flaky(1), flaky(1)]
    await expect(Promise.all(calls.map((c) => withSession(c)))).resolves.toEqual(['ok', 'ok', 'ok'])
    expect(renew).toHaveBeenCalledTimes(1)
    for (const c of calls) expect(c).toHaveBeenCalledTimes(2)
  })

  it('withSession: a refused renewal rethrows the 401', async () => {
    renew.mockRejectedValue(unauthorized())
    const call = flaky(1)
    await expect(withSession(call)).rejects.toMatchObject({ kind: 'http', status: 401 })
    expect(renew).toHaveBeenCalledTimes(1)
    expect(call).toHaveBeenCalledTimes(1)
    expect(saveConsoleSession).not.toHaveBeenCalled()
  })

  it('withSession: a stored record without a refresh token attempts no renewal', async () => {
    renew.mockRejectedValue(unauthorized())
    await expect(withSession(flaky(1))).rejects.toMatchObject({ status: 401 })
    expect(renew, 'a record with a refresh token tries to renew').toHaveBeenCalledTimes(1)

    renew.mockClear()
    store(JSON.stringify({ v: 2, token: 'OLD', from: 'app' }))
    const call = flaky(1)
    await expect(withSession(call)).rejects.toMatchObject({ kind: 'http', status: 401 })
    expect(call).toHaveBeenCalledTimes(1)
    expect(renew).not.toHaveBeenCalled()
  })

  it('withSession: a non-staff renewed token is refused', async () => {
    renew.mockResolvedValue({ token: CUSTOMER, refreshToken: 'R2' })
    const call = flaky(1)
    await expect(withSession(call)).rejects.toMatchObject({ kind: 'http', status: 401 })
    expect(renew).toHaveBeenCalledTimes(1)
    expect(call).toHaveBeenCalledTimes(1)
    expect(saveConsoleSession).not.toHaveBeenCalled()
  })

  it('withSession: a 401 on the retry is final', async () => {
    renew.mockResolvedValue({ token: STAFF, refreshToken: 'R2' })
    const call = flaky(2)
    await expect(withSession(call)).rejects.toMatchObject({ kind: 'http', status: 401 })
    expect(call).toHaveBeenCalledTimes(2)
    expect(renew).toHaveBeenCalledTimes(1)
  })
})

describe('expireOnce', () => {
  it('expireOnce signs out once', () => {
    expireOnce()
    expireOnce()
    expireOnce()
    expect(expireConsole).toHaveBeenCalledTimes(1)
  })
})
