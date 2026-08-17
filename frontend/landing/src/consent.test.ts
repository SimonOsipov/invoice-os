// Specs for the versioned consent gate: acceptance-criteria coverage plus
// adversarial/edge cases (malformed input, hostile stores, non-vacuity controls).
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  analyticsAllowed,
  CONSENT_DEFAULT_ANALYTICS,
  CONSENT_STORAGE_KEY,
  CONSENT_VERSION,
  parseConsent,
  readConsent,
  writeConsent,
  type ConsentRecord,
  type ConsentStore,
} from './consent'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function spyOnConsole() {
  return {
    error: vi.spyOn(console, 'error').mockImplementation(() => undefined),
    warn: vi.spyOn(console, 'warn').mockImplementation(() => undefined),
    log: vi.spyOn(console, 'log').mockImplementation(() => undefined),
    info: vi.spyOn(console, 'info').mockImplementation(() => undefined),
  }
}

function expectNoConsoleCalls(spies: ReturnType<typeof spyOnConsole>) {
  expect(spies.error).not.toHaveBeenCalled()
  expect(spies.warn).not.toHaveBeenCalled()
  expect(spies.log).not.toHaveBeenCalled()
  expect(spies.info).not.toHaveBeenCalled()
}

/** Minimal Map-backed ConsentStore fake. */
function createStore(seed: Record<string, string> = {}): ConsentStore {
  const data = new Map<string, string>(Object.entries(seed))
  return {
    getItem: (key: string) => data.get(key) ?? null,
    setItem: (key: string, value: string) => {
      data.set(key, value)
    },
  }
}

const FIXED_DATE = new Date('2026-01-01T00:00:00.000Z')

// Control for every expectNoConsoleCalls() assertion below: proves the spies
// actually observe a call, so "not called" elsewhere means silence, not a broken spy.
it('the console spies detect a call (non-vacuity control)', () => {
  const spies = spyOnConsole()
  console.error('x')
  console.warn('x')
  console.log('x')
  console.info('x')
  expect(spies.error).toHaveBeenCalledTimes(1)
  expect(spies.warn).toHaveBeenCalledTimes(1)
  expect(spies.log).toHaveBeenCalledTimes(1)
  expect(spies.info).toHaveBeenCalledTimes(1)
})

describe('module constants', () => {
  it('CONSENT_STORAGE_KEY and CONSENT_VERSION match the LAND-05 storage contract', () => {
    expect(CONSENT_STORAGE_KEY).toBe('asc_consent')
    expect(CONSENT_VERSION).toBe(1)
  })
})

describe('parseConsent', () => {
  it('accepts a well-formed record and rebuilds it rather than returning the parsed object', () => {
    const raw = JSON.stringify({ analytics: true, ts: FIXED_DATE.toISOString(), v: 1, extra: 'unknown-field' })
    expect(parseConsent(raw)).toEqual({ analytics: true, ts: FIXED_DATE.toISOString(), v: 1 })
  })

  it('drops unknown extra keys instead of leaking them through', () => {
    const raw = JSON.stringify({ analytics: true, ts: FIXED_DATE.toISOString(), v: 1, admin: true, nested: { a: 1 } })
    const result = parseConsent(raw)
    expect(result).not.toBeNull()
    expect(Object.keys(result as ConsentRecord).sort()).toEqual(['analytics', 'ts', 'v'])
  })

  it('rejects null and the empty string', () => {
    expect(parseConsent(null)).toBeNull()
    expect(parseConsent('')).toBeNull()
  })

  it('rejects malformed JSON', () => {
    expect(parseConsent('{')).toBeNull()
  })

  it('rejects non-object JSON values, including JSON null', () => {
    for (const raw of ['null', '42', '"hello"', 'true']) {
      expect(parseConsent(raw), raw).toBeNull()
    }
  })

  it('rejects an array, via the version check rather than an object-shape check', () => {
    expect(parseConsent('[]')).toBeNull()
  })

  it('rejects a wrong or wrongly-typed version', () => {
    for (const v of [0, 2, '1', true]) {
      expect(parseConsent(JSON.stringify({ analytics: true, ts: FIXED_DATE.toISOString(), v })), JSON.stringify(v)).toBeNull()
    }
    expect(parseConsent(JSON.stringify({ analytics: true, ts: FIXED_DATE.toISOString() }))).toBeNull()
  })

  it('accepts v as the JSON float literal 1.0 — it is the same number as 1', () => {
    const raw = '{"analytics":true,"ts":"2026-01-01T00:00:00.000Z","v":1.0}'
    expect(parseConsent(raw)).toEqual({ analytics: true, ts: '2026-01-01T00:00:00.000Z', v: 1 })
  })

  it('rejects a non-boolean analytics value', () => {
    for (const analytics of ['true', 1, 0, null]) {
      expect(parseConsent(JSON.stringify({ analytics, ts: FIXED_DATE.toISOString(), v: 1 })), JSON.stringify(analytics)).toBeNull()
    }
    expect(parseConsent(JSON.stringify({ ts: FIXED_DATE.toISOString(), v: 1 }))).toBeNull()
  })

  it('normalizes a non-string or absent ts to the empty string, without rejecting the record', () => {
    for (const ts of [12345, null]) {
      expect(parseConsent(JSON.stringify({ analytics: true, ts, v: 1 }))).toEqual({ analytics: true, ts: '', v: 1 })
    }
    expect(parseConsent(JSON.stringify({ analytics: true, v: 1 }))).toEqual({ analytics: true, ts: '', v: 1 })
  })

  it('a non-string raw value is handled like any other unparsable input', () => {
    // Bypasses the `string | null` signature to simulate a hostile/untyped caller.
    expect(parseConsent(123 as unknown as string)).toBeNull()
  })
})

