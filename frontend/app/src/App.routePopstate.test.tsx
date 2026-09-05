// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// The popstate restore. Harness is App.routeNavigate.test.tsx's: the real <App/>, a
// session in a stubbed localStorage, ctx captured through a mocked Sidebar.

import { act, cleanup, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const JOB_A = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'

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

// Records every mount, including the job id it carried -- the Q6 oracle needs to see the
// screen did NOT render, not just that extractionJobId cleared.
const { extractionReviewMounts } = vi.hoisted(() => ({ extractionReviewMounts: [] as unknown[] }))
vi.mock('./components/ExtractionReview', () => ({
  ExtractionReview: (p: { jobId: unknown }) => {
    extractionReviewMounts.push(p.jobId)
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  extractionReviewMounts.length = 0
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function bootAt(path: string) {
  window.history.replaceState(null, '', path)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

// jsdom runs no real history stack for back()/forward() -- move the URL the way the
// browser would, then fire the event the browser fires.
async function popTo(path: string) {
  window.history.replaceState(null, '', path)
  await act(async () => {
    window.dispatchEvent(new PopStateEvent('popstate'))
  })
}

describe('AC-1: Back from a top-level view restores the previously visited view', () => {
  it('popstate_backFromATopLevelViewReturnsToThePreviousView', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    await popTo('/invoices')
    expect(requireCtx().view, 'Back must restore invoices, not fall back to dashboard').toBe('invoices')
  })

  // Three steps, not two: a handler hard-wired to one view passes row 1 by accident and
  // fails rows 2 and 3 -- a two-item fixture cannot discriminate a skip from a stop.
  it('popstate_aThreeStepChainWalksBackInOrder', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })
    await act(async () => {
      capturedCtx!.nav('settings')
    })

    await popTo('/audit')
    expect(requireCtx().view, 'first Back must land on audit').toBe('audit')

    await popTo('/invoices')
    expect(requireCtx().view, 'second Back must land on invoices').toBe('invoices')

    await popTo('/')
    expect(requireCtx().view, 'third Back must land on dashboard').toBe('dashboard')
  })
})

describe('AC-2: Forward re-applies the view it left', () => {
  it('popstate_forwardReAppliesTheViewItLeft', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    await popTo('/invoices')
    // Floor: Forward is meaningless to assert unless Back actually moved the view away
    // from 'audit' first -- otherwise the two hops could cancel out on a stuck value.
    expect(requireCtx().view, 'Back must land on invoices before Forward can be meaningful').toBe('invoices')

    await popTo('/audit')
    expect(requireCtx().view, 'Forward must re-apply audit').toBe('audit')
  })
})

describe('AC-3: the handler performs no history write', () => {
  it('popstate_theHandlerWritesNoHistory', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.nav('invoices')
    })
    await act(async () => {
      capturedCtx!.nav('audit')
    })

    // Move the URL the way a real Back press would BEFORE installing the spies -- this
    // harness's own move must not be mistaken for the handler's.
    window.history.replaceState(null, '', '/invoices')
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    const lengthBefore = window.history.length
    const urlBeforeDispatch = window.location.pathname + window.location.search + window.location.hash

    await act(async () => {
      window.dispatchEvent(new PopStateEvent('popstate'))
    })

    // AC-3's actual claim is "no duplicate history entry per Back press", not "zero
    // history-API calls" -- the pre-existing review-hash mirror (App.tsx:540-546) also
    // fires on this view change and calls replaceState, but only to rewrite the URL it
    // already is (a no-op). Assert the handler pushes nothing, adds no entry, and any
    // replaceState observed is that no-op rewrite -- a handler that wrote a *different*
    // URL, or pushed, still fails.
    expect(pushSpy, 'the popstate handler must never call pushState').not.toHaveBeenCalled()
    expect(window.history.length, 'a popstate restore must add no history entry').toBe(lengthBefore)
    for (const call of replaceSpy.mock.calls) {
      const url = call[2]
      expect(url, 'any replaceState after a popstate must rewrite the current URL, not a different one').toBe(
        urlBeforeDispatch,
      )
    }
  })
})

