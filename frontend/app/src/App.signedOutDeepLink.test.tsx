// @vitest-environment jsdom
// ROUTE-05-02: the capture call inside the front-door effect (App.tsx:1685-1689), which
// must run under the same activeSession/autoPersona guards as the bounce it precedes.

import { StrictMode } from 'react'
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { captureDestination, readDestination, DEEP_LINK_TTL_MS } from './lib/deepLink'
import type { Member } from './lib/members'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'
import App from './App'

let originalLocation: PropertyDescriptor | undefined

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

// Extends App.frontDoor.test.tsx's stubLocation (:40-53), local to this file only --
// that file's own specs must keep passing unchanged. Two additions: a `pathname`
// override (every capture spec here boots off '/'), and an `hrefWrites` recorder via
// `get`/`set href` accessors -- a plain field only remembers the last write, so a
// zero-write case would be indistinguishable from the untouched initial value.
// `hash` (QA addition) defaults to '' like the parent helper; only the hash spec below sets it.
function stubLocation(overrides: { search?: string; pathname?: string; hash?: string } = {}) {
  const hrefWrites: string[] = []
  Object.defineProperty(window, 'location', {
    configurable: true,
    writable: true,
    value: {
      get href() {
        return hrefWrites.length ? hrefWrites[hrefWrites.length - 1] : 'http://localhost/'
      },
      set href(v: string) {
        hrefWrites.push(v)
      },
      pathname: overrides.pathname ?? '/',
      hash: overrides.hash ?? '',
      hostname: 'localhost',
      origin: 'http://localhost',
      search: overrides.search ?? '',
    },
  })
  return { hrefWrites }
}

// Same two-signal check App.frontDoor.test.tsx's workspaceIsRendered() uses -- one alone
// could survive a half-done replacement.
function workspaceIsRendered(): boolean {
  return screen.queryByTestId('env-banner') !== null || document.querySelector('.pf-shell') !== null
}

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: 'tok', me: null, verified: true }
const REVIEW_ID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'

const MEMBER: Member = {
  id: 'm-restore-001',
  name: 'Tunde Bello',
  initials: 'TB',
  email: 'tunde@example.ng',
  role: 'preparer',
  status: 'active',
  isYou: false,
}

// ROUTE-05-03's restore specs need REAL jsdom navigation -- stubLocation() above replaces
// window.location with a static object that history.replaceState cannot update, so the
// mount-alignment effect's URL write would be invisible. Mirrors App.routeBoot.test.tsx's
// bootAt: a fresh module per boot (DEMO_MODE is read once at import time), a live session,
// dynamic import so the Sidebar mock above still applies.
async function bootWorkspaceAt(path: string, opts: { demoMode?: boolean; strict?: boolean } = {}) {
  window.history.replaceState(null, '', path)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  if (opts.demoMode) vi.stubEnv('VITE_DEMO_MODE', 'true')
  vi.resetModules()
  const { default: FreshApp } = await import('./App')
  return render(opts.strict ? (
    <StrictMode>
      <FreshApp />
    </StrictMode>
  ) : (
    <FreshApp />
  ))
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

// QA addition: captures ctx so the signed-out-transition spec can call ctx.signOut()
// directly, same pattern as App.suspended.test.tsx:52-57. Harmless no-op for every other
// spec in this file, which never reads capturedCtx.
let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  // sessionStorage has no Node-v25/jsdom collision (deepLink.test.ts:14-16) -- clearing
  // the real one is enough, no stub needed.
  sessionStorage.clear()
  window.history.replaceState(null, '', '/')
  capturedCtx = undefined
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
})