describe('analyticsAllowed / readConsent', () => {
  it('no stored record means analytics is denied', () => {
    const store = createStore()
    expect(analyticsAllowed(readConsent(store))).toBe(false)
  })

  it('a stored granted record is honoured', () => {
    const store = createStore({
      [CONSENT_STORAGE_KEY]: JSON.stringify({ analytics: true, ts: FIXED_DATE.toISOString(), v: CONSENT_VERSION }),
    })
    expect(analyticsAllowed(readConsent(store))).toBe(true)
  })

  it('a stored denied record is honoured over the default', () => {
    const store = createStore({
      [CONSENT_STORAGE_KEY]: JSON.stringify({ analytics: false, ts: FIXED_DATE.toISOString(), v: CONSENT_VERSION }),
    })
    expect(analyticsAllowed(readConsent(store))).toBe(false)
  })

  it('the default is a single literal, not a branch', () => {
    expect(analyticsAllowed(null)).toBe(CONSENT_DEFAULT_ANALYTICS)
  })

  // Pairs with the symbol comparison above: that one alone passes for either
  // value, so the literal itself is pinned here rather than inside it.
  it('the default literal is denied', () => {
    expect(CONSENT_DEFAULT_ANALYTICS).toBe(false)
  })

  it('a record from a superseded policy version is ignored', () => {
    const store = createStore({
      [CONSENT_STORAGE_KEY]: JSON.stringify({ analytics: false, ts: FIXED_DATE.toISOString(), v: 0 }),
    })
    expect(readConsent(store)).toBeNull()
    expect(analyticsAllowed(readConsent(store))).toBe(CONSENT_DEFAULT_ANALYTICS)
    // Value pin: the symbol comparison above re-prompts under either default.
    expect(analyticsAllowed(readConsent(store))).toBe(false)
  })

  // Non-vacuity control is the console-spy test at the top of this file.
  it('every branch that consults the default is silent', () => {
    const spies = spyOnConsole()

    const stores = [
      createStore(),
      createStore({
        [CONSENT_STORAGE_KEY]: JSON.stringify({ analytics: true, ts: FIXED_DATE.toISOString(), v: CONSENT_VERSION }),
      }),
      createStore({
        [CONSENT_STORAGE_KEY]: JSON.stringify({ analytics: false, ts: FIXED_DATE.toISOString(), v: CONSENT_VERSION }),
      }),
      createStore({
        [CONSENT_STORAGE_KEY]: JSON.stringify({ analytics: false, ts: FIXED_DATE.toISOString(), v: 0 }),
      }),
    ]
    // Floor on the population exercised: a shrunk list would make silence cheap.
    expect(stores.length).toBe(4)
    for (const store of stores) analyticsAllowed(readConsent(store))
    analyticsAllowed(null)

    expectNoConsoleCalls(spies)
  })

  it('malformed storage falls back to the default, silently', () => {
    const spies = spyOnConsole()
    for (const raw of ['{', 'null', '[]', '{"analytics":"yes","v":1}']) {
      const store = createStore({ [CONSENT_STORAGE_KEY]: raw })
      expect(readConsent(store), raw).toBeNull()
    }
    expectNoConsoleCalls(spies)
  })

  it('an absent localStorage is not an error', () => {
    vi.stubGlobal('localStorage', undefined)
    const spies = spyOnConsole()

    let result: ConsentRecord | null | undefined
    expect(() => {
      result = readConsent()
    }).not.toThrow()
    expect(result).toBeNull()
    expectNoConsoleCalls(spies)
  })

  it('a throwing localStorage is not an error', () => {
    const store: ConsentStore = {
      getItem: () => {
        throw new Error('storage disabled')
      },
      setItem: () => {},
    }
    const spies = spyOnConsole()

    let result: ConsentRecord | null | undefined
    expect(() => {
      result = readConsent(store)
    }).not.toThrow()
    expect(result).toBeNull()
    expectNoConsoleCalls(spies)
  })

  it('a store whose getItem returns a non-string value is not an error', () => {
    const store: ConsentStore = {
      // Bypasses the ConsentStore type to simulate a hostile/untyped caller.
      getItem: () => 12345 as unknown as string,
      setItem: () => {},
    }
    const spies = spyOnConsole()

    let result: ConsentRecord | null | undefined
    expect(() => {
      result = readConsent(store)
    }).not.toThrow()
    expect(result).toBeNull()
    expectNoConsoleCalls(spies)
  })

  it('an explicit null store means no storage — it must not fall back to a real global', () => {
    const backing = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => backing.get(key) ?? null,
      setItem: (key: string, value: string) => backing.set(key, value),
    })
    backing.set(CONSENT_STORAGE_KEY, JSON.stringify({ analytics: true, ts: FIXED_DATE.toISOString(), v: 1 }))

    expect(readConsent(null)).toBeNull()
  })
})

