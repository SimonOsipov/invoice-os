// Two sources, read as TEXT: the shared seed helper (e2e/smoke/landingConsent.ts) and its
// call site (e2e/smoke/landing-demo.spec.ts). LAND-05-01's AC-4 and AC-5 have no runnable
// oracle — the seeding they describe only changes behaviour when the suite is pointed at
// www.ascomply.com, because isProductionHost closes the gate on every preview host. So
// deleting the seed is green in CI and red only in production. These read the source
// instead, the way rule-set.test.ts and workspaceCoverage.test.ts do for facts CI cannot
// otherwise reach.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = dirname(dirname(fileURLToPath(import.meta.url)))
const SPEC = join(REPO, 'e2e/smoke/landing-demo.spec.ts')
const HELPER = join(REPO, 'e2e/smoke/landingConsent.ts')
const NAV = join(REPO, 'e2e/smoke/landing-nav.spec.ts')
const CONSENT_TS = join(REPO, 'frontend/landing/src/consent.ts')

/** '' for a missing file, so an absent helper fails the floor below as an assertion. */
function readOrEmpty(path: string): string {
  try {
    return readFileSync(path, 'utf8')
  } catch {
    return ''
  }
}

const spec = readFileSync(SPEC, 'utf8')
const specLines = spec.split('\n')
const helper = readOrEmpty(HELPER)
const nav = readFileSync(NAV, 'utf8')
const consentSrc = readFileSync(CONSENT_TS, 'utf8')

const SEED_FN = 'seedConsent'
const SEED_GRANTED = `${SEED_FN}(page, true)`
const SEED_DENIED = `${SEED_FN}(page, false)`
const EXPECT_TAG_LINE = "const EXPECT_TAG = LANDING_HOST === 'www.ascomply.com'"

/** Body of a top-level `async function <name>(...)`, to its closing brace at column 0. */
function functionBody(src: string, name: string): string {
  const start = src.indexOf(`async function ${name}(`)
  expect(start, `function ${name} not found`).toBeGreaterThan(-1)
  const end = src.indexOf('\n}\n', start)
  expect(end, `function ${name} has no column-0 close`).toBeGreaterThan(start)
  return src.slice(start, end)
}

/**
 * The JSDoc block immediately above `async function <name>(`.
 *
 * Both indexOf results are guarded: on a miss, slicing from -1 returns an unrelated part
 * of the file and the assertions below pass against text they were never written for.
 */
function docAbove(src: string, name: string): string {
  const at = src.indexOf(`async function ${name}(`)
  expect(at, `function ${name} not found, so there is no doc comment to read above it`).toBeGreaterThan(-1)
  const doc = src.slice(Math.max(0, at - 700), at)
  const opened = doc.lastIndexOf('/**')
  expect(opened, `no JSDoc block in the 700 characters above ${name}`).toBeGreaterThan(-1)
  return doc.slice(opened)
}

describe('control: the source scanners find what these assertions claim is absent', () => {
  // A synthetic helper carrying every needle, including the two that must NOT appear in
  // the real one. If the instrument cannot find them here, the absence assertions below
  // are decoration.
  const PLANTED = [
    '/**',
    ' * EXPECT_TAG, the biconditional, a denied default, and the addInitScript reason.',
    ' */',
    'export async function seedConsent(page: Page, analytics: boolean): Promise<void> {',
    "  await page.evaluate(() => window.localStorage.setItem('asc_consent', JSON.stringify({ analytics, v: 1 })))",
    '}',
    '',
  ].join('\n')

  it('functionBody returns the planted body and sees the evaluate the helper must not use', () => {
    const body = functionBody(PLANTED, SEED_FN)
    expect(body).toContain('page.evaluate(')
    expect(body).toContain("'asc_consent'")
    expect(body).toMatch(/v:\s*1\b/)
  })

  it('docAbove returns the planted JSDoc, not the code beneath it', () => {
    const block = docAbove(PLANTED, SEED_FN)
    expect(block).toContain('EXPECT_TAG')
    expect(block).not.toContain('export async function')
  })

  it('docAbove refuses a missing function instead of slicing an unrelated block', () => {
    expect(() => docAbove(PLANTED, 'seedNothing')).toThrow()
  })

  it('the denied-call needle is a shape a body can actually contain', () => {
    expect(`  await ${SEED_DENIED}\n`).toContain(SEED_DENIED)
    expect(SEED_DENIED).not.toBe(SEED_GRANTED)
  })
})

describe('control: both sources were read and still have the shape these assertions walk', () => {
  it('the spec is non-trivial and still defines openLanding', () => {
    expect(spec.length).toBeGreaterThan(20000)
    expect(specLines.length).toBeGreaterThan(500)
    expect(spec).toContain('async function openLanding(')
  })

  it('the helper file exists, is non-trivial, and declares seedConsent as a function statement', () => {
    // functionBody parses `async function <name>(` and stops at a column-0 close, so an
    // arrow const would make every helper assertion below unreachable.
    expect(helper.length, `${HELPER} is missing or empty`).toBeGreaterThan(300)
    expect(helper.split('\n').length).toBeGreaterThan(10)
    expect(helper).toContain(`export async function ${SEED_FN}(`)
    expect(helper, 'the helper has no column-0 closing brace for functionBody to stop at').toMatch(/\n}\n/)
  })

  it('every navigation in the spec goes through openLanding', () => {
    // The floor that makes the ordering assertion below cover the whole suite:
    // a second page.goto would be an unseeded entry point.
    expect(spec.match(/page\.goto\(/g) ?? []).toHaveLength(1)
    expect(spec.match(/await openLanding\(page\)/g) ?? []).toHaveLength(7)
    expect(spec.match(/expectClosedGateStayedSilent\(sinks\)/g) ?? []).toHaveLength(7)
  })
})

