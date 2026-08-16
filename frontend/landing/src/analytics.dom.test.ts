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

describe('ensureTag — adversarial and edge cases', () => {
  it('AC-2: a null consent record falls back to the granted default', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    expect(mod.ensureTag('www.ascomply.com', null)).toBe(true)
    expect(document.head.querySelectorAll('script').length).toBe(1)
    expect((window as TestWindow).dataLayer?.length).toBe(2)
  })

  // Guards a `loaded = true` set before the gate check: a closed-gate call must not
  // silently block a later open-gate call on the same module instance.
  it('a closed-gate call does not poison a later open-gate call', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')

    expect(mod.ensureTag('landing-pr-42.up.railway.app', GRANTED)).toBe(false)
    expect(document.head.querySelectorAll('script').length).toBe(0)

    expect(mod.ensureTag('www.ascomply.com', GRANTED)).toBe(true)
    expect(document.head.querySelectorAll('script').length).toBe(1)
    expect((window as TestWindow).dataLayer?.length).toBe(2)
  })

  it('a loaded tag ignores a later call with a different hostname', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')

    expect(mod.ensureTag('www.ascomply.com', GRANTED)).toBe(true)
    expect(mod.ensureTag('landing-pr-42.up.railway.app', GRANTED)).toBe(true)
    expect(document.head.querySelectorAll('script').length).toBe(1)
  })

  it('appends to a dataLayer another script already populated, never resets it', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    ;(window as TestWindow).dataLayer = ['external-marker'] as unknown as IArguments[]
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    const layer = (window as TestWindow).dataLayer ?? []
    expect(layer.length).toBe(3)
    expect(layer[0]).toBe('external-marker')
  })

  it('overwrites a pre-existing window.gtag without throwing', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const sentinel = vi.fn()
    ;(window as TestWindow).gtag = sentinel
    const mod = await import('./analytics')

    expect(() => mod.ensureTag('www.ascomply.com', GRANTED)).not.toThrow()
    expect((window as TestWindow).gtag).not.toBe(sentinel)
    expect(sentinel).not.toHaveBeenCalled()
    expect((window as TestWindow).dataLayer?.length).toBe(2)
  })
})

describe('trackDemoOpen', () => {
  it('AC-3: demo_open carries the call site that opened the modal', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    mod.trackDemoOpen('footer')

    const layer = (window as TestWindow).dataLayer ?? []
    const entry = layer[layer.length - 1]
    expect(Array.from(entry as IArguments)).toEqual(['event', 'demo_open', { cta_location: 'footer' }])
  })

  it('AC-3: every declared source produces a distinct payload', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    for (const source of mod.DEMO_CTA_SOURCES) mod.trackDemoOpen(source)

    const layer = (window as TestWindow).dataLayer ?? []
    // Filtered by event name: ensureTag already left 'js' and 'config' entries in the layer.
    const demoOpenEntries = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'demo_open')
    expect(demoOpenEntries.length).toBe(mod.DEMO_CTA_SOURCES.length)
    const locations = demoOpenEntries.map((e) => (e[2] as { cta_location: string }).cta_location)
    expect(new Set(locations)).toEqual(new Set(mod.DEMO_CTA_SOURCES))
    expect(new Set(locations).size).toBe(mod.DEMO_CTA_SOURCES.length)
  })

  it('AC-2: demo_open sends nothing when the tag was not injected', async () => {
    // No stubbed env id — the gate never opens.
    const mod = await import('./analytics')
    expect(() => mod.trackDemoOpen('nav')).not.toThrow()
    expect((window as TestWindow).dataLayer).toBeUndefined()
    expect((window as TestWindow).gtag).toBeUndefined()
  })

  it('the demo_open entry is an arguments object, not an array (mirrors analytics.dom.test.ts:93-94 for this sender)', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)
    mod.trackDemoOpen('hero')

    const layer = (window as TestWindow).dataLayer ?? []
    const entry = layer[layer.length - 1]
    expect(Object.prototype.toString.call(entry)).toBe('[object Arguments]')
    expect(Array.isArray(entry)).toBe(false)
  })

  it('an unexpected source string is still forwarded as cta_location — TypeScript is the only gate, send() does not validate', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    // Cast past the DemoCtaSource union to prove there is no runtime guard —
    // documents current behaviour, not a defect: the union is enforced at
    // compile time by AC-7's static App.tsx check, not by this function.
    mod.trackDemoOpen('sidebar' as unknown as (typeof mod.DEMO_CTA_SOURCES)[number])

    const layer = (window as TestWindow).dataLayer ?? []
    const entry = Array.from(layer[layer.length - 1] as IArguments)
    expect(entry).toEqual(['event', 'demo_open', { cta_location: 'sidebar' }])
  })
})

