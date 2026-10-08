// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://library.ascomply.com/" }
// The landing's gate, consent seam and senders, driven on the library host (LIB-07-01).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_STORAGE_KEY, CONSENT_VERSION, type ConsentRecord } from './consent'

type TestWindow = Window & { dataLayer?: IArguments[]; gtag?: (...args: unknown[]) => void }

const ID = 'G-E409H76XYY'
const LIB = 'library.ascomply.com'
const GRANTED: ConsentRecord = { analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }
const TAG = `script[src="https://www.googletagmanager.com/gtag/js?id=${ID}"]`
const ANY_TAG = 'script[src^="https://www.googletagmanager.com/"]'

const entries = () => ((window as TestWindow).dataLayer ?? []).map((a) => Array.from(a))
const events = () => entries().filter((e) => e[0] === 'event')

// Node's own localStorage global shadows jsdom's on some versions; install a known store.
function installStorage(): void {
  const map = new Map<string, string>()
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    writable: true,
    value: {
      getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
      setItem: (k: string, v: string) => void map.set(k, String(v)),
    },
  })
}

beforeEach(() => {
  document.head.innerHTML = ''
  installStorage()
  const w = window as TestWindow
  delete w.dataLayer
  delete w.gtag
  vi.resetModules()
  vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
  delete (document as { cookie?: string }).cookie
  delete (globalThis as { localStorage?: unknown }).localStorage
})

describe('shouldLoadTag with a host list', () => {
  it('LS-01 the library list opens the gate on the library host only', async () => {
    const { shouldLoadTag } = await import('./analytics')
    const { LIBRARY_HOSTNAMES } = await import('./hubspot')
    for (const h of [LIB, 'LIBRARY.ASCOMPLY.COM', ' library.ascomply.com ']) {
      expect(shouldLoadTag(h, true, ID, LIBRARY_HOSTNAMES), h).toBe(true)
    }
    for (const h of [
      'www.ascomply.com',
      'pr-12-library.up.railway.app',
      'library.ascomply.com.evil.example',
      'library.ascomply.com.',
      'localhost',
      '',
    ]) {
      expect(shouldLoadTag(h, true, ID, LIBRARY_HOSTNAMES), h).toBe(false)
    }
  })

  it("LS-02 with no list the gate is the landing's", async () => {
    const { shouldLoadTag } = await import('./analytics')
    const { LIBRARY_HOSTNAMES, PRODUCTION_HOSTNAMES } = await import('./hubspot')
    expect(shouldLoadTag(LIB, true, ID)).toBe(false)
    expect(shouldLoadTag('www.ascomply.com', true, ID)).toBe(true)
    expect(LIBRARY_HOSTNAMES).toEqual([LIB])
    expect(LIBRARY_HOSTNAMES.filter((h) => PRODUCTION_HOSTNAMES.includes(h))).toEqual([])
  })

  it('LS-03 a closed gate holds without consent or without an id', async () => {
    const { shouldLoadTag } = await import('./analytics')
    const { LIBRARY_HOSTNAMES } = await import('./hubspot')
    expect(shouldLoadTag(LIB, false, ID, LIBRARY_HOSTNAMES)).toBe(false)
    expect(shouldLoadTag(LIB, true, null, LIBRARY_HOSTNAMES)).toBe(false)
  })
})

describe('boot and Accept on the library host', () => {
  it('LS-04 boot on the library host loads the tag for a stored grant only', async () => {
    const { bootAnalytics } = await import('./analytics')
    const { LIBRARY_HOSTNAMES } = await import('./hubspot')

    expect(bootAnalytics(LIBRARY_HOSTNAMES)).toBe(false)
    expect(document.querySelectorAll(ANY_TAG).length).toBe(0)

    localStorage.setItem(CONSENT_STORAGE_KEY, JSON.stringify({ analytics: true, v: CONSENT_VERSION }))
    expect(bootAnalytics()).toBe(false)
    expect(document.querySelectorAll(ANY_TAG).length).toBe(0)

    expect(bootAnalytics(LIBRARY_HOSTNAMES)).toBe(true)
    expect(document.querySelectorAll(TAG).length).toBe(1)
  })

  it('LS-05 Accept on the library host writes the record and loads the tag', async () => {
    const { applyChoice } = await import('./consentActions')
    const { LIBRARY_HOSTNAMES } = await import('./hubspot')

    applyChoice('accept', { hosts: LIBRARY_HOSTNAMES })
    const stored = JSON.parse(localStorage.getItem(CONSENT_STORAGE_KEY)!)
    expect(stored).toMatchObject({ analytics: true, v: CONSENT_VERSION })
    expect(document.querySelectorAll(TAG).length).toBe(1)
    expect(entries().map((e) => e[0])).toEqual(['js', 'config'])
    expect(entries()[1]).toEqual(['config', ID])
  })

  it('LS-05 a cookie domain lands in the config call', async () => {
    const { applyChoice } = await import('./consentActions')
    const { LIBRARY_HOSTNAMES } = await import('./hubspot')
    applyChoice('accept', { hosts: LIBRARY_HOSTNAMES, cookieDomain: LIB })
    expect(entries()[1]).toEqual(['config', ID, { cookie_domain: LIB }])
  })

  it('LS-05 Accept with no hosts writes the record and loads nothing', async () => {
    const { applyChoice } = await import('./consentActions')
    applyChoice('accept')
    expect(JSON.parse(localStorage.getItem(CONSENT_STORAGE_KEY)!).analytics).toBe(true)
    expect(document.querySelectorAll(ANY_TAG).length).toBe(0)
  })
})

