// RED specs (task-543, LAND-03-02, Test-first) — pin the measurement id resolver, the
// composed gate and two static source-shape guards before analytics.ts is implemented.
// vi.stubEnv idiom copies hubspot.test.ts:34-38. Node environment (package default):
// no window/document exist, which is itself asserted below (module-scope purity guard).
//
// Seeds the implicit node type-library for this project (no other landing file carries
// it, unlike frontend/app's approvals.test.ts) — without it node:* imports fail TS2591.
/// <reference types="node" />
import { afterEach, describe, expect, it, vi } from 'vitest'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { measurementId, shouldLoadTag, tagSrc, DEMO_CTA_SOURCES, isScrollable, scrollDepthPercent } from './analytics'

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
const DEMO_LEAD_FORM_PATH = join(HERE, 'components', 'DemoLeadForm.tsx')

const ID = 'G-E409H76XYY'

// Every non-test .ts/.tsx under src, not just DemoModal.tsx: the one call site must be
// provable wherever it moves to.
const SRC_ROOT = HERE
function listSourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...listSourceFiles(full))
    else if (/\.tsx?$/.test(entry.name) && !/\.test\.|\.d\.ts$/.test(entry.name)) out.push(full)
  }
  return out
}

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

  it('rounds to nearest, not up — kills a Math.ceil mutant', () => {
    // 412/4000*100 = 10.3: round -> 10, ceil -> 11, floor -> 10 (already
    // covered by the sub-pixel row above). Only round differs here.
    expect(scrollDepthPercent(412, 800, 4800)).toBe(10)
  })

  it('a negative document height is treated as non-scrollable', () => {
    expect(scrollDepthPercent(100, 800, -50)).toBe(100)
  })

  it('a viewport taller than the document reads as fully seen even with nonzero scroll', () => {
    expect(scrollDepthPercent(500, 900, 400)).toBe(100)
  })

  it('all three measurements at zero is a non-scrollable page, not division by zero', () => {
    expect(scrollDepthPercent(0, 0, 0)).toBe(100)
  })

  it('an astronomically large scroll position still clamps to 100', () => {
    expect(scrollDepthPercent(Number.MAX_SAFE_INTEGER, 800, 4800)).toBe(100)
  })
})

