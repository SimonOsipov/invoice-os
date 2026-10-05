// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// ROUTE-01-05, Mode A. No history write ever carries `?persona=`:
// the param is not a credential, so a stored seat boots on the first commit and the router
// seam drops the unowned param. Harness is App.routeNavigate.test.tsx's: the real <App/>, a
// session in a stubbed localStorage, ctx captured through a mocked Sidebar.

import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }

// Node v25's native localStorage collides with jsdom's.
function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    clear: vi.fn(() => {
      store.clear()
    }),
  }
}

let capturedCtx: PlatformCtx | undefined
// Sidebar renders only inside Workspace -- the first value captured here is the search
// string live at the moment Workspace's subtree first rendered, per decision
// [ordering-is-structural-and-tested].
let firstSidebarSearch: string | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    if (firstSidebarSearch === undefined) firstSidebarSearch = window.location.search
    capturedCtx = p.ctx
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  firstSidebarSearch = undefined
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

// Imports and renders the real App against whatever URL/localStorage the test already set,
// then waits for the Workspace to mount.
async function mountApp() {
  vi.resetModules()
  const { default: App } = await import('./App')
  render(<App />)
  await waitFor(() => requireCtx())
}

type HistoryCall = { url: string; searchAtCallTime: string }

// Wraps, rather than replaces, pushState/replaceState -- the real call must still land or
// every downstream effect (the strip, the mount alignment) would see a URL that never moved.
function installHistorySpies(): HistoryCall[] {
  const calls: HistoryCall[] = []
  const realPush = window.history.pushState.bind(window.history)
  const realReplace = window.history.replaceState.bind(window.history)
  vi.spyOn(window.history, 'pushState').mockImplementation((data, title, url) => {
    calls.push({ url: String(url), searchAtCallTime: window.location.search })
    realPush(data, title, url as string | URL | null | undefined)
  })
  vi.spyOn(window.history, 'replaceState').mockImplementation((data, title, url) => {
    calls.push({ url: String(url), searchAtCallTime: window.location.search })
    realReplace(data, title, url as string | URL | null | undefined)
  })
  return calls
}

describe('AC-1: an unrelated query string is dropped by a push, never carried', () => {
  // Delta over App.routeNavigate.test.tsx's nav_neverEchoesASearchStringThatAppearsAfterMount:
  // that test only reads the pushState SPY's argument. It never asserts on the LIVE
  // window.location.search once the push has actually landed -- a writer that dropped the
  // string from its own pushState argument but left a stale search sitting in the document
  // (e.g. via a stray separate write) would still pass it. This row checks both, on the
  // same post-mount-injection method (boot-time injection is confounded by the mount
  // alignment effect stripping search on every mount, per that file's own comment), and
  // uses TWO unrelated keys so a regex that strips only one named key still reds here.
  it('noEcho_anUnrelatedQueryStringIsDroppedByAPushRatherThanCarried', async () => {
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    await mountApp()
    window.history.replaceState(null, '', window.location.pathname + '?foo=1&bar=2')
    const pushSpy = vi.spyOn(window.history, 'pushState')

    const ctx = requireCtx()
    await act(async () => {
      ctx.nav('clients')
    })

    const call = pushSpy.mock.calls.find((c) => typeof c[2] === 'string' && c[2].startsWith('/clients'))
    expect(call, 'no pushState call to /clients was recorded').toBeDefined()
    expect(call![2], 'a writer that echoed search would emit /clients?foo=1&bar=2').toBe('/clients')
    expect(window.location.search, 'the live URL must carry no query string once the push has landed').toBe('')
  })
})

// `?persona=` is not a credential, so a stored seat boots on the first commit.
describe('a stored seat ignores ?persona=', () => {
  const INHOUSE_SEAT: Session = { persona: APP_PERSONAS.inhouse, token: null, me: null, verified: true }

  it('ordering_noHistoryWriteEverCarriesThePersonaParam_afterTwoNavigations', async () => {
    window.history.replaceState(null, '', '/?persona=firm')
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    const calls = installHistorySpies()

    await mountApp()
    const ctx = requireCtx()
    await act(async () => {
      ctx.nav('clients')
    })
    await act(async () => {
      ctx.nav('audit')
    })

    expect(calls.length, 'no history write was ever recorded -- the spy or the mount is broken').toBeGreaterThan(0)
    expect(calls.some((c) => /persona=/.test(c.url)), 'a history write carried the persona param').toBe(false)
  })

  it('ordering_aPersonaParamDoesNotDelayTheWorkspace', async () => {
    window.history.replaceState(null, '', '/?persona=firm')
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    vi.resetModules()
    const { default: App } = await import('./App')

    render(<App />)

    expect(firstSidebarSearch, 'Workspace did not render on the first commit').toBeDefined()
    expect(capturedCtx).toBeDefined()
  })

  it('ordering_theSeatInitialiserIgnoresThePersonaParam', async () => {
    const modes: Record<string, string | undefined> = {}
    for (const p of ['firm', 'bogus']) {
      cleanup()
      capturedCtx = undefined
      localStorage.setItem(SESSION_KEY, serializeSession(INHOUSE_SEAT))
      window.history.replaceState(null, '', `/?persona=${p}`)
      await mountApp()
      modes[p] = requireCtx().mode
    }

    expect(Object.keys(modes), 'both boots ran').toEqual(['firm', 'bogus'])
    expect(modes, 'the stored in-house seat wins under either param').toEqual({ firm: 'inhouse', bogus: 'inhouse' })
    expect(JSON.parse(localStorage.getItem(SESSION_KEY)!).personaId).toBe('inhouse')
  })

  it('ordering_thePersonaParamDoesNotSurviveTheFirstNavigation', async () => {
    window.history.replaceState(null, '', '/?persona=firm')
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    await mountApp()

    await act(async () => {
      requireCtx().nav('clients')
    })

    expect(window.location.pathname).toBe('/clients')
    expect(window.location.search, 'the live URL carries no query once the push has landed').toBe('')
  })
})
