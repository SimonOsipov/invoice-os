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

import { measurementId, shouldLoadTag, tagSrc, DEMO_CTA_SOURCES, scrollDepthPercent } from './analytics'

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

const HERE = dirname(fileURLToPath(import.meta.url))
const ANALYTICS_SRC = readFileSync(join(HERE, 'analytics.ts'), 'utf8')
const MAIN_SRC = readFileSync(join(HERE, 'main.tsx'), 'utf8')
const APP_SRC = readFileSync(join(HERE, 'App.tsx'), 'utf8')
const DEMO_MODAL_SRC = readFileSync(join(HERE, 'components', 'DemoModal.tsx'), 'utf8')
const CTA_COMPONENTS = ['Nav.tsx', 'Hero.tsx', 'Audience.tsx', 'Pricing.tsx', 'DemoCta.tsx', 'Footer.tsx']

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

describe('scrollDepthPercent (AC-7)', () => {
  it('depth is the rounded fraction of scrollable height', () => {
    expect(scrollDepthPercent(1000, 800, 4800)).toBe(25)
    expect(scrollDepthPercent(2000, 800, 4800)).toBe(50)
    expect(scrollDepthPercent(4000, 800, 4800)).toBe(100)
  })

  it('the bottom of the page reads as 100 despite sub-pixel scroll', () => {
    // Kills a Math.floor mutant: floor(99.99) = 99, round(99.99) = 100.
    expect(scrollDepthPercent(3999.6, 800, 4800)).toBe(100)
  })

  it('a page that fits the viewport reads as fully seen', () => {
    expect(scrollDepthPercent(0, 900, 900)).toBe(100)
    expect(scrollDepthPercent(0, 900, 400)).toBe(100)
  })

  it('depth is clamped to 0..100 under rubber-band scrolling', () => {
    expect(scrollDepthPercent(-120, 800, 4800)).toBe(0)
    expect(scrollDepthPercent(9999, 800, 4800)).toBe(100)
  })

  it('AC-9: a non-finite measurement fails dark, never wrong', () => {
    expect(scrollDepthPercent(NaN, 800, 4800)).toBe(0)
    expect(scrollDepthPercent(1000, NaN, 4800)).toBe(0)
    expect(scrollDepthPercent(1000, 800, NaN)).toBe(0)
    expect(scrollDepthPercent(Infinity, 800, 4800)).toBe(0)
    expect(scrollDepthPercent(1000, Infinity, 4800)).toBe(0)
    expect(scrollDepthPercent(1000, 800, Infinity)).toBe(0)
  })

  it('AC-9: a non-finite viewport does not masquerade as a fully-seen page', () => {
    // Guard-order pin: checking `scrollable <= 0` before the finite check
    // would return 100 here instead of 0.
    expect(scrollDepthPercent(0, Infinity, 900)).toBe(0)
    expect(scrollDepthPercent(0, NaN, 900)).toBe(0)
    expect(scrollDepthPercent(0, 900, Infinity)).toBe(0)
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

describe('App.tsx CTA bindings (AC-3)', () => {
  it('all six App.tsx call sites are bound to distinct sources', () => {
    // Control needle first (A-14): a misresolved/empty read would otherwise pass vacuously.
    expect(APP_SRC.length).toBeGreaterThan(0)
    expect(APP_SRC).toContain('onBookDemo')

    const bound = Array.from(APP_SRC.matchAll(/book\('([a-z_]+)'\)/g)).map((m) => m[1])
    expect(bound.length).toBe(DEMO_CTA_SOURCES.length)
    expect(new Set(bound).size).toBe(bound.length)
    expect(new Set(bound)).toEqual(new Set(DEMO_CTA_SOURCES))
    expect(APP_SRC).not.toMatch(/onBookDemo=\{onBookDemo\}/)
  })
})

describe('App.tsx scroll-depth listener (AC-7)', () => {
  it('registers one passive throttled scroll listener and removes it', () => {
    // Control needle first (A-14): a misresolved/empty read would otherwise pass vacuously.
    expect(APP_SRC.length).toBeGreaterThan(0)
    expect(APP_SRC).toContain('onBookDemo')

    expect(Array.from(APP_SRC.matchAll(/addEventListener\('scroll'/g)).length).toBe(1)
    expect(APP_SRC).toMatch(/addEventListener\('scroll',\s*\w+,\s*\{\s*passive:\s*true\s*\}\)/)
    expect(Array.from(APP_SRC.matchAll(/removeEventListener\('scroll'/g)).length).toBe(1)
    expect(Array.from(APP_SRC.matchAll(/requestAnimationFrame\(/g)).length).toBe(1)
    expect(Array.from(APP_SRC.matchAll(/cancelAnimationFrame\(/g)).length).toBe(1)

    // Nav.tsx keeps its own single pair — proves App.tsx's listener isn't folded
    // into Nav.tsx's effect (AC-6, decision A-6).
    const NAV_SRC = readFileSync(join(HERE, 'components', 'Nav.tsx'), 'utf8')
    expect(Array.from(NAV_SRC.matchAll(/addEventListener\('scroll'/g)).length).toBe(1)
    expect(Array.from(NAV_SRC.matchAll(/removeEventListener\('scroll'/g)).length).toBe(1)
  })

  it('composes trackScrollDepth(scrollDepthPercent(...)) rather than an empty measure()', () => {
    expect(APP_SRC.length).toBeGreaterThan(0)
    expect(APP_SRC).toContain('onBookDemo')

    const analyticsImports = Array.from(APP_SRC.matchAll(/import\s*\{([^}]*)\}\s*from\s*['"]\.\/analytics['"]/g))
    expect(analyticsImports.length, 'expected exactly one import statement from ./analytics').toBe(1)
    const names = analyticsImports[0][1].split(',').map((s) => s.trim())
    expect(names).toEqual(expect.arrayContaining(['scrollDepthPercent', 'trackScrollDepth']))

    expect(APP_SRC).toMatch(/trackScrollDepth\(\s*scrollDepthPercent\(/)
  })
})

describe('CTA components untouched (AC-3)', () => {
  it.each(CTA_COMPONENTS)('%s still declares onBookDemo: () => void and imports no analytics', (file) => {
    const src = readFileSync(join(HERE, 'components', file), 'utf8')
    expect(src.length).toBeGreaterThan(0)
    expect(src).toContain('onBookDemo')
    expect(src).toMatch(/onBookDemo\s*:\s*\(\)\s*=>\s*void/)
    expect(src).not.toMatch(/from\s*['"]\.\.\/analytics['"]/)
  })
})

describe('honeypot cannot reach outcome senders (AC-5)', () => {
  it('DemoModal imports trackedHubSpotSubmit and neither sender directly', () => {
    expect(DEMO_MODAL_SRC.length).toBeGreaterThan(0)
    expect(DEMO_MODAL_SRC).toContain('submitDemoLead')

    expect(DEMO_MODAL_SRC).toMatch(
      /import\s*\{[^}]*\btrackedHubSpotSubmit\b[^}]*\}\s*from\s*['"]\.\.\/analytics['"]/,
    )
    expect(DEMO_MODAL_SRC).not.toMatch(/\bgenerate_lead\b/)
    expect(DEMO_MODAL_SRC).not.toMatch(/\bdemo_submit_failed\b/)

    const occurrences = DEMO_MODAL_SRC.match(/trackedHubSpotSubmit\(/g) ?? []
    expect(occurrences.length).toBe(1)

    const line = DEMO_MODAL_SRC.split('\n').find((l) => l.includes('trackedHubSpotSubmit('))
    expect(line).toBeDefined()
    expect(line).toContain('submitDemoLead')
  })

  // Closes a gap the row above leaves open: it never checked that trackedHubSpotSubmit
  // is the ONLY analytics binding DemoModal.tsx pulls in. Exporting a private sender
  // and calling it directly at the shared success/catch transition (:186/:190) would
  // still satisfy every assertion above — the import regex's [^}]* tolerates extra
  // names alongside trackedHubSpotSubmit, and neither outcome event's literal string
  // is written in this file (only in analytics.ts). That mutation makes the honeypot
  // and closed-gate branches count as conversions, and it survives unless this row
  // exists.
  it('DemoModal imports exactly one binding from analytics.ts, and never via a namespace import', () => {
    const braceImports = Array.from(DEMO_MODAL_SRC.matchAll(/import\s*\{([^}]*)\}\s*from\s*['"]\.\.\/analytics['"]/g))
    expect(braceImports.length, 'expected exactly one import statement from ../analytics').toBe(1)
    const names = braceImports[0][1]
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    expect(names).toEqual(['trackedHubSpotSubmit'])
    expect(DEMO_MODAL_SRC).not.toMatch(/import\s*\*\s*as\s+\w+\s*from\s*['"]\.\.\/analytics['"]/)
  })
})

describe('honeypot branch and runStub pinned (AC-6)', () => {
  it('the honeypot branch and runStub are unchanged', () => {
    expect(DEMO_MODAL_SRC.length).toBeGreaterThan(0)
    expect(DEMO_MODAL_SRC).toContain('if (trap) await runStub()')
    const delayCalls = DEMO_MODAL_SRC.match(/setTimeout\(resolve, 1300\)/g) ?? []
    expect(delayCalls.length).toBe(1)
  })
})

describe('DemoModal SSR graph purity (AC-8, gap)', () => {
  it('importing DemoModal in a node environment is inert', async () => {
    // Precondition mirrors the module-scope purity guard above.
    expect(globalThis.window).toBeUndefined()
    expect(globalThis.document).toBeUndefined()
    await expect(import('./components/DemoModal')).resolves.toBeDefined()
    expect(globalThis.window).toBeUndefined()
    expect(globalThis.document).toBeUndefined()
  })
})
