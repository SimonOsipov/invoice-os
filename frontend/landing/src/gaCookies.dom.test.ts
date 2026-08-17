// @vitest-environment jsdom
// T3-4. gaCookies.ts is the repo's only document.cookie writer and this file its only
// reader-back, so there is no in-repo idiom to copy.
//
// Two jsdom limits shape the file, and both are proven rather than asserted in prose:
//   1. the cookie jar persists across tests in one file, so every case resets it;
//   2. jsdom drops a Set-Cookie whose domain= is outside the document origin, so this
//      layer CANNOT observe cookieDomainVariants at all. That is a stated deferral,
//      not a covered claim — T3-3 in gaCookies.test.ts is their only oracle.
//
// The module is loaded through a runtime specifier behind an existsSync guard so a
// missing module fails as an ASSERTION rather than a collection error.
//
// This file cannot see the host-only write (gaCookies.ts:49) — jsdom does not separate
// a host-only cookie from a domain-scoped one. gaCookies.adversarial.test.ts owns it.
/// <reference types="node" />
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = dirname(fileURLToPath(import.meta.url))
const MODULE_PATH = join(HERE, 'gaCookies.ts')
// Non-literal on purpose — see the header note.
const MODULE_SPECIFIER = './gaCookies'

type GaCookiesModule = {
  isGaCookieName: (name: string) => boolean
  gaCookieNames: (raw: string) => string[]
  cookieDomainVariants: (hostname: string) => string[]
  clearGaCookies: (hostname?: string, doc?: Pick<Document, 'cookie'>) => string[]
}

async function loadGaCookies(): Promise<GaCookiesModule> {
  expect(existsSync(MODULE_PATH), `expected ${MODULE_PATH} to exist`).toBe(true)
  const mod = (await import(MODULE_SPECIFIER)) as GaCookiesModule
  expect(typeof mod.clearGaCookies, 'expected a clearGaCookies named export').toBe('function')
  return mod
}

function cookieNamesInJar(): string[] {
  return document.cookie
    .split(';')
    .map((pair) => pair.split('=')[0]?.trim() ?? '')
    .filter((name) => name.length > 0)
}

function resetJar() {
  for (const name of cookieNamesInJar()) {
    document.cookie = `${name}=; Max-Age=0; path=/`
  }
}

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

let consoleSpies: ReturnType<typeof spyOnConsole>

beforeEach(() => {
  resetJar()
  consoleSpies = spyOnConsole()
  vi.resetModules()
})

afterEach(() => {
  vi.restoreAllMocks()
  resetJar()
})

describe('T3-4: clearGaCookies actually removes them, read back', () => {
  it('control: the per-test reset leaves an empty jar', () => {
    expect(cookieNamesInJar()).toEqual([])
  })

  it('control: the jar accepts a host-only seed and reads it back', () => {
    // Non-vacuity partner for every "not present" assertion below: without this,
    // a jar that silently refused the seed would make them all pass for free.
    document.cookie = '_ga=GA1.1.1234567890.1700000000; path=/'
    expect(cookieNamesInJar()).toContain('_ga')
  })

  it('AC-3/AC-4: both GA cookies go, a non-GA cookie alongside survives', async () => {
    document.cookie = '_ga=GA1.1.1234567890.1700000000; path=/'
    document.cookie = '_ga_E409H76XYY=GS1.1.1700000000.1.1.1700000123.0.0.0; path=/'
    document.cookie = 'hs_keep=stay; path=/'
    expect(cookieNamesInJar().sort(), 'seed did not land').toEqual(['_ga', '_ga_E409H76XYY', 'hs_keep'])

    const { clearGaCookies } = await loadGaCookies()
    const targeted = clearGaCookies(window.location.hostname)

    expect(targeted.length, 'clearGaCookies reported no targets').toBeGreaterThan(0)
    expect([...targeted].sort()).toEqual(['_ga', '_ga_E409H76XYY'])

    const left = cookieNamesInJar()
    expect(left, 'the _ga cookie survived').not.toContain('_ga')
    expect(left, 'the _ga_ container cookie survived').not.toContain('_ga_E409H76XYY')
    expect(left, 'an unrelated cookie was collateral damage').toContain('hs_keep')
  })

  it('AC-3: the hostname argument defaults to the live hostname', async () => {
    document.cookie = '_ga=GA1.1.1234567890.1700000000; path=/'
    expect(cookieNamesInJar(), 'seed did not land').toContain('_ga')

    const { clearGaCookies } = await loadGaCookies()
    const targeted = clearGaCookies()

    expect(targeted.length, 'clearGaCookies reported no targets').toBeGreaterThan(0)
    expect(cookieNamesInJar()).not.toContain('_ga')
  })

  it('AC-3: an empty jar is a no-op that returns nothing and throws nothing', async () => {
    const { clearGaCookies } = await loadGaCookies()
    expect(clearGaCookies(window.location.hostname)).toEqual([])
    expect(cookieNamesInJar()).toEqual([])
  })
})

describe('STATED DEFERRAL: the domain variants are unobservable at this layer', () => {
  it('jsdom drops a Set-Cookie whose domain= is outside the document origin', () => {
    // The document origin here is localhost. This proves the deferral rather than
    // asserting it in prose: the variant sweep clearGaCookies performs cannot be
    // observed from the jar, which is why its oracle re-reads document.cookie
    // (above) instead of counting writes, and why T3-3 — the pure unit — is the
    // only place cookieDomainVariants is pinned.
    expect(window.location.hostname).toBe('localhost')
    document.cookie = '_ga=foreign; domain=ascomply.com; path=/'
    expect(cookieNamesInJar(), 'jsdom accepted a foreign domain= after all').not.toContain('_ga')
  })
})

describe('AC-8: the cookie path is console-silent', () => {
  it('control: the console spies detect a call', () => {
    console.error('x')
    console.warn('x')
    console.log('x')
    console.info('x')
    expect(consoleSpies.error).toHaveBeenCalledTimes(1)
    expect(consoleSpies.warn).toHaveBeenCalledTimes(1)
    expect(consoleSpies.log).toHaveBeenCalledTimes(1)
    expect(consoleSpies.info).toHaveBeenCalledTimes(1)
  })

  it('AC-8: clearing writes nothing to console, including on the rejected variants', async () => {
    document.cookie = '_ga=GA1.1.1234567890.1700000000; path=/'
    const { clearGaCookies } = await loadGaCookies()
    clearGaCookies('www.ascomply.com')
    expectNoConsoleCalls(consoleSpies)
  })
})
