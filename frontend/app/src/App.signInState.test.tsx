// @vitest-environment jsdom
// The front door and the `?auth=start` bounce carry the stored state.

import { StrictMode } from 'react'
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { captureDestination, readDestination } from './lib/deepLink'
import { SESSION_KEY, serializeSession } from './lib/session'
import App from './App'

const KEY = 'invoice-os.signInState'
const STATE_RE = /^[A-Za-z0-9_-]{43}$/
const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: 'tok', me: null, verified: true }

const { signInSpy } = vi.hoisted(() => ({ signInSpy: vi.fn() }))
vi.mock('./auth', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./auth')>()
  signInSpy.mockImplementation(actual.signIn)
  return { ...actual, signIn: signInSpy }
})
vi.mock('./components/Sidebar', () => ({ Sidebar: () => null }))

// Node 22 (CI) has no web storage globals; Node 25's collide with jsdom's.
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

// Real jsdom location (so replaceState strips are observable); only href writes are recorded.
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

function storedState(): string | null {
  const raw = sessionStorage.getItem(KEY)
  if (raw == null) return null
  try {
    const s = JSON.parse(raw)?.s
    return typeof s === 'string' ? s : null
  } catch {
    return null
  }
}

let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('sessionStorage', createMemoryStorage())
  window.history.replaceState(null, '', '/')
  signInSpy.mockClear()
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
  window.history.replaceState(null, '', '/')
})

describe('the front door carries the stored state (AUTH-05-11)', () => {
  it('StrictMode bounces with one state', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    const { hrefWrites } = interceptHref()
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    expect(hrefWrites.length, 'the front door must navigate').toBeGreaterThan(0)
    const s = storedState()
    expect(hrefWrites).toEqual(hrefWrites.map(() => `https://landing.example/?state=${s}`))
    expect(s).toEqual(expect.stringMatching(STATE_RE))
  })
})

describe('?auth=start bounces to landing with signin=ready (AUTH-05-11)', () => {
  it('auth=start bounces with signin=ready', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(hrefWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(s).toEqual(expect.stringMatching(STATE_RE))
  })

  it('auth=start bounces over a stored session', () => {
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(hrefWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(s).toEqual(expect.stringMatching(STATE_RE))
  })

  it('a captured destination survives the start bounce', () => {
    captureDestination('/audit', '', Date.now())
    expect(readDestination(), 'sanity: the destination is stored before boot').toEqual({ path: '/audit', query: '' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(hrefWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(readDestination()).toEqual({ path: '/audit', query: '' })
  })

  it('persona wins over auth=start', async () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start&persona=firm')
    const { hrefWrites } = interceptHref()
    await act(async () => {
      render(<App />)
    })
    expect(signInSpy).toHaveBeenCalledWith(APP_PERSONAS.firm)
    expect(hrefWrites).toEqual([])
    expect(window.location.search, 'the start param is stripped unused').toBe('')
  })

  it('auth=start without a landing URL shows the picker', () => {
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    render(<App />)
    expect(screen.getByText('Choose an account')).toBeTruthy()
    expect(hrefWrites).toEqual([])
    expect(window.location.search).toBe('')
  })
})

// Adversarial coverage at App level.
const X = 'XxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxX'

describe('signInState adversarial: App', () => {
  it('App adversarial: a URL state beside auth=start is never adopted', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', `/?state=${X}&auth=start`)
    const { hrefWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(s).not.toBe(X)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
  })

  it('App adversarial: a URL state on a sessionless boot is never adopted', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', `/?state=${X}`)
    const { hrefWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(s).not.toBe(X)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${s}`])
  })

  it('App adversarial: the front door reuses a live stored state', () => {
    sessionStorage.setItem(KEY, JSON.stringify({ v: 1, s: X, at: Date.now() }))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    const { hrefWrites } = interceptHref()
    render(<App />)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${X}`])
  })

  it('App adversarial: auth=start over a stored session mounts no Workspace and keeps the destination', () => {
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    captureDestination('/audit', '', Date.now())
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    const { container } = render(<App />)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${storedState()}&signin=ready`])
    expect(container.innerHTML, 'nothing renders while the bounce leaves').toBe('')
    expect(readDestination()).toEqual({ path: '/audit', query: '' })
  })

  it('App adversarial: a stored session without auth=start mounts the Workspace', () => {
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    const { hrefWrites } = interceptHref()
    const { container } = render(<App />)
    expect(hrefWrites).toEqual([])
    expect(container.innerHTML).not.toBe('')
  })

  it('App adversarial: persona wins over auth=start with one strip', async () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start&persona=firm')
    const replace = vi.spyOn(window.history, 'replaceState')
    const { hrefWrites } = interceptHref()
    await act(async () => {
      render(<App />)
    })
    expect(signInSpy).toHaveBeenCalledWith(APP_PERSONAS.firm)
    expect(hrefWrites).toEqual([])
    expect(storedState(), 'no state is minted').toBeNull()
    // The strip writes a null history state; Workspace's own URL writes carry `{ e }`.
    const strips = replace.mock.calls.filter((c) => c[0] === null)
    expect(strips.length).toBe(1)
    expect(window.location.search).toBe('')
  })

  it('App adversarial: StrictMode auth=start navigates once', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    expect(hrefWrites).toEqual([`https://landing.example/?state=${storedState()}&signin=ready`])
  })

  it('App adversarial: a repeated auth param reads the first value', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start&auth=other')
    const { hrefWrites } = interceptHref()
    render(<App />)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${storedState()}&signin=ready`])
    expect(window.location.search).toBe('')
  })

  it('App adversarial: auth=other is stripped and takes the plain front door', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=other&auth=start')
    const { hrefWrites } = interceptHref()
    render(<App />)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${storedState()}`])
    expect(window.location.search).toBe('')
  })

  // Pins executor choice 1: the strip keeps the path and drops the whole query.
  it('App adversarial: auth=other over a stored session keeps the path and strips the query', () => {
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/audit?auth=other&utm_source=x')
    const { hrefWrites } = interceptHref()
    const { container } = render(<App />)
    expect(hrefWrites).toEqual([])
    expect(container.innerHTML).not.toBe('')
    expect(window.location.pathname).toBe('/audit')
    expect(window.location.search).toBe('')
  })

  // No session: Workspace would canonicalise the URL itself.
  it('App adversarial: an unused persona param is not stripped', () => {
    window.history.replaceState(null, '', '/?persona=bogus')
    render(<App />)
    expect(screen.getByText('Choose an account')).toBeTruthy()
    expect(window.location.search).toBe('?persona=bogus')
  })

  // Pins executor choice 4.
  it('App adversarial: no landing URL mints no state', () => {
    const { hrefWrites } = interceptHref()
    render(<App />)
    expect(screen.getByText('Choose an account')).toBeTruthy()
    expect(hrefWrites).toEqual([])
    expect(sessionStorage.getItem(KEY)).toBeNull()
  })

  it('App adversarial: auth=start without a landing URL mints no state', () => {
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    render(<App />)
    expect(screen.getByText('Choose an account')).toBeTruthy()
    expect(hrefWrites).toEqual([])
    expect(sessionStorage.getItem(KEY)).toBeNull()
  })
})

