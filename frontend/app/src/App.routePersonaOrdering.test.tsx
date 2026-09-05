// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// ROUTE-01-05, Mode A. Pins Core AC 6/7: no history write ever carries `?persona=`, and the
// structural reason it cannot -- App.tsx:1518 starts `seat` at null whenever `?persona=`
// names an openable persona, so Workspace (and the whole router seam) never mounts on that
// commit. Harness is App.routeNavigate.test.tsx's: the real <App/>, a session in a stubbed
// localStorage, ctx captured through a mocked Sidebar.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }

// Node v25's native localStorage collides with jsdom's (App.standIn.test.tsx:74-75).
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
// then waits for the persona hand-off (mint -> /me, no gateway in this harness so it
// resolves with no network call) to settle into a mounted Workspace.
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

describe('AC-1: no history write performed by the seam ever contains persona=', () => {
  it('ordering_noHistoryWriteEverCarriesThePersonaParam', async () => {
    window.history.replaceState(null, '', '/?persona=firm')
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    const calls = installHistorySpies()

    await mountApp()
    const ctx = requireCtx()
    await act(async () => {
      ctx.nav('audit')
    })

    // Vacuity floor: an unmounted Workspace or a spy installed too late leaves this array
    // empty, and the negative assertion below would pass on a completely broken feature.
    expect(calls.length, 'no history write was ever recorded -- the spy or the mount is broken').toBeGreaterThan(0)
    expect(
      calls.some((c) => /persona=/.test(c.url)),
      'a history write carried the persona param forward',
    ).toBe(false)

    // Ordering check on searchAtCallTime: the strip effect's OWN write necessarily fires
    // while persona= is still live (that is what stripping means), so asserting it never
    // happens anywhere is wrong. What must hold is that OUR nav('audit') push -- the seam's
    // write -- fires only after the strip has already run.
    const navCall = calls.find((c) => c.url.startsWith('/audit'))
    expect(navCall, "no history write for nav('audit') was recorded").toBeDefined()
    expect(
      navCall!.searchAtCallTime.includes('persona'),
      "nav('audit') pushed before persona= was stripped from the live URL",
    ).toBe(false)
  })
})

describe('AC-2: Workspace provably does not mount while ?persona= is live', () => {
  it('ordering_workspaceDoesNotMountWhileThePersonaParamIsLive', async () => {
    window.history.replaceState(null, '', '/?persona=firm')
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))

    await mountApp()

    expect(firstSidebarSearch, 'Sidebar never rendered -- no search string was ever captured').toBeDefined()
    expect(
      firstSidebarSearch,
      'the first render inside Workspace must never see a live persona param',
    ).not.toMatch(/persona/)
  })

  it('ordering_theSeatInitialiserIsWhatGuaranteesIt', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/App.tsx'), 'utf8')
    expect(
      /autoPersona \? null :/.test(src),
      'App.tsx no longer starts `seat` at null when autoPersona is set -- the ordering claim above must be re-derived',
    ).toBe(true)
  })
})

describe('AC-4: ?persona= does not survive the hand-off', () => {
  it('ordering_thePersonaParamDoesNotSurviveTheHandOff', async () => {
    window.history.replaceState(null, '', '/?persona=firm')

    await mountApp()

    expect(new URLSearchParams(window.location.search).has('persona')).toBe(false)
  })
})

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
