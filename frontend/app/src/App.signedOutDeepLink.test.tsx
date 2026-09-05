// @vitest-environment jsdom
// ROUTE-05-02: the capture call inside the front-door effect (App.tsx:1685-1689), which
// must run under the same activeSession/autoPersona guards as the bounce it precedes.

import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { readDestination } from './lib/deepLink'
import { SESSION_KEY, serializeSession } from './lib/session'
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
function stubLocation(overrides: { search?: string; pathname?: string } = {}) {
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
      hash: '',
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

// Matches sibling App test files that mount a live session; harmless no-op otherwise.
vi.mock('./components/Sidebar', () => ({ Sidebar: () => null }))

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  // sessionStorage has no Node-v25/jsdom collision (deepLink.test.ts:14-16) -- clearing
  // the real one is enough, no stub needed.
  sessionStorage.clear()
  window.history.replaceState(null, '', '/')
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
