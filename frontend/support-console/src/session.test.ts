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

describe('withSession edges', () => {
  const failing = (e: unknown) => vi.fn(async () => Promise.reject(e))

  it('withSession: an error that is not a 401 never renews and is rethrown as is', async () => {
    const errors = [
      new ApiError('http', 'Staff only', 403),
      new ApiError('http', 'boom', 500),
      new ApiError('network', 'offline'),
      new ApiError('http', 'bad', 422, { fields: { title: 'x' } }),
      new TypeError('Failed to fetch'),
    ]
    expect(errors.length).toBeGreaterThan(0)
    for (const e of errors) {
      const call = failing(e)
      await expect(withSession(call)).rejects.toBe(e)
      expect(call).toHaveBeenCalledTimes(1)
    }
    expect(renew).not.toHaveBeenCalled()
  })

  it('withSession: a 401 shaped object that is not an ApiError never renews', async () => {
    const e = Object.assign(new Error('x'), { status: 401 })
    await expect(withSession(failing(e))).rejects.toBe(e)
    expect(renew).not.toHaveBeenCalled()
  })

  it('withSession: parallel 401s with a refused renewal all rethrow and share one attempt', async () => {
    renew.mockImplementation(() => new Promise((_, rej) => setTimeout(() => rej(unauthorized()), 10)))
    const calls = [flaky(1), flaky(1), flaky(1)]
    const out = await Promise.allSettled(calls.map((c) => withSession(c)))
    expect(out.map((o) => o.status)).toEqual(['rejected', 'rejected', 'rejected'])
    expect(renew).toHaveBeenCalledTimes(1)
    for (const c of calls) expect(c).toHaveBeenCalledTimes(1)
  })

  it('withSession: a later 401 renews again once the first renewal is done', async () => {
    renew.mockResolvedValue({ token: STAFF, refreshToken: 'R2' })
    await withSession(flaky(1))
    await withSession(flaky(1))
    expect(renew).toHaveBeenCalledTimes(2)
  })

  it('withSession: a refused renewal does not block the next one', async () => {
    renew.mockRejectedValueOnce(unauthorized())
    await expect(withSession(flaky(1))).rejects.toMatchObject({ status: 401 })
    renew.mockResolvedValue({ token: STAFF, refreshToken: 'R2' })
    await expect(withSession(flaky(1))).resolves.toBe('ok')
    expect(renew).toHaveBeenCalledTimes(2)
  })

  it('withSession: no stored session or no gateway attempts no renewal', async () => {
    store(null)
    const none = flaky(1)
    await expect(withSession(none)).rejects.toMatchObject({ status: 401 })
    expect(none).toHaveBeenCalledTimes(1)

    store(JSON.stringify({ v: 2, token: 'OLD', refresh_token: 'R' }))
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const noGw = flaky(1)
    await expect(withSession(noGw)).rejects.toMatchObject({ status: 401 })
    expect(noGw).toHaveBeenCalledTimes(1)
    expect(renew).not.toHaveBeenCalled()
  })

  it('withSession: a renewal that fails for another reason still rethrows the original 401', async () => {
    renew.mockRejectedValue(new ApiError('network', 'offline'))
    const call = flaky(1)
    await expect(withSession(call)).rejects.toMatchObject({ kind: 'http', status: 401, message: 'Unauthorized' })
    expect(call).toHaveBeenCalledTimes(1)
    expect(saveConsoleSession).not.toHaveBeenCalled()
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
