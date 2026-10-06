// The invite link's fragment capture (D2): the token is read from `#token=`, kept in sessionStorage and stripped from the address bar.
import { afterEach, describe, expect, it, vi } from 'vitest'


const KEY = 'ascomply.inviteToken'
const T = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const NEW = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn_-9'
const STORED = '0123456789abcdefghijklmnopqrstuvwxyzABCDEFG'

function fakeStorage(initial?: string) {
  const map = new Map<string, string>(initial === undefined ? [] : [[KEY, initial]])
  return {
    map,
    getItem: vi.fn((k: string) => map.get(k) ?? null),
    setItem: vi.fn((k: string, v: string) => void map.set(k, v)),
  }
}

// The module captures when it evaluates, so a static import would read the node environment's missing globals.
// The globals below are a harmless page at `/`; each test then calls the function with its own fakes.
async function load() {
  vi.resetModules()
  vi.stubGlobal('location', { pathname: '/', search: '', hash: '' })
  vi.stubGlobal('history', { replaceState: vi.fn() })
  vi.stubGlobal('sessionStorage', fakeStorage())
  return import('./inviteLink')
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.resetModules()
})

describe('captureInviteToken_readsStoresAndStrips', () => {
  // [name, pathname, search, hash, stored, returned, stored after, replaceState url or null when untouched]
  const rows: [string, string, string, string, string | undefined, string | null, string | undefined, string | null][] = [
    ['a valid hash is returned, stored and stripped', '/invite', '', `#token=${T}`, undefined, T, T, '/invite'],
    ['the query stays in the stripped url', '/invite', '?x=1', `#token=${T}`, undefined, T, T, '/invite?x=1'],
    ['a malformed token captures null and still strips', '/invite', '', '#token=bad', undefined, null, undefined, '/invite'],
    ['a 42-character token is malformed', '/invite', '', `#token=${T.slice(1)}`, undefined, null, undefined, '/invite'],
    ['a 44-character token is malformed', '/invite', '', `#token=${T}x`, undefined, null, undefined, '/invite'],
    ['a hash without a token is stripped', '/invite', '', '#other=1', undefined, null, undefined, '/invite'],
    ['no hash reads the stored token and strips nothing', '/invite', '', '', STORED, STORED, STORED, null],
    ['a new hash replaces the stored token', '/invite', '', `#token=${NEW}`, STORED, NEW, NEW, '/invite'],
  ]

  it.each(rows)('%s', async (_name, pathname, search, hash, stored, returned, storedAfter, url) => {
    const loc = { pathname, search, hash }
    const hist = { replaceState: vi.fn() }
    const storage = fakeStorage(stored)
    const { captureInviteToken } = await load()

    expect(captureInviteToken(loc, hist, storage)).toBe(returned)

    expect(storage.map.get(KEY)).toBe(storedAfter)
    if (url === null) expect(hist.replaceState).not.toHaveBeenCalled()
    else expect(hist.replaceState.mock.calls).toEqual([[null, '', url]])
    // The hash is always read from `loc`, never written: only replaceState changes the address bar.
    expect(loc.hash).toBe(hash)
  })
})

describe('captureInviteToken_leavesOtherPathsUntouched', () => {
  it('only /invite captures: elsewhere nothing is read, written or stripped', async () => {
    const { captureInviteToken } = await load()
    // Control: the same hash on /invite is captured, so a null below is the path rule and not a dead function.
    expect(captureInviteToken({ pathname: '/invite', search: '', hash: `#token=${T}` }, { replaceState: vi.fn() }, fakeStorage())).toBe(T)

    for (const pathname of ['/', '/privacy', '/invite/x', '/invites']) {
      const loc = { pathname, search: '', hash: `#token=${T}` }
      const hist = { replaceState: vi.fn() }
      const storage = fakeStorage(STORED)
      expect(captureInviteToken(loc, hist, storage), pathname).toBeNull()
      expect(hist.replaceState, pathname).not.toHaveBeenCalled()
      expect(storage.setItem, pathname).not.toHaveBeenCalled()
      expect(storage.map.get(KEY), pathname).toBe(STORED)
    }
  })

  it('on /invite with no hash and nothing stored the token is null', async () => {
    const { captureInviteToken } = await load()
    const control = fakeStorage()
    expect(captureInviteToken({ pathname: '/invite', search: '', hash: `#token=${T}` }, { replaceState: vi.fn() }, control)).toBe(T)
    // Fresh storage: the control's token is not visible.
    expect(captureInviteToken({ pathname: '/invite', search: '', hash: '' }, { replaceState: vi.fn() }, fakeStorage())).toBeNull()
  })
})

describe('inviteLink_capturesOnceWhenTheModuleEvaluates', () => {
  it('evaluating the module captures the token once and inviteToken() returns it', async () => {
    const loc = { pathname: '/invite', search: '?x=1', hash: `#token=${T}` }
    const hist = { replaceState: vi.fn() }
    const storage = fakeStorage()
    vi.stubGlobal('location', loc)
    vi.stubGlobal('history', hist)
    vi.stubGlobal('sessionStorage', storage)

    const mod = await import('./inviteLink')

    expect(mod.inviteToken()).toBe(T)
    expect(mod.inviteToken()).toBe(T)
    expect(hist.replaceState.mock.calls).toEqual([[null, '', '/invite?x=1']])
    expect(storage.map.get(KEY)).toBe(T)
  })
})
