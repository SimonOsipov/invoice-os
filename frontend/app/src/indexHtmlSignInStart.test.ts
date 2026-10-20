// Runs the inline script(s) of the real index.html in a vm context with a fake location
// (jsdom's location.replace cannot be spied).
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import vm from 'node:vm'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { holdPendingInvite, PENDING_INVITE_KEY } from './lib/pendingInvite'
import { BASE64URL_43, consumeSignInState, SIGN_IN_STATE_KEY, SIGN_IN_STATE_SCHEMA_VERSION } from './lib/signInState'

const LANDING = 'https://landing.test'
const HTML = readFileSync(resolve(__dirname, '../index.html'), 'utf8')

// Vite's HTML env replacement: %VAR% becomes the value; an unset var stays literal.
function viteHtml(env: Record<string, string>): string {
  return HTML.replace(/%(\S+?)%/g, (m, k: string) => (k in env ? env[k] : m))
}

function scripts(html: string) {
  const out: { module: boolean; code: string; at: number }[] = []
  for (const m of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)) {
    out.push({ module: /type\s*=\s*["']module["']/i.test(m[1]), code: m[2], at: m.index ?? 0 })
  }
  return out
}

function makeStorage() {
  const store = new Map<string, string>()
  return {
    store,
    getItem: (k: string) => (store.has(k) ? (store.get(k) as string) : null),
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  }
}

// Executes every classic inline script ahead of the module script, as the browser would.
type Overrides = { storage?: unknown; crypto?: unknown; held?: Record<string, string> }

function load(url: string, env: Record<string, string>, over: Overrides = {}) {
  const html = viteHtml(env)
  const all = scripts(html)
  const moduleAt = all.find((s) => s.module)?.at ?? Infinity
  const inline = all.filter((s) => !s.module && s.code.trim() !== '')
  const u = new URL(url)
  const replace = vi.fn()
  const location = { href: u.href, search: u.search, hash: u.hash, pathname: u.pathname, origin: u.origin, replace }
  const storage = makeStorage()
  for (const [k, v] of Object.entries(over.held ?? {})) storage.setItem(k, v)
  // A vm context gives the script real classic-script semantics: top-level `var` becomes a global.
  const globals: Record<string, unknown> = {
    location,
    sessionStorage: 'storage' in over ? over.storage : storage,
    crypto: 'crypto' in over ? over.crypto : globalThis.crypto,
    btoa: globalThis.btoa.bind(globalThis),
    URLSearchParams,
    Uint8Array,
    Date,
    JSON,
  }
  const ctx = vm.createContext(globals)
  const declared = new Set(Object.keys(globals))
  let threw: unknown = null
  try {
    for (const s of inline) vm.runInContext(s.code, ctx)
  } catch (e) {
    threw = e
  }
  const leaked = Object.keys(globals).filter((k) => !declared.has(k))
  return { inline, beforeModule: inline.every((s) => s.at < moduleAt), replace, storage, location, threw, leaked, startHref: u.href }
}

// A live held invite as the app writes it, so the key and schema come from pendingInvite.ts.
function heldInvite(): Record<string, string> {
  const probe = makeStorage()
  vi.stubGlobal('sessionStorage', probe)
  holdPendingInvite('B'.repeat(43))
  vi.unstubAllGlobals()
  expect(probe.store.has(PENDING_INVITE_KEY)).toBe(true)
  return { [PENDING_INVITE_KEY]: probe.getItem(PENDING_INVITE_KEY) as string }
}

beforeEach(() => vi.unstubAllGlobals())

describe('index.html ?auth=start answered before the bundle (BUG-41 A)', () => {
  it('A1: stores a state and replaces to the landing with ?state=<that state>&signin=ready, ahead of main.tsx', () => {
    const { inline, beforeModule, replace, storage } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING })
    expect(inline.length).toBeGreaterThan(0)
    expect(beforeModule).toBe(true)
    const raw = storage.getItem(SIGN_IN_STATE_KEY)
    expect(raw).not.toBeNull()
    const s = (JSON.parse(raw as string) as { s: string }).s
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith(`${LANDING}/?state=${s}&signin=ready`)
  })

  it('A2: the stored blob is a live state for consumeSignInState (schema, key, 43-char base64url)', () => {
    const { inline, storage } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING })
    expect(inline.length).toBeGreaterThan(0)
    const blob = JSON.parse(storage.getItem(SIGN_IN_STATE_KEY) as string) as { v: number; s: string; at: number }
    expect(blob.v).toBe(SIGN_IN_STATE_SCHEMA_VERSION)
    expect(blob.s).toMatch(BASE64URL_43)
    vi.stubGlobal('sessionStorage', storage)
    expect(consumeSignInState()).toBe(blob.s)
  })

  describe('A3: other cases are left to the React path', () => {
    const cases: [string, string, Record<string, string>][] = [
      ['an invite fragment', 'https://app.test/?auth=start#invite=tok123', { VITE_LANDING_URL: LANDING }],
      ['a hand-off code', 'https://app.test/?auth=start&handoff=abc', { VITE_LANDING_URL: LANDING }],
      ['an empty landing URL', 'https://app.test/?auth=start', { VITE_LANDING_URL: '' }],
      ['an unset landing URL', 'https://app.test/?auth=start', {}],
    ]
    it.each(cases)('does nothing for %s', (_n, url, env) => {
      // The positive control: the script exists, so "does nothing" is a decision, not an absence.
      const { inline, replace, storage } = load(url, env)
      expect(inline.length).toBeGreaterThan(0)
      expect(storage.store.size).toBe(0)
      expect(replace).not.toHaveBeenCalled()
    })

    it('does nothing without ?auth=start', () => {
      const { inline, replace, storage } = load('https://app.test/', { VITE_LANDING_URL: LANDING })
      expect(inline.length).toBeGreaterThan(0)
      expect(storage.store.size).toBe(0)
      expect(replace).not.toHaveBeenCalled()
    })
  })

  describe('edge cases', () => {
    it('trims whitespace and trailing slashes off the landing URL, as landingBase() does', () => {
      const { inline, replace } = load('https://app.test/?auth=start', { VITE_LANDING_URL: '  https://landing.test///  ' })
      expect(inline.length).toBeGreaterThan(0)
      expect(replace).toHaveBeenCalledTimes(1)
      expect(replace.mock.calls[0][0]).toMatch(/^https:\/\/landing\.test\/\?state=[A-Za-z0-9_-]{43}&signin=ready$/)
    })

    it('replaces the entry; it never assigns location.href (no extra history entry)', () => {
      const { replace, location, startHref } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING })
      expect(replace).toHaveBeenCalledTimes(1)
      expect(location.href).toBe(startHref)
    })

    it('stamps at with the current time, so consumeSignInState reads it as live', () => {
      const before = Date.now()
      const { storage } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING })
      const at = (JSON.parse(storage.getItem(SIGN_IN_STATE_KEY) as string) as { at: number }).at
      expect(at).toBeGreaterThanOrEqual(before)
      expect(at).toBeLessThanOrEqual(Date.now())
    })

    it('a second mint replaces the held blob, as mintSignInState does', () => {
      const held = JSON.stringify({ v: SIGN_IN_STATE_SCHEMA_VERSION, s: 'A'.repeat(43), at: Date.now() - 1000 })
      const { storage, replace } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING }, { held: { [SIGN_IN_STATE_KEY]: held } })
      const s = (JSON.parse(storage.getItem(SIGN_IN_STATE_KEY) as string) as { s: string }).s
      expect(s).not.toBe('A'.repeat(43))
      expect(replace).toHaveBeenCalledWith(`${LANDING}/?state=${s}&signin=ready`)
    })

    it('drops a held pending invite, as the React bounce does for a start without an invite', () => {
      const held = heldInvite()
      const { storage, replace } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING }, { held })
      expect(replace).toHaveBeenCalledTimes(1)
      expect(storage.getItem(PENDING_INVITE_KEY)).toBeNull()
    })

    it('keeps a held pending invite when it leaves the start to React (invite fragment)', () => {
      const held = heldInvite()
      const { inline, storage, replace } = load('https://app.test/?auth=start#invite=tok', { VITE_LANDING_URL: LANDING }, { held })
      expect(inline.length).toBeGreaterThan(0)
      expect(replace).not.toHaveBeenCalled()
      expect(storage.getItem(PENDING_INVITE_KEY)).not.toBeNull()
    })

    it('storage that throws: no replace, nothing thrown, React path takes over', () => {
      const storage = {
        getItem: () => null,
        removeItem: () => {},
        setItem: () => {
          throw new DOMException('quota', 'QuotaExceededError')
        },
      }
      const { inline, replace, threw } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING }, { storage })
      expect(inline.length).toBeGreaterThan(0)
      expect(threw).toBeNull()
      expect(replace).not.toHaveBeenCalled()
    })

    it('sessionStorage unreachable: no replace, nothing thrown', () => {
      const { inline, replace, threw } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING }, { storage: undefined })
      expect(inline.length).toBeGreaterThan(0)
      expect(threw).toBeNull()
      expect(replace).not.toHaveBeenCalled()
    })

    it('no crypto: no replace, no stored state, nothing thrown', () => {
      const { inline, replace, storage, threw } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING }, { crypto: undefined })
      expect(inline.length).toBeGreaterThan(0)
      expect(threw).toBeNull()
      expect(replace).not.toHaveBeenCalled()
      expect(storage.store.size).toBe(0)
    })

    const skipped: [string, string][] = [
      ['?auth=other', 'https://app.test/?auth=other'],
      ['?auth=START (case)', 'https://app.test/?auth=START'],
      ['an empty ?handoff=', 'https://app.test/?auth=start&handoff='],
      ['an empty #invite=', 'https://app.test/?auth=start#invite='],
      ['a second invite in the fragment', 'https://app.test/?auth=start#invite=a&invite=b'],
      ['an invite beside another fragment key', 'https://app.test/?auth=start#x=1&invite=tok'],
      ['?auth=verify', 'https://app.test/?auth=verify#token=t'],
    ]
    it.each(skipped)('leaves %s to the React path', (_n, url) => {
      const { inline, replace, storage } = load(url, { VITE_LANDING_URL: LANDING })
      expect(inline.length).toBeGreaterThan(0)
      expect(replace).not.toHaveBeenCalled()
      expect(storage.store.size).toBe(0)
    })

    it('an unreplaced %VITE_LANDING_URL% literal is never used as a landing URL', () => {
      const { inline, replace } = load('https://app.test/?auth=start', { VITE_LANDING_URL: '%VITE_LANDING_URL%' })
      expect(inline.length).toBeGreaterThan(0)
      expect(replace).not.toHaveBeenCalled()
    })

    // Source scan: only a browser sees the stall (probe: a 3 s font sheet held the redirect 3 s).
    it('the redirect script precedes every stylesheet link, which would hold a later script back', () => {
      const html = viteHtml({ VITE_LANDING_URL: LANDING })
      const links = [...html.matchAll(/<link\b[^>]*rel\s*=\s*["']stylesheet["'][^>]*>/gi)]
      const inline = scripts(html).filter((x) => !x.module && x.code.trim() !== '')
      expect(links.length).toBeGreaterThan(0)
      expect(inline.length).toBeGreaterThan(0)
      for (const x of inline) for (const l of links) expect(x.at).toBeLessThan(l.index ?? 0)
    })

    it('leaks no global into the page', () => {
      const { inline, leaked } = load('https://app.test/?auth=start', { VITE_LANDING_URL: LANDING })
      expect(inline.length).toBeGreaterThan(0)
      expect(leaked).toEqual([])
    })
  })
})
