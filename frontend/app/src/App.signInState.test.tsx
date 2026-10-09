// @vitest-environment jsdom
// The front door and the `?auth=start` bounce carry the stored state.

import { StrictMode } from 'react'
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import { captureDestination, readDestination } from './lib/deepLink'
import { SESSION_KEY, serializeSession } from './lib/session'
import { ensureSignInState } from './lib/signInState'
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
  const replaceWrites: string[] = []
  const real = window.location
  // Plain target: jsdom's location.replace is non-configurable, so a get trap on the real location cannot override it.
  const proxy = new Proxy({} as Location, {
    set(_t, prop, value) {
      if (prop === 'href') {
        hrefWrites.push(value)
        return true
      }
      return Reflect.set(real, prop, value)
    },
    get(_t, prop) {
      if (prop === 'replace') return (url: string) => void replaceWrites.push(url)
      const v = (real as unknown as Record<PropertyKey, unknown>)[prop]
      return typeof v === 'function' ? v.bind(real) : v
    },
  })
  Object.defineProperty(window, 'location', { configurable: true, value: proxy })
  return { hrefWrites, replaceWrites }
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
    const { hrefWrites, replaceWrites } = interceptHref()
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    expect(hrefWrites.length, 'the front door must navigate').toBeGreaterThan(0)
    expect(replaceWrites).toEqual([])
    const s = storedState()
    expect(hrefWrites).toEqual(hrefWrites.map(() => `https://landing.example/?state=${s}`))
    expect(s).toEqual(expect.stringMatching(STATE_RE))
  })
})

describe('?auth=start bounces to landing with signin=ready (AUTH-05-11)', () => {
  it('auth=start bounces with signin=ready', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites, replaceWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(replaceWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(hrefWrites).toEqual([])
    expect(s).toEqual(expect.stringMatching(STATE_RE))
  })

  it('auth=start bounces over a stored session', () => {
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites, replaceWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(replaceWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(hrefWrites).toEqual([])
    expect(s).toEqual(expect.stringMatching(STATE_RE))
  })

  it('a captured destination survives the start bounce', () => {
    captureDestination('/audit', '', Date.now())
    expect(readDestination(), 'sanity: the destination is stored before boot').toEqual({ path: '/audit', query: '' })
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites, replaceWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(replaceWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(hrefWrites).toEqual([])
    expect(readDestination()).toEqual({ path: '/audit', query: '' })
  })

  it('auth=start wins over an inert persona', async () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start&persona=firm')
    const replace = vi.spyOn(window.history, 'replaceState')
    const { hrefWrites, replaceWrites } = interceptHref()
    await act(async () => {
      render(<App />)
    })
    const s = storedState()
    expect(replaceWrites, 'the start bounce, as without persona=').toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(hrefWrites).toEqual([])
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(signInSpy, 'no mint').not.toHaveBeenCalled()
    // The strip writes a null history state; Workspace's own URL writes carry `{ e }`.
    expect(replace.mock.calls.filter((c) => c[0] === null)).toHaveLength(1)
    expect(window.location.search).toBe('')
  })

  it('auth=start without a landing URL shows the picker', () => {
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites, replaceWrites } = interceptHref()
    render(<App />)
    expect(screen.getByText('Choose an account')).toBeTruthy()
    expect(hrefWrites).toEqual([])
    expect(replaceWrites).toEqual([])
    expect(window.location.search).toBe('')
  })
})

// Adversarial coverage at App level.
const X = 'XxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxX'

