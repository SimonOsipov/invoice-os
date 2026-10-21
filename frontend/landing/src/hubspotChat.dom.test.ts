// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// jsdom cannot separate host-only cookies from domain cookies, so the cookie specs
// seed on `.ascomply.com` and read the jar back.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_VERSION, type ConsentRecord } from './consent'

type Sdk = { widget: { load: () => void; remove: () => void }; clear: (opts?: { resetWidget?: boolean }) => void }
type TestWindow = Window & {
  _hsq?: unknown[]
  HubSpotConversations?: Sdk
  hsConversationsSettings?: Record<string, unknown>
}
type ChatModule = typeof import('./hubspotChat')

const GRANTED: ConsentRecord = { analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }
const DENIED: ConsentRecord = { analytics: false, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }
const WWW = 'www.ascomply.com'
const PR_HOST = 'landing-pr-42.up.railway.app'
const FIVE = ['__hstc', 'hubspotutk', '__hssrc', '__hssc', 'messagesUtk']
const CONTAINER_ID = 'hubspot-messages-iframe-container'

const w = window as TestWindow

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

function loaderScripts() {
  return document.head.querySelectorAll('script[src*="hs-scripts.com"]')
}

function container() {
  return document.getElementById(CONTAINER_ID)
}

/** Mirrors the vendor document: remove() deletes the container, load() re-creates it. */
function installSdk(): Sdk {
  const make = () => {
    if (container()) return
    const el = document.createElement('div')
    el.id = CONTAINER_ID
    document.body.appendChild(el)
  }
  make()
  const sdk: Sdk = {
    widget: { load: vi.fn(make), remove: vi.fn(() => container()?.remove()) },
    clear: vi.fn(),
  }
  w.HubSpotConversations = sdk
  return sdk
}

async function fresh(): Promise<ChatModule> {
  return import('./hubspotChat')
}