describe('writeConsent', () => {
  it('round-trips under the current version', () => {
    const store = createStore()
    writeConsent(false, store, FIXED_DATE)
    expect(readConsent(store)).toEqual({ analytics: false, ts: FIXED_DATE.toISOString(), v: CONSENT_VERSION })
  })

  it('a storage quota rejection on write is not an error', () => {
    const store: ConsentStore = {
      getItem: () => null,
      setItem: () => {
        const err = new Error('quota exceeded')
        err.name = 'QuotaExceededError'
        throw err
      },
    }
    const spies = spyOnConsole()

    let result: ConsentRecord | undefined
    expect(() => {
      result = writeConsent(true, store)
    }).not.toThrow()
    expect(result).toEqual({ analytics: true, ts: expect.any(String), v: CONSENT_VERSION })
    expectNoConsoleCalls(spies)
  })

  it('an absent localStorage is not an error on the write path either', () => {
    vi.stubGlobal('localStorage', undefined)
    const spies = spyOnConsole()

    let result: ConsentRecord | undefined
    expect(() => {
      result = writeConsent(false)
    }).not.toThrow()
    expect(result).toEqual({ analytics: false, ts: expect.any(String), v: CONSENT_VERSION })
    expectNoConsoleCalls(spies)
  })

  it('a present-but-unusable localStorage is not an error on either path', () => {
    vi.stubGlobal('localStorage', {
      getItem() {
        throw new Error('denied')
      },
      setItem() {
        throw new Error('denied')
      },
    })
    const spies = spyOnConsole()

    let readResult: ConsentRecord | null | undefined
    expect(() => {
      readResult = readConsent()
    }).not.toThrow()
    expect(readResult).toBeNull()

    let writeResult: ConsentRecord | undefined
    expect(() => {
      writeResult = writeConsent(true)
    }).not.toThrow()
    expect(writeResult).toEqual({ analytics: true, ts: expect.any(String), v: CONSENT_VERSION })

    expectNoConsoleCalls(spies)
  })

  it("the stored key and shape match LAND-05's spec verbatim", () => {
    const store = createStore()
    writeConsent(true, store, FIXED_DATE)
    const raw = store.getItem('asc_consent')
    expect(raw).not.toBeNull()
    const parsed = JSON.parse(raw as string)
    expect(Object.keys(parsed).sort()).toEqual(['analytics', 'ts', 'v'])
    expect(parsed).toEqual({ analytics: true, ts: FIXED_DATE.toISOString(), v: 1 })
  })

  it('a setItem that throws a non-Error value is not an error', () => {
    const store: ConsentStore = {
      getItem: () => null,
      setItem: () => {
        throw 'denied'
      },
    }
    const spies = spyOnConsole()

    let result: ConsentRecord | undefined
    expect(() => {
      result = writeConsent(true, store)
    }).not.toThrow()
    expect(result).toEqual({ analytics: true, ts: expect.any(String), v: CONSENT_VERSION })
    expectNoConsoleCalls(spies)
  })

  it('a later write on the same store overwrites the earlier one', () => {
    const store = createStore()
    writeConsent(true, store, FIXED_DATE)
    writeConsent(false, store, FIXED_DATE)
    expect(readConsent(store)).toEqual({ analytics: false, ts: FIXED_DATE.toISOString(), v: CONSENT_VERSION })
  })

  it('an explicit null store means no storage — it must not fall back to a real global', () => {
    const backing = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => backing.get(key) ?? null,
      setItem: (key: string, value: string) => backing.set(key, value),
    })

    const result = writeConsent(true, null, FIXED_DATE)
    expect(result).toEqual({ analytics: true, ts: FIXED_DATE.toISOString(), v: CONSENT_VERSION })
    expect(backing.size).toBe(0)
  })
})