describe('AC-4: exactly one listener is registered, and it is removed on unmount', () => {
  it('popstate_exactlyOneListenerIsRegisteredAndItIsRemovedOnUnmount', async () => {
    const addSpy = vi.spyOn(window, 'addEventListener')
    const removeSpy = vi.spyOn(window, 'removeEventListener')
    const { unmount } = await bootAt('/')

    const addCalls = addSpy.mock.calls.filter((c) => c[0] === 'popstate')
    // Floor before indexing: an empty match set must fail loudly, not read as "removed
    // cleanly" by never reaching the .toHaveLength(1) below.
    expect(addCalls.length, 'no addEventListener("popstate", ...) call was recorded at all').toBeGreaterThan(0)
    expect(addCalls, 'exactly one popstate listener must be registered').toHaveLength(1)
    const handler = addCalls[0]![1]

    unmount()

    const removeCalls = removeSpy.mock.calls.filter((c) => c[0] === 'popstate')
    expect(removeCalls.length, 'no removeEventListener("popstate", ...) call was recorded at all').toBeGreaterThan(0)
    expect(removeCalls, 'exactly one popstate listener must be removed').toHaveLength(1)
    expect(removeCalls[0]![1], 'the removed handler must be the same reference that was added').toBe(handler)
  })
})

describe('AC-5: an unrecognised path restores dashboard', () => {
  it('popstate_anUnrecognisedPathRestoresDashboard', async () => {
    await bootAt('/audit')
    expect(requireCtx().view, 'sanity: booting at /audit must seed that view').toBe('audit')

    await popTo('/gone')
    expect(
      requireCtx().view,
      'a popstate onto an unrecognised path must fall back to dashboard, not leave audit stale',
    ).toBe('dashboard')
  })
})

describe('Core AC 4: the sessions first screen pushed nothing to go back to', () => {
  it('popstate_theSessionsFirstScreenPushedNothingToGoBackTo', async () => {
    const pushSpy = vi.spyOn(window.history, 'pushState')
    await bootAt('/')
    requireCtx()

    expect(
      pushSpy,
      'the boot render must not push a history entry -- the workspace\'s first entry is the landing hand-off\'s own',
    ).not.toHaveBeenCalled()
  })
})

describe('AC-6 (Q6 Back half): Back after a company switch cannot reach the company just left', () => {
  it('popstate_backAfterACompanySwitchCannotReachTheCompanyJustLeft', async () => {
    await bootAt('/')
    await act(async () => {
      capturedCtx!.openExtraction(JOB_A)
    })
    let ctx = requireCtx()
    expect(window.location.pathname, 'sanity: openExtraction must push /extraction').toBe('/extraction')
    expect(ctx.extractionJobId, 'sanity: the job id must be set').toBe(JOB_A)

    await act(async () => {
      capturedCtx!.switchClient('other-entity-999')
    })
    ctx = requireCtx()
    expect(ctx.extractionJobId, 'sanity: switchClient (ROUTE-01-03) must already clear the job').toBeNull()

    // The spy is push-only and openExtraction already recorded one mount above -- reset so
    // the assertion below measures only the window after Back, not that earlier mount.
    extractionReviewMounts.length = 0

    await popTo('/extraction')
    ctx = requireCtx()
    expect(ctx.view, 'Back must restore the extraction view').toBe('extraction')
    expect(ctx.extractionJobId, 'the cleared job must not come back on a popstate restore').toBeNull()
    expect(
      extractionReviewMounts,
      'no ExtractionReview may render for a null job id -- App.tsx\'s view===extraction && extractionJobId!=null gate',
    ).toHaveLength(0)
  })
})
