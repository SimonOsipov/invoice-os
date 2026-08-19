// @vitest-environment jsdom
// First jsdom coverage of <App/> (DEMO-06-02). DEMO_MODE binds at module scope
// (demo/flag.ts), so every flag-on case needs vi.stubEnv + vi.resetModules() + a
// dynamic import, same idiom as demo/flag.test.ts. No gateway is configured under
// vitest, so signIn never hits the network by default (auth.ts) -- only the failed-
// mint test needs to force a rejection.
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import { APP_PERSONAS, type Persona, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import { accessRoleLabel } from './lib/members'
import type { Member } from './lib/members'
import { TOAST_META, TOAST_TITLE } from './demo/copy'
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

// A second, distinct stand-in -- rows that must prove the LATER switch's identity
// (toast, timer restart), not just that a switch happened at all.
const MEMBER_B: Member = {
  id: 'm-standin-002',
  name: 'Ngozi Chukwu',
  initials: 'NC',
  email: 'ngozi@example.ng',
  role: 'reviewer',
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
  vi.useRealTimers()
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

  // D5: "the floor is not held on failure" -- Promise.all rejects at round-trip
  // time, not floor time. Fake timers with ZERO advancement: if the rejection needed
  // the 700ms floor to elapse, this test would hang/timeout rather than settle.
  it('a rejecting mint surfaces the error immediately, without waiting for the 700ms floor', async () => {
    vi.useFakeTimers()
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    signInMock.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))

    await expect(
      act(async () => {
        await capturedCtx!.becomePersona!(MEMBER, 'invoices')
      }),
      'the rejection must settle without any timer advancement past the floor',
    ).rejects.toThrow('forbidden')
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

  // The untested branch: the seat's own row is reachable and clickable even when
  // NOT standing in (PersonaPopover renders it same as any other row). No prior
  // subtask exercised this -- must be a true no-op, not just "no toast".
  it("clicking the seat's own row while already seated is a no-op -- no toast, no remount, no mint", async () => {
    const { container } = await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    const shellBefore = container.querySelector('.pf-shell')
    expect(shellBefore, 'Workspace did not render -- no .pf-shell in the tree').not.toBeNull()

    await act(async () => {
      await capturedCtx!.becomePersona!(SEAT_AS_MEMBER, 'invoices')
    })

    expect(capturedCtx!.user.name, 'identity must be unchanged').toBe(SEAT_SESSION.persona.name)
    expect(screen.queryByTestId('persona-toast'), 'no toast -- nothing was standing in to return from').toBeNull()
    expect(container.querySelector('.pf-shell'), 'the workspace must not remount -- same subject, same key').toBe(shellBefore)
    expect(signInMock, 'a same-subject click must never mint').not.toHaveBeenCalled()
  })

  it('an instant mint still holds the identity for 700ms', async () => {
    vi.useFakeTimers()
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    let call!: Promise<void>
    act(() => {
      call = capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(699)
    })
    expect(capturedCtx!.user.name, 'the floor must not have committed at 699ms').toBe(SEAT_SESSION.persona.name)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2)
    })
    expect(capturedCtx!.user.name, 'the floor must have committed by 701ms').toBe(MEMBER.name)
    await call
  })

  // GUARD: green today (becomePersona has no floor yet, so it is purely gated by the
  // round trip either way) -- goes red only against a floor written as a race
  // (Promise.race) that abandons the round trip instead of awaiting it (Promise.all).
  it('a slow mint is not committed early', async () => {
    vi.useFakeTimers()
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    signInMock.mockImplementationOnce(
      (persona: Persona) => new Promise<Session>((resolve) => setTimeout(() => resolve({ persona, token: null, me: null, verified: false }), 1500)),
    )

    let call!: Promise<void>
    act(() => {
      call = capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1499)
    })
    expect(capturedCtx!.user.name, 'the round trip has not settled at 1499ms').toBe(SEAT_SESSION.persona.name)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2)
    })
    expect(capturedCtx!.user.name, 'the round trip settled by 1501ms').toBe(MEMBER.name)
    await call
  })

  // Only the sign-out half of D7's race guard had a red-first test. This covers the
  // other half: two overlapping becomePersona calls, with the earlier-started one's
  // mint settling AFTER the later one has already committed.
  it('the later of two overlapping switches wins even when the earlier mint resolves second', async () => {
    vi.useFakeTimers()
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    let resolveA!: () => void
    let resolveB!: () => void
    signInMock.mockImplementationOnce(
      (persona: Persona) => new Promise<Session>((resolve) => { resolveA = () => resolve({ persona, token: null, me: null, verified: false }) }),
    )
    signInMock.mockImplementationOnce(
      (persona: Persona) => new Promise<Session>((resolve) => { resolveB = () => resolve({ persona, token: null, me: null, verified: false }) }),
    )

    let callA!: Promise<void>
    let callB!: Promise<void>
    act(() => {
      callA = capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    act(() => {
      callB = capturedCtx!.becomePersona!(MEMBER_B, 'invoices')
    })

    // B (started second, higher generation) settles first.
    await act(async () => {
      resolveB()
      await vi.advanceTimersByTimeAsync(700)
    })
    await callB
    expect(capturedCtx!.user.name, 'the later-started switch must commit').toBe(MEMBER_B.name)

    // A (started first, now stale) settles after -- it must not resurrect MEMBER.
    await act(async () => {
      resolveA()
      await vi.advanceTimersByTimeAsync(700)
    })
    await callA
    expect(capturedCtx!.user.name, 'a stale earlier switch arriving late must not overwrite the later one').toBe(MEMBER_B.name)
  })

  it('a mint that lands after sign-out cannot resurrect the workspace', async () => {
    vi.useFakeTimers()
    const { container } = await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    let resolveMint!: () => void
    signInMock.mockImplementationOnce(
      (persona: Persona) =>
        new Promise<Session>((resolve) => {
          resolveMint = () => resolve({ persona, token: null, me: null, verified: false })
        }),
    )

    let call!: Promise<void>
    act(() => {
      call = capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })

    await act(async () => {
      capturedCtx!.signOut()
    })
    expect(localStorage.getItem(SESSION_KEY), 'sign-out must clear the persisted session immediately').toBeNull()

    await act(async () => {
      resolveMint()
      await vi.advanceTimersByTimeAsync(750)
    })
    await call

    expect(container.querySelector('.pf-shell'), 'a late mint must not resurrect the workspace after sign-out').toBeNull()
    expect(localStorage.getItem(SESSION_KEY), 'sign-out must stay cleared').toBeNull()
  })

  it('a return that lands after sign-out commits nothing (task-594, DEMO-06-06, AC-6)', async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    // Real timers for the sanity switch (same idiom as the carryView describe below);
    // fake timers only start once the race under test begins.
    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    expect(capturedCtx!.user.name, 'sanity: the stand-in did not take effect').toBe(MEMBER.name)

    vi.useFakeTimers()
    let call!: Promise<void>
    act(() => {
      call = capturedCtx!.returnToSeat!('approvals', SEAT_AS_MEMBER)
    })

    await act(async () => {
      capturedCtx!.signOut()
    })
    expect(localStorage.getItem(SESSION_KEY), 'sign-out must clear the persisted session immediately').toBeNull()

    await act(async () => {
      await vi.advanceTimersByTimeAsync(750)
    })
    await call

    expect(screen.getByText('Choose an account'), 'the picker must render after sign-out').toBeTruthy()
    const pickButton = screen.getByText(SEAT_SESSION.persona.name).closest('button')
    expect(pickButton, 'the firm persona button was not found in the picker').toBeTruthy()
    await act(async () => {
      fireEvent.click(pickButton as HTMLButtonElement)
    })

    expect(
      screen.queryByTestId('persona-toast'),
      'a return that commits after sign-out must not announce a switch to the next sign-in',
    ).toBeNull()
    expect(capturedCtx!.view, 'a return that commits after sign-out must not carry its view into the next sign-in').toBe('dashboard')
  })

  it('signOut clears a mounted toast so the next sign-in does not see it (task-594, DEMO-06-06)', async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    expect(screen.getByTestId('persona-toast'), 'sanity: the switch toast did not mount').toBeTruthy()

    await act(async () => {
      capturedCtx!.signOut()
    })

    expect(screen.getByText('Choose an account'), 'the picker must render after sign-out').toBeTruthy()
    const pickButton = screen.getByText(SEAT_SESSION.persona.name).closest('button')
    expect(pickButton, 'the firm persona button was not found in the picker').toBeTruthy()
    await act(async () => {
      fireEvent.click(pickButton as HTMLButtonElement)
    })

    expect(
      screen.queryByTestId('persona-toast'),
      'signOut must clear the toast so it cannot reappear on the next sign-in',
    ).toBeNull()
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
      await capturedCtx!.returnToSeat!('detail', SEAT_AS_MEMBER)
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

describe('toast (DEMO-06-05)', () => {
  it('a completed switch renders the toast; a failed switch renders none', async () => {
    const { unmount } = await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    expect(capturedCtx!.user.name, 'sanity: the stand-in did not take effect').toBe(MEMBER.name)
    expect(
      screen.getByTestId('persona-toast').textContent,
      'a completed switch must render the toast for the new identity',
    ).toContain(TOAST_TITLE.replace('{full name}', MEMBER.name))

    // Paired deliberately: the failure half alone is green today (no toast exists at
    // all), so only the success half above proves this row red. Fresh App, not a second
    // call on the same one, so no lingering toast from the success half muddies "none".
    unmount()
    capturedCtx = undefined
    const { unmount: unmount2 } = await renderAppWithSeat(true)

    signInMock.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    await expect(
      act(async () => {
        await capturedCtx!.becomePersona!(MEMBER_B, 'invoices')
      }),
      'the rejection must propagate to the caller',
    ).rejects.toThrow('forbidden')

    expect(capturedCtx!.user.name, 'a failed switch must not change identity').toBe(SEAT_SESSION.persona.name)
    expect(screen.queryByTestId('persona-toast'), 'a failed switch must render no toast').toBeNull()
    unmount2()
  })

  it('returning after two switches toasts the seat, not the previous stand-in', async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    expect(capturedCtx!.user.name, 'sanity: the first stand-in did not take effect').toBe(MEMBER.name)

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER_B, 'invoices')
    })
    expect(capturedCtx!.user.name, 'sanity: the second stand-in did not take effect').toBe(MEMBER_B.name)

    await act(async () => {
      await capturedCtx!.returnToSeat!('invoices', SEAT_AS_MEMBER)
    })

    expect(capturedCtx!.user.name, 'returning must land on the seat').toBe(SEAT_SESSION.persona.name)
    expect(capturedCtx!.seatSubject, 'the seat identity must be unchanged').toBe(SEAT_SESSION.persona.subject)
    expect(
      screen.getByTestId('persona-toast').textContent,
      'the return toast must name the seat, not the previous stand-in',
    ).toContain(TOAST_TITLE.replace('{full name}', SEAT_SESSION.persona.name))
  })

  it('a second switch replaces the toast and restarts its timer', async () => {
    vi.useFakeTimers()
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    let firstCall!: Promise<void>
    act(() => {
      firstCall = capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(700)
    })
    await firstCall
    expect(screen.getByTestId('persona-toast').textContent).toContain(TOAST_TITLE.replace('{full name}', MEMBER.name))

    // An un-restarted timer from the first toast's mount (t=700) would fire at its
    // own +5200ms deadline (t=5900). Start the second switch well before that, so the
    // second toast is already mounted (and must survive) when that stale deadline lands.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(900)
    })

    let secondCall!: Promise<void>
    act(() => {
      secondCall = capturedCtx!.becomePersona!(MEMBER_B, 'invoices')
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(700)
    })
    await secondCall
    expect(screen.getByTestId('persona-toast').textContent).toContain(TOAST_TITLE.replace('{full name}', MEMBER_B.name))

    // t is now 2300ms; cross the first toast's stale 5900ms deadline while staying
    // well inside the second toast's own fresh 5200ms budget (expires at 7500ms).
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4000)
    })

    expect(
      screen.getByTestId('persona-toast').textContent,
      "a restarted toast must survive the first toast's stale deadline",
    ).toContain(TOAST_TITLE.replace('{full name}', MEMBER_B.name))
  })

  // The remount-survival claim (App.tsx comment above <PersonaToast>) has no other
  // oracle: DOM containment is the one check that would catch a future edit rendering
  // the toast inside Workspace's own returned tree (`.pf-shell`) instead of beside it.
  it('the toast sits outside the keyed .pf-shell root, not inside it', async () => {
    const { container } = await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    expect(capturedCtx!.user.name, 'sanity: the stand-in did not take effect').toBe(MEMBER.name)

    const shell = container.querySelector('.pf-shell')
    const toastEl = screen.getByTestId('persona-toast')
    expect(shell, 'Workspace did not render -- no .pf-shell in the tree').not.toBeNull()
    expect(
      shell!.contains(toastEl),
      'the toast must not be a descendant of the keyed Workspace root -- a remount would destroy it there',
    ).toBe(false)
  })

  // The "restart" claim above (D2: PersonaToast key={toast.seq}) turns out to be
  // masked by onDismiss's own inline-closure identity, which ALSO changes every App
  // render and would independently restart the effect's timer -- proven by reverting
  // the key alone and finding the timer-restart test above still green. This checks
  // the key's specific contribution: a second switch must produce a genuinely NEW
  // toast DOM node (a remount), not an in-place prop update on the same node.
  it('a second switch remounts the toast node, not just its props', async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    const firstNode = screen.getByTestId('persona-toast')

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER_B, 'invoices')
    })
    const secondNode = screen.getByTestId('persona-toast')

    expect(secondNode, 'a second switch must mount a fresh toast node, not patch the old one in place').not.toBe(firstNode)
  })

  it("the return toast names the seat's access role with an empty roster", async () => {
    await renderAppWithSeat(true)
    expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
    expect(capturedCtx!.members, 'sanity: no gateway is configured, so the roster never resolves').toEqual([])

    await act(async () => {
      await capturedCtx!.becomePersona!(MEMBER, 'invoices')
    })
    expect(capturedCtx!.user.name, 'sanity: the stand-in did not take effect').toBe(MEMBER.name)

    await act(async () => {
      await capturedCtx!.returnToSeat!('invoices', SEAT_AS_MEMBER)
    })

    expect(capturedCtx!.user.name, 'returning must land on the seat').toBe(SEAT_SESSION.persona.name)
    expect(capturedCtx!.members, 'the roster must still be empty -- no gateway is configured').toEqual([])
    expect(screen.getByTestId('persona-toast-meta').textContent).toBe(
      TOAST_META.replace('{ROLE}', accessRoleLabel(SEAT_AS_MEMBER.role).toUpperCase()),
    )
  })
})
