// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Boot seeding: the view a path implies before navigate()/popstate exist. Harness mirrors
// App.extractionRoute.test.tsx -- the real <App/>, a session in a stubbed localStorage, ctx
// captured through a mocked Sidebar.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { StrictMode } from 'react'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { ROUTE_PATHS } from './lib/route'
import type { Member } from './lib/members'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx, View } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const REVIEW_ID = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'

const MEMBER: Member = {
  id: 'm-boot-001',
  name: 'Tunde Bello',
  initials: 'TB',
  email: 'tunde@example.ng',
  role: 'preparer',
  status: 'active',
  isYou: false,
}

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
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  // jsdom's environment is per FILE, not per test -- without this, one test's boot URL
  // seeds the next test's boot.
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function bootAt(path: string, opts: { demoMode?: boolean; strict?: boolean } = {}) {
  window.history.replaceState(null, '', path)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  if (opts.demoMode) vi.stubEnv('VITE_DEMO_MODE', 'true')
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(opts.strict ? (
    <StrictMode>
      <App />
    </StrictMode>
  ) : (
    <App />
  ))
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

describe('AC-1: a path seeds the view it names', () => {
  it('boot_aPathSeedsTheViewItNames', async () => {
    await bootAt('/audit')
    const ctx = requireCtx()
    expect(ctx.view, `booting at /audit should seed view 'audit', got '${ctx.view}'`).toBe('audit')
  })

  it('boot_everyNonDefaultPathSeedsItsOwnView', async () => {
    const nonDefault = (Object.entries(ROUTE_PATHS) as [View, string][]).filter(([view]) => view !== 'dashboard')
    // Floor: a loop over an empty array passes every assertion inside it.
    expect(nonDefault, 'the route table must have exactly 12 non-dashboard entries').toHaveLength(12)

    for (const [view, path] of nonDefault) {
      cleanup()
      capturedCtx = undefined
      await bootAt(path)
      const ctx = requireCtx()
      expect(ctx.view, `booting at ${path} should seed view '${view}', got '${ctx.view}'`).toBe(view)
    }
  })
})

describe('AC-2: an unknown path falls back to dashboard', () => {
  it('boot_anUnknownPathFallsBackToDashboardAndTheUrlIsCorrected', async () => {
    await bootAt('/nonsense')
    const ctx = requireCtx()
    expect(ctx.view, `an unknown path should fall back to dashboard, got '${ctx.view}'`).toBe('dashboard')
    expect(window.location.pathname, 'the corrected URL must be the bare root').toBe('/')
  })
})

describe('AC-3: initialView still beats the path', () => {
  it('boot_initialViewStillBeatsThePath', async () => {
    await bootAt('/audit', { demoMode: true })
    const ctx = requireCtx()
    expect(typeof ctx.becomePersona, 'DEMO_MODE must expose becomePersona on ctx').toBe('function')

    await act(async () => {
      await ctx.becomePersona!(MEMBER, 'approvals')
    })
    expect(
      capturedCtx!.view,
      `a DEMO-06 initialView carry must beat the path, got '${capturedCtx!.view}'`,
    ).toBe('approvals')
  })
})

describe('AC-4: the review hash still beats the path', () => {
  it('boot_theReviewHashStillBeatsThePath', async () => {
    await bootAt(`/audit#review/${REVIEW_ID}`)
    const ctx = requireCtx()
    expect(ctx.view, `a live review hash must still win over the path, got '${ctx.view}'`).toBe('create')
    expect(ctx.createStep, 'the review step must be active').toBe('review')
  })
})

describe('AC-5: the alignment preserves the hash', () => {
  it('boot_theAlignmentPreservesTheHash', async () => {
    const hash = `#review/${REVIEW_ID}`
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAt(`/${hash}`)
    requireCtx()
    expect(window.location.pathname, 'the alignment must rewrite the path to /create').toBe('/create')
    expect(window.location.hash, 'the alignment must preserve the review hash verbatim').toBe(hash)

    // The final `window.location.hash` above is a WEAK oracle for the alignment's own
    // line: App.tsx's pre-existing, untouched review-hash mirror (the effect declared
    // right after the alignment, App.tsx:524-530) recomputes and re-writes the identical
    // hash on this same commit whenever createStep is 'review', independent of what the
    // alignment wrote. A `replaceState` call that drops the hash from the alignment would
    // still leave `window.location.hash` correct, repaired by that unrelated effect. The
    // alignment's OWN write -- its first recorded call after the test's own boot-setup
    // call -- must therefore be checked directly.
    const own = replaceSpy.mock.calls.find((call) => typeof call[2] === 'string' && call[2].startsWith('/create'))
    expect(own, 'no replaceState call to /create was recorded').toBeDefined()
    expect(own![2], "the alignment's own replaceState call must itself carry the hash").toBe(`/create${hash}`)
  })
})

describe('AC-6: the alignment writes no history entry, and is idempotent', () => {
  it('boot_mountAddsNoHistoryEntry', async () => {
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const lengthBefore = window.history.length
    await bootAt('/nonsense')
    requireCtx()
    expect(pushSpy, 'mount must never call pushState').not.toHaveBeenCalled()
    expect(window.history.length, 'mount must add no history entry').toBe(lengthBefore)
  })

  it('boot_theAlignmentIsIdempotentUnderStrictModeDoubleInvocation', async () => {
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAt('/audit', { strict: true })
    requireCtx()
    // Floor: a writer swapped to pushState leaves this spy with zero calls, and
    // .filter(...).toHaveLength(0) below would pass on that broken build for the wrong
    // reason. boot_mountAddsNoHistoryEntry pins that swap directly; this floor keeps this
    // test meaningful on its own.
    expect(replaceSpy.mock.calls.length, 'replaceState must have been called at least once during boot').toBeGreaterThan(0)
    const differing = replaceSpy.mock.calls.filter((call) => call[2] !== '/audit')
    expect(
      differing,
      `an already-aligned boot must never replaceState to a differing URL: ${JSON.stringify(differing)}`,
    ).toHaveLength(0)
  })

  // No AC test pins the alignment's own dependency array. `ctx.nav` still just calls
  // `setView` at this point in the story (navigate()/pushState is ROUTE-01-03), so a
  // later view change is reachable today without any history write. The pre-existing
  // review-hash mirror (App.tsx:524-530, untouched, out of scope) ALSO depends on `view`
  // and legitimately re-fires on every nav -- so this checks for a replaceState call
  // naming the NEW view's own path specifically, which only a widened alignment would
  // ever produce; a raw call-count comparison would false-fail on the mirror's own calls.
  it('boot_theAlignmentDoesNotReRunWhenViewChangesAfterMount', async () => {
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    await bootAt('/audit')
    const ctx = requireCtx()

    await act(async () => {
      ctx.nav('invoices')
    })

    const wroteInvoicesPath = replaceSpy.mock.calls.some((call) => typeof call[2] === 'string' && call[2].startsWith('/invoices'))
    expect(
      wroteInvoicesPath,
      'a post-mount view change must not re-trigger the mount-only alignment effect (it would replaceState to /invoices)',
    ).toBe(false)
  })
})

describe('AC-7: signOut resets the pathname to /, and preserves the hash', () => {
  it('signOut_thePathnameDoesNotSurviveIntoTheNextSignIn', async () => {
    // ctx.nav() does not touch the URL until navigate()/pushState land (ROUTE-01-03) --
    // booting straight at /invoices is what dirties the pathname today, via the mount
    // alignment this subtask adds (AC-1).
    await bootAt('/invoices')
    let ctx = requireCtx()
    expect(ctx.view, 'sanity: booting at /invoices must seed that view').toBe('invoices')

    await act(async () => {
      ctx.signOut()
    })
    expect(window.location.pathname, 'signOut must reset the pathname to /').toBe('/')

    expect(screen.getByText('Choose an account'), 'the in-app picker must render after sign-out').toBeTruthy()
    const pickButton = screen.getByText(SEAT_SESSION.persona.name).closest('button')
    expect(pickButton, 'the firm persona button was not found in the picker').toBeTruthy()
    capturedCtx = undefined
    await act(async () => {
      fireEvent.click(pickButton as HTMLButtonElement)
    })

    ctx = requireCtx()
    expect(ctx.view, 'a fresh sign-in must never inherit the previous session\'s view').toBe('dashboard')
  })

  it('signOut_preservesTheHashAndEveryOtherReset', async () => {
    const hash = `#review/${REVIEW_ID}`
    await bootAt(`/invoices${hash}`)
    const ctx = requireCtx()
    // sanity: the review hash beats the path (AC-4), so this boots onto /create.
    expect(window.location.pathname, 'sanity: the review hash must beat the path').toBe('/create')

    await act(async () => {
      ctx.signOut()
    })

    expect(window.location.hash, 'signOut must preserve the hash verbatim').toBe(hash)
    expect(window.location.pathname, 'signOut must still reset the pathname').toBe('/')
    expect(localStorage.getItem(SESSION_KEY), 'the persisted session must be cleared').toBeNull()
    expect(screen.queryByTestId('persona-toast'), 'no toast must be mounted after sign-out').toBeNull()
  })
})

describe('AC-8: the pre-existing sign-out regression oracle is untouched', () => {
  it('standIn_theExistingSignOutOracleIsUnmodified', () => {
    const source = readFileSync(path.join(process.cwd(), 'src/App.standIn.test.tsx'), 'utf8')
    expect(
      source.includes(
        "a return that commits after sign-out must not carry its view into the next sign-in').toBe('dashboard')",
      ),
      'the oracle\'s message and its .toBe(\'dashboard\') assertion must both survive verbatim',
    ).toBe(true)
  })
})

describe('QA adversarial coverage', () => {
  it('boot_neverEchoesAnExistingQueryStringIntoTheAlignedUrl', async () => {
    await bootAt('/audit?foo=bar')
    requireCtx()
    expect(window.location.pathname, 'the view still seeds from the path').toBe('/audit')
    expect(window.location.search, 'the alignment must never echo an existing query string').toBe('')
  })

  it('boot_toleratesExactlyOneTrailingSlash', async () => {
    await bootAt('/audit/')
    const ctx = requireCtx()
    expect(ctx.view, `a single trailing slash should still seed 'audit', got '${ctx.view}'`).toBe('audit')
  })

  it('boot_detailWithNoSelectionColdBootsToTheEmptyState (documented limitation, ROUTE-02 closes it)', async () => {
    await bootAt('/invoice')
    requireCtx()
    expect(screen.getByText('No invoice selected'), 'a cold /invoice boot must render the honest EmptyState').toBeTruthy()
  })

  it('boot_extractionWithNoJobIdRendersNothing (documented limitation, ROUTE-02 closes it)', async () => {
    await bootAt('/extraction')
    requireCtx()
    expect(
      screen.queryByTestId('extraction-review'),
      'App.tsx\'s `extractionJobId != null` gate must render nothing for a cold /extraction boot',
    ).toBeNull()
  })

  it('boot_aWrongCasePathFallsBackToDashboard', async () => {
    await bootAt('/Audit')
    const ctx = requireCtx()
    expect(ctx.view, `a wrong-case path must not match, got '${ctx.view}'`).toBe('dashboard')
    expect(window.location.pathname, 'the corrected URL must be the bare root').toBe('/')
  })

  it('boot_theReviewHashWinsOverAMismatchedPathAndTheAlignmentWritesCreatePlusTheHash', async () => {
    const hash = `#review/${REVIEW_ID}`
    await bootAt(`/settings${hash}`)
    const ctx = requireCtx()
    expect(ctx.view, 'the review hash must win over a completely unrelated path').toBe('create')
    expect(window.location.pathname, 'the alignment must correct the path to /create').toBe('/create')
    expect(window.location.hash, 'the alignment must carry the hash along').toBe(hash)
  })
})


