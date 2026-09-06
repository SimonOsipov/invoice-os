// @vitest-environment jsdom
// jsdom for a real Storage: these specs assert clear(), null-for-absent and JSON
// round-trip, which a hand-rolled stub would re-implement and can diverge from.
// The `node` default only has sessionStorage on Node >= 24; CI pins Node 22.
// Specs for the deepLink.ts storage contract. Mirrors session.test.ts: spyOnConsole for
// the warn-never-error invariant, explicit `now` per call (this repo's convention over
// vi.useFakeTimers).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  DEEP_LINK_KEY,
  DEEP_LINK_SCHEMA_VERSION,
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
    captureDestination('/audit', '', 1000)

    expect(JSON.parse(sessionStorage.getItem(DEEP_LINK_KEY) as string)).toEqual({
      v: 2,
      path: '/audit',
      query: '',
      at: 1000,
    })
  })

  // isCapturablePath has no export; refusal is observed through captureDestination's own
  // signature, now three args instead of two.
  it('capture_refusesTheBareRoot', () => {
    captureDestination('/', '', 1000)

    expect(sessionStorage.getItem(DEEP_LINK_KEY)).toBeNull()
  })

  it('capture_refusesANonPathValue', () => {
    captureDestination('https://evil.test/x', '', 1000)

    expect(sessionStorage.getItem(DEEP_LINK_KEY)).toBeNull()
  })

  it('capture_refusesAProtocolRelativeValue', () => {
    captureDestination('//evil.test/x', '', 1000)

    expect(sessionStorage.getItem(DEEP_LINK_KEY)).toBeNull()
  })

  it('capture_theLatestWins', () => {
    captureDestination('/audit', '', 1000)
    captureDestination('/settings', '', 2000)

    expect(readDestination(2000)).toEqual({ path: '/settings', query: '' })
  })

  // No length cap in the design or ACs — this pins that a defensively-added truncation
  // limit would be a regression, not a feature.
  it('capture_acceptsAVeryLongPathname', () => {
    const longPath = '/' + 'a'.repeat(10_000)
    captureDestination(longPath, '', 1000)

    expect(readDestination(1000)).toEqual({ path: longPath, query: '' })
  })

  // Query is now its own argument, not embedded in pathname — the codec's caller owns
  // splitting path from query, same split routeQuery uses to write it.
  it('capture_preservesAQueryString', () => {
    captureDestination('/audit', '?invoiceId=42', 1000)

    expect(readDestination(1000)).toEqual({ path: '/audit', query: '?invoiceId=42' })
  })
})

