import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { bounceToStart, PREFLIGHT_MS } from './signIn'

const h = vi.hoisted(() => ({ captureApiFailure: vi.fn() }))

// Only the Sentry edge is mocked, so countsAsIssue runs for real.
vi.mock('@invoice-os/monitoring/report', () => ({ captureApiFailure: h.captureApiFailure }))

beforeEach(() => {
  h.captureApiFailure.mockReset()
  vi.stubEnv('VITE_APP_URL', 'https://app.x/')
  vi.stubGlobal('window', { location: { set href(_v: string) {} } })
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('bounceToStart reporting', () => {
  it('bounceToStart_deadOrigin_reportsANetworkFailureOnce', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    expect(await bounceToStart()).toBe(false)
    expect(h.captureApiFailure).toHaveBeenCalledTimes(1)
    expect(h.captureApiFailure).toHaveBeenCalledWith(
      expect.objectContaining({ kind: 'network', status: null, method: 'GET', url: 'https://app.x' }),
    )
  })

  it('bounceToStart_preflightTimesOut_reportsANetworkFailureOnce', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn((_u: string, init: RequestInit) => new Promise((_res, rej) => {
      init.signal!.addEventListener('abort', () => rej(init.signal!.reason))
    })))
    const p = bounceToStart()
    await vi.advanceTimersByTimeAsync(PREFLIGHT_MS)
    expect(await p).toBe(false)
    expect(h.captureApiFailure).toHaveBeenCalledTimes(1)
  })

  it('bounceToStart_success_reportsNothing', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null)))
    expect(await bounceToStart()).toBe(true)
    expect(h.captureApiFailure).not.toHaveBeenCalled()
  })
})
