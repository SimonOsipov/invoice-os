// @vitest-environment jsdom
// RED specs (task-562, LAND-05-03, Test-first) — T3-8 and T3-14, the two the plan
// specified VACUOUSLY. Both are here rather than in consentActions.dom.test.tsx, and
// both call applyChoice directly, for reasons that are load-bearing:
//
//   1. send() needs loaded === true, which needs a successful ensureTag, which needs
//      isProductionHost(hostname). PRODUCTION_HOSTNAMES is ['www.ascomply.com'], exact
//      match only (hubspot.ts:9). Driven through an App click the hostname is this
//      document's, the gate closes, and BOTH the claim and its non-vacuity control
//      collapse into a green that asserts nothing.
//   2. This file therefore keeps the DEFAULT localhost URL — a control below pins it —
//      so the explicit { hostname } argument is what opens the gate and cannot quietly
//      stop being load-bearing. consentActions.dom.test.tsx runs at the production URL
//      and covers the App click path.
//   3. Module identity: applyChoice must observe the SAME analytics.ts instance this
//      file asserts against. Inside one vi.resetModules() scope, consentActions is
//      imported FIRST and analytics SECOND, both dynamically — a static import of
//      either binds a stale registry entry and the `revoked` flag read here is not the
//      one applyChoice set. The idiom is analytics.dom.test.ts:33-39; the two-of-our-
//      own-modules-in-one-scope ordering is new and is why it carries this note.
//      resetModules also clears firedMilestones, which is what makes "an uncrossed
//      milestone" mean anything below.
//
// bootAnalytics() (main.tsx:22) may already have set loaded = true before any click on
// a real page load with a granted record stored, so nothing here assumes a fresh flag:
// every phase reads the counter it is about to compare against.
//
// No vi.mock anywhere in this file, deliberately — a mock of ./analytics defeats the
// only thing these two specs measure.
/// <reference types="node" />
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { type ConsentRecord, type ConsentStore } from './consent'

type TestWindow = Window & { dataLayer?: IArguments[]; gtag?: (...args: unknown[]) => void }

type ConsentActionsModule = {
  applyChoice: (
    choice: 'accept' | 'reject',
    opts?: { hostname?: string; store?: ConsentStore | null },
  ) => ConsentRecord
}

type AnalyticsModule = {
  tagIsLoaded: () => boolean
  setAnalyticsRevoked: (v: boolean) => void
  trackDemoOpen: (source: string) => void
  trackScrollDepth: (percent: number) => void
}

const HERE = dirname(fileURLToPath(import.meta.url))
const CONSENT_ACTIONS_PATH = join(HERE, 'consentActions.ts')
// Non-literal on purpose: a missing module must fail as an assertion, not a collection
// error, and tsc does not resolve non-literal dynamic imports.
const CONSENT_ACTIONS_SPECIFIER = './consentActions'
const ANALYTICS_SPECIFIER = './analytics'

const ID = 'G-E409H76XYY'
const PROD_HOST = 'www.ascomply.com'

function memoryStorage(): ConsentStore {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => void map.set(k, String(v)),
  }
}

function layer(): IArguments[] {
  return (window as TestWindow).dataLayer ?? []
}

function eventEntries(): unknown[][] {
  return layer()
    .map((entry) => Array.from(entry))
    .filter((args) => args[0] === 'event')
}

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

let consoleSpies: ReturnType<typeof spyOnConsole>

