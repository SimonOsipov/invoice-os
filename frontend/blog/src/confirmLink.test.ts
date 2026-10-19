import { afterEach, describe, expect, it, vi } from 'vitest'

const KEY = 'ascomply.blogConfirmToken'
const T = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const STORED = '0123456789abcdefghijklmnopqrstuvwxyzABCDEFG'
const PATH = '/subscribe/confirm'

function fakeStorage(initial?: string) {
  const map = new Map<string, string>(initial === undefined ? [] : [[KEY, initial]])
  return {
    map,
    getItem: vi.fn((k: string) => map.get(k) ?? null),
    setItem: vi.fn((k: string, v: string) => void map.set(k, v)),
  }
}

// The module captures when it evaluates, so load it against a harmless page at `/`.
async function load() {
  vi.resetModules()
  vi.stubGlobal('location', { pathname: '/', search: '', hash: '' })
  vi.stubGlobal('history', { replaceState: vi.fn() })
  vi.stubGlobal('sessionStorage', fakeStorage())
  return import('./confirmLink')
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.resetModules()
})

describe('captureConfirmToken', () => {
  it('capture_readsStoresAndStrips', async () => {
    const { captureConfirmToken } = await load()
    const storage = fakeStorage()
    const replaceState = vi.fn()
    expect(captureConfirmToken({ pathname: PATH, search: `?token=${T}`, hash: '' }, { replaceState }, storage)).toBe(T)
    expect(storage.map.get(KEY)).toBe(T)
    expect(replaceState).toHaveBeenCalledWith(null, '', PATH)
  })

  it('capture_reloadReadsStorage', async () => {
    const { captureConfirmToken } = await load()
    const replaceState = vi.fn()
    expect(captureConfirmToken({ pathname: PATH, search: '', hash: '' }, { replaceState }, fakeStorage(STORED))).toBe(STORED)
    expect(replaceState).not.toHaveBeenCalled()
  })

  it('capture_rejectsOtherShapes', async () => {
    const { captureConfirmToken } = await load()
    for (const search of [`?token=${T.slice(1)}`, `?token=${T}&x=b`, `?t=${T}`, `?token=${T}&token=${T}`]) {
      const storage = fakeStorage()
      const replaceState = vi.fn()
      expect(captureConfirmToken({ pathname: PATH, search, hash: '' }, { replaceState }, storage), search).toBeNull()
      expect(storage.setItem, search).not.toHaveBeenCalled()
      expect(replaceState, search).toHaveBeenCalledWith(null, '', PATH)
    }
  })

  it('capture_fragmentIsStripped', async () => {
    const { captureConfirmToken } = await load()
    const replaceState = vi.fn()
    captureConfirmToken({ pathname: PATH, search: '', hash: '#x' }, { replaceState }, fakeStorage())
    expect(replaceState).toHaveBeenCalledWith(null, '', PATH)
  })

  it('capture_otherPathIsUntouched', async () => {
    const { captureConfirmToken } = await load()
    for (const pathname of ['/', '/subscribe', '/subscribe/x']) {
      const storage = fakeStorage()
      const replaceState = vi.fn()
      expect(captureConfirmToken({ pathname, search: `?token=${T}`, hash: '' }, { replaceState }, storage), pathname).toBeNull()
      expect(replaceState, pathname).not.toHaveBeenCalled()
      expect(storage.setItem, pathname).not.toHaveBeenCalled()
    }
  })

  it('capture_trailingSlashPathCaptures', async () => {
    const { captureConfirmToken } = await load()
    const replaceState = vi.fn()
    expect(captureConfirmToken({ pathname: `${PATH}/`, search: `?token=${T}`, hash: '' }, { replaceState }, fakeStorage())).toBe(T)
    expect(replaceState).toHaveBeenCalledWith(null, '', `${PATH}/`)
  })

  it('capture_invalidStoredTokenIsNull', async () => {
    const { captureConfirmToken } = await load()
    for (const bad of ['short', `${STORED}x`, `${STORED.slice(1)}!`]) {
      expect(captureConfirmToken({ pathname: PATH, search: '', hash: '' }, { replaceState: vi.fn() }, fakeStorage(bad)), bad).toBeNull()
    }
  })

  it('capture_freshTokenWinsOverStored', async () => {
    const { captureConfirmToken } = await load()
    const storage = fakeStorage(STORED)
    expect(captureConfirmToken({ pathname: PATH, search: `?token=${T}`, hash: '' }, { replaceState: vi.fn() }, storage)).toBe(T)
    expect(storage.map.get(KEY)).toBe(T)
  })

  it('capture_storageThrowsStillStrips', async () => {
    const { captureConfirmToken } = await load()
    const boom = () => {
      throw new Error('blocked')
    }
    const replaceState = vi.fn()
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(captureConfirmToken({ pathname: PATH, search: `?token=${T}`, hash: '' }, { replaceState }, { getItem: boom, setItem: boom })).toBe(T)
    expect(replaceState).toHaveBeenCalledWith(null, '', PATH)
    warn.mockRestore()
  })
})

describe('clearConfirmToken', () => {
  it('clear_removesTheStoredTokenAndTheCapture', async () => {
    vi.resetModules()
    vi.stubGlobal('location', { pathname: PATH, search: `?token=${T}`, hash: '' })
    vi.stubGlobal('history', { replaceState: vi.fn() })
    const storage = { ...fakeStorage(), removeItem: vi.fn() }
    vi.stubGlobal('sessionStorage', storage)
    const mod = await import('./confirmLink')
    expect(mod.confirmToken()).toBe(T)
    mod.clearConfirmToken()
    expect(storage.removeItem).toHaveBeenCalledWith(KEY)
    expect(mod.confirmToken()).toBeNull()
  })
})