beforeEach(() => {
  document.head.innerHTML = ''
  document.body.innerHTML = ''
  delete w._hsq
  delete w.HubSpotConversations
  delete w.hsConversationsSettings
  resetJar()
  vi.resetModules()
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('loadChat gate', () => {
  it('AC2: a production host with Accept injects the loader', async () => {
    const { loadChat } = await fresh()
    expect(loadChat(WWW, GRANTED)).toBe(true)
    const scripts = document.head.querySelectorAll('script#hs-script-loader')
    expect(scripts.length).toBe(1)
    const script = scripts[0] as HTMLScriptElement
    expect(script.src).toBe('https://js-eu1.hs-scripts.com/148915098.js')
    expect(script.async).toBe(true)
  })

  it('AC2: a production host with Reject injects nothing', async () => {
    const { loadChat } = await fresh()
    expect(loadChat(WWW, DENIED)).toBe(false)
    expect(loaderScripts().length).toBe(0)
    expect(w._hsq).toBeUndefined()
  })

  it('AC2: a production host with no record injects nothing', async () => {
    const { loadChat } = await fresh()
    expect(loadChat(WWW, null)).toBe(false)
    expect(loaderScripts().length).toBe(0)
  })

  it('AC2: a PR host with Accept injects nothing', async () => {
    const { loadChat } = await fresh()
    expect(loadChat(PR_HOST, GRANTED)).toBe(false)
    expect(loaderScripts().length).toBe(0)
  })

  it('a lookalike host injects nothing', async () => {
    const { loadChat } = await fresh()
    expect(loadChat('www.ascomply.com.attacker.example', GRANTED)).toBe(false)
    expect(loadChat('ascomply.com', GRANTED)).toBe(false)
    expect(loaderScripts().length).toBe(0)
  })

  it('AC1: loadChat is idempotent', async () => {
    const { loadChat } = await fresh()
    expect([loadChat(WWW, GRANTED), loadChat(WWW, GRANTED), loadChat(WWW, GRANTED)]).toEqual([true, true, true])
    expect(loaderScripts().length).toBe(1)
  })

  it('the gate needs no measurement id', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', '')
    const { loadChat } = await fresh()
    loadChat(WWW, GRANTED)
    expect(loaderScripts().length).toBe(1)
  })

  it('a host list argument replaces the default', async () => {
    const { loadChat } = await fresh()
    expect(loadChat('blog.ascomply.com', GRANTED)).toBe(false)
    expect(loadChat('blog.ascomply.com', GRANTED, ['blog.ascomply.com'])).toBe(true)
    expect(loaderScripts().length).toBe(1)
  })

  it('AC1: bootChat reads the stored record for this host', async () => {
    const record = encodeURIComponent(JSON.stringify(GRANTED))
    document.cookie = `asc_consent=${record}; path=/; domain=.ascomply.com`
    const { bootChat } = await fresh()
    expect(bootChat()).toBe(true)
    expect(loaderScripts().length).toBe(1)
  })
})

describe('removeChat', () => {
  async function loaded() {
    const mod = await fresh()
    mod.loadChat(WWW, GRANTED)
    return mod
  }

  it('AC4: Reject after Accept removes the widget, mutes tracking and deletes the five cookies', async () => {
    const { removeChat } = await loaded()
    const sdk = installSdk()
    seed(...FIVE)
    expect(jarNames().sort(), 'seed did not land').toEqual([...FIVE].sort())

    removeChat(WWW, DENIED)

    expect(container()).toBeNull()
    expect(sdk.widget.remove).toHaveBeenCalledTimes(1)
    expect(sdk.clear).toHaveBeenCalledTimes(1)
    expect(vi.mocked(sdk.clear).mock.calls[0]).toEqual([])
    expect(w._hsq?.at(-1)).toEqual(['doNotTrack'])
    expect(w.hsConversationsSettings?.loadImmediately).toBe(false)
    for (const name of FIVE) expect(jarNames()).not.toContain(name)
  })

  it('AC4: the order is settings, remove, clear, doNotTrack, cookies', async () => {
    const { removeChat } = await loaded()
    const sdk = installSdk()
    seed('__hstc')
    const order: string[] = []
    vi.mocked(sdk.widget.remove).mockImplementation(() => {
      order.push(`remove:${String(w.hsConversationsSettings?.loadImmediately)}`)
    })
    vi.mocked(sdk.clear).mockImplementation(() => {
      order.push('clear')
    })
    const hsq: unknown[] = []
    hsq.push = (...items: unknown[]) => {
      order.push('doNotTrack')
      return Array.prototype.push.apply(hsq, items)
    }
    w._hsq = hsq
    const jar = vi.spyOn(document, 'cookie', 'set')
    jar.mockImplementation(() => {
      order.push('cookie')
    })

    removeChat(WWW, DENIED)

    expect(order.slice(0, 3)).toEqual(['remove:false', 'clear', 'doNotTrack'])
    expect(order.indexOf('cookie')).toBeGreaterThan(order.indexOf('doNotTrack'))
  })

  it('AC4: an absent SDK is skipped without a throw', async () => {
    const { removeChat } = await loaded()
    seed(...FIVE)
    expect(() => removeChat(WWW, DENIED)).not.toThrow()
    expect(w._hsq).toEqual([['doNotTrack']])
    for (const name of FIVE) expect(jarNames()).not.toContain(name)
  })

  it('a Reject with nothing loaded pushes no doNotTrack', async () => {
    const { removeChat } = await fresh()
    seed('__hstc')
    removeChat(WWW, DENIED)
    expect(w._hsq).toBeUndefined()
    expect(jarNames()).not.toContain('__hstc')
  })

  it('removeChat on a PR host or with a granted record changes nothing', async () => {
    const { removeChat } = await loaded()
    const sdk = installSdk()
    seed('__hstc')
    removeChat(PR_HOST, DENIED)
    removeChat(WWW, GRANTED)
    expect(sdk.widget.remove).not.toHaveBeenCalled()
    expect(JSON.stringify(w._hsq ?? [])).not.toContain('doNotTrack')
    expect(jarNames()).toContain('__hstc')
  })

  it('a repeated Reject writes no cookie', async () => {
    const { removeChat } = await loaded()
    removeChat(WWW, DENIED)
    expect(jarNames().filter((n) => FIVE.includes(n))).toEqual([])
    const setter = vi.spyOn(document, 'cookie', 'set')
    removeChat(WWW, DENIED)
    expect(setter).not.toHaveBeenCalled()
  })
})

describe('state machine', () => {
  it('Accept after Reject re-shows the widget without a second script', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    const sdk = installSdk()
    removeChat(WWW, DENIED)
    expect(container()).toBeNull()

    expect(loadChat(WWW, GRANTED)).toBe(true)

    expect(sdk.widget.load).toHaveBeenCalledTimes(1)
    expect(w.hsConversationsSettings?.loadImmediately).toBe(true)
    expect(loaderScripts().length).toBe(1)
    expect(container()).not.toBeNull()
  })

  it('PM-29: Reject before the SDK exists defers the widget and queues doNotTrack', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    expect(() => removeChat(WWW, DENIED)).not.toThrow()
    expect(w.hsConversationsSettings?.loadImmediately).toBe(false)
    expect(w._hsq).toEqual([['doNotTrack']])
  })

  it('PM-29: Accept after that, with the SDK still absent, lets the embed load the widget', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    expect(loadChat(WWW, GRANTED)).toBe(true)
    expect(w.hsConversationsSettings?.loadImmediately).toBe(true)
    expect(loaderScripts().length).toBe(1)
  })

  it('PM-29: Accept after that, with the deferred stub present, calls widget.load', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    const load = vi.fn()
    w.HubSpotConversations = { widget: { load, remove: vi.fn() }, clear: vi.fn() }
    loadChat(WWW, GRANTED)
    expect(load).toHaveBeenCalledTimes(1)
  })
})