describe('readDestination', () => {
  it('read_returnsAFreshDestinationWithoutRemovingIt', () => {
    captureDestination('/audit', '', 1000)

    expect(readDestination(2000)).toEqual({ path: '/audit', query: '' })
    expect(readDestination(2000)).toEqual({ path: '/audit', query: '' })
    expect(sessionStorage.getItem(DEEP_LINK_KEY)).not.toBeNull()
  })

  it('read_absentIsSilent', () => {
    const { warn } = spyOnConsole()

    expect(readDestination(1000)).toBeNull()
    expect(warn).not.toHaveBeenCalled()
  })

  it('read_expiredIsSilent', () => {
    const { warn } = spyOnConsole()
    captureDestination('/audit', '', 1000)

    expect(readDestination(1000 + DEEP_LINK_TTL_MS + 1)).toBeNull()
    expect(warn).not.toHaveBeenCalled()
  })

  it('read_theBoundaryIsInclusive', () => {
    captureDestination('/audit', '', 1000)

    expect(readDestination(1000 + DEEP_LINK_TTL_MS)).toEqual({ path: '/audit', query: '' })
  })

  // v bumped to the CURRENT version and `query` added: isolates this spec to the
  // future-timestamp clause alone, not a version or shape mismatch.
  it('read_aFutureTimestampIsRejected', () => {
    const { warn, error } = spyOnConsole()
    const now = 1_000_000
    sessionStorage.setItem(
      DEEP_LINK_KEY,
      JSON.stringify({ v: 2, path: '/audit', query: '', at: now + 60_000 }),
    )

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

  // Rejected ONLY by the version clause: v:3 is never a version this codec produced, and
  // path/query/at are otherwise well-formed.
  it('read_wrongSchemaVersionIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 3, path: '/audit', query: '', at: 1000 }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  // The exact shape left in a real user's sessionStorage across THIS deploy: the pre-bump
  // codec (v: 1) never wrote `query`. Strict rejection is the point -- a lenient reader
  // ("accept v1 too") would feed a query-less blob into the restore path.
  it('read_aPreDeployV1BlobIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 1, path: '/audit', at: 1000 }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  // Exact equality against ONE constant, not `>=`: one version below and one above current
  // are both rejected, only current round-trips. Argues the cross-build claim from the
  // predicate's shape -- no test here actually runs the old build.
  it('readDestination_acceptsOnlyTheCurrentVersion', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(
      DEEP_LINK_KEY,
      JSON.stringify({ v: DEEP_LINK_SCHEMA_VERSION - 1, path: '/audit', query: '', at: 1000 }),
    )
    expect(readDestination(1000), 'one version below current must be rejected').toBeNull()

    sessionStorage.setItem(
      DEEP_LINK_KEY,
      JSON.stringify({ v: DEEP_LINK_SCHEMA_VERSION + 1, path: '/audit', query: '', at: 1000 }),
    )
    expect(readDestination(1000), 'one version above current must be rejected').toBeNull()

    captureDestination('/audit', '', 1000)
    expect(readDestination(1000), 'the current version round-trips').toEqual({ path: '/audit', query: '' })

    expect(warn, 'both rejections warn once each').toHaveBeenCalledTimes(2)
  })

  // Isolates the query-type gate from the version gate: v matches current and path is
  // valid, so only a missing `query` decides the outcome. read_aPreDeployV1BlobIsRejected
  // above writes the same query-less shape but is rejected by its `v: 1` mismatch first --
  // it cannot tell a strict query check from a lenient `?? ''` one. This can.
  it('read_aQueryLessBlobAtTheCurrentVersionIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: DEEP_LINK_SCHEMA_VERSION, path: '/audit', at: 1000 }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  it('read_aNonPathStoredValueIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 2, path: '//evil.test', query: '', at: 1000 }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  it('read_aNonNumericTimestampIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 2, path: '/audit', query: '', at: 'soon' }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  // AC-6 lists a wrong-typed `path` alongside bad-shape `path` as a distinct corruption case.
  it('read_aNonStringPathValueIsRejected', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 2, path: 42, query: '', at: 1000 }))

    expect(readDestination(1000)).toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
  })

  // `at: 0` is a legitimate epoch timestamp, not "missing" — guards against a falsy-check
  // bug (`!parsed.at`) that would reject it.
  it('read_aTimestampOfZeroIsValid', () => {
    const { warn } = spyOnConsole()
    sessionStorage.setItem(DEEP_LINK_KEY, JSON.stringify({ v: 2, path: '/audit', query: '', at: 0 }))

    expect(readDestination(0)).toEqual({ path: '/audit', query: '' })
    expect(warn).not.toHaveBeenCalled()
  })

  // The `at === now` boundary sits between "valid" and "future-rejected" and isn't hit by
  // either read_theBoundaryIsInclusive (TTL edge) or read_aFutureTimestampIsRejected (well past it).
  it('read_isValidAtTheExactCaptureInstant', () => {
    captureDestination('/audit', '', 5000)

    expect(readDestination(5000)).toEqual({ path: '/audit', query: '' })
  })
})

describe('clearDestination', () => {
  it('clear_removesTheKey', () => {
    captureDestination('/audit', '', 1000)

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

    expect(() => captureDestination('/audit', '', 1000)).not.toThrow()
    expect(() => readDestination(1000)).not.toThrow()
    expect(() => clearDestination()).not.toThrow()

    expect(warn).toHaveBeenCalledTimes(3)
    expect(error).not.toHaveBeenCalled()
  })
})