// F1b: the start bounce always mints, so landing holds a state with the full TTL.
describe('?auth=start mints a fresh state', () => {
  const NOW = new Date('2026-09-25T12:00:00Z').getTime()
  const NINE_MIN = 9 * 60 * 1000

  function storedAt(): unknown {
    const raw = sessionStorage.getItem(KEY)
    return raw == null ? null : (JSON.parse(raw) as { at?: unknown }).at
  }

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(NOW)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('auth=start replaces a live stored state and stamps it now', () => {
    sessionStorage.setItem(KEY, JSON.stringify({ v: 1, s: X, at: NOW - NINE_MIN }))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(s, 'the live stored state is not reused').not.toBe(X)
    expect(storedAt()).toBe(NOW)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
  })

  it('StrictMode auth=start over a live state navigates once with the stored fresh state', () => {
    sessionStorage.setItem(KEY, JSON.stringify({ v: 1, s: X, at: NOW - NINE_MIN }))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites } = interceptHref()
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    const s = storedState()
    expect(s, 'the live stored state is not reused').not.toBe(X)
    expect(storedAt()).toBe(NOW)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
  })

  it('control: the front door still reuses the same live state', () => {
    sessionStorage.setItem(KEY, JSON.stringify({ v: 1, s: X, at: NOW - 30_000 }))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    const { hrefWrites } = interceptHref()
    render(<App />)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${X}`])
    expect(storedAt()).toBe(NOW - 30_000)
  })
})

// Landing holds a state 9 min, so the front door reuses one only while it has that much TTL left.
describe('the front door re-mints a state older than a minute', () => {
  const NOW = new Date('2026-09-25T12:00:00Z').getTime()
  const MINUTE = 60 * 1000

  function storedAt(): unknown {
    const raw = sessionStorage.getItem(KEY)
    return raw == null ? null : (JSON.parse(raw) as { at?: unknown }).at
  }

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(NOW)
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('a state one ms under a minute old is reused and not re-stamped', () => {
    sessionStorage.setItem(KEY, JSON.stringify({ v: 1, s: X, at: NOW - MINUTE + 1 }))
    const { hrefWrites } = interceptHref()
    render(<App />)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${X}`])
    expect(storedState()).toBe(X)
    expect(storedAt()).toBe(NOW - MINUTE + 1)
  })

  for (const [label, age] of [
    ['a minute', MINUTE],
    ['9.5 minutes', 9.5 * MINUTE],
  ] as const) {
    it(`a state ${label} old is replaced by a new one stamped now`, () => {
      sessionStorage.setItem(KEY, JSON.stringify({ v: 1, s: X, at: NOW - age }))
      const { hrefWrites } = interceptHref()
      render(<App />)
      const s = storedState()
      expect(s).toEqual(expect.stringMatching(STATE_RE))
      expect(s, 'the aged state is not reused').not.toBe(X)
      expect(storedAt()).toBe(NOW)
      expect(hrefWrites).toEqual([`https://landing.example/?state=${s}`])
    })
  }

  it('StrictMode over an aged state navigates once with one fresh state', () => {
    sessionStorage.setItem(KEY, JSON.stringify({ v: 1, s: X, at: NOW - 9.5 * MINUTE }))
    const { hrefWrites } = interceptHref()
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    const s = storedState()
    expect(s).not.toBe(X)
    expect(storedAt()).toBe(NOW)
    expect(hrefWrites).toEqual([`https://landing.example/?state=${s}`])
  })
})
