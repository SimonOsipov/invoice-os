// @vitest-environment jsdom
// F-021 unit half: a rejected mint (doSignIn's catch) still seats the visitor unverified
// instead of dead-ending. Harness: the real <App/> with an EMPTY store
// (no pre-seeded session); the in-app picker click drives doSignIn.
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY } from './lib/session'
import type { PlatformCtx } from './types'

const { signInMock } = vi.hoisted(() => ({ signInMock: vi.fn() }))

vi.mock('./auth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./auth')>()
  signInMock.mockImplementation(actual.signIn)
  return { ...actual, signIn: signInMock }
})

// The only way to reach the ctx App builds.
let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

// Node v25's native localStorage collides with jsdom's (lib/session.test.ts:230).
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
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

// A pre-seeded signed-in session never reaches doSignIn's catch. This needs an empty store and a picker click.
async function renderAppFresh() {
  vi.resetModules()
  const { default: App } = await import('./App')
  const view = render(<App />)
  fireEvent.click(screen.getByText(APP_PERSONAS.firm.name))
  return view
}

// doSignIn's promise settles across several microtask hops (mock rejection -> catch ->
// setSeat -> commit -> the mirror effect's saveSession), so the write is not necessarily
// there the instant render() returns. waitFor polls until it lands.
function waitForPersistedSession() {
  return waitFor(() => {
    const stored = localStorage.getItem(SESSION_KEY)
    expect(stored, 'no session was persisted -- the fallback setSeat never ran').not.toBeNull()
    return JSON.parse(stored!)
  })
}

describe('offline fallback: a rejected mint still seats the visitor (F-021)', () => {
  it('OFF-1: a rejected mint still seats the visitor -- persists token: null, verified: false, me: null', async () => {
    signInMock.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    await renderAppFresh()

    const parsed = await waitForPersistedSession()
    expect(parsed.token).toBeNull()
    expect(parsed.verified).toBe(false)
    expect(parsed.me).toBeNull()
  })

  it('OFF-2: the seated persona is the one asked for', async () => {
    signInMock.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    await renderAppFresh()

    const parsed = await waitForPersistedSession()
    expect(parsed.personaId).toBe('firm')
  })

  // Fails if console.error fires >0 times before the control call below (a real error
  // escaped App's catch), or if it fires 0 times even after the control call (the spy is
  // not actually wired to the global -- an absence assertion that could never observe one).
  it('OFF-3: it warns, it does not error', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    signInMock.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))

    await renderAppFresh()
    await waitFor(() => expect(warnSpy).toHaveBeenCalledTimes(1))
    expect(errorSpy).not.toHaveBeenCalled()

    // Vacuity control: prove the spy is live and would have caught a real console.error,
    // so "never called" above is a real absence, not a spy that silently isn't attached.
    console.error('control: this deliberate call must be observed')
    expect(errorSpy).toHaveBeenCalledTimes(1)

    warnSpy.mockRestore()
    errorSpy.mockRestore()
  })

  // Discriminator: without this row, OFF-1 would also pass against an app that always
  // seats unverified regardless of what signIn returns.
  it('OFF-4: a resolved, verified mint persists verified: true', async () => {
    signInMock.mockResolvedValueOnce({
      persona: APP_PERSONAS.firm,
      token: 'mock-token',
      me: { tenant: { id: 't1', name: 'Okafor & Partners', kind: 'firm' }, user: { id: 'u1', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' } },
      verified: true,
    } satisfies Session)

    await renderAppFresh()

    const parsed = await waitForPersistedSession()
    expect(parsed.verified).toBe(true)
    // A persona session names its persona, not the /me user (AUTH-09 D1).
    await waitFor(() => expect(capturedCtx?.user.verified).toBe(true))
    expect(capturedCtx?.user.name).toBe(APP_PERSONAS.firm.name)
    expect(capturedCtx?.user.initials).toBe(APP_PERSONAS.firm.initials)
  })

  it('OFF-5: the app opens a workspace rather than dead-ending', async () => {
    signInMock.mockRejectedValueOnce(new ApiError('http', 'forbidden', 403))
    await renderAppFresh()

    await waitFor(() =>
      expect(capturedCtx, 'Sidebar never rendered -- the app dead-ended instead of opening a workspace').toBeDefined(),
    )
    expect(capturedCtx?.user).toEqual({
      name: APP_PERSONAS.firm.name,
      initials: APP_PERSONAS.firm.initials,
      tenantName: null,
      verified: false,
    })
  })

  // Adversarial: the catch branch does not narrow on err's type, so a rejection that isn't
  // an ApiError (a network abort, a raw TypeError) must fall back the same way.
  it('OFF-6: a non-ApiError rejection still seats the visitor unverified', async () => {
    signInMock.mockRejectedValueOnce(new TypeError('network aborted'))
    await renderAppFresh()

    const parsed = await waitForPersistedSession()
    expect(parsed.token).toBeNull()
    expect(parsed.verified).toBe(false)
  })
})