describe('signInState adversarial: App', () => {
  it('App adversarial: a URL state beside auth=start is never adopted', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', `/?state=${X}&auth=start`)
    const { hrefWrites, replaceWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(s).not.toBe(X)
    expect(replaceWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(hrefWrites).toEqual([])
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
    const { hrefWrites, replaceWrites } = interceptHref()
    const { container } = render(<App />)
    expect(replaceWrites).toEqual([`https://landing.example/?state=${storedState()}&signin=ready`])
    expect(hrefWrites).toEqual([])
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

  it('App adversarial: StrictMode auth=start navigates once', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites, replaceWrites } = interceptHref()
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    expect(replaceWrites).toEqual([`https://landing.example/?state=${storedState()}&signin=ready`])
    expect(hrefWrites).toEqual([])
  })

  it('App adversarial: a repeated auth param reads the first value', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start&auth=other')
    const { hrefWrites, replaceWrites } = interceptHref()
    render(<App />)
    expect(replaceWrites).toEqual([`https://landing.example/?state=${storedState()}&signin=ready`])
    expect(hrefWrites).toEqual([])
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
    const { hrefWrites, replaceWrites } = interceptHref()
    render(<App />)
    expect(screen.getByText('Choose an account')).toBeTruthy()
    expect(hrefWrites).toEqual([])
    expect(replaceWrites).toEqual([])
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
    const { hrefWrites, replaceWrites } = interceptHref()
    render(<App />)
    const s = storedState()
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(s, 'the live stored state is not reused').not.toBe(X)
    expect(storedAt()).toBe(NOW)
    expect(replaceWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(hrefWrites).toEqual([])
  })

  it('StrictMode auth=start over a live state navigates once with the stored fresh state', () => {
    sessionStorage.setItem(KEY, JSON.stringify({ v: 1, s: X, at: NOW - NINE_MIN }))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites, replaceWrites } = interceptHref()
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    const s = storedState()
    expect(s, 'the live stored state is not reused').not.toBe(X)
    expect(storedAt()).toBe(NOW)
    expect(replaceWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(hrefWrites).toEqual([])
  })

  // Under a minute old, ensureSignInState would reuse it; only a mint replaces it.
  it('auth=start replaces a state under a minute old, StrictMode included', () => {
    sessionStorage.setItem(KEY, JSON.stringify({ v: 1, s: X, at: NOW - 30_000 }))
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    window.history.replaceState(null, '', '/?auth=start')
    const { hrefWrites, replaceWrites } = interceptHref()
    render(
      <StrictMode>
        <App />
      </StrictMode>,
    )
    const s = storedState()
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(s, 'the stored state is not reused').not.toBe(X)
    expect(storedAt()).toBe(NOW)
    expect(replaceWrites).toEqual([`https://landing.example/?state=${s}&signin=ready`])
    expect(hrefWrites).toEqual([])
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

describe('?auth=verify opens the gateway confirm page (LOGFIX-04-05)', () => {
  const GATEWAY = 'https://gw.test'
  const T = 'tok_123-abc'
  const configure = () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  }
  const boot = (url: string, strict = false) => {
    window.history.replaceState(null, '', url)
    const { hrefWrites } = interceptHref()
    render(strict ? <StrictMode><App /></StrictMode> : <App />)
    return hrefWrites
  }

  it('auth=verify in a fresh tab mints a state and opens the confirm page', () => {
    configure()
    const hrefWrites = boot(`/?auth=verify#token=${T}`)
    const s = storedState()
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(hrefWrites).toEqual([`${GATEWAY}/auth/verify?token=${T}&type=signup&state=${s}`])
    expect(sessionStorage.getItem('invoice-os.pendingVerify')).not.toBeNull()
  })

  it('the confirm token is stripped from the address bar and never logged or stored', () => {
    configure()
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) => vi.spyOn(console, m))
    boot(`/?auth=verify#token=${T}`)
    expect(window.location.hash).toBe('')
    expect(window.location.search).toBe('')
    expect(spies.flatMap((sp) => sp.mock.calls).filter((c) => JSON.stringify(c).includes(T))).toEqual([])
    const stored = [localStorage, sessionStorage].flatMap((st) => Object.keys(st).map((k) => st.getItem(k) ?? ''))
    expect(stored.length).toBeGreaterThan(0)
    expect(stored.filter((v) => v.includes(T))).toEqual([])
  })

  it('StrictMode auth=verify navigates once', () => {
    configure()
    const hrefWrites = boot(`/?auth=verify#token=${T}`, true)
    expect(hrefWrites).toEqual([`${GATEWAY}/auth/verify?token=${T}&type=signup&state=${storedState()}`])
  })

  it('auth=verify bounces over a stored session', () => {
    configure()
    localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
    captureDestination('/audit', '', Date.now())
    const hrefWrites = boot(`/?auth=verify#token=${T}`)
    expect(hrefWrites).toEqual([`${GATEWAY}/auth/verify?token=${T}&type=signup&state=${storedState()}`])
    expect(document.body.textContent).toBe('')
    expect(readDestination()).toEqual({ path: '/audit', query: '' })
  })

  it('?handoff= wins over ?auth=verify', async () => {
    configure()
    const exchange = vi.fn(() => Promise.reject(new TypeError('Failed to fetch')))
    vi.stubGlobal('fetch', exchange)
    ensureSignInState()
    const hrefWrites = boot(`/?handoff=${'C'.repeat(43)}&auth=verify#token=${T}`)
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect((exchange.mock.calls as unknown[][]).map((c) => c[0])).toContain(`${GATEWAY}/auth/exchange`)
    expect(hrefWrites.filter((h) => h.includes('/auth/verify'))).toEqual([])
  })

  it('auth=verify with a bad token goes to the failed notice', () => {
    for (const hash of ['#token=a b', '']) {
      cleanup()
      configure()
      const hrefWrites = boot(`/?auth=verify${hash}`)
      expect(hrefWrites).toEqual(['https://landing.example/?verify=failed'])
      expect(sessionStorage.getItem('invoice-os.pendingVerify')).toBeNull()
    }
    cleanup()
    vi.unstubAllEnvs()
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    expect(boot(`/?auth=verify#token=${T}`)).toEqual(['https://landing.example/?verify=failed'])
  })

  it('auth=verify with a bad token and no landing falls through to the picker, not a blank page', () => {
    vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
    const hrefWrites = boot('/?auth=verify')
    expect(hrefWrites).toEqual([])
    expect(document.body.textContent).not.toBe('')
  })

  it('a bfcache restore of the spent verify bounce leaves the blank page for the front door', () => {
    configure()
    const hrefWrites = boot(`/?auth=verify#token=${T}`)
    expect(hrefWrites).toHaveLength(1)
    const pageshow = (persisted: boolean) =>
      act(() => {
        const e = new Event('pageshow') as PageTransitionEvent
        Object.defineProperty(e, 'persisted', { value: persisted })
        window.dispatchEvent(e)
      })
    pageshow(false)
    expect(hrefWrites).toHaveLength(1)
    expect(sessionStorage.getItem('invoice-os.pendingVerify')).not.toBeNull()
    pageshow(true)
    expect(hrefWrites).toHaveLength(2)
    expect(hrefWrites[1]).toMatch(/^https:\/\/landing\.example\/\?state=/)
    expect(sessionStorage.getItem('invoice-os.pendingVerify')).toBeNull()
    pageshow(true)
    expect(hrefWrites).toHaveLength(2)
  })

  it('App adversarial: a URL state beside auth=verify is never adopted', () => {
    configure()
    const hrefWrites = boot(`/?auth=verify&state=${X}#token=${T}`)
    const s = storedState()
    expect(s).not.toBe(X)
    expect(hrefWrites).toEqual([`${GATEWAY}/auth/verify?token=${T}&type=signup&state=${s}`])
  })
})
