// Specs for the deepLink.ts storage contract. Mirrors session.test.ts: spyOnConsole for
// the warn-never-error invariant, explicit `now` per call (this repo's convention over
// vi.useFakeTimers).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  DEEP_LINK_KEY,
  DEEP_LINK_TTL_MS,
  captureDestination,
  clearDestination,
  readDestination,
} from './deepLink'

beforeEach(() => {
  sessionStorage.clear()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function spyOnConsole() {
  return {
    warn: vi.spyOn(console, 'warn').mockImplementation(() => {}),
    error: vi.spyOn(console, 'error').mockImplementation(() => {}),
  }
}

describe('captureDestination', () => {
  it('capture_writesTheVersionedBlob', () => {
    captureDestination('/audit', 1000)

    expect(JSON.parse(sessionStorage.getItem(DEEP_LINK_KEY) as string)).toEqual({
      v: 1,
      path: '/audit',
      at: 1000,
    })
  })

  it('capture_refusesTheBareRoot', () => {
    captureDestination('/', 1000)

    expect(sessionStorage.getItem(DEEP_LINK_KEY)).toBeNull()
  })

  it('capture_refusesANonPathValue', () => {
    captureDestination('https://evil.test/x', 1000)

    expect(sessionStorage.getItem(DEEP_LINK_KEY)).toBeNull()
  })

  it('capture_refusesAProtocolRelativeValue', () => {
    captureDestination('//evil.test/x', 1000)

    expect(sessionStorage.getItem(DEEP_LINK_KEY)).toBeNull()
  })

  it('capture_theLatestWins', () => {
    captureDestination('/audit', 1000)
    captureDestination('/settings', 2000)

    expect(readDestination(2000)).toBe('/settings')
  })

  // No length cap in the design or ACs — this pins that a defensively-added truncation
  // limit would be a regression, not a feature.
  it('capture_acceptsAVeryLongPathname', () => {
    const longPath = '/' + 'a'.repeat(10_000)
    captureDestination(longPath, 1000)

    expect(readDestination(1000)).toBe(longPath)
  })

  // The stored path is app-relative only (isCapturablePath), never off-origin, so a query
  // string carries no auth/identity risk here — preserved as-is for the caller to navigate to.
  it('capture_preservesAQueryString', () => {
    captureDestination('/audit?invoiceId=42', 1000)

    expect(readDestination(1000)).toBe('/audit?invoiceId=42')
  })
})

describe('readDestination', () => {
  it('read_returnsAFreshDestinationWithoutRemovingIt', () => {
    captureDestination('/audit', 1000)

    expect(readDestination(2000)).toBe('/audit')
    expect(readDestination(2000)).toBe('/audit')
    expect(sessionStorage.getItem(DEEP_LINK_KEY)).not.toBeNull()
  })

  it('read_absentIsSilent', () => {
    const { warn } = spyOnConsole()

    expect(readDestination(1000)).toBeNull()
    expect(warn).not.toHaveBeenCalled()
  })

  it('read_expiredIsSilent', () => {
    const { warn } = spyOnConsole()
    captureDestination('/audit', 1000)

    expect(readDestination(1000 + DEEP_LINK_TTL_MS + 1)).toBeNull()
    expect(warn).not.toHaveBeenCalled()
  })

  it('read_theBoundaryIsInclusive', () => {
    captureDestination('/audit', 1000)

    expect(readDestination(1000 + DEEP_LINK_TTL_MS)).toBe('/audit')
  })

  it('read_aFutureTimestampIsRejected', () => {
    const { warn, error } = spyOnConsole()
    const now = 1_000_000
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 1, path: '/audit', at: now + 60_000 }))

    expect(readDestination(now)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
    expect(error).not.toHaveBeenCalled()
  })

  it('read_corruptJsonWarnsOnceAndReturnsNull', () => {
    const { warn, error } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, '{')

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
    expect(error).not.toHaveBeenCalled()
  })

  it('read_wrongSchemaVersionIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 2, path: '/audit', at: 1000 }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  it('read_aNonPathStoredValueIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 1, path: '//evil.test', at: 1000 }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  it('read_aNonNumericTimestampIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 1, path: '/audit', at: 'soon' }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  // AC-6 lists a wrong-typed `path` alongside bad-shape `path` as a distinct corruption case.
  it('read_aNonStringPathValueIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 1, path: 42, at: 1000 }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  // `at: 0` is a legitimate epoch timestamp, not "missing" — guards against a falsy-check
  // bug (`!parsed.at`) that would reject it.
  it('read_aTimestampOfZeroIsValid', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 1, path: '/audit', at: 0 }))

    expect(readDestination(0)).toBe('/audit')
    expect(warn).not.toHaveBeenCalled()
  })

  // The `at === now` boundary sits between "valid" and "future-rejected" and isn't hit by
  // either read_theBoundaryIsInclusive (TTL edge) or read_aFutureTimestampIsRejected (well past it).
  it('read_isValidAtTheExactCaptureInstant', () => {
    captureDestination('/audit', 5000)

    expect(readDestination(5000)).toBe('/audit')
  })
})

describe('clearDestination', () => {
  it('clear_removesTheKey', () => {
    captureDestination('/audit', 1000)

    clearDestination()

    expect(readDestination(1000)).toBeNull()
  })
})

describe('storage failure degrades to a warn', () => {
  it('storage_aThrowingSessionStorageDegradesToAWarn', () => {
    const { warn, error } = spyOnConsole()
    vi.stubGlobal('sessionStorage', {
      getItem: vi.fn(() => {
        throw new Error('getItem boom')
      }),
      setItem: vi.fn(() => {
        throw new Error('setItem boom')
      }),
      removeItem: vi.fn(() => {
        throw new Error('removeItem boom')
      }),
    })

    expect(() => captureDestination('/audit', 1000)).not.toThrow()
    expect(() => readDestination(1000)).not.toThrow()
    expect(() => clearDestination()).not.toThrow()

    expect(warn).toHaveBeenCalledTimes(3)
    expect(error).not.toHaveBeenCalled()
  })
})
