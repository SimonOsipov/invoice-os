// RED specs for the versioned consent gate (LAND-03-01). consent.ts is a stub that
// throws 'not implemented'; every case must fail on that throw or on an assertion,
// never on module resolution or a TypeScript error.
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  analyticsAllowed,
  CONSENT_DEFAULT_ANALYTICS,
  CONSENT_STORAGE_KEY,
  CONSENT_VERSION,
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

describe('analyticsAllowed / readConsent', () => {
  it('no stored record means analytics is allowed', () => {
    const store = createStore()
    expect(analyticsAllowed(readConsent(store))).toBe(true)
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

  it('a record from a superseded policy version is ignored', () => {
    const store = createStore({
      [CONSENT_STORAGE_KEY]: JSON.stringify({ analytics: false, ts: FIXED_DATE.toISOString(), v: 0 }),
    })
    expect(readConsent(store)).toBeNull()
    expect(analyticsAllowed(readConsent(store))).toBe(CONSENT_DEFAULT_ANALYTICS)
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
})
