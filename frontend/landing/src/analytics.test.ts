// RED specs (task-543, LAND-03-02, Test-first) — pin the measurement id resolver, the
// composed gate and two static source-shape guards before analytics.ts is implemented.
// vi.stubEnv idiom copies hubspot.test.ts:34-38. Node environment (package default):
// no window/document exist, which is itself asserted below (module-scope purity guard).
//
// Seeds the implicit node type-library for this project (no other landing file carries
// it, unlike frontend/app's approvals.test.ts) — without it node:* imports fail TS2591.
/// <reference types="node" />
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { measurementId, shouldLoadTag, tagSrc } from './analytics'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

const HERE = dirname(fileURLToPath(import.meta.url))
const ANALYTICS_SRC = readFileSync(join(HERE, 'analytics.ts'), 'utf8')
const MAIN_SRC = readFileSync(join(HERE, 'main.tsx'), 'utf8')

const ID = 'G-E409H76XYY'

describe('measurementId', () => {
  it('AC-1: resolves from the environment at call time', () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    expect(measurementId()).toBe(ID)
  })

  it('AC-2: an unset or blank measurement id is null, never an empty string', () => {
    expect(measurementId()).toBeNull()

    vi.stubEnv('VITE_GA_MEASUREMENT_ID', '')
    expect(measurementId()).toBeNull()

    vi.stubEnv('VITE_GA_MEASUREMENT_ID', '   ')
    expect(measurementId()).toBeNull()
  })

  it('trims surrounding whitespace around a real id, not just blanks', () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', `  ${ID}  `)
    expect(measurementId()).toBe(ID)
  })
})

describe('shouldLoadTag', () => {
  it('AC-2: the gate is closed on every non-production hostname', () => {
    for (const h of [
      'localhost',
      'landing-pr-42.up.railway.app',
      'ascomply.com',
      'WWW.ASCOMPLY.COM.attacker.example',
      '',
    ]) {
      expect(shouldLoadTag(h, true, ID), h).toBe(false)
    }
  })

  it('AC-2: the gate is open only on the exact production hostname', () => {
    expect(shouldLoadTag('www.ascomply.com', true, ID)).toBe(true)
    expect(shouldLoadTag(' WWW.ASCOMPLY.COM ', true, ID)).toBe(true)
  })

  it('the gate stays closed on a trailing dot, a port, or a userinfo prefix', () => {
    for (const h of ['www.ascomply.com.', 'www.ascomply.com:443', 'user@www.ascomply.com']) {
      expect(shouldLoadTag(h, true, ID), h).toBe(false)
    }
  })

  it('AC-8: denied consent closes the gate on the production hostname', () => {
    expect(shouldLoadTag('www.ascomply.com', false, ID)).toBe(false)
  })

  it('AC-2: a null measurement id closes the gate on the production hostname', () => {
    expect(shouldLoadTag('www.ascomply.com', true, null)).toBe(false)
  })

  it('AC-2: the gate reuses hubspot.ts\'s predicate rather than a copy', () => {
    expect(ANALYTICS_SRC).toMatch(/import\s*\{[^}]*\bisProductionHost\b[^}]*\}\s*from\s*['"]\.\/hubspot['"]/)
    expect(ANALYTICS_SRC).not.toMatch(/ascomply\.com/)
  })
})

describe('tagSrc', () => {
  it('AC-1: points at googletagmanager with the id as a query parameter', () => {
    expect(tagSrc(ID)).toBe(`https://www.googletagmanager.com/gtag/js?id=${ID}`)
  })
})

describe('module-scope purity', () => {
  it('AC-2: importing the module in a node environment is inert', async () => {
    // Precondition, not a redundant check: proves the node environment carries
    // no window/document before trusting the "never touched" claim below.
    expect(globalThis.window).toBeUndefined()
    expect(globalThis.document).toBeUndefined()
    await expect(import('./analytics')).resolves.toBeDefined()
    expect(globalThis.window).toBeUndefined()
    expect(globalThis.document).toBeUndefined()
  })
})

describe('consent-mode absence (Q2)', () => {
  const CONSENT_MODE = /gtag\s*\(\s*['"]consent['"]/

  it('AC-1: the matcher detects a consent-mode call in three syntactic forms (planted-positive control)', () => {
    expect(CONSENT_MODE.test(`gtag('consent', 'default', {})`)).toBe(true)
    expect(CONSENT_MODE.test(`gtag("consent","update",{})`)).toBe(true)
    expect(CONSENT_MODE.test(`gtag(\n  'consent', 'default')`)).toBe(true)
    expect(CONSENT_MODE.test(`gtag('config','G-E409H76XYY')`)).toBe(false)
  })

  it('AC-1: the shipped module contains no consent-mode call', () => {
    // Control needles first: an empty or misresolved read would otherwise pass vacuously.
    expect(ANALYTICS_SRC.length).toBeGreaterThan(0)
    expect(ANALYTICS_SRC).toContain('gtag')
    expect(ANALYTICS_SRC).toContain('tagSrc')
    expect(CONSENT_MODE.test(ANALYTICS_SRC)).toBe(false)
  })
})

describe('main.tsx boot wiring', () => {
  it('AC-1: boots analytics after render', () => {
    expect(MAIN_SRC).toContain('createRoot(')
    expect(MAIN_SRC).toMatch(/import\s*\{[^}]*\bbootAnalytics\b[^}]*\}\s*from\s*['"]\.\/analytics['"]/)
    expect(MAIN_SRC.indexOf('bootAnalytics()')).toBeGreaterThan(MAIN_SRC.indexOf('.render('))
  })
})