describe('isScrollable (AC-7)', () => {
  it('a page longer than the viewport is scrollable', () => {
    expect(isScrollable(800, 4800)).toBe(true)
    expect(isScrollable(900, 901)).toBe(true)
  })

  it('a document exactly the viewport height is not scrollable', () => {
    // The `>` vs `>=` boundary: scrollDepthPercent answers 100 here, which is honest
    // about what was seen and wrong about what was scrolled.
    expect(isScrollable(900, 900)).toBe(false)
    expect(isScrollable(0, 0)).toBe(false)
  })

  it('a document shorter than the viewport is not scrollable', () => {
    expect(isScrollable(900, 400)).toBe(false)
    expect(isScrollable(800, -50)).toBe(false)
  })

  it('a non-finite measurement is not scrollable', () => {
    // -Infinity viewport and Infinity document both make the subtraction alone say
    // "scrollable"; they die only to the finite checks.
    for (const bad of [NaN, Infinity, -Infinity]) {
      expect(isScrollable(bad, 4800), `viewport ${bad}`).toBe(false)
      expect(isScrollable(800, bad), `document ${bad}`).toBe(false)
    }
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

  it('guards the mount-time measurement with isScrollable and returns early', () => {
    // Control needle first (A-14): a misresolved/empty read would otherwise pass vacuously.
    expect(APP_SRC.length).toBeGreaterThan(0)
    expect(APP_SRC).toContain('onBookDemo')

    const analyticsImports = Array.from(APP_SRC.matchAll(/import\s*\{([^}]*)\}\s*from\s*['"]\.\/analytics['"]/g))
    expect(analyticsImports.length, 'expected exactly one import statement from ./analytics').toBe(1)
    expect(analyticsImports[0][1].split(',').map((s) => s.trim())).toContain('isScrollable')

    const guard = APP_SRC.search(/if\s*\(\s*!isScrollable\([^)]*\)\s*\)\s*return\b/)
    expect(guard, 'expected an early-return !isScrollable guard').toBeGreaterThan(-1)
    expect(guard, 'the guard must precede the report, not follow it').toBeLessThan(
      APP_SRC.indexOf('trackScrollDepth('),
    )

    // One height read feeds both the guard and the report: two reads can disagree.
    expect(Array.from(APP_SRC.matchAll(/document\.documentElement\.scrollHeight/g)).length).toBe(1)
  })

  it('measures document.documentElement.scrollHeight, not body.scrollHeight', () => {
    // Control needle first (A-14): a misresolved/empty read would otherwise pass vacuously.
    expect(APP_SRC.length).toBeGreaterThan(0)
    expect(APP_SRC).toContain('onBookDemo')

    // body.scrollHeight excludes body margins and under-reports (design doc,
    // "Height source"). landing-nav.spec.ts:274 uses body.scrollHeight for an
    // unrelated job — a scroll target the browser clamps anyway — so this row
    // pins the two are not accidentally harmonised.
    expect(APP_SRC).toContain('document.documentElement.scrollHeight')
    expect(APP_SRC).not.toContain('document.body.scrollHeight')
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
  // A directory scan, not a DemoModal.tsx-scoped count, so the single call site stays
  // provable wherever it moves to. Comment-blind — paired with A9a in
  // DemoLeadForm.adversarial.test.tsx, which re-asserts the same fact on stripped source.
  it('S1: exactly one file in the whole package calls trackedHubSpotSubmit(, and it is components/DemoLeadForm.tsx', () => {
    // analytics.ts declares trackedHubSpotSubmit — its own `function trackedHubSpotSubmit(`
    // line matches the substring too, so it is excluded as the declaring module, not a caller.
    const analyticsPath = join(HERE, 'analytics.ts')
    const files = listSourceFiles(SRC_ROOT).filter((f) => f !== analyticsPath)
    // Floor on the population: a walk that silently returned one directory would
    // otherwise read as "nothing else calls it".
    expect(files.length).toBeGreaterThanOrEqual(25)
    expect(files).toContain(join(HERE, 'components', 'DemoCta.tsx'))
    const matches = files.filter((f) => readFileSync(f, 'utf8').includes('trackedHubSpotSubmit('))
    expect(matches.length).toBe(1)
    expect(matches[0]).toBe(DEMO_LEAD_FORM_PATH)
  })

  // S2 (NEW-BEHAVIOUR): DemoLeadForm.tsx doesn't exist yet — guard existence first so a
  // missing file fails this assertion, not a thrown ENOENT.
  it('S2: the trackedHubSpotSubmit call site passes submitDemoLead and names no event literal', () => {
    const exists = existsSync(DEMO_LEAD_FORM_PATH)
    expect(exists, 'expected components/DemoLeadForm.tsx to exist').toBe(true)
    if (!exists) return

    const src = readFileSync(DEMO_LEAD_FORM_PATH, 'utf8')
    expect(src).toContain('submitDemoLead')
    const line = src.split('\n').find((l) => l.includes('trackedHubSpotSubmit('))
    expect(line, 'expected a trackedHubSpotSubmit( call site').toBeDefined()
    expect(line).toContain('submitDemoLead')
    expect(src).not.toMatch(/\bgenerate_lead\b/)
    expect(src).not.toMatch(/\bdemo_submit_failed\b/)
  })

  // S3 (NEW-BEHAVIOUR): re-points the old :340-349 DEMO_MODAL_SRC check at DemoLeadForm.tsx.
  // Closes the same gap the original comment named — trackedHubSpotSubmit must be the ONLY
  // analytics binding the new component pulls in, never via a namespace import.
  it('S3: DemoLeadForm.tsx imports exactly one binding from analytics.ts, and never via a namespace import', () => {
    const exists = existsSync(DEMO_LEAD_FORM_PATH)
    expect(exists, 'expected components/DemoLeadForm.tsx to exist').toBe(true)
    if (!exists) return

    const src = readFileSync(DEMO_LEAD_FORM_PATH, 'utf8')
    const braceImports = Array.from(src.matchAll(/import\s*\{([^}]*)\}\s*from\s*['"]\.\.\/analytics['"]/g))
    expect(braceImports.length, 'expected exactly one import statement from ../analytics').toBe(1)
    const names = braceImports[0][1]
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    expect(names).toEqual(['trackedHubSpotSubmit'])
    expect(src).not.toMatch(/import\s*\*\s*as\s+\w+\s*from\s*['"]\.\.\/analytics['"]/)
  })

  // S4 (NEW-BEHAVIOUR): today DemoModal.tsx still imports trackedHubSpotSubmit directly
  // (:23) — fails honestly until the extraction moves it to DemoLeadForm.tsx.
  it('S4: DemoModal.tsx and DemoCta.tsx import nothing from ../analytics', () => {
    expect(DEMO_MODAL_SRC.length).toBeGreaterThan(0)
    expect(DEMO_MODAL_SRC).not.toMatch(/from\s*['"]\.\.\/analytics['"]/)

    const demoCtaSrc = readFileSync(join(HERE, 'components', 'DemoCta.tsx'), 'utf8')
    expect(demoCtaSrc.length).toBeGreaterThan(0)
    expect(demoCtaSrc).not.toMatch(/from\s*['"]\.\.\/analytics['"]/)
  })
})

describe('honeypot branch and runStub pinned (AC-6)', () => {
  // S5 (NEW-BEHAVIOUR): today's single setTimeout(resolve, 1300) still lives in
  // DemoModal.tsx — fails on the "zero times in DemoModal.tsx" assertion until it moves.
  it('S5: the honeypot branch and the single 1300ms stub live in DemoLeadForm.tsx, and DemoModal.tsx has neither', () => {
    expect(DEMO_MODAL_SRC.length).toBeGreaterThan(0)
    const modalDelayCalls = DEMO_MODAL_SRC.match(/setTimeout\(resolve, 1300\)/g) ?? []
    expect(modalDelayCalls.length).toBe(0)

    const exists = existsSync(DEMO_LEAD_FORM_PATH)
    expect(exists, 'expected components/DemoLeadForm.tsx to exist').toBe(true)
    if (!exists) return
    const formSrc = readFileSync(DEMO_LEAD_FORM_PATH, 'utf8')
    expect(formSrc).toContain('if (trap) await runStub()')
    const formDelayCalls = formSrc.match(/setTimeout\(resolve, 1300\)/g) ?? []
    expect(formDelayCalls.length).toBe(1)
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

  // S13 (NEW-BEHAVIOUR): DemoLeadForm.tsx doesn't exist yet, so the dynamic import
  // rejects and `.resolves` fails honestly — no static import, no collection error.
  it('S13: importing DemoLeadForm in a node environment is inert', async () => {
    expect(globalThis.window).toBeUndefined()
    expect(globalThis.document).toBeUndefined()
    await expect(import('./components/DemoLeadForm')).resolves.toBeDefined()
    expect(globalThis.window).toBeUndefined()
    expect(globalThis.document).toBeUndefined()
  })
})