describe('front door: capturing the destination before the bounce (ROUTE-05-02)', () => {
  it('vacuity control: the extended stub overrides pathname and records hrefWrites', () => {
    // Without this, every "hrefWrites is empty"/"pathname is X" assertion below could be
    // passing because the harness silently ignores the override or the write, not
    // because the app behaved correctly.
    const { hrefWrites } = stubLocation({ pathname: '/audit' })
    expect(window.location.pathname).toBe('/audit')
    window.location.href = 'https://example.com/probe'
    expect(hrefWrites).toEqual(['https://example.com/probe'])
  })

  it('capture_aSessionlessDeepLinkIsStoredBeforeTheBounce', () => {
    const { hrefWrites } = stubLocation({ pathname: '/audit' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(readDestination()).toBe('/audit')
    expect(hrefWrites[hrefWrites.length - 1]).toBe('https://landing.example')
  })

  it('capture_theBounceUrlCarriesNothingExtra', () => {
    const { hrefWrites } = stubLocation({ pathname: '/audit' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    // Byte-equal: no suffix, no param, no hash appended to the landing base.
    expect(hrefWrites).toEqual(['https://landing.example'])
  })

  it('capture_theBareRootIsNotStored', () => {
    const { hrefWrites } = stubLocation({ pathname: '/' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(readDestination()).toBeNull()
    // The bounce itself is unaffected by the root's non-capturability.
    expect(hrefWrites).toEqual(['https://landing.example'])
  })

  it('capture_aLiveSessionStoresNothing', () => {
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    const { hrefWrites } = stubLocation({ pathname: '/audit' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(readDestination()).toBeNull()
    expect(hrefWrites).toEqual([])
    expect(workspaceIsRendered()).toBe(true)
  })

  it('capture_aLivePersonaParamStoresNothing', () => {
    const { hrefWrites } = stubLocation({ pathname: '/audit', search: '?persona=firm' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    // Deliberately un-awaited: with VITE_GATEWAY_URL unset the auto-sign-in resolves with
    // no network (auth.ts:89) fast enough that awaiting act() here would settle straight
    // through SignInLoading into Workspace, which is a different, unrelated assertion.
    // The guard this row targets (`|| autoPersona`) fires synchronously on mount, so the
    // capture check doesn't need the sign-in to finish.
    render(<App />)
    expect(readDestination()).toBeNull()
    expect(hrefWrites).toEqual([])
    expect(screen.getByText(/Signing in as/)).toBeTruthy()
    expect(workspaceIsRendered()).toBe(false)
  })

  it('capture_theShowcaseBuildStoresNothingAndStaysPut', () => {
    // VITE_LANDING_URL deliberately left unset -- landingBase() returns null (auth.ts:70-73).
    const { hrefWrites } = stubLocation({ pathname: '/audit' })
    render(<App />)
    expect(readDestination()).toBeNull()
    expect(hrefWrites).toEqual([])
    expect(screen.getByText('Choose an account')).toBeTruthy()
  })
})

// QA adversarial coverage (Stage 4). A path+?persona= combination is already exercised
// by capture_aLivePersonaParamStoresNothing above -- not repeated here.
describe('front door: adversarial coverage (QA)', () => {
  it('capture_aDeepPathWithAHashCapturesPathOnly', () => {
    // window.location.pathname structurally excludes the fragment, so a hash present at
    // boot must not leak into the stored value -- would catch a rewrite that read
    // `pathname + hash` instead of `pathname` alone.
    const { hrefWrites } = stubLocation({ pathname: '/audit', hash: '#x' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(readDestination()).toBe('/audit')
    expect(hrefWrites).toEqual(['https://landing.example'])
  })

  it('capture_aSecondSessionlessRenderOverwritesTheFirst', () => {
    // sessionStorage.setItem always overwrites; the most recent deep link is the one a
    // later consumer should replay. Would catch an accidental "only capture once" guard.
    const first = stubLocation({ pathname: '/audit' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    const { unmount } = render(<App />)
    expect(readDestination()).toBe('/audit')
    expect(first.hrefWrites).toEqual(['https://landing.example'])
    unmount()

    stubLocation({ pathname: '/reports' })
    render(<App />)
    expect(readDestination()).toBe('/reports')
  })

  it('capture_aSignedOutTransitionAlsoCapturesTheCurrentPath', () => {
    // Not a fresh boot: a LIVE session renders first (front-door effect's guard returns
    // early, nothing captured), then ctx.signOut() -- the same callback the 401 seam fires
    // (App.suspended.test.tsx:166) -- flips activeSession to null. The effect's dependency
    // array is [activeSession, autoPersona], so it re-runs and reaches the capture on this
    // transition, not just on a sessionless mount.
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    const { hrefWrites } = stubLocation({ pathname: '/reports' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(readDestination()).toBeNull()
    expect(hrefWrites).toEqual([])

    expect(capturedCtx?.signOut).toBeDefined()
    act(() => {
      capturedCtx?.signOut()
    })
    expect(readDestination()).toBe('/reports')
  })
})

// ROUTE-05-03, Mode A. The App-level capture above (ROUTE-05-02) stores the destination;
// this describe block is the OTHER half -- Workspace's boot restoring it. Not yet wired:
// App.tsx still seeds `view` from `parseRoute(window.location.pathname)` alone.
describe('Workspace boot: restoring the captured destination (ROUTE-05-03)', () => {
  it('restore_vacuityControl_theBootAtHelperObservesARealViewAndUrl', async () => {
    // Proves this describe block's harness (real jsdom navigation, dynamic re-import, the
    // mocked Sidebar's ctx capture) can see a genuine view+URL outcome -- independent of
    // the restore feature, which this boot never touches (nothing stored, a live
    // non-root path). Every "===null"/"was not restored" assertion below relies on this
    // plumbing actually working; if it were broken, this row would fail first.
    await bootWorkspaceAt('/settings')
    const ctx = requireCtx()
    expect(ctx.view, 'the harness must observe the real seeded view').toBe('settings')
    expect(window.location.pathname, 'the harness must observe the real jsdom pathname').toBe('/settings')
  })

  it('restore_aStoredDestinationSeedsTheBootView', async () => {
    captureDestination('/audit')
    await bootWorkspaceAt('/')
    const ctx = requireCtx()
    expect(ctx.view, `a stored /audit destination should seed view 'audit', got '${ctx.view}'`).toBe('audit')
    expect(window.location.pathname, 'the URL must settle on the restored destination').toBe('/audit')
  })

  it('restore_isSingleUse', async () => {
    captureDestination('/audit')
    const first = await bootWorkspaceAt('/')
    requireCtx()
    expect(readDestination(), 'the destination must be gone after the boot that consumed it').toBeNull()
    first.unmount()

    capturedCtx = undefined
    const second = await bootWorkspaceAt('/')
    const ctx = requireCtx()
    expect(ctx.view, 'a second boot at / must land on dashboard -- the destination is single-use').toBe('dashboard')
    expect(window.location.pathname, "the second boot's URL must settle on the bare root").toBe('/')
    second.unmount()
  })

  it('restore_consumesEvenWhenItDoesNotWin', async () => {
    // initialView (the DEMO-06 carry) can only exist from a SECOND, interactive mount --
    // there is no way to seed it on a truly fresh import. So: boot once with nothing
    // stored (pathname stays '/', since dashboard IS the root), THEN store a destination,
    // THEN trigger the persona-switch remount. That remount's own bootPath initializer
    // still runs (pathname is still '/'), so it still calls readDestination(), and the
    // mount effect still calls clearDestination() -- even though initialView pre-empts
    // the ternary entirely. This is AC-6: initialView wins, the destination is still
    // cleared.
    await bootWorkspaceAt('/', { demoMode: true })
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: no destination stored yet, boot at / lands on dashboard').toBe('dashboard')

    captureDestination('/audit')
    expect(typeof ctx.becomePersona, 'DEMO_MODE must expose becomePersona on ctx').toBe('function')
    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'clients')
    })
    ctx = requireCtx()
    expect(ctx.view, 'the DEMO-06 carried view must win over the newly-stored destination').toBe('clients')
    expect(readDestination(), 'the destination must be cleared by this mount even though it did not win').toBeNull()
  })

  it('restore_aLiveNonRootPathWins', async () => {
    captureDestination('/audit')
    await bootWorkspaceAt('/settings')
    const ctx = requireCtx()
    expect(ctx.view, 'a live non-root path must win over any stored destination').toBe('settings')
    expect(window.location.pathname, 'the URL must honour the live path').toBe('/settings')
    // Plan text (task-923, "the restore point"): clearDestination() in the mount effect
    // is unconditional -- ANY Workspace mount sweeps a stored destination, whether or not
    // bootPath ever consulted it. A "smarter" implementation that only clears when the
    // destination was actually read would violate this.
    expect(readDestination(), 'the mount effect clears the destination unconditionally, even when unused').toBeNull()
  })

  it('restore_theReviewHashStillWins', async () => {
    captureDestination('/audit')
    await bootWorkspaceAt(`/#review/${REVIEW_ID}`)
    const ctx = requireCtx()
    expect(ctx.view, 'a live review hash must still win over a stored destination').toBe('create')
    expect(ctx.createStep, 'the review step must be active').toBe('review')
    expect(window.location.hash, 'the hash must survive the alignment verbatim').toBe(`#review/${REVIEW_ID}`)
    expect(readDestination(), 'the destination is still consumed even though the hash won').toBeNull()
  })

  it('restore_anUnparseablePathFallsBackToDashboard', async () => {
    const badPaths = ['/nonsense', '/Audit', '/audit//', '/audit?foo=bar']
    for (const bad of badPaths) {
      capturedCtx = undefined
      sessionStorage.clear()
      captureDestination(bad)
      const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
      const rendered = await bootWorkspaceAt('/')
      const ctx = requireCtx()
      expect(ctx.view, `a stored destination of '${bad}' must fall back to dashboard, got '${ctx.view}'`).toBe(
        'dashboard',
      )
      expect(window.location.pathname, `the URL must settle on the bare root for '${bad}'`).toBe('/')
      expect(errSpy, `a rejected destination of '${bad}' must not throw/log an error`).not.toHaveBeenCalled()
      expect(readDestination(), `the rejected destination '${bad}' must still be consumed, not left behind`).toBeNull()
      errSpy.mockRestore()
      rendered.unmount()
    }
  })

  it('restore_anExpiredDestinationFallsBackToDashboard', async () => {
    captureDestination('/audit', Date.now() - (DEEP_LINK_TTL_MS + 1000))
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    await bootWorkspaceAt('/')
    const ctx = requireCtx()
    expect(ctx.view, 'an expired destination must fall back to dashboard').toBe('dashboard')
    expect(window.location.pathname, 'the URL must settle on the bare root').toBe('/')
    expect(warnSpy, 'expiry is a normal outcome and must warn nothing').not.toHaveBeenCalled()
    expect(readDestination(), 'the expired destination must still be consumed').toBeNull()
    warnSpy.mockRestore()
  })

  it('restore_noStoredDestinationBootsToDashboardAsToday', async () => {
    await bootWorkspaceAt('/')
    const ctx = requireCtx()
    expect(ctx.view, 'with nothing stored, / must still boot to dashboard exactly as today').toBe('dashboard')
    expect(window.location.pathname).toBe('/')
  })

  it('restore_strictModeYieldsTheSameResult', async () => {
    // Catches a naive "read-then-clear inside the initializer" implementation: StrictMode
    // double-invokes lazy initializers, so a version that clears storage as a SIDE EFFECT
    // of the read (rather than splitting read-in-render / clear-in-effect) would have its
    // two invocations race -- the first sees '/audit' and clears it, the second sees
    // nothing, and which one the committed state resolves to isn't guaranteed. A correct
    // split implementation is deterministic regardless.
    captureDestination('/audit')
    await bootWorkspaceAt('/', { strict: true })
    const ctx = requireCtx()
    expect(ctx.view, 'StrictMode must not change the outcome: view must still be audit').toBe('audit')
    expect(window.location.pathname, 'StrictMode must not change the outcome: URL must still be /audit').toBe(
      '/audit',
    )
    expect(readDestination(), 'the destination must be consumed exactly once, not left dangling').toBeNull()
  })

  it('restore_aDemoPersonaSwitchDoesNotResurrectAConsumedDestinationForTheNewPersona', async () => {
    // Distinct from restore_consumesEvenWhenItDoesNotWin above: here the FIRST mount (the
    // original persona) is the one that consumes an already-live destination; the
    // question is whether the SUBSEQUENT persona-switch remount (a genuinely different
    // identity, sessionStorage being tab-scoped rather than persona-scoped) can ever see
    // it again.
    captureDestination('/audit')
    await bootWorkspaceAt('/', { demoMode: true })
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: the first mount (original persona) restores the destination').toBe('audit')
    expect(readDestination(), 'sanity: the first mount consumes it').toBeNull()

    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'invoices')
    })
    ctx = requireCtx()
    expect(ctx.view, "the new persona's carried view must win, not a resurrected destination").toBe('invoices')
    expect(readDestination(), 'no destination must resurface for the new persona').toBeNull()
  })
})
