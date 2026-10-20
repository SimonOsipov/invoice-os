// Runs the inline script(s) of the real index.html against a fake window. A fake location
// is used because jsdom's location.replace cannot be spied.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, describe, expect, it, vi } from 'vitest'

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
function load(url: string, env: Record<string, string>) {
  const html = viteHtml(env)
  const all = scripts(html)
  const moduleAt = all.find((s) => s.module)?.at ?? Infinity
  const inline = all.filter((s) => !s.module && s.code.trim() !== '')
  const u = new URL(url)
  const replace = vi.fn()
  const location = { href: u.href, search: u.search, hash: u.hash, pathname: u.pathname, origin: u.origin, replace }
  const storage = makeStorage()
  const win = { location, sessionStorage: storage, crypto: globalThis.crypto, btoa: globalThis.btoa.bind(globalThis) }
  for (const s of inline) {
    new Function('window', 'location', 'sessionStorage', 'crypto', 'btoa', s.code)(win, location, storage, win.crypto, win.btoa)
  }
  return { inline, beforeModule: inline.every((s) => s.at < moduleAt), replace, storage }
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
})