describe('trackedHubSpotSubmit', () => {
  it('AC-4: a resolved HubSpot submit is recorded as a lead', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    await mod.trackedHubSpotSubmit(async () => {})

    const layer = (window as TestWindow).dataLayer ?? []
    const outcomes = layer
      .map((e) => Array.from(e as IArguments))
      .filter((e) => e[1] === 'generate_lead' || e[1] === 'demo_submit_failed')
    expect(outcomes).toEqual([['event', 'generate_lead', { form_name: 'book_a_demo' }]])
  })

  it('AC-4: a rejected HubSpot submit is recorded as a failure and rethrown', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    const err = new Error('hubspot 500')
    // toBe, not toThrow: the wrapper must rethrow the identical object, not a substitute.
    await expect(mod.trackedHubSpotSubmit(() => Promise.reject(err))).rejects.toBe(err)

    const layer = (window as TestWindow).dataLayer ?? []
    const outcomes = layer
      .map((e) => Array.from(e as IArguments))
      .filter((e) => e[1] === 'generate_lead' || e[1] === 'demo_submit_failed')
    expect(outcomes).toEqual([['event', 'demo_submit_failed', { form_name: 'book_a_demo' }]])
  })

  it('AC-4: the two outcome events are mutually exclusive', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)
    const named = (name: string) =>
      ((window as TestWindow).dataLayer ?? []).map((e) => Array.from(e as IArguments)).filter((e) => e[1] === name)

    await mod.trackedHubSpotSubmit(async () => {})
    expect(named('generate_lead').length).toBe(1)
    expect(named('demo_submit_failed').length).toBe(0)

    await mod.trackedHubSpotSubmit(() => Promise.reject(new Error('x'))).catch(() => {})
    expect(named('generate_lead').length).toBe(1)
    expect(named('demo_submit_failed').length).toBe(1)
  })

  it('the outcome entry is an arguments object, not an array', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    await mod.trackedHubSpotSubmit(async () => {})
    const layer = (window as TestWindow).dataLayer ?? []
    const entry = layer[layer.length - 1]
    expect(Object.prototype.toString.call(entry)).toBe('[object Arguments]')
    expect(Array.isArray(entry)).toBe(false)
  })

  it('a run() that throws synchronously is caught the same as a rejected promise', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    const err = new Error('sync boom')
    const thrower = () => {
      throw err
    }
    await expect(mod.trackedHubSpotSubmit(thrower)).rejects.toBe(err)

    const layer = (window as TestWindow).dataLayer ?? []
    const outcomes = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'demo_submit_failed' || e[1] === 'generate_lead')
    expect(outcomes).toEqual([['event', 'demo_submit_failed', { form_name: 'book_a_demo' }]])
  })

  it('a rejection with a non-Error value (a string) is recorded and rethrown identically', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    await expect(mod.trackedHubSpotSubmit(() => Promise.reject('hubspot 500'))).rejects.toBe('hubspot 500')

    const layer = (window as TestWindow).dataLayer ?? []
    const outcomes = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'demo_submit_failed' || e[1] === 'generate_lead')
    expect(outcomes).toEqual([['event', 'demo_submit_failed', { form_name: 'book_a_demo' }]])
  })

  it('a rejection with undefined is recorded and rethrown identically', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    await expect(mod.trackedHubSpotSubmit(() => Promise.reject(undefined))).rejects.toBeUndefined()

    const layer = (window as TestWindow).dataLayer ?? []
    const outcomes = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'demo_submit_failed' || e[1] === 'generate_lead')
    expect(outcomes).toEqual([['event', 'demo_submit_failed', { form_name: 'book_a_demo' }]])
  })

  it('concurrent calls do not cross-contaminate their outcomes', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    const err = new Error('concurrent failure')
    const resolving = mod.trackedHubSpotSubmit(() => new Promise<void>((resolve) => setTimeout(resolve, 5)))
    const rejecting = mod.trackedHubSpotSubmit(() => new Promise<void>((_, reject) => setTimeout(() => reject(err), 1)))

    // Both handlers attached in the same tick — awaiting `resolving` first would
    // leave `rejecting` unobserved for several ticks and trip Node's unhandled-
    // rejection detector even though the assertion below does handle it.
    await Promise.all([expect(resolving).resolves.toBeUndefined(), expect(rejecting).rejects.toBe(err)])

    const layer = (window as TestWindow).dataLayer ?? []
    const named = (name: string) => layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === name)
    expect(named('generate_lead').length).toBe(1)
    expect(named('demo_submit_failed').length).toBe(1)
  })

  it('an explicitly closed gate (ensureTag called and returns false) still rethrows while sending nothing', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    // Distinct from the "never called ensureTag" row below: this path exercises
    // ensureTag's early return, not merely an untouched `loaded` flag.
    expect(mod.ensureTag('landing-pr-42.up.railway.app', GRANTED)).toBe(false)

    const err = new Error('closed gate')
    await expect(mod.trackedHubSpotSubmit(() => Promise.reject(err))).rejects.toBe(err)
    await expect(mod.trackedHubSpotSubmit(async () => {})).resolves.toBeUndefined()

    expect((window as TestWindow).dataLayer).toBeUndefined()
    expect((window as TestWindow).gtag).toBeUndefined()
  })
})

