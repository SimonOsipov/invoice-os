// @vitest-environment jsdom
// RED specs (task-543, LAND-03-02, Test-first) — pin idempotent gtag.js injection, the
// arguments-object shim and console silence before analytics.ts is implemented. Every
// case re-imports the module fresh: the module-level `loaded` flag resets on
// vi.resetModules(), but jsdom's `document` persists across tests in this file, so both
// resets are mandatory or a later case inherits an earlier case's script.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_VERSION, type ConsentRecord } from './consent'

type TestWindow = Window & { dataLayer?: IArguments[]; gtag?: (...args: unknown[]) => void }

const ID = 'G-E409H76XYY'
const GRANTED: ConsentRecord = { analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }
const DENIED: ConsentRecord = { analytics: false, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }

function spyOnConsole() {
  return {
    error: vi.spyOn(console, 'error').mockImplementation(() => undefined),
    warn: vi.spyOn(console, 'warn').mockImplementation(() => undefined),
    log: vi.spyOn(console, 'log').mockImplementation(() => undefined),
    info: vi.spyOn(console, 'info').mockImplementation(() => undefined),
  }
}

function expectNoConsoleCalls(spies: ReturnType<typeof spyOnConsole>) {
  expect(spies.error).not.toHaveBeenCalled()
  expect(spies.warn).not.toHaveBeenCalled()
  expect(spies.log).not.toHaveBeenCalled()
  expect(spies.info).not.toHaveBeenCalled()
}

beforeEach(() => {
  document.head.innerHTML = ''
  const w = window as TestWindow
  delete w.dataLayer
  delete w.gtag
  vi.resetModules()
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('ensureTag — closed gate', () => {
  it('AC-2: no script element is appended off the live hostname', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('landing-pr-42.up.railway.app', GRANTED)
    expect(document.querySelectorAll('script[src^="https://www.googletagmanager.com/"]').length).toBe(0)
    expect((window as TestWindow).dataLayer).toBeUndefined()
  })

  it('AC-2: no script element is appended when consent is denied', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', DENIED)
    expect(document.querySelectorAll('script[src^="https://www.googletagmanager.com/"]').length).toBe(0)
    expect((window as TestWindow).dataLayer).toBeUndefined()
  })

  it('AC-2: no script element is appended when the id is unset', async () => {
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)
    expect(document.querySelectorAll('script[src^="https://www.googletagmanager.com/"]').length).toBe(0)
    expect((window as TestWindow).dataLayer).toBeUndefined()
  })
})

describe('ensureTag — open gate', () => {
  it('AC-1: exactly one async gtag script is appended on the live hostname', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    expect(mod.ensureTag('www.ascomply.com', GRANTED)).toBe(true)

    const scripts = document.head.querySelectorAll('script')
    expect(scripts.length).toBe(1)
    const script = scripts[0] as HTMLScriptElement
    expect(script.async).toBe(true)
    expect(script.src).toBe(`https://www.googletagmanager.com/gtag/js?id=${ID}`)
  })

  it('AC-1: the shim pushes an arguments object, not an array', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    const entry = (window as TestWindow).dataLayer?.[0]
    expect(entry).toBeDefined()
    // The single highest-risk line: a shim that pushes an array instead of `arguments`
    // is silently ignored by GA4 — no error, no event, nothing in DebugView.
    expect(Object.prototype.toString.call(entry)).toBe('[object Arguments]')
    expect(Array.isArray(entry)).toBe(false)
    const asArray = Array.from(entry as IArguments)
    expect(asArray[0]).toBe('js')
    expect(asArray[1]).toBeInstanceOf(Date)
  })

  it('AC-1: config is sent for the configured property', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    const entry = (window as TestWindow).dataLayer?.[1]
    expect(Array.from(entry as IArguments)).toEqual(['config', ID])
  })

  it('Q2: no consent-mode signal reaches dataLayer', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    const layer = (window as TestWindow).dataLayer ?? []
    // Non-vacuity: an empty dataLayer would otherwise make the loop below pass for free.
    expect(layer.length).toBeGreaterThan(0)
    for (const entry of layer) {
      expect(Array.from(entry)[0]).not.toBe('consent')
    }
  })

  it("AC-1: ensureTag is idempotent for LAND-05's Accept handler", async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')

    const results = [
      mod.ensureTag('www.ascomply.com', GRANTED),
      mod.ensureTag('www.ascomply.com', GRANTED),
      mod.ensureTag('www.ascomply.com', GRANTED),
    ]

    expect(results).toEqual([true, true, true])
    expect(document.head.querySelectorAll('script').length).toBe(1)
    expect((window as TestWindow).dataLayer?.length).toBe(2)
  })
})

describe('console silence', () => {
  it('the console spies detect a call (non-vacuity control)', () => {
    const spies = spyOnConsole()
    console.error('x')
    console.warn('x')
    console.log('x')
    console.info('x')
    expect(spies.error).toHaveBeenCalledTimes(1)
    expect(spies.warn).toHaveBeenCalledTimes(1)
    expect(spies.log).toHaveBeenCalledTimes(1)
    expect(spies.info).toHaveBeenCalledTimes(1)
  })

  it('AC-9: nothing is written to the console on any gate path', async () => {
    const spies = spyOnConsole()

    // Closed: off-hostname, denied consent, unset id.
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    let mod = await import('./analytics')
    mod.ensureTag('landing-pr-42.up.railway.app', GRANTED)
    mod.ensureTag('www.ascomply.com', DENIED)

    vi.unstubAllEnvs()
    vi.resetModules()
    mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    // Open, including the idempotent second call.
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    vi.resetModules()
    mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)
    mod.ensureTag('www.ascomply.com', GRANTED)

    expectNoConsoleCalls(spies)
  })
})

describe('bootAnalytics', () => {
  it("AC-2: reads the live hostname and the stored consent — jsdom's default host is not production", async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')

    expect(window.location.hostname).toBe('localhost')
    expect(mod.bootAnalytics()).toBe(false)
    expect(document.querySelectorAll('script[src^="https://www.googletagmanager.com/"]').length).toBe(0)
  })
})
