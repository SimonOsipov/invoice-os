// @vitest-environment jsdom
// Adversarial coverage for lib/signInState.ts.
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { consumeSignInState, ensureSignInState, landingSignInUrl } from './signInState'

const KEY = 'invoice-os.signInState'
const TTL = 600_000
const STATE_RE = /^[A-Za-z0-9_-]{43}$/
const S = 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE'

function createMemoryStorage(opts: { throwOnGet?: boolean; throwOnSet?: boolean; throwOnRemove?: boolean } = {}) {
  const store = new Map<string, string>()
  return {
    store,
    getItem: vi.fn((key: string) => {
      if (opts.throwOnGet) throw new Error('SecurityError')
      return store.has(key) ? (store.get(key) as string) : null
    }),
    setItem: vi.fn((key: string, value: string) => {
      if (opts.throwOnSet) throw new Error('QuotaExceededError')
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      if (opts.throwOnRemove) throw new Error('SecurityError')
      store.delete(key)
    }),
    clear: vi.fn(() => store.clear()),
  }
}

let storage: ReturnType<typeof createMemoryStorage>

beforeEach(() => {
  storage = createMemoryStorage()
  vi.stubGlobal('sessionStorage', storage)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

function blob(s: unknown, at: unknown, v: unknown = 1): string {
  return JSON.stringify({ v, s, at })
}

// Fills every getRandomValues buffer with one byte value.
function fixedBytes(byte: number) {
  return vi.spyOn(crypto, 'getRandomValues').mockImplementation(<T extends ArrayBufferView | null>(a: T): T => {
    ;(a as unknown as Uint8Array).fill(byte)
    return a
  })
}

describe('signInState adversarial: blob shape', () => {
  // A blob consume must refuse, keyed by why; each is also re-minted over by ensure.
  const refused: Array<[string, string]> = [
    ['s with +', blob(`${S.slice(0, 41)}+A`, 1000)],
    ['s with /', blob(`${S.slice(0, 41)}/A`, 1000)],
    ['s of 44 chars', blob(`${S}A`, 1000)],
    ['s with a leading bad char', blob(`!${S}`, 1000)],
    ['s with a trailing bad char', blob(`${S}!`, 1000)],
    ['s with a trailing newline', blob(`${S}\n`, 1000)],
    ['s as an array', blob([S], 1000)],
    ['at as a string', blob(S, '1000')],
    ['at missing', JSON.stringify({ v: 1, s: S })],
    ['v as a string', blob(S, 1000, '1')],
    ['JSON null', 'null'],
    ['JSON number', '5'],
  ]

  it('signInState adversarial: consume refuses every malformed blob and removes it', () => {
    expect(refused.length).toBeGreaterThan(0)
    for (const [label, raw] of refused) {
      storage.store.set(KEY, raw)
      expect(consumeSignInState(1001), label).toBeNull()
      expect(storage.store.has(KEY), `${label}: removed`).toBe(false)
    }
    // Control: the same clock accepts a well-formed blob.
    storage.store.set(KEY, blob(S, 1000))
    expect(consumeSignInState(1001)).toBe(S)
  })

  it('signInState adversarial: ensure replaces every malformed blob with a fresh stored one', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    for (const [label, raw] of refused) {
      storage.store.set(KEY, raw)
      const s = ensureSignInState(1001)
      expect(s, label).toEqual(expect.stringMatching(STATE_RE))
      expect(s, label).not.toBe(S)
      expect(JSON.parse(storage.store.get(KEY) ?? 'null'), `${label}: overwritten`).toEqual({ v: 1, s, at: 1001 })
    }
    // Pins executor choice 3: a malformed blob is replaced silently.
    expect(warn).not.toHaveBeenCalled()
  })

  it('signInState adversarial: a non-JSON blob is overwritten without a warning', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    storage.store.set(KEY, '{not json')
    const s = ensureSignInState(1000)
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(JSON.parse(storage.store.get(KEY) ?? 'null')).toEqual({ v: 1, s, at: 1000 })
    expect(warn).not.toHaveBeenCalled()
  })

  it('signInState adversarial: a blob stamped now is live', () => {
    storage.store.set(KEY, blob(S, 5000))
    expect(ensureSignInState(5000)).toBe(S)
    expect(consumeSignInState(5000)).toBe(S)
  })

  // DEFECT (executor choice 2): the module is meant to mirror lib/deepLink.ts, which
  // refuses a future timestamp; the live-session ceiling relies on a 10-minute life. A future `at`
  // (clock moved back, or a tampered blob) keeps the state live past that life.
  it('signInState adversarial: a future-dated blob is not live', () => {
    const now = 1_000_000
    storage.store.set(KEY, blob(S, now + 1))
    expect(consumeSignInState(now), 'consume refuses a future stamp').toBeNull()
    expect(storage.store.has(KEY)).toBe(false)

    storage.store.set(KEY, blob(S, now + 24 * 60 * 60 * 1000))
    const s = ensureSignInState(now)
    expect(s, 'ensure re-mints over a future stamp').not.toBe(S)
    expect(s).toEqual(expect.stringMatching(STATE_RE))
  })
})