describe('AC-4: openLanding seeds a granted record before navigating', () => {
  const body = functionBody(spec, 'openLanding')

  it('the seed call precedes page.goto', () => {
    const seedAt = body.indexOf(SEED_GRANTED)
    const gotoAt = body.indexOf('page.goto(')
    expect(seedAt, `openLanding does not call ${SEED_GRANTED}`).toBeGreaterThan(-1)
    expect(gotoAt, 'openLanding does not navigate').toBeGreaterThan(-1)
    expect(seedAt, 'the seed lands after navigation, so bootAnalytics has already run').toBeLessThan(gotoAt)
  })

  it('the record seeded is GRANTED — a denied seed would falsify the biconditional', () => {
    // The helper takes the answer as a parameter, so granted-ness is now a fact about the
    // call site. This is the only place it can still be read.
    expect(body).toContain(SEED_GRANTED)
    expect(body, 'a denied seed keeps the tag dark on production').not.toContain(SEED_DENIED)
  })

  it("EXPECT_TAG's definition is unchanged", () => {
    // A substring pin, not an absolute line index: adding the helper import shifts every
    // line below it, and the fact asserted is the definition, not where it sits.
    expect(specLines.filter((line) => line.includes('const EXPECT_TAG ='))).toHaveLength(1)
    expect(spec).toContain(EXPECT_TAG_LINE)
  })

  it('the biconditional was not weakened to a one-way assertion', () => {
    expect(spec).toContain('.toBe(EXPECT_TAG)')
  })
})

describe('AC-4: the shared helper writes the record before the first navigation', () => {
  it('seeds through addInitScript, not a pre-goto evaluate', () => {
    const seed = functionBody(helper, SEED_FN)
    expect(seed).toContain('page.addInitScript(')
    // A pre-navigation page.evaluate touching localStorage throws: there is no origin yet.
    expect(seed, 'a pre-goto page.evaluate has no origin to write to').not.toContain('page.evaluate(')
  })

  it('passes the answer into the init script instead of closing over it', () => {
    // addInitScript serialises the function and runs it in the page: a closed-over
    // `analytics` is undefined there, and the seed silently writes the wrong record.
    const seed = functionBody(helper, SEED_FN)
    expect(seed, 'analytics is not forwarded as an addInitScript argument').toMatch(/,\s*analytics\s*\)/)
  })

  it('takes the answer as a boolean parameter rather than hardcoding one', () => {
    const seed = functionBody(helper, SEED_FN)
    expect(helper).toMatch(new RegExp(`export async function ${SEED_FN}\\(\\s*page: Page,\\s*analytics: boolean`))
    expect(seed, 'a hardcoded answer cannot seed both directions').not.toMatch(/analytics:\s*(true|false)\b/)
  })

  it("the retyped key and version still match consent.ts's contract", () => {
    // The helper retypes rather than imports, on purpose: e2e pins what the deployed build
    // serves. Retyped constants drift silently; this is the only thing that notices.
    const seed = functionBody(helper, SEED_FN)
    const key = consentSrc.match(/export const CONSENT_STORAGE_KEY = '([^']+)'/)?.[1]
    const version = consentSrc.match(/export const CONSENT_VERSION = (\d+)/)?.[1]
    expect(key, 'CONSENT_STORAGE_KEY not found in consent.ts').toBe('asc_consent')
    expect(version, 'CONSENT_VERSION not found in consent.ts').toBe('1')
    expect(seed, `the helper seeds a key other than ${key}`).toContain(`'${key}'`)
    expect(seed, `the helper seeds a version other than ${version}`).toMatch(new RegExp(`v:\\s*${version}\\b`))
  })
})

describe('AC-5: the helper documents why the seed exists', () => {
  it('a doc comment immediately above seedConsent names the gate it restores', () => {
    const block = docAbove(helper, SEED_FN)
    expect(block).toContain('EXPECT_TAG')
    expect(block.toLowerCase()).toContain('biconditional')
    expect(block.toLowerCase(), 'the doc does not say WHY the seed is needed').toContain('denied')
    expect(block, 'the doc does not record the addInitScript reason').toContain('addInitScript')
  })
})

describe('landing-nav.spec.ts runs against a MOUNTED notice and must never seed a choice', () => {
  it('is unseeded, so its eight assertions still prove the notice displaces nothing', () => {
    // Seeding here makes LAND-05's "nothing shifts by a pixel" AC vacuous. Recorded as a
    // test rather than a comment so a later agent cannot "fix the inconsistency" quietly.
    expect(nav.split('\n').length).toBeGreaterThan(200) // floor: the file was really read
    expect(nav).toContain('async function openLanding(')
    expect(nav.match(/test\(/g) ?? []).toHaveLength(8)
    expect(nav.match(/page\.goto\(/g) ?? []).toHaveLength(1)
    expect(nav, 'landing-nav must arrive with an empty store').not.toContain(SEED_FN)
    expect(nav, 'landing-nav must not import the seed helper').not.toContain('landingConsent')
  })

  it('the same scan does find the seed in the spec that is meant to have it', () => {
    // The control needle for the absence claim above: without it, a renamed helper turns
    // that assertion green everywhere and it stops meaning anything.
    expect(spec, `${SEED_FN} is not in landing-demo.spec.ts, so the absence scan above proves nothing`).toContain(SEED_FN)
  })
})
