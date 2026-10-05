// @vitest-environment jsdom
// F-201 unit half: the NAVIGATING arm of the front door (App.tsx:1629-1633, render gate
// :1644) plus the one control (FD-5) proving it isn't an unconditional redirect. The
// STANDALONE arm's own assertions belong to App.suspended.test.tsx; FD-5 does not repeat
// them beyond this file's own discriminator.

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Me, type Session } from './auth'
import { SESSION_KEY, serializeSession } from './lib/session'
import App from './App'

// landingBase() is a call-time read of import.meta.env.VITE_LANDING_URL (auth.ts:70-73),
// so vi.stubEnv alone flips the arm -- no vi.resetModules()/dynamic import needed here.

let originalLocation: PropertyDescriptor | undefined

// Node v25's native localStorage collides with jsdom's.
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

// One stub serves both jobs: capturing an `href` assignment AND seeding the query string.
// history.replaceState writes to jsdom's real, un-stubbed internal location and never touches it.
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

function storedSignInState(): string | null {
  const raw = sessionStorage.getItem('invoice-os.signInState')
  if (raw == null) return null
  try {
    const s = JSON.parse(raw)?.s
    return typeof s === 'string' ? s : null
  } catch {
    return null
  }
}

const GATEWAY = 'https://gw.test'
const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: null },
}

// Records every href write; `search` seeds the boot query.
function stubRecordingLocation(search: string) {
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
      pathname: '/',
      hash: '',
      hostname: 'localhost',
      origin: 'http://localhost',
      search,
    },
  })
  return { hrefWrites }
}

// Returns the list every fetched URL is pushed onto.
function stubGatewayFetch(): string[] {
  const urls: string[] = []
  const reply = (body: unknown) =>
    Promise.resolve({ ok: true, status: 200, statusText: 'OK', json: () => Promise.resolve(body) })
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      urls.push(url)
      if (url === `${GATEWAY}/auth/login`) return reply({ access_token: 'a.b.c' })
      if (url === `${GATEWAY}/api/tenancy/v1/me`) return reply(ME)
      return reply({ entities: [], policies: [], members: [], roles: [], invoices: [], total: 0, clients: [] })
    }),
  )
  return urls
}

async function settle(ms = 30) {
  await act(async () => {
    await new Promise((r) => setTimeout(r, ms))
  })
}

// Matches 5/5 sibling App test files that mount a live session (FD-4 is the row that
// reaches Workspace); harmless no-op for the other rows.
vi.mock('./components/Sidebar', () => ({ Sidebar: () => null }))

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
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
    const s = storedSignInState()
    expect(window.location.href).toBe(`https://landing.example/?state=${s}`)
    expect(s).toEqual(expect.stringMatching(/^[A-Za-z0-9_-]{43}$/))
  })

  it('FD-2: it offers no second sign-in', () => {
    stubLocation()
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    render(<App />)
    expect(screen.queryByText('Choose an account')).toBeNull()
  })

  // `?persona=` is unowned: the sweep covers the two openable ids and three that never were.
  it.each(['firm', 'inhouse', 'developer', 'support', 'bogus'])(
    'FD-3: a ?persona= link is not a credential (×5 params) [%s]',
    async (p) => {
      const { hrefWrites } = stubRecordingLocation(`?persona=${p}`)
      vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
      vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
      const fetched = stubGatewayFetch()
      await act(async () => {
        render(<App />)
      })
      await settle()

      expect(hrefWrites, 'the link must leave for the landing sign-in URL once').toHaveLength(1)
      expect(hrefWrites[0]).toBe(`https://landing.example/?state=${storedSignInState()}`)
      expect(storedSignInState()).toEqual(expect.stringMatching(/^[A-Za-z0-9_-]{43}$/))
      expect(fetched.filter((u) => u === `${GATEWAY}/auth/login`), 'no mint').toEqual([])
      expect(localStorage.getItem(SESSION_KEY), 'no session is stored').toBeNull()
    },
  )

  // Control: the same spy and harness see a mint when the in-app picker drives one.
  it('FD-3-neg: the sweep\'s spy sees a mint', async () => {
    const { hrefWrites } = stubRecordingLocation('')
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
    const fetched = stubGatewayFetch()
    await act(async () => {
      render(<App />)
    })
    fireEvent.click(screen.getByText(APP_PERSONAS.firm.name))
    await waitFor(() => expect(fetched.filter((u) => u === `${GATEWAY}/auth/login`)).toHaveLength(1))
    await settle()

    expect(hrefWrites).toEqual([])
    expect(localStorage.getItem(SESSION_KEY), 'the picker mint stores a session').not.toBeNull()
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
