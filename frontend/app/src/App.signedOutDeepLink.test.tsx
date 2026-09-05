// @vitest-environment jsdom
// ROUTE-05-02: the capture call inside the front-door effect (App.tsx:1685-1689), which
// must run under the same activeSession/autoPersona guards as the bounce it precedes.

import { StrictMode } from 'react'
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import {
  captureDestination,
  readDestination,
  DEEP_LINK_KEY,
  DEEP_LINK_SCHEMA_VERSION,
  DEEP_LINK_TTL_MS,
} from './lib/deepLink'
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

const { captureDestinationSpy } = vi.hoisted(() => ({ captureDestinationSpy: vi.fn() }))

// ROUTE-05-05: wraps the REAL captureDestination -- every other spec in this file calls it
// directly to seed sessionStorage and must see identical behaviour. Only
// signOut_reFiresTheFrontDoorEffectButThePathIsAlreadyRootSoNothingIsCaptured below reads
// captureDestinationSpy.mock.calls, because readDestination() alone can't distinguish "the
// front-door effect never re-ran" from "it re-ran and was correctly refused" -- both leave
// storage empty.
vi.mock('./lib/deepLink', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./lib/deepLink')>()
  captureDestinationSpy.mockImplementation(actual.captureDestination)
  return { ...actual, captureDestination: captureDestinationSpy }
})

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  // sessionStorage has no Node-v25/jsdom collision (deepLink.test.ts:14-16) -- clearing
  // the real one is enough, no stub needed.
  sessionStorage.clear()
  window.history.replaceState(null, '', '/')
  capturedCtx = undefined
  captureDestinationSpy.mockClear()
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
})

// ROUTE-05-03. The App-level capture above (ROUTE-05-02) stores the destination; this
// describe block is the OTHER half -- Workspace's boot restoring it via `bootPath`
// (App.tsx:320-322), substituted into the `view` seed at App.tsx:326-327.
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
    // QA addition (ROUTE-05-04 AC-5): task-924's own Stage 1 validation recommended this
    // spy on this test specifically; it landed on the two new specs but not here.
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    await bootWorkspaceAt('/')
    const ctx = requireCtx()
    expect(ctx.view, 'an expired destination must fall back to dashboard').toBe('dashboard')
    expect(window.location.pathname, 'the URL must settle on the bare root').toBe('/')
    expect(warnSpy, 'expiry is a normal outcome and must warn nothing').not.toHaveBeenCalled()
    expect(errSpy, 'the abandoned-attempt path is a normal outcome and must not error').not.toHaveBeenCalled()
    // readDestination()'s own TTL check returns null whether cleared or merely expired --
    // read the raw key so this can actually distinguish the two.
    expect(
      sessionStorage.getItem(DEEP_LINK_KEY),
      'the expired destination must actually be removed from storage, not just filtered by readDestination()',
    ).toBeNull()
    // Vacuity control (App.offlineFallback.test.tsx:118-120): proves the spy is live and
    // would have caught a real console.error, so the absence above is not a spy nobody wired.
    console.error('control: this deliberate call must be observed')
    expect(errSpy, 'the spy must catch a real call -- otherwise the assertion above is vacuous').toHaveBeenCalledTimes(1)
    warnSpy.mockRestore()
    errSpy.mockRestore()
  })

  it('restore_noStoredDestinationBootsToDashboardAsToday', async () => {
    await bootWorkspaceAt('/')
    const ctx = requireCtx()
    expect(ctx.view, 'with nothing stored, / must still boot to dashboard exactly as today').toBe('dashboard')
    expect(window.location.pathname).toBe('/')
  })

  it('restore_strictModeYieldsTheSameResult', async () => {
    // QA (Stage 4): does NOT catch a naive read-then-clear-inside-the-initializer variant
    // -- verified empirically (React 19.2.7) that StrictMode's double-invoked lazy
    // initializer commits the FIRST call's return value, so that variant still passes this
    // assertion by coincidence. This spec only pins that the SHIPPED split (read in the
    // initializer, clear in the mount effect) is StrictMode-safe, not that mount-alignment
    // effect and initializer are protecting against that particular defect.
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

  // QA adversarial coverage (Stage 4).
  it('restore_adversarial_aFreshCaptureBetweenTwoSeparateBootsIsRestoredByTheSecond', async () => {
    // Distinct from restore_isSingleUse (same destination gone by boot 2): proves restore
    // isn't gated by an "only ever once per tab" flag -- it's pure storage state.
    const first = await bootWorkspaceAt('/')
    requireCtx()
    expect(readDestination(), 'sanity: nothing stored for the first boot').toBeNull()
    first.unmount()

    captureDestination('/reports')
    capturedCtx = undefined
    const second = await bootWorkspaceAt('/')
    const ctx = requireCtx()
    expect(
      ctx.view,
      'a destination captured after the first boot tore down must still restore on the next boot',
    ).toBe('reports')
    expect(window.location.pathname).toBe('/reports')
    second.unmount()
  })

  it('restore_adversarial_aCorruptRootShapedEntryIsRejectedNotRestored', async () => {
    // captureDestination() itself refuses to store '/'; this shape only reaches storage via
    // a bypass. Raw sessionStorage write, deliberately skipping captureDestination's guard.
    sessionStorage.setItem(
      DEEP_LINK_KEY,
      JSON.stringify({ v: DEEP_LINK_SCHEMA_VERSION, path: '/', at: Date.now() }),
    )
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    await bootWorkspaceAt('/')
    const ctx = requireCtx()
    expect(ctx.view, 'a corrupt-shaped entry must fall back to dashboard').toBe('dashboard')
    expect(window.location.pathname).toBe('/')
    expect(warnSpy, 'a corrupt destination warns once, per readDestination\'s own contract').toHaveBeenCalledWith(
      expect.stringContaining('ignoring corrupt destination'),
    )
    expect(
      sessionStorage.getItem(DEEP_LINK_KEY),
      'the corrupt entry must still be swept by the unconditional mount-effect clear',
    ).toBeNull()
    warnSpy.mockRestore()
  })
})

