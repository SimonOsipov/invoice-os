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
    // One `token` key and nothing else: a second pair, a repeated key or a different key first is no token.
    ['a second pair beside the token is no token', '/invite', '', `#token=${T}&x=1`, undefined, null, undefined, '/invite'],
    ['a different key before the token is no token', '/invite', '', `#x=1&token=${T}`, undefined, null, undefined, '/invite'],
    ['a repeated token key is no token', '/invite', '', `#token=${T}&token=${T}`, undefined, null, undefined, '/invite'],
    ['another key holding a token-shaped value is no token', '/invite', '', `#other=${T}`, undefined, null, undefined, '/invite'],
    ['a slash is outside the base64url alphabet', '/invite', '', `#token=${T.slice(0, 20)}/${T.slice(21)}`, undefined, null, undefined, '/invite'],
    ['a plus is outside the base64url alphabet', '/invite', '', `#token=${T.slice(0, 20)}%2B${T.slice(21)}`, undefined, null, undefined, '/invite'],
    ['an empty token is no token', '/invite', '', '#token=', undefined, null, undefined, '/invite'],
    ['a second pair leaves the stored token and stores nothing new', '/invite', '', `#token=${NEW}&x=1`, STORED, STORED, STORED, '/invite'],
    // A malformed fragment is stripped and the stored token stays.
    ['a malformed token strips and keeps the stored token', '/invite', '', '#token=bad', STORED, STORED, STORED, '/invite'],
    ['a hash without a token strips and keeps the stored token', '/invite', '?x=1', '#other=1', STORED, STORED, STORED, '/invite?x=1'],
    ['a stored value that is not a token reads null', '/invite', '', '', 'bad', null, 'bad', null],
    // The strip keeps the pathname as typed; the route rule is case-blind.
    ['an upper-case path captures and keeps its case in the strip', '/INVITE', '', `#token=${T}`, undefined, T, T, '/INVITE'],
    ['a trailing slash is kept in the strip', '/invite/', '?x=1', `#token=${T}`, undefined, T, T, '/invite/?x=1'],
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

describe('captureInviteToken_failuresWarnNeverThrowAndNeverLogTheToken', () => {
  async function run(storage: Parameters<Awaited<ReturnType<typeof load>>['captureInviteToken']>[2], hist: { replaceState: (data: unknown, unused: string, url?: string | null) => void }, hash: string) {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    const log = vi.spyOn(console, 'log').mockImplementation(() => undefined)
    const error = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const { captureInviteToken } = await load()
    const result = captureInviteToken({ pathname: '/invite', search: '', hash }, hist, storage)
    const lines = [...warn.mock.calls, ...log.mock.calls, ...error.mock.calls].map((args) => args.map(String).join(' '))
    vi.restoreAllMocks()
    return { result, lines }
  }
  const boom = () => {
    throw new Error(`storage refused ${T}`)
  }

  it('a storage that refuses the write still returns the token, strips the hash and warns without it', async () => {
    const hist = { replaceState: vi.fn() }
    const { result, lines } = await run({ getItem: vi.fn(), setItem: boom }, hist, `#token=${T}`)
    expect(result).toBe(T)
    expect(hist.replaceState.mock.calls).toEqual([[null, '', '/invite']])
    expect(lines.length, 'control: a failure is reported').toBeGreaterThan(0)
    for (const l of lines) expect(l).not.toContain(T)
  })

  it('a storage that refuses the read returns null and warns without throwing', async () => {
    const { result, lines } = await run({ getItem: boom, setItem: vi.fn() }, { replaceState: vi.fn() }, '')
    expect(result).toBeNull()
    expect(lines.length, 'control: a failure is reported').toBeGreaterThan(0)
  })

  it('a history that refuses the strip still returns and stores the token and warns without it', async () => {
    const storage = fakeStorage()
    const { result, lines } = await run(storage, { replaceState: vi.fn(boom) }, `#token=${T}`)
    expect(result).toBe(T)
    expect(storage.map.get(KEY)).toBe(T)
    expect(lines.length, 'control: a failure is reported').toBeGreaterThan(0)
    for (const l of lines) expect(l).not.toContain(T)
  })

  it('a sessionStorage that throws when the module reads it still strips the fragment', async () => {
    const loc = { pathname: '/invite', search: '', hash: `#token=${T}` }
    const hist = { replaceState: vi.fn() }
    vi.resetModules()
    vi.stubGlobal('location', loc)
    vi.stubGlobal('history', hist)
    // Safari with all cookies blocked throws a SecurityError on the property read itself.
    Object.defineProperty(globalThis, 'sessionStorage', {
      configurable: true,
      get() {
        throw new Error('SecurityError')
      },
    })
    vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    try {
      const mod = await import('./inviteLink')
      expect(mod.inviteToken()).toBe(T)
      expect(hist.replaceState.mock.calls, 'the token must leave the address bar before ./instrument runs').toEqual([[null, '', '/invite']])
    } finally {
      delete (globalThis as { sessionStorage?: unknown }).sessionStorage
    }
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