describe('opt-out lift', () => {
  it('DC-6: Accept in a later page load lifts the opt-out before the loader', async () => {
    seed('__hs_do_not_track')
    const { loadChat } = await fresh()
    let hsqAtAppend: unknown[] | undefined
    const append = document.head.appendChild.bind(document.head)
    vi.spyOn(document.head, 'appendChild').mockImplementation((node) => {
      hsqAtAppend = [...(w._hsq ?? [])]
      return append(node)
    })

    loadChat(WWW, GRANTED)

    expect(w._hsq?.[0]).toEqual(['doNotTrack', { track: true }])
    expect(hsqAtAppend).toEqual([['doNotTrack', { track: true }]])
  })

  it('DC-6: Accept with no opt-out cookie pushes nothing', async () => {
    const { loadChat } = await fresh()
    loadChat(WWW, GRANTED)
    expect(w._hsq).toBeUndefined()
  })

  it('DC-6: Accept after Reject in one page lifts the opt-out', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    seed('__hs_do_not_track')
    loadChat(WWW, GRANTED)
    expect(w._hsq?.at(-1)).toEqual(['doNotTrack', { track: true }])
  })
})

describe('HubSpot cookie cleanup', () => {
  it('DC-20: clearHubSpotCookies expires only the HubSpot names present', async () => {
    seed('__hstc', '_ga')
    const { clearHubSpotCookies } = await fresh()
    expect(clearHubSpotCookies(WWW)).toEqual(['__hstc'])
    expect(jarNames()).not.toContain('__hstc')
    expect(jarNames()).toContain('_ga')
  })

  it('DC-20: bootChat with a denied record deletes leftover HubSpot cookies', async () => {
    const record = encodeURIComponent(JSON.stringify(DENIED))
    document.cookie = `asc_consent=${record}; path=/; domain=.ascomply.com`
    seed('hubspotutk')
    const { bootChat } = await fresh()
    expect(bootChat()).toBe(false)
    expect(loaderScripts().length).toBe(0)
    expect(jarNames()).not.toContain('hubspotutk')
  })
})