// ROUTE-05-04, AC-5. The two other planned specs for this subtask (an abandoned attempt
// not hijacking a later sign-in, and the expired blob still being cleared) are dropped as
// same-level duplicates of restore_anExpiredDestinationFallsBackToDashboard above (:367-382):
// that spec already boots the rendered app with an expired blob and asserts dashboard, the
// root URL, and sessionStorage having been swept. Stubbing Date.now forward and backdating
// `at` both reach the identical `now - at > TTL` branch that spec already exercises.
describe('Expiry and the abandoned attempt (ROUTE-05-04)', () => {
  it('expiry_atTheBoundaryTheDestinationStillApplies', async () => {
    // deepLink.test.ts's read_theBoundaryIsInclusive proves the boundary at the module
    // level; nothing exercises it through App.tsx's own call site (App.tsx:320-321), which
    // calls readDestination() with no `now` argument -- so proving the boundary here means
    // controlling the real clock the app reads. Fake only Date: becomePersona's real
    // BUSY_MS delay elsewhere in this file must not hang under a fully-faked clock.
    vi.useFakeTimers({ toFake: ['Date'] })
    const at = 1_700_000_000_000
    vi.setSystemTime(at)
    captureDestination('/audit', at)
    vi.setSystemTime(at + DEEP_LINK_TTL_MS)
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    await bootWorkspaceAt('/')
    const ctx = requireCtx()
    expect(ctx.view, 'a destination exactly at the TTL boundary must still apply').toBe('audit')
    expect(window.location.pathname, 'the URL must settle on the still-valid destination').toBe('/audit')
    expect(errSpy, 'a boundary-valid destination is a normal outcome and must not error').not.toHaveBeenCalled()
    // Vacuity control (App.offlineFallback.test.tsx:118-120): proves the spy is live and
    // would have caught a real console.error, so the absence above is not a spy nobody wired.
    console.error('control: this deliberate call must be observed')
    expect(errSpy, 'the spy must catch a real call -- otherwise the assertion above is vacuous').toHaveBeenCalledTimes(1)
    errSpy.mockRestore()
    vi.useRealTimers()
  })

  it('expiry_aPersonaSwitchRemountIgnoresAStoredDestination', async () => {
    // Distinct from restore_aDemoPersonaSwitchDoesNotResurrectAConsumedDestinationForTheNewPersona
    // (:408) and restore_consumesEvenWhenItDoesNotWin (:301): both boot at '/', so bootPath's
    // ternary (App.tsx:320-321) already calls readDestination() on the FIRST mount. Here the
    // first mount boots at a live non-root path, so bootPath never reaches readDestination()
    // at all on mount 1 -- the blob is then written straight to sessionStorage, bypassing
    // captureDestination, so nothing in this test has EVER gone through the read call site.
    // Proves the remount's unconditional clearDestination() sweep (App.tsx:525) and
    // initialView's precedence (App.tsx:326-327) hold even so.
    await bootWorkspaceAt('/clients', { demoMode: true })
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: a live non-root boot lands on the path itself, not dashboard').toBe('clients')

    sessionStorage.setItem(
      DEEP_LINK_KEY,
      JSON.stringify({ v: DEEP_LINK_SCHEMA_VERSION, path: '/audit', at: Date.now() }),
    )
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(typeof ctx.becomePersona, 'DEMO_MODE must expose becomePersona on ctx').toBe('function')
    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'invoices')
    })
    ctx = requireCtx()
    expect(
      ctx.view,
      "the new persona's carried view must win, not the destination stashed after mount 1",
    ).toBe('invoices')
    expect(
      readDestination(),
      'the remount must still sweep a destination its own first mount never read',
    ).toBeNull()
    expect(
      errSpy,
      'a persona-switch remount ignoring a stored destination is a normal outcome and must not error',
    ).not.toHaveBeenCalled()
    // Vacuity control (App.offlineFallback.test.tsx:118-120): proves the spy is live and
    // would have caught a real console.error, so the absence above is not a spy nobody wired.
    console.error('control: this deliberate call must be observed')
    expect(errSpy, 'the spy must catch a real call -- otherwise the assertion above is vacuous').toHaveBeenCalledTimes(1)
    errSpy.mockRestore()
  })
})

