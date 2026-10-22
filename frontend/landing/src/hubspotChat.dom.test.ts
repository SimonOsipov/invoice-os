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
  hsConversationsOnReady?: Array<() => void>
}
type ChatModule = typeof import('./hubspotChat')

const GRANTED: ConsentRecord = { analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }
const DENIED: ConsentRecord = { analytics: false, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }
const WWW = 'www.ascomply.com'
const PR_HOST = 'landing-pr-42.up.railway.app'
const COOKIES = ['__hstc', 'hubspotutk', '__hssrc', '__hssc', 'messagesUtk', 'hs-messages-is-open', 'hs-messages-hide-welcome-message']
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
  delete w.hsConversationsOnReady
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

  it('AC4: Reject after Accept removes the widget, mutes tracking and deletes the HubSpot cookies', async () => {
    const { removeChat } = await loaded()
    const sdk = installSdk()
    seed(...COOKIES)
    expect(jarNames().sort(), 'seed did not land').toEqual([...COOKIES].sort())

    removeChat(WWW, DENIED)

    expect(container()).toBeNull()
    expect(sdk.widget.remove).toHaveBeenCalledTimes(1)
    expect(sdk.clear).toHaveBeenCalledTimes(1)
    expect(vi.mocked(sdk.clear).mock.calls[0]).toEqual([])
    expect(w._hsq?.at(-1)).toEqual(['doNotTrack'])
    expect(w.hsConversationsSettings?.loadImmediately).toBe(false)
    for (const name of COOKIES) expect(jarNames()).not.toContain(name)
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
    seed(...COOKIES)
    expect(() => removeChat(WWW, DENIED)).not.toThrow()
    expect(w._hsq).toEqual([['doNotTrack']])
    for (const name of COOKIES) expect(jarNames()).not.toContain(name)
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
    expect(jarNames().filter((n) => COOKIES.includes(n))).toEqual([])
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

describe('re-Accept races', () => {
  it('Accept, Reject, Accept before the loader runs ends on doNotTrack track:true', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    loadChat(WWW, GRANTED)
    const last = (w._hsq ?? []).filter((c) => (c as unknown[])[0] === 'doNotTrack').at(-1)
    expect(last).toEqual(['doNotTrack', { track: true }])
  })

  it('a widget.load that throws does not escape loadChat', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    w.HubSpotConversations = {
      widget: { load: vi.fn(() => { throw new Error('vendor') }), remove: vi.fn() },
      clear: vi.fn(),
    }
    expect(loadChat(WWW, GRANTED)).toBe(true)
  })

  it('re-Accept with the SDK absent queues widget.load on hsConversationsOnReady', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    loadChat(WWW, GRANTED)
    const load = vi.fn()
    const queue = w.hsConversationsOnReady ?? []
    expect(queue.length, 'a callback is queued').toBe(1)
    w.HubSpotConversations = { widget: { load, remove: vi.fn() }, clear: vi.fn() }
    queue.forEach((fn) => fn())
    expect(load).toHaveBeenCalledTimes(1)
  })

  it('a queued widget.load does nothing when the visitor rejected again', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    const load = vi.fn()
    w.HubSpotConversations = { widget: { load, remove: vi.fn() }, clear: vi.fn() }
    w.hsConversationsOnReady?.forEach((fn) => fn())
    expect(load).not.toHaveBeenCalled()
  })

  it('a queued widget.load that throws does not escape', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    loadChat(WWW, GRANTED)
    w.HubSpotConversations = {
      widget: { load: vi.fn(() => { throw new Error('vendor') }), remove: vi.fn() },
      clear: vi.fn(),
    }
    expect(() => w.hsConversationsOnReady?.forEach((fn) => fn())).not.toThrow()
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

describe('adversarial', () => {
  it('the exported constants hold the vendor values', async () => {
    const m = await fresh()
    expect(m.HUBSPOT_PORTAL_ID).toBe('148915098')
    expect(m.CHAT_SCRIPT_HOST).toBe('js-eu1.hs-scripts.com')
    expect(m.CHAT_SCRIPT_URL).toBe('https://js-eu1.hs-scripts.com/148915098.js')
    expect(m.CHAT_SCRIPT_ID).toBe('hs-script-loader')
    expect(m.HUBSPOT_COOKIE_NAMES).toEqual(COOKIES)
    expect(m.HUBSPOT_CHAT_DOMAINS.length).toBeGreaterThan(0)
    expect(m.HUBSPOT_CHAT_DOMAINS).toContain('usemessages.com')
  })

  it('bootChat with no stored record loads nothing', async () => {
    const { bootChat } = await fresh()
    expect(bootChat()).toBe(false)
    expect(loaderScripts().length).toBe(0)
  })

  it('bootChat on a host outside its list loads nothing even when granted', async () => {
    document.cookie = `asc_consent=${encodeURIComponent(JSON.stringify(GRANTED))}; path=/; domain=.ascomply.com`
    const { bootChat } = await fresh()
    expect(bootChat(['blog.ascomply.com'])).toBe(false)
    expect(loaderScripts().length).toBe(0)
  })

  it('a lookalike host and a mixed-case production host on Reject', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    const sdk = installSdk()
    seed('__hstc')
    removeChat('www.ascomply.com.attacker.example', DENIED)
    expect(sdk.widget.remove).not.toHaveBeenCalled()
    expect(jarNames()).toContain('__hstc')
    removeChat('WWW.Ascomply.com', DENIED)
    expect(sdk.widget.remove).toHaveBeenCalledTimes(1)
    expect(jarNames()).not.toContain('__hstc')
  })

  it('removeChat honours a host list argument', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat('blog.ascomply.com', GRANTED, ['blog.ascomply.com'])
    const sdk = installSdk()
    removeChat('blog.ascomply.com', DENIED)
    expect(sdk.widget.remove).not.toHaveBeenCalled()
    removeChat('blog.ascomply.com', DENIED, ['blog.ascomply.com'])
    expect(sdk.widget.remove).toHaveBeenCalledTimes(1)
  })

  it('a throwing SDK still mutes tracking and deletes the cookies', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    const sdk = installSdk()
    vi.mocked(sdk.widget.remove).mockImplementation(() => {
      throw new Error('vendor')
    })
    seed(...COOKIES)
    expect(() => removeChat(WWW, DENIED)).not.toThrow()
    expect(w._hsq).toEqual([['doNotTrack']])
    for (const name of COOKIES) expect(jarNames()).not.toContain(name)
  })

  it('a second Reject adds no second doNotTrack and no second remove', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    const sdk = installSdk()
    removeChat(WWW, DENIED)
    removeChat(WWW, DENIED)
    expect(w._hsq).toEqual([['doNotTrack']])
    expect(sdk.widget.remove).toHaveBeenCalledTimes(1)
  })

  it('Accept, Reject, Accept, Reject removes the widget both times with one script', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    const sdk = installSdk()
    removeChat(WWW, DENIED)
    loadChat(WWW, GRANTED)
    loadChat(WWW, GRANTED)
    removeChat(WWW, DENIED)
    expect(sdk.widget.load).toHaveBeenCalledTimes(1)
    expect(sdk.widget.remove).toHaveBeenCalledTimes(2)
    expect(container()).toBeNull()
    expect(w.hsConversationsSettings?.loadImmediately).toBe(false)
    expect(loaderScripts().length).toBe(1)
  })

  it('the opt-out lift is pushed before widget.load on a re-Accept', async () => {
    const { loadChat, removeChat } = await fresh()
    loadChat(WWW, GRANTED)
    const sdk = installSdk()
    removeChat(WWW, DENIED)
    seed('__hs_do_not_track')
    let hsqAtLoad: unknown[] | undefined
    vi.mocked(sdk.widget.load).mockImplementation(() => {
      hsqAtLoad = [...(w._hsq ?? [])]
    })
    loadChat(WWW, GRANTED)
    expect(hsqAtLoad?.at(-1)).toEqual(['doNotTrack', { track: true }])
  })

  it('the opt-out cookie is matched by exact name', async () => {
    seed('x__hs_do_not_track_y')
    const { loadChat } = await fresh()
    loadChat(WWW, GRANTED)
    expect(w._hsq).toBeUndefined()
  })

  it('a denied loadChat does not lift the opt-out', async () => {
    seed('__hs_do_not_track')
    const { loadChat } = await fresh()
    loadChat(WWW, DENIED)
    expect(w._hsq).toBeUndefined()
  })

  it('a repeated Accept while loaded does not call widget.load or touch the settings', async () => {
    const { loadChat } = await fresh()
    loadChat(WWW, GRANTED)
    const sdk = installSdk()
    loadChat(WWW, GRANTED)
    expect(sdk.widget.load).not.toHaveBeenCalled()
    expect(w.hsConversationsSettings).toBeUndefined()
  })
})
