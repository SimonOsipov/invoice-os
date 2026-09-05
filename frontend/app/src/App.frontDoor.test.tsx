// @vitest-environment jsdom
// F-201 unit half: the NAVIGATING arm of the front door (App.tsx:1629-1633, render gate
// :1644) plus the one control (FD-5) proving it isn't an unconditional redirect. The
// STANDALONE arm's own assertions belong to App.suspended.test.tsx; FD-5 does not repeat
// them beyond this file's own discriminator.

import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import App from './App'

// landingBase() is a call-time read of import.meta.env.VITE_LANDING_URL (auth.ts:70-73),
// so vi.stubEnv alone flips the arm -- no vi.resetModules()/dynamic import needed here.

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

// One stub serves both jobs: capturing an `href` assignment AND seeding the query string
// `autoPersona` reads once at first render (App.tsx:1476-1479) -- both read the same
// window.location. history.replaceState (App.tsx:1615-1620) writes to jsdom's real,
// un-stubbed internal location and never touches this object.
function stubLocation(overrides: { search?: string } = {}) {
  Object.defineProperty(window, 'location', {
    configurable: true,
    writable: true,
    value: {
      href: 'http://localhost/',
      pathname: '/',
      hash: '',
      hostname: 'localhost',
      origin: 'http://localhost',
      search: overrides.search ?? '',
    },
  })
}

// Same two-signal check App.suspended.test.tsx's workspaceIsRendered() uses -- one alone
// could survive a half-done replacement.
function workspaceIsRendered(): boolean {
  return screen.queryByTestId('env-banner') !== null || document.querySelector('.pf-shell') !== null
}

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: 'tok', me: null, verified: true }

// Matches 5/5 sibling App test files that mount a live session (FD-4 is the row that
// reaches Workspace); harmless no-op for the other rows.
vi.mock('./components/Sidebar', () => ({ Sidebar: () => null }))

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
})

describe('front door: the redirect arm (F-201)', () => {
  it('vacuity control: the location stub captures a direct assignment', () => {
    // Without this, every "href untouched" assertion below would pass against an
    // untouched jsdom location that swallows the write silently (measured: no throw,
    // no href change, only jsdom's virtual console sees it).
    stubLocation()
    window.location.href = 'https://example.com/probe'
    expect(window.location.href).toBe('https://example.com/probe')
  })

  it('FD-1: a sessionless visit navigates to landing', () => {
    stubLocation()
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(window.location.href).toBe('https://landing.example')
  })

  it('FD-2: it offers no second sign-in', () => {
    stubLocation()
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(screen.queryByText('Choose an account')).toBeNull()
  })

  it('FD-3: a ?persona= hand-off suppresses the redirect', async () => {
    // Fails if the `|| autoPersona` half of the guard is dropped -- the effect would fire
    // the assignment and this would read the landing URL instead of "untouched".
    stubLocation({ search: '?persona=firm' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    // autoPersona truthy fires a real auto-sign-in (VITE_GATEWAY_URL stays unset, so it
    // resolves with no network); await act so that pending promise settles before assert.
    await act(async () => {
      render(<App />)
    })
    expect(window.location.href).toBe('http://localhost/')
  })

  it('FD-4: an existing session suppresses the redirect', () => {
    // Fails if `activeSession ||` is dropped -- the effect would fire the assignment over
    // a live session and bounce every signed-in reload back to landing.
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    stubLocation()
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(window.location.href).toBe('http://localhost/')
    // Second, independent oracle: a malformed session blob would crash before the effect
    // ever ran, making href read "untouched" for the wrong reason.
    expect(workspaceIsRendered()).toBe(true)
  })

  it('FD-5 control: the standalone arm keeps its own picker', () => {
    // Fails if the guard is dropped entirely (unconditional assignment) -- with
    // VITE_LANDING_URL unset dest is null, so this proves FD-1/FD-2 aren't passing
    // against an app that redirects no matter what.
    stubLocation()
    render(<App />)
    expect(window.location.href).toBe('http://localhost/')
    expect(screen.getByText('Choose an account')).toBeTruthy()
  })

  // Adversarial: landingBase() trims before checking truthiness (auth.ts:71), so a
  // whitespace-only VITE_LANDING_URL is the boundary between the two arms, not just "unset".
  it('FD-6: a landing URL that trims to empty behaves like unset', () => {
    stubLocation()
    vi.stubEnv('VITE_LANDING_URL', '   ')
    render(<App />)
    expect(window.location.href).toBe('http://localhost/')
    expect(screen.getByText('Choose an account')).toBeTruthy()
  })
})