describe('Reject on the library host', () => {
  function spyOnCookieWrites(): string[] {
    const writes: string[] = []
    Object.defineProperty(document, 'cookie', {
      configurable: true,
      get: () => '_ga=x',
      set: (v: string) => void writes.push(v),
    })
    return writes
  }

  it('LS-06 Reject with a cookie domain expires _ga on that domain only', async () => {
    const { applyChoice } = await import('./consentActions')
    const { LIBRARY_HOSTNAMES } = await import('./hubspot')
    const writes = spyOnCookieWrites()

    applyChoice('reject', { hosts: LIBRARY_HOSTNAMES, cookieDomain: LIB })

    expect(JSON.parse(localStorage.getItem(CONSENT_STORAGE_KEY)!).analytics).toBe(false)
    expect(document.querySelectorAll(ANY_TAG).length).toBe(0)
    expect(writes.length).toBe(2)
    expect(writes.filter((w) => !w.includes('domain='))).toHaveLength(1)
    expect(writes.filter((w) => w.endsWith(`; domain=${LIB}`))).toHaveLength(1)
    for (const w of writes) {
      expect(w).not.toContain('domain=ascomply.com')
      expect(w).not.toContain('domain=.ascomply.com')
    }
  })

  it('LS-06 control: with no cookie domain the sweep still reaches .ascomply.com', async () => {
    const { applyChoice } = await import('./consentActions')
    const writes = spyOnCookieWrites()
    applyChoice('reject')
    expect(writes.some((w) => w.includes('domain=.ascomply.com'))).toBe(true)
  })
})

describe('library senders', () => {
  async function loadTag() {
    const mod = await import('./analytics')
    const { LIBRARY_HOSTNAMES } = await import('./hubspot')
    expect(mod.ensureTag(LIB, GRANTED, LIBRARY_HOSTNAMES)).toBe(true)
    return { mod, LIBRARY_HOSTNAMES }
  }

  it('LS-07 each library sender pushes its event', async () => {
    const { mod } = await loadTag()
    mod.trackLibraryPageView()
    mod.trackLibraryDemoOpen()
    mod.trackTourStart()
    mod.trackOpenInPlatform({ feature_id: 'validate' })
    expect(events()).toEqual([
      ['event', 'page_view', {}],
      ['event', 'demo_open', { cta_location: 'library' }],
      ['event', 'tour_start', {}],
      ['event', 'open_in_platform', { feature_id: 'validate' }],
    ])
    mod.trackOpenInPlatform({ group_id: 'rules' })
    expect(events().at(-1)).toEqual(['event', 'open_in_platform', { group_id: 'rules' }])
    for (const a of (window as TestWindow).dataLayer!) {
      expect(Object.prototype.toString.call(a)).toBe('[object Arguments]')
    }
  })

  it('LS-08 a library sender before the tag touches nothing', async () => {
    const mod = await import('./analytics')
    mod.trackLibraryPageView()
    mod.trackLibraryDemoOpen()
    mod.trackTourStart()
    mod.trackOpenInPlatform({ feature_id: 'validate' })
    expect((window as TestWindow).dataLayer).toBeUndefined()
    expect((window as TestWindow).gtag).toBeUndefined()
  })

  it('LS-09 a library sender after Reject sends nothing', async () => {
    const { mod, LIBRARY_HOSTNAMES } = await loadTag()
    const { applyChoice } = await import('./consentActions')

    applyChoice('reject', { hosts: LIBRARY_HOSTNAMES })
    const before = events().length
    mod.trackLibraryPageView()
    mod.trackLibraryDemoOpen()
    mod.trackTourStart()
    mod.trackOpenInPlatform({ feature_id: 'validate' })
    expect(events().length).toBe(before)

    applyChoice('accept', { hosts: LIBRARY_HOSTNAMES })
    mod.trackTourStart()
    expect(events().length).toBe(before + 1)
  })
})
