// @vitest-environment jsdom
// Adversarial coverage for the choice seam, added at QA. Same module-identity rule as
// consentActions.revocation.dom.test.ts: consentActions FIRST, analytics SECOND, both
// dynamic, inside one vi.resetModules() scope, and no vi.mock anywhere — a mock of
// ./analytics defeats the only thing the revocation cases measure.
/// <reference types="node" />
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { CONSENT_STORAGE_KEY, type ConsentRecord, type ConsentStore } from './consent'

type TestWindow = Window & { dataLayer?: IArguments[]; gtag?: (...args: unknown[]) => void }

type Seam = {
  applyChoice: (
    choice: 'accept' | 'reject',
    opts?: { hostname?: string; store?: ConsentStore | null },
  ) => ConsentRecord
  analytics: {
    tagIsLoaded: () => boolean
    ensureTag: (hostname: string, record: ConsentRecord | null) => boolean
    trackDemoOpen: (source: string) => void
    trackScrollDepth: (percent: number) => void
  }
}

const HERE = dirname(fileURLToPath(import.meta.url))
const ID = 'G-E409H76XYY'
const PROD_HOST = 'www.ascomply.com'

function memoryStorage(): ConsentStore & { raw: () => string | null } {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => void map.set(k, String(v)),
    raw: () => map.get(CONSENT_STORAGE_KEY) ?? null,
  }
}

function eventEntries(): unknown[][] {
  return ((window as TestWindow).dataLayer ?? []).map((e) => Array.from(e)).filter((a) => a[0] === 'event')
}

function cookieNamesInJar(): string[] {
  return document.cookie
    .split(';')
    .map((pair) => pair.split('=')[0]?.trim() ?? '')
    .filter((n) => n.length > 0)
}

function resetJar() {
  for (const name of cookieNamesInJar()) document.cookie = `${name}=; Max-Age=0; path=/`
}

async function loadSeam(): Promise<Seam> {
  const actions = (await import('./consentActions')) as Pick<Seam, 'applyChoice'>
  const analytics = (await import('./analytics')) as Seam['analytics']
  return { applyChoice: actions.applyChoice, analytics }
}

beforeEach(() => {
  document.head.innerHTML = ''
  const w = window as TestWindow
  delete w.dataLayer
  delete w.gtag
  resetJar()
  vi.resetModules()
  vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
})