describe('trackScrollDepth (AC-7)', () => {
  it('each milestone fires at most once per page load', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    mod.trackScrollDepth(30)
    mod.trackScrollDepth(30)
    mod.trackScrollDepth(26)

    const layer = (window as TestWindow).dataLayer ?? []
    const scrollEvents = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'scroll_depth')
    expect(scrollEvents).toEqual([['event', 'scroll_depth', { percent_scrolled: 25 }]])
  })

  it('a jump past several thresholds fires each of them, ascending', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    mod.trackScrollDepth(80)

    const layer = (window as TestWindow).dataLayer ?? []
    const scrollEvents = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'scroll_depth')
    expect(scrollEvents).toEqual([
      ['event', 'scroll_depth', { percent_scrolled: 25 }],
      ['event', 'scroll_depth', { percent_scrolled: 50 }],
      ['event', 'scroll_depth', { percent_scrolled: 75 }],
    ])
  })

  it('reaching the bottom fires the fourth threshold and no more', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    // percent === 100 exactly hits the last milestone's boundary — the case
    // that kills a `>` mutant on the `percent < m` comparison.
    mod.trackScrollDepth(100)
    mod.trackScrollDepth(100)

    const layer = (window as TestWindow).dataLayer ?? []
    const scrollEvents = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'scroll_depth')
    expect(scrollEvents).toEqual([
      ['event', 'scroll_depth', { percent_scrolled: 25 }],
      ['event', 'scroll_depth', { percent_scrolled: 50 }],
      ['event', 'scroll_depth', { percent_scrolled: 75 }],
      ['event', 'scroll_depth', { percent_scrolled: 100 }],
    ])
  })

  it('AC-2: scroll depth sends nothing when the tag was not injected', async () => {
    // No stubbed env id — the gate never opens.
    const mod = await import('./analytics')
    expect(() => mod.trackScrollDepth(100)).not.toThrow()
    expect((window as TestWindow).dataLayer).toBeUndefined()
    expect((window as TestWindow).gtag).toBeUndefined()
  })

  it('a non-integer percent still crosses whichever milestones it reaches', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    mod.trackScrollDepth(33.7)

    const layer = (window as TestWindow).dataLayer ?? []
    const scrollEvents = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'scroll_depth')
    expect(scrollEvents).toEqual([['event', 'scroll_depth', { percent_scrolled: 25 }]])
  })

  it('a negative percent crosses no milestone', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    mod.trackScrollDepth(-10)

    const layer = (window as TestWindow).dataLayer ?? []
    const scrollEvents = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'scroll_depth')
    expect(scrollEvents).toEqual([])
  })

  it('a percent past 100 fires all four milestones once each, not a fifth', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    mod.trackScrollDepth(150)

    const layer = (window as TestWindow).dataLayer ?? []
    const scrollEvents = layer.map((e) => Array.from(e as IArguments)).filter((e) => e[1] === 'scroll_depth')
    expect(scrollEvents).toEqual([
      ['event', 'scroll_depth', { percent_scrolled: 25 }],
      ['event', 'scroll_depth', { percent_scrolled: 50 }],
      ['event', 'scroll_depth', { percent_scrolled: 75 }],
      ['event', 'scroll_depth', { percent_scrolled: 100 }],
    ])
  })

  it('the scroll_depth entry is an arguments object, not an array', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)

    mod.trackScrollDepth(25)

    const layer = (window as TestWindow).dataLayer ?? []
    const entry = layer[layer.length - 1]
    expect(Object.prototype.toString.call(entry)).toBe('[object Arguments]')
    expect(Array.isArray(entry)).toBe(false)
  })
})

