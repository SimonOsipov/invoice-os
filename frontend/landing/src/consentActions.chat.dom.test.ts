// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Production URL on purpose: both gates need an exact production hostname. localStorage is
// installed because Node 25's own global shadows jsdom's (see consentActions.dom.test.tsx).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { writeConsent, type ConsentStore } from './consent'
import { BLOG_HOSTNAMES, LIBRARY_HOSTNAMES } from './hubspot'

type Sdk = { widget: { load: () => void; remove: () => void }; clear: (opts?: { resetWidget?: boolean }) => void }
type TestWindow = Window & { _hsq?: unknown[]; HubSpotConversations?: Sdk; hsConversationsSettings?: Record<string, unknown> }
type Actions = typeof import('./consentActions')

const w = window as TestWindow
const WWW = 'www.ascomply.com'
const FIVE = ['__hstc', 'hubspotutk', '__hssrc', '__hssc', 'messagesUtk']
const CONTAINER_ID = 'hubspot-messages-iframe-container'
const LOADER = 'script#hs-script-loader'
const GTAG = 'script[src^="https://www.googletagmanager.com/"]'

function memoryStorage(): ConsentStore {
  const map = new Map<string, string>()
  return {
    getItem: (k) => (map.has(k) ? map.get(k)! : null),
    setItem: (k, v) => void map.set(k, String(v)),
  }
}

function jarNames(): string[] {
  return document.cookie
    .split(';')
    .map((pair) => pair.split('=')[0].trim())
    .filter(Boolean)
}

function resetJar() {
  for (const name of jarNames()) {
    document.cookie = `${name}=; Max-Age=0; path=/`
    document.cookie = `${name}=; Max-Age=0; path=/; domain=.ascomply.com`
  }
}

function seed(...names: string[]) {
  for (const name of names) document.cookie = `${name}=v; path=/; domain=.ascomply.com`
}

const loaders = () => document.head.querySelectorAll(LOADER).length
const container = () => document.getElementById(CONTAINER_ID)

function installSdk() {
  const make = () => {
    if (container()) return
    const el = document.createElement('div')
    el.id = CONTAINER_ID
    document.body.appendChild(el)
  }
  make()
  w.HubSpotConversations = { widget: { load: make, remove: () => container()?.remove() }, clear: () => undefined }
}

async function fresh(): Promise<Actions> {
  return import('./consentActions')
}

beforeEach(() => {
  Object.defineProperty(globalThis, 'localStorage', { value: memoryStorage(), configurable: true, writable: true, enumerable: true })
  document.head.innerHTML = ''
  document.body.innerHTML = ''
  delete w._hsq
  delete w.HubSpotConversations
  delete w.hsConversationsSettings
  resetJar()
  vi.resetModules()
  vi.stubEnv('VITE_GA_MEASUREMENT_ID', 'G-E409H76XYY')
})

afterEach(() => {
  resetJar()
  vi.unstubAllEnvs()
})

describe('applyChoice', () => {
  it('AC3: Accept loads the chat beside the tag', async () => {
    const { applyChoice } = await fresh()
    applyChoice('accept')
    expect(loaders()).toBe(1)
    expect(document.head.querySelectorAll(GTAG).length).toBe(1)
  })

  it('AC3: Accept loads the chat with no measurement id', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', '')
    const { applyChoice } = await fresh()
    applyChoice('accept')
    expect(loaders()).toBe(1)
    expect(document.head.querySelectorAll(GTAG).length).toBe(0)
  })

  it('AC4: Reject after Accept removes the chat', async () => {
    const { applyChoice } = await fresh()
    applyChoice('accept')
    installSdk()
    seed('__hstc')
    expect(container()).not.toBeNull()
    applyChoice('reject')
    expect(container()).toBeNull()
    expect(w._hsq?.at(-1)).toEqual(['doNotTrack'])
    expect(jarNames()).not.toContain('__hstc')
  })

  it('AC3: a first Reject loads nothing and pushes nothing', async () => {
    const { applyChoice } = await fresh()
    applyChoice('reject')
    expect(loaders()).toBe(0)
    expect(w._hsq).toBeUndefined()
  })

  it('the blog and library host lists load no chat', async () => {
    const { applyChoice, syncConsent } = await fresh()
    for (const [hostname, hosts] of [
      ['blog.ascomply.com', BLOG_HOSTNAMES],
      ['library.ascomply.com', LIBRARY_HOSTNAMES],
    ] as const) {
      applyChoice('accept', { hostname, hosts })
      syncConsent({ hostname, hosts })
      expect(loaders(), hostname).toBe(0)
    }
  })

  it('DC-20: Reject on the blog host deletes the shared HubSpot cookies and loads nothing', async () => {
    seed(...FIVE)
    const { applyChoice } = await fresh()
    applyChoice('reject', { hostname: 'blog.ascomply.com', hosts: BLOG_HOSTNAMES })
    expect(jarNames().filter((n) => FIVE.includes(n))).toEqual([])
    expect(loaders()).toBe(0)
    expect(w._hsq).toBeUndefined()
  })
})

describe('syncConsent', () => {
  it('AC3: loads the chat after another tab accepts', async () => {
    writeConsent(true, null, undefined, undefined, WWW)
    const { syncConsent } = await fresh()
    syncConsent()
    expect(loaders()).toBe(1)
  })

  it('AC3: removes the chat after another tab rejects', async () => {
    const { applyChoice, syncConsent } = await fresh()
    applyChoice('accept')
    installSdk()
    writeConsent(false, null, new Date(Date.now() + 1000), undefined, WWW)
    syncConsent()
    expect(container()).toBeNull()
    expect(w._hsq?.at(-1)).toEqual(['doNotTrack'])
  })

  it('with no record changes nothing', async () => {
    const { syncConsent } = await fresh()
    expect(syncConsent()).toBeNull()
    expect(loaders()).toBe(0)
  })

  it('DC-20: a denied record on the library host deletes the HubSpot cookies', async () => {
    writeConsent(false, null, undefined, undefined, WWW)
    seed('__hssc')
    const { syncConsent } = await fresh()
    syncConsent({ hostname: 'library.ascomply.com', hosts: LIBRARY_HOSTNAMES })
    expect(jarNames()).not.toContain('__hssc')
    expect(loaders()).toBe(0)
  })
})