describe('signInState adversarial: TTL', () => {
  it('signInState adversarial: consume after ensure after the TTL gives null', () => {
    const t0 = 2_000_000
    const s = ensureSignInState(t0)
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(consumeSignInState(t0 + TTL)).toBeNull()
    expect(storage.store.has(KEY), 'an expired blob is removed').toBe(false)
  })

  it('signInState adversarial: consume one ms under the TTL gives the state', () => {
    const t0 = 2_000_000
    const s = ensureSignInState(t0)
    expect(consumeSignInState(t0 + TTL - 1)).toBe(s)
  })
})

describe('signInState adversarial: storage failures', () => {
  it('signInState adversarial: ensure survives a throwing getItem', () => {
    const throwing = createMemoryStorage({ throwOnGet: true })
    vi.stubGlobal('sessionStorage', throwing)
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    let s: string | undefined
    expect(() => {
      s = ensureSignInState(1000)
    }).not.toThrow()
    expect(throwing.getItem).toHaveBeenCalled()
    expect(s).toEqual(expect.stringMatching(STATE_RE))
    expect(warn).toHaveBeenCalledTimes(1)
  })

  it('signInState adversarial: consume survives a throwing getItem', () => {
    const throwing = createMemoryStorage({ throwOnGet: true })
    vi.stubGlobal('sessionStorage', throwing)
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    let r: string | null | undefined
    expect(() => {
      r = consumeSignInState(1000)
    }).not.toThrow()
    expect(throwing.getItem).toHaveBeenCalled()
    expect(r).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  it('signInState adversarial: consume with a throwing removeItem fails closed', () => {
    const throwing = createMemoryStorage({ throwOnRemove: true })
    throwing.store.set(KEY, blob(S, 1000))
    vi.stubGlobal('sessionStorage', throwing)
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(consumeSignInState(1001)).toBeNull()
    expect(throwing.removeItem).toHaveBeenCalled()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  it('signInState adversarial: a live consume warns nothing', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    storage.store.set(KEY, blob(S, 1000))
    expect(consumeSignInState(1001)).toBe(S)
    expect(warn).not.toHaveBeenCalled()
  })
})

describe('signInState adversarial: mint', () => {
  it('signInState adversarial: getRandomValues is called with 32 bytes', () => {
    const spy = vi.spyOn(crypto, 'getRandomValues')
    ensureSignInState(1000)
    expect(spy).toHaveBeenCalledTimes(1)
    const arg = spy.mock.calls[0][0] as Uint8Array
    expect(arg).toBeInstanceOf(Uint8Array)
    expect(arg.length).toBe(32)
  })

  it('signInState adversarial: known bytes encode as unpadded base64url', () => {
    fixedBytes(0x00)
    expect(ensureSignInState(1000)).toBe('A'.repeat(43))
    storage.store.clear()
    fixedBytes(0xff)
    expect(ensureSignInState(1000), '/ becomes _').toBe(`${'_'.repeat(42)}8`)
    storage.store.clear()
    fixedBytes(0xfb)
    expect(ensureSignInState(1000), '+ becomes -').toBe(`${'-_v7'.repeat(10)}-_s`)
  })

  it('signInState adversarial: many mints stay in the base64url alphabet and never repeat', () => {
    const seen = new Set<string>()
    const chars = new Set<string>()
    for (let i = 0; i < 300; i++) {
      storage.store.clear()
      const s = ensureSignInState(1000)
      expect(s).toEqual(expect.stringMatching(STATE_RE))
      seen.add(s)
      for (const c of s) chars.add(c)
    }
    expect(seen.size).toBe(300)
    expect(chars.has('-'), 'a - appears across 300 mints').toBe(true)
    expect(chars.has('_'), 'a _ appears across 300 mints').toBe(true)
  })
})

describe('signInState adversarial: URL', () => {
  it('signInState adversarial: a trailing-slash landing URL builds one slash', () => {
    vi.stubEnv('VITE_LANDING_URL', 'https://landing.example/')
    expect(landingSignInUrl(S, 'failed')).toBe(`https://landing.example/?state=${S}&signin=failed`)
  })

  it('signInState adversarial: the module never reads window.location', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/lib/signInState.ts'), 'utf8')
    expect(src, 'sanity: the source was read').toContain('sessionStorage')
    const code = src.replace(/\/\/.*$/gm, '')
    expect(code).not.toMatch(/\blocation\b/)
    expect(code).not.toMatch(/\bURLSearchParams\b/)
  })
})
