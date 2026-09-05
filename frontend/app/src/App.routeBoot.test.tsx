// @vitest-environment jsdom
// vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Boot seeding: the view a path implies before navigate()/popstate exist. Harness mirrors
// App.extractionRoute.test.tsx -- the real <App/>, a session in a stubbed localStorage, ctx
// captured through a mocked Sidebar.

import { StrictMode } from 'react'
import { act, cleanup, render } from '@testing-library/react'
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
    await bootAt(`/${hash}`)
    requireCtx()
    expect(window.location.pathname, 'the alignment must rewrite the path to /create').toBe('/create')
    expect(window.location.hash, 'the alignment must preserve the review hash verbatim').toBe(hash)
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
    const differing = replaceSpy.mock.calls.filter((call) => call[2] !== '/audit')
    expect(
      differing,
      `an already-aligned boot must never replaceState to a differing URL: ${JSON.stringify(differing)}`,
    ).toHaveLength(0)
  })
})
