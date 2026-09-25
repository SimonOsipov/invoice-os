// @vitest-environment jsdom
// jsdom gives a real window.location to plant a URL state in; storage is a memory stub
// because CI's Node 22 has no sessionStorage global.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { consumeSignInState, ensureSignInState, landingSignInUrl } from './signInState'

const KEY = 'invoice-os.signInState'
const TTL = 600_000
const STATE_RE = /^[A-Za-z0-9_-]{43}$/
const S = 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE'
const X = 'XxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxX'

function createMemoryStorage(opts: { throwOnSet?: boolean } = {}) {
  const store = new Map<string, string>()
  return {
    store,
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      if (opts.throwOnSet) throw new Error('QuotaExceededError')
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

let storage: ReturnType<typeof createMemoryStorage>

beforeEach(() => {
  storage = createMemoryStorage()
  vi.stubGlobal('sessionStorage', storage)
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
  window.history.replaceState(null, '', '/')
})

describe('ensureSignInState', () => {
  it('ensureSignInState mints a 43-character base64url state and stores it', () => {
    const s = ensureSignInState(1000)
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(JSON.parse(storage.store.get(KEY) ?? 'null')).toEqual({ v: 1, s, at: 1000 })
  })

  it('ensureSignInState reuses a fresh state', () => {
    const spy = vi.spyOn(crypto, 'getRandomValues')
    const a = ensureSignInState(1000)
    const b = ensureSignInState(1001)
    expect(b).toBe(a)
    expect(spy, 'a fresh stored state must not be re-minted').toHaveBeenCalledTimes(1)
    expect(a).toEqual(expect.stringMatching(STATE_RE))
  })

  it('ensureSignInState re-mints at the TTL boundary', () => {
    const t0 = 1_000_000
    const first = ensureSignInState(t0)
    expect(first).toEqual(expect.stringMatching(STATE_RE))
    expect(ensureSignInState(t0 + TTL - 1), 'one ms under the TTL reuses').toBe(first)
    const next = ensureSignInState(t0 + TTL)
    expect(next, 'at the TTL a new state is minted').not.toBe(first)
    expect(next).toEqual(expect.stringMatching(STATE_RE))
    expect(JSON.parse(storage.store.get(KEY) ?? 'null')).toEqual({ v: 1, s: next, at: t0 + TTL })
  })

  it('a URL state is never adopted', () => {
    window.history.replaceState(null, '', `/?state=${X}`)
    expect(window.location.search, 'sanity: the URL carries the planted state').toBe(`?state=${X}`)
    const s = ensureSignInState(1000)
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(s).not.toBe(X)
    expect(JSON.parse(storage.store.get(KEY) ?? 'null')).toEqual({ v: 1, s, at: 1000 })
  })

  it('a throwing storage still yields a state', () => {
    const throwing = createMemoryStorage({ throwOnSet: true })
    vi.stubGlobal('sessionStorage', throwing)
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    let s: string | undefined
    expect(() => {
      s = ensureSignInState(1000)
    }).not.toThrow()
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(throwing.setItem, 'the write must have been attempted').toHaveBeenCalled()
    expect(warn).toHaveBeenCalledTimes(1)
    expect(throwing.store.size).toBe(0)
  })
})

describe('consumeSignInState', () => {
  it('consumeSignInState returns the state once', () => {
    storage.store.set(KEY, JSON.stringify({ v: 1, s: S, at: 1000 }))
    expect(consumeSignInState(1001)).toBe(S)
    expect(storage.store.has(KEY), 'consume removes the key').toBe(false)
    expect(consumeSignInState(1002)).toBeNull()

    const minted = ensureSignInState(2000)
    expect(minted).toEqual(expect.stringMatching(STATE_RE))
    expect(consumeSignInState(2001)).toBe(minted)
    expect(consumeSignInState(2002)).toBeNull()
  })

  it('consumeSignInState refuses bad blobs', () => {
    const bad: Array<[string, string]> = [
      ['expired', JSON.stringify({ v: 1, s: S, at: 1000 })],
      ['not json', 'not json'],
      ['wrong version', JSON.stringify({ v: 2, s: S, at: 1000 + TTL })],
      ['42-char s', JSON.stringify({ v: 1, s: S.slice(0, 42), at: 1000 + TTL })],
    ]
    expect(bad.length).toBeGreaterThan(0)
    for (const [label, raw] of bad) {
      storage.store.set(KEY, raw)
      expect(consumeSignInState(1000 + TTL), label).toBeNull()
      expect(storage.store.has(KEY), `${label}: the key must be removed`).toBe(false)
    }
    // Control: the same clock accepts a live blob.
    storage.store.set(KEY, JSON.stringify({ v: 1, s: S, at: 1000 + TTL - 1 }))
    expect(consumeSignInState(1000 + TTL)).toBe(S)
  })
})

describe('landingSignInUrl', () => {
  it('landingSignInUrl builds each form', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example')
    expect(landingSignInUrl(S)).toBe(`https://landing.example/?state=${S}`)
    expect(landingSignInUrl(S, 'ready')).toBe(`https://landing.example/?state=${S}&signin=ready`)
    expect(landingSignInUrl(S, 'failed')).toBe(`https://landing.example/?state=${S}&signin=failed`)
    expect(landingSignInUrl(S, 'no-workspace')).toBe(`https://landing.example/?state=${S}&signin=no-workspace`)
    vi.stubEnv('VITE_LANDING_URL', '')
    expect(landingSignInUrl(S)).toBeNull()
    expect(landingSignInUrl(S, 'ready')).toBeNull()
  })
})
