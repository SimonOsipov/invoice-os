// @vitest-environment jsdom
// First jsdom coverage of <App/> (DEMO-06-02). DEMO_MODE binds at module scope
// (demo/flag.ts), so every flag-on case needs vi.stubEnv + vi.resetModules() + a
// dynamic import, same idiom as demo/flag.test.ts. No gateway is configured under
// vitest, so signIn never hits the network by default (auth.ts) -- only the failed-
// mint test needs to force a rejection.
import { act, cleanup, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { Member } from './lib/members'
import type { PlatformCtx } from './types'

const { signInMock } = vi.hoisted(() => ({ signInMock: vi.fn() }))

// Default implementation is the REAL signIn, reset on every fresh import so a
// mockRejectedValueOnce from one test never leaks into the next module graph.
vi.mock('./auth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./auth')>()
  signInMock.mockImplementation(actual.signIn)
  return { ...actual, signIn: signInMock }
})

// The only way to reach the ctx App builds: no UI calls becomePersona/returnToSeat
// until DEMO-06-05, so capture it through the one component Workspace always renders.
let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }

const MEMBER: Member = {
  id: 'm-standin-001',
  name: 'Tunde Bello',
  initials: 'TB',
  email: 'tunde@example.ng',
  role: 'preparer',
  status: 'active',
  isYou: false,
}

// Same subject as the seat -- the row that must short-circuit rather than mint a
// same-subject stand-in (S3 invariant).
const SEAT_AS_MEMBER: Member = {
  id: SEAT_SESSION.persona.subject,
  name: SEAT_SESSION.persona.name,
  initials: SEAT_SESSION.persona.initials,
  email: null,
  role: 'admin',
  status: 'active',
  isYou: true,
}

// Node v25's native localStorage collides with jsdom's (lib/session.test.ts:230 --
// verified locally). vi.stubGlobal with an in-memory Map sidesteps both.
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

beforeEach(() => {
  capturedCtx = undefined
  vi.stubGlobal('localStorage', createMemoryStorage())
  signInMock.mockClear()
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

async function renderAppWithSeat(demoMode: boolean) {
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  if (demoMode) vi.stubEnv('VITE_DEMO_MODE', 'true')
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

// __reactFiber$<id> is attached to the real DOM node by react-dom; walking .return
// from it climbs the fiber tree (div -> Workspace -> ... -> root). Workspace is not
// exported, so this is the only way to read the key React assigned it without
// depending on internal module structure.
function fiberKeyChain(node: Element): (string | null)[] {
  const fiberProp = Object.keys(node).find((k) => k.startsWith('__reactFiber$'))
  if (!fiberProp) throw new Error('no React fiber found on the rendered root -- render() may have failed')
  const keys: (string | null)[] = []
  let fiber = (node as unknown as Record<string, { key: string | null; return: unknown } | undefined>)[fiberProp]
  while (fiber) {
    keys.push(fiber.key)
    fiber = fiber.return as typeof fiber
  }
  return keys
}

describe('AC-1: the workspace remount key is demo-only', () => {
  it('the workspace key is undefined when the flag is off', async () => {
    const { container } = await renderAppWithSeat(false)

    const shell = container.querySelector('.pf-shell')
    expect(shell, 'Workspace did not render -- no .pf-shell in the tree').not.toBeNull()
    const keys = fiberKeyChain(shell!)
    expect(keys.every((k) => k === null), `expected every fiber key to be null, got ${JSON.stringify(keys)}`).toBe(true)
  })

  // Pairs with the test above: the flag-off assertion alone is unchanged behaviour and
  // stays green whether or not the ternary exists. This is the paired proof that a red
  // exists for AC-1 at all.
  it('the workspace key equals the seat subject when the flag is on', async () => {
    const { container } = await renderAppWithSeat(true)

    const shell = container.querySelector('.pf-shell')
    expect(shell, 'Workspace did not render -- no .pf-shell in the tree').not.toBeNull()
    const keys = fiberKeyChain(shell!)
    const matches = keys.filter((k) => k === SEAT_SESSION.persona.subject)
    expect(matches.length, `expected exactly one fiber key to equal the seat subject, got ${JSON.stringify(keys)}`).toBe(1)
  })
})

describe('becomePersona / returnToSeat', () => {
  it('a stand-in is never persisted', async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    // Sanity: prove the stand-in actually took hold before checking storage, or a
    // silently-failing becomePersona would pass this test for the wrong reason.
    expect(capturedCtx!.user.name, 'sanity: the stand-in did not take effect').toBe(MEMBER.name)

    const stored = localStorage.getItem(SESSION_KEY)
    expect(stored, 'the persisted session must survive a stand-in').not.toBeNull()
    const parsed = JSON.parse(stored!)
    expect(parsed.personaId, 'the persisted blob must still name the seat, not the stand-in').toBe(SEAT_SESSION.persona.id)
    expect(parsed.token, 'the persisted token must still be the seat token').toBe(SEAT_SESSION.token)
    // personaId/token alone don't discriminate seat-vs-standIn here: personaFromMember keeps
    // seat.id, and both sessions carry a null token under the no-gateway mock signIn. verified
    // does discriminate -- the seat fixture is true, a mint's mock result is always false.
    expect(parsed.verified, "the persisted verified flag must still be the seat's, not the mint's").toBe(
      SEAT_SESSION.verified,
    )
  })

  it('a failed mint keeps the current identity', async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    signInMock.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))

    await expect(
      act(async () => {
        await capturedCtx!.becomePersona!(MEMBER, 'invoices')
      }),
      'the rejection must propagate to the caller',
    ).rejects.toThrow('forbidden')

    expect(capturedCtx!.user.name, 'the seat identity must be unchanged').toBe(SEAT_SESSION.persona.name)
    expect(capturedCtx!.user.verified, 'no unverified stand-in may have been created').toBe(true)
    expect(warnSpy, "becomePersona must not degrade like doSignIn's console.warn fallback").not.toHaveBeenCalled()

    warnSpy.mockRestore()
  })

  it('sign out clears both the seat and the stand-in', async () => {
    const { container } = await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    expect(capturedCtx!.user.name, 'sanity: the stand-in did not take effect').toBe(MEMBER.name)

    await act(async () => {
      capturedCtx!.signOut()
    })

    expect(localStorage.getItem(SESSION_KEY), 'the persisted session must be cleared').toBeNull()
    expect(
      container.querySelector('.pf-shell'),
      'the workspace must be gone -- a live stand-in would leave it mounted',
    ).toBeNull()
  })

  it("clicking the seat's own row while standing in returns to the seat", async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    expect(capturedCtx!.user.name, 'sanity: the stand-in did not take effect').toBe(MEMBER.name)

    await act(async () => {
      await capturedCtx!.becomePersona!(SEAT_AS_MEMBER, 'invoices')
    })

    expect(
      capturedCtx!.user.name,
      "the seat's own row must short-circuit back to the seat, not mint a same-subject stand-in",
    ).toBe(SEAT_SESSION.persona.name)
    expect(signInMock, 'the short-circuit must not call signIn a second time for the seat').toHaveBeenCalledTimes(1)
  })
})

