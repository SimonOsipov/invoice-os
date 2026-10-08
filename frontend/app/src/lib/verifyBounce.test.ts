// @vitest-environment jsdom
// Storage is a memory stub because CI's Node 22 has no sessionStorage global.
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { mintSignInState } from './signInState'
import { consumePendingVerify, gatewayVerifyUrl, holdPendingVerify, peekPendingVerify, readVerifyFragment } from './verifyBounce'

const RAW_KEY = 'invoice-os.pendingVerify'
const TTL = 600_000
const NOW = 1_800_000_000_000

beforeEach(() => {
  const store = new Map<string, string>()
  vi.stubGlobal('sessionStorage', {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  })
})

describe('readVerifyFragment', () => {
  it('readVerifyFragment_acceptsOneSafeToken', () => {
    expect(readVerifyFragment('#token=abc')).toBe('abc')
    expect(readVerifyFragment(`#token=${'a'.repeat(256)}`)).toBe('a'.repeat(256))
    expect(readVerifyFragment('#token=a b')).toBeNull()
    expect(readVerifyFragment(`#token=${'a'.repeat(257)}`)).toBeNull()
    expect(readVerifyFragment('#token=a&token=b')).toBeNull()
    expect(readVerifyFragment('#token=')).toBeNull()
    expect(readVerifyFragment('')).toBeNull()
  })
})

describe('gatewayVerifyUrl', () => {
  it('gatewayVerifyUrl_buildsTheConfirmPageUrl', () => {
    const state = 'S'.repeat(43)
    expect(gatewayVerifyUrl('https://gw.test', 'a_b', state)).toBe(`https://gw.test/auth/verify?token=a_b&type=signup&state=${state}`)
  })
})

describe('pending verify marker', () => {
  it('pendingVerify_holdPeekConsume', () => {
    mintSignInState(NOW)
    holdPendingVerify(NOW)
    expect(peekPendingVerify(NOW)).toBe(true)
    expect(consumePendingVerify(NOW)).toBe(true)
    expect(consumePendingVerify(NOW)).toBe(false)

    mintSignInState(NOW)
    holdPendingVerify(NOW)
    expect(peekPendingVerify(NOW + TTL)).toBe(false)
    expect(consumePendingVerify(NOW + TTL)).toBe(false)
    expect(sessionStorage.getItem(RAW_KEY)).toBeNull()

    sessionStorage.setItem(RAW_KEY, '{not json')
    expect(peekPendingVerify(NOW)).toBe(false)
    expect(consumePendingVerify(NOW)).toBe(false)
    expect(sessionStorage.getItem(RAW_KEY)).toBeNull()
  })

  it('pendingVerify_peekFalseOnceAnotherStateIsMinted', () => {
    mintSignInState(NOW)
    holdPendingVerify(NOW)
    mintSignInState(NOW + 1)
    expect(peekPendingVerify(NOW + 1)).toBe(false)
  })

  it('pendingVerify_peekFalseWithoutAStoredState', () => {
    holdPendingVerify(NOW)
    expect(peekPendingVerify(NOW)).toBe(false)
  })
})
