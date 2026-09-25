// @vitest-environment jsdom
// D25 steps 2-3 in the app: the front door and the `?auth=start` bounce carry the stored state.

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