// QA addition (Stage 4, AC-3). Neither harness above can observe a real signOut redirect:
// stubLocation() freezes pathname so signOut's replaceState is invisible (the ROUTE-05-05
// rewrite's whole reason for existing); real un-stubbed navigation lets replaceState work
// but jsdom silently refuses to update `.href` for a cross-origin write (confirmed: assigning
// window.location.href under bootWorkspaceAt leaves .href unchanged, only logging "Not
// implemented: navigation to another Document"). A Proxy over the REAL location forwards
// pathname reads/replaceState to jsdom untouched while intercepting only the `href` setter.
function interceptHref() {
  const hrefWrites: string[] = []
  const real = window.location
  const proxy = new Proxy(real, {
    set(target, prop, value) {
      if (prop === 'href') {
        hrefWrites.push(value)
        return true
      }
      return Reflect.set(target, prop, value)
    },
    get(target, prop) {
      const v = (target as unknown as Record<PropertyKey, unknown>)[prop]
      return typeof v === 'function' ? v.bind(target) : v
    },
  })
  Object.defineProperty(window, 'location', { configurable: true, value: proxy })
  return { hrefWrites }
}

// ROUTE-05-05. signOut's own pathname rewrite (App.tsx:1589) already lands before the
// front-door effect re-fires on the resulting activeSession->null transition (deps
// [activeSession, autoPersona], App.tsx:1701) -- App.routeBoot.test.tsx's
// signOut_thePathnameDoesNotSurviveIntoTheNextSignIn proves that ordering under real
// navigation. So AC-2/AC-4 below hold today without any new production line; only AC-1's
// OTHER scenario -- a destination stored BEFORE this signOut, left by an earlier, unrelated
// sessionless bounce -- needs clearDestination() added to signOut, since the pathname
// rewrite has no power over a value already sitting in storage.
describe('Sign-out clears the captured destination (ROUTE-05-05)', () => {
  it('signOut_reFiresTheFrontDoorEffectButThePathIsAlreadyRootSoNothingIsCaptured', async () => {
    // Was capture_aSignedOutTransitionAlsoCapturesTheCurrentPath, which used this file's
    // stubLocation() -- a static object history.replaceState cannot update, so it froze the
    // pre-signOut pathname ('/reports') and observed a capture that can never happen in the
    // real app: signOut always rewrites the pathname to '/' first, and '/' is refused.
    // Rewritten under real jsdom navigation (bootWorkspaceAt); readDestination() alone can't
    // tell "the effect never re-ran" from "it re-ran and was correctly refused" (both leave
    // storage empty), so captureDestinationSpy pins the call itself as proof of the re-fire.
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    await bootWorkspaceAt('/reports')
    const ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /reports must seed that view').toBe('reports')
    captureDestinationSpy.mockClear()

    await act(async () => {
      ctx.signOut()
    })

    expect(
      captureDestinationSpy,
      "the front-door effect's dependency array is [activeSession, autoPersona] -- it must re-run on this transition and reach its capture call",
    ).toHaveBeenCalledWith('/')
    expect(readDestination(), 'the rewritten root path is refused, so nothing is captured').toBeNull()
  })

  it('signOut_clearsAStoredDestination', async () => {
    // Genuinely RED without the fix (Stage 1): the pathname rewrite alone cannot reach a
    // value already written to sessionStorage by an earlier, unrelated sessionless bounce --
    // only clearDestination() inside signOut removes it.
    await bootWorkspaceAt('/audit')
    const ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /audit must seed that view').toBe('audit')

    // Simulates a stray leftover written after this mount's own sweep (App.tsx:526) already
    // ran, so signOut is the only remaining thing that can clear it.
    captureDestination('/settings')
    expect(readDestination(), 'sanity: the stray blob is present before signOut').toBe('/settings')

    await act(async () => {
      ctx.signOut()
    })

    expect(readDestination(), 'a destination stored before this signOut must not survive it').toBeNull()
  })

  it('signOut_capturesNothingOnTheWayOut', async () => {
    // GREEN on arrival: no fix required for this scenario -- signOut's pathname rewrite to
    // '/' (App.tsx:1589) already lands before the front-door effect re-fires, and '/' is
    // refused. A landing URL is configured so the re-fire actually attempts the capture (and
    // is refused), rather than skipping it via the `if (dest)` guard. Confirmed able to fail:
    // deleting App.tsx:1589 leaves '/audit' captured here (see the sign-in test below).
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    await bootWorkspaceAt('/audit')
    const ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /audit must seed that view').toBe('audit')

    await act(async () => {
      ctx.signOut()
    })

    expect(
      sessionStorage.getItem(DEEP_LINK_KEY),
      'signOut must capture nothing new on the way out',
    ).toBeNull()
    expect(window.location.pathname, 'signOut must reset the pathname to /').toBe('/')
  })

  it('signOut_thenSignInLandsOnTheDashboard', async () => {
    // GREEN on arrival: signOut's pathname reset to '/' (App.tsx:1589) runs before the
    // front-door effect's re-fire, so the re-fire can only ever try to capture '/', never
    // '/audit' -- and '/' is refused. Without that reset, the re-fire would capture '/audit'
    // for real (this file's landing URL makes the capture attempt happen, not skip via the
    // `if (dest)` guard), and this boot's own restore (App.tsx:320-322) would then seed
    // 'audit' instead of 'dashboard' -- confirmed by temporarily deleting App.tsx:1589.
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    const first = await bootWorkspaceAt('/audit')
    const ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /audit must seed that view').toBe('audit')

    await act(async () => {
      ctx.signOut()
    })
    first.unmount()

    capturedCtx = undefined
    const second = await bootWorkspaceAt('/')
    const nextCtx = requireCtx()
    expect(
      nextCtx.view,
      'a sign-in after a sign-out must land on dashboard, not the screen just left',
    ).toBe('dashboard')
    expect(window.location.pathname).toBe('/')
    second.unmount()
  })

  it('signOut_redirectsToLandingBase', async () => {
    // AC-3 ("signing out still redirects to landingBase(), unchanged") has no assertion
    // anywhere else in the suite: App.routeBoot.test.tsx and App.standIn.test.tsx exercise
    // signOut with VITE_LANDING_URL unset (the in-app picker path), never the redirect
    // itself. interceptHref() (above) is what makes this observable without breaking the
    // real pathname reset this describe block depends on.
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    await bootWorkspaceAt('/audit')
    const ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /audit must seed that view').toBe('audit')
    const { hrefWrites } = interceptHref()

    await act(async () => {
      ctx.signOut()
    })

    // Two identical writes, not one: signOut's own tail navigates, then the front-door
    // effect re-fires on the activeSession->null transition (Part 2 of this task's Stage 1
    // notes) and navigates again since landingBase() is still configured -- captureDestination
    // is refused but the unconditional `if (dest) window.location.href = dest` below it still
    // runs. Harmless (same URL, a real browser only navigates once) but worth pinning exactly
    // rather than asserting a single write that isn't what happens.
    expect(hrefWrites.every((w) => w === 'https://landing.example'), 'every write must target landingBase()').toBe(
      true,
    )
    expect(hrefWrites.length, 'signOut writes href twice: its own tail, then the front-door re-fire').toBe(2)
  })
})