afterEach(() => {
  resetJar()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('applyChoice fails closed on hostile input', () => {
  it('an unrecognised choice is treated as Reject, never as Accept', async () => {
    // `choice` is typed, but the notice's onChoose crosses a prop boundary and the
    // record is a public disclosure. Anything that is not exactly 'accept' must deny.
    const { applyChoice, analytics } = await loadSeam()
    for (const bad of ['Accept', 'ACCEPT', 'accepted', '', 'yes', undefined, null, 1, {}]) {
      const store = memoryStorage()
      const record = applyChoice(bad as unknown as 'accept', { hostname: PROD_HOST, store })
      expect(record.analytics, `"${String(bad)}" was treated as consent`).toBe(false)
      expect(analytics.tagIsLoaded(), `"${String(bad)}" loaded the tag`).toBe(false)
      expect(document.querySelectorAll('script[src*="googletagmanager"]').length).toBe(0)
    }
  })

  it('a store whose setItem throws does not stop the revocation or the cookie clear', async () => {
    document.cookie = '_ga=GA1.1.1.1; path=/'
    const throwing: ConsentStore = {
      getItem: () => null,
      setItem: () => {
        throw new DOMException('QuotaExceededError')
      },
    }
    const { applyChoice, analytics } = await loadSeam()

    applyChoice('accept', { hostname: PROD_HOST, store: throwing })
    expect(analytics.tagIsLoaded(), 'the tag never loaded, so the phases below assert nothing').toBe(true)
    const live = eventEntries().length
    analytics.trackDemoOpen('hero')
    expect(eventEntries().length).toBe(live + 1)

    const record = applyChoice('reject', { hostname: PROD_HOST, store: throwing })
    expect(record.analytics, 'the returned record must be the choice, persisted or not').toBe(false)
    // The persistence failure is silent by design (consent.ts writeConsent), but the
    // in-page effects must still happen or the visitor's Reject does nothing at all.
    expect(cookieNamesInJar(), 'a failed write skipped the cookie clear').not.toContain('_ga')
    analytics.trackDemoOpen('footer')
    expect(eventEntries().length, 'a failed write skipped the revocation').toBe(live + 1)
  })

  it('a second identical Accept in one page load injects nothing further', async () => {
    // The notice unmounts on the first choice, but the seam is public and a double
    // event must not double-configure the property.
    const { applyChoice, analytics } = await loadSeam()
    const store = memoryStorage()
    applyChoice('accept', { hostname: PROD_HOST, store })
    const scripts = document.querySelectorAll('script[src*="googletagmanager"]').length
    const pushes = ((window as TestWindow).dataLayer ?? []).length

    applyChoice('accept', { hostname: PROD_HOST, store })
    applyChoice('accept', { hostname: PROD_HOST, store })

    expect(document.querySelectorAll('script[src*="googletagmanager"]').length, 'the tag was injected twice').toBe(
      scripts,
    )
    expect(((window as TestWindow).dataLayer ?? []).length, 'js/config were sent again').toBe(pushes)
    expect(analytics.tagIsLoaded()).toBe(true)
  })

  it('a repeated Reject stays a no-op and never resurrects a cookie', async () => {
    document.cookie = '_ga=GA1.1.1.1; path=/'
    document.cookie = 'hs_keep=stay; path=/'
    const { applyChoice } = await loadSeam()
    const store = memoryStorage()
    applyChoice('reject', { hostname: PROD_HOST, store })
    applyChoice('reject', { hostname: PROD_HOST, store })
    expect(cookieNamesInJar()).toEqual(['hs_keep'])
  })
})

describe('the revocation cannot be bypassed by the tag path', () => {
  it('ensureTag after a Reject neither injects nor unmutes the senders', async () => {
    // ensureTag is exported and `revoked` does not gate it. It must stay harmless:
    // `loaded` short-circuits it, and nothing there touches the flag.
    const { applyChoice, analytics } = await loadSeam()
    const store = memoryStorage()
    applyChoice('accept', { hostname: PROD_HOST, store })
    expect(analytics.tagIsLoaded()).toBe(true)
    applyChoice('reject', { hostname: PROD_HOST, store })
    const muted = eventEntries().length

    analytics.ensureTag(PROD_HOST, { analytics: true, ts: '', v: 1 })

    expect(document.querySelectorAll('script[src*="googletagmanager"]').length).toBe(1)
    analytics.trackDemoOpen('nav')
    expect(eventEntries().length, 'ensureTag cleared the revocation').toBe(muted)
  })

  it('a milestone crossed while revoked never fires on a later Accept', async () => {
    // trackScrollDepth marks a milestone fired BEFORE send() drops it, so scroll that
    // happened while the visitor had declined is not reported retroactively. Pinned so
    // the behaviour cannot flip silently into back-filling denied-period scroll.
    const { applyChoice, analytics } = await loadSeam()
    const store = memoryStorage()
    applyChoice('accept', { hostname: PROD_HOST, store })
    applyChoice('reject', { hostname: PROD_HOST, store })
    analytics.trackScrollDepth(30)
    const muted = eventEntries().length

    applyChoice('accept', { hostname: PROD_HOST, store })
    analytics.trackScrollDepth(30)
    expect(eventEntries().length, 'the 25 milestone was back-filled after re-consent').toBe(muted)

    // An uncrossed one still fires, so the case above is not passing because scroll
    // tracking is dead altogether.
    analytics.trackScrollDepth(60)
    expect(eventEntries().length, 'an uncrossed milestone did not fire after re-consent').toBe(muted + 1)
  })
})

describe('applyChoice is the only writer of the revocation flag', () => {
  const files = readdirSync(HERE, { recursive: true, encoding: 'utf8' })
    .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f))
    .map((f) => join(HERE, f))

  it('control: the scan reads the package and finds the seam', () => {
    expect(files.length, 'the source scan found nothing to read').toBeGreaterThan(10)
    const combined = files.map((f) => readFileSync(f, 'utf8')).join('\n')
    expect(combined).toContain('applyChoice')
    expect(combined).toContain('setAnalyticsRevoked')
  })

  it('exactly one production call site, in consentActions.ts', () => {
    // A second caller — a stray setAnalyticsRevoked(false) on any other path — would
    // reopen the hole with every behavioural spec still green.
    const callers = files.filter((f) => {
      const src = readFileSync(f, 'utf8')
      return /setAnalyticsRevoked\s*\(/.test(src) && !/export function setAnalyticsRevoked/.test(src)
    })
    expect(callers.map((f) => f.replace(`${HERE}/`, ''))).toEqual(['consentActions.ts'])
    const src = readFileSync(join(HERE, 'consentActions.ts'), 'utf8')
    expect((src.match(/setAnalyticsRevoked\s*\(/g) ?? []).length, 'more than one call in the seam').toBe(1)
    // Unconditional: a guarded call is what leaves the tag resident-but-muted.
    expect(src).toMatch(/^\s*setAnalyticsRevoked\(!accepted\)$/m)
  })
})
