// RED specs (task-562, LAND-05-03, Test-first) — T3-1..T3-3, authored before
// gaCookies.ts exists. Pure functions only, environment 'node' (vitest.config.ts).
// The jar-level behaviour is gaCookies.dom.test.ts.
//
// The module is loaded through a runtime specifier behind an existsSync guard so a
// missing module fails as an ASSERTION rather than a collection error; tsc does not
// resolve non-literal dynamic imports, so typecheck stays green while it is absent.
// Same idiom as CookieNotice.render.test.tsx (task-561).
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

const EXPORTS = ['isGaCookieName', 'gaCookieNames', 'cookieDomainVariants', 'clearGaCookies'] as const

async function loadGaCookies(): Promise<GaCookiesModule> {
  expect(existsSync(MODULE_PATH), `expected ${MODULE_PATH} to exist`).toBe(true)
  const mod = (await import(MODULE_SPECIFIER)) as GaCookiesModule
  for (const name of EXPORTS) {
    expect(typeof mod[name], `expected a ${name} named export`).toBe('function')
  }
  return mod
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
  consoleSpies = spyOnConsole()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('T3-1: isGaCookieName admits GA names and nothing adjacent', () => {
  it('AC-4: _ga and _ga_<container> are GA names', async () => {
    const { isGaCookieName } = await loadGaCookies()
    expect(isGaCookieName('_ga'), '_ga').toBe(true)
    expect(isGaCookieName('_ga_E409H76XYY'), '_ga_E409H76XYY').toBe(true)
  })

  it('AC-4: the adjacent names are not GA names', async () => {
    const { isGaCookieName } = await loadGaCookies()
    // _gat and _gid are real Google cookies this story does not own; a lax
    // startsWith('_ga') swallows _gat.
    for (const name of ['_gat', '_gid', 'ga', 'x_ga', '', '_g', 'ga_X']) {
      expect(isGaCookieName(name), `"${name}" must not be treated as a GA cookie`).toBe(false)
    }
  })
})

describe('T3-2: gaCookieNames parses a real cookie string and is total on junk', () => {
  it('AC-4: only the GA names come back, in document order', async () => {
    const { gaCookieNames } = await loadGaCookies()
    expect(gaCookieNames('_ga=1; _ga_X=2; hs=3')).toEqual(['_ga', '_ga_X'])
  })

  it('AC-4: junk yields an empty list rather than throwing', async () => {
    const { gaCookieNames } = await loadGaCookies()
    for (const raw of ['', ';;', '=', '   ', ';=;']) {
      expect(gaCookieNames(raw), `raw: "${raw}"`).toEqual([])
    }
  })
})

describe('T3-3: cookieDomainVariants stops at two labels and dots each', () => {
  it('AC-4: the production host yields four entries in order', async () => {
    const { cookieDomainVariants } = await loadGaCookies()
    expect(cookieDomainVariants('www.ascomply.com')).toEqual([
      'www.ascomply.com',
      '.www.ascomply.com',
      'ascomply.com',
      '.ascomply.com',
    ])
  })

  it('AC-4: a single-label host yields only itself', async () => {
    const { cookieDomainVariants } = await loadGaCookies()
    expect(cookieDomainVariants('localhost')).toEqual(['localhost'])
  })

  it('AC-4: a two-label host yields itself and its dotted form', async () => {
    const { cookieDomainVariants } = await loadGaCookies()
    expect(cookieDomainVariants('ascomply.com')).toEqual(['ascomply.com', '.ascomply.com'])
  })

  it('AC-4: the over-generation on a public suffix is deliberate and harmless', async () => {
    // "Stop at two labels" over-generates on foo.co.uk: co.uk is a public suffix,
    // and every browser discards a Set-Cookie whose domain= is one, so those two
    // writes never reach the jar while the two useful ones still run. A PSL is a
    // new dependency and out of scope. Without this case a future trim to three
    // labels reads as a correction and silently stops clearing ascomply.com.
    const { cookieDomainVariants } = await loadGaCookies()
    expect(cookieDomainVariants('foo.co.uk')).toEqual(['foo.co.uk', '.foo.co.uk', 'co.uk', '.co.uk'])
  })
})

describe('AC-8: the pure functions are console-silent', () => {
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

  it('AC-8: parsing, naming and variant generation write nothing to console', async () => {
    const { isGaCookieName, gaCookieNames, cookieDomainVariants } = await loadGaCookies()
    isGaCookieName('_gat')
    gaCookieNames(';;')
    cookieDomainVariants('foo.co.uk')
    expectNoConsoleCalls(consoleSpies)
  })
})
