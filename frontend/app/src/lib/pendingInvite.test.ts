// @vitest-environment jsdom
// Storage is a memory stub because CI's Node 22 has no sessionStorage global.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import * as pendingInvite from './pendingInvite'
import { consumePendingInvite, holdPendingInvite, peekPendingInvite, readInviteFragment } from './pendingInvite'

// Read off the namespace so a missing export fails the assertion, not the import.
const KEY = (pendingInvite as Record<string, unknown>).PENDING_INVITE_KEY
const RAW_KEY = 'invoice-os.pendingInvite'
const TTL = 600_000
const T = 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE'
const T2 = 'XxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxXxX'
const NOW = 1_800_000_000_000

function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    store,
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

let storage: ReturnType<typeof createMemoryStorage>

beforeEach(() => {
  storage = createMemoryStorage()
  vi.stubGlobal('sessionStorage', storage)
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('readInviteFragment (D11)', () => {
  it('readInviteFragment_acceptsOnlyOneWellFormedInvite', () => {
    expect(T).toHaveLength(43)
    const rows: [string, string, string | null][] = [
      ['one 43-character token', `#invite=${T}`, T],
      ['the full base64url alphabet', `#invite=${'_-'.repeat(21)}A`, `${'_-'.repeat(21)}A`],
      ['no hash', '', null],
      ['a bare #', '#', null],
      ['an empty token', '#invite=', null],
      ['42 characters', `#invite=${T.slice(1)}`, null],
      ['44 characters', `#invite=${T}A`, null],
      ['padding makes it 44', `#invite=${T}=`, null],
      ['a +', `#invite=${T.slice(1)}+`, null],
      ['a /', `#invite=${T.slice(1)}/`, null],
      ['a repeated invite, same token', `#invite=${T}&invite=${T}`, null],
      ['a repeated invite, two tokens', `#invite=${T}&invite=${T2}`, null],
      ['the wrong key', `#token=${T}`, null],
    ]
    for (const [name, hash, want] of rows) {
      expect(readInviteFragment(hash), name).toBe(want)
    }
  })
})

describe('holdPendingInvite and consumePendingInvite (D11)', () => {
  it('pendingInvite_isOneShotAndLivesTenMinutes', () => {
    expect(KEY).toBe(RAW_KEY)
    holdPendingInvite(T, NOW)
    expect(JSON.parse(storage.store.get(RAW_KEY) ?? 'null')).toEqual({ v: 1, t: T, at: NOW })
    expect(peekPendingInvite(NOW + TTL - 1), 'peek reads a live token').toBe(T)
    expect(storage.store.has(RAW_KEY), 'peek leaves the key').toBe(true)
    expect(peekPendingInvite(NOW + TTL), 'peek at exactly ten minutes').toBeNull()
    expect(storage.store.has(RAW_KEY), 'peek leaves an expired key too').toBe(true)
    expect(consumePendingInvite(NOW + TTL - 1000)).toBe(T)
    expect(storage.store.has(RAW_KEY), 'consuming removes the key').toBe(false)
    expect(consumePendingInvite(NOW + TTL - 1000), 'the second consume').toBeNull()

    holdPendingInvite(T, NOW)
    expect(consumePendingInvite(NOW + TTL), 'exactly ten minutes old').toBeNull()
    expect(storage.store.has(RAW_KEY), 'an expired blob is removed too').toBe(false)

    holdPendingInvite(T, NOW)
    expect(consumePendingInvite(NOW + TTL - 1), 'one millisecond inside the TTL').toBe(T)

    holdPendingInvite(T, NOW)
    holdPendingInvite(T2, NOW + 1)
    expect(consumePendingInvite(NOW + 2), 'a second hold replaces the first').toBe(T2)
  })

  it('pendingInvite_malformedOrThrowingStorageReadsNull', () => {
    holdPendingInvite(T, NOW)
    expect(consumePendingInvite(NOW), 'control: a well-formed blob reads').toBe(T)

    const rows: [string, string][] = [
      ['wrong version', JSON.stringify({ v: 2, t: T, at: NOW })],
      ['missing version', JSON.stringify({ t: T, at: NOW })],
      ['a token that is not 43 base64url characters', JSON.stringify({ v: 1, t: 'x', at: NOW })],
      ['a numeric token', JSON.stringify({ v: 1, t: 7, at: NOW })],
      ['a non-numeric at', JSON.stringify({ v: 1, t: T, at: 'x' })],
      ['a missing at', JSON.stringify({ v: 1, t: T })],
      ['a future at', JSON.stringify({ v: 1, t: T, at: NOW + 1 })],
      ['not json', 'not json'],
      ['json null', 'null'],
    ]
    for (const [name, raw] of rows) {
      storage.store.set(RAW_KEY, raw)
      expect(peekPendingInvite(NOW), `peek: ${name}`).toBeNull()
      expect(consumePendingInvite(NOW), name).toBeNull()
    }

    holdPendingInvite(T, NOW)
    holdPendingInvite(null, NOW)
    expect(storage.store.has(RAW_KEY), 'holding null removes the key').toBe(false)
    expect(consumePendingInvite(NOW), 'holding null removes a held token').toBeNull()

    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    storage.getItem.mockImplementation(() => {
      throw new Error('SecurityError')
    })
    expect(peekPendingInvite(NOW), 'peek: getItem throws').toBeNull()
    expect(warn).toHaveBeenCalledTimes(1)
    expect(consumePendingInvite(NOW), 'getItem throws').toBeNull()
    expect(warn).toHaveBeenCalledTimes(2)

    storage.setItem.mockImplementation(() => {
      throw new Error('QuotaExceededError')
    })
    expect(() => holdPendingInvite(T, NOW), 'setItem throws').not.toThrow()
    expect(warn).toHaveBeenCalledTimes(3)
  })
})