beforeEach(() => {
  document.head.innerHTML = ''
  const w = window as TestWindow
  delete w.dataLayer
  delete w.gtag
  vi.resetModules()
  vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
  consoleSpies = spyOnConsole()
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

/** consentActions FIRST, analytics SECOND, both dynamic, one reset scope. */
async function loadSeam(): Promise<{ applyChoice: ConsentActionsModule['applyChoice']; analytics: AnalyticsModule }> {
  expect(existsSync(CONSENT_ACTIONS_PATH), `expected ${CONSENT_ACTIONS_PATH} to exist`).toBe(true)
  const actions = (await import(CONSENT_ACTIONS_SPECIFIER)) as ConsentActionsModule
  const analytics = (await import(ANALYTICS_SPECIFIER)) as AnalyticsModule
  expect(typeof actions.applyChoice, 'expected an applyChoice named export').toBe('function')
  expect(typeof analytics.tagIsLoaded, 'expected a tagIsLoaded named export').toBe('function')
  expect(typeof analytics.setAnalyticsRevoked, 'expected a setAnalyticsRevoked named export').toBe('function')
  return { applyChoice: actions.applyChoice, analytics }
}

describe('controls', () => {
  it('control: this document is NOT the allowlisted host, so the hostname argument is what opens the gate', () => {
    expect(window.location.hostname).not.toBe(PROD_HOST)
  })

  it('control: each case starts with an empty dataLayer', () => {
    expect(layer().length).toBe(0)
    expect(eventEntries().length).toBe(0)
  })

  it('control: the console spies detect a call', () => {
    console.error('x')
    console.warn('x')
    console.log('x')
    console.info('x')
    expect(consoleSpies.error).toHaveBeenCalledTimes(1)
    expect(consoleSpies.warn).toHaveBeenCalledTimes(1)
    expect(consoleSpies.log).toHaveBeenCalledTimes(1)
    expect(consoleSpies.info).toHaveBeenCalledTimes(1)
  })
})

describe('T3-8: Reject mutes our senders without a reload', () => {
  it('AC-3: the senders are live between Accept and Reject, and silent after it', async () => {
    const { applyChoice, analytics } = await loadSeam()
    const store = memoryStorage()

    applyChoice('accept', { hostname: PROD_HOST, store })
    // Also the module-identity proof: `loaded` is private to analytics.ts and only
    // applyChoice's ensureTag sets it. Reading true through THIS handle is what shows
    // both sides are the same module instance.
    expect(analytics.tagIsLoaded(), 'the gate never opened — every phase below would assert nothing').toBe(true)
    expect(layer().length, 'ensureTag pushed nothing').toBeGreaterThan(0)

    // Non-vacuity partner: the identical senders, while consent stands.
    const live = eventEntries().length
    analytics.trackScrollDepth(30) // crosses the 25 milestone
    analytics.trackDemoOpen('hero')
    expect(eventEntries().length, 'the senders were already muted before Reject').toBe(live + 2)

    applyChoice('reject', { hostname: PROD_HOST, store })

    // 50 is still uncrossed, so firedMilestones cannot be the reason nothing lands.
    analytics.trackScrollDepth(60)
    analytics.trackDemoOpen('footer')
    expect(eventEntries().length, 'a sender reached dataLayer after Reject').toBe(live + 2)

    expectNoConsoleCalls(consoleSpies)
  })
})

describe('T3-14: Accept then Reject then Accept, within one page load', () => {
  it('AC-6: the third click clears revoked and the next event reaches gtag', async () => {
    const { applyChoice, analytics } = await loadSeam()
    const store = memoryStorage()

    applyChoice('accept', { hostname: PROD_HOST, store })
    expect(analytics.tagIsLoaded(), 'the gate never opened — every phase below would assert nothing').toBe(true)

    applyChoice('reject', { hostname: PROD_HOST, store })
    const muted = eventEntries().length
    analytics.trackDemoOpen('nav')
    // Fails if `revoked` is never SET.
    expect(eventEntries().length, 'a sender reached dataLayer while the visitor had rejected').toBe(muted)

    // The second Accept returns early from ensureTag, because `loaded` is already
    // true. Clearing `revoked` only inside the injection branch leaves the tag
    // resident-but-muted and the visitor's consent silently ignored.
    applyChoice('accept', { hostname: PROD_HOST, store })
    analytics.trackDemoOpen('hero')
    const after = eventEntries()
    // Fails if `revoked` is never CLEARED.
    expect(after.length, 'the third click did not restore the senders').toBe(muted + 1)
    expect(after[after.length - 1]).toEqual(['event', 'demo_open', { cta_location: 'hero' }])

    expectNoConsoleCalls(consoleSpies)
  })
})