describe('carryView: create/detail collapse to invoices, other views pass through', () => {
  it('collapses across both becomePersona and returnToSeat, and leaves a non-collapsed view alone', async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    // Each call below changes the Workspace key (seat <-> stand-in subject), which is
    // required to observe a new `initialView` -- `view` is a useState initializer read
    // once per mount, so calling becomePersona twice on the SAME subject would not remount.
    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'create')
    })
    expect(capturedCtx!.view, 'becomePersona must collapse create to invoices').toBe('invoices')

    await act(async () => {
      capturedCtx!.returnToSeat!('detail')
    })
    expect(capturedCtx!.view, 'returnToSeat must collapse detail to invoices').toBe('invoices')

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'approvals')
    })
    expect(capturedCtx!.view, 'a non-create/detail view must pass through unchanged').toBe('approvals')
  })
})

describe('AC-5: reload while standing in', () => {
  it('returns to the seat', async () => {
    const { unmount } = await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    expect(capturedCtx!.user.name, 'sanity: the stand-in did not take effect').toBe(MEMBER.name)

    unmount()
    capturedCtx = undefined
    vi.resetModules()
    const { default: App } = await import('./App')
    render(<App />)

    expect(capturedCtx, 'Sidebar never rendered on reload').toBeDefined()
    expect(capturedCtx!.user.name, 'a reload must return to the seat, not the stand-in').toBe(SEAT_SESSION.persona.name)
    expect(capturedCtx!.seatSubject, 'seatSubject must still name the seat').toBe(SEAT_SESSION.persona.subject)
    // personaId-only rehydration means user.name/seatSubject alone would read as the seat
    // even if the WRONG session got persisted (personaFromMember keeps seat.id). verified
    // is the one field that would expose a standIn (always false) having been the write.
    expect(capturedCtx!.user.verified, "a reload must rehydrate the seat's verified flag, not a mint's").toBe(
      SEAT_SESSION.verified,
    )
  })
})