describe('send — the loaded guard, isolated from the gtag optional-chain (gap coverage)', () => {
  it('a sender no-ops even when window.gtag is already defined but this module never loaded the tag', async () => {
    const mod = await import('./analytics')
    // window.gtag can outlive a module instance (e.g. jsdom's window persists
    // across vi.resetModules()) while this fresh module's private `loaded` is
    // still false — the one state gtag?.() alone cannot distinguish. Removing
    // send()'s `if (!loaded) return` guard is invisible to every other case in
    // this file, because window.gtag is otherwise always undefined until this
    // module's own ensureTag sets it.
    const spy = vi.fn()
    ;(window as TestWindow).gtag = spy
    mod.trackDemoOpen('nav')
    expect(spy).not.toHaveBeenCalled()
  })
})

describe('senders — no-op before ensureTag', () => {
  it('AC-6: a sender before ensureTag touches no browser global', async () => {
    const spies = spyOnConsole()
    // Fresh module, ensureTag never called on it — the same guarantee AC-2's closed-gate
    // row proves for trackDemoOpen, extended to the two outcome senders.
    const mod = await import('./analytics')

    expect(() => mod.trackDemoOpen('nav')).not.toThrow()
    await expect(mod.trackedHubSpotSubmit(async () => {})).resolves.toBeUndefined()
    const err = new Error('x')
    await expect(mod.trackedHubSpotSubmit(() => Promise.reject(err))).rejects.toBe(err)

    expect((window as TestWindow).dataLayer).toBeUndefined()
    expect((window as TestWindow).gtag).toBeUndefined()
    expectNoConsoleCalls(spies)
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

  it('AC-9: no outcome path writes to the console', async () => {
    const spies = spyOnConsole()

    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    const mod = await import('./analytics')
    mod.ensureTag('www.ascomply.com', GRANTED)
    await mod.trackedHubSpotSubmit(async () => {}).catch(() => {})
    await mod.trackedHubSpotSubmit(() => Promise.reject(new Error('x'))).catch(() => {})

    // Closed-gate rejection: the failure sender no-ops, but the wrapper still rethrows quietly.
    vi.unstubAllEnvs()
    vi.resetModules()
    const closedMod = await import('./analytics')
    await closedMod.trackedHubSpotSubmit(() => Promise.reject(new Error('x'))).catch(() => {})

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
